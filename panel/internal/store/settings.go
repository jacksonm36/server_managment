package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

// GetSetting decodes a setting into v; it leaves v untouched if unset.
func (s *Store) GetSetting(ctx context.Context, key string, v any) error {
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT value FROM settings WHERE key = $1`, key).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func (s *Store) SetSetting(ctx context.Context, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, key, raw)
	return err
}

// ClaimBaseline returns the baseline playbook for a server that has not had
// one applied yet and marks it applied, atomically, so it runs only once.
func (s *Store) ClaimBaseline(ctx context.Context, serverID string) (playbookID string, ok bool, err error) {
	var pb *string
	err = s.db.QueryRow(ctx, `
		WITH claimed AS (
			UPDATE servers SET baseline_applied_at = now()
			WHERE id::text = $1 AND baseline_applied_at IS NULL
			RETURNING id
		)
		SELECT t.playbook_id::text FROM claimed c
		LEFT JOIN join_tokens t ON t.server_id = c.id`, serverID).Scan(&pb)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil || pb == nil {
		return "", false, err
	}
	return *pb, true, nil
}
