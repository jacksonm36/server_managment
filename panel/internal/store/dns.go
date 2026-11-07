package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// DNSProvider is a DNS hosting account. Config is sealed.
type DNSProvider struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Config    string    `json:"-"`
	Zones     []string  `json:"zones"`
	LastError string    `json:"last_error"`
	CreatedAt time.Time `json:"created_at"`
}

const dnsCols = `id::text, name, type, config, zones, last_error, created_at`

func scanDNS(row pgx.Row) (*DNSProvider, error) {
	p := &DNSProvider{}
	var zones []byte
	err := row.Scan(&p.ID, &p.Name, &p.Type, &p.Config, &zones, &p.LastError, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err == nil {
		_ = json.Unmarshal(zones, &p.Zones)
	}
	if p.Zones == nil {
		p.Zones = []string{}
	}
	return p, err
}

func (s *Store) ListDNSProviders(ctx context.Context) ([]*DNSProvider, error) {
	rows, err := s.db.Query(ctx, `SELECT `+dnsCols+` FROM dns_providers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*DNSProvider{}
	for rows.Next() {
		p, err := scanDNS(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetDNSProvider(ctx context.Context, id string) (*DNSProvider, error) {
	return scanDNS(s.db.QueryRow(ctx, `SELECT `+dnsCols+` FROM dns_providers WHERE id::text = $1`, id))
}

func (s *Store) SaveDNSProvider(ctx context.Context, p *DNSProvider) error {
	zones, _ := json.Marshal(orEmptyStrings(p.Zones))
	if p.ID == "" {
		p.ID = NewUUID()
		return s.db.QueryRow(ctx, `INSERT INTO dns_providers (id, name, type, config, zones) VALUES ($1, $2, $3, $4, $5) RETURNING created_at`,
			p.ID, p.Name, p.Type, p.Config, zones).Scan(&p.CreatedAt)
	}
	tag, err := s.db.Exec(ctx, `UPDATE dns_providers SET name = $2, type = $3, config = $4, zones = $5, last_error = '', updated_at = now() WHERE id::text = $1`,
		p.ID, p.Name, p.Type, p.Config, zones)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// SetDNSZones records the zones last seen at a provider, or its error.
func (s *Store) SetDNSZones(ctx context.Context, id string, zones []string, lastError string) error {
	z, _ := json.Marshal(orEmptyStrings(zones))
	if lastError != "" {
		_, err := s.db.Exec(ctx, `UPDATE dns_providers SET last_error = $2 WHERE id::text = $1`, id, lastError)
		return err
	}
	_, err := s.db.Exec(ctx, `UPDATE dns_providers SET zones = $2, last_error = '' WHERE id::text = $1`, id, z)
	return err
}

func (s *Store) DeleteDNSProvider(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM dns_providers WHERE id::text = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func orEmptyStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
