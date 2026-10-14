package api

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/jacksonm36/servermgr/panel/internal/store"
)

// A permission is granted by a role. Server permissions ("server.*") can be
// granted on all servers or on a server group; the others are panel-wide and
// only come from roles granted on all servers.
type Permission struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Group  string `json:"group"`
	Server bool   `json:"server"` // can be scoped to a server group
}

var permissions = []Permission{
	{"server.view", "See servers, live stats, status and job history", "Servers", true},
	{"server.files_read", "Read file contents (may contain secrets)", "Servers", true},
	{"server.system", "Updates, reboot, services, cron and agent updates", "Servers", true},
	{"server.ssh_keys", "Add and remove SSH keys", "Servers", true},
	{"server.security", "Firewall, Fail2ban and CrowdSec", "Servers", true},
	{"server.web", "Web server, sites, SSL, apps and backups", "Servers", true},
	{"server.databases", "Databases and database users", "Servers", true},
	{"server.files", "Create, change and delete files", "Servers", true},
	{"server.docker", "Docker containers and compose stacks", "Servers", true},
	{"server.terminal", "Web terminal (a root shell)", "Servers", true},
	{"server.ansible", "Run Ansible playbooks on the server", "Servers", true},
	{"server.delete", "Remove the server from the panel", "Servers", true},

	{"servers.add", "Add servers (install command)", "Servers", false},
	{"keys.manage", "Manage the SSH key library and deploy keys", "Servers", false},

	{"automation.view", "See playbooks, schedules and alert rules", "Automation", false},
	{"playbooks.manage", "Create and edit playbooks", "Automation", false},
	{"playbooks.run", "Run playbooks and bulk actions", "Automation", false},
	{"schedules.manage", "Create and run schedules (they run unattended)", "Automation", false},
	{"alerts.manage", "Create and edit alert rules", "Automation", false},
	{"ansible.view", "See Ansible playbooks and runs", "Automation", false},
	{"ansible.run", "Run Ansible playbooks", "Automation", false},
	{"ansible.manage", "Set up Ansible, edit playbooks, prepare servers", "Automation", false},
	{"tofu.view", "See OpenTofu workspaces and runs", "Automation", false},
	{"tofu.run", "Plan and apply OpenTofu", "Automation", false},
	{"tofu.manage", "Edit OpenTofu workspaces, apply destroy plans", "Automation", false},

	{"crowdsec.view", "See CrowdSec bans and alerts", "Security", false},
	{"crowdsec.ban", "Ban and unban IPs everywhere", "Security", false},
	{"crowdsec.manage", "Edit the CrowdSec allowlist and console", "Security", false},

	{"proxmox.view", "See Proxmox clusters and guests", "Proxmox", false},
	{"proxmox.operate", "Power actions and snapshots", "Proxmox", false},
	{"proxmox.console", "Open a guest's console (full control of the guest)", "Proxmox", false},
	{"proxmox.guests", "Create and delete VMs, roll back snapshots", "Proxmox", false},
	{"proxmox.manage", "Add and change Proxmox clusters", "Proxmox", false},
	{"tenants.manage", "Create tenants and set up their isolation on Proxmox", "Panel", false},

	{"audit.view", "See the audit log", "Panel", false},
	{"notifications.manage", "Edit notification channels", "Panel", false},
	{"settings.manage", "Change panel settings (baselines)", "Panel", false},
	{"vault.manage", "Set up, unlock and lock the secrets vault", "Panel", false},
	{"users.manage", "Manage users, roles and groups (can grant any access)", "Panel", false},
}

func permKnown(key string) bool {
	return slices.ContainsFunc(permissions, func(p Permission) bool { return p.Key == key })
}

func serverPerm(key string) bool { return strings.HasPrefix(key, "server.") }

// Built-in roles. Their permissions live here, not in the database, so new
// permissions reach them automatically.
const (
	roleAdmin    = "admin"
	roleOperator = "operator"
	roleViewer   = "viewer"
	roleNone     = "none"
)

