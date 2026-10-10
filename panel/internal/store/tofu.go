package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type TofuWorkspace struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Source      string            `json:"source"` // inline | git
	Files       map[string]string `json:"files"`
	GitURL      string            `json:"git_url"`
	GitRef      string            `json:"git_ref"`
	GitPath     string            `json:"git_path"`
	SealedVars  string            `json:"-"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

const tofuWsCols = `id::text, name, description, source, files, git_url, git_ref, git_path, vars, created_at, updated_at`

func scanTofuWorkspace(row pgx.Row) (*TofuWorkspace, error) {
	w := &TofuWorkspace{}
	var files []byte
	err := row.Scan(&w.ID, &w.Name, &w.Description, &w.Source, &files, &w.GitURL, &w.GitRef, &w.GitPath, &w.SealedVars, &w.CreatedAt, &w.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return w, json.Unmarshal(files, &w.Files)
}

func (s *Store) ListTofuWorkspaces(ctx context.Context) ([]*TofuWorkspace, error) {
	rows, err := s.db.Query(ctx, `SELECT `+tofuWsCols+` FROM tofu_workspaces ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*TofuWorkspace{}
	for rows.Next() {
		w, err := scanTofuWorkspace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) GetTofuWorkspace(ctx context.Context, id string) (*TofuWorkspace, error) {
	return scanTofuWorkspace(s.db.QueryRow(ctx, `SELECT `+tofuWsCols+` FROM tofu_workspaces WHERE id::text = $1`, id))
}

