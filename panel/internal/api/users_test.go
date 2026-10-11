package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func accessOf(role string) *Access {
	ac := &Access{Role: role, global: map[string]bool{}}
	for _, p := range builtinPerms[role] {
		ac.global[p] = true
	}
	return ac
}

func TestNeed(t *testing.T) {
	h := need("playbooks.run", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	for role, want := range map[string]int{"viewer": 403, "operator": 204, "admin": 204, "none": 403, "": 403} {
		r := httptest.NewRequest("POST", "/", nil)
		if role != "" {
			r = r.WithContext(context.WithValue(r.Context(), accessKey{}, accessOf(role)))
		}
		w := httptest.NewRecorder()
		h(w, r)
		if w.Code != want {
			t.Errorf("role %q: got %d want %d", role, w.Code, want)
		}
	}
}

func TestBuiltinRolesAllowOps(t *testing.T) {
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
		{"operator", "files.read", true},
		{"operator", "db.create", true},
		{"none", "system.info", false},
		{"admin", "made.up", true},
		{"operator", "made.up", false},
	}
	for _, c := range cases {
		if got := accessOf(c.role).CanOn(opPerm(c.op), "s1"); got != c.want {
			t.Errorf("%s/%s: got %v", c.role, c.op, got)
		}
	}
	var system *Access
	if !system.CanOn(opPerm("packages.upgrade"), "s1") || !system.Can("users.manage") {
		t.Error("the panel itself must be allowed everything")
	}
}

func TestEveryOpHasKnownPerm(t *testing.T) {
	for op := range opsByName {
		if p := opPerm(op); !permKnown(p) || !serverPerm(p) {
			t.Errorf("op %s maps to %q", op, p)
		}
	}
	for role, ps := range builtinPerms {
		for _, p := range ps {
			if !permKnown(p) {
				t.Errorf("role %s has unknown permission %s", role, p)
			}
		}
	}
	if slices.Contains(builtinPerms[roleOperator], "users.manage") {
		t.Error("operator must not manage users")
	}
}

func TestScopedAccess(t *testing.T) {
	ac := &Access{
		global:   map[string]bool{"automation.view": true},
		scoped:   map[int64]map[string]bool{1: {"server.view": true, "server.system": true}, 2: {"server.view": true}},
		groupsOf: map[string][]int64{"a": {1}, "b": {2}, "c": {1, 2}},
	}
	cases := []struct {
		perm, server string
		want         bool
	}{
		{"server.view", "a", true},
		{"server.system", "a", true},
		{"server.view", "b", true},
		{"server.system", "b", false},
		{"server.system", "c", true},
		{"server.view", "d", false},
	}
	for _, c := range cases {
		if got := ac.CanOn(c.perm, c.server); got != c.want {
			t.Errorf("%s on %s: got %v", c.perm, c.server, got)
		}
	}
	if !ac.CanOnGroup("server.system", 1) || ac.CanOnGroup("server.system", 2) {
		t.Error("CanOnGroup")
	}
	if ac.Can("server.view") || !ac.CanAnywhere("server.view") {
		t.Error("scoped grants must not become global")
	}
	ps := ac.PerServer()
	if !slices.Equal(ps["b"], []string{"server.view"}) || len(ps["a"]) != 2 || ps["d"] != nil {
		t.Errorf("PerServer: %v", ps)
	}
}
