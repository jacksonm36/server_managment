package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jacksonm36/servermgr/panel/internal/proxmox"
	"github.com/jacksonm36/servermgr/panel/internal/store"
)

// Flavors (named VM sizes) and one-step deploys from the image catalog.

func (a *API) handleListFlavors(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.ListFlavors(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

var flavorName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,39}$`)

func (a *API) handleSaveFlavor(w http.ResponseWriter, r *http.Request) {
	var f store.ProxmoxFlavor
	if !readJSON(w, r, &f) {
		return
	}
	f.ID = 0
	if id := r.PathValue("fid"); id != "" {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad flavor id")
			return
		}
		f.ID = n
	}
	f.Description = strings.TrimSpace(f.Description)
	switch {
	case !flavorName.MatchString(f.Name):
		writeErr(w, http.StatusBadRequest, "name: lowercase letters, digits, - _ .")
		return
	case f.Cores < 1 || f.Cores > 128 || f.MemoryMB < 256 || f.MemoryMB > 1<<20 || f.DiskGB < 0 || f.DiskGB > 10000:
		writeErr(w, http.StatusBadRequest, "1-128 vCPUs, 256 MB to 1 TB of memory, disk up to 10000 GB")
		return
	case len(f.Description) > 200:
		writeErr(w, http.StatusBadRequest, "description: at most 200 characters")
		return
	}
	err := a.Store.SaveFlavor(r.Context(), &f)
	switch {
	case errors.Is(err, store.ErrExists):
		writeErr(w, http.StatusConflict, "a flavor with that name exists")
		return
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "flavor not found")
		return
	case err != nil:
		a.internal(w, err)
		return
	}
	a.audit(r, "", "proxmox.flavor_save", f.Name, f, true)
	writeJSON(w, http.StatusOK, f)
}

func (a *API) handleDeleteFlavor(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("fid"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad flavor id")
		return
	}
	if err := a.Store.DeleteFlavor(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "flavor not found")
		return
	} else if err != nil {
		a.internal(w, err)
		return
	}
	a.audit(r, "", "proxmox.flavor_delete", strconv.FormatInt(id, 10), nil, true)
	w.WriteHeader(http.StatusNoContent)
}

// ---- one-step deploy ----

type pveDeployReq struct {
	Image        string   `json:"image"`
	Node         string   `json:"node"`
	Name         string   `json:"name"`
	FlavorID     int64    `json:"flavor_id"` // sets cores, memory and disk; 0 uses the fields below
	Cores        int      `json:"cores"`
	MemoryMB     int      `json:"memory_mb"`
	DiskGB       int      `json:"disk_gb"`
	ImageStorage string   `json:"image_storage"`
	DiskStorage  string   `json:"disk_storage"`
	Bridge       string   `json:"bridge"`
	VLAN         int      `json:"vlan"` // 0 = untagged
	IP           string   `json:"ip"`
	Gateway      string   `json:"gateway"`
	Enroll       bool     `json:"enroll"`
	Apps         []string `json:"apps"`  // catalog apps installed once enrolled
	Count        int      `json:"count"` // VMs to create: name, or name-1..name-N
	pveCIOpts
}

// catalogTemplate finds the newest template servermgr built from the
// image on the node (it tags them servermgr;<image id>).
func catalogTemplate(res []proxmox.Resource, node, image string) int {
	best := 0
	for _, x := range res {
		if x.Type == "qemu" && x.Template == 1 && x.Node == node && slices.Contains(strings.Split(x.Tags, ";"), image) && x.VMID > best {
			best = x.VMID
		}
	}
	return best
}

// handlePVEDeploy creates a VM from a catalog image in one step: it reuses
// the image's template on the node, or builds one first, then clones it
// like New VM does. Platform admins only; tenants use the templates they
// were given.
func (a *API) handlePVEDeploy(w http.ResponseWriter, r *http.Request) {
	var req pveDeployReq
	if !readJSON(w, r, &req) {
		return
	}
	bad := func(m string) { writeErr(w, http.StatusBadRequest, m) }
	img := proxmox.ImageByID(req.Image)
	if img == nil {
		bad("pick an image")
		return
	}
	if !pveHost.MatchString(req.Name) {
		bad("name must be a valid hostname (lowercase letters, digits, dashes)")
		return
	}
	if req.FlavorID != 0 {
		f, err := a.Store.GetFlavor(r.Context(), req.FlavorID)
		if errors.Is(err, store.ErrNotFound) {
			bad("unknown flavor")
			return
		} else if err != nil {
			a.internal(w, err)
			return
		}
		req.Cores, req.MemoryMB, req.DiskGB = f.Cores, f.MemoryMB, f.DiskGB
	}
	if req.Cores < 1 || req.Cores > 128 || req.MemoryMB < 256 || req.MemoryMB > 1<<20 || req.DiskGB < 0 || req.DiskGB > 10000 {
		bad("resources out of range")
		return
	}
	if req.IP == "" {
		req.IP = "dhcp"
	}
	if req.Count == 0 {
		req.Count = 1
	}
	if req.Count < 1 || req.Count > 10 {
		bad("deploy 1 to 10 VMs at a time")
		return
	}
	if req.Count > 1 && req.IP != "dhcp" {
		bad("several VMs need DHCP; a static address fits one VM")
		return
	}
	if req.Count > 1 && len(req.Name) > 60 {
		bad("name too long for numbered VMs")
		return
	}
	if msg := checkDeployApps(req.Apps, req.Enroll); msg != "" {
		bad(msg)
		return
	}
	if msg := a.checkCIOpts(r.Context(), accessFrom(r.Context()), &req.pveCIOpts); msg != "" {
		bad(msg)
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
	s, ok := a.pveAdmin(w, r)
	if !ok {
		return
	}
	t, err := templateTargets(r.Context(), s.Cl, req.Node)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	res, err := s.Cl.Resources(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	tpl := catalogTemplate(res, req.Node, img.ID)
	switch {
	case !slices.Contains(t.Nodes, req.Node):
		bad("pick an online node")
		return
	case !slices.Contains(t.DiskStorages, req.DiskStorage):
		bad("pick a storage for the VM's disk")
		return
	case !slices.Contains(t.Bridges, req.Bridge):
		bad("pick a network bridge")
		return
	case req.VLAN < 0 || req.VLAN > 4094:
		bad("VLAN: 1-4094, or 0 for untagged")
		return
	case tpl == 0 && slices.Contains(t.FileStorages, req.ImageStorage):
		bad(fmt.Sprintf(`storage %s cannot hold images yet: enable the "Import" content type on it in Proxmox (Datacenter, Storage)`, req.ImageStorage))
		return
	case tpl == 0 && !slices.Contains(t.ImageStorages, req.ImageStorage):
		bad("pick a storage for the downloaded image")
		return
	}
	who := whoFrom(r)
	var ps []*store.ProxmoxProvision
	for i := 1; i <= req.Count; i++ {
		name := req.Name
		if req.Count > 1 {
			name = fmt.Sprintf("%s-%d", req.Name, i)
		}
		p := &store.ProxmoxProvision{ID: store.NewUUID(), ClusterID: s.C.ID, Name: name, Status: "running", CreatedBy: who.Actor}
		if err := a.Store.CreateProvision(r.Context(), p); err != nil {
			a.internal(w, err)
			return
		}
		ps = append(ps, p)
	}
	audit := req
	if audit.CIPassword != "" {
		audit.CIPassword = "(set)"
	}
	a.audit(r, "", "proxmox.deploy", s.C.Name+"/"+req.Name, audit, true)
	go a.runDeploy(who, s, ps, img, req, tpl)
	writeJSON(w, http.StatusAccepted, ps[0])
}

// runDeploy builds the template once if needed, then creates the VMs one
// after another (each clone needs the template unlocked).
func (a *API) runDeploy(who Who, s *pveScope, ps []*store.ProxmoxProvision, img *proxmox.Image, req pveDeployReq, tpl int) {
	ctx, cancel := context.WithTimeout(a.baseCtx(), 90*time.Minute)
	defer cancel()
	p := ps[0]
	logf := a.provisionLog(p)
	for _, q := range ps[1:] {
		a.provisionLog(q)("queued: starts after %s", p.Name)
	}
	err := func() error {
		if tpl != 0 {
			logf("using template %d, built earlier from %s", tpl, img.Name)
		} else {
			logf("no %s template on %s yet; building one first", img.Name, req.Node)
			var err error
			tpl, err = a.makeTemplate(ctx, s.Cl, img, pveTemplateReq{
				Image: img.ID, Node: req.Node, Name: strings.ReplaceAll(img.ID, ".", "-") + "-template",
				ImageStorage: req.ImageStorage, DiskStorage: req.DiskStorage, Bridge: req.Bridge, VLAN: req.VLAN, Cores: 2, MemoryMB: 2048,
			}, logf)
			if err != nil {
				return fmt.Errorf("building the template: %w", err)
			}
			logf("template %d is ready; it is reused by the next deploy of %s", tpl, img.Name)
		}
		return nil
	}()
	for i, q := range ps {
		ql := logf
		if i > 0 {
			ql = a.provisionLog(q)
		}
		if err == nil {
			err2 := a.provisionVM(ctx, who, s, q, pveProvisionReq{
				TemplateVMID: tpl, Node: req.Node, Name: q.Name, Cores: req.Cores, MemoryMB: req.MemoryMB, DiskGB: req.DiskGB,
				Disk: "scsi0", FullClone: true, Storage: req.DiskStorage, Bridge: req.Bridge, VLAN: req.VLAN, IP: req.IP, Gateway: req.Gateway, Enroll: req.Enroll,
				Apps: req.Apps, pveCIOpts: req.pveCIOpts,
			}, func() {}, ql)
			a.finishProvision(q, err2, ql)
			continue
		}
		if i > 0 {
			ql("not started: the template could not be built")
		}
		a.finishProvision(q, err, ql)
	}
}
