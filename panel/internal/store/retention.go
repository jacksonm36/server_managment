package store

import (
	"context"
	"fmt"
)

// Retention is how many days of history the panel keeps (0 = forever).
type Retention struct {
	JobsDays   int `json:"jobs_days"`
	AuditDays  int `json:"audit_days"`
	AlertsDays int `json:"alerts_days"`
	RunsDays   int `json:"runs_days"`
}

// DefaultRetention applies until an admin changes it in Settings.
var DefaultRetention = Retention{JobsDays: 90, AuditDays: 365, AlertsDays: 180, RunsDays: 180}

const retentionKey = "retention"

func (s *Store) GetRetention(ctx context.Context) (Retention, error) {
	r := DefaultRetention
	err := s.GetSetting(ctx, retentionKey, &r)
	return r, err
}

func (s *Store) SetRetention(ctx context.Context, r Retention) error {
	return s.SetSetting(ctx, retentionKey, r)
}

// PurgeHistory deletes finished jobs, audit entries, resolved alerts and
// finished runs older than the retention settings.
func (s *Store) PurgeHistory(ctx context.Context) (map[string]int64, error) {
	r, err := s.GetRetention(ctx)
	if err != nil {
		return nil, err
	}
	type q struct {
		name string
		days int
		sql  string
	}
	qs := []q{
		{"jobs", r.JobsDays, `DELETE FROM jobs WHERE status IN ('succeeded','failed') AND created_at < now() - make_interval(days => $1)`},
		{"audit", r.AuditDays, `DELETE FROM audit_log WHERE ts < now() - make_interval(days => $1)`},
		{"alerts", r.AlertsDays, `DELETE FROM alert_events WHERE state = 'resolved' AND resolved_at < now() - make_interval(days => $1)`},
		{"playbook_runs", r.RunsDays, `DELETE FROM playbook_runs WHERE status <> 'running' AND created_at < now() - make_interval(days => $1)`},
		{"ansible_runs", r.RunsDays, `DELETE FROM ansible_runs WHERE status <> 'running' AND created_at < now() - make_interval(days => $1)`},
		{"tofu_runs", r.RunsDays, `DELETE FROM tofu_runs WHERE status <> 'running' AND created_at < now() - make_interval(days => $1)`},
		{"proxmox_provisions", r.RunsDays, `DELETE FROM proxmox_provisions WHERE status <> 'running' AND created_at < now() - make_interval(days => $1)`},
	}
	out := map[string]int64{}
	for _, x := range qs {
		if x.days <= 0 {
			continue
		}
		tag, err := s.db.Exec(ctx, x.sql, x.days)
		if err != nil {
			return out, fmt.Errorf("purge %s: %w", x.name, err)
		}
		out[x.name] = tag.RowsAffected()
	}
	return out, nil
}
