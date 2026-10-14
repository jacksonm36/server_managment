package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jacksonm36/servermgr/panel/internal/proxmox"
	"github.com/jacksonm36/servermgr/panel/internal/store"
)

// Tenants keep customers or teams apart. Isolation on a Proxmox cluster is
// enforced by Proxmox itself: each tenant gets a pool, a Proxmox user
// (sm-<slug>@pve) with ACLs on exactly that pool, the storages, bridges
// and templates it was given, and an API token of that user. The panel
// talks to Proxmox with that token for the tenant's users, and checks the
// pool on every guest route as well.

var (
	tenantSlugRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)
	pvePoolRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{1,39}$`)
)

const tenantTokenName = "servermgr"

func pveTenantUser(slug string) string { return "sm-" + slug + "@pve" }

type tenantView struct {
	*store.Tenant
	Clusters []*store.TenantCluster `json:"clusters"`
}

func (a *API) handleListTenants(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.ListTenants(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	tcs, err := a.Store.ListTenantClusters(r.Context(), 0)
	if err != nil {
		a.internal(w, err)
		return
	}
	out := make([]tenantView, 0, len(list))
	for _, t := range list {
		v := tenantView{Tenant: t, Clusters: []*store.TenantCluster{}}
		for _, tc := range tcs {
			if tc.TenantID == t.ID {
				v.Clusters = append(v.Clusters, tc)
			}
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleSaveTenant(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name          string `json:"name"`
		Slug          string `json:"slug"`
		Description   string `json:"description"`
		ServerGroupID *int64 `json:"server_group_id"`
		MaxGuests     int    `json:"max_guests"`
		MaxCores      int    `json:"max_cores"`
		MaxMemoryMB   int64  `json:"max_memory_mb"`
		MaxDiskGB     int64  `json:"max_disk_gb"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	t := &store.Tenant{Name: strings.TrimSpace(req.Name), Description: strings.TrimSpace(req.Description), ServerGroupID: req.ServerGroupID,
		MaxGuests: req.MaxGuests, MaxCores: req.MaxCores, MaxMemoryMB: req.MaxMemoryMB, MaxDiskGB: req.MaxDiskGB}
	if r.PathValue("id") != "" {
		id, ok := pathID(r)
		if !ok {
			writeErr(w, http.StatusBadRequest, "bad id")
			return
		}
		t.ID = id
	} else {
		t.Slug = strings.TrimSpace(req.Slug)
		if !tenantSlugRe.MatchString(t.Slug) {
			writeErr(w, http.StatusBadRequest, "short name: 2-31 lowercase letters, digits or dashes, starting with a letter")
			return
		}
	}
	if !groupNameRe.MatchString(t.Name) {
		writeErr(w, http.StatusBadRequest, "name: 1-48 letters, digits, spaces, . _ -")
		return
	}
	if len(t.Description) > 300 {
		writeErr(w, http.StatusBadRequest, "description is too long (max 300)")
		return
	}
	if t.MaxGuests < 0 || t.MaxCores < 0 || t.MaxMemoryMB < 0 || t.MaxDiskGB < 0 {
		writeErr(w, http.StatusBadRequest, "limits cannot be negative (0 means no limit)")
		return
	}
	if err := a.Store.SaveTenant(r.Context(), t); err != nil {
		a.storeErr(w, err, "tenant or server group")
		return
	}
	a.audit(r, "", "tenant.save", t.Name, req, true)
	writeJSON(w, http.StatusOK, t)
}

// handleDeleteTenant removes the tenant's Proxmox users (their tokens and
// ACLs go with them) and then the tenant. Pools and guests stay.
func (a *API) handleDeleteTenant(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	t, err := a.Store.GetTenant(r.Context(), id)
	if err != nil {
		a.storeErr(w, err, "tenant")
		return
	}
	if t.Users > 0 {
		writeErr(w, http.StatusConflict, store.ErrTenantInUse.Error())
		return
	}
	tcs, err := a.Store.ListTenantClusters(r.Context(), id)
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, tc := range tcs {
		if err := a.teardownTenantCluster(r.Context(), t, tc); err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
	}
	if err := a.Store.DeleteTenant(r.Context(), id); err != nil {
		a.storeErr(w, err, "tenant")
		return
	}
	a.audit(r, "", "tenant.delete", t.Name, nil, true)
	w.WriteHeader(http.StatusNoContent)
}

