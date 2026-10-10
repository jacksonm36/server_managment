package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type AnsibleHost struct {
	ServerID   string    `json:"server_id"`
	Address    string    `json:"address"`
	Port       int       `json:"port"`
	HostKeys   []string  `json:"host_keys"`
	RootLogin  string    `json:"root_login"`
	PreparedAt time.Time `json:"prepared_at"`
}

func (s *Store) ListAnsibleHosts(ctx context.Context) (map[string]*AnsibleHost, error) {
	rows, err := s.db.Query(ctx, `SELECT server_id::text, address, port, host_keys, root_login, prepared_at FROM ansible_hosts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*AnsibleHost{}
	for rows.Next() {
		h := &AnsibleHost{}
		var keys []byte
		if err := rows.Scan(&h.ServerID, &h.Address, &h.Port, &keys, &h.RootLogin, &h.PreparedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(keys, &h.HostKeys); err != nil {
			return nil, err
		}
		out[h.ServerID] = h
	}
	return out, rows.Err()
}

func (s *Store) SaveAnsibleHost(ctx context.Context, h *AnsibleHost) error {
	keys, _ := json.Marshal(h.HostKeys)
	_, err := s.db.Exec(ctx, `
		INSERT INTO ansible_hosts (server_id, address, port, host_keys, root_login, prepared_at)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (server_id) DO UPDATE SET address = $2, port = $3, host_keys = $4, root_login = $5, prepared_at = now()`,
		h.ServerID, h.Address, h.Port, keys, h.RootLogin)
	return err
}

func (s *Store) DeleteAnsibleHost(ctx context.Context, serverID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM ansible_hosts WHERE server_id::text = $1`, serverID)
	return err
}

