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
	"github.com/jacksonm36/servermgr/panel/internal/vault"
)

type Config struct {
	// PublicURL is the panel's HTTP base URL as reachable by agents,
	// e.g. http://10.0.1.240:8090.
	PublicURL string
	// GRPCAddr is host:port agents dial for the mTLS stream.
	GRPCAddr string
	// AgentBinDir holds servermgr-agent-linux-<arch> binaries for download.
	AgentBinDir string
	// DataDir holds panel state (PKI, Ansible venv and key).
	DataDir string
}

type API struct {
	Cfg   Config
	Store *store.Store
	Hub   *hub.Hub
	CA    *pki.CA
	Log   *slog.Logger
	Web   fs.FS // built UI (web/dist), may be nil
	// SecretKey (32 bytes) encrypts TOTP secrets at rest, and other
	// secrets until the vault is set up.
	SecretKey []byte
	// Vault seals stored secrets with a key that only lives in memory.
	Vault *vault.Vault
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
	// OpenTofu http state backend: basic auth with a per-run token.
	for _, m := range []string{"GET", "POST", "DELETE", "LOCK", "UNLOCK"} {
		mux.HandleFunc(m+" /tofu/state/{id}", a.handleTofuState)
	}
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
	authed.HandleFunc("GET /api/v1/servers/{id}", needOn("server.view", a.handleGetServer))
	authed.HandleFunc("DELETE /api/v1/servers/{id}", needOn("server.delete", a.handleDeleteServer))
	authed.HandleFunc("PUT /api/v1/servers/{id}/groups", need("users.manage", a.handleSetServerGroups))
	authed.HandleFunc("GET /api/v1/servers/{id}/metrics", needOn("server.view", a.handleMetrics))
	authed.HandleFunc("GET /api/v1/servers/{id}/terminal", needOn("server.terminal", a.handleTerminal))
	authed.HandleFunc("GET /api/v1/servers/{id}/jobs", needOn("server.view", a.handleListJobs))
	authed.HandleFunc("POST /api/v1/servers/{id}/jobs", a.handleCreateJob) // per-op check in Dispatch
	authed.HandleFunc("GET /api/v1/jobs/{id}", a.handleGetJob)
	authed.HandleFunc("GET /api/v1/jobs", a.handleRecentJobs)
	authed.HandleFunc("POST /api/v1/bulk", need("playbooks.run", a.handleBulk))
	authed.HandleFunc("GET /api/v1/playbooks", need("automation.view", a.handleListPlaybooks))
	authed.HandleFunc("POST /api/v1/playbooks", need("playbooks.manage", a.handleSavePlaybook))
	authed.HandleFunc("PUT /api/v1/playbooks/{id}", need("playbooks.manage", a.handleSavePlaybook))
	authed.HandleFunc("DELETE /api/v1/playbooks/{id}", need("playbooks.manage", a.handleDeletePlaybook))
	authed.HandleFunc("POST /api/v1/playbooks/{id}/run", need("playbooks.run", a.handleRunPlaybook))
	authed.HandleFunc("GET /api/v1/playbook-runs", need("automation.view", a.handleListRuns))
	authed.HandleFunc("GET /api/v1/schedules", need("automation.view", a.handleListSchedules))
	authed.HandleFunc("GET /api/v1/channels", need("notifications.manage", a.handleListChannels))
	authed.HandleFunc("POST /api/v1/channels", need("notifications.manage", a.handleSaveChannel))
	authed.HandleFunc("PUT /api/v1/channels/{id}", need("notifications.manage", a.handleSaveChannel))
	authed.HandleFunc("DELETE /api/v1/channels/{id}", need("notifications.manage", a.handleDeleteChannel))
	authed.HandleFunc("POST /api/v1/channels/{id}/test", need("notifications.manage", a.handleTestChannel))
	authed.HandleFunc("GET /api/v1/alert-rules", need("automation.view", a.handleListRules))
	authed.HandleFunc("POST /api/v1/alert-rules", need("alerts.manage", a.handleSaveRule))
	authed.HandleFunc("PUT /api/v1/alert-rules/{id}", need("alerts.manage", a.handleSaveRule))
	authed.HandleFunc("DELETE /api/v1/alert-rules/{id}", need("alerts.manage", a.handleDeleteRule))
	authed.HandleFunc("GET /api/v1/alerts", a.handleListAlerts) // filtered per server
	authed.HandleFunc("GET /api/v1/settings/baseline", need("automation.view", a.handleGetBaseline))
	authed.HandleFunc("PUT /api/v1/settings/baseline", need("settings.manage", a.handleSetBaseline))
	authed.HandleFunc("POST /api/v1/schedules", need("schedules.manage", a.handleSaveSchedule))
	authed.HandleFunc("PUT /api/v1/schedules/{id}", need("schedules.manage", a.handleSaveSchedule))
	authed.HandleFunc("DELETE /api/v1/schedules/{id}", need("schedules.manage", a.handleDeleteSchedule))
	authed.HandleFunc("POST /api/v1/schedules/{id}/run", need("schedules.manage", a.handleRunScheduleNow))
	authed.HandleFunc("GET /api/v1/audit", need("audit.view", a.handleListAudit))
	authed.HandleFunc("POST /api/v1/join-tokens", need("servers.add", a.handleCreateJoinToken))
	authed.HandleFunc("GET /api/v1/events", a.handleEvents) // filtered per server
	authed.HandleFunc("GET /api/v1/agent-build", a.handleAgentBuild)
	authed.HandleFunc("GET /api/v1/apps", a.handleListApps)
	authed.HandleFunc("GET /api/v1/proxmox", need("proxmox.view", a.handleListPVE))
	authed.HandleFunc("POST /api/v1/proxmox/probe", need("proxmox.manage", a.handlePVEProbe))
	authed.HandleFunc("POST /api/v1/proxmox", need("proxmox.manage", a.handleSavePVE))
	authed.HandleFunc("PUT /api/v1/proxmox/{id}", need("proxmox.manage", a.handleSavePVE))
	authed.HandleFunc("DELETE /api/v1/proxmox/{id}", need("proxmox.manage", a.handleDeletePVE))
	authed.HandleFunc("GET /api/v1/proxmox/provisions", need("proxmox.operate", a.handleListProvisions))
	authed.HandleFunc("GET /api/v1/proxmox/{id}/resources", need("proxmox.view", a.handlePVEResources))
	authed.HandleFunc("POST /api/v1/proxmox/{id}/provision", need("proxmox.manage", a.handlePVEProvision))
	authed.HandleFunc("POST /api/v1/proxmox/{id}/guests/{node}/{type}/{vmid}/power", need("proxmox.operate", a.handlePVEPower))
	authed.HandleFunc("DELETE /api/v1/proxmox/{id}/guests/{node}/{type}/{vmid}", need("proxmox.manage", a.handlePVEDeleteGuest))
	authed.HandleFunc("GET /api/v1/proxmox/{id}/guests/{node}/{type}/{vmid}/snapshots", need("proxmox.view", a.handlePVESnapshots))
	authed.HandleFunc("POST /api/v1/proxmox/{id}/guests/{node}/{type}/{vmid}/snapshots", need("proxmox.operate", a.handlePVESnapshotCreate))
	authed.HandleFunc("DELETE /api/v1/proxmox/{id}/guests/{node}/{type}/{vmid}/snapshots/{snap}", need("proxmox.operate", a.handlePVESnapshotDelete))
	authed.HandleFunc("POST /api/v1/proxmox/{id}/guests/{node}/{type}/{vmid}/snapshots/{snap}/rollback", need("proxmox.manage", a.handlePVESnapshotRollback))
	authed.HandleFunc("GET /api/v1/tofu", need("tofu.view", a.handleTofuOverview))
	authed.HandleFunc("POST /api/v1/tofu/setup", need("tofu.manage", a.handleTofuSetup))
	authed.HandleFunc("POST /api/v1/tofu/workspaces", need("tofu.manage", a.handleSaveTofuWorkspace))
	authed.HandleFunc("PUT /api/v1/tofu/workspaces/{id}", need("tofu.manage", a.handleSaveTofuWorkspace))
	authed.HandleFunc("DELETE /api/v1/tofu/workspaces/{id}", need("tofu.manage", a.handleDeleteTofuWorkspace))
	authed.HandleFunc("POST /api/v1/tofu/workspaces/{id}/unlock", need("tofu.manage", a.handleTofuUnlock))
	authed.HandleFunc("POST /api/v1/tofu/workspaces/{id}/plan", need("tofu.run", a.handleTofuPlan))
	authed.HandleFunc("POST /api/v1/tofu/runs/{run}/apply", need("tofu.run", a.handleTofuApply))
	authed.HandleFunc("GET /api/v1/tofu/runs", need("tofu.view", a.handleListTofuRuns))
	authed.HandleFunc("GET /api/v1/tofu/runs/{id}", need("tofu.view", a.handleGetTofuRun))
	authed.HandleFunc("POST /api/v1/tofu/runs/{id}/cancel", need("tofu.run", a.handleCancelTofuRun))
	authed.HandleFunc("GET /api/v1/ansible", need("ansible.view", a.handleAnsibleOverview))
	authed.HandleFunc("POST /api/v1/ansible/setup", need("ansible.manage", a.handleAnsibleSetup))
	authed.HandleFunc("POST /api/v1/servers/{id}/ansible/prepare", need("ansible.manage", needOn("server.ansible", a.handleAnsiblePrepare)))
	authed.HandleFunc("POST /api/v1/servers/{id}/ansible/revoke", need("ansible.manage", needOn("server.ansible", a.handleAnsibleRevoke)))
	authed.HandleFunc("GET /api/v1/ansible/playbooks", need("ansible.view", a.handleListAnsiblePlaybooks))
	authed.HandleFunc("POST /api/v1/ansible/playbooks", need("ansible.manage", a.handleSaveAnsiblePlaybook))
	authed.HandleFunc("PUT /api/v1/ansible/playbooks/{id}", need("ansible.manage", a.handleSaveAnsiblePlaybook))
	authed.HandleFunc("DELETE /api/v1/ansible/playbooks/{id}", need("ansible.manage", a.handleDeleteAnsiblePlaybook))
	authed.HandleFunc("POST /api/v1/ansible/playbooks/{id}/run", need("ansible.run", a.handleRunAnsiblePlaybook))
	authed.HandleFunc("GET /api/v1/ansible/runs", need("ansible.view", a.handleListAnsibleRuns))
	authed.HandleFunc("GET /api/v1/ansible/runs/{id}", need("ansible.view", a.handleGetAnsibleRun))
	authed.HandleFunc("POST /api/v1/ansible/runs/{id}/cancel", need("ansible.run", a.handleCancelAnsibleRun))
	authed.HandleFunc("GET /api/v1/crowdsec", need("crowdsec.view", a.handleCrowdSecOverview))
	authed.HandleFunc("POST /api/v1/crowdsec/decisions", need("crowdsec.ban", a.handleCrowdSecBan))
	authed.HandleFunc("DELETE /api/v1/crowdsec/decisions/{id}", need("crowdsec.ban", a.handleCrowdSecUnban))
	authed.HandleFunc("POST /api/v1/crowdsec/allowlist", need("crowdsec.manage", a.handleCrowdSecAllow))
	authed.HandleFunc("POST /api/v1/crowdsec/enroll", need("crowdsec.manage", a.handleCrowdSecEnroll))
	authed.HandleFunc("POST /api/v1/servers/{id}/crowdsec/install", needOn("server.security", a.handleCrowdSecInstall))
	authed.HandleFunc("POST /api/v1/servers/{id}/crowdsec/remove", needOn("server.security", a.handleCrowdSecRemove))
	authed.HandleFunc("POST /api/v1/servers/{id}/apps", needOn("server.web", a.handleInstallApp))
	authed.HandleFunc("POST /api/v1/servers/{id}/apps/{domain}/update", needOn("server.web", a.handleUpdateApp))
	authed.HandleFunc("POST /api/v1/servers/{id}/apps/{domain}/uninstall", needOn("server.web", a.handleUninstallApp))
	authed.HandleFunc("POST /api/v1/servers/{id}/agent-update", needOn("server.system", a.handleAgentUpdate))

	// Users, roles and groups.
	authed.HandleFunc("GET /api/v1/permissions", a.handleListPermissions)
	authed.HandleFunc("GET /api/v1/vault", a.handleVaultStatus)
	authed.HandleFunc("POST /api/v1/vault/setup", need("vault.manage", a.handleVaultSetup))
	authed.HandleFunc("POST /api/v1/vault/unlock", need("vault.manage", a.handleVaultUnlock))
	authed.HandleFunc("POST /api/v1/vault/lock", need("vault.manage", a.handleVaultLock))
	authed.HandleFunc("POST /api/v1/vault/passphrase", need("vault.manage", a.handleVaultPassphrase))
	authed.HandleFunc("GET /api/v1/ssh-keys", need("keys.manage", a.handleListSSHKeys))
	authed.HandleFunc("POST /api/v1/ssh-keys", need("keys.manage", a.handleImportSSHKeys))
	authed.HandleFunc("POST /api/v1/ssh-keys/parse", need("keys.manage", a.handleParseSSHKeys))
	authed.HandleFunc("POST /api/v1/ssh-keys/scan", need("keys.manage", a.handleScanSSHKeys))
	authed.HandleFunc("PUT /api/v1/ssh-keys/{id}", need("keys.manage", a.handleRenameSSHKey))
	authed.HandleFunc("DELETE /api/v1/ssh-keys/{id}", need("keys.manage", a.handleDeleteSSHKey))
	authed.HandleFunc("GET /api/v1/ssh-keys/{id}/public", need("keys.manage", a.handleSSHKeyPublic))
	authed.HandleFunc("POST /api/v1/ssh-keys/{id}/deploy", need("keys.manage", a.handleDeploySSHKey))     // + server.ssh_keys per server
	authed.HandleFunc("POST /api/v1/ssh-keys/{id}/undeploy", need("keys.manage", a.handleUndeploySSHKey)) // + server.ssh_keys per server

	authed.HandleFunc("GET /api/v1/users", need("users.manage", a.handleListUsers))
	authed.HandleFunc("POST /api/v1/users", need("users.manage", a.handleCreateUser))
	authed.HandleFunc("PUT /api/v1/users/{id}", need("users.manage", a.handleUpdateUser))
	authed.HandleFunc("POST /api/v1/users/{id}/password", need("users.manage", a.handleResetUserPassword))
	authed.HandleFunc("POST /api/v1/users/{id}/sign-out", need("users.manage", a.handleEndUserSessions))
	authed.HandleFunc("DELETE /api/v1/users/{id}", need("users.manage", a.handleDeleteUser))
	authed.HandleFunc("GET /api/v1/roles", need("users.manage", a.handleListRoles))
	authed.HandleFunc("POST /api/v1/roles", need("users.manage", a.handleSaveRole))
	authed.HandleFunc("PUT /api/v1/roles/{id}", need("users.manage", a.handleSaveRole))
	authed.HandleFunc("DELETE /api/v1/roles/{id}", need("users.manage", a.handleDeleteRole))
	authed.HandleFunc("GET /api/v1/user-groups", need("users.manage", a.handleListUserGroups))
	authed.HandleFunc("POST /api/v1/user-groups", need("users.manage", a.handleSaveUserGroup))
	authed.HandleFunc("PUT /api/v1/user-groups/{id}", need("users.manage", a.handleSaveUserGroup))
	authed.HandleFunc("DELETE /api/v1/user-groups/{id}", need("users.manage", a.handleDeleteUserGroup))
	authed.HandleFunc("GET /api/v1/server-groups", a.handleListServerGroups) // filtered to visible servers
	authed.HandleFunc("POST /api/v1/server-groups", need("users.manage", a.handleSaveServerGroup))
	authed.HandleFunc("PUT /api/v1/server-groups/{id}", need("users.manage", a.handleSaveServerGroup))
	authed.HandleFunc("DELETE /api/v1/server-groups/{id}", need("users.manage", a.handleDeleteServerGroup))
	authed.HandleFunc("GET /api/v1/grants", need("users.manage", a.handleListGrants))
	authed.HandleFunc("POST /api/v1/grants", need("users.manage", a.handleCreateGrant))
	authed.HandleFunc("DELETE /api/v1/grants/{id}", need("users.manage", a.handleDeleteGrant))
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
	Groups []int64     `json:"groups"`
}

