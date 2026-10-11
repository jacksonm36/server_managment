package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const vaultSetting = "vault"

// sealedColumns lists every column holding a sealed secret that the vault
// protects: table, key column, value column.
var sealedColumns = [][3]string{
	{"notify_channels", "id", "config"},
	{"proxmox_clusters", "id", "token_secret"},
	{"tofu_workspaces", "id", "vars"},
	{"tofu_states", "workspace_id", "state"},
	{"tofu_state_versions", "id", "state"},
	{"ssh_keys", "id", "public_key"},
}

// VaultHeader loads the stored vault header into v; ok is false when the
// vault was never set up.
func (s *Store) VaultHeader(ctx context.Context, v any) (ok bool, err error) {
	var raw []byte
	err = s.db.QueryRow(ctx, `SELECT value FROM settings WHERE key = $1`, vaultSetting).Scan(&raw)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(raw, v)
}

func (s *Store) SaveVaultHeader(ctx context.Context, v any) error {
	return s.SetSetting(ctx, vaultSetting, v)
}

// SetupVault stores a new vault header and re-seals every existing secret
// with reseal, in one transaction: either all secrets move into the vault or
// nothing changes. It fails if a vault already exists.
func (s *Store) SetupVault(ctx context.Context, header any, reseal func(string) (string, error)) (int, error) {
	raw, err := json.Marshal(header)
	if err != nil {
		return 0, err
	}
	n := 0
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO settings (key, value) VALUES ($1, $2) ON CONFLICT DO NOTHING`, vaultSetting, raw)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrExists
		}
		for _, c := range sealedColumns {
			table, key, col := c[0], c[1], c[2]
			rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT %s::text, %s FROM %s WHERE %s <> '' FOR UPDATE`, key, col, table, col))
			if err != nil {
				return err
			}
			type kv struct{ id, val string }
			var vals []kv
			for rows.Next() {
				var r kv
				if err := rows.Scan(&r.id, &r.val); err != nil {
					rows.Close()
					return err
				}
				vals = append(vals, r)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			for _, r := range vals {
				sealed, err := reseal(r.val)
				if err != nil {
					return fmt.Errorf("%s.%s: %w", table, col, err)
				}
				if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s = $2 WHERE %s::text = $1`, table, col, key), r.id, sealed); err != nil {
					return err
				}
				n++
			}
		}
		return nil
	})
	return n, err
}
