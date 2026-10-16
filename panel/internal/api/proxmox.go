package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jacksonm36/servermgr/panel/internal/proxmox"
	"github.com/jacksonm36/servermgr/panel/internal/store"
)

// Proxmox VE clusters: inventory, power, snapshots and provisioning VMs from
// cloud-init templates that enroll themselves as servermgr servers.

func (a *API) pveClient(ctx context.Context, id string) (*store.ProxmoxCluster, *proxmox.Client, error) {
	c, err := a.Store.GetProxmoxCluster(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	secret, err := a.openSecret(c.SealedSecret)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot decrypt the API token: %v", err)
	}
	return c, proxmox.New(c.URL, c.TokenID, string(secret), c.Fingerprint), nil
}

// pveScope is what one request may reach on one cluster. Platform users
// use the cluster's token. A tenant's user uses the tenant's own token,
// which Proxmox ACLs limit to the tenant's pool, and every guest route
// checks the pool as well (pveGuest).
type pveScope struct {
	C  *store.ProxmoxCluster
	Cl *proxmox.Client
	TC *store.TenantCluster // nil for platform users
}

// errNoCluster is also what a tenant gets for a cluster it has no access to.
var errNoCluster = errors.New("cluster not found")

func (a *API) pveScope(r *http.Request) (*pveScope, error) {
	ac := accessFrom(r.Context())
	if ac.Tenant == nil {
		c, cl, err := a.pveClient(r.Context(), r.PathValue("id"))
		if errors.Is(err, store.ErrNotFound) {
			return nil, errNoCluster
		}
		return &pveScope{C: c, Cl: cl}, err
	}
	c, err := a.Store.GetProxmoxCluster(r.Context(), r.PathValue("id"))
	if err != nil {
		return nil, errNoCluster
	}
	tc, err := a.Store.GetTenantCluster(r.Context(), ac.Tenant.ID, c.ID)
	if err != nil || !tc.Isolated() {
		return nil, errNoCluster
	}
	secret, err := a.openSecret(tc.Sealed)
	if err != nil {
		return nil, fmt.Errorf("cannot decrypt the API token: %v", err)
	}
	return &pveScope{C: c, Cl: proxmox.New(c.URL, tc.TokenID, string(secret), c.Fingerprint), TC: tc}, nil
}

// scopeErr answers a pveScope error.
func scopeErr(w http.ResponseWriter, err error) {
	if errors.Is(err, errNoCluster) {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeErr(w, http.StatusServiceUnavailable, err.Error())
}

// errNoGuest is the answer for a guest outside the caller's tenant too, so
// a tenant cannot even learn which VMIDs exist elsewhere.
var errNoGuest = errors.New("guest not found")

// pveGuest resolves the {node}/{type}/{vmid} of a guest route. For a
// tenant's user the guest must be in the tenant's pool.
func (a *API) pveGuest(w http.ResponseWriter, r *http.Request) (s *pveScope, node, typ string, vmid int, ok bool) {
	node, typ, vmid, err := guestPath(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return nil, "", "", 0, false
	}
	if s, err = a.pveScope(r); err != nil {
		scopeErr(w, err)
		return nil, "", "", 0, false
	}
	if s.TC != nil {
		res, err := s.Cl.Resources(r.Context())
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return nil, "", "", 0, false
		}
		if !slices.ContainsFunc(res, func(x proxmox.Resource) bool {
			return x.Type == typ && x.VMID == vmid && x.Node == node && x.Pool == s.TC.Pool
		}) {
			writeErr(w, http.StatusNotFound, errNoGuest.Error())
			return nil, "", "", 0, false
		}
	}
	return s, node, typ, vmid, true
}

var (
	pveTokenID = regexp.MustCompile(`^[\w.-]+@[\w.-]+![\w.-]+$`)
	pveName    = regexp.MustCompile(`^[\w][\w .-]{0,62}$`)
	pveNode    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,62}$`)
	pveSnap    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{1,39}$`)
	pveFP      = regexp.MustCompile(`^([0-9A-F]{2}:){31}[0-9A-F]{2}$`)
	pveHost    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

func (a *API) handlePVEProbe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	fp, subject, err := proxmox.Probe(r.Context(), strings.TrimSpace(req.URL))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "cannot reach Proxmox: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"fingerprint": fp, "subject": subject})
}

// pveClusterView is a cluster as a tenant's user sees it: no address,
// token or fingerprint of the platform's connection.
type pveClusterView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Pool string `json:"pool"`
}