// handleTenantOptions lists what a tenant can be given on a cluster.
func (a *API) handleTenantOptions(w http.ResponseWriter, r *http.Request) {
	_, cl, err := a.pveClient(r.Context(), r.PathValue("cluster"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "cluster not found")
		return
	}
	res, err := cl.Resources(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	storages, bridges, templates := map[string]bool{}, map[string]bool{}, []map[string]any{}
	for _, x := range res {
		switch {
		case x.Type == "storage":
			storages[x.Storage] = true
		case x.Type == "qemu" && x.Template == 1:
			templates = append(templates, map[string]any{"vmid": x.VMID, "name": x.Name, "node": x.Node})
		case x.Type == "node" && x.Status == "online":
			var ifs []struct {
				Iface string `json:"iface"`
			}
			if err := cl.Do(r.Context(), http.MethodGet, "/nodes/"+url.PathEscape(x.Node)+"/network", url.Values{"type": {"any_bridge"}}, &ifs); err == nil {
				for _, i := range ifs {
					bridges[i.Iface] = true
				}
			}
		}
	}
	var pools []pvePool
	_ = cl.Do(r.Context(), http.MethodGet, "/pools", nil, &pools)
	sortedKeys := func(m map[string]bool) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	poolNames := []string{}
	for _, p := range pools {
		poolNames = append(poolNames, p.PoolID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"storages": sortedKeys(storages), "bridges": sortedKeys(bridges), "templates": templates, "pools": poolNames})
}

type tenantClusterReq struct {
	Pool      string   `json:"pool"`
	Storages  []string `json:"storages"`
	Bridges   []string `json:"bridges"`
	Templates []int    `json:"templates"`
	// Rotate replaces the tenant's Proxmox token.
	Rotate bool `json:"rotate"`
}

// handleSetupTenantCluster creates or updates a tenant's isolation on a
// cluster: pool, Proxmox user, ACLs and token. It is safe to run again.
func (a *API) handleSetupTenantCluster(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	var req tenantClusterReq
	if !readJSON(w, r, &req) {
		return
	}
	t, err := a.Store.GetTenant(r.Context(), id)
	if err != nil {
		a.storeErr(w, err, "tenant")
		return
	}
	c, cl, err := a.pveClient(r.Context(), r.PathValue("cluster"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "cluster not found")
		return
	}
	req.Pool = strings.TrimSpace(req.Pool)
	if req.Pool == "" {
		req.Pool = "tenant-" + t.Slug
	}
	if !pvePoolRe.MatchString(req.Pool) {
		writeErr(w, http.StatusBadRequest, "pool: 2-40 letters, digits, . _ -")
		return
	}
	for _, s := range req.Storages {
		if !pveStorage.MatchString(s) {
			writeErr(w, http.StatusBadRequest, "invalid storage "+strconv.Quote(s))
			return
		}
	}
	for _, b := range req.Bridges {
		if !pveBridge.MatchString(b) {
			writeErr(w, http.StatusBadRequest, "invalid bridge "+strconv.Quote(b))
			return
		}
	}
	for _, v := range req.Templates {
		if v < 100 || v > 999999999 {
			writeErr(w, http.StatusBadRequest, "invalid template id")
			return
		}
	}
	slices.Sort(req.Storages)
	slices.Sort(req.Bridges)
	slices.Sort(req.Templates)
	tc, err := a.Store.GetTenantCluster(r.Context(), t.ID, c.ID)
	if errors.Is(err, store.ErrNotFound) {
		tc = &store.TenantCluster{TenantID: t.ID, ClusterID: c.ID}
	} else if err != nil {
		a.internal(w, err)
		return
	}
	tc.Pool, tc.Storages, tc.Bridges, tc.Templates = req.Pool, slices.Compact(req.Storages), slices.Compact(req.Bridges), slices.Compact(req.Templates)
	tc.PVEUser = pveTenantUser(t.Slug)

	steps, err := a.setupTenantCluster(r.Context(), cl, t, tc, req.Rotate)
	if err != nil {
		a.audit(r, "", "tenant.isolation", t.Name+" on "+c.Name, map[string]any{"pool": tc.Pool, "error": err.Error()}, false)
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "steps": steps})
		return
	}
	if err := a.Store.SaveTenantCluster(r.Context(), tc); err != nil {
		if errors.Is(err, store.ErrExists) {
			writeErr(w, http.StatusConflict, "another tenant already uses that pool on this cluster")
			return
		}
		a.storeErr(w, err, "tenant or cluster")
		return
	}
	a.audit(r, "", "tenant.isolation", t.Name+" on "+c.Name, map[string]any{
		"pool": tc.Pool, "storages": tc.Storages, "bridges": tc.Bridges, "templates": tc.Templates, "pve_user": tc.PVEUser, "rotated": req.Rotate,
	}, true)
	writeJSON(w, http.StatusOK, map[string]any{"cluster": tc, "steps": steps})
}