var builtinPerms = func() map[string][]string {
	all := make([]string, 0, len(permissions))
	for _, p := range permissions {
		all = append(all, p.Key)
	}
	operator := []string{}
	for _, p := range all {
		switch p {
		case "server.delete", "servers.add", "keys.manage", "notifications.manage", "settings.manage",
			"users.manage", "vault.manage", "ansible.manage", "tofu.manage", "proxmox.manage", "crowdsec.manage", "tenants.manage":
		default:
			operator = append(operator, p)
		}
	}
	return map[string][]string{
		roleAdmin:    all,
		roleOperator: operator,
		roleViewer:   {"server.view", "automation.view", "proxmox.view"},
		roleNone:     {},
	}
}()

// opPerm is the server permission an agent op needs.
func opPerm(op string) string {
	if viewerOp(op) {
		return "server.view"
	}
	switch op {
	case "files.read", "compose.read":
		return "server.files_read"
	case "ansible.prepare", "ansible.revoke":
		return "server.ansible"
	}
	prefix, _, _ := strings.Cut(op, ".")
	switch prefix {
	case "system", "packages", "services", "cron", "agent", "logs":
		return "server.system"
	case "ssh_keys":
		return "server.ssh_keys"
	case "firewall", "fail2ban", "crowdsec":
		return "server.security"
	case "web", "site", "app", "backup":
		return "server.web"
	case "db":
		return "server.databases"
	case "files":
		return "server.files"
	case "docker", "compose":
		return "server.docker"
	}
	// Unknown ops need full rights.
	return "server.delete"
}

// viewerOp reports whether op only reads status: anyone who can see the
// server may run it. Reading file contents is excluded (secrets).
func viewerOp(op string) bool {
	return readOnlyOps[op] && op != "files.read" && op != "compose.read"
}

// tenantPerms are the only panel-wide permissions a tenant's user can
// hold, whatever their roles say. Everything else (users, settings,
// automation, clusters) belongs to the platform.
var tenantPerms = map[string]bool{
	"proxmox.view": true, "proxmox.operate": true, "proxmox.console": true, "proxmox.guests": true, "audit.view": true,
}

// Access is what one user may do. A nil *Access is the panel itself
// (schedules, baselines, provisioning) and may do everything.
type Access struct {
	Role string
	// Tenant is set for a tenant's users: they only reach the tenant's
	// Proxmox pools and the servers of its server group.
	Tenant *store.Tenant
	global map[string]bool
	// scoped holds server permissions granted per server group.
	scoped map[int64]map[string]bool
	// groupsOf maps a server ID to its server groups (only loaded when
	// there are scoped grants).
	groupsOf map[string][]int64
}

func (ac *Access) Can(perm string) bool {
	return ac == nil || ac.global[perm]
}

func (ac *Access) CanOn(perm, serverID string) bool {
	if ac == nil || ac.global[perm] {
		return true
	}
	for _, g := range ac.groupsOf[serverID] {
		if ac.scoped[g][perm] {
			return true
		}
	}
	return false
}

// CanOnGroup reports whether perm is granted on every server of a group
// now and later: globally, or by a grant on that same group.
func (ac *Access) CanOnGroup(perm string, groupID int64) bool {
	return ac == nil || ac.global[perm] || ac.scoped[groupID][perm]
}

// CanAnywhere reports whether perm is granted on at least some servers.
func (ac *Access) CanAnywhere(perm string) bool {
	if ac.Can(perm) {
		return true
	}
	for _, ps := range ac.scoped {
		if ps[perm] {
			return true
		}
	}
	return false
}

func (ac *Access) Global() []string {
	out := []string{}
	for _, p := range permissions {
		if ac.Can(p.Key) {
			out = append(out, p.Key)
		}
	}
	return out
}

// PerServer lists server permissions granted through server groups only
// (on top of Global) for each server.
func (ac *Access) PerServer() map[string][]string {
	out := map[string][]string{}
	if ac == nil {
		return out
	}
	for sid, groups := range ac.groupsOf {
		var ps []string
		for _, p := range permissions {
			if !p.Server || ac.global[p.Key] {
				continue
			}
			for _, g := range groups {
				if ac.scoped[g][p.Key] {
					ps = append(ps, p.Key)
					break
				}
			}
		}
		if len(ps) > 0 {
			out[sid] = ps
		}
	}
	return out
}

