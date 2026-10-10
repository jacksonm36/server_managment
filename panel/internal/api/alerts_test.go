package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jacksonm36/servermgr/panel/internal/hub"
)

func samples(now time.Time, cpus ...float64) []hub.Metric {
	out := []hub.Metric{}
	for i, c := range cpus {
		ts := now.Add(-time.Duration(len(cpus)-1-i) * 5 * time.Second)
		out = append(out, hub.Metric{TS: ts.UnixMilli(), CPU: c, MemTotal: 100, MemUsed: 50})
	}
	return out
}

func TestEvalMetric(t *testing.T) {
	now := time.Now()
	dur := 30 * time.Second
	// 8 samples = 35 s of data: covers the 30 s window.
	if b, v, _ := evalMetric(samples(now, 95, 95, 95, 95, 95, 95, 95, 96), "cpu", 90, dur, now); !b || v != 96 {
		t.Errorf("sustained high CPU not breached: %v %v", b, v)
	}
	if b, _, _ := evalMetric(samples(now, 95, 95, 95, 95, 40, 95, 95, 96), "cpu", 90, dur, now); b {
		t.Error("a dip inside the window should not breach")
	}
	if b, _, _ := evalMetric(samples(now, 95, 95, 95), "cpu", 90, dur, now); b {
		t.Error("15 s of data cannot prove a 30 s breach")
	}
	if b, _, normal := evalMetric(samples(now, 95, 95, 95, 95, 95, 95, 95, 20), "cpu", 90, dur, now); b || !normal {
		t.Error("latest sample below threshold should be normal")
	}
	if b, v, _ := evalMetric(samples(now, 1, 1, 1, 1, 1, 1, 1, 1), "mem", 40, dur, now); !b || v != 50 {
		t.Errorf("mem percent wrong: %v %v", b, v)
	}
	if b, _, _ := evalMetric(nil, "cpu", 90, dur, now); b {
		t.Error("no data breached")
	}
}

func TestValidateChannel(t *testing.T) {
	bad := []struct {
		typ string
		cfg map[string]string
	}{
		{"discord", map[string]string{"webhook_url": "https://evil.example/api/webhooks/1/x"}},
		{"discord", map[string]string{"webhook_url": "http://discord.com/api/webhooks/1/x"}},
		{"telegram", map[string]string{"bot_token": "nope", "chat_id": "1"}},
		{"email", map[string]string{"host": "smtp", "port": "99999", "from": "a@b.c", "to": "x@y.z"}},
		{"email", map[string]string{"host": "smtp", "port": "587", "from": "a@b.c", "to": "x@y.z,not-an-email"}},
		{"webhook", map[string]string{"url": "file:///etc/passwd"}},
		{"sms", map[string]string{}},
	}
	for _, c := range bad {
		if err := validateChannel(c.typ, c.cfg); err == nil {
			t.Errorf("accepted %s %v", c.typ, c.cfg)
		}
	}
	good := []struct {
		typ string
		cfg map[string]string
	}{
		{"discord", map[string]string{"webhook_url": "https://discord.com/api/webhooks/123/abc"}},
		{"telegram", map[string]string{"bot_token": "123456:ABCdefGHIjklMNOpqrSTUvwx", "chat_id": "-1001234"}},
		{"email", map[string]string{"host": "smtp.example.com", "port": "587", "from": "Ops <ops@example.com>", "to": "a@example.com, b@example.com"}},
		{"webhook", map[string]string{"url": "https://hooks.example.com/x"}},
	}
	for _, c := range good {
		if err := validateChannel(c.typ, c.cfg); err != nil {
			t.Errorf("rejected %s: %v", c.typ, err)
		}
	}
}

func TestWebhookSignature(t *testing.T) {
	var body []byte
	var sig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		sig = r.Header.Get("X-Servermgr-Signature")
	}))
	defer srv.Close()
	err := send(context.Background(), "webhook", map[string]string{"url": srv.URL, "secret": "s3cret"}, Message{Title: "[FIRING] x", Firing: true})
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(body)
	if sig != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
		t.Errorf("bad signature %q", sig)
	}
	if !strings.Contains(string(body), `"title":"[FIRING] x"`) {
		t.Errorf("unexpected body %s", body)
	}
}

func TestPostErrorHidesURL(t *testing.T) {
	err := postJSON(context.Background(), "http://127.0.0.1:1/botSECRET-TOKEN/sendMessage", []byte("{}"), nil)
	if err == nil || strings.Contains(err.Error(), "SECRET-TOKEN") {
		t.Fatalf("error leaks the URL: %v", err)
	}
}
