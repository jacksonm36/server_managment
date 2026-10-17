package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jacksonm36/servermgr/panel/internal/proxmox"
	"github.com/jacksonm36/servermgr/panel/internal/store"
)

// VM templates from official cloud images. A platform admin picks an
// image; Proxmox downloads it into a storage's import area, servermgr
// creates a cloud-init VM from it and turns that into a template, which
// the New VM dialog (and tenants given the template) can then clone.
// The build runs in the background and shows up with the provisioning
// runs.

func (a *API) handlePVEImages(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, proxmox.Images)
}

type pveStorageInfo struct {
	Storage string `json:"storage"`
	Type    string `json:"type"`
	Content string `json:"content"`
	Active  int    `json:"active"`
	Enabled int    `json:"enabled"`
	Avail   int64  `json:"avail"`
}

func (st pveStorageInfo) has(content string) bool {
	return slices.Contains(strings.Split(st.Content, ","), content)
}

type pveTemplateTargets struct {
	Nodes []string `json:"nodes"`
	// ImageStorages can hold downloaded images: they have the "import"
	// content type.
	ImageStorages []string `json:"image_storages"`
	// FileStorages could hold images once "import" is enabled on them.
	FileStorages []string `json:"file_storages"`
	DiskStorages []string `json:"disk_storages"`
	Bridges      []string `json:"bridges"`
}

func templateTargets(ctx context.Context, cl *proxmox.Client, node string) (*pveTemplateTargets, error) {
	out := &pveTemplateTargets{Nodes: []string{}, ImageStorages: []string{}, FileStorages: []string{}, DiskStorages: []string{}, Bridges: []string{}}
	var nodes []struct {
		Node   string `json:"node"`
		Status string `json:"status"`
	}
	if err := cl.Do(ctx, http.MethodGet, "/nodes", nil, &nodes); err != nil {
		return nil, err
	}
	for _, n := range nodes {
		if n.Status == "online" {
			out.Nodes = append(out.Nodes, n.Node)
		}
	}
	slices.Sort(out.Nodes)
	if node == "" && len(out.Nodes) > 0 {
		node = out.Nodes[0]
	}
	if !slices.Contains(out.Nodes, node) {
		return out, nil
	}
	var storages []pveStorageInfo
	if err := cl.Do(ctx, http.MethodGet, "/nodes/"+url.PathEscape(node)+"/storage", url.Values{"enabled": {"1"}}, &storages); err != nil {
		return nil, err
	}
	for _, st := range storages {
		if st.Active == 0 {
			continue
		}
		switch {
		case st.has("import"):
			out.ImageStorages = append(out.ImageStorages, st.Storage)
		case slices.Contains([]string{"dir", "nfs", "cifs", "cephfs", "glusterfs", "btrfs"}, st.Type):
			out.FileStorages = append(out.FileStorages, st.Storage)
		}
		if st.has("images") {
			out.DiskStorages = append(out.DiskStorages, st.Storage)
		}
	}
	slices.Sort(out.ImageStorages)
	slices.Sort(out.FileStorages)
	slices.Sort(out.DiskStorages)
	var ifs []struct {
		Iface string `json:"iface"`
	}
	if err := cl.Do(ctx, http.MethodGet, "/nodes/"+url.PathEscape(node)+"/network", url.Values{"type": {"any_bridge"}}, &ifs); err != nil {
		return nil, err
	}
	for _, i := range ifs {
		out.Bridges = append(out.Bridges, i.Iface)
	}
	slices.Sort(out.Bridges)
	return out, nil
}

