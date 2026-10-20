package api

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// agentBuild describes the agent binaries the panel can hand out.
type agentBuild struct {
	Version string            `json:"version"`
	SHA256  map[string]string `json:"sha256"` // arch -> hex
}

var (
	buildMu    sync.Mutex
	buildCache agentBuild
	buildStamp time.Time
)

// agentBuildFiles maps the build keys (Linux arch, or windows-<arch>) to
// the files /download serves.
var agentBuildFiles = map[string]string{
	"amd64":         "servermgr-agent-linux-amd64",
	"arm64":         "servermgr-agent-linux-arm64",
	"windows-amd64": "servermgr-agent-windows-amd64.exe",
}

// buildKey is the agentBuildFiles key for a server.
func buildKey(os, arch string) string {
	if os == "windows" {
		return "windows-" + arch
	}
	return arch
}

// currentAgentBuild hashes the agent builds (cached until the files
// change) and reads the version written by `make agent`.
func (a *API) currentAgentBuild() agentBuild {
	buildMu.Lock()
	defer buildMu.Unlock()
	newest := time.Time{}
	for _, file := range agentBuildFiles {
		if fi, err := os.Stat(filepath.Join(a.Cfg.AgentBinDir, file)); err == nil && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
	}
	if !newest.IsZero() && newest.Equal(buildStamp) {
		return buildCache
	}
	b := agentBuild{SHA256: map[string]string{}}
	if v, err := os.ReadFile(filepath.Join(a.Cfg.AgentBinDir, "agent.version")); err == nil {
		b.Version = strings.TrimSpace(string(v))
	}
	for key, file := range agentBuildFiles {
		f, err := os.Open(filepath.Join(a.Cfg.AgentBinDir, file))
		if err != nil {
			continue
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err == nil {
			b.SHA256[key] = hex.EncodeToString(h.Sum(nil))
		}
	}
	buildCache, buildStamp = b, newest
	return b
}

func (a *API) handleAgentBuild(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.currentAgentBuild())
}

// handleAgentUpdate tells a server's agent to install the panel's build.
func (a *API) handleAgentUpdate(w http.ResponseWriter, r *http.Request) {
	srv, err := a.Store.GetServer(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "server not found")
		return
	}
	b := a.currentAgentBuild()
	sum, ok := b.SHA256[buildKey(srv.OS, srv.Arch)]
	if !ok {
		writeErr(w, http.StatusConflict, "no agent build for "+buildKey(srv.OS, srv.Arch))
		return
	}
	job, err := a.Dispatch(r.Context(), whoFrom(r), srv.ID, "agent.update", map[string]string{"sha256": sum, "version": b.Version, "current": srv.AgentVersion, "panel_url": a.Cfg.PublicURL})
	if err != nil {
		a.writeDispatchErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}
