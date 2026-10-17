package ops

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
)

// PanelURL is set by main from the enrollment state; the new binary is
// downloaded from the panel itself.
var PanelURL string

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// selfUpdate replaces the agent binary with the panel's build, downloaded
// over HTTPS trusting only the panel CA. The expected SHA-256 arrives over
// the mTLS stream as well. The agent restarts itself after replying.
func selfUpdate(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	want := strings.ToLower(p["sha256"])
	if !sha256Hex.MatchString(want) {
		return nil, errors.New("invalid sha256")
	}
	base := PanelURL
	if u := p["panel_url"]; strings.HasPrefix(u, "https://") {
		base = u // the panel's current address (agents enrolled over HTTP move to HTTPS)
	}
	if base == "" {
		return nil, errors.New("panel URL unknown; re-enroll the agent")
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return nil, err
	}
	url := strings.TrimRight(base, "/") + "/download/servermgr-agent-linux-" + runtime.GOARCH
	logf("downloading %s", url)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := PanelClient(PanelCA, 10*time.Minute).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	tmp := exe + ".update"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
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
	// Sanity check: the new binary must run.
	out, err := exec.CommandContext(ctx, tmp, "version").Output()
	if err != nil {
		os.Remove(tmp)
		return nil, fmt.Errorf("new binary does not run: %w", err)
	}
	newVersion := strings.TrimSpace(string(out))
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	logf("installed %s (%d bytes), version %s; restarting in 2 seconds", exe, n, newVersion)
	go func() { _ = exec.Command("sh", "-c", "sleep 2; systemctl restart servermgr-agent").Run() }()
	return map[string]any{"version": newVersion, "previous": p["current"]}, nil
}
