package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jacksonm36/servermgr/panel/internal/proxmox"
)

// Guest page tabs: backups, cloud-init and firewall. Like the rest of the
// guest routes they go through pveGuest, so tenants only reach their own
// pool, with their own token, and only their own storages.

// ---- backups ----

type pveBackup struct {
	VolID   string `json:"volid"`
	Storage string `json:"storage"`
	Size    int64  `json:"size"`
	CTime   int64  `json:"ctime"`
	Format  string `json:"format,omitempty"`
	Notes   string `json:"notes,omitempty"`
	Protect int    `json:"protected,omitempty"`
}

// backupStorages lists the storages on node that hold backups and that
// the scope may use.
func backupStorages(ctx context.Context, s *pveScope, node string) ([]string, error) {
	var list []struct {
		Storage string `json:"storage"`
		Active  int    `json:"active"`
		Enabled int    `json:"enabled"`
	}
	if err := s.Cl.Do(ctx, http.MethodGet, "/nodes/"+url.PathEscape(node)+"/storage", url.Values{"content": {"backup"}}, &list); err != nil {
		return nil, err
	}
	out := []string{}
	for _, st := range list {
		if st.Active == 0 || (s.TC != nil && !slices.Contains(s.TC.Storages, st.Storage)) {
			continue
		}
		out = append(out, st.Storage)
	}
	slices.Sort(out)
	return out, nil
}

// guestBackups lists this guest's backups on the given storages.
func guestBackups(ctx context.Context, s *pveScope, node string, vmid int, storages []string) ([]pveBackup, error) {
	out := []pveBackup{}
	for _, st := range storages {
		var list []pveBackup
		form := url.Values{"content": {"backup"}, "vmid": {strconv.Itoa(vmid)}}
		if err := s.Cl.Do(ctx, http.MethodGet, "/nodes/"+url.PathEscape(node)+"/storage/"+url.PathEscape(st)+"/content", form, &list); err != nil {
			return nil, err
		}
		for _, b := range list {
			if strings.HasPrefix(b.VolID, st+":") {
				b.Storage = st
				out = append(out, b)
			}
		}
	}
	slices.SortFunc(out, func(a, b pveBackup) int { return int(b.CTime - a.CTime) })
	return out, nil
}

// handlePVEBackups lists the guest's backups on every backup storage of
// its node. A storage that is inactive there or fails to list (a PBS that
// is down, say) is reported next to the list instead of hiding the rest.
func (a *API) handlePVEBackups(w http.ResponseWriter, r *http.Request) {
	s, node, _, vmid, ok := a.pveGuest(w, r)
	if !ok {
		return
	}
	var list []struct {
		Storage string `json:"storage"`
		Active  int    `json:"active"`
	}
	if err := s.Cl.Do(r.Context(), http.MethodGet, "/nodes/"+url.PathEscape(node)+"/storage", url.Values{"content": {"backup"}}, &list); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	type problem struct {
		Storage string `json:"storage"`
		Error   string `json:"error"`
	}
	storages, backups, problems := []string{}, []pveBackup{}, []problem{}
	for _, st := range list {
		if s.TC != nil && !slices.Contains(s.TC.Storages, st.Storage) {
			continue
		}
		if st.Active == 0 {
			problems = append(problems, problem{st.Storage, "not active on " + node})
			continue
		}
		storages = append(storages, st.Storage)
		b, err := guestBackups(r.Context(), s, node, vmid, []string{st.Storage})
		if err != nil {
			problems = append(problems, problem{st.Storage, err.Error()})
			continue
		}
		backups = append(backups, b...)
	}
	slices.Sort(storages)
	slices.SortFunc(backups, func(a, b pveBackup) int { return int(b.CTime - a.CTime) })
	writeJSON(w, http.StatusOK, map[string]any{"storages": storages, "backups": backups, "problems": problems})
}

