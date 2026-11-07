package dns

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

// PowerDNS Authoritative HTTP API. Names are absolute with a trailing dot.
type powerDNS struct{ c *apiClient }

func newPowerDNS(cfg map[string]string, hc httpDoer) (Provider, error) {
	u, err := url.Parse(strings.TrimSpace(cfg["url"]))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("API URL must be http(s)://host:port")
	}
	sid := strings.TrimSpace(cfg["server_id"])
	if sid == "" {
		sid = "localhost"
	}
	return &rrsetProvider{minTTL: 1, b: &powerDNS{c: &apiClient{
		hc:      hc,
		base:    strings.TrimRight(u.String(), "/") + "/api/v1/servers/" + url.PathEscape(sid),
		headers: map[string]string{"X-API-Key": strings.TrimSpace(cfg["api_key"])},
		errMsg: func(b []byte) string {
			var e struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(b, &e)
			return e.Error
		},
	}}}, nil
}

func (p *powerDNS) zones(ctx context.Context) ([]Zone, error) {
	var res []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := p.c.do(ctx, "GET", "/zones", nil, &res); err != nil {
		return nil, err
	}
	out := make([]Zone, 0, len(res))
	for _, z := range res {
		out = append(out, Zone{ID: z.ID, Name: strings.TrimSuffix(z.Name, ".")})
	}
	return out, nil
}

type pdnsRRSet struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	TTL        int    `json:"ttl,omitempty"`
	ChangeType string `json:"changetype,omitempty"`
	Records    []struct {
		Content  string `json:"content"`
		Disabled bool   `json:"disabled"`
	} `json:"records"`
}

func (p *powerDNS) rrsets(ctx context.Context, zone Zone) ([]rrset, error) {
	var res struct {
		RRSets []pdnsRRSet `json:"rrsets"`
	}
	if err := p.c.do(ctx, "GET", "/zones/"+esc(zone.ID), nil, &res); err != nil {
		return nil, err
	}
	out := []rrset{}
	for _, s := range res.RRSets {
		if s.Type == "SOA" {
			continue
		}
		set := rrset{Name: strings.TrimSuffix(s.Name, "."), Type: s.Type, TTL: s.TTL}
		for _, r := range s.Records {
			if !r.Disabled {
				set.Values = append(set.Values, r.Content)
			}
		}
		if len(set.Values) > 0 {
			out = append(out, set)
		}
	}
	return out, nil
}

func (p *powerDNS) patch(ctx context.Context, zone Zone, set map[string]any) error {
	return p.c.do(ctx, "PATCH", "/zones/"+esc(zone.ID), map[string]any{"rrsets": []any{set}}, nil)
}

func (p *powerDNS) put(ctx context.Context, zone Zone, s rrset, _ bool) error {
	recs := make([]map[string]any, len(s.Values))
	for i, v := range s.Values {
		recs[i] = map[string]any{"content": v, "disabled": false}
	}
	return p.patch(ctx, zone, map[string]any{"name": s.Name + ".", "type": s.Type, "ttl": s.TTL, "changetype": "REPLACE", "records": recs})
}

func (p *powerDNS) del(ctx context.Context, zone Zone, name, typ string) error {
	return p.patch(ctx, zone, map[string]any{"name": name + ".", "type": typ, "changetype": "DELETE"})
}
