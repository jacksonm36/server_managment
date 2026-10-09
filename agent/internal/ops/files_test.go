package ops

import (
	"os"
	"path/filepath"
	"testing"
)

func withFilesRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old := filesRoot
	filesRoot = root
	t.Cleanup(func() { filesRoot = old })
	for _, d := range []string{"a.test/public", "a.test/logs", "b.test/public"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestResolvePathConfinement(t *testing.T) {
	root := withFilesRoot(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "a.test/public/escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "a.test/public/passwd")); err != nil {
		t.Fatal(err)
	}
	bad := []string{
		"relative/path",
		root + "/../etc",
		"/etc/passwd",
		root + "/a.test/public/escape",
		root + "/a.test/public/escape/new.txt",
		root + "/a.test/public/passwd",
	}
	for _, p := range bad {
		if r, err := resolvePath(p); err == nil {
			t.Errorf("%s resolved to %s, want error", p, r)
		}
	}
	good := []string{root, root + "/a.test/public", root + "/a.test/public/new/deeper.txt"}
	for _, p := range good {
		if _, err := resolvePath(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}

func TestFilesOps(t *testing.T) {
	root := withFilesRoot(t)
	logf := func(string, ...any) {}
	f := root + "/a.test/public/index.php"
	if _, err := filesWrite(map[string]string{"path": f, "content": "<?php echo 1;"}, logf); err != nil {
		t.Fatal(err)
	}
	if _, err := filesWrite(map[string]string{"path": f, "content": "x"}, logf); err == nil {
		t.Fatal("overwrote without overwrite=true")
	}
	r, err := filesRead(map[string]string{"path": f})
	if err != nil || r.(map[string]any)["text"] != "<?php echo 1;" {
		t.Fatalf("read back: %v %v", r, err)
	}
	if _, err := filesWrite(map[string]string{"path": root + "/a.test", "content": "x", "overwrite": "true"}, logf); err == nil {
		t.Fatal("wrote over the site home")
	}
	for _, p := range []string{root, root + "/a.test", root + "/a.test/public", root + "/a.test/logs"} {
		if _, err := filesDelete(map[string]string{"path": p}, logf); err == nil {
			t.Errorf("deleted protected %s", p)
		}
	}
	if _, err := filesRename(map[string]string{"from": f, "to": root + "/b.test/public/index.php"}, logf); err == nil {
		t.Fatal("moved a file between sites")
	}
	if _, err := filesChmod(map[string]string{"path": f, "mode": "0666"}, logf); err == nil {
		t.Fatal("allowed world-writable mode")
	}
	if _, err := filesMkdir(map[string]string{"path": root + "/a.test/public/assets"}, logf); err != nil {
		t.Fatal(err)
	}
	l, err := filesList(map[string]string{"path": root + "/a.test/public"})
	if err != nil {
		t.Fatal(err)
	}
	ents := l.(map[string]any)["entries"].([]fileEntry)
	if len(ents) != 2 || ents[0].Name != "assets" || ents[0].Type != "dir" {
		t.Fatalf("unexpected listing %+v", ents)
	}
	if _, err := filesDelete(map[string]string{"path": f}, logf); err != nil {
		t.Fatal(err)
	}
}
