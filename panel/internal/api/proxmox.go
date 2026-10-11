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

func (a *API) handleListPVE(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.ListProxmoxClusters(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
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
}

func (a *API) handlePVEResources(w http.ResponseWriter, r *http.Request) {
	c, cl, err := a.pveClient(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	ver, err := cl.Version(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"cluster": c, "error": err.Error()})
		return
	}
	res, err := cl.Resources(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"cluster": c, "error": err.Error()})
		return
	}
	links, _ := a.Store.ProxmoxLinks(r.Context(), c.ID)
	servers, _ := a.Store.ListServers(r.Context())
	names := map[string]string{}
	for _, s := range servers {
		names[s.ID] = s.Name
	}
	nodes, guests, storage := []proxmox.Resource{}, []pveGuest{}, []proxmox.Resource{}
	for _, x := range res {
		switch x.Type {
		case "node":
			nodes = append(nodes, x)
		case "qemu", "lxc":
			g := pveGuest{Resource: x}
			if sid := links[x.VMID]; sid != "" && names[sid] != "" {
				g.ServerID, g.ServerName = sid, names[sid]
			}
			guests = append(guests, g)
		case "storage":
			storage = append(storage, x)
		}
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Node < nodes[j].Node })
	sort.Slice(guests, func(i, j int) bool { return guests[i].VMID < guests[j].VMID })
	writeJSON(w, http.StatusOK, map[string]any{"cluster": c, "version": ver, "nodes": nodes, "guests": guests, "storage": storage})
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
	c, cl, err := a.pveClient(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return false
	}
	node, typ, vmid, err := guestPath(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return false
	}
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
	_, cl, err := a.pveClient(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	node, typ, vmid, err := guestPath(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
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
	_, cl, err := a.pveClient(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	node, typ, vmid, err := guestPath(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
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
	Disk         string `json:"disk"`
	FullClone    bool   `json:"full_clone"`
	IP           string `json:"ip"` // "dhcp" or CIDR
	Gateway      string `json:"gateway"`
	Enroll       bool   `json:"enroll"`
}

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
	if req.Cores < 0 || req.Cores > 128 || req.MemoryMB < 0 || req.MemoryMB > 1<<20 || req.DiskGrowGB < 0 || req.DiskGrowGB > 10000 {
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
	c, cl, err := a.pveClient(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	p := &store.ProxmoxProvision{ID: store.NewUUID(), ClusterID: c.ID, Name: req.Name, Status: "running", CreatedBy: whoFrom(r).Actor}
	if err := a.Store.CreateProvision(r.Context(), p); err != nil {
		a.internal(w, err)
		return
	}
	a.audit(r, "", "proxmox.provision", c.Name+"/"+req.Name, req, true)
	go a.runProvision(whoFrom(r), c, cl, p, req)
	writeJSON(w, http.StatusAccepted, p)
}

func (a *API) handleListProvisions(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.ListProvisions(r.Context(), 50)
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

func (a *API) runProvision(who Who, c *store.ProxmoxCluster, cl *proxmox.Client, p *store.ProxmoxProvision, req pveProvisionReq) {
	ctx, cancel := context.WithTimeout(a.baseCtx(), 30*time.Minute)
	defer cancel()
	var mu sync.Mutex
	logf := func(format string, args ...any) {
		mu.Lock()
		p.Log += time.Now().UTC().Format("15:04:05 ") + fmt.Sprintf(format, args...) + "\n"
		mu.Unlock()
		_ = a.Store.UpdateProvision(a.baseCtx(), p, false)
	}
	err := func() error {
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
		var upid string
		if err := cl.Do(ctx, http.MethodPost, proxmox.GuestPath(req.Node, "qemu", req.TemplateVMID)+"/clone", form, &upid); err != nil {
			return err
		}
		if err := cl.WaitTask(ctx, req.Node, upid); err != nil {
			return err
		}

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
		cfg := url.Values{"ciuser": {"root"}, "sshkeys": {encodeURIComponent(pub + " servermgr-ansible")}, "ipconfig0": {ipcfg}, "agent": {"1"}}
		if req.Cores > 0 {
			cfg.Set("cores", strconv.Itoa(req.Cores))
		}
		if req.MemoryMB > 0 {
			cfg.Set("memory", strconv.Itoa(req.MemoryMB))
		}
		logf("cloud-init: root with the panel SSH key, %s; agent enabled", ipcfg)
		if err := cl.Do(ctx, http.MethodPut, proxmox.GuestPath(node, "qemu", vmid)+"/config", cfg, nil); err != nil {
			return err
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
		serverID, err := a.enrollOverSSH(ctx, addr, req.Name, logf)
		if err != nil {
			return err
		}
		p.ServerID = &serverID
		_ = a.Store.LinkProxmoxGuest(ctx, c.ID, vmid, serverID)
		logf("enrolled as server %s; restricting the panel key and pinning host keys (Prepare for Ansible)", serverID)
		if _, err := a.Dispatch(ctx, Who{Actor: who.Actor, IP: "panel"}, serverID, "ansible.prepare", map[string]string{"address": addr}); err != nil {
			logf("warning: ansible.prepare: %v", err)
		}
		return nil
	}()
	p.Status = "succeeded"
	if err != nil {
		p.Status = "failed"
		logf("error: %v", err)
	}
	_ = a.Store.UpdateProvision(a.baseCtx(), p, true)
	a.Log.Info("proxmox provision finished", "name", p.Name, "status", p.Status)
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
func (a *API) enrollOverSSH(ctx context.Context, addr, name string, logf func(string, ...any)) (string, error) {
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
		User: "root", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, Timeout: 15 * time.Second,
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
	cmd := fmt.Sprintf("curl -fsSL %s/install.sh | sh -s -- --token %s --ca-sha256 %s", a.Cfg.PublicURL, raw, a.CA.Fingerprint())
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
