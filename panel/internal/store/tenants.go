package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Tenant struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	Slug          string    `json:"slug"`
	Description   string    `json:"description"`
	ServerGroupID *int64    `json:"server_group_id"`
	MaxGuests     int       `json:"max_guests"`
	MaxCores      int       `json:"max_cores"`
	MaxMemoryMB   int64     `json:"max_memory_mb"`
	MaxDiskGB     int64     `json:"max_disk_gb"`
	Users         int       `json:"users"`
	CreatedAt     time.Time `json:"created_at"`
}

const tenantCols = `t.id, t.name, t.slug, t.description, t.server_group_id, t.max_guests, t.max_cores, t.max_memory_mb, t.max_disk_gb,
	(SELECT count(*) FROM admins a WHERE a.tenant_id = t.id), t.created_at`

func scanTenant(row pgx.Row) (*Tenant, error) {
	t := &Tenant{}
	err := row.Scan(&t.ID, &t.Name, &t.Slug, &t.Description, &t.ServerGroupID, &t.MaxGuests, &t.MaxCores, &t.MaxMemoryMB, &t.MaxDiskGB, &t.Users, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

func (s *Store) ListTenants(ctx context.Context) ([]*Tenant, error) {
	rows, err := s.db.Query(ctx, `SELECT `+tenantCols+` FROM tenants t ORDER BY t.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Tenant{}
	for rows.Next() {
		t, err := scanTenant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) GetTenant(ctx context.Context, id int64) (*Tenant, error) {
	return scanTenant(s.db.QueryRow(ctx, `SELECT `+tenantCols+` FROM tenants t WHERE t.id = $1`, id))
}

// SaveTenant creates (ID 0) or updates a tenant. The slug never changes
// after creation: it names the tenant's Proxmox users.
func (s *Store) SaveTenant(ctx context.Context, t *Tenant) error {
	var err error
	if t.ID == 0 {
		err = s.db.QueryRow(ctx, `INSERT INTO tenants (name, slug, description, server_group_id, max_guests, max_cores, max_memory_mb, max_disk_gb)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id, created_at`,
			t.Name, t.Slug, t.Description, t.ServerGroupID, t.MaxGuests, t.MaxCores, t.MaxMemoryMB, t.MaxDiskGB).Scan(&t.ID, &t.CreatedAt)
	} else {
		var tag pgconn.CommandTag
		tag, err = s.db.Exec(ctx, `UPDATE tenants SET name = $2, description = $3, server_group_id = $4, max_guests = $5, max_cores = $6,
			max_memory_mb = $7, max_disk_gb = $8 WHERE id = $1`,
			t.ID, t.Name, t.Description, t.ServerGroupID, t.MaxGuests, t.MaxCores, t.MaxMemoryMB, t.MaxDiskGB)
		if err == nil && tag.RowsAffected() == 0 {
			return ErrNotFound
		}
	}
	switch {
	case isUnique(err):
		return ErrExists
	case isFK(err):
		return ErrNotFound
	}
	return err
}

var ErrTenantInUse = errors.New("the tenant still has users; move or delete them first")

func (s *Store) DeleteTenant(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, id)
	if isFK(err) {
		return ErrTenantInUse
	}
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// TenantCluster is a tenant's slice of one Proxmox cluster.
type TenantCluster struct {
	TenantID  int64      `json:"tenant_id"`
	ClusterID string     `json:"cluster_id"`
	Pool      string     `json:"pool"`
	Storages  []string   `json:"storages"`
	Bridges   []string   `json:"bridges"`
	VLANs     []int      `json:"vlans"` // VLAN tags allowed on the bridges; empty = untagged only
	Templates []int      `json:"templates"`
	PVEUser   string     `json:"pve_user"`
	TokenID   string     `json:"token_id"`
	Sealed    string     `json:"-"`
	SetUpAt   *time.Time `json:"set_up_at"`
}

// Isolated reports whether the tenant has its own Proxmox token; without
// one, tenant users get no access to the cluster at all.
func (tc *TenantCluster) Isolated() bool { return tc.TokenID != "" && tc.Sealed != "" }

const tcCols = `tenant_id, cluster_id::text, pool, storages, bridges, vlans, templates, pve_user, token_id, token_secret, set_up_at`

func scanTC(row pgx.Row) (*TenantCluster, error) {
	tc := &TenantCluster{}
	err := row.Scan(&tc.TenantID, &tc.ClusterID, &tc.Pool, &tc.Storages, &tc.Bridges, &tc.VLANs, &tc.Templates, &tc.PVEUser, &tc.TokenID, &tc.Sealed, &tc.SetUpAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return tc, err
}

func (s *Store) GetTenantCluster(ctx context.Context, tenantID int64, clusterID string) (*TenantCluster, error) {
	return scanTC(s.db.QueryRow(ctx, `SELECT `+tcCols+` FROM tenant_clusters WHERE tenant_id = $1 AND cluster_id::text = $2`, tenantID, clusterID))
}

// ListTenantClusters lists a tenant's clusters (tenantID > 0) or all of
// them (tenantID 0).
func (s *Store) ListTenantClusters(ctx context.Context, tenantID int64) ([]*TenantCluster, error) {
	rows, err := s.db.Query(ctx, `SELECT `+tcCols+` FROM tenant_clusters WHERE $1 = 0 OR tenant_id = $1 ORDER BY tenant_id, cluster_id`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*TenantCluster{}
	for rows.Next() {
		tc, err := scanTC(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, tc)
	}
	return out, rows.Err()
}

func (s *Store) SaveTenantCluster(ctx context.Context, tc *TenantCluster) error {
	_, err := s.db.Exec(ctx, `INSERT INTO tenant_clusters (tenant_id, cluster_id, pool, storages, bridges, templates, pve_user, token_id, token_secret, set_up_at, vlans)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET pool = $3, storages = $4, bridges = $5, templates = $6,
			pve_user = $7, token_id = $8, token_secret = $9, set_up_at = $10, vlans = $11`,
		tc.TenantID, tc.ClusterID, tc.Pool, orEmptyStr(tc.Storages), orEmptyStr(tc.Bridges), orEmptyInt(tc.Templates), tc.PVEUser, tc.TokenID, tc.Sealed, tc.SetUpAt,
		orEmptyInt(tc.VLANs))
	switch {
	case isUnique(err):
		return ErrExists
	case isFK(err):
		return ErrNotFound
	}
	return err
}

func (s *Store) DeleteTenantCluster(ctx context.Context, tenantID int64, clusterID string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM tenant_clusters WHERE tenant_id = $1 AND cluster_id::text = $2`, tenantID, clusterID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// AddServerToGroup puts a server into a server group (a tenant's servers).
func (s *Store) AddServerToGroup(ctx context.Context, groupID int64, serverID string) error {
	_, err := s.db.Exec(ctx, `INSERT INTO server_group_members (group_id, server_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, groupID, serverID)
	return err
}

func orEmptyStr(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func orEmptyInt(s []int) []int {
	if s == nil {
		return []int{}
	}
	return s
}