type AnsiblePlaybook struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Source      string    `json:"source"` // inline | git
	Content     string    `json:"content"`
	GitURL      string    `json:"git_url"`
	GitRef      string    `json:"git_ref"`
	GitPath     string    `json:"git_path"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

const ansiblePlaybookCols = `id::text, name, description, source, content, git_url, git_ref, git_path, created_at, updated_at`

func scanAnsiblePlaybook(row pgx.Row) (*AnsiblePlaybook, error) {
	p := &AnsiblePlaybook{}
	err := row.Scan(&p.ID, &p.Name, &p.Description, &p.Source, &p.Content, &p.GitURL, &p.GitRef, &p.GitPath, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

func (s *Store) ListAnsiblePlaybooks(ctx context.Context) ([]*AnsiblePlaybook, error) {
	rows, err := s.db.Query(ctx, `SELECT `+ansiblePlaybookCols+` FROM ansible_playbooks ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*AnsiblePlaybook{}
	for rows.Next() {
		p, err := scanAnsiblePlaybook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetAnsiblePlaybook(ctx context.Context, id string) (*AnsiblePlaybook, error) {
	return scanAnsiblePlaybook(s.db.QueryRow(ctx, `SELECT `+ansiblePlaybookCols+` FROM ansible_playbooks WHERE id::text = $1`, id))
}

// SaveAnsiblePlaybook inserts (empty ID) or updates a playbook.
func (s *Store) SaveAnsiblePlaybook(ctx context.Context, p *AnsiblePlaybook) error {
	if p.ID == "" {
		p.ID = NewUUID()
		_, err := s.db.Exec(ctx, `
			INSERT INTO ansible_playbooks (id, name, description, source, content, git_url, git_ref, git_path)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			p.ID, p.Name, p.Description, p.Source, p.Content, p.GitURL, p.GitRef, p.GitPath)
		return err
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE ansible_playbooks SET name = $2, description = $3, source = $4, content = $5, git_url = $6, git_ref = $7,
			git_path = $8, updated_at = now()
		WHERE id::text = $1`,
		p.ID, p.Name, p.Description, p.Source, p.Content, p.GitURL, p.GitRef, p.GitPath)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) DeleteAnsiblePlaybook(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM ansible_playbooks WHERE id::text = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// AnsibleRecap is one host's line from PLAY RECAP.
type AnsibleRecap struct {
	OK          int `json:"ok"`
	Changed     int `json:"changed"`
	Unreachable int `json:"unreachable"`
	Failed      int `json:"failed"`
	Skipped     int `json:"skipped"`
}

type AnsibleRun struct {
	ID           string                   `json:"id"`
	PlaybookID   *string                  `json:"playbook_id"`
	PlaybookName string                   `json:"playbook_name"`
	ServerIDs    []string                 `json:"server_ids"`
	Check        bool                     `json:"check"`
	Status       string                   `json:"status"` // running | succeeded | failed | cancelled
	Output       string                   `json:"output,omitempty"`
	Recap        map[string]*AnsibleRecap `json:"recap"`
	CreatedBy    string                   `json:"created_by"`
	CreatedAt    time.Time                `json:"created_at"`
	FinishedAt   *time.Time               `json:"finished_at"`
}

func scanAnsibleRun(row pgx.Row, withOutput bool) (*AnsibleRun, error) {
	r := &AnsibleRun{}
	var ids, recap []byte
	dest := []any{&r.ID, &r.PlaybookID, &r.PlaybookName, &ids, &r.Check, &r.Status, &recap, &r.CreatedBy, &r.CreatedAt, &r.FinishedAt}
	if withOutput {
		dest = append(dest, &r.Output)
	}
	err := row.Scan(dest...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(ids, &r.ServerIDs); err != nil {
		return nil, err
	}
	if recap != nil {
		_ = json.Unmarshal(recap, &r.Recap)
	}
	return r, nil
}

const ansibleRunCols = `id::text, playbook_id::text, playbook_name, server_ids, check_mode, status, recap, created_by, created_at, finished_at`

func (s *Store) CreateAnsibleRun(ctx context.Context, r *AnsibleRun) error {
	ids, _ := json.Marshal(r.ServerIDs)
	return s.db.QueryRow(ctx, `
		INSERT INTO ansible_runs (id, playbook_id, playbook_name, server_ids, check_mode, status, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING created_at`,
		r.ID, r.PlaybookID, r.PlaybookName, ids, r.Check, r.Status, r.CreatedBy).Scan(&r.CreatedAt)
}

func (s *Store) SetAnsibleRunOutput(ctx context.Context, id, output string) error {
	_, err := s.db.Exec(ctx, `UPDATE ansible_runs SET output = $2 WHERE id::text = $1`, id, output)
	return err
}

func (s *Store) FinishAnsibleRun(ctx context.Context, id, status, output string, recap map[string]*AnsibleRecap) error {
	rc, _ := json.Marshal(recap)
	_, err := s.db.Exec(ctx, `UPDATE ansible_runs SET status = $2, output = $3, recap = $4, finished_at = now() WHERE id::text = $1`,
		id, status, output, rc)
	return err
}

// FailStaleAnsibleRuns marks runs left "running" by a panel restart.
func (s *Store) FailStaleAnsibleRuns(ctx context.Context) error {
	_, err := s.db.Exec(ctx, `
		UPDATE ansible_runs SET status = 'failed', output = output || E'\n[servermgr] interrupted: the panel restarted\n',
			finished_at = now()
		WHERE status = 'running'`)
	return err
}

func (s *Store) ListAnsibleRuns(ctx context.Context, limit int) ([]*AnsibleRun, error) {
	rows, err := s.db.Query(ctx, `SELECT `+ansibleRunCols+` FROM ansible_runs ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*AnsibleRun{}
	for rows.Next() {
		r, err := scanAnsibleRun(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetAnsibleRun(ctx context.Context, id string) (*AnsibleRun, error) {
	return scanAnsibleRun(s.db.QueryRow(ctx, `SELECT `+ansibleRunCols+`, output FROM ansible_runs WHERE id::text = $1`, id), true)
}
