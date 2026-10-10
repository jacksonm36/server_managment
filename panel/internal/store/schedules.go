package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type Schedule struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Cron       string            `json:"cron"`
	Kind       string            `json:"kind"` // op | playbook | ansible
	Op         string            `json:"op"`
	Params     map[string]string `json:"params"`
	PlaybookID *string           `json:"playbook_id"`
	ServerIDs  []string          `json:"server_ids"`
	AllServers bool              `json:"all_servers"`
	Enabled    bool              `json:"enabled"`
	CreatedBy  string            `json:"created_by"`
	LastRun    *time.Time        `json:"last_run"`
	LastResult string            `json:"last_result"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

const scheduleCols = `id::text, name, cron, kind, op, params, COALESCE(playbook_id, ansible_playbook_id)::text, server_ids, all_servers, enabled, created_by, last_run, last_result, created_at, updated_at`

func scanSchedule(row pgx.Row) (*Schedule, error) {
	s := &Schedule{}
	var params, servers []byte
	err := row.Scan(&s.ID, &s.Name, &s.Cron, &s.Kind, &s.Op, &params, &s.PlaybookID, &servers, &s.AllServers, &s.Enabled,
		&s.CreatedBy, &s.LastRun, &s.LastResult, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(params, &s.Params)
	_ = json.Unmarshal(servers, &s.ServerIDs)
	if s.ServerIDs == nil {
		s.ServerIDs = []string{}
	}
	return s, nil
}

func (s *Store) ListSchedules(ctx context.Context) ([]*Schedule, error) {
	rows, err := s.db.Query(ctx, `SELECT `+scheduleCols+` FROM schedules ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Schedule{}
	for rows.Next() {
		sc, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

func (s *Store) GetSchedule(ctx context.Context, id string) (*Schedule, error) {
	return scanSchedule(s.db.QueryRow(ctx, `SELECT `+scheduleCols+` FROM schedules WHERE id::text = $1`, id))
}

func (s *Store) SaveSchedule(ctx context.Context, sc *Schedule) error {
	params, _ := json.Marshal(sc.Params)
	servers, _ := json.Marshal(sc.ServerIDs)
	pbID, ansID := sc.PlaybookID, (*string)(nil)
	if sc.Kind == "ansible" {
		pbID, ansID = nil, sc.PlaybookID
	}
	if sc.ID == "" {
		sc.ID = NewUUID()
		return s.db.QueryRow(ctx, `
			INSERT INTO schedules (id, name, cron, kind, op, params, playbook_id, server_ids, all_servers, enabled, created_by, ansible_playbook_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING created_at, updated_at`,
			sc.ID, sc.Name, sc.Cron, sc.Kind, sc.Op, params, pbID, servers, sc.AllServers, sc.Enabled, sc.CreatedBy, ansID).
			Scan(&sc.CreatedAt, &sc.UpdatedAt)
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE schedules SET name = $2, cron = $3, kind = $4, op = $5, params = $6, playbook_id = $7, server_ids = $8,
		all_servers = $9, enabled = $10, ansible_playbook_id = $11, updated_at = now() WHERE id::text = $1`,
		sc.ID, sc.Name, sc.Cron, sc.Kind, sc.Op, params, pbID, servers, sc.AllServers, sc.Enabled, ansID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) DeleteSchedule(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM schedules WHERE id::text = $1`, id)
	return err
}

// ClaimScheduleRun marks a schedule as run for the given minute. It returns
// false if that minute was already claimed, so a run never fires twice.
func (s *Store) ClaimScheduleRun(ctx context.Context, id string, minute time.Time) (bool, error) {
	tag, err := s.db.Exec(ctx, `UPDATE schedules SET last_run = $2 WHERE id::text = $1 AND (last_run IS NULL OR last_run < $2)`, id, minute)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) SetScheduleResult(ctx context.Context, id, result string) error {
	_, err := s.db.Exec(ctx, `UPDATE schedules SET last_result = $2 WHERE id::text = $1`, id, result)
	return err
}
