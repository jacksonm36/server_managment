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

// Cluster storage, node disks and ZFS pools. Every route here needs
// proxmox.manage, which tenants never hold; pveAdmin checks again so a
// tenant token is never used for them.

func (a *API) pveAdmin(w http.ResponseWriter, r *http.Request) (*pveScope, bool) {
	s, err := a.pveScope(r)
	if err != nil {
		scopeErr(w, err)
		return nil, false
	}
	if s.TC != nil {
		writeErr(w, http.StatusNotFound, errNoCluster.Error())
		return nil, false
	}
	return s, true
}

// pveAdminNode also resolves {node}, which must be a node of the cluster.
func (a *API) pveAdminNode(w http.ResponseWriter, r *http.Request) (*pveScope, string, bool) {
	s, ok := a.pveAdmin(w, r)
	if !ok {
		return nil, "", false
	}
	node := r.PathValue("node")
	if !pveNode.MatchString(node) {
		writeErr(w, http.StatusBadRequest, "bad node")
		return nil, "", false
	}
	res, err := s.Cl.Resources(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return nil, "", false
	}
	if !slices.ContainsFunc(res, func(x proxmox.Resource) bool { return x.Type == "node" && x.Node == node }) {
		writeErr(w, http.StatusNotFound, "node not found")
		return nil, "", false
	}
	return s, node, true
}

func nodePath(node string) string { return "/nodes/" + url.PathEscape(node) }

// pvePass answers a Proxmox GET as is.
func pvePass(w http.ResponseWriter, r *http.Request, s *pveScope, path string, q url.Values) {
	var out json.RawMessage
	if err := s.Cl.Do(r.Context(), http.MethodGet, path, q, &out); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if len(out) == 0 {
		out = json.RawMessage("[]")
	}
	writeJSON(w, http.StatusOK, out)
}

