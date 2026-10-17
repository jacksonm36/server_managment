package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPSRedirect(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(299) })
	h := HTTPSRedirect(ok, "8443")
	for _, c := range []struct {
		method, host, remote, path string
		code                       int
		loc                        string
	}{
		{"GET", "10.0.1.240:8090", "10.0.1.5:5000", "/proxmox?x=1", 308, "https://10.0.1.240:8443/proxmox?x=1"},
		{"POST", "panel.lan:8090", "10.0.1.5:5000", "/api/v1/auth/login", 308, "https://panel.lan:8443/api/v1/auth/login"},
		{"GET", "[fd00::5]:8090", "[fd00::9]:5000", "/", 308, "https://[fd00::5]:8443/"},
		{"GET", "10.0.1.240:8090", "10.0.1.5:5000", "/install.sh", 308, "https://10.0.1.240:8443/install.sh"},
		{"POST", "10.0.1.240:8090", "10.0.1.5:5000", "/api/v1/enroll", 308, "https://10.0.1.240:8443/api/v1/enroll"},
		{"GET", "10.0.1.240:8090", "10.0.1.5:5000", "/download/servermgr-agent-linux-amd64", 308, "https://10.0.1.240:8443/download/servermgr-agent-linux-amd64"},
		{"POST", "127.0.0.1:8090", "127.0.0.1:40000", "/tofu/state/x", 299, ""},
		{"GET", "127.0.0.1:8090", "127.0.0.1:40000", "/api/v1/me", 299, ""},
		{"GET", "evil.example/x:8090", "10.0.1.5:5000", "/", 400, ""},
	} {
		r := httptest.NewRequest(c.method, c.path, nil)
		r.Host, r.RemoteAddr = c.host, c.remote
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.code || w.Header().Get("Location") != c.loc {
			t.Errorf("%s %s%s: %d %q", c.method, c.host, c.path, w.Code, w.Header().Get("Location"))
		}
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	r.Host, r.RemoteAddr = "panel.example.com", "10.0.0.1:1"
	HTTPSRedirect(ok, "443").ServeHTTP(w, r)
	if w.Header().Get("Location") != "https://panel.example.com/" {
		t.Errorf("port 443: %q", w.Header().Get("Location"))
	}
}