func (a *API) view(s *store.Server, groups map[string][]int64) serverView {
	return serverView{Server: s, Online: a.Hub.Online(s.ID), Latest: a.Hub.LatestMetric(s.ID), Groups: orEmpty(groups[s.ID])}
}

// handleListServers lists the servers the caller may see.
func (a *API) handleListServers(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.ListServers(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	groups, err := a.Store.ServerGroupsByServer(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	ac := accessFrom(r.Context())
	out := make([]serverView, 0, len(list))
	for _, s := range list {
		if ac.CanOn("server.view", s.ID) {
			out = append(out, a.view(s, groups))
		}
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
	groups, err := a.Store.ServerGroupsByServer(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a.view(s, groups))
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
	"site.tls":               {op: agentv1.Op_OP_SITE_TLS, params: []string{"domain", "mode", "email", "staging", "acme_directory"}},
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
	"docker.status":          {op: agentv1.Op_OP_DOCKER_STATUS},
	"docker.install":         {op: agentv1.Op_OP_DOCKER_INSTALL},
	"docker.container":       {op: agentv1.Op_OP_DOCKER_CONTAINER, params: []string{"id", "action"}},
	"docker.logs":            {op: agentv1.Op_OP_DOCKER_LOGS, params: []string{"id", "lines"}},
	"docker.image_remove":    {op: agentv1.Op_OP_DOCKER_IMAGE_REMOVE, params: []string{"id"}},
	"docker.prune":           {op: agentv1.Op_OP_DOCKER_PRUNE},
	"compose.read":           {op: agentv1.Op_OP_COMPOSE_READ, params: []string{"name"}},
	"compose.save":           {op: agentv1.Op_OP_COMPOSE_SAVE, params: []string{"name", "yaml", "deploy"}, maxParam: 256 << 10},
	"compose.action":         {op: agentv1.Op_OP_COMPOSE_ACTION, params: []string{"name", "action"}},
	"agent.update":           {op: agentv1.Op_OP_AGENT_UPDATE, params: []string{"sha256", "version", "current"}},
	"app.deploy":             {op: agentv1.Op_OP_APP_DEPLOY, params: []string{"domain", "app", "name", "version", "steps", "version_steps", "vars", "compose"}, maxParam: 64 << 10},
	"crowdsec.status":        {op: agentv1.Op_OP_CROWDSEC_STATUS},
	"ansible.prepare":        {op: agentv1.Op_OP_ANSIBLE_PREPARE, params: []string{"address"}}, // key + source address are added by Dispatch
	"ansible.revoke":         {op: agentv1.Op_OP_ANSIBLE_REVOKE},
	"crowdsec.install":       {op: agentv1.Op_OP_CROWDSEC_INSTALL, params: []string{"trust_lan"}}, // credentials are added by Dispatch
	"crowdsec.hub":           {op: agentv1.Op_OP_CROWDSEC_HUB, params: []string{"action", "items"}},
	"crowdsec.remove":        {op: agentv1.Op_OP_CROWDSEC_REMOVE},
	"site.access":            {op: agentv1.Op_OP_SITE_ACCESS, params: []string{"domain", "auth", "auth_user", "auth_password", "allow_ips"}},
	"app.update":             {op: agentv1.Op_OP_APP_UPDATE, params: []string{"domain", "steps", "version_steps", "vars"}, maxParam: 64 << 10},
	"backup.list":            {op: agentv1.Op_OP_BACKUP_LIST},
	"backup.create":          {op: agentv1.Op_OP_BACKUP_CREATE, params: []string{"domain", "databases"}},
	"backup.delete":          {op: agentv1.Op_OP_BACKUP_DELETE, params: []string{"domain", "file"}},
	"backup.restore":         {op: agentv1.Op_OP_BACKUP_RESTORE, params: []string{"domain", "file", "files", "databases"}},
	"backup.policy":          {op: agentv1.Op_OP_BACKUP_POLICY, params: []string{"domain", "enabled", "every", "hour", "keep", "databases"}},
}

// secretParams are forwarded to the agent but never stored in job history
// or the audit log.
var secretParams = []string{"password", "vars", "auth_password", "bouncer_key"}

// bulkyParams (file contents) are replaced by their size when stored.
var bulkyParams = []string{"content", "content_b64", "yaml"}

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
	"services.list": true, "logs.read": true, "crowdsec.status": true,
	"docker.status": true, "docker.logs": true, "compose.read": true,
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
	for _, j := range jobs {
		hideFromViewer(r, j)
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
	if !accessFrom(r.Context()).CanOn("server.view", j.ServerID) {
		writeErr(w, http.StatusNotFound, "job not found")
		return
	}
	hideFromViewer(r, j)
	writeJSON(w, http.StatusOK, j)
}

// hideFromViewer blanks the output of jobs the user may not run themselves
// (file contents can hold secrets), so they cannot read an admin's result.
func hideFromViewer(r *http.Request, j *store.Job) {
	if !accessFrom(r.Context()).CanOn(opPerm(j.Op), j.ServerID) {
		j.Result, j.Log = nil, ""
	}
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
	ac := accessFrom(r.Context())

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
			if e.ServerID != "" && !ac.CanOn("server.view", e.ServerID) {
				continue
			}
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
OOMPolicy=continue

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now servermgr-agent
echo "servermgr agent installed and running."
`
