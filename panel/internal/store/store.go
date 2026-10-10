// Package store is the panel's PostgreSQL persistence layer.
package store

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

var ErrNotFound = errors.New("not found")

type Store struct{ db *pgxpool.Pool }

func Open(ctx context.Context, url string) (*Store, error) {
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(ctx); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() { s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	names, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		var exists bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)`, name).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		sql, err := migrationsFS.ReadFile(name)
		if err != nil {
			return err
		}
		err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, name)
			return err
		})
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

// NewUUID returns a random (v4) UUID string.
func NewUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ---- admins & sessions ----

type Admin struct {
	ID           int64
	Username     string
	PasswordHash string
	TOTPEnabled  bool
	TOTPSecret   string // sealed
	TOTPPending  string // sealed, set during setup
}

const adminCols = `a.id, a.username, a.password_hash, a.totp_enabled, COALESCE(a.totp_secret, ''), COALESCE(a.totp_pending, '')`

func scanAdmin(row pgx.Row) (*Admin, error) {
	a := &Admin{}
	err := row.Scan(&a.ID, &a.Username, &a.PasswordHash, &a.TOTPEnabled, &a.TOTPSecret, &a.TOTPPending)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

func (s *Store) AdminCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM admins`).Scan(&n)
	return n, err
}

func (s *Store) CreateAdmin(ctx context.Context, username, hash string) error {
	_, err := s.db.Exec(ctx, `INSERT INTO admins (username, password_hash) VALUES ($1, $2)`, username, hash)
	return err
}

func (s *Store) AdminByUsername(ctx context.Context, username string) (*Admin, error) {
	return scanAdmin(s.db.QueryRow(ctx, `SELECT `+adminCols+` FROM admins a WHERE a.username = $1`, username))
}

func (s *Store) CreateSession(ctx context.Context, tokenHash []byte, adminID int64, expires time.Time) error {
	_, err := s.db.Exec(ctx, `INSERT INTO sessions (token_hash, admin_id, expires_at) VALUES ($1, $2, $3)`, tokenHash, adminID, expires)
	return err
}

func (s *Store) SessionAdmin(ctx context.Context, tokenHash []byte) (*Admin, error) {
	return scanAdmin(s.db.QueryRow(ctx, `
		SELECT `+adminCols+` FROM sessions s JOIN admins a ON a.id = s.admin_id
		WHERE s.token_hash = $1 AND s.expires_at > now()`, tokenHash))
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	return err
}

func (s *Store) PurgeExpired(ctx context.Context) error {
	_, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now()`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `DELETE FROM join_tokens WHERE used_at IS NULL AND expires_at < now() - interval '1 day'`)
	if err != nil {
		return err
	}
	// File browsing jobs carry file contents; they are only useful briefly.
	_, err = s.db.Exec(ctx, `DELETE FROM jobs WHERE op IN ('files.list', 'files.read', 'logs.read', 'services.list') AND created_at < now() - interval '1 hour'`)
	return err
}

// ---- join tokens & enrollment ----

func (s *Store) CreateJoinToken(ctx context.Context, tokenHash []byte, name string, expires time.Time, playbookID *string) error {
	_, err := s.db.Exec(ctx, `INSERT INTO join_tokens (token_hash, name, expires_at, playbook_id) VALUES ($1, $2, $3, $4)`, tokenHash, name, expires, playbookID)
	return err
}

// Enroll atomically consumes a join token and creates the server record.
// It returns ErrNotFound if the token is unknown, used or expired.
func (s *Store) Enroll(ctx context.Context, tokenHash []byte, hostname string) (serverID, name string, err error) {
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT name FROM join_tokens
			WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
			FOR UPDATE`, tokenHash).Scan(&name)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if name == "" {
			name = hostname
		}
		serverID = NewUUID()
		if _, err := tx.Exec(ctx, `INSERT INTO servers (id, name, hostname) VALUES ($1, $2, $3)`, serverID, name, hostname); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE join_tokens SET used_at = now(), server_id = $2 WHERE token_hash = $1`, tokenHash, serverID)
		return err
	})
	return serverID, name, err
}

// ---- servers ----

type Server struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Hostname     string     `json:"hostname"`
	OS           string     `json:"os"`
	OSVersion    string     `json:"os_version"`
	OSPretty     string     `json:"os_pretty"`
	Kernel       string     `json:"kernel"`
	Arch         string     `json:"arch"`
	CPUCount     int        `json:"cpu_count"`
	AgentVersion string     `json:"agent_version"`
	CreatedAt    time.Time  `json:"created_at"`
	LastSeenAt   *time.Time `json:"last_seen_at"`
}

const serverCols = `id::text, name, hostname, os, os_version, os_pretty, kernel, arch, cpu_count, agent_version, created_at, last_seen_at`

func scanServer(row pgx.Row) (*Server, error) {
	v := &Server{}
	err := row.Scan(&v.ID, &v.Name, &v.Hostname, &v.OS, &v.OSVersion, &v.OSPretty, &v.Kernel, &v.Arch, &v.CPUCount, &v.AgentVersion, &v.CreatedAt, &v.LastSeenAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return v, err
}

func (s *Store) ListServers(ctx context.Context) ([]*Server, error) {
	rows, err := s.db.Query(ctx, `SELECT `+serverCols+` FROM servers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Server{}
	for rows.Next() {
		v, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) GetServer(ctx context.Context, id string) (*Server, error) {
	return scanServer(s.db.QueryRow(ctx, `SELECT `+serverCols+` FROM servers WHERE id::text = $1`, id))
}

