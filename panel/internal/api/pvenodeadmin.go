package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Node management: status and history, services, package updates, disks,
// network, DNS and time, the system log and tasks, power, bulk guest
// actions, storage contents (browse, upload, download, delete) and the
// node's own shell. All of it is for platform users with proxmox.manage;
// tenants never reach a node.

var (
	pveService = regexp.MustCompile(`^[a-z0-9][a-z0-9@._-]{0,63}$`)
	pveIface   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,19}$`)
	pveFile    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,190}\.(iso|img|tar\.(gz|xz|zst)|tgz)$`)
)

// pveNodeReads are the node views, by the name the UI asks for.
var pveNodeReads = map[string]string{
	"status": "/status", "services": "/services", "updates": "/apt/update", "versions": "/apt/versions",
	"repos": "/apt/repositories", "disks": "/disks/list", "smart": "/disks/smart", "zfs": "/disks/zfs",
	"lvm": "/disks/lvm", "lvmthin": "/disks/lvmthin", "directory": "/disks/directory",
	"network": "/network", "dns": "/dns", "time": "/time", "hosts": "/hosts", "storage": "/storage",
	"subscription": "/subscription", "certificates": "/certificates/info", "replication": "/replication",
	"pci": "/hardware/pci", "usb": "/hardware/usb", "netstat": "/netstat", "report": "/report",
}

// pveNodeWrites are node settings that can be changed, with the method
// Proxmox takes for each.
var pveNodeWrites = map[string][2]string{"dns": {"/dns", http.MethodPut}, "time": {"/time", http.MethodPut}, "hosts": {"/hosts", http.MethodPost}}

// pveNodeScope resolves {id}/{node} for a platform user.
func (a *API) pveNodeScope(w http.ResponseWriter, r *http.Request) (*pveScope, string, bool) {
	node := r.PathValue("node")
	if !pveNode.MatchString(node) {
		writeErr(w, http.StatusBadRequest, "bad node")
		return nil, "", false
	}
	s, err := a.pveScope(r)
	if err != nil {
		scopeErr(w, err)
		return nil, "", false
	}
	if s.TC != nil {
		writeErr(w, http.StatusNotFound, errNoCluster.Error())
		return nil, "", false
	}
	return s, node, true
}

func nodeTarget(s *pveScope, node string) string { return s.C.Name + "/" + node }

// readParams reads {"params": {...}} as a Proxmox form.
func readParams(w http.ResponseWriter, r *http.Request) (url.Values, bool) {
	var req struct {
		Params map[string]any `json:"params"`
	}
	if !readJSON(w, r, &req) {
		return nil, false
	}
	form, err := pveForm(req.Params)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	return form, true
}

// pveGetRaw relays a Proxmox GET answer as it is.
func (a *API) pveGetRaw(w http.ResponseWriter, r *http.Request, s *pveScope, path string, form url.Values) {
	var out json.RawMessage
	if err := s.Cl.Do(r.Context(), http.MethodGet, path, form, &out); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if len(out) == 0 {
		out = json.RawMessage("null")
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

func (a *API) handlePVENodeView(w http.ResponseWriter, r *http.Request) {
	sub, ok := pveNodeReads[r.PathValue("what")]
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown node view")
		return
	}
	s, node, ok := a.pveNodeScope(w, r)
	if !ok {
		return
	}
	form := url.Values{}
	if r.PathValue("what") == "smart" {
		disk := r.URL.Query().Get("disk")
		if !pveDevPath.MatchString(disk) {
			writeErr(w, http.StatusBadRequest, "bad disk")
			return
		}
		form.Set("disk", disk)
	}
	a.pveGetRaw(w, r, s, nodePath(node)+sub, form)
}

func (a *API) handlePVENodeSet(w http.ResponseWriter, r *http.Request) {
	wr, ok := pveNodeWrites[r.PathValue("what")]
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown node setting")
		return
	}
	form, ok := readParams(w, r)
	if !ok {
		return
	}
	s, node, ok := a.pveNodeScope(w, r)
	if !ok {
		return
	}
	a.pveRun(w, r, s.Cl, node, wr[1], nodePath(node)+wr[0], form, "proxmox.node_"+r.PathValue("what"), nodeTarget(s, node), time.Minute)
}

