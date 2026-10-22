package api

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/jacksonm36/servermgr/panel/internal/proxmox"
)

// Guest actions that start a Proxmox task: clone, migrate and convert to
// template. They answer 202 with the task's UPID; the guest page follows
// it in its task list.

func (a *API) startGuestTask(w http.ResponseWriter, r *http.Request, s *pveScope, node, typ string, vmid int, sub string, form url.Values, audit string) {
	var upid string
	target := fmt.Sprintf("%s/%d", s.C.Name, vmid)
	if err := s.Cl.Do(r.Context(), http.MethodPost, proxmox.GuestPath(node, typ, vmid)+sub, form, &upid); err != nil {
		a.audit(r, "", audit, target, map[string]string{"error": err.Error()}, false)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	a.audit(r, "", audit, target, form, true)
	writeJSON(w, http.StatusAccepted, map[string]string{"upid": upid})
}

func (a *API) handlePVEClone(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Full    bool   `json:"full"`
		Target  string `json:"target"`
		Storage string `json:"storage"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s, node, typ, vmid, ok := a.pveGuest(w, r)
	if !ok {
		return
	}
	if !pveHost.MatchString(req.Name) {
		writeErr(w, http.StatusBadRequest, "name: lowercase letters, digits and hyphens")
		return
	}
	res, err := s.Cl.Resources(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	i := slices.IndexFunc(res, func(x proxmox.Resource) bool { return x.Type == typ && x.VMID == vmid })
	if i < 0 {
		writeErr(w, http.StatusNotFound, errNoGuest.Error())
		return
	}
	src := res[i]
	if req.Target != "" && req.Target != node {
		if !pveNode.MatchString(req.Target) || !slices.ContainsFunc(res, func(x proxmox.Resource) bool { return x.Type == "node" && x.Node == req.Target }) {
			writeErr(w, http.StatusBadRequest, "unknown target node")
			return
		}
		if s.TC != nil {
			writeErr(w, http.StatusBadRequest, "clones stay on the same node")
			return
		}
	}
	// A linked clone only exists from a template.
	if src.Template == 0 {
		req.Full = true
	}
	if s.TC != nil {
		// A linked clone would live on the template's storage.
		req.Full = true
		if req.Storage == "" && len(s.TC.Storages) == 1 {
			req.Storage = s.TC.Storages[0]
		}
		if !slices.Contains(s.TC.Storages, req.Storage) {
			writeErr(w, http.StatusBadRequest, "pick one of your storages")
			return
		}
		a.pveMu.Lock()
		total := poolUsage(res, s.TC.Pool)
		total.add(a.pveReserved[s.TC.TenantID])
		a.pveMu.Unlock()
		add := guestUsage(src)
		total.add(add)
		if limit := overQuota(accessFrom(r.Context()).Tenant, total); limit != "" {
			writeErr(w, http.StatusForbidden, "the clone would go over your limit of "+limit)
			return
		}
	} else if req.Storage != "" && !pveStorage.MatchString(req.Storage) {
		writeErr(w, http.StatusBadRequest, "bad storage")
		return
	}
	var next flexString
	if err := s.Cl.Do(r.Context(), http.MethodGet, "/cluster/nextid", nil, &next); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	newid, err := strconv.Atoi(string(next))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "unexpected nextid "+string(next))
		return
	}
	form := url.Values{"newid": {strconv.Itoa(newid)}, map[string]string{"qemu": "name", "lxc": "hostname"}[typ]: {req.Name}, "full": {b01(req.Full)}}
	if req.Target != "" && req.Target != node {
		form.Set("target", req.Target)
	}
	if req.Storage != "" && req.Full {
		form.Set("storage", req.Storage)
	}
	if s.TC != nil {
		form.Set("pool", s.TC.Pool)
	}
	a.startGuestTask(w, r, s, node, typ, vmid, "/clone", form, "proxmox.clone")
}

// handlePVEMigrate moves a guest to another node (platform admins only:
// tenants don't choose nodes).
func (a *API) handlePVEMigrate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target string `json:"target"`
		Online bool   `json:"online"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s, node, typ, vmid, ok := a.pveGuest(w, r)
	if !ok {
		return
	}
	if s.TC != nil {
		writeErr(w, http.StatusForbidden, "only platform admins migrate guests")
		return
	}
	res, err := s.Cl.Resources(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if req.Target == node || !slices.ContainsFunc(res, func(x proxmox.Resource) bool {
		return x.Type == "node" && x.Node == req.Target && x.Status == "online"
	}) {
		writeErr(w, http.StatusBadRequest, "pick another online node")
		return
	}
	form := url.Values{"target": {req.Target}}
	if req.Online {
		if typ == "qemu" {
			form.Set("online", "1")
		} else {
			form.Set("restart", "1") // containers restart on the new node
		}
	}
	a.startGuestTask(w, r, s, node, typ, vmid, "/migrate", form, "proxmox.migrate")
}

// handlePVEMakeTemplate converts a stopped guest into a template; this
// cannot be undone.
func (a *API) handlePVEMakeTemplate(w http.ResponseWriter, r *http.Request) {
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
	if req.Confirm != strconv.Itoa(vmid) {
		writeErr(w, http.StatusBadRequest, "type the VMID to confirm")
		return
	}
	var st struct {
		Status string `json:"status"`
	}
	if err := s.Cl.Do(r.Context(), http.MethodGet, proxmox.GuestPath(node, typ, vmid)+"/status/current", nil, &st); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if st.Status != "stopped" {
		writeErr(w, http.StatusConflict, "stop the guest first")
		return
	}
	a.pveGuestCall(w, r, http.MethodPost, "/template", nil, "proxmox.make_template")
}
