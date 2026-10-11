package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/jacksonm36/servermgr/panel/internal/hub"
	"github.com/jacksonm36/servermgr/panel/internal/store"
)

// TestRBACEndToEnd runs against a real, throwaway PostgreSQL database:
//
//	SM_TEST_DATABASE_URL=postgres://sm:sm@localhost/smtest go test ./panel/...
//
// The database is wiped first.
func TestRBACEndToEnd(t *testing.T) {
	url := os.Getenv("SM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("SM_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	a := &API{Store: st, Hub: hub.New(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Ctx: ctx, SecretKey: make([]byte, 32)}
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()

	const pw = "correct horse battery"
	hash, _ := HashPassword(pw)
	if err := st.CreateAdmin(ctx, "root", hash); err != nil {
		t.Fatal(err)
	}
	web, db, other := store.NewUUID(), store.NewUUID(), store.NewUUID()
	for name, id := range map[string]string{"web1": web, "db1": db, "other": other} {
		if _, err := conn.Exec(ctx, `INSERT INTO servers (id, name) VALUES ($1, $2)`, id, name); err != nil {
			t.Fatal(err)
		}
	}

	type client struct{ cookie string }
	call := func(c client, method, path string, body any) (int, map[string]any, []any) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+path, rd)
		req.Header.Set("X-Requested-With", "servermgr")
		if c.cookie != "" {
			req.Header.Set("Cookie", sessionCookie+"="+c.cookie)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		var obj map[string]any
		var list []any
		if json.Unmarshal(raw, &obj) != nil {
			_ = json.Unmarshal(raw, &list)
		}
		return res.StatusCode, obj, list
	}
	login := func(user, pass string) (client, map[string]any) {
		t.Helper()
		b, _ := json.Marshal(map[string]string{"username": user, "password": pass})
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/auth/login", bytes.NewReader(b))
		req.Header.Set("X-Requested-With", "servermgr")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var obj map[string]any
		_ = json.NewDecoder(res.Body).Decode(&obj)
		if res.StatusCode != 200 {
			t.Fatalf("login %s: %d %v", user, res.StatusCode, obj)
		}
		for _, c := range res.Cookies() {
			if c.Name == sessionCookie {
				return client{c.Value}, obj
			}
		}
		t.Fatal("no session cookie")
		return client{}, nil
	}
	expect := func(what string, got, want int) {
		t.Helper()
		if got != want {
			t.Errorf("%s: got %d, want %d", what, got, want)
		}
	}
	id := func(m map[string]any) int64 { return int64(m["id"].(float64)) }

	root, _ := login("root", pw)

	// A server group with web1 and a custom role that may only restart
	// services and read status.
	code, sg, _ := call(root, "POST", "/api/v1/server-groups", map[string]any{"name": "Web servers", "color": "#22c55e", "server_ids": []string{web}})
	expect("create server group", code, 200)
	code, role, _ := call(root, "POST", "/api/v1/roles", map[string]any{"name": "Web ops", "permissions": []string{"server.view", "server.system"}})
	expect("create role", code, 200)
	code, _, _ = call(root, "POST", "/api/v1/roles", map[string]any{"name": "bad", "permissions": []string{"launch.missiles"}})
	expect("unknown permission", code, 400)
	code, _, _ = call(root, "POST", "/api/v1/roles", map[string]any{"name": "admin", "permissions": []string{}})
	expect("reserved role name", code, 400)

	// alice has no role of her own; her user group gets "Web ops" on the
	// web group.
	code, ug, _ := call(root, "POST", "/api/v1/user-groups", map[string]any{"name": "Web team"})
	expect("create user group", code, 200)
	code, u, _ := call(root, "POST", "/api/v1/users", map[string]any{
		"username": "alice", "password": "alice-password-1", "role": "none",
		"groups": []int64{id(ug)}, "must_change_password": true,
	})
	expect("create user", code, 201)
	alice := id(u)
	code, _, _ = call(root, "POST", "/api/v1/grants", map[string]any{"user_group_id": id(ug), "role_id": id(role), "server_group_id": id(sg)})
	expect("grant", code, 201)

	// Forced password change: everything but the account is blocked.
	al, lr := login("alice", "alice-password-1")
	if lr["must_change_password"] != true {
		t.Errorf("login should say must_change_password: %v", lr)
	}
	code, body, _ := call(al, "GET", "/api/v1/servers", nil)
	expect("blocked until password change", code, 403)
	if body["must_change_password"] != true {
		t.Errorf("403 body: %v", body)
	}
	code, _, _ = call(al, "POST", "/api/v1/account/password", map[string]string{"current": "alice-password-1", "new": "alice-password-2"})
	expect("change password", code, 204)

	// alice sees only web1.
	code, _, list := call(al, "GET", "/api/v1/servers", nil)
	expect("list servers", code, 200)
	if len(list) != 1 || list[0].(map[string]any)["id"] != web {
		t.Errorf("alice should see only web1, got %v", list)
	}
	code, me, _ := call(al, "GET", "/api/v1/me", nil)
	expect("me", code, 200)
	if sp := me["server_permissions"].(map[string]any); len(sp) != 1 || sp[web] == nil {
		t.Errorf("server_permissions: %v", sp)
	}
	expect("view other server", first(call(al, "GET", "/api/v1/servers/"+other, nil)), 403)
	expect("view own server", first(call(al, "GET", "/api/v1/servers/"+web, nil)), 200)

	// Allowed op on an offline server reaches the online check (409);
	// forbidden ones stop at 403 first.
	job := func(sid, op string) int {
		return first(call(al, "POST", "/api/v1/servers/"+sid+"/jobs", map[string]any{"op": op, "params": map[string]string{}}))
	}
	expect("status op on own server", job(web, "system.info"), 409)
	expect("op on other server", job(other, "system.info"), 403)
	expect("firewall op on own server", job(web, "firewall.status"), 409) // viewer-level read
	expect("docker op on own server", job(web, "docker.install"), 403)
	expect("reboot-type op on own server", job(web, "packages.upgrade"), 409)
	expect("users page", first(call(al, "GET", "/api/v1/users", nil)), 403)
	expect("delete server", first(call(al, "DELETE", "/api/v1/servers/"+web, nil)), 403)

	// Schedules cannot be used to escalate: give alice schedules.manage
	// panel-wide, she still can't target servers outside her group.
	code, sr, _ := call(root, "POST", "/api/v1/roles", map[string]any{"name": "Scheduler", "permissions": []string{"schedules.manage", "automation.view"}})
	expect("scheduler role", code, 200)
	expect("grant scheduler", first(call(root, "POST", "/api/v1/grants", map[string]any{"user_id": alice, "role_id": id(sr)})), 201)
	sched := func(target map[string]any) int {
		b := map[string]any{"name": "nightly", "cron": "0 3 * * *", "kind": "op", "op": "packages.upgrade", "enabled": false}
		for k, v := range target {
			b[k] = v
		}
		return first(call(al, "POST", "/api/v1/schedules", b))
	}
	expect("schedule on own group", sched(map[string]any{"server_group_id": id(sg)}), 200)
	expect("schedule on all servers", sched(map[string]any{"all_servers": true}), 403)
	expect("schedule on other server", sched(map[string]any{"server_ids": []string{other}}), 403)

	// Role in use cannot be deleted; last admin is protected.
	expect("delete role in use", first(call(root, "DELETE", fmt.Sprintf("/api/v1/roles/%d", id(role)), nil)), 409)
	var rootID int64
	_ = conn.QueryRow(ctx, `SELECT id FROM admins WHERE username = 'root'`).Scan(&rootID)
	expect("demote self", first(call(root, "PUT", fmt.Sprintf("/api/v1/users/%d", rootID), map[string]any{"role": "viewer"})), 400)

	// Ending sessions logs alice out.
	expect("sign out alice", first(call(root, "POST", fmt.Sprintf("/api/v1/users/%d/sign-out", alice), nil)), 200)
	expect("alice after sign-out", first(call(al, "GET", "/api/v1/me", nil)), 401)
}

func first(code int, _ map[string]any, _ []any) int { return code }
