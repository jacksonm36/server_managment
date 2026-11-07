package api

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jacksonm36/servermgr/panel/internal/dns"
	"github.com/jacksonm36/servermgr/panel/internal/store"
)

// cfFake is a tiny in-memory Cloudflare: one zone, records CRUD.
type cfFake struct {
	mu   sync.Mutex
	next int
	recs map[string]map[string]any
}

func (f *cfFake) Do(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec := httptest.NewRecorder()
	reply := func(code int, v any) {
		rec.WriteHeader(code)
		_ = json.NewEncoder(rec).Encode(v)
	}
	page := map[string]any{"page": 1, "total_pages": 1}
	if r.Header.Get("Authorization") != "Bearer good" {
		reply(403, map[string]any{"success": false, "errors": []any{map[string]any{"message": "Invalid API Token"}}})
		return rec.Result(), nil
	}
	path := r.URL.Path
	switch {
	case r.Method == "GET" && strings.HasSuffix(path, "/zones"):
		reply(200, map[string]any{"result": []any{map[string]any{"id": "z1", "name": "example.com"}}, "result_info": page})
	case r.Method == "GET" && strings.HasSuffix(path, "/dns_records"):
		list := []any{}
		for _, v := range f.recs {
			list = append(list, v)
		}
		reply(200, map[string]any{"result": list, "result_info": page})
	case r.Method == "POST":
		var v map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &v)
		f.next++
		v["id"] = fmt.Sprintf("r%d", f.next)
		f.recs[v["id"].(string)] = v
		reply(200, map[string]any{"success": true, "result": v})
	case r.Method == "DELETE":
		id := path[strings.LastIndex(path, "/")+1:]
		delete(f.recs, id)
		reply(200, map[string]any{"success": true})
	default:
		reply(404, map[string]any{"success": false})
	}
	return rec.Result(), nil
}