func (a *API) handlePVENodeSyslog(w http.ResponseWriter, r *http.Request) {
	s, node, ok := a.pveNodeScope(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	form := url.Values{"limit": {"500"}}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n <= 5000 {
		form.Set("limit", strconv.Itoa(n))
	}
	if n, err := strconv.Atoi(q.Get("start")); err == nil && n >= 0 {
		form.Set("start", strconv.Itoa(n))
	}
	if svc := q.Get("service"); svc != "" {
		if !pveService.MatchString(svc) {
			writeErr(w, http.StatusBadRequest, "bad service")
			return
		}
		form.Set("service", svc)
	}
	a.pveGetRaw(w, r, s, nodePath(node)+"/syslog", form)
}

// nodeUPID checks that upid is a task of node.
func nodeUPID(node, upid string) bool {
	f := strings.Split(upid, ":")
	return len(f) >= 8 && f[0] == "UPID" && f[1] == node
}

func (a *API) handlePVENodeTaskLog(w http.ResponseWriter, r *http.Request) {
	s, node, ok := a.pveNodeScope(w, r)
	if !ok {
		return
	}
	upid := r.URL.Query().Get("upid")
	if !nodeUPID(node, upid) {
		writeErr(w, http.StatusNotFound, "no such task on this node")
		return
	}
	var lines []struct {
		N int    `json:"n"`
		T string `json:"t"`
	}
	tp := nodePath(node) + "/tasks/" + url.PathEscape(upid)
	if err := s.Cl.Do(r.Context(), http.MethodGet, tp+"/log", url.Values{"limit": {"5000"}}, &lines); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	var st map[string]any
	_ = s.Cl.Do(r.Context(), http.MethodGet, tp+"/status", nil, &st)
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.T)
		b.WriteByte('\n')
	}
	writeJSON(w, http.StatusOK, map[string]any{"log": b.String(), "status": st})
}

func (a *API) handlePVENodeTaskStop(w http.ResponseWriter, r *http.Request) {
	s, node, ok := a.pveNodeScope(w, r)
	if !ok {
		return
	}
	upid := r.URL.Query().Get("upid")
	if !nodeUPID(node, upid) {
		writeErr(w, http.StatusNotFound, "no such task on this node")
		return
	}
	a.pveRun(w, r, s.Cl, node, http.MethodDelete, nodePath(node)+"/tasks/"+url.PathEscape(upid), nil, "proxmox.task_stop", nodeTarget(s, node), 0)
}

func (a *API) handlePVENodePower(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Command string `json:"command"`
		Confirm string `json:"confirm"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Command != "reboot" && req.Command != "shutdown" {
		writeErr(w, http.StatusBadRequest, "command: reboot or shutdown")
		return
	}
	s, node, ok := a.pveNodeScope(w, r)
	if !ok {
		return
	}
	if req.Confirm != node {
		writeErr(w, http.StatusBadRequest, "type the node name to confirm")
		return
	}
	a.pveRun(w, r, s.Cl, node, http.MethodPost, nodePath(node)+"/status", url.Values{"command": {req.Command}}, "proxmox.node_"+req.Command, nodeTarget(s, node), 0)
}

func (a *API) handlePVENodeService(w http.ResponseWriter, r *http.Request) {
	svc, action := r.PathValue("service"), r.PathValue("action")
	if !pveService.MatchString(svc) {
		writeErr(w, http.StatusBadRequest, "bad service")
		return
	}
	switch action {
	case "start", "stop", "restart", "reload":
	default:
		writeErr(w, http.StatusBadRequest, "action: start, stop, restart or reload")
		return
	}
	s, node, ok := a.pveNodeScope(w, r)
	if !ok {
		return
	}
	a.pveRun(w, r, s.Cl, node, http.MethodPost, nodePath(node)+"/services/"+url.PathEscape(svc)+"/"+action, nil, "proxmox.service_"+action, nodeTarget(s, node)+"/"+svc, time.Minute)
}

func (a *API) handlePVENodeAptRefresh(w http.ResponseWriter, r *http.Request) {
	s, node, ok := a.pveNodeScope(w, r)
	if !ok {
		return
	}
	a.pveRun(w, r, s.Cl, node, http.MethodPost, nodePath(node)+"/apt/update", nil, "proxmox.apt_refresh", nodeTarget(s, node), 0)
}

// handlePVENodeBulk starts or stops all guests, or migrates them away.
func (a *API) handlePVENodeBulk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string `json:"action"`
		Target string `json:"target"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	form := url.Values{}
	switch req.Action {
	case "startall", "stopall":
	case "migrateall":
		if !pveNode.MatchString(req.Target) {
			writeErr(w, http.StatusBadRequest, "pick a target node")
			return
		}
		form.Set("target", req.Target)
	default:
		writeErr(w, http.StatusBadRequest, "action: startall, stopall or migrateall")
		return
	}
	s, node, ok := a.pveNodeScope(w, r)
	if !ok {
		return
	}
	a.pveRun(w, r, s.Cl, node, http.MethodPost, nodePath(node)+"/"+req.Action, form, "proxmox."+req.Action, nodeTarget(s, node), 0)
}