func (a *API) handlePVETemplateTargets(w http.ResponseWriter, r *http.Request) {
	s, err := a.pveScope(r)
	if err != nil {
		scopeErr(w, err)
		return
	}
	node := r.URL.Query().Get("node")
	if node != "" && !pveNode.MatchString(node) {
		writeErr(w, http.StatusBadRequest, "bad node")
		return
	}
	t, err := templateTargets(r.Context(), s.Cl, node)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

type pveTemplateReq struct {
	Image        string `json:"image"`
	Node         string `json:"node"`
	Name         string `json:"name"`
	ImageStorage string `json:"image_storage"`
	DiskStorage  string `json:"disk_storage"`
	Bridge       string `json:"bridge"`
	VLAN         int    `json:"vlan"` // 0 = untagged
	Cores        int    `json:"cores"`
	MemoryMB     int    `json:"memory_mb"`
	DiskGB       int    `json:"disk_gb"` // 0 keeps the image's size
}

func (a *API) handlePVETemplateCreate(w http.ResponseWriter, r *http.Request) {
	var req pveTemplateReq
	if !readJSON(w, r, &req) {
		return
	}
	bad := func(m string) { writeErr(w, http.StatusBadRequest, m) }
	img := proxmox.ImageByID(req.Image)
	switch {
	case img == nil:
		bad("pick an image")
		return
	case !pveHost.MatchString(req.Name):
		bad("name must be a valid hostname (lowercase letters, digits, dashes)")
		return
	case req.Cores < 1 || req.Cores > 128 || req.MemoryMB < 256 || req.MemoryMB > 1<<20 || req.DiskGB < 0 || req.DiskGB > 10000:
		bad("resources out of range")
		return
	}
	s, err := a.pveScope(r)
	if err != nil {
		scopeErr(w, err)
		return
	}
	if s.TC != nil {
		writeErr(w, http.StatusForbidden, "templates are made by the platform's administrators")
		return
	}
	t, err := templateTargets(r.Context(), s.Cl, req.Node)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	switch {
	case !slices.Contains(t.Nodes, req.Node):
		bad("pick an online node")
		return
	case slices.Contains(t.FileStorages, req.ImageStorage):
		bad(fmt.Sprintf(`storage %s cannot hold images yet: enable the "Import" content type on it in Proxmox (Datacenter, Storage)`, req.ImageStorage))
		return
	case !slices.Contains(t.ImageStorages, req.ImageStorage):
		bad("pick a storage for the downloaded image")
		return
	case !slices.Contains(t.DiskStorages, req.DiskStorage):
		bad("pick a storage for the template's disk")
		return
	case !slices.Contains(t.Bridges, req.Bridge):
		bad("pick a network bridge")
		return
	case req.VLAN < 0 || req.VLAN > 4094:
		bad("VLAN: 1-4094, or 0 for untagged")
		return
	}
	who := whoFrom(r)
	p := &store.ProxmoxProvision{ID: store.NewUUID(), ClusterID: s.C.ID, Name: "template " + req.Name, Status: "running", CreatedBy: who.Actor}
	if err := a.Store.CreateProvision(r.Context(), p); err != nil {
		a.internal(w, err)
		return
	}
	a.audit(r, "", "proxmox.template_create", s.C.Name+"/"+req.Name, req, true)
	go a.buildTemplate(s, p, img, req)
	writeJSON(w, http.StatusAccepted, p)
}

func (a *API) buildTemplate(s *pveScope, p *store.ProxmoxProvision, img *proxmox.Image, req pveTemplateReq) {
	ctx, cancel := context.WithTimeout(a.baseCtx(), 60*time.Minute)
	defer cancel()
	logf := a.provisionLog(p)
	vmid, err := a.makeTemplate(ctx, s.Cl, img, req, logf)
	if err == nil {
		logf("done: template %d is ready to use in New VM", vmid)
	}
	if vmid != 0 {
		p.VMID = &vmid
	}
	a.finishProvision(p, err, logf)
}

// makeTemplate has Proxmox download the image and builds a cloud-init
// template from it on req.Node. It returns the template's VMID (also when
// a later step failed, once the VM exists).
func (a *API) makeTemplate(ctx context.Context, cl *proxmox.Client, img *proxmox.Image, req pveTemplateReq, logf func(string, ...any)) (int, error) {
	node := req.Node
	nodePath := "/nodes/" + url.PathEscape(node)
	id := 0
	err := func() error {
		// A fresh download each time, so a template gets the vendor's
		// current image. Templates keep their own copy of the disk.
		file := img.ImportName()
		volid := req.ImageStorage + ":import/" + file
		contentPath := nodePath + "/storage/" + url.PathEscape(req.ImageStorage) + "/content"
		var existing []struct {
			VolID string `json:"volid"`
		}
		if err := cl.Do(ctx, http.MethodGet, contentPath, url.Values{"content": {"import"}}, &existing); err != nil {
			return err
		}
		if slices.ContainsFunc(existing, func(e struct {
			VolID string `json:"volid"`
		}) bool {
			return e.VolID == volid
		}) {
			logf("removing the previous download of %s", file)
			var upid string
			if err := cl.Do(ctx, http.MethodDelete, contentPath+"/"+url.PathEscape(volid), nil, &upid); err != nil {
				return err
			}
			if upid != "" {
				if err := cl.WaitTask(ctx, node, upid); err != nil {
					return err
				}
			}
		}
		form := url.Values{"content": {"import"}, "filename": {file}, "url": {img.URL}, "verify-certificates": {"1"}}
		if algo, sum, err := img.FetchChecksum(ctx); err != nil {
			logf("warning: no checksum to verify against (%v)", err)
		} else {
			form.Set("checksum", sum)
			form.Set("checksum-algorithm", algo)
			logf("expecting %s %s", algo, sum)
		}
		logf("Proxmox downloads %s to %s on %s", img.URL, req.ImageStorage, node)
		var upid string
		if err := cl.Do(ctx, http.MethodPost, nodePath+"/storage/"+url.PathEscape(req.ImageStorage)+"/download-url", form, &upid); err != nil {
			return err
		}
		if err := cl.WaitTask(ctx, node, upid); err != nil {
			return fmt.Errorf("download: %w", err)
		}

		var next flexString
		if err := cl.Do(ctx, http.MethodGet, "/cluster/nextid", nil, &next); err != nil {
			return err
		}
		vmid, err := strconv.Atoi(string(next))
		if err != nil {
			return fmt.Errorf("unexpected nextid %q", next)
		}
		id = vmid
		logf("creating VM %d (%s) with the image as its disk on %s", vmid, req.Name, req.DiskStorage)
		cfg := url.Values{
			"vmid":        {strconv.Itoa(vmid)},
			"name":        {req.Name},
			"cores":       {strconv.Itoa(req.Cores)},
			"memory":      {strconv.Itoa(req.MemoryMB)},
			"cpu":         {"x86-64-v2-AES"},
			"ostype":      {"l26"},
			"scsihw":      {"virtio-scsi-single"},
			"scsi0":       {req.DiskStorage + ":0,import-from=" + volid + ",discard=on,iothread=1"},
			"ide2":        {req.DiskStorage + ":cloudinit"},
			"boot":        {"order=scsi0"},
			"serial0":     {"socket"},
			"vga":         {"serial0"},
			"agent":       {"enabled=1"},
			"net0":        {"virtio,bridge=" + req.Bridge + vlanOpt(req.VLAN) + ",firewall=1"},
			"ciuser":      {img.User},
			"ipconfig0":   {"ip=dhcp"},
			"tags":        {"servermgr;" + img.ID},
			"description": {fmt.Sprintf("%s cloud image, made into a template by servermgr on %s.\nSource: %s", img.Name, time.Now().UTC().Format("2006-01-02"), img.URL)},
		}
		if err := cl.Do(ctx, http.MethodPost, nodePath+"/qemu", cfg, &upid); err != nil {
			return err
		}
		if err := cl.WaitTask(ctx, node, upid); err != nil {
			return err
		}
		if req.DiskGB > 0 {
			logf("setting the disk to %d GB", req.DiskGB)
			err := cl.Do(ctx, http.MethodPut, proxmox.GuestPath(node, "qemu", vmid)+"/resize", url.Values{"disk": {"scsi0"}, "size": {fmt.Sprintf("%dG", req.DiskGB)}}, nil)
			var ae *proxmox.APIError
			if errors.As(err, &ae) && strings.Contains(ae.Msg, "shrink") {
				logf("the image is already bigger than %d GB; keeping its size", req.DiskGB)
			} else if err != nil {
				return err
			}
		}
		logf("converting VM %d to a template", vmid)
		upid = ""
		if err := cl.Do(ctx, http.MethodPost, proxmox.GuestPath(node, "qemu", vmid)+"/template", nil, &upid); err != nil {
			return err
		}
		if upid != "" {
			if err := cl.WaitTask(ctx, node, upid); err != nil {
				return err
			}
		}
		return nil
	}()
	return id, err
}
