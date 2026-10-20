package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type ProxmoxCluster struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	URL          string    `json:"url"`
	TokenID      string    `json:"token_id"`
	SealedSecret string    `json:"-"`
	Fingerprint  string    `json:"fingerprint"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

const pveCols = `id::text, name, url, token_id, token_secret, fingerprint, created_at, updated_at`

func scanPVE(row pgx.Row) (*ProxmoxCluster, error) {
	c := &ProxmoxCluster{}
	err := row.Scan(&c.ID, &c.Name, &c.URL, &c.TokenID, &c.SealedSecret, &c.Fingerprint, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

func (s *Store) ListProxmoxClusters(ctx context.Context) ([]*ProxmoxCluster, error) {
	rows, err := s.db.Query(ctx, `SELECT `+pveCols+` FROM proxmox_clusters ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ProxmoxCluster{}
	for rows.Next() {
		c, err := scanPVE(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetProxmoxCluster(ctx context.Context, id string) (*ProxmoxCluster, error) {
	return scanPVE(s.db.QueryRow(ctx, `SELECT `+pveCols+` FROM proxmox_clusters WHERE id::text = $1`, id))
}

func (s *Store) SaveProxmoxCluster(ctx context.Context, c *ProxmoxCluster) error {
	if c.ID == "" {
		c.ID = NewUUID()
		_, err := s.db.Exec(ctx, `INSERT INTO proxmox_clusters (id, name, url, token_id, token_secret, fingerprint) VALUES ($1, $2, $3, $4, $5, $6)`,
			c.ID, c.Name, c.URL, c.TokenID, c.SealedSecret, c.Fingerprint)
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE proxmox_clusters SET name = $2, url = $3, token_id = $4, token_secret = $5, fingerprint = $6, updated_at = now()
		WHERE id::text = $1`, c.ID, c.Name, c.URL, c.TokenID, c.SealedSecret, c.Fingerprint)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) DeleteProxmoxCluster(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM proxmox_clusters WHERE id::text = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ProxmoxLinks maps vmid -> servermgr server id for a cluster.
func (s *Store) ProxmoxLinks(ctx context.Context, clusterID string) (map[int]string, error) {
	rows, err := s.db.Query(ctx, `SELECT vmid, server_id::text FROM proxmox_links WHERE cluster_id::text = $1`, clusterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]string{}
	for rows.Next() {
		var vmid int
		var sid string
		if err := rows.Scan(&vmid, &sid); err != nil {
			return nil, err
		}
		out[vmid] = sid
	}
	return out, rows.Err()
}

func (s *Store) LinkProxmoxGuest(ctx context.Context, clusterID string, vmid int, serverID string) error {
	_, err := s.db.Exec(ctx, `INSERT INTO proxmox_links (cluster_id, vmid, server_id) VALUES ($1, $2, $3)
		ON CONFLICT (cluster_id, vmid) DO UPDATE SET server_id = $3`, clusterID, vmid, serverID)
	return err
}

func (s *Store) UnlinkProxmoxGuest(ctx context.Context, clusterID string, vmid int) error {
	_, err := s.db.Exec(ctx, `DELETE FROM proxmox_links WHERE cluster_id::text = $1 AND vmid = $2`, clusterID, vmid)
	return err
}

// JoinTokenServer returns the server a join token enrolled ("" if unused).
func (s *Store) JoinTokenServer(ctx context.Context, tokenHash []byte) (string, error) {
	var id *string
	err := s.db.QueryRow(ctx, `SELECT server_id::text FROM join_tokens WHERE token_hash = $1`, tokenHash).Scan(&id)
	if err != nil || id == nil {
		return "", err
	}
	return *id, nil
}

type ProxmoxProvision struct {
	ID         string     `json:"id"`
	ClusterID  string     `json:"cluster_id"`
	Name       string     `json:"name"`
	VMID       *int       `json:"vmid"`
	Status     string     `json:"status"` // running | succeeded | failed
	Log        string     `json:"log"`
	ServerID   *string    `json:"server_id"`
	CreatedBy  string     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at"`
	TenantID   *int64     `json:"tenant_id"`
}

func (s *Store) CreateProvision(ctx context.Context, p *ProxmoxProvision) error {
	return s.db.QueryRow(ctx, `INSERT INTO proxmox_provisions (id, cluster_id, name, created_by, tenant_id) VALUES ($1, $2, $3, $4, $5) RETURNING created_at`,
		p.ID, p.ClusterID, p.Name, p.CreatedBy, p.TenantID).Scan(&p.CreatedAt)
}

func (s *Store) UpdateProvision(ctx context.Context, p *ProxmoxProvision, finished bool) error {
	q := `UPDATE proxmox_provisions SET vmid = $2, status = $3, log = $4, server_id = $5`
	if finished {
		q += `, finished_at = now()`
	}
	_, err := s.db.Exec(ctx, q+` WHERE id::text = $1`, p.ID, p.VMID, p.Status, p.Log, p.ServerID)
	return err
}

// ListProvisions lists recent provisioning runs; a non-nil tenant only
// sees its own.
func (s *Store) ListProvisions(ctx context.Context, limit int, tenant *int64) ([]*ProxmoxProvision, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, cluster_id::text, name, vmid, status, log, server_id::text, created_by, created_at, finished_at, tenant_id
		FROM proxmox_provisions WHERE $2::bigint IS NULL OR tenant_id = $2 ORDER BY created_at DESC LIMIT $1`, limit, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ProxmoxProvision{}
	for rows.Next() {
		p := &ProxmoxProvision{}
		if err := rows.Scan(&p.ID, &p.ClusterID, &p.Name, &p.VMID, &p.Status, &p.Log, &p.ServerID, &p.CreatedBy, &p.CreatedAt, &p.FinishedAt, &p.TenantID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// windowsBuildMark is what a Windows template build logs once the VM
// installs by itself (api.winSetupStarted).
const windowsBuildMark = "Windows Setup runs unattended"

// ResumableWindowsBuild reports whether p is a running Windows template
// build whose VM can be watched again after a panel restart.
func ResumableWindowsBuild(p *ProxmoxProvision) bool {
	return p.Status == "running" && p.VMID != nil && strings.Contains(p.Log, windowsBuildMark)
}

// FailStaleProvisions ends the runs a panel restart cut off, except the
// Windows template builds that ResumeWindowsBuilds picks up again.
func (s *Store) FailStaleProvisions(ctx context.Context) error {
	_, err := s.db.Exec(ctx, `UPDATE proxmox_provisions SET status = 'failed', log = log || E'interrupted: the panel restarted\n', finished_at = now()
		WHERE status = 'running' AND NOT (vmid IS NOT NULL AND position($1 in log) > 0)`, windowsBuildMark)
	return err
}

// ProxmoxFlavor is a named VM size.
type ProxmoxFlavor struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Cores       int    `json:"cores"`
	MemoryMB    int    `json:"memory_mb"`
	DiskGB      int    `json:"disk_gb"`
	Description string `json:"description"`
}

func (s *Store) ListFlavors(ctx context.Context) ([]*ProxmoxFlavor, error) {
	rows, err := s.db.Query(ctx, `SELECT id, name, cores, memory_mb, disk_gb, description FROM proxmox_flavors ORDER BY cores, memory_mb, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ProxmoxFlavor{}
	for rows.Next() {
		f := &ProxmoxFlavor{}
		if err := rows.Scan(&f.ID, &f.Name, &f.Cores, &f.MemoryMB, &f.DiskGB, &f.Description); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) GetFlavor(ctx context.Context, id int64) (*ProxmoxFlavor, error) {
	f := &ProxmoxFlavor{}
	err := s.db.QueryRow(ctx, `SELECT id, name, cores, memory_mb, disk_gb, description FROM proxmox_flavors WHERE id = $1`, id).
		Scan(&f.ID, &f.Name, &f.Cores, &f.MemoryMB, &f.DiskGB, &f.Description)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return f, err
}

// SaveFlavor inserts a flavor (ID 0) or updates it.
func (s *Store) SaveFlavor(ctx context.Context, f *ProxmoxFlavor) error {
	var err error
	if f.ID == 0 {
		err = s.db.QueryRow(ctx, `INSERT INTO proxmox_flavors (name, cores, memory_mb, disk_gb, description) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			f.Name, f.Cores, f.MemoryMB, f.DiskGB, f.Description).Scan(&f.ID)
	} else {
		var tag pgconn.CommandTag
		tag, err = s.db.Exec(ctx, `UPDATE proxmox_flavors SET name = $2, cores = $3, memory_mb = $4, disk_gb = $5, description = $6 WHERE id = $1`,
			f.ID, f.Name, f.Cores, f.MemoryMB, f.DiskGB, f.Description)
		if err == nil && tag.RowsAffected() == 0 {
			return ErrNotFound
		}
	}
	if isUnique(err) {
		return ErrExists
	}
	return err
}

func (s *Store) DeleteFlavor(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM proxmox_flavors WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// GuestFact is what a guest's QEMU agent last said about its OS.
type GuestFact struct {
	VMID     int
	VMUUID   string
	OS       string
	Pretty   string
	Hostname string
	SeenAt   time.Time
}

// GuestFacts returns the stored facts for a cluster, by VMID.
func (s *Store) GuestFacts(ctx context.Context, clusterID string) (map[int]GuestFact, error) {
	rows, err := s.db.Query(ctx, `SELECT vmid, vm_uuid, os, pretty, hostname, seen_at FROM proxmox_guest_facts WHERE cluster_id = $1`, clusterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]GuestFact{}
	for rows.Next() {
		var f GuestFact
		if err := rows.Scan(&f.VMID, &f.VMUUID, &f.OS, &f.Pretty, &f.Hostname, &f.SeenAt); err != nil {
			return nil, err
		}
		out[f.VMID] = f
	}
	return out, rows.Err()
}

// SaveGuestFact records what a guest's agent reported just now.
func (s *Store) SaveGuestFact(ctx context.Context, clusterID string, f GuestFact) error {
	_, err := s.db.Exec(ctx, `INSERT INTO proxmox_guest_facts (cluster_id, vmid, vm_uuid, os, pretty, hostname, seen_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (cluster_id, vmid) DO UPDATE SET vm_uuid = $3, os = $4, pretty = $5, hostname = $6, seen_at = now()`,
		clusterID, f.VMID, f.VMUUID, f.OS, f.Pretty, f.Hostname)
	return err
}
