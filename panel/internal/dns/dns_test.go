package dns

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fake routes every provider call to a handler and records what was sent.
type fake struct {
	t     *testing.T
	calls []string
	h     func(method, path string, body []byte) (int, any)
}

func (f *fake) Do(r *http.Request) (*http.Response, error) {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}
	call := r.Method + " " + r.URL.Host + r.URL.RequestURI()
	if len(body) > 0 {
		call += " " + string(body)
	}
	f.calls = append(f.calls, call)
	status, out := f.h(r.Method, r.URL.Host+r.URL.Path, body)
	rec := httptest.NewRecorder()
	rec.WriteHeader(status)
	if out != nil {
		_ = json.NewEncoder(rec).Encode(out)
	}
	return rec.Result(), nil
}

func useFake(t *testing.T, h func(method, path string, body []byte) (int, any)) *fake {
	f := &fake{t: t, h: h}
	old := HTTPClient
	HTTPClient = f
	t.Cleanup(func() { HTTPClient = old })
	return f
}

func has(f *fake, sub string) bool {
	for _, c := range f.calls {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

var zone = Zone{ID: "z1", Name: "example.com"}

func TestValidate(t *testing.T) {
	r := Record{Type: "a", Name: "www", Content: " 1.2.3.4 "}
	if err := Validate(zone, &r); err != nil || r.Name != "www.example.com" || r.Type != "A" || r.TTL != 3600 {
		t.Fatalf("%+v %v", r, err)
	}
	r = Record{Type: "MX", Name: "@", Content: "mail.example.com"}
	if err := Validate(zone, &r); err != nil || r.Name != "example.com" || r.Priority == nil || *r.Priority != 10 {
		t.Fatalf("%+v %v", r, err)
	}
	if err := Validate(zone, &Record{Type: "PTR", Name: "x", Content: "y"}); err == nil {
		t.Fatal("PTR accepted")
	}
}

func TestWire(t *testing.T) {
	p := 5
	cases := []Record{
		{Type: "TXT", Content: `v=spf1 include:_spf.example.net "quoted" ~all`},
		{Type: "TXT", Content: strings.Repeat("k", 300)},
		{Type: "CNAME", Content: "target.example.net"},
		{Type: "MX", Content: "mx.example.net", Priority: &p},
		{Type: "SRV", Content: "1 443 sip.example.net", Priority: &p},
		{Type: "A", Content: "1.2.3.4"},
	}
	for _, c := range cases {
		w, err := toWire(c)
		if err != nil {
			t.Fatal(err)
		}
		back := fromWire(c.Type, w)
		if back.Content != c.Content || (c.Priority != nil && *back.Priority != *c.Priority) {
			t.Errorf("%s: %q -> %q -> %+v", c.Type, c.Content, w, back)
		}
	}
	if w, _ := toWire(Record{Type: "TXT", Content: strings.Repeat("k", 300)}); strings.Count(w, `"`) != 4 {
		t.Errorf("long TXT not split: %s", w)
	}
}

func TestCloudflare(t *testing.T) {
	f := useFake(t, func(method, path string, body []byte) (int, any) {
		switch {
		case method == "GET" && strings.HasSuffix(path, "/zones"):
			return 200, map[string]any{"success": true, "result": []any{map[string]any{"id": "z1", "name": "example.com"}}, "result_info": map[string]any{"page": 1, "total_pages": 1}}
		case method == "GET" && strings.HasSuffix(path, "/dns_records"):
			return 200, map[string]any{"success": true, "result": []any{
				map[string]any{"id": "r1", "type": "A", "name": "www.example.com", "content": "1.2.3.4", "ttl": 1, "proxied": true},
			}, "result_info": map[string]any{"page": 1, "total_pages": 1}}
		case method == "POST" && strings.HasSuffix(path, "/dns_records"):
			return 200, map[string]any{"success": true}
		case method == "DELETE":
			return 403, map[string]any{"success": false, "errors": []any{map[string]any{"message": "Authentication error"}}}
		}
		return 404, nil
	})
	p, err := New("cloudflare", map[string]string{"api_token": "tok"})
	if err != nil {
		t.Fatal(err)
	}
	zs, err := p.Zones(context.Background())
	if err != nil || len(zs) != 1 || zs[0].Name != "example.com" {
		t.Fatalf("zones %v %v", zs, err)
	}
	rs, err := p.Records(context.Background(), zone)
	if err != nil || len(rs) != 1 || rs[0].TTL != 0 || rs[0].Proxied == nil || !*rs[0].Proxied {
		t.Fatalf("records %+v %v", rs, err)
	}
	yes := true
	if err := p.Create(context.Background(), zone, Record{Type: "A", Name: "api.example.com", Content: "5.6.7.8", TTL: 300, Proxied: &yes}); err != nil {
		t.Fatal(err)
	}
	if !has(f, `"proxied":true`) || !has(f, `"ttl":1`) {
		t.Errorf("create body: %v", f.calls)
	}
	if err := p.Delete(context.Background(), zone, rs[0]); err == nil || !strings.Contains(err.Error(), "Authentication error") {
		t.Errorf("want the API's message, got %v", err)
	}
}

func TestHetznerRRSets(t *testing.T) {
	f := useFake(t, func(method, path string, body []byte) (int, any) {
		switch {
		case method == "GET" && strings.HasSuffix(path, "/rrsets"):
			return 200, map[string]any{"rrsets": []any{
				map[string]any{"name": "@", "type": "TXT", "ttl": 3600, "records": []any{map[string]any{"value": `"v=spf1 -all"`}}},
				map[string]any{"name": "www", "type": "A", "ttl": nil, "records": []any{map[string]any{"value": "1.2.3.4"}, map[string]any{"value": "1.2.3.5"}}},
			}, "meta": map[string]any{"pagination": map[string]any{"next_page": nil}}}
		}
		return 201, map[string]any{}
	})
	p, _ := New("hetzner", map[string]string{"api_token": "tok"})
	rs, err := p.Records(context.Background(), zone)
	if err != nil || len(rs) != 3 || rs[0].Content != "v=spf1 -all" || rs[0].Name != "example.com" {
		t.Fatalf("%+v %v", rs, err)
	}
	// Adding a value to an existing set replaces the set's records.
	if err := p.Create(context.Background(), zone, Record{Type: "A", Name: "www.example.com", Content: "9.9.9.9", TTL: 300}); err != nil {
		t.Fatal(err)
	}
	if !has(f, `/rrsets/www/A/actions/set_records {"records":[{"value":"1.2.3.4"},{"value":"1.2.3.5"},{"value":"9.9.9.9"}]}`) {
		t.Errorf("calls: %v", f.calls)
	}
	// A new name creates a set.
	if err := p.Create(context.Background(), zone, Record{Type: "CNAME", Name: "blog.example.com", Content: "example.com", TTL: 300}); err != nil {
		t.Fatal(err)
	}
	if !has(f, `POST api.hetzner.cloud/v1/zones/z1/rrsets {"name":"blog","records":[{"value":"example.com."}],"ttl":300,"type":"CNAME"}`) {
		t.Errorf("calls: %v", f.calls)
	}
	// Removing the only value deletes the set.
	if err := p.Delete(context.Background(), zone, rs[0]); err != nil {
		t.Fatal(err)
	}
	if !has(f, "DELETE api.hetzner.cloud/v1/zones/z1/rrsets/@/TXT") {
		t.Errorf("calls: %v", f.calls)
	}
}

func TestDeSECAndPowerDNS(t *testing.T) {
	f := useFake(t, func(method, path string, body []byte) (int, any) {
		if method == "GET" && strings.Contains(path, "desec.io") {
			return 200, []any{}
		}
		if method == "GET" {
			return 200, map[string]any{"rrsets": []any{}}
		}
		return 204, nil
	})
	d, _ := New("desec", map[string]string{"api_token": "tok"})
	if err := d.Create(context.Background(), zone, Record{Type: "A", Name: "example.com", Content: "1.2.3.4", TTL: 60}); err != nil {
		t.Fatal(err)
	}
	if !has(f, `PATCH desec.io/api/v1/domains/z1/rrsets/ [{"subname":"","type":"A","ttl":3600,"records":["1.2.3.4"]}]`) {
		t.Errorf("deSEC: min TTL and subname: %v", f.calls)
	}
	p, err := New("powerdns", map[string]string{"url": "http://ns1.example.net:8081", "api_key": "k"})
	if err != nil {
		t.Fatal(err)
	}
	pz := Zone{ID: "example.com.", Name: "example.com"}
	if err := p.Create(context.Background(), pz, Record{Type: "MX", Name: "example.com", Content: "mx.example.com", TTL: 300}); err != nil {
		t.Fatal(err)
	}
	if !has(f, `PATCH ns1.example.net:8081/api/v1/servers/localhost/zones/example.com. {"rrsets":[{"changetype":"REPLACE","name":"example.com.","records":[{"content":"10 mx.example.com.","disabled":false}],"ttl":300,"type":"MX"}]}`) {
		t.Errorf("PowerDNS: %v", f.calls)
	}
}

func TestDigitalOcean(t *testing.T) {
	f := useFake(t, func(method, path string, body []byte) (int, any) {
		if method == "GET" {
			return 200, map[string]any{"domain_records": []any{
				map[string]any{"id": 7, "type": "CNAME", "name": "www", "data": "@", "ttl": 1800},
				map[string]any{"id": 8, "type": "MX", "name": "@", "data": "mx.example.com.", "ttl": 1800, "priority": 10},
			}, "links": map[string]any{}}
		}
		return 201, map[string]any{}
	})
	p, _ := New("digitalocean", map[string]string{"api_token": "tok"})
	rs, err := p.Records(context.Background(), Zone{ID: "example.com", Name: "example.com"})
	if err != nil || len(rs) != 2 || rs[1].Content != "example.com" || rs[0].Content != "mx.example.com" || rs[0].Name != "example.com" {
		t.Fatalf("%+v %v", rs, err)
	}
	if err := p.Create(context.Background(), Zone{ID: "example.com", Name: "example.com"}, Record{Type: "CNAME", Name: "app.example.com", Content: "example.com", TTL: 300}); err != nil {
		t.Fatal(err)
	}
	if !has(f, `"name":"app"`) || !has(f, `"data":"example.com."`) {
		t.Errorf("DO body: %v", f.calls)
	}
}
