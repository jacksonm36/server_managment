package dns

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Cloudflare API v4 with an API token.
type cloudflare struct{ c *apiClient }

func newCloudflare(cfg map[string]string, hc httpDoer) (Provider, error) {
	return &cloudflare{c: &apiClient{
		hc:      hc,
		base:    "https://api.cloudflare.com/client/v4",
		headers: map[string]string{"Authorization": "Bearer " + strings.TrimSpace(cfg["api_token"])},
		errMsg: func(b []byte) string {
			var e struct {
				Errors []struct {
					Message string `json:"message"`
				} `json:"errors"`
			}
			if json.Unmarshal(b, &e) == nil && len(e.Errors) > 0 {
				return e.Errors[0].Message
			}
			return ""
		},
	}}, nil
}

type cfPage struct {
	ResultInfo struct {
		Page       int `json:"page"`
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

func (p *cloudflare) Zones(ctx context.Context) ([]Zone, error) {
	out := []Zone{}
	for page := 1; ; page++ {
		var res struct {
			cfPage
			Result []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"result"`
		}
		if err := p.c.do(ctx, "GET", fmt.Sprintf("/zones?per_page=50&page=%d", page), nil, &res); err != nil {
			return nil, err
		}
		for _, z := range res.Result {
			out = append(out, Zone{ID: z.ID, Name: z.Name})
		}
		if page >= res.ResultInfo.TotalPages {
			return out, nil
		}
	}
}

type cfRecord struct {
	ID       string         `json:"id,omitempty"`
	Type     string         `json:"type"`
	Name     string         `json:"name"`
	Content  string         `json:"content,omitempty"`
	TTL      int            `json:"ttl"`
	Priority *int           `json:"priority,omitempty"`
	Proxied  *bool          `json:"proxied,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
}

func (p *cloudflare) Records(ctx context.Context, zone Zone) ([]Record, error) {
	out := []Record{}
	for page := 1; ; page++ {
		var res struct {
			cfPage
			Result []cfRecord `json:"result"`
		}
		if err := p.c.do(ctx, "GET", fmt.Sprintf("/zones/%s/dns_records?per_page=500&page=%d", esc(zone.ID), page), nil, &res); err != nil {
			return nil, err
		}
		for _, r := range res.Result {
			rec := Record{ID: r.ID, Type: r.Type, Name: r.Name, Content: r.Content, TTL: r.TTL, Priority: r.Priority, Proxied: r.Proxied}
			if rec.TTL == 1 {
				rec.TTL = 0 // "automatic"
			}
			out = append(out, rec)
		}
		if page >= res.ResultInfo.TotalPages {
			sortRecords(out)
			return out, nil
		}
	}
}

// body builds the API body. SRV and CAA go in "data", which Cloudflare
// requires for them.
func (p *cloudflare) body(r Record) (cfRecord, error) {
	b := cfRecord{Type: r.Type, Name: r.Name, Content: r.Content, TTL: r.TTL, Priority: r.Priority}
	if b.TTL <= 1 {
		b.TTL = 1
	}
	switch r.Type {
	case "A", "AAAA", "CNAME":
		proxied := r.Proxied != nil && *r.Proxied
		b.Proxied = &proxied
		if proxied {
			b.TTL = 1 // proxied records are always "auto"
		}
	case "CAA":
		f := strings.Fields(r.Content)
		if len(f) < 3 {
			return b, fmt.Errorf("CAA content must be: flags tag value, e.g. 0 issue \"letsencrypt.org\"")
		}
		flags, _ := strconv.Atoi(f[0])
		b.Content = ""
		b.Data = map[string]any{"flags": flags, "tag": f[1], "value": strings.Trim(strings.Join(f[2:], " "), `"`)}
	case "SRV":
		f := strings.Fields(r.Content)
		if len(f) != 3 {
			return b, fmt.Errorf("SRV content must be: weight port target")
		}
		weight, _ := strconv.Atoi(f[0])
		port, _ := strconv.Atoi(f[1])
		prio := 10
		if r.Priority != nil {
			prio = *r.Priority
		}
		b.Content = ""
		b.Data = map[string]any{"priority": prio, "weight": weight, "port": port, "target": strings.TrimSuffix(f[2], ".")}
	}
	return b, nil
}

func (p *cloudflare) Create(ctx context.Context, zone Zone, r Record) error {
	b, err := p.body(r)
	if err != nil {
		return err
	}
	return p.c.do(ctx, "POST", "/zones/"+esc(zone.ID)+"/dns_records", b, nil)
}

func (p *cloudflare) Update(ctx context.Context, zone Zone, old, r Record) error {
	b, err := p.body(r)
	if err != nil {
		return err
	}
	return p.c.do(ctx, "PUT", "/zones/"+esc(zone.ID)+"/dns_records/"+esc(old.ID), b, nil)
}

func (p *cloudflare) Delete(ctx context.Context, zone Zone, r Record) error {
	return p.c.do(ctx, "DELETE", "/zones/"+esc(zone.ID)+"/dns_records/"+esc(r.ID), nil, nil)
}