func (s *Store) DeleteServer(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM servers WHERE id::text = $1`, id)
	return err
}

func (s *Store) UpdateServerHello(ctx context.Context, v *Server) error {
	_, err := s.db.Exec(ctx, `
		UPDATE servers SET hostname=$2, os=$3, os_version=$4, os_pretty=$5, kernel=$6, arch=$7,
		cpu_count=$8, agent_version=$9, last_seen_at=now() WHERE id::text=$1`,
		v.ID, v.Hostname, v.OS, v.OSVersion, v.OSPretty, v.Kernel, v.Arch, v.CPUCount, v.AgentVersion)
	return err
}

func (s *Store) TouchServer(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `UPDATE servers SET last_seen_at = now() WHERE id::text = $1`, id)
	return err
}

// ---- jobs ----

type Job struct {
	ID         string            `json:"id"`
	ServerID   string            `json:"server_id"`
	Op         string            `json:"op"`
	Params     map[string]string `json:"params"`
	Status     string            `json:"status"`
	Log        string            `json:"log"`
	Result     json.RawMessage   `json:"result"`
	Error      string            `json:"error"`
	CreatedBy  string            `json:"created_by"`
	CreatedAt  time.Time         `json:"created_at"`
	FinishedAt *time.Time        `json:"finished_at"`
}

func (s *Store) CreateJob(ctx context.Context, j *Job) error {
	params, _ := json.Marshal(j.Params)
	return s.db.QueryRow(ctx, `
		INSERT INTO jobs (id, server_id, op, params, status, created_by) VALUES ($1, $2, $3, $4, $5, $6) RETURNING created_at`,
		j.ID, j.ServerID, j.Op, params, j.Status, j.CreatedBy).Scan(&j.CreatedAt)
}

// UpdateJob appends log lines and sets status/result/error.
func (s *Store) UpdateJob(ctx context.Context, id, status string, logAppend string, result []byte, errMsg string, finished bool) error {
	var res any
	if len(result) > 0 {
		res = result
	}
	_, err := s.db.Exec(ctx, `
		UPDATE jobs SET status = $2, log = log || $3, result = COALESCE($4::jsonb, result),
		error = CASE WHEN $5 = '' THEN error ELSE $5 END,
		finished_at = CASE WHEN $6 THEN now() ELSE finished_at END
		WHERE id::text = $1`, id, status, logAppend, res, errMsg, finished)
	return err
}

const jobCols = `id::text, server_id::text, op, params, status, log, result, error, created_by, created_at, finished_at`

func scanJob(row pgx.Row) (*Job, error) {
	j := &Job{}
	var params []byte
	var result []byte
	err := row.Scan(&j.ID, &j.ServerID, &j.Op, &params, &j.Status, &j.Log, &result, &j.Error, &j.CreatedBy, &j.CreatedAt, &j.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(params, &j.Params)
	if len(result) > 0 {
		j.Result = result
	}
	return j, nil
}

func (s *Store) GetJob(ctx context.Context, id string) (*Job, error) {
	return scanJob(s.db.QueryRow(ctx, `SELECT `+jobCols+` FROM jobs WHERE id::text = $1`, id))
}

func (s *Store) ListJobs(ctx context.Context, serverID string, limit int) ([]*Job, error) {
	rows, err := s.db.Query(ctx, `SELECT `+jobCols+` FROM jobs WHERE server_id::text = $1 ORDER BY created_at DESC LIMIT $2`, serverID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// FailOrphanedJobs marks jobs that were running when the panel stopped.
func (s *Store) FailOrphanedJobs(ctx context.Context) error {
	_, err := s.db.Exec(ctx, `UPDATE jobs SET status='failed', error='panel restarted', finished_at=now() WHERE status IN ('queued','running')`)
	return err
}
