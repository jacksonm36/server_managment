package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type Step struct {
	Op              string            `json:"op"`
	Params          map[string]string `json:"params"`
	ContinueOnError bool              `json:"continue_on_error,omitempty"`
}

type Playbook struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Steps       []Step    `json:"steps"`
	Builtin     bool      `json:"builtin"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// RunStep is a step's state inside a playbook run.
type RunStep struct {
	Step
	Status string `json:"status"` // pending | running | succeeded | failed | skipped
	JobID  string `json:"job_id,omitempty"`
	Error  string `json:"error,omitempty"`
}

type PlaybookRun struct {
	ID           string     `json:"id"`
	PlaybookID   *string    `json:"playbook_id"`
	PlaybookName string     `json:"playbook_name"`
	ServerID     string     `json:"server_id"`
	ServerName   string     `json:"server_name"`
	Status       string     `json:"status"` // running | succeeded | failed
	Steps        []RunStep  `json:"steps"`
	CreatedBy    string     `json:"created_by"`
	CreatedAt    time.Time  `json:"created_at"`
	FinishedAt   *time.Time `json:"finished_at"`
}

const playbookCols = `id::text, name, description, steps, builtin, created_at, updated_at`

func scanPlaybook(row pgx.Row) (*Playbook, error) {
	p := &Playbook{}
	var steps []byte
	err := row.Scan(&p.ID, &p.Name, &p.Description, &steps, &p.Builtin, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return p, json.Unmarshal(steps, &p.Steps)
}

func (s *Store) ListPlaybooks(ctx context.Context) ([]*Playbook, error) {
	rows, err := s.db.Query(ctx, `SELECT `+playbookCols+` FROM playbooks ORDER BY builtin DESC, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Playbook{}
	for rows.Next() {
		p, err := scanPlaybook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetPlaybook(ctx context.Context, id string) (*Playbook, error) {
	return scanPlaybook(s.db.QueryRow(ctx, `SELECT `+playbookCols+` FROM playbooks WHERE id::text = $1`, id))
}

// SavePlaybook inserts (empty ID) or updates a playbook.
func (s *Store) SavePlaybook(ctx context.Context, p *Playbook) error {
	steps, _ := json.Marshal(p.Steps)
	if p.ID == "" {
		p.ID = NewUUID()
		return s.db.QueryRow(ctx, `INSERT INTO playbooks (id, name, description, steps) VALUES ($1, $2, $3, $4) RETURNING created_at, updated_at`,
			p.ID, p.Name, p.Description, steps).Scan(&p.CreatedAt, &p.UpdatedAt)
	}
	tag, err := s.db.Exec(ctx, `UPDATE playbooks SET name = $2, description = $3, steps = $4, updated_at = now() WHERE id::text = $1`,
		p.ID, p.Name, p.Description, steps)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) DeletePlaybook(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM playbooks WHERE id::text = $1`, id)
	return err
}

// ---- runs ----

func (s *Store) CreateRun(ctx context.Context, r *PlaybookRun) error {
	steps, _ := json.Marshal(r.Steps)
	return s.db.QueryRow(ctx, `
		INSERT INTO playbook_runs (id, playbook_id, playbook_name, server_id, status, steps, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING created_at`,
		r.ID, r.PlaybookID, r.PlaybookName, r.ServerID, r.Status, steps, r.CreatedBy).Scan(&r.CreatedAt)
}

func (s *Store) UpdateRun(ctx context.Context, r *PlaybookRun) error {
	steps, _ := json.Marshal(r.Steps)
	_, err := s.db.Exec(ctx, `
		UPDATE playbook_runs SET status = $2, steps = $3,
		finished_at = CASE WHEN $2 <> 'running' THEN now() ELSE NULL END WHERE id::text = $1`,
		r.ID, r.Status, steps)
	return err
}

const runCols = `r.id::text, r.playbook_id::text, r.playbook_name, r.server_id::text, s.name, r.status, r.steps, r.created_by, r.created_at, r.finished_at`

func scanRun(row pgx.Row) (*PlaybookRun, error) {
	r := &PlaybookRun{}
	var steps []byte
	err := row.Scan(&r.ID, &r.PlaybookID, &r.PlaybookName, &r.ServerID, &r.ServerName, &r.Status, &steps, &r.CreatedBy, &r.CreatedAt, &r.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return r, json.Unmarshal(steps, &r.Steps)
}

func (s *Store) ListRuns(ctx context.Context, limit int) ([]*PlaybookRun, error) {
	rows, err := s.db.Query(ctx, `SELECT `+runCols+` FROM playbook_runs r JOIN servers s ON s.id = r.server_id ORDER BY r.created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*PlaybookRun{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FailOrphanedRuns marks runs that were in progress when the panel stopped.
func (s *Store) FailOrphanedRuns(ctx context.Context) error {
	_, err := s.db.Exec(ctx, `UPDATE playbook_runs SET status = 'failed', finished_at = now() WHERE status = 'running'`)
	return err
}
