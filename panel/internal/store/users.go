package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var ErrExists = errors.New("already exists")

type User struct {
	ID          int64      `json:"id"`
	Username    string     `json:"username"`
	Role        string     `json:"role"`
	Disabled    bool       `json:"disabled"`
	TOTPEnabled bool       `json:"totp_enabled"`
	LastLogin   *time.Time `json:"last_login"`
	CreatedAt   time.Time  `json:"created_at"`
}

func (s *Store) ListUsers(ctx context.Context) ([]*User, error) {
	rows, err := s.db.Query(ctx, `SELECT id, username, role, disabled, totp_enabled, last_login, created_at FROM admins ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*User{}
	for rows.Next() {
		u := &User{}
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &u.Disabled, &u.TOTPEnabled, &u.LastLogin, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) CreateUser(ctx context.Context, username, hash, role string) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx, `INSERT INTO admins (username, password_hash, role) VALUES ($1, $2, $3) RETURNING id`, username, hash, role).Scan(&id)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return 0, ErrExists
	}
	return id, err
}

// UpdateUser changes role/disabled. It refuses to leave the panel without
// an enabled admin; disabling a user also ends their sessions.
func (s *Store) UpdateUser(ctx context.Context, id int64, role string, disabled bool) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE admins SET role = $2, disabled = $3 WHERE id = $1`, id, role, disabled)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if err := ensureAdmin(ctx, tx); err != nil {
			return err
		}
		if disabled {
			_, err = tx.Exec(ctx, `DELETE FROM sessions WHERE admin_id = $1`, id)
		}
		return err
	})
}

var ErrLastAdmin = errors.New("at least one enabled admin must remain")

func ensureAdmin(ctx context.Context, tx pgx.Tx) error {
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM admins WHERE role = 'admin' AND NOT disabled`).Scan(&n); err != nil {
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
// optionally, turns off their 2FA (for a lost authenticator).
func (s *Store) ResetPassword(ctx context.Context, id int64, hash string, reset2FA bool) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE admins SET password_hash = $2 WHERE id = $1`, id, hash)
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

func (s *Store) TouchLogin(ctx context.Context, id int64) {
	_, _ = s.db.Exec(ctx, `UPDATE admins SET last_login = now() WHERE id = $1`, id)
}
