// Package api is the panel's HTTP JSON API, agent enrollment endpoints and
// static UI server.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	agentv1 "github.com/jacksonm36/servermgr/gen/agent/v1"
	"github.com/jacksonm36/servermgr/panel/internal/hub"
	"github.com/jacksonm36/servermgr/panel/internal/pki"
	"github.com/jacksonm36/servermgr/panel/internal/store"
)

type Config struct {
	// PublicURL is the panel's HTTP base URL as reachable by agents,
	// e.g. http://10.0.1.240:8090.
	PublicURL string
	// GRPCAddr is host:port agents dial for the mTLS stream.
	GRPCAddr string
	// AgentBinDir holds servermgr-agent-linux-<arch> binaries for download.
	AgentBinDir string
	// ProgressFile is the build-progress JSON shown on /progress. It is
	// re-read on every request.
	ProgressFile string
}

type API struct {
	Cfg   Config
	Store *store.Store
	Hub   *hub.Hub
	CA    *pki.CA
	Log   *slog.Logger
	Web   fs.FS // built UI (web/dist), may be nil
	// SecretKey (32 bytes) encrypts TOTP secrets at rest.
	SecretKey []byte
	// Ctx lives as long as the panel; background work (playbooks,
	// schedules, alerts) uses it instead of a request context.
	Ctx context.Context

	limiter *loginLimiter
}