func TestDNSAndHardeningEndToEnd(t *testing.T) {
	p := newTestPanel(t)
	ctx := context.Background()
	root, _ := p.login("root", rootPW)

	fake := &cfFake{recs: map[string]map[string]any{}}
	old := dns.HTTPClient
	dns.HTTPClient = fake
	t.Cleanup(func() { dns.HTTPClient = old })

	// A bad token is caught before anything is saved.
	code, body, _ := p.call(root, "POST", "/api/v1/dns/providers", map[string]any{"name": "CF", "type": "cloudflare", "config": map[string]string{"api_token": "bad"}})
	p.expect("bad token", code, 400)
	if !strings.Contains(fmt.Sprint(body["error"]), "Invalid API Token") {
		t.Errorf("error should carry the provider's message: %v", body)
	}
	code, prov, _ := p.call(root, "POST", "/api/v1/dns/providers", map[string]any{"name": "CF", "type": "cloudflare", "config": map[string]string{"api_token": "good"}})
	p.expect("add provider", code, 200)
	id := prov["id"].(string)
	var sealed string
	_ = p.conn.QueryRow(ctx, `SELECT config FROM dns_providers`).Scan(&sealed)
	if strings.Contains(sealed, "good") {
		t.Fatal("token stored in the clear")
	}
	// Renaming keeps the token when the field is left blank.
	code, _, _ = p.call(root, "PUT", "/api/v1/dns/providers/"+id, map[string]any{"name": "Cloudflare", "type": "cloudflare", "config": map[string]string{"api_token": ""}})
	p.expect("rename", code, 200)

	// Records.
	recPath := "/api/v1/dns/providers/" + id + "/zones/z1/records"
	code, _, _ = p.call(root, "POST", recPath, map[string]any{"record": map[string]any{"type": "TXT", "name": "@", "content": "v=spf1 -all"}})
	p.expect("create TXT", code, 204)
	code, _, _ = p.call(root, "POST", recPath, map[string]any{"record": map[string]any{"type": "BOGUS", "name": "x", "content": "y"}})
	p.expect("bad type", code, 400)
	code, recs, _ := p.call(root, "GET", recPath, nil)
	p.expect("list records", code, 200)
	if n := len(recs["records"].([]any)); n != 1 {
		t.Fatalf("records: %v", recs)
	}

	// Point a site name at a server: a CNAME there is replaced by A records.
	_, _, _ = p.call(root, "POST", recPath, map[string]any{"record": map[string]any{"type": "CNAME", "name": "shop", "content": "example.com"}})
	code, _, _ = p.call(root, "POST", "/api/v1/dns/point", map[string]any{"name": "shop.example.com", "addresses": []string{"203.0.113.7", "2001:db8::7"}})
	p.expect("point", code, 200)
	types := map[string]string{}
	for _, v := range fake.recs {
		if v["name"] == "shop.example.com" {
			types[v["type"].(string)] = fmt.Sprint(v["content"])
		}
	}
	if types["A"] != "203.0.113.7" || types["AAAA"] != "2001:db8::7" || types["CNAME"] != "" {
		t.Fatalf("after point: %v", types)
	}
	code, _, _ = p.call(root, "POST", "/api/v1/dns/point", map[string]any{"name": "shop.other.org", "addresses": []string{"203.0.113.7"}})
	p.expect("no zone", code, 400)
	code, _, _ = p.call(root, "POST", "/api/v1/dns/point", map[string]any{"name": "shop.example.com", "addresses": []string{"127.0.0.1"}})
	p.expect("loopback refused", code, 400)

	// A viewer sees DNS but can't change it.
	viewer := p.newUser("dnsviewer", "viewer")
	p.expect("viewer lists", first(p.call(viewer, "GET", recPath, nil)), 200)
	p.expect("viewer can't edit", first(p.call(viewer, "POST", recPath, map[string]any{"record": map[string]any{"type": "A", "name": "x", "content": "1.2.3.4"}})), 403)

	// Panel certificate: an uploaded one is served for its name only, and
	// the agents' address is refused.
	certPEM, keyPEM := selfSigned(t, "panel.example.com")
	code, _, _ = p.call(root, "PUT", "/api/v1/settings/panel-cert", map[string]any{"mode": "custom", "cert_pem": certPEM, "key_pem": keyPEM})
	p.expect("custom cert", code, 200)
	internal := tls.Certificate{Certificate: [][]byte{{1}}}
	get := p.api.TLSCertificate(internal)
	if c, _ := get(&tls.ClientHelloInfo{ServerName: "panel.example.com"}); c == &internal || c.Leaf == nil {
		t.Fatal("public certificate not served for its name")
	}
	if c, _ := get(&tls.ClientHelloInfo{ServerName: "10.0.1.240"}); c.Leaf != nil {
		t.Fatal("public certificate served to the agents' address")
	}
	activePanelCert.Store(nil)
	p.api.LoadPanelCert(ctx) // as after a restart
	if activePanelCert.Load() == nil {
		t.Fatal("certificate not loaded back from the database")
	}
	p.api.Cfg.PublicURL = "https://panel.example.com:8443"
	code, _, _ = p.call(root, "PUT", "/api/v1/settings/panel-cert", map[string]any{"mode": "custom", "cert_pem": certPEM, "key_pem": keyPEM})
	p.expect("agents' host refused", code, 400)
	code, _, _ = p.call(root, "PUT", "/api/v1/settings/panel-cert", map[string]any{"mode": "internal"})
	p.expect("back to internal", code, 200)
	activePanelCert.Store(nil)

	// Sessions: listed, the other one can be ended, idle ones expire.
	other, _ := p.login("root", rootPW)
	code, s, _ := p.call(root, "GET", "/api/v1/account/sessions", nil)
	p.expect("sessions", code, 200)
	list := s["sessions"].([]any)
	if len(list) < 2 {
		t.Fatalf("sessions: %v", list)
	}
	for _, x := range list {
		m := x.(map[string]any)
		if m["current"] == false {
			p.expect("end other", first(p.call(root, "DELETE", "/api/v1/account/sessions/"+m["id"].(string), nil)), 204)
		}
	}
	p.expect("ended session is gone", first(p.call(other, "GET", "/api/v1/me", nil)), 401)
	_, _ = p.conn.Exec(ctx, `UPDATE sessions SET last_used_at = now() - interval '3 hours'`)
	p.expect("idle session", first(p.call(root, "GET", "/api/v1/me", nil)), 401)
	root, _ = p.login("root", rootPW)

	// Retention removes old finished jobs and audit entries.
	_, _ = p.conn.Exec(ctx, `INSERT INTO jobs (id, server_id, op, params, status, created_at) VALUES ($1, $2, 'system.info', '{}', 'succeeded', now() - interval '400 days')`, store.NewUUID(), p.web)
	code, _, _ = p.call(root, "PUT", "/api/v1/settings/retention", map[string]any{"jobs_days": 30, "audit_days": 365, "alerts_days": 180, "runs_days": 180})
	p.expect("retention", code, 200)
	if n, err := p.api.Store.PurgeHistory(ctx); err != nil || n["jobs"] != 1 {
		t.Fatalf("purge: %v %v", n, err)
	}

	// Webhooks over plain http only go to private addresses.
	p.expect("public http webhook", first(p.call(root, "POST", "/api/v1/channels", map[string]any{"name": "h", "type": "webhook", "enabled": true, "config": map[string]string{"url": "http://hooks.example.com/x"}})), 400)
	p.expect("private http webhook", first(p.call(root, "POST", "/api/v1/channels", map[string]any{"name": "h", "type": "webhook", "enabled": true, "notify_events": true, "config": map[string]string{"url": "http://10.0.0.5/x"}})), 200)

	// Cancelling a job of an offline server says so.
	_, _ = p.conn.Exec(ctx, `INSERT INTO jobs (id, server_id, op, params, status) VALUES ('11111111-1111-1111-1111-111111111111', $1, 'packages.upgrade', '{}', 'running')`, p.web)
	p.expect("cancel offline", first(p.call(root, "POST", "/api/v1/jobs/11111111-1111-1111-1111-111111111111/cancel", nil)), 409)
	send, detach := p.api.Hub.Attach(p.web)
	defer detach()
	p.expect("cancel", first(p.call(root, "POST", "/api/v1/jobs/11111111-1111-1111-1111-111111111111/cancel", nil)), 202)
	select {
	case m := <-send:
		if m.GetJobCancel().GetJobId() != "11111111-1111-1111-1111-111111111111" {
			t.Fatalf("sent %v", m)
		}
	case <-time.After(time.Second):
		t.Fatal("no cancel sent to the agent")
	}
}

func selfSigned(t *testing.T, name string) (string, string) {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(90 * 24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kd, _ := x509.MarshalECPrivateKey(k)
	var c, kb bytes.Buffer
	_ = pem.Encode(&c, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	_ = pem.Encode(&kb, &pem.Block{Type: "EC PRIVATE KEY", Bytes: kd})
	return c.String(), kb.String()
}

// newUser creates a user with a built-in role and logs them in.
func (p *testPanel) newUser(name, role string) client {
	p.t.Helper()
	root, _ := p.login("root", rootPW)
	code, _, _ := p.call(root, "POST", "/api/v1/users", map[string]any{"username": name, "password": name + "-password-1", "role": role})
	p.expect("create "+name, code, 201)
	c, _ := p.login(name, name+"-password-1")
	return c
}
