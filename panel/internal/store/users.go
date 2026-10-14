package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrExists = errors.New("already exists")

type User struct {
	ID                 int64      `json:"id"`
	Username           string     `json:"username"`
	DisplayName        string     `json:"display_name"`
	Email              string     `json:"email"`
	Role               string     `json:"role"`
	Disabled           bool       `json:"disabled"`
	MustChangePassword bool       `json:"must_change_password"`
	TOTPEnabled        bool       `json:"totp_enabled"`
	Groups             []int64    `json:"groups"`
	TenantID           *int64     `json:"tenant_id"`
	Sessions           int        `json:"sessions"`
	LastLogin          *time.Time `json:"last_login"`
	CreatedAt          time.Time  `json:"created_at"`
}

func (s *Store) ListUsers(ctx context.Context) ([]*User, error) {
	rows, err := s.db.Query(ctx, `
		SELECT a.id, a.username, a.display_name, a.email, a.role, a.disabled, a.must_change_password, a.totp_enabled,
		       COALESCE((SELECT array_agg(m.group_id ORDER BY m.group_id) FROM user_group_members m WHERE m.admin_id = a.id), '{}'),
		       (SELECT count(*) FROM sessions x WHERE x.admin_id = a.id AND x.expires_at > now()),
		       a.last_login, a.created_at, a.tenant_id
		FROM admins a ORDER BY a.username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*User{}
	for rows.Next() {
		u := &User{}
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.Role, &u.Disabled, &u.MustChangePassword,
			&u.TOTPEnabled, &u.Groups, &u.Sessions, &u.LastLogin, &u.CreatedAt, &u.TenantID); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CreateUser adds a user. ErrNotFound means the role or a group does not exist.
func (s *Store) CreateUser(ctx context.Context, u *User, hash string) error {
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO admins (username, password_hash, role, display_name, email, must_change_password, tenant_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
			u.Username, hash, u.Role, u.DisplayName, u.Email, u.MustChangePassword, u.TenantID).Scan(&u.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO user_group_members (group_id, admin_id) SELECT unnest($2::bigint[]), $1 ON CONFLICT DO NOTHING`, u.ID, u.Groups)
		return err
	})
	switch {
	case isUnique(err):
		return ErrExists
	case isFK(err):
		return ErrNotFound
	}
	return err
}

// UpdateUser changes a user's profile, main role, disabled flag and groups.
// It refuses to leave the panel without an enabled admin; disabling a user
// also ends their sessions.
//
// keepTenant leaves the user's tenant as it is; otherwise u.TenantID is set.
func (s *Store) UpdateUser(ctx context.Context, u *User, keepTenant bool) error {
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if keepTenant {
			if err := tx.QueryRow(ctx, `SELECT tenant_id FROM admins WHERE id = $1`, u.ID).Scan(&u.TenantID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrNotFound
				}
				return err
			}
		}
		// Moving a user into or out of a tenant ends their sessions, so
		// no session keeps the access it had before.
		var moved bool
		if err := tx.QueryRow(ctx, `SELECT tenant_id IS DISTINCT FROM $2 FROM admins WHERE id = $1`, u.ID, u.TenantID).Scan(&moved); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE admins SET role = $2, disabled = $3, display_name = $4, email = $5, tenant_id = $6 WHERE id = $1`,
			u.ID, u.Role, u.Disabled, u.DisplayName, u.Email, u.TenantID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if err := ensureAdmin(ctx, tx); err != nil {
			return err
		}
		if u.Groups != nil {
			if _, err := tx.Exec(ctx, `DELETE FROM user_group_members WHERE admin_id = $1`, u.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO user_group_members (group_id, admin_id) SELECT unnest($2::bigint[]), $1 ON CONFLICT DO NOTHING`, u.ID, u.Groups); err != nil {
				return err
			}
		}
		if u.Disabled || moved {
			_, err = tx.Exec(ctx, `DELETE FROM sessions WHERE admin_id = $1`, u.ID)
		}
		return err
	})
	if isFK(err) {
		return ErrNotFound
	}
	return err
}

var ErrLastAdmin = errors.New("at least one enabled admin outside any tenant must remain")

func ensureAdmin(ctx context.Context, tx pgx.Tx) error {
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM admins WHERE role = 'admin' AND NOT disabled AND tenant_id IS NULL`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrLastAdmin
	}
	return nil
}

func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM admins WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return ensureAdmin(ctx, tx)
	})
}

// ResetPassword sets a new password, ends all of the user's sessions and,
// optionally, turns off their 2FA (for a lost authenticator) and makes them
// choose a new password at the next login.
func (s *Store) ResetPassword(ctx context.Context, id int64, hash string, reset2FA, mustChange bool) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE admins SET password_hash = $2, must_change_password = $3 WHERE id = $1`, id, hash, mustChange)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if reset2FA {
			if _, err := tx.Exec(ctx, `UPDATE admins SET totp_secret = NULL, totp_pending = NULL, totp_enabled = false, totp_last_step = 0 WHERE id = $1`, id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM admin_recovery_codes WHERE admin_id = $1`, id); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `DELETE FROM sessions WHERE admin_id = $1`, id)
		return err
	})
}

// EndSessions signs a user out everywhere.
func (s *Store) EndSessions(ctx context.Context, id int64) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE admin_id = $1`, id)
	return tag.RowsAffected(), err
}

func (s *Store) TouchLogin(ctx context.Context, id int64) {
	_, _ = s.db.Exec(ctx, `UPDATE admins SET last_login = now() WHERE id = $1`, id)
}