// handlePVEStorage lists the storage configuration and each online
// node's view of it (usage, active).
func (a *API) handlePVEStorage(w http.ResponseWriter, r *http.Request) {
	s, ok := a.pveAdmin(w, r)
	if !ok {
		return
	}
	var cfg []map[string]any
	if err := s.Cl.Do(r.Context(), http.MethodGet, "/storage", nil, &cfg); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	res, err := s.Cl.Resources(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	nodes := map[string][]map[string]any{}
	var errs []string
	for _, n := range res {
		if n.Type != "node" || n.Status != "online" {
			continue
		}
		var st []map[string]any
		if err := s.Cl.Do(r.Context(), http.MethodGet, nodePath(n.Node)+"/storage", nil, &st); err != nil {
			errs = append(errs, n.Node+": "+err.Error())
			continue
		}
		nodes[n.Node] = st
	}
	if cfg == nil {
		cfg = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": cfg, "nodes": nodes, "errors": errs})
}

var (
	pveContentTypes = []string{"images", "rootdir", "iso", "vztmpl", "backup", "snippets", "import"}
	pveAbsPath      = regexp.MustCompile(`^/[A-Za-z0-9_./-]{0,250}$`)
	pveNFSServer    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:-]{0,252}$`)
	pveVGName       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.+-]{0,126}$`)
	pveZPool        = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.:-]{0,62}(/[A-Za-z0-9_.:-]{1,63}){0,4}$`)
	pveVolume       = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,62}:[A-Za-z0-9_./+-]{1,250}$`)
	pveFileName     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.+-]{0,200}$`)
	pveDevPath      = regexp.MustCompile(`^/dev/[A-Za-z0-9/_.-]{1,120}$`)
	pveHexDigest    = regexp.MustCompile(`^[0-9a-fA-F]{32,128}$`)
)

type pveStorageReq struct {
	Storage  string   `json:"storage"`
	Type     string   `json:"type"`
	Content  []string `json:"content"`
	Nodes    []string `json:"nodes"`
	Disable  *bool    `json:"disable"`
	Path     string   `json:"path"`     // dir
	Server   string   `json:"server"`   // nfs
	Export   string   `json:"export"`   // nfs
	VGName   string   `json:"vgname"`   // lvmthin
	ThinPool string   `json:"thinpool"` // lvmthin
	Pool     string   `json:"pool"`     // zfspool
}

// storageForm validates the fields shared by create and update.
func storageForm(req pveStorageReq, typ string, res []proxmox.Resource) (url.Values, string) {
	form := url.Values{}
	if req.Content != nil {
		allowed := pveContentTypes
		if typ == "lvmthin" || typ == "zfspool" {
			allowed = []string{"images", "rootdir"}
		}
		for _, c := range req.Content {
			if !slices.Contains(allowed, c) {
				return nil, "content " + c + " is not allowed on " + typ
			}
		}
		if len(req.Content) == 0 {
			return nil, "pick at least one content type"
		}
		form.Set("content", strings.Join(req.Content, ","))
	}
	if req.Nodes != nil {
		for _, n := range req.Nodes {
			if !slices.ContainsFunc(res, func(x proxmox.Resource) bool { return x.Type == "node" && x.Node == n }) {
				return nil, "unknown node " + n
			}
		}
		form.Set("nodes", strings.Join(req.Nodes, ","))
	}
	if req.Disable != nil {
		form.Set("disable", b01(*req.Disable))
	}
	return form, ""
}

func (a *API) handlePVEStorageCreate(w http.ResponseWriter, r *http.Request) {
	var req pveStorageReq
	if !readJSON(w, r, &req) {
		return
	}
	s, ok := a.pveAdmin(w, r)
	if !ok {
		return
	}
	bad := func(m string) { writeErr(w, http.StatusBadRequest, m) }
	if !pveStorage.MatchString(req.Storage) {
		bad("storage ID: a letter, then letters, digits, - _ .")
		return
	}
	if req.Content == nil {
		bad("pick at least one content type")
		return
	}
	res, err := s.Cl.Resources(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	form, msg := storageForm(req, req.Type, res)
	if msg != "" {
		bad(msg)
		return
	}
	form.Set("storage", req.Storage)
	form.Set("type", req.Type)
	switch req.Type {
	case "dir":
		if !pveAbsPath.MatchString(req.Path) || strings.Contains(req.Path, "..") {
			bad("path: an absolute directory path")
			return
		}
		form.Set("path", req.Path)
	case "nfs":
		if !pveNFSServer.MatchString(req.Server) {
			bad("server: a host name or IP address")
			return
		}
		if !pveAbsPath.MatchString(req.Export) || strings.Contains(req.Export, "..") {
			bad("export: the absolute export path")
			return
		}
		form.Set("server", req.Server)
		form.Set("export", req.Export)
	case "lvmthin":
		if !pveVGName.MatchString(req.VGName) || !pveVGName.MatchString(req.ThinPool) {
			bad("volume group and thin pool names are required")
			return
		}
		form.Set("vgname", req.VGName)
		form.Set("thinpool", req.ThinPool)
	case "zfspool":
		if !pveZPool.MatchString(req.Pool) {
			bad("pool: a ZFS pool or dataset name")
			return
		}
		form.Set("pool", req.Pool)
	default:
		bad("type must be dir, nfs, lvmthin or zfspool")
		return
	}
	target := s.C.Name + "/" + req.Storage
	if err := s.Cl.Do(r.Context(), http.MethodPost, "/storage", form, nil); err != nil {
		a.audit(r, "", "proxmox.storage_create", target, map[string]string{"error": err.Error()}, false)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	a.audit(r, "", "proxmox.storage_create", target, form, true)
	writeJSON(w, http.StatusCreated, map[string]string{"storage": req.Storage})
}

// storageType looks up the type of a configured storage.
func storageType(ctx context.Context, s *pveScope, id string) (string, error) {
	var cfg []struct {
		Storage string `json:"storage"`
		Type    string `json:"type"`
	}
	if err := s.Cl.Do(ctx, http.MethodGet, "/storage", nil, &cfg); err != nil {
		return "", err
	}
	for _, c := range cfg {
		if c.Storage == id {
			return c.Type, nil
		}
	}
	return "", nil
}

func (a *API) handlePVEStorageUpdate(w http.ResponseWriter, r *http.Request) {
	var req pveStorageReq
	if !readJSON(w, r, &req) {
		return
	}
	s, ok := a.pveAdmin(w, r)
	if !ok {
		return
	}
	id := r.PathValue("storage")
	if !pveStorage.MatchString(id) {
		writeErr(w, http.StatusBadRequest, "bad storage")
		return
	}
	typ, err := storageType(r.Context(), s, id)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if typ == "" {
		writeErr(w, http.StatusNotFound, "storage not found")
		return
	}
	res, err := s.Cl.Resources(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	form, msg := storageForm(req, typ, res)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if req.Nodes != nil && len(req.Nodes) == 0 {
		form.Del("nodes")
		form.Set("delete", "nodes") // available on every node
	}
	if len(form) == 0 {
		writeErr(w, http.StatusBadRequest, "nothing to change")
		return
	}
	target := s.C.Name + "/" + id
	if err := s.Cl.Do(r.Context(), http.MethodPut, "/storage/"+url.PathEscape(id), form, nil); err != nil {
		a.audit(r, "", "proxmox.storage_update", target, map[string]string{"error": err.Error()}, false)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	a.audit(r, "", "proxmox.storage_update", target, form, true)
	w.WriteHeader(http.StatusNoContent)
}

// handlePVEStorageDelete removes a storage from the configuration; the
// data on it stays where it is.
func (a *API) handlePVEStorageDelete(w http.ResponseWriter, r *http.Request) {
	s, ok := a.pveAdmin(w, r)
	if !ok {
		return
	}
	id := r.PathValue("storage")
	if !pveStorage.MatchString(id) {
		writeErr(w, http.StatusBadRequest, "bad storage")
		return
	}
	if r.URL.Query().Get("confirm") != id {
		writeErr(w, http.StatusBadRequest, "type the storage ID to confirm")
		return
	}
	target := s.C.Name + "/" + id
	if err := s.Cl.Do(r.Context(), http.MethodDelete, "/storage/"+url.PathEscape(id), nil, nil); err != nil {
		a.audit(r, "", "proxmox.storage_delete", target, map[string]string{"error": err.Error()}, false)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	a.audit(r, "", "proxmox.storage_delete", target, nil, true)
	w.WriteHeader(http.StatusNoContent)
}

func storagePath(node, id string) string {
	return nodePath(node) + "/storage/" + url.PathEscape(id)
}

func (a *API) handlePVEStorageContent(w http.ResponseWriter, r *http.Request) {
	s, node, ok := a.pveAdminNode(w, r)
	if !ok {
		return
	}
	id := r.PathValue("storage")
	if !pveStorage.MatchString(id) {
		writeErr(w, http.StatusBadRequest, "bad storage")
		return
	}
	q := url.Values{}
	if c := r.URL.Query().Get("content"); c != "" {
		if !slices.Contains(pveContentTypes, c) {
			writeErr(w, http.StatusBadRequest, "bad content type")
			return
		}
		q.Set("content", c)
	}
	pvePass(w, r, s, storagePath(node, id)+"/content", q)
}

// handlePVEVolumeDelete deletes one volume (an ISO, a template, a backup
// or a disk image) from a storage.
func (a *API) handlePVEVolumeDelete(w http.ResponseWriter, r *http.Request) {
	s, node, ok := a.pveAdminNode(w, r)
	if !ok {
		return
	}
	id := r.PathValue("storage")
	vol := r.URL.Query().Get("volume")
	if !pveStorage.MatchString(id) || !pveVolume.MatchString(vol) || !strings.HasPrefix(vol, id+":") || strings.Contains(vol, "..") {
		writeErr(w, http.StatusBadRequest, "bad volume")
		return
	}
	if r.URL.Query().Get("confirm") != vol {
		writeErr(w, http.StatusBadRequest, "type the volume name to confirm")
		return
	}
	target := s.C.Name + "/" + node + "/" + vol
	var upid string
	err := s.Cl.Do(r.Context(), http.MethodDelete, storagePath(node, id)+"/content/"+url.PathEscape(vol), nil, &upid)
	if err == nil && upid != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		err = s.Cl.WaitTask(ctx, node, upid)
		cancel()
	}
	if err != nil {
		a.audit(r, "", "proxmox.volume_delete", target, map[string]string{"error": err.Error()}, false)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	a.audit(r, "", "proxmox.volume_delete", target, nil, true)
	w.WriteHeader(http.StatusNoContent)
}

// handlePVEDownloadURL makes the node fetch an ISO or container template
// into a storage. It answers 202 with the task's UPID.
func (a *API) handlePVEDownloadURL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL      string `json:"url"`
		Content  string `json:"content"`
		Filename string `json:"filename"`
		Checksum string `json:"checksum"`
		Algo     string `json:"checksum_algorithm"`
		// Compression has Proxmox unpack the download (ISOs only); the
		// checksum is then the compressed file's.
		Compression string `json:"compression"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s, node, ok := a.pveAdminNode(w, r)
	if !ok {
		return
	}
	bad := func(m string) { writeErr(w, http.StatusBadRequest, m) }
	id := r.PathValue("storage")
	if !pveStorage.MatchString(id) {
		bad("bad storage")
		return
	}
	u, err := url.Parse(req.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		bad("url: an http or https link")
		return
	}
	if req.Content != "iso" && req.Content != "vztmpl" {
		bad("content must be iso or vztmpl")
		return
	}
	if req.Filename == "" {
		req.Filename = u.Path[strings.LastIndex(u.Path, "/")+1:]
	}
	if !pveFileName.MatchString(req.Filename) {
		bad("file name: letters, digits, - _ . +")
		return
	}
	if req.Content == "iso" && !strings.HasSuffix(strings.ToLower(req.Filename), ".iso") && !strings.HasSuffix(strings.ToLower(req.Filename), ".img") {
		bad("an ISO file name ends in .iso or .img")
		return
	}
	form := url.Values{"url": {req.URL}, "content": {req.Content}, "filename": {req.Filename}}
	if req.Checksum != "" {
		if !slices.Contains([]string{"md5", "sha1", "sha224", "sha256", "sha384", "sha512"}, req.Algo) || !pveHexDigest.MatchString(req.Checksum) {
			bad("checksum: hex digest with md5, sha1 or sha2 algorithm")
			return
		}
		form.Set("checksum", req.Checksum)
		form.Set("checksum-algorithm", req.Algo)
	}
	if req.Compression != "" {
		if req.Content != "iso" || !slices.Contains([]string{"gz", "lzo", "zst", "bz2"}, req.Compression) {
			bad("compression: gz, lzo, zst or bz2, for ISOs only")
			return
		}
		form.Set("compression", req.Compression)
	}
	target := s.C.Name + "/" + node + "/" + id + ":" + req.Filename
	var upid string
	if err := s.Cl.Do(r.Context(), http.MethodPost, storagePath(node, id)+"/download-url", form, &upid); err != nil {
		a.audit(r, "", "proxmox.download_url", target, map[string]string{"url": req.URL, "error": err.Error()}, false)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	a.audit(r, "", "proxmox.download_url", target, map[string]string{"url": req.URL}, true)
	writeJSON(w, http.StatusAccepted, map[string]string{"upid": upid})
}