func (a *API) handlePVEBackupCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Storage string `json:"storage"`
		Mode    string `json:"mode"` // snapshot | suspend | stop
		Notes   string `json:"notes"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Mode == "" {
		req.Mode = "snapshot"
	}
	if req.Mode != "snapshot" && req.Mode != "suspend" && req.Mode != "stop" {
		writeErr(w, http.StatusBadRequest, "mode: snapshot, suspend or stop")
		return
	}
	s, node, _, vmid, ok := a.pveGuest(w, r)
	if !ok {
		return
	}
	storages, err := backupStorages(r.Context(), s, node)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if !slices.Contains(storages, req.Storage) {
		writeErr(w, http.StatusBadRequest, "pick one of the backup storages offered")
		return
	}
	form := url.Values{"vmid": {strconv.Itoa(vmid)}, "storage": {req.Storage}, "mode": {req.Mode}, "compress": {"zstd"}}
	if n := strings.TrimSpace(req.Notes); n != "" {
		form.Set("notes-template", strings.NewReplacer("\n", " ", "\r", "").Replace(n[:min(len(n), 200)]))
	}
	target := fmt.Sprintf("%s/%d", s.C.Name, vmid)
	var upid string
	if err := s.Cl.Do(r.Context(), http.MethodPost, "/nodes/"+url.PathEscape(node)+"/vzdump", form, &upid); err != nil {
		a.audit(r, "", "proxmox.backup", target, map[string]string{"error": err.Error()}, false)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	a.audit(r, "", "proxmox.backup", target, map[string]string{"storage": req.Storage, "mode": req.Mode}, true)
	writeJSON(w, http.StatusAccepted, map[string]string{"upid": upid})
}

// findBackup returns volid if it is one of this guest's backups the scope
// may use.
func findBackup(ctx context.Context, s *pveScope, node string, vmid int, volid string) (*pveBackup, error) {
	storages, err := backupStorages(ctx, s, node)
	if err != nil {
		return nil, err
	}
	st, _, _ := strings.Cut(volid, ":")
	if !slices.Contains(storages, st) {
		return nil, nil
	}
	list, err := guestBackups(ctx, s, node, vmid, []string{st})
	if err != nil {
		return nil, err
	}
	for _, b := range list {
		if b.VolID == volid {
			return &b, nil
		}
	}
	return nil, nil
}

func (a *API) handlePVEBackupDelete(w http.ResponseWriter, r *http.Request) {
	volid := r.URL.Query().Get("volid")
	s, node, _, vmid, ok := a.pveGuest(w, r)
	if !ok {
		return
	}
	b, err := findBackup(r.Context(), s, node, vmid, volid)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if b == nil {
		writeErr(w, http.StatusNotFound, "no such backup of this guest")
		return
	}
	target := fmt.Sprintf("%s/%d", s.C.Name, vmid)
	var upid string
	err = s.Cl.Do(r.Context(), http.MethodDelete, "/nodes/"+url.PathEscape(node)+"/storage/"+url.PathEscape(b.Storage)+"/content/"+url.PathEscape(volid), nil, &upid)
	if err == nil && upid != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		err = s.Cl.WaitTask(ctx, node, upid)
		cancel()
	}
	if err != nil {
		a.audit(r, "", "proxmox.backup_delete", target, map[string]string{"volid": volid, "error": err.Error()}, false)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	a.audit(r, "", "proxmox.backup_delete", target, map[string]string{"volid": volid}, true)
	w.WriteHeader(http.StatusNoContent)
}

// handlePVEBackupRestore restores a backup over the guest, which must be
// stopped. Disks go back to the storages they came from.
func (a *API) handlePVEBackupRestore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		VolID   string `json:"volid"`
		Confirm string `json:"confirm"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s, node, typ, vmid, ok := a.pveGuest(w, r)
	if !ok {
		return
	}
	var st struct {
		Status string `json:"status"`
		Name   string `json:"name"`
	}
	if err := s.Cl.Do(r.Context(), http.MethodGet, proxmox.GuestPath(node, typ, vmid)+"/status/current", nil, &st); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if st.Status != "stopped" {
		writeErr(w, http.StatusConflict, "stop the guest before restoring it")
		return
	}
	if req.Confirm != strconv.Itoa(vmid) {
		writeErr(w, http.StatusBadRequest, "type the guest's ID to confirm")
		return
	}
	b, err := findBackup(r.Context(), s, node, vmid, req.VolID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if b == nil {
		writeErr(w, http.StatusNotFound, "no such backup of this guest")
		return
	}
	form := url.Values{"vmid": {strconv.Itoa(vmid)}, "force": {"1"}}
	if typ == "qemu" {
		form.Set("archive", b.VolID)
	} else {
		form.Set("ostemplate", b.VolID)
		form.Set("restore", "1")
	}
	if s.TC != nil {
		form.Set("pool", s.TC.Pool)
	}
	target := fmt.Sprintf("%s/%d", s.C.Name, vmid)
	var upid string
	if err := s.Cl.Do(r.Context(), http.MethodPost, "/nodes/"+url.PathEscape(node)+"/"+typ, form, &upid); err != nil {
		a.audit(r, "", "proxmox.restore", target, map[string]string{"volid": b.VolID, "error": err.Error()}, false)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	a.audit(r, "", "proxmox.restore", target, map[string]string{"volid": b.VolID}, true)
	writeJSON(w, http.StatusAccepted, map[string]string{"upid": upid})
}

// ---- cloud-init ----

var (
	pveCIUser   = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	pveSearch   = regexp.MustCompile(`^[A-Za-z0-9.-]+( [A-Za-z0-9.-]+){0,5}$`)
	pveIPConfig = regexp.MustCompile(`^ipconfig([0-9]|[1-2][0-9]|3[01])$`)
	pveSSHKey   = regexp.MustCompile(`^(ssh-(ed25519|rsa|dss)|ecdsa-sha2-nistp(256|384|521)|sk-(ssh-ed25519|ecdsa-sha2-nistp256)@openssh\.com) [A-Za-z0-9+/=]+( [^\r\n]{0,200})?$`)
)

// validIPConfig checks "ip=…,gw=…,ip6=…,gw6=…".
func validIPConfig(v string) bool {
	if v == "" {
		return true
	}
	seen := map[string]bool{}
	for _, part := range strings.Split(v, ",") {
		k, val, ok := strings.Cut(part, "=")
		if !ok || seen[k] {
			return false
		}
		seen[k] = true
		switch k {
		case "ip":
			if _, _, err := net.ParseCIDR(val); val != "dhcp" && (err != nil || !strings.Contains(val, ".")) {
				return false
			}
		case "ip6":
			if _, _, err := net.ParseCIDR(val); val != "dhcp" && val != "auto" && (err != nil || !strings.Contains(val, ":")) {
				return false
			}
		case "gw", "gw6":
			if net.ParseIP(val) == nil {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func (a *API) handlePVECloudInit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		User         string            `json:"ciuser"`
		Password     string            `json:"cipassword"` // empty keeps the current one
		SSHKeys      string            `json:"sshkeys"`
		Nameserver   string            `json:"nameserver"`
		Searchdomain string            `json:"searchdomain"`
		IPConfig     map[string]string `json:"ipconfig"` // ipconfig0 -> "ip=dhcp"
	}
	if !readJSON(w, r, &req) {
		return
	}
	s, node, typ, vmid, ok := a.pveGuest(w, r)
	if !ok {
		return
	}
	if typ != "qemu" {
		writeErr(w, http.StatusBadRequest, "cloud-init is for VMs")
		return
	}
	form := url.Values{}
	var del []string
	set := func(k, v string) {
		if v == "" {
			del = append(del, k)
		} else {
			form.Set(k, v)
		}
	}
	if req.User != "" && !pveCIUser.MatchString(req.User) {
		writeErr(w, http.StatusBadRequest, "user: lower-case letters, digits, - and _")
		return
	}
	set("ciuser", req.User)
	if req.Password != "" {
		if len(req.Password) < 8 || len(req.Password) > 128 {
			writeErr(w, http.StatusBadRequest, "password: 8 to 128 characters")
			return
		}
		form.Set("cipassword", req.Password)
	}
	var keys []string
	for _, k := range strings.Split(strings.ReplaceAll(req.SSHKeys, "\r", ""), "\n") {
		if k = strings.TrimSpace(k); k == "" {
			continue
		}
		if !pveSSHKey.MatchString(k) {
			writeErr(w, http.StatusBadRequest, "SSH keys: one OpenSSH public key per line")
			return
		}
		keys = append(keys, k)
	}
	if len(keys) > 20 {
		writeErr(w, http.StatusBadRequest, "SSH keys: at most 20")
		return
	}
	// Proxmox wants sshkeys URL-encoded, with %20 for spaces.
	set("sshkeys", strings.ReplaceAll(url.QueryEscape(strings.Join(keys, "\n")), "+", "%20"))
	var ns []string
	for _, n := range strings.Fields(req.Nameserver) {
		if net.ParseIP(n) == nil || len(ns) == 3 {
			writeErr(w, http.StatusBadRequest, "DNS servers: up to 3 IP addresses")
			return
		}
		ns = append(ns, n)
	}
	set("nameserver", strings.Join(ns, " "))
	if d := strings.TrimSpace(req.Searchdomain); d != "" && (len(d) > 512 || !pveSearch.MatchString(d)) {
		writeErr(w, http.StatusBadRequest, "search domain: domain names separated by spaces")
		return
	}
	set("searchdomain", strings.TrimSpace(req.Searchdomain))
	for k, v := range req.IPConfig {
		v = strings.TrimSpace(v)
		if !pveIPConfig.MatchString(k) || !validIPConfig(v) {
			writeErr(w, http.StatusBadRequest, k+": use ip=dhcp or ip=10.0.0.5/24,gw=10.0.0.1 (and ip6=…,gw6=…)")
			return
		}
		set(k, v)
	}
	if len(del) > 0 {
		form.Set("delete", strings.Join(del, ","))
	}
	if len(form) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	audit := form
	if audit.Has("cipassword") {
		audit = url.Values{}
		for k, v := range form {
			audit[k] = v
		}
		audit.Set("cipassword", "(changed)")
	}
	target := fmt.Sprintf("%s/%d", s.C.Name, vmid)
	if err := s.Cl.Do(r.Context(), http.MethodPut, proxmox.GuestPath(node, typ, vmid)+"/config", form, nil); err != nil {
		a.audit(r, "", "proxmox.cloudinit", target, map[string]string{"error": err.Error()}, false)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	a.audit(r, "", "proxmox.cloudinit", target, audit, true)
	w.WriteHeader(http.StatusNoContent)
}

// ---- firewall ----

type pveFWRule struct {
	Pos     int    `json:"pos"`
	Type    string `json:"type"`   // in | out
	Action  string `json:"action"` // ACCEPT | DROP | REJECT
	Enable  int    `json:"enable"`
	Proto   string `json:"proto,omitempty"`
	Source  string `json:"source,omitempty"`
	Dest    string `json:"dest,omitempty"`
	DPort   string `json:"dport,omitempty"`
	SPort   string `json:"sport,omitempty"`
	Iface   string `json:"iface,omitempty"`
	Comment string `json:"comment,omitempty"`
}

var (
	pveFWPort  = regexp.MustCompile(`^\d{1,5}(:\d{1,5})?(,\d{1,5}(:\d{1,5})?){0,14}$`)
	pveFWIface = regexp.MustCompile(`^net\d{1,2}$`)
	pveFWProto = []string{"tcp", "udp", "icmp", "ipv6-icmp", "gre", "esp", "ah", "sctp"}
)

// validFWAddr allows IPs and CIDRs only, comma separated: no aliases or
// ipsets, which are cluster-wide names.
func validFWAddr(v string) bool {
	if v == "" {
		return true
	}
	parts := strings.Split(v, ",")
	if len(parts) > 20 {
		return false
	}
	for _, p := range parts {
		if net.ParseIP(p) == nil {
			if _, _, err := net.ParseCIDR(p); err != nil {
				return false
			}
		}
	}
	return true
}

func (a *API) handlePVEFirewall(w http.ResponseWriter, r *http.Request) {
	s, node, typ, vmid, ok := a.pveGuest(w, r)
	if !ok {
		return
	}
	base := proxmox.GuestPath(node, typ, vmid) + "/firewall"
	var opts map[string]any
	var rules []pveFWRule
	if err := s.Cl.Do(r.Context(), http.MethodGet, base+"/options", nil, &opts); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if err := s.Cl.Do(r.Context(), http.MethodGet, base+"/rules", nil, &rules); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if rules == nil {
		rules = []pveFWRule{}
	}
	num := func(k string) int {
		f, _ := opts[k].(float64)
		return int(f)
	}
	str := func(k, def string) string {
		if v, ok := opts[k].(string); ok && v != "" {
			return v
		}
		return def
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enable":     num("enable"),
		"policy_in":  str("policy_in", "DROP"),
		"policy_out": str("policy_out", "ACCEPT"),
		"rules":      rules,
	})
}

func (a *API) handlePVEFirewallOptions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enable    bool   `json:"enable"`
		PolicyIn  string `json:"policy_in"`
		PolicyOut string `json:"policy_out"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	pol := []string{"ACCEPT", "DROP", "REJECT"}
	if !slices.Contains(pol, req.PolicyIn) || !slices.Contains(pol, req.PolicyOut) {
		writeErr(w, http.StatusBadRequest, "policies: ACCEPT, DROP or REJECT")
		return
	}
	form := url.Values{"enable": {map[bool]string{true: "1", false: "0"}[req.Enable]}, "policy_in": {req.PolicyIn}, "policy_out": {req.PolicyOut}}
	a.pveFWCall(w, r, http.MethodPut, "/options", form, "proxmox.firewall_options")
}

func (a *API) handlePVEFirewallRuleAdd(w http.ResponseWriter, r *http.Request) {
	var req pveFWRule
	if !readJSON(w, r, &req) {
		return
	}
	switch {
	case req.Type != "in" && req.Type != "out":
		writeErr(w, http.StatusBadRequest, "direction: in or out")
	case !slices.Contains([]string{"ACCEPT", "DROP", "REJECT"}, req.Action):
		writeErr(w, http.StatusBadRequest, "action: ACCEPT, DROP or REJECT")
	case req.Proto != "" && !slices.Contains(pveFWProto, req.Proto):
		writeErr(w, http.StatusBadRequest, "protocol: "+strings.Join(pveFWProto, ", "))
	case (req.DPort != "" || req.SPort != "") && req.Proto != "tcp" && req.Proto != "udp" && req.Proto != "sctp":
		writeErr(w, http.StatusBadRequest, "ports need tcp, udp or sctp")
	case req.DPort != "" && !pveFWPort.MatchString(req.DPort), req.SPort != "" && !pveFWPort.MatchString(req.SPort):
		writeErr(w, http.StatusBadRequest, "ports: numbers or ranges like 8000:8080, comma separated")
	case !validFWAddr(req.Source) || !validFWAddr(req.Dest):
		writeErr(w, http.StatusBadRequest, "source and destination: IP addresses or CIDRs, comma separated")
	case req.Iface != "" && !pveFWIface.MatchString(req.Iface):
		writeErr(w, http.StatusBadRequest, "interface: net0, net1, …")
	case len(req.Comment) > 200 || strings.ContainsAny(req.Comment, "\r\n"):
		writeErr(w, http.StatusBadRequest, "comment: one line, up to 200 characters")
	default:
		form := url.Values{"type": {req.Type}, "action": {req.Action}, "enable": {map[bool]string{true: "1", false: "0"}[req.Enable != 0]}}
		for k, v := range map[string]string{"proto": req.Proto, "source": req.Source, "dest": req.Dest, "dport": req.DPort, "sport": req.SPort, "iface": req.Iface, "comment": req.Comment} {
			if v != "" {
				form.Set(k, v)
			}
		}
		a.pveFWCall(w, r, http.MethodPost, "/rules", form, "proxmox.firewall_rule_add")
	}
}

func (a *API) handlePVEFirewallRuleDelete(w http.ResponseWriter, r *http.Request) {
	pos, err := strconv.Atoi(r.PathValue("pos"))
	if err != nil || pos < 0 || pos > 9999 {
		writeErr(w, http.StatusBadRequest, "bad rule position")
		return
	}
	a.pveFWCall(w, r, http.MethodDelete, "/rules/"+strconv.Itoa(pos), nil, "proxmox.firewall_rule_delete")
}

func (a *API) pveFWCall(w http.ResponseWriter, r *http.Request, method, sub string, form url.Values, audit string) {
	s, node, typ, vmid, ok := a.pveGuest(w, r)
	if !ok {
		return
	}
	target := fmt.Sprintf("%s/%d", s.C.Name, vmid)
	if err := s.Cl.Do(r.Context(), method, proxmox.GuestPath(node, typ, vmid)+"/firewall"+sub, form, nil); err != nil {
		a.audit(r, "", audit, target, map[string]string{"error": err.Error()}, false)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	a.audit(r, "", audit, target, form, true)
	w.WriteHeader(http.StatusNoContent)
}
