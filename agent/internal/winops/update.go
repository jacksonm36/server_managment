//go:build windows

package winops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/jacksonm36/servermgr/agent/internal/panelhttp"
)

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// selfUpdate installs the panel's Windows build: downloaded from the panel
// (its CA only), checked against the SHA-256 the panel sent, run once,
// then swapped in. The running file can be renamed but not overwritten;
// the agent then exits and the service's recovery action starts the new
// one.
func selfUpdate(ctx context.Context, p map[string]string, logf logFunc) (any, error) {
	want := strings.ToLower(p["sha256"])
	if !sha256Hex.MatchString(want) {
		return nil, errors.New("invalid sha256")
	}
	base := PanelURL
	if u := p["panel_url"]; strings.HasPrefix(u, "https://") {
		base = u
	}
	if !strings.HasPrefix(base, "https://") {
		return nil, errors.New("panel URL unknown; re-enroll the agent")
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(base, "/") + "/download/servermgr-agent-windows-" + runtime.GOARCH + ".exe"
	logf("downloading %s", url)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := panelhttp.PanelClient(PanelCA, 10*time.Minute).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	tmp := exe + ".update"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, 200<<20))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return nil, err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		os.Remove(tmp)
		return nil, fmt.Errorf("checksum mismatch (got %s…), update aborted", got[:12])
	}
	out, err := exec.CommandContext(ctx, tmp, "version").Output()
	if err != nil {
		os.Remove(tmp)
		return nil, fmt.Errorf("new binary does not run: %w", err)
	}
	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	if err := os.Rename(tmp, exe); err != nil {
		_ = os.Rename(old, exe)
		os.Remove(tmp)
		return nil, err
	}
	logf("installed %s (%d bytes), version %s; restarting in 2 seconds", filepath.Base(exe), n, strings.TrimSpace(string(out)))
	go func() {
		time.Sleep(2 * time.Second)
		os.Exit(3) // the service's recovery action restarts it
	}()
	return map[string]any{"version": strings.TrimSpace(string(out))}, nil
}
