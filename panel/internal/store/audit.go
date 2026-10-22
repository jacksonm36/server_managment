package store

import (
	"context"
	"encoding/json"
	"time"
)

type AuditEntry struct {
	ID      int64           `json:"id"`
	TS      time.Time       `json:"ts"`
	Actor   string          `json:"actor"`
	IP      string          `json:"ip"`
	Action  string          `json:"action"`
	Target  string          `json:"target"`
	Detail  json.RawMessage `json:"detail"`
	Success bool            `json:"success"`
	// TenantID is the tenant of the user who acted, if any.
	TenantID *int64 `json:"tenant_id"`
}

func (s *Store) Audit(ctx context.Context, e *AuditEntry) error {
	detail := e.Detail
	if len(detail) == 0 {
		detail = json.RawMessage("{}")
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO audit_log (actor, ip, action, target, detail, success, tenant_id) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		e.Actor, e.IP, e.Action, e.Target, []byte(detail), e.Success, e.TenantID)
	return err
}

// ListAudit returns entries newest first. before > 0 pages backwards by id;
// action filters by prefix (e.g. "auth."); a non-nil tenant limits the
// list to what that tenant's users did.
func (s *Store) ListAudit(ctx context.Context, limit int, before int64, action string, tenant *int64) ([]*AuditEntry, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, ts, actor, ip, action, target, detail, success, tenant_id FROM audit_log
		WHERE ($2 = 0 OR id < $2) AND ($3 = '' OR action LIKE $3 || '%') AND ($4::bigint IS NULL OR tenant_id = $4)
		ORDER BY id DESC LIMIT $1`, limit, before, action, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*AuditEntry{}
	for rows.Next() {
		e := &AuditEntry{}
		var detail []byte
		if err := rows.Scan(&e.ID, &e.TS, &e.Actor, &e.IP, &e.Action, &e.Target, &detail, &e.Success, &e.TenantID); err != nil {
			return nil, err
		}
		e.Detail = detail
		out = append(out, e)
	}
	return out, rows.Err()
}

// JobSummary is a job row for the global history, without the log.
type JobSummary struct {
	ID         string     `json:"id"`
	ServerID   string     `json:"server_id"`
	ServerName string     `json:"server_name"`
	Op         string     `json:"op"`
	Status     string     `json:"status"`
	Error      string     `json:"error"`
	CreatedBy  string     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

func (s *Store) ListRecentJobs(ctx context.Context, limit int) ([]*JobSummary, error) {
	return s.listJobSummaries(ctx, `ORDER BY j.created_at DESC LIMIT $1`, limit)
}

// ListActiveJobs returns unfinished jobs and jobs that finished within the
// last `recent`, newest first.
func (s *Store) ListActiveJobs(ctx context.Context, recent time.Duration, limit int) ([]*JobSummary, error) {
	return s.listJobSummaries(ctx, `
		WHERE j.status IN ('queued','running') OR j.finished_at > now() - make_interval(secs => $2)
		ORDER BY j.created_at DESC LIMIT $1`, limit, recent.Seconds())
}

func (s *Store) listJobSummaries(ctx context.Context, tail string, args ...any) ([]*JobSummary, error) {
	rows, err := s.db.Query(ctx, `
		SELECT j.id::text, j.server_id::text, s.name, j.op, j.status, j.error, j.created_by, j.created_at, j.finished_at
		FROM jobs j JOIN servers s ON s.id = j.server_id `+tail, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*JobSummary{}
	for rows.Next() {
		j := &JobSummary{}
		if err := rows.Scan(&j.ID, &j.ServerID, &j.ServerName, &j.Op, &j.Status, &j.Error, &j.CreatedBy, &j.CreatedAt, &j.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