// pveACL is one Proxmox ACL entry for the tenant's user.
type pveACL struct{ Path, Role string }

// pveACLEntry is an entry of GET /access/acl.
type pveACLEntry struct {
	Path   string `json:"path"`
	RoleID string `json:"roleid"`
	Type   string `json:"type"`
	UGID   string `json:"ugid"`
}

type pvePool struct {
	PoolID string `json:"poolid"`
}

// tenantACLs is everything the tenant's Proxmox user is granted. Proxmox
// needs Pool.Allocate (PVEPoolAdmin) on the pool to create a VM into it;
// adding other guests to the pool would also need rights on those guests,
// which the user does not have.
func tenantACLs(tc *store.TenantCluster) []pveACL {
	want := []pveACL{{"/pool/" + tc.Pool, "PVEVMAdmin"}, {"/pool/" + tc.Pool, "PVEPoolAdmin"}}
	for _, s := range tc.Storages {
		want = append(want, pveACL{"/storage/" + s, "PVEDatastoreUser"})
	}
	for _, b := range tc.Bridges {
		want = append(want, pveACL{"/sdn/zones/localnetwork/" + b, "PVESDNUser"})
	}
	for _, v := range tc.Templates {
		want = append(want, pveACL{"/vms/" + strconv.Itoa(v), "PVETemplateUser"})
	}
	return want
}

// setupTenantCluster makes Proxmox match tc and fills in tc's token. It
// returns what it did, step by step, for the UI.
func (a *API) setupTenantCluster(ctx context.Context, cl *proxmox.Client, t *store.Tenant, tc *store.TenantCluster, rotate bool) ([]string, error) {
	steps := []string{}
	step := func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) }
	hint := func(err error) error {
		var ae *proxmox.APIError
		if errors.As(err, &ae) && ae.Status == http.StatusForbidden {
			return fmt.Errorf("%v: the cluster's API token needs Pool.Allocate, User.Modify, Permissions.Modify and Realm.AllocateUser (a root@pam token has them)", err)
		}
		return err
	}

	// 1. The pool.
	var pools []pvePool
	if err := cl.Do(ctx, http.MethodGet, "/pools", nil, &pools); err != nil {
		return steps, hint(err)
	}
	if slices.ContainsFunc(pools, func(p pvePool) bool { return p.PoolID == tc.Pool }) {
		step("pool %s exists", tc.Pool)
	} else {
		if err := cl.Do(ctx, http.MethodPost, "/pools", url.Values{"poolid": {tc.Pool}, "comment": {"servermgr tenant " + t.Name}}, nil); err != nil {
			return steps, hint(err)
		}
		step("created pool %s", tc.Pool)
	}

	// 2. The user: no password, so it cannot log in to the Proxmox UI.
	var users []struct {
		UserID string `json:"userid"`
	}
	if err := cl.Do(ctx, http.MethodGet, "/access/users", nil, &users); err != nil {
		return steps, hint(err)
	}
	if slices.IndexFunc(users, func(u struct {
		UserID string `json:"userid"`
	}) bool {
		return u.UserID == tc.PVEUser
	}) >= 0 {
		step("user %s exists", tc.PVEUser)
	} else {
		if err := cl.Do(ctx, http.MethodPost, "/access/users", url.Values{"userid": {tc.PVEUser}, "comment": {"servermgr tenant " + t.Name + " (no password; API token only)"}}, nil); err != nil {
			return steps, hint(err)
		}
		step("created user %s", tc.PVEUser)
	}

	// 3. ACLs: exactly the wanted ones for this user, nothing else.
	var acls []pveACLEntry
	if err := cl.Do(ctx, http.MethodGet, "/access/acl", nil, &acls); err != nil {
		return steps, hint(err)
	}
	want := tenantACLs(tc)
	for _, e := range acls {
		if e.Type != "user" || e.UGID != tc.PVEUser || slices.Contains(want, pveACL{e.Path, e.RoleID}) {
			continue
		}
		if err := cl.Do(ctx, http.MethodPut, "/access/acl", url.Values{"path": {e.Path}, "roles": {e.RoleID}, "users": {tc.PVEUser}, "delete": {"1"}}, nil); err != nil {
			return steps, hint(err)
		}
		step("removed %s on %s", e.RoleID, e.Path)
	}
	for _, p := range want {
		if slices.ContainsFunc(acls, func(e pveACLEntry) bool {
			return e.Type == "user" && e.UGID == tc.PVEUser && e.Path == p.Path && e.RoleID == p.Role
		}) {
			continue
		}
		if err := cl.Do(ctx, http.MethodPut, "/access/acl", url.Values{"path": {p.Path}, "roles": {p.Role}, "users": {tc.PVEUser}, "propagate": {"1"}}, nil); err != nil {
			return steps, hint(err)
		}
		step("granted %s on %s", p.Role, p.Path)
	}

	// 4. The token. privsep=0: it has exactly the user's ACLs above.
	if rotate || !tc.Isolated() {
		tokenPath := "/access/users/" + url.PathEscape(tc.PVEUser) + "/token/" + tenantTokenName
		_ = cl.Do(ctx, http.MethodDelete, tokenPath, nil, nil) // a stale one, if any
		var tok struct {
			FullTokenID string `json:"full-tokenid"`
			Value       string `json:"value"`
		}
		if err := cl.Do(ctx, http.MethodPost, tokenPath, url.Values{"privsep": {"0"}, "comment": {"servermgr tenant " + t.Name}}, &tok); err != nil {
			return steps, hint(err)
		}
		if tok.Value == "" {
			return steps, errors.New("proxmox did not return the new token's secret")
		}
		if tok.FullTokenID == "" {
			tok.FullTokenID = tc.PVEUser + "!" + tenantTokenName
		}
		sealed, err := a.sealSecret([]byte(tok.Value))
		if err != nil {
			return steps, err
		}
		tc.TokenID, tc.Sealed = tok.FullTokenID, sealed
		step("issued token %s", tc.TokenID)
	}

	// 5. Check what the token can see: nothing outside the pool except the
	// templates it may clone. Anything else means a broader ACL (for
	// example one on a group the user is in, or on /) leaks through.
	secret, err := a.openSecret(tc.Sealed)
	if err != nil {
		return steps, err
	}
	res, err := cl.WithToken(tc.TokenID, string(secret)).Resources(ctx)
	if err != nil {
		return steps, fmt.Errorf("the tenant's token does not work: %v", err)
	}
	for _, x := range res {
		if (x.Type == "qemu" || x.Type == "lxc") && x.Pool != tc.Pool && !(x.Template == 1 && slices.Contains(tc.Templates, x.VMID)) {
			return steps, fmt.Errorf("isolation check failed: the tenant's token can see %s %d (%s) outside pool %s; remove the broader permission on Proxmox and run this again", x.Type, x.VMID, x.Name, tc.Pool)
		}
	}
	step("checked: the token only sees pool %s and its templates", tc.Pool)
	now := time.Now()
	tc.SetUpAt = &now
	return steps, nil
}

