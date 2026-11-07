package ops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRotateLogDir(t *testing.T) {
	dir := t.TempDir()
	logs := filepath.Join(dir, "logs")
	if err := os.Mkdir(logs, 0o750); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dir, "secret")
	if err := os.WriteFile(outside, []byte("do not touch"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A symlinked log must be skipped, not followed.
	if err := os.Symlink(outside, filepath.Join(logs, "error.log")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logs, "access.log"), []byte("line\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	// First pass only starts the clock.
	if err := rotateLogDir(logs, now); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(logs, "access.log")); string(b) != "line\n" {
		t.Fatalf("rotated on first sight: %q", b)
	}
	for day := 1; day <= logKeep+3; day++ {
		if err := os.WriteFile(filepath.Join(logs, "access.log"), []byte("line\n"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := rotateLogDir(logs, now.Add(time.Duration(day)*25*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(logs, "access.log")); len(b) != 0 {
		t.Fatalf("access.log not truncated: %q", b)
	}
	ents, _ := os.ReadDir(logs)
	n := 0
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "access.log.") && strings.HasSuffix(e.Name(), ".gz") {
			n++
		}
	}
	if n != logKeep {
		t.Fatalf("kept %d archives, want %d", n, logKeep)
	}
	if b, _ := os.ReadFile(outside); string(b) != "do not touch" {
		t.Fatalf("followed a symlinked log: %q", b)
	}
}