func (a *API) Handler() http.Handler {
	a.limiter = &loginLimiter{fail: map[string][]time.Time{}}
	mux := http.NewServeMux()

	// Public: login, agent enrollment and install.
	mux.HandleFunc("POST /api/v1/auth/login", a.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", a.handleLogout)
	mux.HandleFunc("POST /api/v1/enroll", a.handleEnroll)
	mux.HandleFunc("GET /install.sh", a.handleInstallScript)
	mux.HandleFunc("GET /ca.crt", a.handleCACert)
	mux.HandleFunc("GET /download/{file}", a.handleDownload)

	// Authenticated API.
	authed := http.NewServeMux()
	authed.HandleFunc("GET /api/v1/me", a.handleMe)
	authed.HandleFunc("GET /api/v1/account", a.handleAccount)
	authed.HandleFunc("POST /api/v1/account/password", a.handleChangePassword)
	authed.HandleFunc("POST /api/v1/account/totp/setup", a.handleTOTPSetup)
	authed.HandleFunc("POST /api/v1/account/totp/enable", a.handleTOTPEnable)
	authed.HandleFunc("POST /api/v1/account/totp/disable", a.handleTOTPDisable)
	authed.HandleFunc("POST /api/v1/account/recovery-codes", a.handleRecoveryRegenerate)
	authed.HandleFunc("GET /api/v1/servers", a.handleListServers)
	authed.HandleFunc("GET /api/v1/servers/{id}", a.handleGetServer)
	authed.HandleFunc("DELETE /api/v1/servers/{id}", a.handleDeleteServer)
	authed.HandleFunc("GET /api/v1/servers/{id}/metrics", a.handleMetrics)
	authed.HandleFunc("GET /api/v1/servers/{id}/terminal", a.handleTerminal)
	authed.HandleFunc("GET /api/v1/servers/{id}/jobs", a.handleListJobs)
	authed.HandleFunc("POST /api/v1/servers/{id}/jobs", a.handleCreateJob)
	authed.HandleFunc("GET /api/v1/jobs/{id}", a.handleGetJob)
	authed.HandleFunc("GET /api/v1/jobs", a.handleRecentJobs)
	authed.HandleFunc("POST /api/v1/bulk", a.handleBulk)
	authed.HandleFunc("GET /api/v1/playbooks", a.handleListPlaybooks)
	authed.HandleFunc("POST /api/v1/playbooks", a.handleSavePlaybook)
	authed.HandleFunc("PUT /api/v1/playbooks/{id}", a.handleSavePlaybook)
	authed.HandleFunc("DELETE /api/v1/playbooks/{id}", a.handleDeletePlaybook)
	authed.HandleFunc("POST /api/v1/playbooks/{id}/run", a.handleRunPlaybook)
	authed.HandleFunc("GET /api/v1/playbook-runs", a.handleListRuns)
	authed.HandleFunc("GET /api/v1/schedules", a.handleListSchedules)
	authed.HandleFunc("GET /api/v1/channels", a.handleListChannels)
	authed.HandleFunc("POST /api/v1/channels", a.handleSaveChannel)
	authed.HandleFunc("PUT /api/v1/channels/{id}", a.handleSaveChannel)
	authed.HandleFunc("DELETE /api/v1/channels/{id}", a.handleDeleteChannel)
	authed.HandleFunc("POST /api/v1/channels/{id}/test", a.handleTestChannel)
	authed.HandleFunc("GET /api/v1/alert-rules", a.handleListRules)
	authed.HandleFunc("POST /api/v1/alert-rules", a.handleSaveRule)
	authed.HandleFunc("PUT /api/v1/alert-rules/{id}", a.handleSaveRule)
	authed.HandleFunc("DELETE /api/v1/alert-rules/{id}", a.handleDeleteRule)
	authed.HandleFunc("GET /api/v1/alerts", a.handleListAlerts)
	authed.HandleFunc("GET /api/v1/settings/baseline", a.handleGetBaseline)
	authed.HandleFunc("PUT /api/v1/settings/baseline", a.handleSetBaseline)
	authed.HandleFunc("POST /api/v1/schedules", a.handleSaveSchedule)
	authed.HandleFunc("PUT /api/v1/schedules/{id}", a.handleSaveSchedule)
	authed.HandleFunc("DELETE /api/v1/schedules/{id}", a.handleDeleteSchedule)
	authed.HandleFunc("POST /api/v1/schedules/{id}/run", a.handleRunScheduleNow)
	authed.HandleFunc("GET /api/v1/audit", a.handleListAudit)
	authed.HandleFunc("POST /api/v1/join-tokens", a.handleCreateJoinToken)
	authed.HandleFunc("GET /api/v1/events", a.handleEvents)
	authed.HandleFunc("GET /api/v1/progress", a.handleProgress)
	mux.Handle("/api/v1/", a.requireAuth(authed))

	mux.Handle("/", a.staticHandler())
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// staticHandler serves the SPA, falling back to index.html for client routes.
func (a *API) staticHandler() http.Handler {
	if a.Web == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "UI not built (run `make web`)", http.StatusNotFound)
		})
	}
	files := http.FileServerFS(a.Web)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if _, err := fs.Stat(a.Web, p); err == nil {
				if strings.HasPrefix(p, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, a.Web, "index.html")
	})
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (a *API) internal(w http.ResponseWriter, err error) {
	a.Log.Error("internal error", "err", err)
	writeErr(w, http.StatusInternalServerError, "internal error")
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	// 4 MiB fits a 2 MiB file upload once base64-encoded.
	dec := json.NewDecoder(io.LimitReader(r.Body, 4<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

// ---- servers ----

type serverView struct {
	*store.Server
	Online bool        `json:"online"`
	Latest *hub.Metric `json:"latest"`
}

func (a *API) view(s *store.Server) serverView {
	return serverView{Server: s, Online: a.Hub.Online(s.ID), Latest: a.Hub.LatestMetric(s.ID)}
}

func (a *API) handleListServers(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.ListServers(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	out := make([]serverView, 0, len(list))
	for _, s := range list {
		out = append(out, a.view(s))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleGetServer(w http.ResponseWriter, r *http.Request) {
	s, err := a.Store.GetServer(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "server not found")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a.view(s))
}

func (a *API) handleDeleteServer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, err := a.Store.GetServer(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "server not found")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	if err := a.Store.DeleteServer(r.Context(), id); err != nil {
		a.internal(w, err)
		return
	}
	a.audit(r, "", "server.delete", s.Name, map[string]string{"server_id": id, "hostname": s.Hostname}, true)
	a.Hub.Forget(id)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleMetrics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.Hub.Metrics(r.PathValue("id")))
}

// ---- jobs ----

// opSpec is an op the UI may run and the parameters it accepts. The agent
// validates parameter values itself; the panel only rejects unknown keys.
type opSpec struct {
	op     agentv1.Op
	params []string
	// maxParam overrides the default 16 KiB limit per parameter value.
	maxParam int
}

var opsByName = map[string]opSpec{
	"system.info":            {op: agentv1.Op_OP_SYSTEM_INFO},
	"system.reboot":          {op: agentv1.Op_OP_SYSTEM_REBOOT},
	"packages.check_updates": {op: agentv1.Op_OP_PACKAGES_CHECK_UPDATES},
	"packages.upgrade":       {op: agentv1.Op_OP_PACKAGES_UPGRADE, params: []string{"packages", "dist", "autoremove"}},
	"ssh_keys.list":          {op: agentv1.Op_OP_SSH_KEYS_LIST, params: []string{"user"}},
	"ssh_keys.add":           {op: agentv1.Op_OP_SSH_KEY_ADD, params: []string{"user", "key", "comment"}},
	"ssh_keys.remove":        {op: agentv1.Op_OP_SSH_KEY_REMOVE, params: []string{"user", "fingerprint"}},
	"firewall.status":        {op: agentv1.Op_OP_FIREWALL_STATUS},
	"firewall.install":       {op: agentv1.Op_OP_FIREWALL_INSTALL},
	"firewall.enable":        {op: agentv1.Op_OP_FIREWALL_ENABLE},
	"firewall.disable":       {op: agentv1.Op_OP_FIREWALL_DISABLE},
	"firewall.rule_add":      {op: agentv1.Op_OP_FIREWALL_RULE_ADD, params: []string{"action", "port", "proto", "from", "comment"}},
	"firewall.rule_delete":   {op: agentv1.Op_OP_FIREWALL_RULE_DELETE, params: []string{"num"}},
	"fail2ban.status":        {op: agentv1.Op_OP_FAIL2BAN_STATUS},
	"fail2ban.install":       {op: agentv1.Op_OP_FAIL2BAN_INSTALL},
	"fail2ban.configure":     {op: agentv1.Op_OP_FAIL2BAN_CONFIGURE, params: []string{"bantime", "findtime", "maxretry", "ignoreip"}},
	"fail2ban.ban":           {op: agentv1.Op_OP_FAIL2BAN_BAN, params: []string{"jail", "ip"}},
	"fail2ban.unban":         {op: agentv1.Op_OP_FAIL2BAN_UNBAN, params: []string{"jail", "ip"}},
	"web.status":             {op: agentv1.Op_OP_WEB_STATUS},
	"web.install":            {op: agentv1.Op_OP_WEB_INSTALL, params: []string{"php"}},
	"web.php_install":        {op: agentv1.Op_OP_PHP_INSTALL, params: []string{"version"}},
	"site.create":            {op: agentv1.Op_OP_SITE_CREATE, params: []string{"domain", "aliases", "type", "php_version", "proxy_target"}},
	"site.update":            {op: agentv1.Op_OP_SITE_UPDATE, params: []string{"domain", "aliases", "type", "php_version", "proxy_target", "force_https"}},
	"site.delete":            {op: agentv1.Op_OP_SITE_DELETE, params: []string{"domain", "delete_files"}},
	"site.tls":               {op: agentv1.Op_OP_SITE_TLS, params: []string{"domain", "mode", "email", "staging"}},
	"db.status":              {op: agentv1.Op_OP_DB_STATUS},
	"db.install":             {op: agentv1.Op_OP_DB_INSTALL},
	"db.create":              {op: agentv1.Op_OP_DB_CREATE, params: []string{"name", "user", "host", "password"}},
	"db.delete":              {op: agentv1.Op_OP_DB_DELETE, params: []string{"name", "drop_users"}},
	"db.user_save":           {op: agentv1.Op_OP_DB_USER_SAVE, params: []string{"user", "host", "password", "databases"}},
	"db.user_delete":         {op: agentv1.Op_OP_DB_USER_DELETE, params: []string{"user", "host"}},
	"files.list":             {op: agentv1.Op_OP_FILES_LIST, params: []string{"path"}},
	"files.read":             {op: agentv1.Op_OP_FILES_READ, params: []string{"path"}},
	"files.write":            {op: agentv1.Op_OP_FILES_WRITE, params: []string{"path", "content", "content_b64", "overwrite"}, maxParam: 3 << 20},
	"files.mkdir":            {op: agentv1.Op_OP_FILES_MKDIR, params: []string{"path"}},
	"files.delete":           {op: agentv1.Op_OP_FILES_DELETE, params: []string{"path"}},
	"files.rename":           {op: agentv1.Op_OP_FILES_RENAME, params: []string{"from", "to"}},
	"files.chmod":            {op: agentv1.Op_OP_FILES_CHMOD, params: []string{"path", "mode"}},
	"cron.list":              {op: agentv1.Op_OP_CRON_LIST},
	"cron.save":              {op: agentv1.Op_OP_CRON_SAVE, params: []string{"id", "schedule", "user", "command", "comment", "enabled"}},
	"cron.delete":            {op: agentv1.Op_OP_CRON_DELETE, params: []string{"id"}},
	"cron.run":               {op: agentv1.Op_OP_CRON_RUN, params: []string{"id"}},
	"services.list":          {op: agentv1.Op_OP_SERVICES_LIST},
	"services.action":        {op: agentv1.Op_OP_SERVICE_ACTION, params: []string{"name", "action"}},
	"logs.read":              {op: agentv1.Op_OP_LOGS_READ, params: []string{"source", "unit", "priority", "since", "grep", "path", "lines"}},
	"backup.list":            {op: agentv1.Op_OP_BACKUP_LIST},
	"backup.create":          {op: agentv1.Op_OP_BACKUP_CREATE, params: []string{"domain", "databases"}},
	"backup.delete":          {op: agentv1.Op_OP_BACKUP_DELETE, params: []string{"domain", "file"}},
	"backup.restore":         {op: agentv1.Op_OP_BACKUP_RESTORE, params: []string{"domain", "file", "files", "databases"}},
	"backup.policy":          {op: agentv1.Op_OP_BACKUP_POLICY, params: []string{"domain", "enabled", "every", "hour", "keep", "databases"}},
}

// secretParams are forwarded to the agent but never stored in job history
// or the audit log.
var secretParams = []string{"password"}

// bulkyParams (file contents) are replaced by their size when stored.
var bulkyParams = []string{"content", "content_b64"}

func redact(p map[string]string) map[string]string {
	out := make(map[string]string, len(p))
	for k, v := range p {
		switch {
		case slices.Contains(secretParams, k) && v != "":
			v = "[redacted]"
		case slices.Contains(bulkyParams, k):
			v = fmt.Sprintf("[%d bytes]", len(v))
		}
		out[k] = v
	}
	return out
}

// readOnlyOps only read state; they show up in job history but are not
// written to the audit log, which would otherwise fill with status polls.
var readOnlyOps = map[string]bool{
	"system.info": true, "packages.check_updates": true, "ssh_keys.list": true,
	"firewall.status": true, "fail2ban.status": true, "web.status": true, "db.status": true,
	"files.list": true, "files.read": true, "cron.list": true, "backup.list": true,
	"services.list": true, "logs.read": true,
}

func (s opSpec) checkParams(p map[string]string) error {
	for k, v := range p {
		if !slices.Contains(s.params, k) {
			return fmt.Errorf("unknown parameter %q", k)
		}
		limit := 16 << 10
		if s.maxParam > 0 {
			limit = s.maxParam
		}
		if len(v) > limit {
			return fmt.Errorf("parameter %q is too long", k)
		}
	}
	return nil
}

func (a *API) handleListJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := a.Store.ListJobs(r.Context(), r.PathValue("id"), 50)
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (a *API) handleGetJob(w http.ResponseWriter, r *http.Request) {
	j, err := a.Store.GetJob(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "job not found")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, j)
}

// ---- build progress ----

func (a *API) handleProgress(w http.ResponseWriter, r *http.Request) {
	b, err := os.ReadFile(a.Cfg.ProgressFile)
	if err != nil {
		writeErr(w, http.StatusNotFound, "progress file not found")
		return
	}
	if !json.Valid(b) {
		writeErr(w, http.StatusInternalServerError, "progress file is not valid JSON")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

// ---- live events (SSE) ----

func (a *API) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	events, unsub := a.Hub.Subscribe()
	defer unsub()

	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case e := <-events:
			b, _ := json.Marshal(e)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
	}
}

// ---- join tokens, enrollment, install ----

const joinTokenTTL = time.Hour

func (a *API) handleCreateJoinToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string `json:"name"`
		PlaybookID string `json:"playbook_id"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if len(req.Name) > 100 {
		writeErr(w, http.StatusBadRequest, "name too long")
		return
	}
	var baseline *string
	if req.PlaybookID != "" {
		if _, err := a.Store.GetPlaybook(r.Context(), req.PlaybookID); err != nil {
			writeErr(w, http.StatusBadRequest, "baseline playbook not found")
			return
		}
		baseline = &req.PlaybookID
	}
	raw, hash := randomToken()
	expires := time.Now().Add(joinTokenTTL)
	if err := a.Store.CreateJoinToken(r.Context(), hash, req.Name, expires, baseline); err != nil {
		a.internal(w, err)
		return
	}
	a.audit(r, "", "server.join_token", req.Name, map[string]any{"expires_at": expires, "baseline": req.PlaybookID}, true)
	cmd := fmt.Sprintf("curl -fsSL %s/install.sh | sudo sh -s -- --token %s --ca-sha256 %s",
		a.Cfg.PublicURL, raw, a.CA.Fingerprint())
	writeJSON(w, http.StatusCreated, map[string]any{
		"token": raw, "expires_at": expires, "ca_sha256": a.CA.Fingerprint(), "install_command": cmd,
	})
}

func (a *API) handleEnroll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		CSR      string `json:"csr_pem"`
		Hostname string `json:"hostname"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	ip := clientIP(r)
	if !a.limiter.allowed("enroll:" + ip) {
		writeErr(w, http.StatusTooManyRequests, "too many failed attempts")
		return
	}
	serverID, name, err := a.Store.Enroll(r.Context(), hashToken(req.Token), req.Hostname)
	if errors.Is(err, store.ErrNotFound) {
		a.limiter.failed("enroll:" + ip)
		a.audit(r, "agent", "server.enroll", req.Hostname, map[string]string{"error": "invalid or expired join token"}, false)
		writeErr(w, http.StatusUnauthorized, "invalid or expired join token")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	certPEM, err := a.CA.SignAgentCSR([]byte(req.CSR), serverID, 365*24*time.Hour)
	if err != nil {
		_ = a.Store.DeleteServer(r.Context(), serverID)
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	a.Log.Info("agent enrolled", "server", serverID, "name", name, "ip", ip)
	a.audit(r, "agent", "server.enroll", name, map[string]string{"server_id": serverID, "hostname": req.Hostname}, true)
	writeJSON(w, http.StatusOK, map[string]string{
		"server_id": serverID, "cert_pem": string(certPEM), "ca_pem": string(a.CA.CertPEM), "grpc_addr": a.Cfg.GRPCAddr,
	})
}

func (a *API) handleDownload(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	if file != "servermgr-agent-linux-amd64" && file != "servermgr-agent-linux-arm64" {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(a.Cfg.AgentBinDir, file)
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, path)
}

// handleCACert serves the panel CA so browsers can trust the HTTPS listener.
func (a *API) handleCACert(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/x-x509-ca-cert")
	w.Header().Set("Content-Disposition", `attachment; filename="servermgr-ca.crt"`)
	_, _ = w.Write(a.CA.CertPEM)
}

func (a *API) handleInstallScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript")
	fmt.Fprintf(w, installScript, a.Cfg.PublicURL)
}

const installScript = `#!/bin/sh
# servermgr agent installer
set -eu
PANEL_URL=%q
TOKEN=""
CA_SHA256=""
while [ $# -gt 0 ]; do
  case "$1" in
    --token) TOKEN="$2"; shift 2 ;;
    --ca-sha256) CA_SHA256="$2"; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 1 ;;
  esac
done
[ -n "$TOKEN" ] || { echo "--token is required" >&2; exit 1; }
[ -n "$CA_SHA256" ] || { echo "--ca-sha256 is required" >&2; exit 1; }
[ "$(id -u)" -eq 0 ] || { echo "run as root" >&2; exit 1; }

case "$(uname -m)" in
  x86_64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

TMP=$(mktemp)
curl -fsSL "$PANEL_URL/download/servermgr-agent-linux-$ARCH" -o "$TMP"
install -m 0755 "$TMP" /usr/local/bin/servermgr-agent
rm -f "$TMP"

/usr/local/bin/servermgr-agent enroll --panel "$PANEL_URL" --token "$TOKEN" --ca-sha256 "$CA_SHA256"

cat > /etc/systemd/system/servermgr-agent.service <<'EOF'
[Unit]
Description=servermgr agent
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/servermgr-agent run
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now servermgr-agent
echo "servermgr agent installed and running."
`