// ---- network ----

func (a *API) handlePVENetworkCreate(w http.ResponseWriter, r *http.Request) {
	form, ok := readParams(w, r)
	if !ok {
		return
	}
	if !pveIface.MatchString(form.Get("iface")) || form.Get("type") == "" {
		writeErr(w, http.StatusBadRequest, "an interface needs a name and a type")
		return
	}
	s, node, ok := a.pveNodeScope(w, r)
	if !ok {
		return
	}
	a.pveRun(w, r, s.Cl, node, http.MethodPost, nodePath(node)+"/network", form, "proxmox.network_create", nodeTarget(s, node)+"/"+form.Get("iface"), time.Minute)
}

func (a *API) handlePVENetworkUpdate(w http.ResponseWriter, r *http.Request) {
	iface := r.PathValue("iface")
	if !pveIface.MatchString(iface) {
		writeErr(w, http.StatusBadRequest, "bad interface")
		return
	}
	form, ok := readParams(w, r)
	if !ok {
		return
	}
	if form.Get("type") == "" {
		writeErr(w, http.StatusBadRequest, "type is required")
		return
	}
	s, node, ok := a.pveNodeScope(w, r)
	if !ok {
		return
	}
	a.pveRun(w, r, s.Cl, node, http.MethodPut, nodePath(node)+"/network/"+url.PathEscape(iface), form, "proxmox.network_update", nodeTarget(s, node)+"/"+iface, time.Minute)
}

func (a *API) handlePVENetworkDelete(w http.ResponseWriter, r *http.Request) {
	iface := r.PathValue("iface")
	if !pveIface.MatchString(iface) {
		writeErr(w, http.StatusBadRequest, "bad interface")
		return
	}
	s, node, ok := a.pveNodeScope(w, r)
	if !ok {
		return
	}
	a.pveRun(w, r, s.Cl, node, http.MethodDelete, nodePath(node)+"/network/"+url.PathEscape(iface), nil, "proxmox.network_delete", nodeTarget(s, node)+"/"+iface, time.Minute)
}

// handlePVENetworkApply applies (PUT) or reverts (DELETE) pending changes.
func (a *API) handlePVENetworkApply(w http.ResponseWriter, r *http.Request) {
	s, node, ok := a.pveNodeScope(w, r)
	if !ok {
		return
	}
	action := "proxmox.network_apply"
	if r.Method == http.MethodDelete {
		action = "proxmox.network_revert"
	}
	a.pveRun(w, r, s.Cl, node, r.Method, nodePath(node)+"/network", nil, action, nodeTarget(s, node), 2*time.Minute)
}

// ---- disks ----

// pveDiskActions create storage on disks, or prepare and wipe disks.
var pveDiskActions = map[string]string{
	"zfs": "/disks/zfs", "lvm": "/disks/lvm", "lvmthin": "/disks/lvmthin", "directory": "/disks/directory",
	"initgpt": "/disks/initgpt", "wipedisk": "/disks/wipedisk",
}

