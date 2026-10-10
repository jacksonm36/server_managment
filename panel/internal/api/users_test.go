package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jacksonm36/servermgr/panel/internal/store"
)

func TestNeed(t *testing.T) {
	h := need("operator", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	for role, want := range map[string]int{"viewer": 403, "operator": 204, "admin": 204, "": 403} {
		r := httptest.NewRequest("POST", "/", nil)
		r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, &store.Admin{Role: role}))
		w := httptest.NewRecorder()
		h(w, r)
		if w.Code != want {
			t.Errorf("role %q: got %d want %d", role, w.Code, want)
		}
	}
}

func TestRoleAllowsOp(t *testing.T) {
	cases := []struct {
		role, op string
		want     bool
	}{
		{"viewer", "firewall.status", true},
		{"viewer", "services.list", true},
		{"viewer", "files.read", false},
		{"viewer", "compose.read", false},
		{"viewer", "system.reboot", false},
		{"viewer", "ssh_keys.add", false},
		{"operator", "system.reboot", true},
		{systemRole, "packages.upgrade", true},
		{"", "system.info", false},
		{"hacker", "system.info", false},
	}
	for _, c := range cases {
		if got := roleAllowsOp(c.role, c.op); got != c.want {
			t.Errorf("%s/%s: got %v", c.role, c.op, got)
		}
	}
}
