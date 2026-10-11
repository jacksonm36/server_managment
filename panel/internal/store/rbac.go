package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrInUse is returned when deleting something other records still use.
var ErrInUse = errors.New("in use")

func isUnique(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

func isFK(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23503"
}

// ---- roles ----

type Role struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Permissions []string  `json:"permissions"`
	Builtin     bool      `json:"builtin"`
	Users       int       `json:"users"`  // users with this as main role
	Grants      int       `json:"grants"` // extra grants using it
	CreatedAt   time.Time `json:"created_at"`
}

func (s *Store) ListRoles(ctx context.Context) ([]*Role, error) {
	rows, err := s.db.Query(ctx, `
		SELECT r.id, r.name, r.description, r.permissions, r.builtin, r.created_at,
		       (SELECT count(*) FROM admins a WHERE a.role = r.name),
		       (SELECT count(*) FROM role_grants g WHERE g.role_id = r.id)
		FROM roles r ORDER BY r.builtin DESC, r.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Role{}
	for rows.Next() {
		r := &Role{}
		if err := rows.Scan(&r.ID, &r.Name, &r.Description, &r.Permissions, &r.Builtin, &r.CreatedAt, &r.Users, &r.Grants); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetRole(ctx context.Context, id int64) (*Role, error) {
	r := &Role{}
	err := s.db.QueryRow(ctx, `SELECT id, name, description, permissions, builtin, created_at FROM roles WHERE id = $1`, id).
		Scan(&r.ID, &r.Name, &r.Description, &r.Permissions, &r.Builtin, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

func (s *Store) RoleExists(ctx context.Context, name string) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM roles WHERE name = $1)`, name).Scan(&ok)
	return ok, err
}

// SaveRole creates (ID 0) or updates a custom role. Built-in roles cannot
// be changed.
func (s *Store) SaveRole(ctx context.Context, r *Role) error {
	var err error
	if r.ID == 0 {
		err = s.db.QueryRow(ctx, `INSERT INTO roles (name, description, permissions) VALUES ($1, $2, $3) RETURNING id`,
			r.Name, r.Description, r.Permissions).Scan(&r.ID)
	} else {
		var tag pgconn.CommandTag
		tag, err = s.db.Exec(ctx, `UPDATE roles SET name = $2, description = $3, permissions = $4 WHERE id = $1 AND NOT builtin`,
			r.ID, r.Name, r.Description, r.Permissions)
		if err == nil && tag.RowsAffected() == 0 {
			return ErrNotFound
		}
	}
	if isUnique(err) {
		return ErrExists
	}
	return err
}

// DeleteRole removes a custom role and its grants. It refuses while users
// have it as their main role.
func (s *Store) DeleteRole(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM roles WHERE id = $1 AND NOT builtin`, id)
	if isFK(err) {
		return ErrInUse
	}
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ---- user groups ----

type UserGroup struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Members     []int64   `json:"members"`
	CreatedAt   time.Time `json:"created_at"`
}

func (s *Store) ListUserGroups(ctx context.Context) ([]*UserGroup, error) {
	rows, err := s.db.Query(ctx, `
		SELECT g.id, g.name, g.description, g.created_at,
		       COALESCE(array_agg(m.admin_id ORDER BY m.admin_id) FILTER (WHERE m.admin_id IS NOT NULL), '{}')
		FROM user_groups g LEFT JOIN user_group_members m ON m.group_id = g.id
		GROUP BY g.id ORDER BY g.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*UserGroup{}
	for rows.Next() {
		g := &UserGroup{}
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.CreatedAt, &g.Members); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// SaveUserGroup creates (ID 0) or updates a group and replaces its members.
func (s *Store) SaveUserGroup(ctx context.Context, g *UserGroup) error {
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if g.ID == 0 {
			if err := tx.QueryRow(ctx, `INSERT INTO user_groups (name, description) VALUES ($1, $2) RETURNING id`, g.Name, g.Description).Scan(&g.ID); err != nil {
				return err
			}
		} else {
			tag, err := tx.Exec(ctx, `UPDATE user_groups SET name = $2, description = $3 WHERE id = $1`, g.ID, g.Name, g.Description)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return ErrNotFound
			}
			if _, err := tx.Exec(ctx, `DELETE FROM user_group_members WHERE group_id = $1`, g.ID); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO user_group_members (group_id, admin_id) SELECT $1, unnest($2::bigint[]) ON CONFLICT DO NOTHING`, g.ID, g.Members)
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

func (s *Store) DeleteUserGroup(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM user_groups WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// SetUserGroups replaces the groups one user belongs to.
func (s *Store) SetUserGroups(ctx context.Context, adminID int64, groups []int64) error {
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM user_group_members WHERE admin_id = $1`, adminID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO user_group_members (group_id, admin_id) SELECT unnest($2::bigint[]), $1 ON CONFLICT DO NOTHING`, adminID, groups)
		return err
	})
	if isFK(err) {
		return ErrNotFound
	}
	return err
}

// ---- server groups ----

type ServerGroup struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Color       string    `json:"color"`
	ServerIDs   []string  `json:"server_ids"`
	CreatedAt   time.Time `json:"created_at"`
}

func (s *Store) ListServerGroups(ctx context.Context) ([]*ServerGroup, error) {
	rows, err := s.db.Query(ctx, `
		SELECT g.id, g.name, g.description, g.color, g.created_at,
		       COALESCE(array_agg(m.server_id::text ORDER BY m.server_id) FILTER (WHERE m.server_id IS NOT NULL), '{}')
		FROM server_groups g LEFT JOIN server_group_members m ON m.group_id = g.id
		GROUP BY g.id ORDER BY g.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ServerGroup{}
	for rows.Next() {
		g := &ServerGroup{}
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.Color, &g.CreatedAt, &g.ServerIDs); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) GetServerGroup(ctx context.Context, id int64) (*ServerGroup, error) {
	list, err := s.ListServerGroups(ctx)
	if err != nil {
		return nil, err
	}
	for _, g := range list {
		if g.ID == id {
			return g, nil
		}
	}
	return nil, ErrNotFound
}

// SaveServerGroup creates (ID 0) or updates a group and replaces its servers.
func (s *Store) SaveServerGroup(ctx context.Context, g *ServerGroup) error {
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if g.ID == 0 {
			if err := tx.QueryRow(ctx, `INSERT INTO server_groups (name, description, color) VALUES ($1, $2, $3) RETURNING id`,
				g.Name, g.Description, g.Color).Scan(&g.ID); err != nil {
				return err
			}
		} else {
			tag, err := tx.Exec(ctx, `UPDATE server_groups SET name = $2, description = $3, color = $4 WHERE id = $1`, g.ID, g.Name, g.Description, g.Color)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return ErrNotFound
			}
			if _, err := tx.Exec(ctx, `DELETE FROM server_group_members WHERE group_id = $1`, g.ID); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO server_group_members (group_id, server_id)
			SELECT $1, s.id FROM servers s WHERE s.id::text = ANY($2) ON CONFLICT DO NOTHING`, g.ID, g.ServerIDs)
		return err
	})
	if isUnique(err) {
		return ErrExists
	}
	return err
}

func (s *Store) DeleteServerGroup(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM server_groups WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// SetServerGroups replaces the groups one server belongs to.
func (s *Store) SetServerGroups(ctx context.Context, serverID string, groups []int64) error {
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM server_group_members WHERE server_id::text = $1`, serverID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO server_group_members (group_id, server_id)
			SELECT unnest($2::bigint[]), s.id FROM servers s WHERE s.id::text = $1 ON CONFLICT DO NOTHING`, serverID, groups)
		return err
	})
	if isFK(err) {
		return ErrNotFound
	}
	return err
}

// ServerGroupsByServer maps every grouped server to its group IDs.
func (s *Store) ServerGroupsByServer(ctx context.Context) (map[string][]int64, error) {
	rows, err := s.db.Query(ctx, `SELECT server_id::text, group_id FROM server_group_members ORDER BY group_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]int64{}
	for rows.Next() {
		var sid string
		var gid int64
		if err := rows.Scan(&sid, &gid); err != nil {
			return nil, err
		}
		out[sid] = append(out[sid], gid)
	}
	return out, rows.Err()
}