func (a *API) handlePVENodeDisk(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	sub, ok := pveDiskActions[action]
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown disk action")
		return
	}
	var req struct {
		Params  map[string]any `json:"params"`
		Confirm string         `json:"confirm"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	form, err := pveForm(req.Params)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// The disk is "disk" (initgpt, wipedisk), "device" (lvm, lvmthin,
	// directory) or "devices" (zfs).
	disk, named := "", 0
	for _, k := range []string{"disk", "device", "devices"} {
		if v := form.Get(k); v != "" {
			disk, named = v, named+1
		}
	}
	if named != 1 {
		writeErr(w, http.StatusBadRequest, "pick a disk")
		return
	}
	for _, d := range strings.Split(disk, ",") {
		if !pveDevPath.MatchString(strings.TrimSpace(d)) {
			writeErr(w, http.StatusBadRequest, "bad disk "+d)
			return
		}
	}
	// Everything here destroys what is on the disk.
	if req.Confirm != disk {
		writeErr(w, http.StatusBadRequest, "type the disk path to confirm")
		return
	}
	s, node, ok := a.pveNodeScope(w, r)
	if !ok {
		return
	}
	method := http.MethodPost
	if action == "wipedisk" {
		method = http.MethodPut
	}
	a.pveRun(w, r, s.Cl, node, method, nodePath(node)+sub, form, "proxmox.disk_"+action, nodeTarget(s, node)+":"+disk, 0)
}

// ---- storage contents ----

func (a *API) storagePath(w http.ResponseWriter, r *http.Request) (*pveScope, string, string, bool) {
	st := r.PathValue("storage")
	if !pveStorage.MatchString(st) {
		writeErr(w, http.StatusBadRequest, "bad storage")
		return nil, "", "", false
	}
	s, node, ok := a.pveNodeScope(w, r)
	if !ok {
		return nil, "", "", false
	}
	return s, node, nodePath(node) + "/storage/" + url.PathEscape(st), true
}

func (a *API) handlePVEStorageStatus(w http.ResponseWriter, r *http.Request) {
	s, _, sp, ok := a.storagePath(w, r)
	if !ok {
		return
	}
	a.pveGetRaw(w, r, s, sp+"/status", nil)
}

// handlePVEStorageUpload streams an ISO or container template from the
// browser to the storage. The file is never stored on the panel.
func (a *API) handlePVEStorageUpload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	content, name := q.Get("content"), path.Base(q.Get("filename"))
	if content != "iso" && content != "vztmpl" && content != "import" {
		writeErr(w, http.StatusBadRequest, "content: iso, vztmpl or import")
		return
	}
	if !pveFile.MatchString(name) {
		writeErr(w, http.StatusBadRequest, "file name must end in .iso, .img or .tar.gz/.xz/.zst")
		return
	}
	s, node, sp, ok := a.storagePath(w, r)
	if !ok {
		return
	}
	fields := map[string]string{"content": content}
	if c := q.Get("checksum"); c != "" {
		alg := q.Get("checksum-algorithm")
		if !regexp.MustCompile(`^[0-9a-fA-F]{32,128}$`).MatchString(c) || (alg != "md5" && alg != "sha1" && alg != "sha224" && alg != "sha256" && alg != "sha384" && alg != "sha512") {
			writeErr(w, http.StatusBadRequest, "bad checksum")
			return
		}
		fields["checksum"], fields["checksum-algorithm"] = c, alg
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Hour)
	defer cancel()
	target := fmt.Sprintf("%s/%s:%s/%s", s.C.Name, r.PathValue("storage"), content, name)
	upid, err := s.Cl.Upload(ctx, sp+"/upload", fields, name, io.LimitReader(r.Body, 64<<30))
	if err != nil {
		a.audit(r, "", "proxmox.upload", target, map[string]string{"error": err.Error()}, false)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	a.audit(r, "", "proxmox.upload", target, map[string]string{"node": node}, true)
	writeJSON(w, http.StatusOK, map[string]any{"upid": upid, "done": false})
}

// ---- node shell ----

// handlePVENodeShell opens the node's shell (or its package upgrade) as a
// text console; the browser then connects like to a guest console.
func (a *API) handlePVENodeShell(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cmd string `json:"cmd"` // "" | upgrade
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Cmd != "" && req.Cmd != "upgrade" {
		writeErr(w, http.StatusBadRequest, "cmd: empty or upgrade")
		return
	}
	if !accessFrom(r.Context()).Can("proxmox.console") {
		forbid(w, "proxmox.console")
		return
	}
	s, node, ok := a.pveNodeScope(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	target := nodeTarget(s, node)
	p, err := s.Cl.OpenNodeShell(ctx, node, req.Cmd)
	if err != nil {
		a.audit(r, "", "proxmox.node_shell", target, map[string]string{"error": err.Error()}, false)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	sid, msg := a.addConsole(r, &pveConsole{cl: s.Cl, node: node, typ: "node", target: target, proxy: p, term: true, perm: "proxmox.manage"})
	if msg != "" {
		writeErr(w, http.StatusTooManyRequests, msg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sid": sid, "mode": "term"})
}
