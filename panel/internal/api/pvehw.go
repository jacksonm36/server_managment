package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jacksonm36/servermgr/panel/internal/proxmox"
)

// Guest hardware and lifecycle: edit a guest's configuration (CPU, memory,
// disks, network cards, CD/DVD, boot order, options), resize and move
// disks, clone, migrate, convert to a template, and create VMs and
// containers from an ISO or a container template. Every route goes through
// pveGuest or pveScope, so a tenant's user stays in their pool, on their
// own storages and bridges, and cannot pass host devices through.

var (
	pveQemuKey = regexp.MustCompile(`^(name|description|tags|onboot|protection|startup|boot|agent|ostype|cores|sockets|cpu|cpulimit|cpuunits|vcpus|numa|memory|balloon|shares|bios|machine|scsihw|vga|hotplug|tablet|kvm|acpi|localtime|freeze|hookscript|(scsi|virtio|sata|ide|net|usb|serial|hostpci|unused|numa)\d+|efidisk0|tpmstate0|rng0|audio0|watchdog)$`)
	pveLXCKey  = regexp.MustCompile(`^(hostname|description|tags|onboot|protection|startup|cores|cpulimit|cpuunits|memory|swap|nameserver|searchdomain|features|console|tty|cmode|ostype|(net|mp|unused|dev)\d+|rootfs)$`)
	// pveHostKey are host devices, hooks and container privileges: platform
	// users only.
	pveHostKey = regexp.MustCompile(`^(hostpci\d+|usb\d+|serial\d+|dev\d+|features|hookscript|kvm|unprivileged|arch)$`)
	// pveVolKey are keys whose value starts with a volume or storage.
	pveVolKey    = regexp.MustCompile(`^((scsi|virtio|sata|ide|mp|unused)\d+|efidisk0|tpmstate0|rootfs)$`)
	pveBridgeOpt = regexp.MustCompile(`(?:^|,)bridge=([^,]+)`)
	pveParamKey  = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,40}$`)
)

// checkGuestValue vets one configuration value for the caller's scope.
func checkGuestValue(s *pveScope, key, val string) error {
	if strings.ContainsRune(val, 0) || (key != "description" && strings.ContainsAny(val, "\r\n")) {
		return fmt.Errorf("%s: invalid characters", key)
	}
	if len(val) > 8192 {
		return fmt.Errorf("%s: too long", key)
	}
	if s.TC == nil {
		return nil
	}
	if pveHostKey.MatchString(key) {
		return fmt.Errorf("%s cannot be changed by tenant users", key)
	}
	if pveVolKey.MatchString(key) {
		first, _, _ := strings.Cut(val, ",")
		if first == "none" || first == "cdrom" || first == "" {
			return nil
		}
		if strings.HasPrefix(first, "/") {
			return fmt.Errorf("%s: host paths are not allowed", key)
		}
		stor, _, _ := strings.Cut(first, ":")
		if !slices.Contains(s.TC.Storages, stor) {
			return fmt.Errorf("%s: storage %q is not available to you", key, stor)
		}
	}
	if m := pveBridgeOpt.FindStringSubmatch(val); m != nil && strings.HasPrefix(key, "net") && !slices.Contains(s.TC.Bridges, m[1]) {
		return fmt.Errorf("%s: bridge %q is not available to you", key, m[1])
	}
	return nil
}

// auditForm is form for the audit log, without passwords.
func auditForm(form url.Values) map[string]string {
	out := make(map[string]string, len(form))
	for k := range form {
		v := form.Get(k)
		if strings.Contains(k, "password") || strings.Contains(k, "secret") || k == "ssh-public-keys" || k == "sshkeys" {
			v = "••••••"
		} else if len(v) > 300 {
			v = v[:300] + "…"
		}
		out[k] = v
	}
	return out
}

// pveForm turns JSON parameters into a Proxmox form: strings, numbers and
// booleans (sent as 1/0) under plain parameter names.
func pveForm(m map[string]any) (url.Values, error) {
	form := url.Values{}
	for k, v := range m {
		if !pveParamKey.MatchString(k) {
			return nil, fmt.Errorf("bad parameter name %q", k)
		}
		var s string
		switch x := v.(type) {
		case string:
			s = x
		case float64:
			s = strconv.FormatFloat(x, 'f', -1, 64)
		case bool:
			s = "0"
			if x {
				s = "1"
			}
		case nil:
			continue
		default:
			return nil, fmt.Errorf("%s: must be a string, number or boolean", k)
		}
		if strings.ContainsRune(s, 0) || len(s) > 65536 {
			return nil, fmt.Errorf("%s: invalid value", k)
		}
		form.Set(k, s)
	}
	return form, nil
}

// pveRun calls Proxmox and, when the answer is a task, waits for it up to
// wait. A task still running then (or wait == 0) is reported as started,
// with its UPID, so the UI can follow it.
func (a *API) pveRun(w http.ResponseWriter, r *http.Request, cl *proxmox.Client, node, method, path string, form url.Values, action, target string, wait time.Duration) {
	var raw json.RawMessage
	if err := cl.Do(r.Context(), method, path, form, &raw); err != nil {
		a.audit(r, "", action, target, map[string]string{"error": err.Error()}, false)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	var upid string
	_ = json.Unmarshal(raw, &upid)
	if !strings.HasPrefix(upid, "UPID:") {
		upid = ""
	}
	done := true
	if upid != "" {
		if wait <= 0 {
			done = false
		} else {
			ctx, cancel := context.WithTimeout(r.Context(), wait)
			err := cl.WaitTask(ctx, node, upid)
			timedOut := ctx.Err() != nil && r.Context().Err() == nil
			cancel()
			switch {
			case timedOut:
				done = false
			case err != nil:
				a.audit(r, "", action, target, map[string]string{"error": err.Error(), "upid": upid}, false)
				writeErr(w, http.StatusBadGateway, err.Error())
				return
			}
		}
	}
	a.audit(r, "", action, target, auditForm(form), true)
	writeJSON(w, http.StatusOK, map[string]any{"upid": upid, "done": done})
}

func guestTarget(s *pveScope, vmid int) string { return fmt.Sprintf("%s/%d", s.C.Name, vmid) }

// nextVMID asks the cluster for a free VMID.
func nextVMID(ctx context.Context, cl *proxmox.Client) (int, error) {
	var v any
	if err := cl.Do(ctx, http.MethodGet, "/cluster/nextid", nil, &v); err != nil {
		return 0, err
	}
	switch x := v.(type) {
	case string:
		return strconv.Atoi(x)
	case float64:
		return int(x), nil
	}
	return 0, fmt.Errorf("unexpected next VMID %v", v)
}

// handlePVECreateGuest creates a VM (usually from an ISO) or a container
// (from a container template) on a node.
func (a *API) handlePVECreateGuest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type   string            `json:"type"`
		Params map[string]string `json:"params"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	node := r.PathValue("node")
	if !pveNode.MatchString(node) || (req.Type != "qemu" && req.Type != "lxc") {
		writeErr(w, http.StatusBadRequest, "pick a node and qemu or lxc")
		return
	}
	s, err := a.pveScope(r)
	if err != nil {
		scopeErr(w, err)
		return
	}
	keyRe := pveQemuKey
	extra := map[string]bool{"vmid": true, "start": true, "pool": true}
	if req.Type == "lxc" {
		keyRe = pveLXCKey
		for _, k := range []string{"ostemplate", "password", "ssh-public-keys", "unprivileged", "storage"} {
			extra[k] = true
		}
	}
	form := url.Values{}
	for k, v := range req.Params {
		if !keyRe.MatchString(k) && !extra[k] {
			writeErr(w, http.StatusBadRequest, k+" cannot be set here")
			return
		}
		if s.TC != nil && k == "unprivileged" && v != "1" {
			writeErr(w, http.StatusBadRequest, "containers must be unprivileged")
			return
		}
		check := k
		if k == "ostemplate" {
			check = "rootfs" // a volume on a storage, checked the same way
		}
		if k != "unprivileged" {
			if err := checkGuestValue(s, check, v); err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		form.Set(k, v)
	}
	if s.TC != nil {
		if st := form.Get("storage"); st != "" && !slices.Contains(s.TC.Storages, st) {
			writeErr(w, http.StatusBadRequest, "that storage is not available to you")
			return
		}
		form.Set("pool", s.TC.Pool)
		if req.Type == "lxc" {
			form.Set("unprivileged", "1")
		}
	}
	if form.Get("vmid") == "" {
		id, err := nextVMID(r.Context(), s.Cl)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		form.Set("vmid", strconv.Itoa(id))
	}
	vmid, err := strconv.Atoi(form.Get("vmid"))
	if err != nil || vmid < 100 || vmid > 999999999 {
		writeErr(w, http.StatusBadRequest, "VMID must be 100 or more")
		return
	}
	if req.Type == "lxc" && form.Get("ostemplate") == "" {
		writeErr(w, http.StatusBadRequest, "pick a container template")
		return
	}
	var raw json.RawMessage
	path := "/nodes/" + url.PathEscape(node) + "/" + req.Type
	target := guestTarget(s, vmid)
	if err := s.Cl.Do(r.Context(), http.MethodPost, path, form, &raw); err != nil {
		a.audit(r, "", "proxmox.create_guest", target, map[string]string{"error": err.Error()}, false)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	var upid string
	_ = json.Unmarshal(raw, &upid)
	// Wait for the guest to exist (and report a failed creation) unless it
	// takes long, e.g. a big template being unpacked.
	done := true
	if strings.HasPrefix(upid, "UPID:") {
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		err := s.Cl.WaitTask(ctx, node, upid)
		timedOut := ctx.Err() != nil && r.Context().Err() == nil
		cancel()
		if timedOut {
			done = false
		} else if err != nil {
			a.audit(r, "", "proxmox.create_guest", target, map[string]string{"error": err.Error()}, false)
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
	}
	a.audit(r, "", "proxmox.create_guest", target, auditForm(form), true)
	writeJSON(w, http.StatusOK, map[string]any{"upid": upid, "done": done, "vmid": vmid, "node": node, "type": req.Type})
}

type pveNodeStorage struct {
	Storage string  `json:"storage"`
	Type    string  `json:"type"`
	Content string  `json:"content"`
	Active  int     `json:"active"`
	Enabled int     `json:"enabled"`
	Shared  int     `json:"shared"`
	Avail   int64   `json:"avail"`
	Total   int64   `json:"total"`
	Used    int64   `json:"used"`
	Frac    float64 `json:"used_fraction"`
}

type pveVolumeInfo struct {
	VolID   string `json:"volid"`
	Format  string `json:"format,omitempty"`
	Size    int64  `json:"size"`
	CTime   int64  `json:"ctime,omitempty"`
	Content string `json:"content"`
	VMID    any    `json:"vmid,omitempty"`
	Notes   string `json:"notes,omitempty"`
}

// handlePVEChoices is what the create, clone, migrate and hardware forms
// offer on one node: storages, ISOs, container templates, bridges, nodes
// and a free VMID.
func (a *API) handlePVEChoices(w http.ResponseWriter, r *http.Request) {
	node := r.PathValue("node")
	if !pveNode.MatchString(node) {
		writeErr(w, http.StatusBadRequest, "bad node")
		return
	}
	s, err := a.pveScope(r)
	if err != nil {
		scopeErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	np := "/nodes/" + url.PathEscape(node)
	var storages []pveNodeStorage
	if err := s.Cl.Do(ctx, http.MethodGet, np+"/storage", nil, &storages); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	out := map[string]any{}
	stOut := []pveNodeStorage{}
	isos, tmpls := []string{}, []string{}
	for _, st := range storages {
		if st.Active == 0 || (s.TC != nil && !slices.Contains(s.TC.Storages, st.Storage)) {
			continue
		}
		stOut = append(stOut, st)
		content := strings.Split(st.Content, ",")
		for _, kind := range []string{"iso", "vztmpl"} {
			if !slices.Contains(content, kind) {
				continue
			}
			var vols []pveVolumeInfo
			if s.Cl.Do(ctx, http.MethodGet, np+"/storage/"+url.PathEscape(st.Storage)+"/content", url.Values{"content": {kind}}, &vols) != nil {
				continue
			}
			for _, v := range vols {
				switch {
				case v.Content != "" && v.Content != kind:
					// only what was asked for, even if a storage lists more
				case kind == "iso":
					isos = append(isos, v.VolID)
				default:
					tmpls = append(tmpls, v.VolID)
				}
			}
		}
	}
	slices.Sort(isos)
	slices.Sort(tmpls)
	out["storages"], out["isos"], out["templates"] = stOut, isos, tmpls

	var nets []struct {
		Iface string `json:"iface"`
	}
	bridges := []string{}
	if s.Cl.Do(ctx, http.MethodGet, np+"/network", url.Values{"type": {"any_bridge"}}, &nets) == nil {
		for _, n := range nets {
			if s.TC == nil || slices.Contains(s.TC.Bridges, n.Iface) {
				bridges = append(bridges, n.Iface)
			}
		}
	} else if s.TC != nil {
		bridges = append(bridges, s.TC.Bridges...)
	}
	slices.Sort(bridges)
	out["bridges"] = bridges

	nodes := []string{}
	if res, err := s.Cl.Resources(ctx); err == nil {
		for _, x := range res {
			if x.Type == "node" && x.Status == "online" {
				nodes = append(nodes, x.Node)
			}
		}
	}
	slices.Sort(nodes)
	out["nodes"] = nodes
	if id, err := nextVMID(ctx, s.Cl); err == nil {
		out["nextid"] = id
	}
	writeJSON(w, http.StatusOK, out)
}