// teardownTenantCluster deletes the tenant's Proxmox user on a cluster;
// Proxmox drops its token and ACLs with it.
func (a *API) teardownTenantCluster(ctx context.Context, t *store.Tenant, tc *store.TenantCluster) error {
	_, cl, err := a.pveClient(ctx, tc.ClusterID)
	if err != nil {
		return err
	}
	user := tc.PVEUser
	if user == "" {
		user = pveTenantUser(t.Slug)
	}
	if err := cl.Do(ctx, http.MethodDelete, "/access/users/"+url.PathEscape(user), nil, nil); err != nil {
		var ae *proxmox.APIError
		// Already gone is fine.
		if !errors.As(err, &ae) || !strings.Contains(strings.ToLower(ae.Msg), "does not exist") {
			return fmt.Errorf("removing %s: %v", user, err)
		}
	}
	return nil
}

func (a *API) handleRemoveTenantCluster(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	t, err := a.Store.GetTenant(r.Context(), id)
	if err != nil {
		a.storeErr(w, err, "tenant")
		return
	}
	tc, err := a.Store.GetTenantCluster(r.Context(), id, r.PathValue("cluster"))
	if err != nil {
		a.storeErr(w, err, "tenant cluster")
		return
	}
	if err := a.teardownTenantCluster(r.Context(), t, tc); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if err := a.Store.DeleteTenantCluster(r.Context(), id, tc.ClusterID); err != nil {
		a.storeErr(w, err, "tenant cluster")
		return
	}
	a.audit(r, "", "tenant.isolation_remove", t.Name, map[string]string{"cluster_id": tc.ClusterID, "pool": tc.Pool}, true)
	w.WriteHeader(http.StatusNoContent)
}
