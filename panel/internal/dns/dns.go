// Package dns talks to DNS hosting APIs (Cloudflare, Hetzner, DigitalOcean,
// deSEC, PowerDNS) through one small interface: list zones, list records,
// and create, update or delete a record.
package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Zone is a domain hosted at a provider.
type Zone struct {
	ID   string `json:"id"`
	Name string `json:"name"` // without the trailing dot
}

// Record is one DNS record. Name is the full name without the trailing dot
// (the zone apex is the zone name). Providers that store record sets
// (Hetzner, deSEC, PowerDNS) get a synthetic ID per value.
type Record struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`
	Priority *int   `json:"priority,omitempty"` // MX and SRV
	Proxied  *bool  `json:"proxied,omitempty"`  // Cloudflare only
}

// Provider is a DNS hosting account.
type Provider interface {
	Zones(ctx context.Context) ([]Zone, error)
	Records(ctx context.Context, zone Zone) ([]Record, error)
	Create(ctx context.Context, zone Zone, r Record) error
	Update(ctx context.Context, zone Zone, old, r Record) error
	Delete(ctx context.Context, zone Zone, r Record) error
}

// Field describes one setting of a provider type, for the UI form.
type Field struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Secret      bool   `json:"secret,omitempty"`
	Optional    bool   `json:"optional,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Help        string `json:"help,omitempty"`
}

// Type is a supported provider.
type Type struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Fields  []Field `json:"fields"`
	Proxy   bool    `json:"proxy,omitempty"`   // has a proxied (orange cloud) flag
	MinTTL  int     `json:"min_ttl,omitempty"` // provider minimum
	DocsURL string  `json:"docs_url,omitempty"`
	new     func(cfg map[string]string, hc httpDoer) (Provider, error)
}

// Types lists the supported providers in the order the UI shows them.
var Types = []Type{
	{ID: "cloudflare", Name: "Cloudflare", Proxy: true, MinTTL: 60, DocsURL: "https://dash.cloudflare.com/profile/api-tokens",
		Fields: []Field{{Key: "api_token", Label: "API token", Secret: true, Help: "Create a token with Zone → DNS → Edit (and Zone → Zone → Read) for the zones to manage."}},
		new:    newCloudflare},
	{ID: "hetzner", Name: "Hetzner (Cloud DNS)", MinTTL: 60, DocsURL: "https://console.hetzner.com/",
		Fields: []Field{{Key: "api_token", Label: "API token", Secret: true, Help: "A Read & Write token of the Hetzner Cloud project that holds the zones."}},
		new:    newHetzner},
	{ID: "digitalocean", Name: "DigitalOcean", MinTTL: 30, DocsURL: "https://cloud.digitalocean.com/account/api/tokens",
		Fields: []Field{{Key: "api_token", Label: "API token", Secret: true, Help: "A personal access token with the domain read and write scopes."}},
		new:    newDigitalOcean},
	{ID: "desec", Name: "deSEC", MinTTL: 3600, DocsURL: "https://desec.io/tokens",
		Fields: []Field{{Key: "api_token", Label: "API token", Secret: true}},
		new:    newDeSEC},
	{ID: "powerdns", Name: "PowerDNS (self-hosted)", MinTTL: 1,
		Fields: []Field{
			{Key: "url", Label: "API URL", Placeholder: "https://ns1.example.com:8081", Help: "The PowerDNS Authoritative HTTP API (webserver + api=yes)."},
			{Key: "api_key", Label: "API key", Secret: true},
			{Key: "server_id", Label: "Server ID", Optional: true, Placeholder: "localhost"},
		},
		new: newPowerDNS},
}

// TypeByID finds a provider type.
func TypeByID(id string) (Type, bool) {
	for _, t := range Types {
		if t.ID == id {
			return t, true
		}
	}
	return Type{}, false
}

// New builds a provider from its type and settings.
func New(typ string, cfg map[string]string) (Provider, error) {
	t, ok := TypeByID(typ)
	if !ok {
		return nil, fmt.Errorf("unknown DNS provider %q", typ)
	}
	for _, f := range t.Fields {
		if !f.Optional && strings.TrimSpace(cfg[f.Key]) == "" {
			return nil, fmt.Errorf("%s is required", f.Label)
		}
	}
	return t.new(cfg, HTTPClient)
}

// RecordTypes are the types the editor offers.
var RecordTypes = []string{"A", "AAAA", "CNAME", "MX", "TXT", "SRV", "CAA", "NS"}

// Validate checks a record before it is sent to a provider.
func Validate(zone Zone, r *Record) error {
	r.Type = strings.ToUpper(strings.TrimSpace(r.Type))
	r.Name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(r.Name)), ".")
	r.Content = strings.TrimSpace(r.Content)
	known := false
	for _, t := range RecordTypes {
		known = known || t == r.Type
	}
	if !known {
		return fmt.Errorf("record type must be one of %s", strings.Join(RecordTypes, ", "))
	}
	if r.Name == "" || r.Name == "@" {
		r.Name = zone.Name
	}
	if r.Name != zone.Name && !strings.HasSuffix(r.Name, "."+zone.Name) {
		r.Name = r.Name + "." + zone.Name // a relative name
	}
	if len(r.Name) > 253 || strings.ContainsAny(r.Name, " /\\\"'") {
		return errors.New("invalid record name")
	}
	if r.Content == "" || len(r.Content) > 4096 {
		return errors.New("record content is required")
	}
	if r.TTL == 0 {
		r.TTL = 3600
	}
	if r.TTL < 1 || r.TTL > 86400*7 {
		return errors.New("TTL must be between 1 second and 7 days")
	}
	if (r.Type == "MX" || r.Type == "SRV") && r.Priority == nil {
		p := 10
		r.Priority = &p
	}
	if r.Type != "MX" && r.Type != "SRV" {
		r.Priority = nil
	}
	return nil
}

// relative turns a full name into the zone-relative form ("@" at the apex).
func relative(zone Zone, name string) string {
	if name == zone.Name {
		return "@"
	}
	return strings.TrimSuffix(name, "."+zone.Name)
}

func fqdn(zone Zone, rel string) string {
	rel = strings.TrimSuffix(rel, ".")
	if rel == "" || rel == "@" {
		return zone.Name
	}
	if rel == zone.Name || strings.HasSuffix(rel, "."+zone.Name) {
		return rel
	}
	return rel + "." + zone.Name
}

// sortRecords orders records by name, then type, then content.
func sortRecords(rs []Record) {
	sort.Slice(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		return a.Content < b.Content
	})
}

// ---- HTTP helper ----

// httpDoer is *http.Client, or a fake in tests.
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// HTTPClient is used for every provider call; tests replace it.
var HTTPClient httpDoer = &http.Client{Timeout: 30 * time.Second}

type apiClient struct {
	hc      httpDoer
	base    string
	headers map[string]string
	// errMsg extracts a readable message from an error body.
	errMsg func(body []byte) string
}

// APIError is a non-2xx answer from a provider.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("DNS provider answered HTTP %d", e.Status)
	}
	return fmt.Sprintf("DNS provider answered HTTP %d: %s", e.Status, e.Message)
}

func (c *apiClient) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "servermgr")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := ""
		if c.errMsg != nil {
			msg = c.errMsg(raw)
		}
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
			if len(msg) > 300 {
				msg = msg[:300]
			}
		}
		return &APIError{Status: resp.StatusCode, Message: msg}
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func esc(s string) string { return url.PathEscape(s) }
