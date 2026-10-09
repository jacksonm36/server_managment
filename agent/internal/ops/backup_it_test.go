package ops

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestBackupDatabaseIntegration dumps a real database into a backup, drops
// it and restores it. Run by scripts/check-mariadb.sh.
func TestBackupDatabaseIntegration(t *testing.T) {
	if os.Getenv("SM_MARIADB_IT") == "" {
		t.Skip("SM_MARIADB_IT not set")
	}
	_, s := withBackupRoots(t)
	ctx := context.Background()
	logf := func(f string, a ...any) { t.Logf(f, a...) }
	if _, err := mariadb(ctx, "CREATE DATABASE bk; CREATE TABLE bk.t (v TEXT); INSERT INTO bk.t VALUES ('it''s back');"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = mariadb(ctx, "DROP DATABASE IF EXISTS bk;") })
	name, err := createBackup(ctx, s, []string{"bk"}, 0, logf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mariadb(ctx, "DROP DATABASE bk;"); err != nil {
		t.Fatal(err)
	}
	if _, err := backupRestore(ctx, map[string]string{"domain": s.Domain, "file": name, "files": "false", "databases": "true"}, logf); err != nil {
		t.Fatal(err)
	}
	out, err := mariadb(ctx, "SELECT v FROM bk.t;")
	if err != nil || strings.TrimSpace(out) != "it's back" {
		t.Fatalf("restored data = %q, %v", out, err)
	}
}