// ---- grants ----

type RoleGrant struct {
	ID              int64     `json:"id"`
	AdminID         *int64    `json:"user_id"`
	UserGroupID     *int64    `json:"user_group_id"`
	RoleID          int64     `json:"role_id"`
	RoleName        string    `json:"role"`
	ServerGroupID   *int64    `json:"server_group_id"`
	ServerGroupName string    `json:"server_group"`
	CreatedAt       time.Time `json:"created_at"`
}

func (s *Store) ListGrants(ctx context.Context) ([]*RoleGrant, error) {
	rows, err := s.db.Query(ctx, `
		SELECT g.id, g.admin_id, g.user_group_id, g.role_id, r.name, g.server_group_id, COALESCE(sg.name, ''), g.created_at
		FROM role_grants g JOIN roles r ON r.id = g.role_id LEFT JOIN server_groups sg ON sg.id = g.server_group_id
		ORDER BY g.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*RoleGrant{}
	for rows.Next() {
		g := &RoleGrant{}
		if err := rows.Scan(&g.ID, &g.AdminID, &g.UserGroupID, &g.RoleID, &g.RoleName, &g.ServerGroupID, &g.ServerGroupName, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) CreateGrant(ctx context.Context, g *RoleGrant) error {
	err := s.db.QueryRow(ctx, `
		INSERT INTO role_grants (admin_id, user_group_id, role_id, server_group_id) VALUES ($1, $2, $3, $4) RETURNING id`,
		g.AdminID, g.UserGroupID, g.RoleID, g.ServerGroupID).Scan(&g.ID)
	switch {
	case isUnique(err):
		return ErrExists
	case isFK(err):
		return ErrNotFound
	}
	return err
}

func (s *Store) DeleteGrant(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM role_grants WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// Grant is one source of permissions for a user: their main role, or a
// role granted to them or one of their groups.
type Grant struct {
	RoleName      string
	Builtin       bool
	Permissions   []string
	ServerGroupID *int64 // nil: all servers and panel-wide
}

func (s *Store) Grants(ctx context.Context, adminID int64, mainRole string) ([]Grant, error) {
	rows, err := s.db.Query(ctx, `
		SELECT r.name, r.builtin, r.permissions, NULL::bigint FROM roles r WHERE r.name = $2
		UNION ALL
		SELECT r.name, r.builtin, r.permissions, g.server_group_id
		FROM role_grants g JOIN roles r ON r.id = g.role_id
		WHERE g.admin_id = $1 OR g.user_group_id IN (SELECT group_id FROM user_group_members WHERE admin_id = $1)`,
		adminID, mainRole)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Grant{}
	for rows.Next() {
		var g Grant
		if err := rows.Scan(&g.RoleName, &g.Builtin, &g.Permissions, &g.ServerGroupID); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