func rolePerms(g store.Grant) []string {
	if g.Builtin {
		return builtinPerms[g.RoleName]
	}
	return g.Permissions
}

// loadAccess computes a user's effective permissions from their main role,
// their own role grants and those of their user groups.
func (a *API) loadAccess(ctx context.Context, ad *store.Admin) (*Access, error) {
	ac := &Access{Role: ad.Role, global: map[string]bool{}, scoped: map[int64]map[string]bool{}}
	grants, err := a.Store.Grants(ctx, ad.ID, ad.Role)
	if err != nil {
		return nil, err
	}
	for _, g := range grants {
		for _, p := range rolePerms(g) {
			switch {
			case g.ServerGroupID == nil:
				ac.global[p] = true
			case serverPerm(p):
				if ac.scoped[*g.ServerGroupID] == nil {
					ac.scoped[*g.ServerGroupID] = map[string]bool{}
				}
				ac.scoped[*g.ServerGroupID][p] = true
			}
		}
	}
	if ad.TenantID != nil {
		t, err := a.Store.GetTenant(ctx, *ad.TenantID)
		if err != nil {
			return nil, err
		}
		ac.Tenant = t
		ac.clampToTenant()
	}
	if len(ac.scoped) > 0 {
		if ac.groupsOf, err = a.Store.ServerGroupsByServer(ctx); err != nil {
			return nil, err
		}
	}
	return ac, nil
}

// clampToTenant keeps a tenant user inside the tenant: panel-wide
// permissions shrink to tenantPerms, and server permissions only apply to
// the tenant's server group (none if it has no group).
func (ac *Access) clampToTenant() {
	var group int64
	if ac.Tenant.ServerGroupID != nil {
		group = *ac.Tenant.ServerGroupID
	}
	for p := range ac.global {
		if serverPerm(p) && group != 0 {
			if ac.scoped[group] == nil {
				ac.scoped[group] = map[string]bool{}
			}
			ac.scoped[group][p] = true
		}
		if !tenantPerms[p] {
			delete(ac.global, p)
		}
	}
	for g := range ac.scoped {
		if g != group {
			delete(ac.scoped, g)
		}
	}
}

// TenantID is the user's tenant, nil for platform users.
func (ac *Access) TenantID() *int64 {
	if ac == nil || ac.Tenant == nil {
		return nil
	}
	return &ac.Tenant.ID
}

// tenantGroup is the tenant's server group, 0 if none or no tenant.
func (ac *Access) tenantGroup() int64 {
	if ac == nil || ac.Tenant == nil || ac.Tenant.ServerGroupID == nil {
		return 0
	}
	return *ac.Tenant.ServerGroupID
}

type accessKey struct{}

func accessFrom(ctx context.Context) *Access {
	ac, _ := ctx.Value(accessKey{}).(*Access)
	if ac == nil {
		// No session (should not happen behind requireAuth): deny all.
		return &Access{}
	}
	return ac
}

func forbid(w http.ResponseWriter, perm string) {
	label := perm
	for _, p := range permissions {
		if p.Key == perm {
			label = strings.ToLower(p.Label[:1]) + p.Label[1:]
			break
		}
	}
	writeErr(w, http.StatusForbidden, "you don't have permission to "+strings.TrimSuffix(label, ".")+" ("+perm+")")
}

// need wraps a handler with a panel-wide permission.
func need(perm string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !accessFrom(r.Context()).Can(perm) {
			forbid(w, perm)
			return
		}
		h(w, r)
	}
}

// needOn wraps a /servers/{id}/… handler with a server permission.
func needOn(perm string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !accessFrom(r.Context()).CanOn(perm, r.PathValue("id")) {
			forbid(w, perm)
			return
		}
		h(w, r)
	}
}
