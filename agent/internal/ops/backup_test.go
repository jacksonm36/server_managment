package ops

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func withBackupRoots(t *testing.T) (home string, s *Site) {
	t.Helper()
	base, _ := filepath.EvalSymlinks(t.TempDir())
	oldSites, oldReg, oldBk := sitesRoot, siteRegistry, backupRoot
	sitesRoot, siteRegistry, backupRoot = filepath.Join(base, "sites"), filepath.Join(base, "reg"), filepath.Join(base, "bk")
	t.Cleanup(func() { sitesRoot, siteRegistry, backupRoot = oldSites, oldReg, oldBk })
	s = &Site{Domain: "shop.test", Type: "static", User: "s_shop_test"}
	s.Root = filepath.Join(s.home(), "public")
	for _, d := range []string{s.Root + "/css", s.home() + "/logs"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := saveSite(s); err != nil {
		t.Fatal(err)
	}
	return s.home(), s
}

func TestBackupRestoreRoundTrip(t *testing.T) {
	home, s := withBackupRoots(t)
	ctx := context.Background()
	logf := func(f string, a ...any) { t.Logf(f, a...) }
	write := func(p, c string) {
		if err := os.WriteFile(filepath.Join(home, p), []byte(c), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	write("public/index.html", "v1")
	write("public/css/site.css", "body{}")
	write("logs/access.log", "should not be backed up")
	if err := os.Symlink("index.html", filepath.Join(home, "public/home.html")); err != nil {
		t.Fatal(err)
	}

	name, err := createBackup(ctx, s, nil, 2, logf)
	if err != nil {
		t.Fatal(err)
	}
	write("public/index.html", "v2 (broken deploy)")
	write("public/extra.php", "added after backup")
	write("logs/access.log", "live log")

	if _, err := backupRestore(ctx, map[string]string{"domain": s.Domain, "file": name}, logf); err != nil {
		t.Fatal(err)
	}
	read := func(p string) string { b, _ := os.ReadFile(filepath.Join(home, p)); return string(b) }
	if got := read("public/index.html"); got != "v1" {
		t.Errorf("index.html = %q, want v1", got)
	}
	if read("public/css/site.css") != "body{}" {
		t.Error("nested file not restored")
	}
	if _, err := os.Stat(filepath.Join(home, "public/extra.php")); !os.IsNotExist(err) {
		t.Error("file added after the backup should be gone")
	}
	if read("logs/access.log") != "live log" {
		t.Error("logs must not be touched by restore")
	}
	if l, _ := os.Readlink(filepath.Join(home, "public/home.html")); l != "index.html" {
		t.Errorf("symlink not restored: %q", l)
	}

	// Retention keeps the newest 2.
	for range 3 {
		time.Sleep(1100 * time.Millisecond)
		if _, err := createBackup(ctx, s, nil, 2, logf); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(listBackups(s.Domain)); n != 2 {
		t.Errorf("retention kept %d backups, want 2", n)
	}
}

func TestSafeJoin(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.Symlink("/etc", filepath.Join(base, "evil")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../../etc/passwd", "evil/passwd", "/abs/../../x"} {
		got, err := safeJoin(base, name)
		if err == nil && !(got == base || len(got) > len(base) && got[:len(base)+1] == base+"/") {
			t.Errorf("%q escaped to %s", name, got)
		}
		if name == "evil/passwd" && err == nil {
			t.Errorf("write through symlink allowed: %s", got)
		}
	}
	if got, err := safeJoin(base, "public/a.txt"); err != nil && !os.IsNotExist(err) {
		t.Errorf("normal path rejected: %v %s", err, got)
	}
}

func TestBackupDue(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 10, 9, h, 30, 0, 0, time.Local) }
	p := &BackupPolicy{Enabled: true, Every: "daily", Hour: 3}
	if backupDue(p, at(2)) {
		t.Error("due before the hour")
	}
	if !backupDue(p, at(3)) {
		t.Error("first run not due")
	}
	p.LastRun = at(3).Unix()
	if backupDue(p, at(23)) {
		t.Error("due twice the same day")
	}
	if !backupDue(p, at(3).Add(24*time.Hour)) {
		t.Error("not due the next day")
	}
	p.Enabled = false
	if backupDue(p, at(3).Add(48*time.Hour)) {
		t.Error("disabled policy is due")
	}
}
