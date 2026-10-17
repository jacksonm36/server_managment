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

// currentAgentBuild hashes bin/servermgr-agent-linux-* (cached until the
// files change) and reads the version written by `make agent`.
func (a *API) currentAgentBuild() agentBuild {
	buildMu.Lock()
	defer buildMu.Unlock()
	newest := time.Time{}
	for _, arch := range []string{"amd64", "arm64"} {
		if fi, err := os.Stat(filepath.Join(a.Cfg.AgentBinDir, "servermgr-agent-linux-"+arch)); err == nil && fi.ModTime().After(newest) {
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
	for _, arch := range []string{"amd64", "arm64"} {
		f, err := os.Open(filepath.Join(a.Cfg.AgentBinDir, "servermgr-agent-linux-"+arch))
		if err != nil {
			continue
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err == nil {
			b.SHA256[arch] = hex.EncodeToString(h.Sum(nil))
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
	sum, ok := b.SHA256[srv.Arch]
	if !ok {
		writeErr(w, http.StatusConflict, "no agent build for architecture "+srv.Arch)
		return
	}
	job, err := a.Dispatch(r.Context(), whoFrom(r), srv.ID, "agent.update", map[string]string{"sha256": sum, "version": b.Version, "current": srv.AgentVersion, "panel_url": a.Cfg.PublicURL})
	if err != nil {
		a.writeDispatchErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}