func (a *API) handleListPVE(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.ListProxmoxClusters(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	t := accessFrom(r.Context()).Tenant
	if t == nil {
		writeJSON(w, http.StatusOK, list)
		return
	}
	tcs, err := a.Store.ListTenantClusters(r.Context(), t.ID)
	if err != nil {
		a.internal(w, err)
		return
	}
	out := []pveClusterView{}
	for _, c := range list {
		for _, tc := range tcs {
			if tc.ClusterID == c.ID && tc.Isolated() {
				out = append(out, pveClusterView{ID: c.ID, Name: c.Name, Pool: tc.Pool})
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSavePVE adds or updates a cluster after checking that the token
// works over the pinned certificate. An empty secret keeps the stored one.
func (a *API) handleSavePVE(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		URL         string `json:"url"`
		TokenID     string `json:"token_id"`
		TokenSecret string `json:"token_secret"`
		Fingerprint string `json:"fingerprint"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	c := &store.ProxmoxCluster{ID: r.PathValue("id"), Name: strings.TrimSpace(req.Name), URL: strings.TrimRight(strings.TrimSpace(req.URL), "/"),
		TokenID: strings.TrimSpace(req.TokenID), Fingerprint: strings.ToUpper(strings.TrimSpace(req.Fingerprint))}
	if !pveName.MatchString(c.Name) {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if u, err := url.Parse(c.URL); err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" {
		writeErr(w, http.StatusBadRequest, "URL must look like https://pve.example:8006")
		return
	}
	if !pveTokenID.MatchString(c.TokenID) {
		writeErr(w, http.StatusBadRequest, "token ID must look like user@realm!tokenname")
		return
	}
	if !pveFP.MatchString(c.Fingerprint) {
		writeErr(w, http.StatusBadRequest, "fingerprint must be the SHA-256 fingerprint (AA:BB:…)")
		return
	}
	secret := strings.TrimSpace(req.TokenSecret)
	if secret == "" && c.ID != "" {
		old, err := a.Store.GetProxmoxCluster(r.Context(), c.ID)
		if err != nil {
			writeErr(w, http.StatusNotFound, "cluster not found")
			return
		}
		plain, err := a.openSecret(old.SealedSecret)
		if err != nil {
			a.secretErr(w, err)
			return
		}
		secret = string(plain)
	}
	if len(secret) < 8 || len(secret) > 200 {
		writeErr(w, http.StatusBadRequest, "token secret is required")
		return
	}
	ver, err := proxmox.New(c.URL, c.TokenID, secret, c.Fingerprint).Version(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, "connection test failed: "+err.Error())
		return
	}
	if c.SealedSecret, err = a.sealSecret([]byte(secret)); err != nil {
		a.secretErr(w, err)
		return
	}
	if err := a.Store.SaveProxmoxCluster(r.Context(), c); err != nil {
		if strings.Contains(err.Error(), "unique") {
			writeErr(w, http.StatusConflict, "a cluster with that name exists")
			return
		}
		a.internal(w, err)
		return
	}
	a.audit(r, "", "proxmox.cluster_save", c.Name, map[string]string{"url": c.URL, "token_id": c.TokenID, "fingerprint": c.Fingerprint}, true)
	writeJSON(w, http.StatusOK, map[string]any{"cluster": c, "version": ver})
}

func (a *API) handleDeletePVE(w http.ResponseWriter, r *http.Request) {
	c, err := a.Store.GetProxmoxCluster(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "cluster not found")
		return
	}
	if err := a.Store.DeleteProxmoxCluster(r.Context(), c.ID); err != nil {
		a.internal(w, err)
		return
	}
	a.audit(r, "", "proxmox.cluster_delete", c.Name, nil, true)
	w.WriteHeader(http.StatusNoContent)
}

type pveGuest struct {
	proxmox.Resource
	ServerID   string `json:"server_id,omitempty"`
	ServerName string `json:"server_name,omitempty"`
	// Tenant names the tenant whose pool holds the guest (platform view).
	Tenant string `json:"tenant,omitempty"`
}

// tenantUsage is what a tenant's guests take, counted against its quotas.
type tenantUsage struct {
	Guests   int   `json:"guests"`
	Cores    int   `json:"cores"`
	MemoryMB int64 `json:"memory_mb"`
	DiskGB   int64 `json:"disk_gb"`
}

func (u *tenantUsage) add(o tenantUsage) {
	u.Guests += o.Guests
	u.Cores += o.Cores
	u.MemoryMB += o.MemoryMB
	u.DiskGB += o.DiskGB
}

func guestUsage(x proxmox.Resource) tenantUsage {
	return tenantUsage{Guests: 1, Cores: int(x.MaxCPU), MemoryMB: x.MaxMem >> 20, DiskGB: x.MaxDisk >> 30}
}

func poolUsage(res []proxmox.Resource, pool string) tenantUsage {
	var u tenantUsage
	for _, x := range res {
		if (x.Type == "qemu" || x.Type == "lxc") && x.Template == 0 && x.Pool == pool {
			u.add(guestUsage(x))
		}
	}
	return u
}

// overQuota says which limit u would break, or "".
func overQuota(t *store.Tenant, u tenantUsage) string {
	switch {
	case t.MaxGuests > 0 && u.Guests > t.MaxGuests:
		return fmt.Sprintf("%d guests", t.MaxGuests)
	case t.MaxCores > 0 && u.Cores > t.MaxCores:
		return fmt.Sprintf("%d vCPUs", t.MaxCores)
	case t.MaxMemoryMB > 0 && u.MemoryMB > t.MaxMemoryMB:
		return fmt.Sprintf("%d MB of memory", t.MaxMemoryMB)
	case t.MaxDiskGB > 0 && u.DiskGB > t.MaxDiskGB:
		return fmt.Sprintf("%d GB of disk", t.MaxDiskGB)
	}
	return ""
}

func (a *API) handlePVEResources(w http.ResponseWriter, r *http.Request) {
	s, err := a.pveScope(r)
	if err != nil {
		scopeErr(w, err)
		return
	}
	c := s.C
	var view any = c
	if s.TC != nil {
		view = pveClusterView{ID: c.ID, Name: c.Name, Pool: s.TC.Pool}
	}
	ver, err := s.Cl.Version(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"cluster": view, "error": err.Error()})
		return
	}
	res, err := s.Cl.Resources(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"cluster": view, "error": err.Error()})
		return
	}
	ac := accessFrom(r.Context())
	links, _ := a.Store.ProxmoxLinks(r.Context(), c.ID)
	servers, _ := a.Store.ListServers(r.Context())
	names := map[string]string{}
	for _, sv := range servers {
		if ac.CanOn("server.view", sv.ID) {
			names[sv.ID] = sv.Name
		}
	}
	pools := map[string]string{} // pool -> tenant name (platform view)
	if s.TC == nil {
		tenants, _ := a.Store.ListTenants(r.Context())
		tcs, _ := a.Store.ListTenantClusters(r.Context(), 0)
		for _, tc := range tcs {
			for _, t := range tenants {
				if tc.ClusterID == c.ID && tc.TenantID == t.ID {
					pools[tc.Pool] = t.Name
				}
			}
		}
	}
	nodes, guests, storage := []proxmox.Resource{}, []pveGuest{}, []proxmox.Resource{}
	for _, x := range res {
		switch x.Type {
		case "node":
			if s.TC == nil {
				nodes = append(nodes, x)
			}
		case "qemu", "lxc":
			// A tenant sees its pool, and the templates it may clone.
			if s.TC != nil && x.Pool != s.TC.Pool && (x.Template == 0 || !slices.Contains(s.TC.Templates, x.VMID)) {
				continue
			}
			g := pveGuest{Resource: x, Tenant: pools[x.Pool]}
			if sid := links[x.VMID]; sid != "" && names[sid] != "" {
				g.ServerID, g.ServerName = sid, names[sid]
			}
			guests = append(guests, g)
		case "storage":
			if s.TC == nil || slices.Contains(s.TC.Storages, x.Storage) {
				storage = append(storage, x)
			}
		}
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Node < nodes[j].Node })
	sort.Slice(guests, func(i, j int) bool { return guests[i].VMID < guests[j].VMID })
	out := map[string]any{"cluster": view, "version": ver, "nodes": nodes, "guests": guests, "storage": storage}
	if s.TC != nil {
		out["tenant"] = map[string]any{
			"name": ac.Tenant.Name, "pool": s.TC.Pool, "storages": s.TC.Storages, "bridges": s.TC.Bridges,
			"usage": poolUsage(res, s.TC.Pool),
			"quota": tenantUsage{Guests: ac.Tenant.MaxGuests, Cores: ac.Tenant.MaxCores, MemoryMB: ac.Tenant.MaxMemoryMB, DiskGB: ac.Tenant.MaxDiskGB},
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// guestPath validates the {node}/{type}/{vmid} path values.
func guestPath(r *http.Request) (node, typ string, vmid int, err error) {
	node, typ = r.PathValue("node"), r.PathValue("type")
	vmid, err = strconv.Atoi(r.PathValue("vmid"))
	if err != nil || vmid < 100 || vmid > 999999999 || !pveNode.MatchString(node) || (typ != "qemu" && typ != "lxc") {
		return "", "", 0, errors.New("bad guest path")
	}
	return node, typ, vmid, nil
}

func (a *API) pveGuestCall(w http.ResponseWriter, r *http.Request, method, sub string, form url.Values, audit string) bool {
	s, node, typ, vmid, ok := a.pveGuest(w, r)
	if !ok {
		return false
	}
	c, cl := s.C, s.Cl
	var upid string
	if err := cl.Do(r.Context(), method, proxmox.GuestPath(node, typ, vmid)+sub, form, &upid); err != nil {
		a.audit(r, "", audit, fmt.Sprintf("%s/%d", c.Name, vmid), map[string]string{"error": err.Error()}, false)
		writeErr(w, http.StatusBadGateway, err.Error())
		return false
	}
	if upid != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
		defer cancel()
		if err := cl.WaitTask(ctx, node, upid); err != nil {
			a.audit(r, "", audit, fmt.Sprintf("%s/%d", c.Name, vmid), map[string]string{"error": err.Error()}, false)
			writeErr(w, http.StatusBadGateway, err.Error())
			return false
		}
	}
	a.audit(r, "", audit, fmt.Sprintf("%s/%d", c.Name, vmid), form, true)
	w.WriteHeader(http.StatusNoContent)
	return true
}

func (a *API) handlePVEPower(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string `json:"action"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	switch req.Action {
	case "start", "shutdown", "stop", "reboot", "resume", "suspend":
	default:
		writeErr(w, http.StatusBadRequest, "unknown action")
		return
	}
	a.pveGuestCall(w, r, http.MethodPost, "/status/"+req.Action, nil, "proxmox.power_"+req.Action)
}

func (a *API) handlePVESnapshots(w http.ResponseWriter, r *http.Request) {
	s, node, typ, vmid, ok := a.pveGuest(w, r)
	if !ok {
		return
	}
	cl := s.Cl
	var snaps []map[string]any
	if err := cl.Do(r.Context(), http.MethodGet, proxmox.GuestPath(node, typ, vmid)+"/snapshot", nil, &snaps); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	out := []map[string]any{}
	for _, s := range snaps {
		if s["name"] != "current" {
			out = append(out, s)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handlePVESnapshotCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		VMState     bool   `json:"vmstate"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if !pveSnap.MatchString(req.Name) {
		writeErr(w, http.StatusBadRequest, "snapshot name: letter first, then letters, digits, - or _ (2-40)")
		return
	}
	form := url.Values{"snapname": {req.Name}}
	if d := strings.TrimSpace(req.Description); d != "" {
		form.Set("description", d[:min(len(d), 200)])
	}
	if req.VMState && r.PathValue("type") == "qemu" {
		form.Set("vmstate", "1")
	}
	a.pveGuestCall(w, r, http.MethodPost, "/snapshot", form, "proxmox.snapshot_create")
}

func (a *API) handlePVESnapshotDelete(w http.ResponseWriter, r *http.Request) {
	if !pveSnap.MatchString(r.PathValue("snap")) {
		writeErr(w, http.StatusBadRequest, "bad snapshot name")
		return
	}
	a.pveGuestCall(w, r, http.MethodDelete, "/snapshot/"+r.PathValue("snap"), nil, "proxmox.snapshot_delete")
}

func (a *API) handlePVESnapshotRollback(w http.ResponseWriter, r *http.Request) {
	if !pveSnap.MatchString(r.PathValue("snap")) {
		writeErr(w, http.StatusBadRequest, "bad snapshot name")
		return
	}
	a.pveGuestCall(w, r, http.MethodPost, "/snapshot/"+r.PathValue("snap")+"/rollback", nil, "proxmox.snapshot_rollback")
}

// handlePVEDeleteGuest destroys a stopped guest; the name must be typed.
func (a *API) handlePVEDeleteGuest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Confirm string `json:"confirm"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s, node, typ, vmid, ok := a.pveGuest(w, r)
	if !ok {
		return
	}
	cl := s.Cl
	var cfg struct {
		Name     string `json:"name"`
		Hostname string `json:"hostname"`
	}
	if err := cl.Do(r.Context(), http.MethodGet, proxmox.GuestPath(node, typ, vmid)+"/config", nil, &cfg); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	name := cfg.Name
	if typ == "lxc" {
		name = cfg.Hostname
	}
	if req.Confirm == "" || req.Confirm != name {
		writeErr(w, http.StatusBadRequest, "type the guest's name to confirm")
		return
	}
	if a.pveGuestCall(w, r, http.MethodDelete, "", url.Values{"purge": {"1"}}, "proxmox.guest_delete") {
		_ = a.Store.UnlinkProxmoxGuest(r.Context(), r.PathValue("id"), vmid)
	}
}

// ---- provisioning ----

type pveProvisionReq struct {
	TemplateVMID int    `json:"template_vmid"`
	Node         string `json:"node"`   // node holding the template
	Target       string `json:"target"` // node for the new VM ("" = same)
	Name         string `json:"name"`
	Cores        int    `json:"cores"`
	MemoryMB     int    `json:"memory_mb"`
	DiskGrowGB   int    `json:"disk_grow_gb"`
	DiskGB       int    `json:"disk_gb"` // disk size to set (0 = the template's); never shrinks
	Disk         string `json:"disk"`
	FullClone    bool   `json:"full_clone"`
	IP           string `json:"ip"` // "dhcp" or CIDR
	Gateway      string `json:"gateway"`
	Enroll       bool   `json:"enroll"`
	Storage      string `json:"storage"` // full clone target storage ("" = template's)
	Bridge       string `json:"bridge"`  // network bridge for net0 ("" = template's)
	pveCIOpts
}

// pveCIOpts are the cloud-init and guest options a new VM can get on top
// of the panel's own SSH key.
type pveCIOpts struct {
	CIUser       string   `json:"ciuser"` // "" = root
	CIPassword   string   `json:"cipassword"`
	SSHKeys      string   `json:"sshkeys"`     // extra keys, one per line
	SSHKeyIDs    []int64  `json:"ssh_key_ids"` // keys from the key library
	Nameserver   string   `json:"nameserver"`
	Searchdomain string   `json:"searchdomain"`
	Tags         []string `json:"tags"`
	OnBoot       bool     `json:"onboot"`
	Description  string   `json:"description"`
	keyLines     []string // resolved by check
}

// checkCIOpts validates the options and resolves library keys. ac decides
// whether library keys may be used.
func (a *API) checkCIOpts(ctx context.Context, ac *Access, o *pveCIOpts) string {
	o.CIUser = strings.TrimSpace(o.CIUser)
	if o.CIUser != "" && !pveCIUser.MatchString(o.CIUser) {
		return "user: lower-case letters, digits, - and _"
	}
	if o.CIPassword != "" && (len(o.CIPassword) < 8 || len(o.CIPassword) > 128) {
		return "password: 8 to 128 characters"
	}
	o.keyLines = nil
	for _, k := range strings.Split(strings.ReplaceAll(o.SSHKeys, "\r", ""), "\n") {
		if k = strings.TrimSpace(k); k == "" {
			continue
		}
		if !pveSSHKey.MatchString(k) {
			return "SSH keys: one OpenSSH public key per line"
		}
		o.keyLines = append(o.keyLines, k)
	}
	if len(o.SSHKeyIDs) > 0 && !ac.Can("keys.manage") {
		return "using keys from the key library needs the keys.manage permission"
	}
	for _, id := range o.SSHKeyIDs {
		k, err := a.Store.GetSSHKey(ctx, id)
		if err != nil {
			return fmt.Sprintf("key %d is not in the key library", id)
		}
		line, err := a.openSecret(k.Sealed)
		if err != nil {
			return "unlock the vault to use keys from the key library"
		}
		o.keyLines = append(o.keyLines, strings.TrimSpace(string(line)))
	}
	if len(o.keyLines) > 20 {
		return "SSH keys: at most 20"
	}
	var ns []string
	for _, n := range strings.Fields(o.Nameserver) {
		if net.ParseIP(n) == nil || len(ns) == 3 {
			return "DNS servers: up to 3 IP addresses"
		}
		ns = append(ns, n)
	}
	o.Nameserver = strings.Join(ns, " ")
	o.Searchdomain = strings.TrimSpace(o.Searchdomain)
	if o.Searchdomain != "" && (len(o.Searchdomain) > 512 || !pveSearch.MatchString(o.Searchdomain)) {
		return "search domain: domain names separated by spaces"
	}
	if len(o.Tags) > 10 {
		return "at most 10 tags"
	}
	for _, t := range o.Tags {
		if !pveTag.MatchString(t) {
			return "tags: lower-case letters, digits, - _ . +"
		}
	}
	if len(o.Description) > 2000 {
		return "notes: at most 2000 characters"
	}
	return ""
}

// redacted is the request as the audit log keeps it.
func (r pveProvisionReq) redacted() pveProvisionReq {
	if r.CIPassword != "" {
		r.CIPassword = "(set)"
	}
	return r
}

// user is the cloud-init user the VM gets.
func (o *pveCIOpts) user() string {
	if o.CIUser == "" {
		return "root"
	}
	return o.CIUser
}

var (
	pveStorage = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,62}$`)
	pveBridge  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,14}$`)
)

var pveDisk = regexp.MustCompile(`^(scsi|virtio|sata|ide)\d{1,2}$`)

func (a *API) handlePVEProvision(w http.ResponseWriter, r *http.Request) {
	var req pveProvisionReq
	if !readJSON(w, r, &req) {
		return
	}
	bad := func(m string) { writeErr(w, http.StatusBadRequest, m) }
	if !pveHost.MatchString(req.Name) {
		bad("name must be a valid hostname (lowercase letters, digits, dashes)")
		return
	}
	if !pveNode.MatchString(req.Node) || (req.Target != "" && !pveNode.MatchString(req.Target)) || req.TemplateVMID < 100 {
		bad("pick a template")
		return
	}
	if req.Cores < 0 || req.Cores > 128 || req.MemoryMB < 0 || req.MemoryMB > 1<<20 || req.DiskGrowGB < 0 || req.DiskGrowGB > 10000 || req.DiskGB < 0 || req.DiskGB > 10000 {
		bad("resources out of range")
		return
	}
	if req.Disk == "" {
		req.Disk = "scsi0"
	}
	if !pveDisk.MatchString(req.Disk) {
		bad("disk must look like scsi0")
		return
	}
	if req.IP != "dhcp" {
		if _, _, err := net.ParseCIDR(req.IP); err != nil {
			bad("IP must be dhcp or a CIDR like 10.0.0.50/24")
			return
		}
		if req.Gateway != "" && net.ParseIP(req.Gateway) == nil {
			bad("invalid gateway")
			return
		}
	}
	if (req.Storage != "" && !pveStorage.MatchString(req.Storage)) || (req.Bridge != "" && !pveBridge.MatchString(req.Bridge)) {
		bad("invalid storage or bridge")
		return
	}
	if msg := a.checkCIOpts(r.Context(), accessFrom(r.Context()), &req.pveCIOpts); msg != "" {
		bad(msg)
		return
	}
	s, err := a.pveScope(r)
	if err != nil {
		scopeErr(w, err)
		return
	}
	var reserved *tenantUsage
	if s.TC != nil {
		u, msg := a.tenantProvisionCheck(r, s, &req)
		if msg != "" {
			bad(msg)
			return
		}
		reserved = &u
		defer func() {
			// Released by runProvision once it is running.
			if reserved != nil {
				a.releaseQuota(s.TC.TenantID, *reserved)
			}
		}()
	}
	who := whoFrom(r)
	p := &store.ProxmoxProvision{ID: store.NewUUID(), ClusterID: s.C.ID, Name: req.Name, Status: "running", CreatedBy: who.Actor, TenantID: who.Tenant}
	if err := a.Store.CreateProvision(r.Context(), p); err != nil {
		a.internal(w, err)
		return
	}
	a.audit(r, "", "proxmox.provision", s.C.Name+"/"+req.Name, req.redacted(), true)
	// The reservation ends once the clone is in the pool (or it failed).
	release := func() {}
	if reserved != nil {
		res := *reserved
		reserved = nil
		release = sync.OnceFunc(func() { a.releaseQuota(s.TC.TenantID, res) })
	}
	go func() {
		defer release()
		a.runProvision(who, s, p, req, release)
	}()
	writeJSON(w, http.StatusAccepted, p)
}

// tenantProvisionCheck applies a tenant's rules to a new VM: only its
// templates, storages and bridges, always into its pool, within its
// quotas. It reserves the VM's share of the quota (released by the
// caller) and returns it, or a message saying what is not allowed.
func (a *API) tenantProvisionCheck(r *http.Request, s *pveScope, req *pveProvisionReq) (tenantUsage, string) {
	tc, t := s.TC, accessFrom(r.Context()).Tenant
	if !slices.Contains(tc.Templates, req.TemplateVMID) {
		return tenantUsage{}, "that template is not available to you"
	}
	if req.Target != "" && req.Target != req.Node {
		return tenantUsage{}, "new VMs are created on the template's node"
	}
	req.Target = ""
	if len(tc.Storages) == 0 || len(tc.Bridges) == 0 {
		return tenantUsage{}, "no storage or network has been given to you yet; ask an administrator"
	}
	if req.Storage == "" && len(tc.Storages) == 1 {
		req.Storage = tc.Storages[0]
	}
	if !slices.Contains(tc.Storages, req.Storage) {
		return tenantUsage{}, "pick one of your storages"
	}
	if req.Bridge == "" && len(tc.Bridges) == 1 {
		req.Bridge = tc.Bridges[0]
	}
	if !slices.Contains(tc.Bridges, req.Bridge) {
		return tenantUsage{}, "pick one of your networks"
	}
	// A linked clone would live on the template's storage.
	req.FullClone = true

	res, err := s.Cl.Resources(r.Context())
	if err != nil {
		return tenantUsage{}, "cannot read your guests: " + err.Error()
	}
	i := slices.IndexFunc(res, func(x proxmox.Resource) bool {
		return x.Type == "qemu" && x.VMID == req.TemplateVMID && x.Template == 1
	})
	if i < 0 || res[i].Node != req.Node {
		return tenantUsage{}, "that template is not available to you"
	}
	tpl := res[i]
	add := tenantUsage{Guests: 1, Cores: req.Cores, MemoryMB: int64(req.MemoryMB), DiskGB: max(tpl.MaxDisk>>30, int64(req.DiskGB)) + int64(req.DiskGrowGB)}
	if add.Cores == 0 {
		add.Cores = int(tpl.MaxCPU)
	}
	if add.MemoryMB == 0 {
		add.MemoryMB = tpl.MaxMem >> 20
	}
	a.pveMu.Lock()
	defer a.pveMu.Unlock()
	if a.pveReserved == nil {
		a.pveReserved = map[int64]tenantUsage{}
	}
	total := poolUsage(res, tc.Pool)
	total.add(a.pveReserved[tc.TenantID])
	total.add(add)
	if limit := overQuota(t, total); limit != "" {
		return tenantUsage{}, "this VM would go over your limit of " + limit
	}
	u := a.pveReserved[tc.TenantID]
	u.add(add)
	a.pveReserved[tc.TenantID] = u
	return add, ""
}

func (a *API) releaseQuota(tenant int64, u tenantUsage) {
	a.pveMu.Lock()
	defer a.pveMu.Unlock()
	cur := a.pveReserved[tenant]
	cur.add(tenantUsage{Guests: -u.Guests, Cores: -u.Cores, MemoryMB: -u.MemoryMB, DiskGB: -u.DiskGB})
	a.pveReserved[tenant] = cur
}

func (a *API) handleListProvisions(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.ListProvisions(r.Context(), 50, accessFrom(r.Context()).TenantID())
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// encodeURIComponent is what Proxmox expects for the sshkeys option.
func encodeURIComponent(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// provisionLog appends timestamped lines to a provisioning run's log.
func (a *API) provisionLog(p *store.ProxmoxProvision) func(string, ...any) {
	var mu sync.Mutex
	return func(format string, args ...any) {
		mu.Lock()
		p.Log += time.Now().UTC().Format("15:04:05 ") + fmt.Sprintf(format, args...) + "\n"
		mu.Unlock()
		_ = a.Store.UpdateProvision(a.baseCtx(), p, false)
	}
}

// finishProvision records how a provisioning run ended.
func (a *API) finishProvision(p *store.ProxmoxProvision, err error, logf func(string, ...any)) {
	p.Status = "succeeded"
	if err != nil {
		p.Status = "failed"
		logf("error: %v", err)
	}
	_ = a.Store.UpdateProvision(a.baseCtx(), p, true)
	a.Log.Info("proxmox provisioning finished", "name", p.Name, "status", p.Status)
}

func (a *API) runProvision(who Who, s *pveScope, p *store.ProxmoxProvision, req pveProvisionReq, cloned func()) {
	ctx, cancel := context.WithTimeout(a.baseCtx(), 30*time.Minute)
	defer cancel()
	logf := a.provisionLog(p)
	a.finishProvision(p, a.provisionVM(ctx, who, s, p, req, cloned, logf), logf)
}

// provisionVM clones a template into a new VM, sets cloud-init, starts it
// and, if asked, enrolls it.
func (a *API) provisionVM(ctx context.Context, who Who, s *pveScope, p *store.ProxmoxProvision, req pveProvisionReq, cloned func(), logf func(string, ...any)) error {
	c, cl := s.C, s.Cl
	var next flexString
	if err := cl.Do(ctx, http.MethodGet, "/cluster/nextid", nil, &next); err != nil {
		return err
	}
	vmid, err := strconv.Atoi(string(next))
	if err != nil {
		return fmt.Errorf("unexpected nextid %q", next)
	}
	p.VMID = &vmid
	node := req.Node
	if req.Target != "" {
		node = req.Target
	}
	logf("cloning template %d on %s to VM %d (%s)", req.TemplateVMID, req.Node, vmid, req.Name)
	form := url.Values{"newid": {strconv.Itoa(vmid)}, "name": {req.Name}, "full": {map[bool]string{true: "1", false: "0"}[req.FullClone]}}
	if req.Target != "" {
		form.Set("target", req.Target)
	}
	if req.Storage != "" && req.FullClone {
		form.Set("storage", req.Storage)
	}
	if s.TC != nil {
		form.Set("pool", s.TC.Pool)
		logf("the VM goes into pool %s", s.TC.Pool)
	}
	var upid string
	if err := cl.Do(ctx, http.MethodPost, proxmox.GuestPath(req.Node, "qemu", req.TemplateVMID)+"/clone", form, &upid); err != nil {
		return err
	}
	if err := cl.WaitTask(ctx, req.Node, upid); err != nil {
		return err
	}
	cloned()

	_, pub, err := a.ansibleKey()
	if err != nil {
		return err
	}
	ipcfg := "ip=dhcp"
	if req.IP != "dhcp" {
		ipcfg = "ip=" + req.IP
		if req.Gateway != "" {
			ipcfg += ",gw=" + req.Gateway
		}
	}
	keys := append([]string{pub + " servermgr-ansible"}, req.keyLines...)
	cfg := url.Values{"ciuser": {req.user()}, "sshkeys": {encodeURIComponent(strings.Join(keys, "\n"))}, "ipconfig0": {ipcfg}, "agent": {"1"}}
	if req.CIPassword != "" {
		cfg.Set("cipassword", req.CIPassword)
	}
	if req.Nameserver != "" {
		cfg.Set("nameserver", req.Nameserver)
	}
	if req.Searchdomain != "" {
		cfg.Set("searchdomain", req.Searchdomain)
	}
	if len(req.Tags) > 0 {
		cfg.Set("tags", strings.Join(req.Tags, ";"))
	}
	if req.OnBoot {
		cfg.Set("onboot", "1")
	}
	if req.Description != "" {
		cfg.Set("description", req.Description)
	}
	if req.Cores > 0 {
		cfg.Set("cores", strconv.Itoa(req.Cores))
	}
	if req.MemoryMB > 0 {
		cfg.Set("memory", strconv.Itoa(req.MemoryMB))
	}
	if req.Bridge != "" {
		cfg.Set("net0", "virtio,bridge="+req.Bridge)
	}
	extra := ""
	if n := len(req.keyLines); n > 0 {
		extra = fmt.Sprintf(" and %d more key(s)", n)
	}
	if req.CIPassword != "" {
		extra += ", a password"
	}
	logf("cloud-init: user %s with the panel SSH key%s, %s; agent enabled", req.user(), extra, ipcfg)
	if err := cl.Do(ctx, http.MethodPut, proxmox.GuestPath(node, "qemu", vmid)+"/config", cfg, nil); err != nil {
		return err
	}
	if req.DiskGB > 0 {
		logf("setting %s to %d GB", req.Disk, req.DiskGB)
		err := cl.Do(ctx, http.MethodPut, proxmox.GuestPath(node, "qemu", vmid)+"/resize", url.Values{"disk": {req.Disk}, "size": {fmt.Sprintf("%dG", req.DiskGB)}}, nil)
		var ae *proxmox.APIError
		if errors.As(err, &ae) && strings.Contains(ae.Msg, "shrink") {
			logf("the disk is already bigger than %d GB; keeping its size", req.DiskGB)
		} else if err != nil {
			return err
		}
	}
	if req.DiskGrowGB > 0 {
		logf("growing %s by %d GB", req.Disk, req.DiskGrowGB)
		if err := cl.Do(ctx, http.MethodPut, proxmox.GuestPath(node, "qemu", vmid)+"/resize", url.Values{"disk": {req.Disk}, "size": {fmt.Sprintf("+%dG", req.DiskGrowGB)}}, nil); err != nil {
			return err
		}
	}
	logf("starting VM %d", vmid)
	if err := cl.Do(ctx, http.MethodPost, proxmox.GuestPath(node, "qemu", vmid)+"/status/start", nil, &upid); err != nil {
		return err
	}
	if err := cl.WaitTask(ctx, node, upid); err != nil {
		return err
	}
	if !req.Enroll {
		logf("done (not enrolling)")
		return nil
	}

	// Address: the static IP, or what the guest agent reports.
	addr := ""
	if req.IP != "dhcp" {
		ip, _, _ := net.ParseCIDR(req.IP)
		addr = ip.String()
	} else {
		logf("waiting for the guest agent to report an IP")
		for i := 0; i < 60 && addr == ""; i++ {
			if ip, err := cl.GuestIPv4(ctx, node, vmid); err == nil {
				addr = ip
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
			}
		}
		if addr == "" {
			return errors.New("no IP from the guest agent after 5 minutes (is qemu-guest-agent in the template?)")
		}
	}
	logf("VM address %s; enrolling the agent over SSH", addr)
	serverID, err := a.enrollOverSSH(ctx, addr, req.user(), req.Name, logf)
	if err != nil {
		return err
	}
	p.ServerID = &serverID
	_ = a.Store.LinkProxmoxGuest(ctx, c.ID, vmid, serverID)
	if t := who.Access.tenantGroup(); t != 0 {
		if err := a.Store.AddServerToGroup(ctx, t, serverID); err != nil {
			logf("warning: adding the server to your server group: %v", err)
		}
	}
	logf("enrolled as server %s; restricting the panel key and pinning host keys (Prepare for Ansible)", serverID)
	if _, err := a.Dispatch(ctx, Who{Actor: who.Actor, IP: "panel"}, serverID, "ansible.prepare", map[string]string{"address": addr}); err != nil {
		logf("warning: ansible.prepare: %v", err)
	}
	return nil
}

// flexString decodes a JSON string or number (Proxmox returns nextid as a
// string, some versions as a number).
type flexString string

func (s *flexString) UnmarshalJSON(b []byte) error {
	*s = flexString(strings.Trim(string(b), `"`))
	return nil
}

// enrollOverSSH logs in with the panel key (installed by cloud-init), runs
// the agent installer with a fresh join token and waits for the agent.
func (a *API) enrollOverSSH(ctx context.Context, addr, user, name string, logf func(string, ...any)) (string, error) {
	keyPath, _, err := a.ansibleKey()
	if err != nil {
		return "", err
	}
	pem, err := os.ReadFile(keyPath)
	if err != nil {
		return "", err
	}
	signer, err := ssh.ParsePrivateKey(pem)
	if err != nil {
		return "", err
	}
	// A brand-new VM: its host key cannot be known yet. Record it (it is
	// pinned properly by ansible.prepare over the agent afterwards).
	var seen string
	cfg := &ssh.ClientConfig{
		User: user, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, Timeout: 15 * time.Second,
		HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error { seen = ssh.FingerprintSHA256(k); return nil },
	}
	var conn *ssh.Client
	for i := 0; i < 40; i++ {
		if conn, err = ssh.Dial("tcp", net.JoinHostPort(addr, "22"), cfg); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	if err != nil {
		return "", fmt.Errorf("SSH to %s failed: %v", addr, err)
	}
	defer conn.Close()
	logf("SSH connected (host key %s)", seen)

	raw, hash := randomToken()
	if err := a.Store.CreateJoinToken(ctx, hash, name, time.Now().Add(joinTokenTTL), nil); err != nil {
		return "", err
	}
	// Cloud images give their default user passwordless sudo.
	sh := "sh"
	if user != "root" {
		sh = "sudo -n sh"
	}
	cmd := fmt.Sprintf("curl -fsSL %s/install.sh | %s -s -- --token %s --ca-sha256 %s", a.Cfg.PublicURL, sh, raw, a.CA.Fingerprint())
	sess, err := conn.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	out, err := sess.CombinedOutput(cmd)
	tail := strings.TrimSpace(string(out))
	if len(tail) > 300 {
		tail = "…" + tail[len(tail)-300:]
	}
	logf("installer: %s", strings.ReplaceAll(tail, raw, "••••"))
	if err != nil {
		return "", fmt.Errorf("agent installer failed: %v", err)
	}
	for i := 0; i < 60; i++ {
		if sid, err := a.Store.JoinTokenServer(ctx, hash); err == nil && sid != "" {
			for j := 0; j < 30 && !a.Hub.Online(sid); j++ {
				time.Sleep(time.Second)
			}
			return sid, nil
		}
		time.Sleep(2 * time.Second)
	}
	return "", errors.New("the agent did not enroll within 2 minutes")
}
