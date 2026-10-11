package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type SSHKey struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	Bits        int       `json:"bits"`
	Fingerprint string    `json:"fingerprint"`
	Comment     string    `json:"comment"`
	Source      string    `json:"source"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	// Sealed is the vault-sealed authorized_keys line.
	Sealed string `json:"-"`
}

const sshKeyCols = `id, name, key_type, bits, fingerprint, comment, source, created_by, created_at, public_key`

func scanSSHKey(row pgx.Row) (*SSHKey, error) {
	k := &SSHKey{}
	err := row.Scan(&k.ID, &k.Name, &k.Type, &k.Bits, &k.Fingerprint, &k.Comment, &k.Source, &k.CreatedBy, &k.CreatedAt, &k.Sealed)
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	return k, err
}

func (s *Store) ListSSHKeys(ctx context.Context) ([]*SSHKey, error) {
	rows, err := s.db.Query(ctx, `SELECT `+sshKeyCols+` FROM ssh_keys ORDER BY lower(name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SSHKey{}
	for rows.Next() {
		k, err := scanSSHKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) GetSSHKey(ctx context.Context, id int64) (*SSHKey, error) {
	return scanSSHKey(s.db.QueryRow(ctx, `SELECT `+sshKeyCols+` FROM ssh_keys WHERE id = $1`, id))
}

// CreateSSHKey adds a key; ErrExists means the name or the key is taken.
func (s *Store) CreateSSHKey(ctx context.Context, k *SSHKey) error {
	err := s.db.QueryRow(ctx, `
		INSERT INTO ssh_keys (name, key_type, bits, fingerprint, comment, public_key, source, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id, created_at`,
		k.Name, k.Type, k.Bits, k.Fingerprint, k.Comment, k.Sealed, k.Source, k.CreatedBy).Scan(&k.ID, &k.CreatedAt)
	if isUnique(err) {
		return ErrExists
	}
	return err
}

func (s *Store) RenameSSHKey(ctx context.Context, id int64, name string) error {
	tag, err := s.db.Exec(ctx, `UPDATE ssh_keys SET name = $2 WHERE id = $1`, id, name)
	if isUnique(err) {
		return ErrExists
	}
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) DeleteSSHKey(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM ssh_keys WHERE id = $1`, id)
	return err
}
