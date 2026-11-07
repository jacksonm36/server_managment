package dns

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Hetzner Cloud DNS (api.hetzner.cloud/v1/zones), which replaced the old
// dns.hetzner.com API. Record sets are addressed by relative name and type.
type hetzner struct{ c *apiClient }

func newHetzner(cfg map[string]string, hc httpDoer) (Provider, error) {
	return &rrsetProvider{minTTL: 60, b: &hetzner{c: &apiClient{
		hc:      hc,
		base:    "https://api.hetzner.cloud/v1",
		headers: map[string]string{"Authorization": "Bearer " + strings.TrimSpace(cfg["api_token"])},
		errMsg: func(b []byte) string {
			var e struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			_ = json.Unmarshal(b, &e)
			return e.Error.Message
		},
	}}}, nil
}

type hzMeta struct {
	Meta struct {
		Pagination struct {
			NextPage *int `json:"next_page"`
		} `json:"pagination"`
	} `json:"meta"`
}

func (h *hetzner) zones(ctx context.Context) ([]Zone, error) {
	out := []Zone{}
	for page := 1; ; page++ {
		var res struct {
			hzMeta
			Zones []struct {
				ID   json.Number `json:"id"`
				Name string      `json:"name"`
			} `json:"zones"`
		}
		if err := h.c.do(ctx, "GET", fmt.Sprintf("/zones?per_page=50&page=%d", page), nil, &res); err != nil {
			return nil, err
		}
		for _, z := range res.Zones {
			out = append(out, Zone{ID: z.ID.String(), Name: strings.TrimSuffix(z.Name, ".")})
		}
		if res.Meta.Pagination.NextPage == nil {
			return out, nil
		}
	}
}

type hzRRSet struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	TTL     *int   `json:"ttl"`
	Records []struct {
		Value string `json:"value"`
	} `json:"records"`
}

func (h *hetzner) rrsets(ctx context.Context, zone Zone) ([]rrset, error) {
	out := []rrset{}
	for page := 1; ; page++ {
		var res struct {
			hzMeta
			RRSets []hzRRSet `json:"rrsets"`
		}
		if err := h.c.do(ctx, "GET", fmt.Sprintf("/zones/%s/rrsets?per_page=100&page=%d", esc(zone.ID), page), nil, &res); err != nil {
			return nil, err
		}
		for _, s := range res.RRSets {
			set := rrset{Name: fqdn(zone, s.Name), Type: s.Type}
			if s.TTL != nil {
				set.TTL = *s.TTL
			}
			for _, r := range s.Records {
				set.Values = append(set.Values, r.Value)
			}
			out = append(out, set)
		}
		if res.Meta.Pagination.NextPage == nil {
			return out, nil
		}
	}
}

func hzRecords(vals []string) []map[string]string {
	out := make([]map[string]string, len(vals))
	for i, v := range vals {
		out[i] = map[string]string{"value": v}
	}
	return out
}

func (h *hetzner) put(ctx context.Context, zone Zone, s rrset, exists bool) error {
	name := relative(zone, s.Name)
	if !exists {
		return h.c.do(ctx, "POST", "/zones/"+esc(zone.ID)+"/rrsets",
			map[string]any{"name": name, "type": s.Type, "ttl": s.TTL, "records": hzRecords(s.Values)}, nil)
	}
	base := "/zones/" + esc(zone.ID) + "/rrsets/" + esc(name) + "/" + esc(s.Type) + "/actions/"
	if err := h.c.do(ctx, "POST", base+"set_records", map[string]any{"records": hzRecords(s.Values)}, nil); err != nil {
		return err
	}
	return h.c.do(ctx, "POST", base+"change_ttl", map[string]any{"ttl": s.TTL}, nil)
}

func (h *hetzner) del(ctx context.Context, zone Zone, name, typ string) error {
	return h.c.do(ctx, "DELETE", "/zones/"+esc(zone.ID)+"/rrsets/"+esc(relative(zone, name))+"/"+esc(typ), nil, nil)
}