// handlePVENodeTask reports a node task's status and log tail, for tasks
// started from the storage page.
func (a *API) handlePVENodeTask(w http.ResponseWriter, r *http.Request) {
	s, node, ok := a.pveAdminNode(w, r)
	if !ok {
		return
	}
	upid := r.URL.Query().Get("upid")
	if !strings.HasPrefix(upid, "UPID:"+node+":") || len(upid) > 300 {
		writeErr(w, http.StatusBadRequest, "bad task id")
		return
	}
	st, err := s.Cl.Task(r.Context(), node, upid)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	var lines []struct {
		T string `json:"t"`
	}
	_ = s.Cl.Do(r.Context(), http.MethodGet, nodePath(node)+"/tasks/"+url.PathEscape(upid)+"/log", url.Values{"start": {"0"}, "limit": {"500"}}, &lines)
	log := make([]string, 0, len(lines))
	for _, l := range lines {
		log = append(log, l.T)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": st.Status, "exitstatus": st.ExitStatus, "log": log})
}

// ---- disks ----

type pveDiskInfo struct {
	DevPath string     `json:"devpath"`
	Used    string     `json:"used"`
	Mounted flexString `json:"mounted"`
}

func nodeDisks(ctx context.Context, s *pveScope, node string) ([]pveDiskInfo, error) {
	var disks []pveDiskInfo
	err := s.Cl.Do(ctx, http.MethodGet, nodePath(node)+"/disks/list", nil, &disks)
	return disks, err
}

func (a *API) handlePVEDisks(w http.ResponseWriter, r *http.Request) {
	s, node, ok := a.pveAdminNode(w, r)
	if !ok {
		return
	}
	pvePass(w, r, s, nodePath(node)+"/disks/list", nil)
}

func (a *API) handlePVEDiskSMART(w http.ResponseWriter, r *http.Request) {
	s, node, ok := a.pveAdminNode(w, r)
	if !ok {
		return
	}
	disk := r.URL.Query().Get("disk")
	if !pveDevPath.MatchString(disk) || strings.Contains(disk, "..") {
		writeErr(w, http.StatusBadRequest, "bad disk")
		return
	}
	var out json.RawMessage
	if err := s.Cl.Do(r.Context(), http.MethodGet, nodePath(node)+"/disks/smart", url.Values{"disk": {disk}}, &out); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// diskFree reports whether a disk exists and nothing (LVM, ZFS, a mount,
// Ceph) uses it; leftover partitions are fine.
func diskFree(disks []pveDiskInfo, dev string) (found, free bool) {
	for _, d := range disks {
		if d.DevPath == dev {
			m := string(d.Mounted)
			return true, (d.Used == "" || d.Used == "partitions") && (m == "" || m == "0" || m == "false")
		}
	}
	return false, false
}

func (a *API) handlePVEDiskWipe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Disk    string `json:"disk"`
		Confirm string `json:"confirm"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s, node, ok := a.pveAdminNode(w, r)
	if !ok {
		return
	}
	if !pveDevPath.MatchString(req.Disk) || strings.Contains(req.Disk, "..") {
		writeErr(w, http.StatusBadRequest, "bad disk")
		return
	}
	if req.Confirm != req.Disk {
		writeErr(w, http.StatusBadRequest, "type the disk path to confirm")
		return
	}
	disks, err := nodeDisks(r.Context(), s, node)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	found, free := diskFree(disks, req.Disk)
	if !found {
		writeErr(w, http.StatusNotFound, "no such disk on "+node)
		return
	}
	if !free {
		writeErr(w, http.StatusConflict, "the disk is in use; remove what uses it first")
		return
	}
	target := s.C.Name + "/" + node + ":" + req.Disk
	var upid string
	err = s.Cl.Do(r.Context(), http.MethodPut, nodePath(node)+"/disks/wipedisk", url.Values{"disk": {req.Disk}}, &upid)
	if err == nil && upid != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		err = s.Cl.WaitTask(ctx, node, upid)
		cancel()
	}
	if err != nil {
		a.audit(r, "", "proxmox.disk_wipe", target, map[string]string{"error": err.Error()}, false)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	a.audit(r, "", "proxmox.disk_wipe", target, nil, true)
	w.WriteHeader(http.StatusNoContent)
}

// ---- ZFS ----

func (a *API) handlePVEZFS(w http.ResponseWriter, r *http.Request) {
	s, node, ok := a.pveAdminNode(w, r)
	if !ok {
		return
	}
	pvePass(w, r, s, nodePath(node)+"/disks/zfs", nil)
}

var pveZFSName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.:-]{0,62}$`)

func (a *API) handlePVEZFSDetail(w http.ResponseWriter, r *http.Request) {
	s, node, ok := a.pveAdminNode(w, r)
	if !ok {
		return
	}
	name := r.PathValue("pool")
	if !pveZFSName.MatchString(name) {
		writeErr(w, http.StatusBadRequest, "bad pool name")
		return
	}
	var out json.RawMessage
	if err := s.Cl.Do(r.Context(), http.MethodGet, nodePath(node)+"/disks/zfs/"+url.PathEscape(name), nil, &out); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// zfsMinDisks is the smallest number of devices for each RAID level.
var zfsMinDisks = map[string]int{"single": 1, "mirror": 2, "raid10": 4, "raidz": 3, "raidz2": 4, "raidz3": 5}

var zfsCompression = []string{"on", "off", "lz4", "lzjb", "zle", "gzip", "zstd"}

func (a *API) handlePVEZFSCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string   `json:"name"`
		Devices     []string `json:"devices"`
		RaidLevel   string   `json:"raidlevel"`
		Ashift      int      `json:"ashift"`
		Compression string   `json:"compression"`
		AddStorage  bool     `json:"add_storage"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s, node, ok := a.pveAdminNode(w, r)
	if !ok {
		return
	}
	bad := func(m string) { writeErr(w, http.StatusBadRequest, m) }
	if !pveZFSName.MatchString(req.Name) {
		bad("name: a letter, then letters, digits, - _ . :")
		return
	}
	min, known := zfsMinDisks[req.RaidLevel]
	if !known {
		bad("RAID level must be single, mirror, raid10, raidz, raidz2 or raidz3")
		return
	}
	if len(req.Devices) < min {
		bad(fmt.Sprintf("%s needs at least %d disks", req.RaidLevel, min))
		return
	}
	if req.RaidLevel == "raid10" && len(req.Devices)%2 != 0 {
		bad("raid10 needs an even number of disks")
		return
	}
	if req.RaidLevel == "single" && len(req.Devices) != 1 {
		bad("single takes exactly one disk")
		return
	}
	if req.Ashift == 0 {
		req.Ashift = 12
	}
	if req.Ashift != 9 && req.Ashift != 12 && req.Ashift != 13 {
		bad("ashift must be 9, 12 or 13")
		return
	}
	if req.Compression == "" {
		req.Compression = "lz4"
	}
	if !slices.Contains(zfsCompression, req.Compression) {
		bad("unknown compression")
		return
	}
	disks, err := nodeDisks(r.Context(), s, node)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	seen := map[string]bool{}
	for _, d := range req.Devices {
		if seen[d] {
			bad(d + " is listed twice")
			return
		}
		seen[d] = true
		found, free := diskFree(disks, d)
		if !found {
			bad("no disk " + d + " on " + node)
			return
		}
		if !free {
			writeErr(w, http.StatusConflict, d+" is in use; wipe it first")
			return
		}
	}
	form := url.Values{
		"name":        {req.Name},
		"devices":     {strings.Join(req.Devices, ",")},
		"raidlevel":   {req.RaidLevel},
		"ashift":      {strconv.Itoa(req.Ashift)},
		"compression": {req.Compression},
		"add_storage": {b01(req.AddStorage)},
	}
	target := s.C.Name + "/" + node + ":" + req.Name
	var upid string
	if err := s.Cl.Do(r.Context(), http.MethodPost, nodePath(node)+"/disks/zfs", form, &upid); err != nil {
		a.audit(r, "", "proxmox.zfs_create", target, map[string]string{"error": err.Error()}, false)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	a.audit(r, "", "proxmox.zfs_create", target, form, true)
	writeJSON(w, http.StatusAccepted, map[string]string{"upid": upid})
}

// handlePVEZFSDestroy destroys a pool, optionally removing its storage
// entry and wiping its disks. The data is gone.
func (a *API) handlePVEZFSDestroy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Confirm       string `json:"confirm"`
		CleanupConfig bool   `json:"cleanup_config"`
		CleanupDisks  bool   `json:"cleanup_disks"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s, node, ok := a.pveAdminNode(w, r)
	if !ok {
		return
	}
	name := r.PathValue("pool")
	if !pveZFSName.MatchString(name) {
		writeErr(w, http.StatusBadRequest, "bad pool name")
		return
	}
	if req.Confirm != name {
		writeErr(w, http.StatusBadRequest, "type the pool name to confirm")
		return
	}
	form := url.Values{"cleanup-config": {b01(req.CleanupConfig)}, "cleanup-disks": {b01(req.CleanupDisks)}}
	target := s.C.Name + "/" + node + ":" + name
	var upid string
	if err := s.Cl.Do(r.Context(), http.MethodDelete, nodePath(node)+"/disks/zfs/"+url.PathEscape(name), form, &upid); err != nil {
		a.audit(r, "", "proxmox.zfs_destroy", target, map[string]string{"error": err.Error()}, false)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	a.audit(r, "", "proxmox.zfs_destroy", target, form, true)
	writeJSON(w, http.StatusAccepted, map[string]string{"upid": upid})
}
