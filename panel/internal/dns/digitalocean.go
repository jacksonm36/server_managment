package dns

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// DigitalOcean Domains API with a personal access token. Record names are
// relative ("@" at the apex) and targets of CNAME/MX/SRV end with a dot.
type digitalOcean struct{ c *apiClient }

func newDigitalOcean(cfg map[string]string, hc httpDoer) (Provider, error) {
	return &digitalOcean{c: &apiClient{
		hc:      hc,
		base:    "https://api.digitalocean.com/v2",
		headers: map[string]string{"Authorization": "Bearer " + strings.TrimSpace(cfg["api_token"])},
		errMsg: func(b []byte) string {
			var e struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(b, &e)
			return e.Message
		},
	}}, nil
}

type doLinks struct {
	Links struct {
		Pages struct {
			Next string `json:"next"`
		} `json:"pages"`
	} `json:"links"`
}

func (p *digitalOcean) Zones(ctx context.Context) ([]Zone, error) {
	out := []Zone{}
	for page := 1; ; page++ {
		var res struct {
			doLinks
			Domains []struct {
				Name string `json:"name"`
			} `json:"domains"`
		}
		if err := p.c.do(ctx, "GET", fmt.Sprintf("/domains?per_page=200&page=%d", page), nil, &res); err != nil {
			return nil, err
		}
		for _, d := range res.Domains {
			out = append(out, Zone{ID: d.Name, Name: d.Name})
		}
		if res.Links.Pages.Next == "" {
			return out, nil
		}
	}
}

type doRecord struct {
	ID       int    `json:"id,omitempty"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	Data     string `json:"data"`
	TTL      int    `json:"ttl"`
	Priority *int   `json:"priority,omitempty"`
	Port     *int   `json:"port,omitempty"`
	Weight   *int   `json:"weight,omitempty"`
	Flags    *int   `json:"flags,omitempty"`
	Tag      string `json:"tag,omitempty"`
}

// dotted reports whether the type's data is a host name DO wants with a dot.
func dotted(t string) bool { return t == "CNAME" || t == "MX" || t == "NS" || t == "SRV" }

func (p *digitalOcean) Records(ctx context.Context, zone Zone) ([]Record, error) {
	out := []Record{}
	for page := 1; ; page++ {
		var res struct {
			doLinks
			Records []doRecord `json:"domain_records"`
		}
		if err := p.c.do(ctx, "GET", fmt.Sprintf("/domains/%s/records?per_page=200&page=%d", esc(zone.ID), page), nil, &res); err != nil {
			return nil, err
		}
		for _, r := range res.Records {
			content := r.Data
			if dotted(r.Type) && content != "@" {
				content = strings.TrimSuffix(content, ".")
			}
			if content == "@" {
				content = zone.Name
			}
			switch r.Type {
			case "SRV":
				content = fmt.Sprintf("%d %d %s", deref(r.Weight), deref(r.Port), content)
			case "CAA":
				content = fmt.Sprintf("%d %s %q", deref(r.Flags), r.Tag, content)
			}
			rec := Record{ID: strconv.Itoa(r.ID), Type: r.Type, Name: fqdn(zone, r.Name), Content: content, TTL: r.TTL}
			if r.Type == "MX" || r.Type == "SRV" {
				rec.Priority = r.Priority
			}
			out = append(out, rec)
		}
		if res.Links.Pages.Next == "" {
			sortRecords(out)
			return out, nil
		}
	}
}

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func (p *digitalOcean) body(zone Zone, r Record) (doRecord, error) {
	b := doRecord{Type: r.Type, Name: relative(zone, r.Name), Data: r.Content, TTL: max(r.TTL, 30), Priority: r.Priority}
	switch r.Type {
	case "SRV":
		f := strings.Fields(r.Content)
		if len(f) != 3 {
			return b, fmt.Errorf("SRV content must be: weight port target")
		}
		w, _ := strconv.Atoi(f[0])
		port, _ := strconv.Atoi(f[1])
		b.Weight, b.Port, b.Data = &w, &port, f[2]
	case "CAA":
		f := strings.Fields(r.Content)
		if len(f) < 3 {
			return b, fmt.Errorf("CAA content must be: flags tag value")
		}
		flags, _ := strconv.Atoi(f[0])
		b.Flags, b.Tag, b.Data = &flags, f[1], strings.Trim(strings.Join(f[2:], " "), `"`)
	}
	if dotted(r.Type) && !strings.HasSuffix(b.Data, ".") {
		b.Data += "."
	}
	return b, nil
}

func (p *digitalOcean) Create(ctx context.Context, zone Zone, r Record) error {
	b, err := p.body(zone, r)
	if err != nil {
		return err
	}
	return p.c.do(ctx, "POST", "/domains/"+esc(zone.ID)+"/records", b, nil)
}

func (p *digitalOcean) Update(ctx context.Context, zone Zone, old, r Record) error {
	b, err := p.body(zone, r)
	if err != nil {
		return err
	}
	return p.c.do(ctx, "PUT", "/domains/"+esc(zone.ID)+"/records/"+esc(old.ID), b, nil)
}

func (p *digitalOcean) Delete(ctx context.Context, zone Zone, r Record) error {
	return p.c.do(ctx, "DELETE", "/domains/"+esc(zone.ID)+"/records/"+esc(r.ID), nil, nil)
}
