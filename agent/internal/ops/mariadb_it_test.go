package ops

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"testing"
)

// TestMariaDBIntegration runs the DB ops against a real server. It needs the
// mariadb client with root socket access; scripts/check-mariadb.sh runs it in
// a throwaway container.
func TestMariaDBIntegration(t *testing.T) {
	if os.Getenv("SM_MARIADB_IT") == "" {
		t.Skip("SM_MARIADB_IT not set")
	}
	ctx := context.Background()
	logf := func(f string, a ...any) { t.Logf(f, a...) }
	status := func() map[string]any {
		r, err := dbStatus(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return r.(map[string]any)
	}

	if _, err := dbCreate(ctx, map[string]string{"name": "shop", "user": "shop_app", "password": "pa'ss\\word-12345"}, logf); err != nil {
		t.Fatal(err)
	}
	st := status()
	dbs := st["databases"].([]dbInfo)
	if len(dbs) != 1 || dbs[0].Name != "shop" || !slices.Contains(dbs[0].Users, "shop_app@localhost") {
		t.Fatalf("unexpected databases: %+v", dbs)
	}
	// The odd password must actually work.
	if _, err := mariadbAs(ctx, "shop_app", "pa'ss\\word-12345", "SELECT 1"); err != nil {
		t.Fatalf("login with created password: %v", err)
	}

	if _, err := dbUserCreate(ctx, map[string]string{"user": "reporter", "host": "10.0.1.%", "password": "another-pass-123", "databases": "shop"}, logf); err != nil {
		t.Fatal(err)
	}
	users := status()["users"].([]dbUser)
	if len(users) != 2 {
		t.Fatalf("expected 2 users: %+v", users)
	}
	if _, err := dbCreate(ctx, map[string]string{"name": "bad`name"}, logf); err == nil {
		t.Fatal("bad name accepted")
	}
	if _, err := dbDelete(ctx, map[string]string{"name": "shop", "drop_users": "true"}, logf); err != nil {
		t.Fatal(err)
	}
	st = status()
	if n := len(st["databases"].([]dbInfo)); n != 0 {
		t.Fatalf("database not dropped")
	}
	if n := len(st["users"].([]dbUser)); n != 0 {
		t.Fatalf("users with only that database should be dropped, got %+v", st["users"])
	}
}

func mariadbAs(ctx context.Context, user, pw, sql string) (string, error) {
	cmd := exec.CommandContext(ctx, "mariadb", "--batch", "-u", user, "-e", sql)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+pw)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, out)
	}
	return string(out), nil
}
