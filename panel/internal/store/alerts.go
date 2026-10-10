package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---- notification channels ----

type Channel struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Type       string     `json:"type"`
	Config     string     `json:"-"` // sealed JSON
	Enabled    bool       `json:"enabled"`
	LastSentAt *time.Time `json:"last_sent_at"`
	LastError  string     `json:"last_error"`
	CreatedAt  time.Time  `json:"created_at"`
}

const channelCols = `id::text, name, type, config, enabled, last_sent_at, last_error, created_at`

func scanChannel(row pgx.Row) (*Channel, error) {
	c := &Channel{}
	err := row.Scan(&c.ID, &c.Name, &c.Type, &c.Config, &c.Enabled, &c.LastSentAt, &c.LastError, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

func (s *Store) ListChannels(ctx context.Context) ([]*Channel, error) {
	rows, err := s.db.Query(ctx, `SELECT `+channelCols+` FROM notify_channels ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Channel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetChannel(ctx context.Context, id string) (*Channel, error) {
	return scanChannel(s.db.QueryRow(ctx, `SELECT `+channelCols+` FROM notify_channels WHERE id::text = $1`, id))
}

func (s *Store) SaveChannel(ctx context.Context, c *Channel) error {
	if c.ID == "" {
		c.ID = NewUUID()
		return s.db.QueryRow(ctx, `INSERT INTO notify_channels (id, name, type, config, enabled) VALUES ($1, $2, $3, $4, $5) RETURNING created_at`,
			c.ID, c.Name, c.Type, c.Config, c.Enabled).Scan(&c.CreatedAt)
	}
	tag, err := s.db.Exec(ctx, `UPDATE notify_channels SET name = $2, type = $3, config = $4, enabled = $5 WHERE id::text = $1`,
		c.ID, c.Name, c.Type, c.Config, c.Enabled)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) DeleteChannel(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM notify_channels WHERE id::text = $1`, id)
	return err
}

func (s *Store) ChannelResult(ctx context.Context, id, errMsg string) error {
	_, err := s.db.Exec(ctx, `UPDATE notify_channels SET last_sent_at = CASE WHEN $2 = '' THEN now() ELSE last_sent_at END, last_error = $2 WHERE id::text = $1`, id, errMsg)
	return err
}

// ---- rules ----

type AlertRule struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Metric      string    `json:"metric"`
	Threshold   float64   `json:"threshold"`
	DurationSec int       `json:"duration_sec"`
	ServerIDs   []string  `json:"server_ids"`
	AllServers  bool      `json:"all_servers"`
	ChannelIDs  []string  `json:"channel_ids"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

const ruleCols = `id::text, name, metric, threshold, duration_sec, server_ids, all_servers, channel_ids, enabled, created_at, updated_at`

func scanRule(row pgx.Row) (*AlertRule, error) {
	r := &AlertRule{}
	var servers, channels []byte
	err := row.Scan(&r.ID, &r.Name, &r.Metric, &r.Threshold, &r.DurationSec, &servers, &r.AllServers, &channels, &r.Enabled, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(servers, &r.ServerIDs)
	_ = json.Unmarshal(channels, &r.ChannelIDs)
	if r.ServerIDs == nil {
		r.ServerIDs = []string{}
	}
	if r.ChannelIDs == nil {
		r.ChannelIDs = []string{}
	}
	return r, nil
}

func (s *Store) ListRules(ctx context.Context) ([]*AlertRule, error) {
	rows, err := s.db.Query(ctx, `SELECT `+ruleCols+` FROM alert_rules ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*AlertRule{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetRule(ctx context.Context, id string) (*AlertRule, error) {
	return scanRule(s.db.QueryRow(ctx, `SELECT `+ruleCols+` FROM alert_rules WHERE id::text = $1`, id))
}

func (s *Store) SaveRule(ctx context.Context, r *AlertRule) error {
	servers, _ := json.Marshal(r.ServerIDs)
	channels, _ := json.Marshal(r.ChannelIDs)
	if r.ID == "" {
		r.ID = NewUUID()
		return s.db.QueryRow(ctx, `
			INSERT INTO alert_rules (id, name, metric, threshold, duration_sec, server_ids, all_servers, channel_ids, enabled)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING created_at, updated_at`,
			r.ID, r.Name, r.Metric, r.Threshold, r.DurationSec, servers, r.AllServers, channels, r.Enabled).Scan(&r.CreatedAt, &r.UpdatedAt)
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE alert_rules SET name = $2, metric = $3, threshold = $4, duration_sec = $5, server_ids = $6, all_servers = $7,
		channel_ids = $8, enabled = $9, updated_at = now() WHERE id::text = $1`,
		r.ID, r.Name, r.Metric, r.Threshold, r.DurationSec, servers, r.AllServers, channels, r.Enabled)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) DeleteRule(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM alert_rules WHERE id::text = $1`, id)
	return err
}

// ---- events ----

type AlertEvent struct {
	ID         int64      `json:"id"`
	RuleID     string     `json:"rule_id"`
	RuleName   string     `json:"rule_name"`
	ServerID   string     `json:"server_id"`
	ServerName string     `json:"server_name"`
	Metric     string     `json:"metric"`
	Value      float64    `json:"value"`
	Threshold  float64    `json:"threshold"`
	State      string     `json:"state"`
	StartedAt  time.Time  `json:"started_at"`
	ResolvedAt *time.Time `json:"resolved_at"`
}

const eventCols = `id, rule_id::text, rule_name, server_id::text, server_name, metric, value, threshold, state, started_at, resolved_at`

func scanEvents(rows pgx.Rows) ([]*AlertEvent, error) {
	defer rows.Close()
	out := []*AlertEvent{}
	for rows.Next() {
		e := &AlertEvent{}
		if err := rows.Scan(&e.ID, &e.RuleID, &e.RuleName, &e.ServerID, &e.ServerName, &e.Metric, &e.Value, &e.Threshold, &e.State, &e.StartedAt, &e.ResolvedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) OpenAlerts(ctx context.Context) ([]*AlertEvent, error) {
	rows, err := s.db.Query(ctx, `SELECT `+eventCols+` FROM alert_events WHERE state = 'firing' ORDER BY started_at DESC`)
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}

func (s *Store) ListAlertEvents(ctx context.Context, limit int) ([]*AlertEvent, error) {
	rows, err := s.db.Query(ctx, `SELECT `+eventCols+` FROM alert_events ORDER BY started_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}

// FireAlert opens an alert; it returns false if one is already open.
func (s *Store) FireAlert(ctx context.Context, e *AlertEvent) (bool, error) {
	err := s.db.QueryRow(ctx, `
		INSERT INTO alert_events (rule_id, rule_name, server_id, server_name, metric, value, threshold, state)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'firing')
		ON CONFLICT (rule_id, server_id) WHERE state = 'firing' DO NOTHING
		RETURNING id, started_at`,
		e.RuleID, e.RuleName, e.ServerID, e.ServerName, e.Metric, e.Value, e.Threshold).Scan(&e.ID, &e.StartedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) ResolveAlert(ctx context.Context, id int64, value float64) error {
	_, err := s.db.Exec(ctx, `UPDATE alert_events SET state = 'resolved', resolved_at = now(), value = $2 WHERE id = $1`, id, value)
	return err
}