// SaveTofuWorkspace inserts (empty ID) or updates a workspace, vars included.
func (s *Store) SaveTofuWorkspace(ctx context.Context, w *TofuWorkspace) error {
	files, _ := json.Marshal(w.Files)
	if w.ID == "" {
		w.ID = NewUUID()
		_, err := s.db.Exec(ctx, `
			INSERT INTO tofu_workspaces (id, name, description, source, files, git_url, git_ref, git_path, vars)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			w.ID, w.Name, w.Description, w.Source, files, w.GitURL, w.GitRef, w.GitPath, w.SealedVars)
		return err
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE tofu_workspaces SET name = $2, description = $3, source = $4, files = $5, git_url = $6, git_ref = $7,
			git_path = $8, vars = $9, updated_at = now()
		WHERE id::text = $1`,
		w.ID, w.Name, w.Description, w.Source, files, w.GitURL, w.GitRef, w.GitPath, w.SealedVars)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) DeleteTofuWorkspace(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM tofu_workspaces WHERE id::text = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ---- state (sealed by the caller) ----

type TofuState struct {
	Sealed    string    `json:"-"`
	Serial    int64     `json:"serial"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Store) GetTofuState(ctx context.Context, wsID string) (*TofuState, error) {
	st := &TofuState{}
	err := s.db.QueryRow(ctx, `SELECT state, serial, updated_at FROM tofu_states WHERE workspace_id::text = $1`, wsID).
		Scan(&st.Sealed, &st.Serial, &st.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return st, err
}

// PutTofuState stores the new state and keeps the last 20 versions.
func (s *Store) PutTofuState(ctx context.Context, wsID, sealed string, serial int64) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO tofu_states (workspace_id, state, serial, updated_at) VALUES ($1, $2, $3, now())
		ON CONFLICT (workspace_id) DO UPDATE SET state = $2, serial = $3, updated_at = now()`, wsID, sealed, serial); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO tofu_state_versions (workspace_id, state, serial) VALUES ($1, $2, $3)`, wsID, sealed, serial); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM tofu_state_versions WHERE workspace_id::text = $1 AND id NOT IN (
			SELECT id FROM tofu_state_versions WHERE workspace_id::text = $1 ORDER BY id DESC LIMIT 20)`, wsID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) DeleteTofuState(ctx context.Context, wsID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM tofu_states WHERE workspace_id::text = $1`, wsID)
	return err
}

type TofuStateVersion struct {
	ID        int64     `json:"id"`
	Serial    int64     `json:"serial"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) ListTofuStateVersions(ctx context.Context, wsID string) ([]TofuStateVersion, error) {
	rows, err := s.db.Query(ctx, `SELECT id, serial, created_at FROM tofu_state_versions WHERE workspace_id::text = $1 ORDER BY id DESC`, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TofuStateVersion{}
	for rows.Next() {
		var v TofuStateVersion
		if err := rows.Scan(&v.ID, &v.Serial, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---- locks ----

// TofuLock takes the workspace lock. If it is held, it returns false and
// the current holder's info.
func (s *Store) TofuLock(ctx context.Context, wsID, lockID string, info []byte) (bool, []byte, error) {
	tag, err := s.db.Exec(ctx, `INSERT INTO tofu_locks (workspace_id, lock_id, info) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, wsID, lockID, info)
	if err != nil {
		return false, nil, err
	}
	if tag.RowsAffected() == 1 {
		return true, nil, nil
	}
	var cur []byte
	err = s.db.QueryRow(ctx, `SELECT info FROM tofu_locks WHERE workspace_id::text = $1`, wsID).Scan(&cur)
	return false, cur, err
}

// TofuLockHolder returns the current lock ID ("" if unlocked).
func (s *Store) TofuLockHolder(ctx context.Context, wsID string) (string, error) {
	var id string
	err := s.db.QueryRow(ctx, `SELECT lock_id FROM tofu_locks WHERE workspace_id::text = $1`, wsID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// TofuUnlock releases the lock; an empty lockID force-unlocks.
func (s *Store) TofuUnlock(ctx context.Context, wsID, lockID string) (bool, error) {
	var tag interface{ RowsAffected() int64 }
	var err error
	if lockID == "" {
		tag, err = s.db.Exec(ctx, `DELETE FROM tofu_locks WHERE workspace_id::text = $1`, wsID)
	} else {
		tag, err = s.db.Exec(ctx, `DELETE FROM tofu_locks WHERE workspace_id::text = $1 AND lock_id = $2`, wsID, lockID)
	}
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ---- runs ----

type TofuRun struct {
	ID            string          `json:"id"`
	WorkspaceID   string          `json:"workspace_id"`
	WorkspaceName string          `json:"workspace_name"`
	Kind          string          `json:"kind"` // plan | destroy-plan | apply
	PlanRunID     *string         `json:"plan_run_id"`
	Status        string          `json:"status"` // running | succeeded | failed | cancelled
	Output        string          `json:"output,omitempty"`
	Summary       json.RawMessage `json:"summary"`
	Applied       bool            `json:"applied"`
	CreatedBy     string          `json:"created_by"`
	CreatedAt     time.Time       `json:"created_at"`
	FinishedAt    *time.Time      `json:"finished_at"`
}

const tofuRunCols = `id::text, workspace_id::text, workspace_name, kind, plan_run_id::text, status, summary, applied, created_by, created_at, finished_at`

func scanTofuRun(row pgx.Row, withOutput bool) (*TofuRun, error) {
	r := &TofuRun{}
	dest := []any{&r.ID, &r.WorkspaceID, &r.WorkspaceName, &r.Kind, &r.PlanRunID, &r.Status, &r.Summary, &r.Applied, &r.CreatedBy, &r.CreatedAt, &r.FinishedAt}
	if withOutput {
		dest = append(dest, &r.Output)
	}
	err := row.Scan(dest...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

func (s *Store) CreateTofuRun(ctx context.Context, r *TofuRun) error {
	return s.db.QueryRow(ctx, `
		INSERT INTO tofu_runs (id, workspace_id, workspace_name, kind, plan_run_id, status, created_by)
		VALUES ($1, $2, $3, $4, $5, 'running', $6) RETURNING created_at`,
		r.ID, r.WorkspaceID, r.WorkspaceName, r.Kind, r.PlanRunID, r.CreatedBy).Scan(&r.CreatedAt)
}

func (s *Store) SetTofuRunOutput(ctx context.Context, id, output string) error {
	_, err := s.db.Exec(ctx, `UPDATE tofu_runs SET output = $2 WHERE id::text = $1`, id, output)
	return err
}

func (s *Store) FinishTofuRun(ctx context.Context, id, status, output string, summary any) error {
	sum, _ := json.Marshal(summary)
	_, err := s.db.Exec(ctx, `UPDATE tofu_runs SET status = $2, output = $3, summary = $4, finished_at = now() WHERE id::text = $1`,
		id, status, output, sum)
	return err
}

func (s *Store) MarkTofuPlanApplied(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `UPDATE tofu_runs SET applied = true WHERE id::text = $1`, id)
	return err
}

func (s *Store) FailStaleTofuRuns(ctx context.Context) error {
	_, err := s.db.Exec(ctx, `
		UPDATE tofu_runs SET status = 'failed', output = output || E'\n[servermgr] interrupted: the panel restarted\n', finished_at = now()
		WHERE status = 'running'`)
	return err
}

func (s *Store) ListTofuRuns(ctx context.Context, wsID string, limit int) ([]*TofuRun, error) {
	q := `SELECT ` + tofuRunCols + ` FROM tofu_runs`
	args := []any{limit}
	if wsID != "" {
		q += ` WHERE workspace_id::text = $2`
		args = append(args, wsID)
	}
	rows, err := s.db.Query(ctx, q+` ORDER BY created_at DESC LIMIT $1`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*TofuRun{}
	for rows.Next() {
		r, err := scanTofuRun(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetTofuRun(ctx context.Context, id string) (*TofuRun, error) {
	return scanTofuRun(s.db.QueryRow(ctx, `SELECT `+tofuRunCols+`, output FROM tofu_runs WHERE id::text = $1`, id), true)
}

// LatestTofuPlan is the newest plan or destroy-plan run of a workspace.
func (s *Store) LatestTofuPlan(ctx context.Context, wsID string) (*TofuRun, error) {
	return scanTofuRun(s.db.QueryRow(ctx, `SELECT `+tofuRunCols+` FROM tofu_runs
		WHERE workspace_id::text = $1 AND kind IN ('plan', 'destroy-plan') ORDER BY created_at DESC LIMIT 1`, wsID), false)
}
