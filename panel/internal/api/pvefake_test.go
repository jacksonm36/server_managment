package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"

	"github.com/jacksonm36/servermgr/panel/internal/proxmox"
)

// fakePVE is an in-memory Proxmox VE API with tokens, pools, users and
// ACLs, enforcing permissions roughly like Proxmox does, so tests can
// check both the panel's own checks and the ACLs it sets up.
type fakePVE struct {
	t   *testing.T
	srv *httptest.Server
	FP  string

	mu      sync.Mutex
	tokens  map[string]fakeToken // token id -> token
	users   map[string]bool
	pools   map[string]bool
	acls    []fakeACL
	guests  map[int]*fakeGuest
	nextID  int
	calls   []string // "METHOD path" of every call by a non-root token
	storage []string
	bridges []string
	groups  map[string][]string // user -> groups
	// consoles are open vncproxy/termproxy sessions by ticket.
	consoles map[string]fakeConsole
	backups  map[string]int        // volid -> vmid
	imports  map[string]url.Values // volid -> the download-url request
	firewall map[int][]map[string]string
	// storageCfg is extra storage added through POST /storage.
	storageCfg map[string]url.Values
	wiped      []string
	zfs        map[string]url.Values // pool -> create request
}

type fakeConsole struct {
	user string
	vmid int
	port int
	term bool
}

// fakeStorageInfo is each storage's type and content types.
var fakeStorageInfo = map[string][2]string{
	"local":     {"dir", "iso,vztmpl,backup,import"},
	"local-lvm": {"lvmthin", "images,rootdir,backup"}, // backup: lets the tenant test back up to its one storage
	"ceph":      {"rbd", "images"},
	"nas":       {"nfs", "backup,iso"},
}

type fakeToken struct{ user, secret string }

type fakeACL struct{ path, role, user string }

type fakeGuest struct {
	typ, node, name, pool, status string
	template                      int
	cores                         int
	memMB, diskGB                 int64
	config                        map[string]string
}

const fakeRootToken = "root@pam!servermgr"
const fakeRootSecret = "root-secret-0123456789"

func newFakePVE(t *testing.T) *fakePVE {
	f := &fakePVE{
		t:        t,
		tokens:   map[string]fakeToken{fakeRootToken: {"root@pam", fakeRootSecret}},
		users:    map[string]bool{"root@pam": true},
		pools:    map[string]bool{},
		guests:   map[int]*fakeGuest{},
		nextID:   500,
		groups:   map[string][]string{},
		consoles: map[string]fakeConsole{},
		backups:  map[string]int{},
		imports:  map[string]url.Values{},
		firewall: map[int][]map[string]string{},
		storage:  []string{"local", "local-lvm", "ceph", "nas"},
		bridges:  []string{"vmbr0", "vmbr1"},
	}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	f.FP = proxmox.Fingerprint(f.srv.Certificate().Raw)
	return f
}

func (f *fakePVE) addGuest(vmid int, g *fakeGuest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if g.config == nil {
		g.config = map[string]string{}
	}
	if g.status == "" {
		g.status = "stopped"
	}
	f.guests[vmid] = g
}

// can reports whether user holds role-like rights on path. Roles are
// simplified: PVEVMAdmin on a pool covers its guests; PVETemplateUser on
// /vms/<id> allows audit and clone; PVEDatastoreUser on /storage/<s>;
// PVESDNUser on /sdn/zones/localnetwork/<bridge>; anything on "/" covers all.
func (f *fakePVE) can(user, path string) bool {
	if user == "root@pam" {
		return true
	}
	for _, a := range f.acls {
		g, isGroup := strings.CutPrefix(a.user, "group:")
		if a.user != user && !(isGroup && slices.Contains(f.groups[user], g)) {
			continue
		}
		if a.path == "/" || a.path == path {
			return true
		}
	}
	return false
}

func (f *fakePVE) hasRole(user, path, role string) bool {
	return user == "root@pam" || slices.Contains(f.acls, fakeACL{path, role, user})
}

func (f *fakePVE) canGuest(user string, vmid int, admin bool) bool {
	g := f.guests[vmid]
	if g == nil {
		return false
	}
	if g.pool != "" && f.can(user, "/pool/"+g.pool) {
		return true
	}
	if !admin && f.can(user, "/vms/"+strconv.Itoa(vmid)) {
		return true
	}
	return user == "root@pam"
}

func (f *fakePVE) serve(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/vncwebsocket") {
		f.consoleWS(w, r)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "PVEAPIToken=")
	id, secret, _ := strings.Cut(auth, "=")
	tok, ok := f.tokens[id]
	if !ok || tok.secret != secret {
		f.reply(w, 401, nil, "invalid token")
		return
	}
	user := tok.user
	_ = r.ParseForm()
	p := strings.TrimPrefix(r.URL.Path, "/api2/json")
	if user != "root@pam" {
		f.calls = append(f.calls, r.Method+" "+p)
	}
	deny := func() { f.reply(w, 403, nil, "Permission check failed") }
	root := user == "root@pam"
	parts := strings.Split(strings.Trim(p, "/"), "/")

	switch {
	case p == "/version":
		f.reply(w, 200, map[string]any{"version": "8.4.1"}, "")
	case p == "/cluster/resources":
		out := []map[string]any{}
		for _, n := range []string{"pve1", "pve2"} {
			if root {
				out = append(out, map[string]any{"id": "node/" + n, "type": "node", "node": n, "status": "online", "maxcpu": 16, "maxmem": 64 << 30})
			}
			for _, s := range f.storage {
				if f.can(user, "/storage/"+s) {
					out = append(out, map[string]any{"id": "storage/" + n + "/" + s, "type": "storage", "storage": s, "node": n, "status": "available"})
				}
			}
		}
		for vmid, g := range f.guests {
			if !f.canGuest(user, vmid, false) {
				continue
			}
			out = append(out, map[string]any{"id": fmt.Sprintf("%s/%d", g.typ, vmid), "type": g.typ, "vmid": vmid, "node": g.node, "name": g.name,
				"status": g.status, "pool": g.pool, "template": g.template, "maxcpu": g.cores, "maxmem": g.memMB << 20, "maxdisk": g.diskGB << 30, "tags": g.config["tags"]})
		}
		f.reply(w, 200, out, "")
	case p == "/nodes" && root:
		f.reply(w, 200, []map[string]any{{"node": "pve1", "status": "online", "ssl_fingerprint": "AA:01"}, {"node": "pve2", "status": "online", "ssl_fingerprint": "AA:02"}, {"node": "pve3", "status": "offline", "ssl_fingerprint": "AA:03"}}, "")
	case p == "/cluster/nextid":
		f.nextID++
		f.reply(w, 200, strconv.Itoa(f.nextID), "")
	case p == "/pools" && r.Method == "GET":
		if !root {
			deny()
			return
		}
		out := []map[string]string{}
		for k := range f.pools {
			out = append(out, map[string]string{"poolid": k})
		}
		f.reply(w, 200, out, "")
	case p == "/pools" && r.Method == "POST":
		if !root {
			deny()
			return
		}
		f.pools[r.Form.Get("poolid")] = true
		f.reply(w, 200, nil, "")
	case p == "/access/users" && r.Method == "GET":
		if !root {
			deny()
			return
		}
		out := []map[string]string{}
		for u := range f.users {
			out = append(out, map[string]string{"userid": u})
		}
		f.reply(w, 200, out, "")
	case p == "/access/users" && r.Method == "POST":
		if !root {
			deny()
			return
		}
		f.users[r.Form.Get("userid")] = true
		f.reply(w, 200, nil, "")
	case len(parts) == 3 && parts[0] == "access" && parts[1] == "users" && r.Method == "DELETE":
		if !root {
			deny()
			return
		}
		u := parts[2]
		if !f.users[u] {
			f.reply(w, 500, nil, "user '"+u+"' does not exist")
			return
		}
		delete(f.users, u)
		for k, t := range f.tokens {
			if t.user == u {
				delete(f.tokens, k)
			}
		}
		f.acls = slices.DeleteFunc(f.acls, func(a fakeACL) bool { return a.user == u })
		f.reply(w, 200, nil, "")
	case len(parts) == 5 && parts[0] == "access" && parts[1] == "users" && parts[3] == "token":
		if !root {
			deny()
			return
		}
		u, name := parts[2], parts[4]
		full := u + "!" + name
		switch r.Method {
		case "DELETE":
			delete(f.tokens, full)
			f.reply(w, 200, nil, "")
		case "POST":
			if !f.users[u] {
				f.reply(w, 500, nil, "no such user")
				return
			}
			if _, ok := f.tokens[full]; ok {
				f.reply(w, 500, nil, "token already exists")
				return
			}
			if r.Form.Get("privsep") != "0" {
				f.t.Errorf("fake PVE: token created with privsep %q", r.Form.Get("privsep"))
			}
			sec := fmt.Sprintf("secret-%s-%d", name, len(f.tokens)+len(f.calls))
			f.tokens[full] = fakeToken{u, sec}
			f.reply(w, 200, map[string]string{"full-tokenid": full, "value": sec}, "")
		}
	case p == "/access/acl" && r.Method == "GET":
		if !root {
			deny()
			return
		}
		out := []map[string]any{}
		for _, a := range f.acls {
			typ, ugid := "user", a.user
			if g, ok := strings.CutPrefix(a.user, "group:"); ok {
				typ, ugid = "group", g
			}
			out = append(out, map[string]any{"path": a.path, "roleid": a.role, "type": typ, "ugid": ugid, "propagate": 1})
		}
		f.reply(w, 200, out, "")
	case p == "/access/acl" && r.Method == "PUT":
		if !root {
			deny()
			return
		}
		e := fakeACL{r.Form.Get("path"), r.Form.Get("roles"), r.Form.Get("users")}
		if r.Form.Get("delete") == "1" {
			f.acls = slices.DeleteFunc(f.acls, func(a fakeACL) bool { return a == e })
		} else if !slices.Contains(f.acls, e) {
			f.acls = append(f.acls, e)
		}
		f.reply(w, 200, nil, "")
	case len(parts) == 3 && parts[0] == "nodes" && parts[2] == "status" && root:
		f.reply(w, 200, map[string]any{"uptime": 1000, "cpuinfo": map[string]any{"model": "Fake CPU", "cpus": 16}, "pveversion": "pve-manager/8.4.1"}, "")
	case len(parts) == 3 && parts[0] == "nodes" && parts[2] == "rrddata" && root:
		f.reply(w, 200, []map[string]any{{"time": 1700000000, "cpu": 0.1, "memused": 1 << 30, "secret": "x"}}, "")
	case parts[0] == "storage" || (len(parts) >= 3 && parts[0] == "nodes" && parts[2] == "disks"):
		if !root {
			deny()
			return
		}
		f.storageAdmin(w, r, parts)
	case len(parts) == 3 && parts[0] == "nodes" && parts[2] == "storage" && r.Method == "GET":
		out := []map[string]any{}
		for _, st := range f.storage {
			info := fakeStorageInfo[st]
			if c := r.Form.Get("content"); c != "" && !slices.Contains(strings.Split(info[1], ","), c) {
				continue
			}
			if f.can(user, "/storage/"+st) {
				out = append(out, map[string]any{"storage": st, "active": 1, "enabled": 1, "type": info[0], "content": info[1]})
			}
		}
		f.reply(w, 200, out, "")
	case len(parts) == 5 && parts[0] == "nodes" && parts[2] == "storage" && parts[4] == "content" && r.Method == "GET":
		if !f.can(user, "/storage/"+parts[3]) {
			deny()
			return
		}
		out := []map[string]any{}
		if r.Form.Get("content") == "iso" {
			if strings.Contains(fakeStorageInfo[parts[3]][1], "iso") {
				out = append(out, map[string]any{"volid": parts[3] + ":iso/debian-13.iso"})
			}
			f.reply(w, 200, out, "")
			return
		}
		if r.Form.Get("content") == "import" {
			for volid := range f.imports {
				if strings.HasPrefix(volid, parts[3]+":") {
					out = append(out, map[string]any{"volid": volid})
				}
			}
			f.reply(w, 200, out, "")
			return
		}
		for volid, vmid := range f.backups {
			if strings.HasPrefix(volid, parts[3]+":") && strconv.Itoa(vmid) == r.Form.Get("vmid") && f.canGuest(user, vmid, true) {
				out = append(out, map[string]any{"volid": volid, "size": 1 << 30, "ctime": 1700000000, "format": "vma.zst", "vmid": vmid})
			}
		}
		f.reply(w, 200, out, "")
	case len(parts) >= 6 && parts[0] == "nodes" && parts[2] == "storage" && parts[4] == "content" && r.Method == "DELETE":
		volid := strings.Join(parts[5:], "/")
		if _, ok := f.imports[volid]; ok && root {
			delete(f.imports, volid)
			f.reply(w, 200, nil, "")
			return
		}
		vmid, ok := f.backups[volid]
		if !ok || !f.can(user, "/storage/"+parts[3]) || !f.canGuest(user, vmid, true) {
			deny()
			return
		}
		delete(f.backups, volid)
		f.reply(w, 200, nil, "")
	case len(parts) == 5 && parts[0] == "nodes" && parts[2] == "storage" && parts[4] == "download-url" && r.Method == "POST":
		if !root {
			deny()
			return
		}
		ok := map[string]string{"import": ".qcow2", "iso": ".iso", "vztmpl": ".tar.zst"}[r.Form.Get("content")]
		if ok == "" || !strings.HasSuffix(r.Form.Get("filename"), ok) {
			f.reply(w, 400, nil, "bad download")
			return
		}
		f.imports[parts[3]+":"+r.Form.Get("content")+"/"+r.Form.Get("filename")] = r.Form
		f.reply(w, 200, fmt.Sprintf("UPID:%s:0:0:0:download:0:%s:", parts[1], user), "")
	case len(parts) == 3 && parts[0] == "nodes" && parts[2] == "qemu" && r.Method == "POST" && r.Form.Get("force") == "":
		vmid, _ := strconv.Atoi(r.Form.Get("vmid"))
		_, from, _ := strings.Cut(r.Form.Get("scsi0"), "import-from=")
		from, _, _ = strings.Cut(from, ",")
		if !root || f.guests[vmid] != nil || f.imports[from] == nil {
			f.reply(w, 500, nil, "cannot create VM "+strconv.Itoa(vmid))
			return
		}
		g := &fakeGuest{typ: "qemu", node: parts[1], name: r.Form.Get("name"), status: "stopped", config: map[string]string{}}
		for k, v := range r.Form {
			g.config[k] = v[0]
		}
		g.cores, _ = strconv.Atoi(r.Form.Get("cores"))
		g.memMB, _ = strconv.ParseInt(r.Form.Get("memory"), 10, 64)
		f.guests[vmid] = g
		f.reply(w, 200, fmt.Sprintf("UPID:%s:0:0:0:qmcreate:%d:%s:", parts[1], vmid, user), "")
	case len(parts) == 3 && parts[0] == "nodes" && parts[2] == "vzdump" && r.Method == "POST":
		vmid, _ := strconv.Atoi(r.Form.Get("vmid"))
		st := r.Form.Get("storage")
		if !f.canGuest(user, vmid, true) || !f.can(user, "/storage/"+st) {
			deny()
			return
		}
		f.backups[fmt.Sprintf("%s:backup/vzdump-qemu-%d-new.vma.zst", st, vmid)] = vmid
		f.reply(w, 200, fmt.Sprintf("UPID:%s:0:0:0:vzdump:%d:%s:", parts[1], vmid, user), "")
	case len(parts) == 3 && parts[0] == "nodes" && (parts[2] == "qemu" || parts[2] == "lxc") && r.Method == "POST":
		vmid, _ := strconv.Atoi(r.Form.Get("vmid"))
		g := f.guests[vmid]
		if g == nil || r.Form.Get("force") != "1" || !f.canGuest(user, vmid, true) {
			deny()
			return
		}
		if bv, ok := f.backups[r.Form.Get("archive")]; !ok || bv != vmid {
			f.reply(w, 500, nil, "no such archive")
			return
		}
		if pool := r.Form.Get("pool"); pool != "" {
			g.pool = pool
		}
		f.reply(w, 200, fmt.Sprintf("UPID:%s:0:0:0:qmrestore:%d:%s:", parts[1], vmid, user), "")
	case len(parts) == 3 && parts[0] == "nodes" && parts[2] == "network":
		if !root {
			deny()
			return
		}
		out := []map[string]string{}
		for _, b := range f.bridges {
			out = append(out, map[string]string{"iface": b, "type": "bridge"})
		}
		f.reply(w, 200, out, "")
	case len(parts) == 3 && parts[0] == "nodes" && parts[2] == "tasks" && r.Method == "GET":
		vmid, _ := strconv.Atoi(r.Form.Get("vmid"))
		if !f.canGuest(user, vmid, true) {
			f.reply(w, 200, []any{}, "")
			return
		}
		f.reply(w, 200, []map[string]any{{"upid": fmt.Sprintf("UPID:%s:0:0:0:qmstart:%d:%s:", parts[1], vmid, user), "type": "qmstart", "user": user, "starttime": 1, "status": "OK"}}, "")
	case len(parts) == 5 && parts[0] == "nodes" && parts[2] == "tasks" && parts[4] == "log":
		f.reply(w, 200, []map[string]any{{"n": 1, "t": "starting"}, {"n": 2, "t": "TASK OK"}}, "")
	case len(parts) >= 4 && parts[0] == "nodes" && parts[2] == "tasks":
		f.reply(w, 200, map[string]string{"status": "stopped", "exitstatus": "OK"}, "")
	case len(parts) >= 4 && parts[0] == "nodes" && (parts[2] == "qemu" || parts[2] == "lxc"):
		vmid, _ := strconv.Atoi(parts[3])
		g := f.guests[vmid]
		if g == nil || g.node != parts[1] {
			f.reply(w, 500, nil, fmt.Sprintf("Configuration file 'nodes/%s/qemu-server/%d.conf' does not exist", parts[1], vmid))
			return
		}
		f.guestOp(w, r, user, vmid, g, parts[4:])
	default:
		f.reply(w, 501, nil, "fake PVE: "+r.Method+" "+p+" not implemented")
	}
}

func (f *fakePVE) guestOp(w http.ResponseWriter, r *http.Request, user string, vmid int, g *fakeGuest, rest []string) {
	deny := func() { f.reply(w, 403, nil, "Permission check failed") }
	upid := fmt.Sprintf("UPID:%s:0:0:0:op:%d:%s:", g.node, vmid, user)
	sub := strings.Join(rest, "/")
	switch {
	case sub == "clone" && r.Method == "POST":
		if !f.canGuest(user, vmid, false) {
			deny()
			return
		}
		pool := r.Form.Get("pool")
		// Proxmox: VM.Allocate and Pool.Allocate on the pool.
		if user != "root@pam" && (pool == "" || !f.hasRole(user, "/pool/"+pool, "PVEVMAdmin") || !f.hasRole(user, "/pool/"+pool, "PVEPoolAdmin")) {
			deny()
			return
		}
		if st := r.Form.Get("storage"); st != "" && !f.can(user, "/storage/"+st) {
			deny()
			return
		}
		newid, _ := strconv.Atoi(r.Form.Get("newid"))
		ng := &fakeGuest{typ: g.typ, node: g.node, name: r.Form.Get("name") + r.Form.Get("hostname"), pool: pool, status: "stopped", cores: g.cores, memMB: g.memMB, diskGB: g.diskGB, config: map[string]string{}}
		f.guests[newid] = ng
		f.reply(w, 200, upid, "")
	case !f.canGuest(user, vmid, true):
		deny()
	case strings.HasPrefix(sub, "status/") && r.Method == "POST":
		switch rest[1] {
		case "start", "resume", "reboot":
			g.status = "running"
		default:
			g.status = "stopped"
		}
		f.reply(w, 200, upid, "")
	case sub == "status/current" && r.Method == "GET":
		f.reply(w, 200, map[string]any{"status": g.status, "vmid": vmid, "name": g.name, "cpus": g.cores, "maxmem": g.memMB << 20}, "")
	case sub == "rrddata" && r.Method == "GET":
		f.reply(w, 200, []map[string]any{{"time": 60, "cpu": 0.5, "maxcpu": 2, "secret": "x"}, {"time": 120, "cpu": 0.25}}, "")
	case (sub == "agent/network-get-interfaces" || sub == "interfaces") && r.Method == "GET":
		f.reply(w, 200, map[string]any{"result": []map[string]any{{"ip-addresses": []map[string]string{{"ip-address": "127.0.0.1"}, {"ip-address": "10.0.0.5"}, {"ip-address": "fe80::1"}}}}}, "")
	case sub == "config" && r.Method == "GET":
		out := map[string]string{"name": g.name, "hostname": g.name}
		for k, v := range g.config {
			out[k] = v
		}
		f.reply(w, 200, out, "")
	case sub == "config" && (r.Method == "PUT" || r.Method == "POST"):
		for k, v := range r.Form {
			if strings.HasPrefix(k, "net") {
				_, br, _ := strings.Cut(v[0], "bridge=")
				br, _, _ = strings.Cut(br, ",")
				if !f.can(user, "/sdn/zones/localnetwork/"+br) {
					deny()
					return
				}
			}
		}
		for _, k := range strings.Split(r.Form.Get("delete"), ",") {
			delete(g.config, k)
		}
		r.Form.Del("delete")
		if c := r.Form.Get("cores"); c != "" {
			g.cores, _ = strconv.Atoi(c)
		}
		if m := r.Form.Get("memory"); m != "" {
			g.memMB, _ = strconv.ParseInt(m, 10, 64)
		}
		for k, v := range r.Form {
			g.config[k] = v[0]
		}
		f.reply(w, 200, nil, "")
	case (sub == "vncproxy" || sub == "termproxy") && r.Method == "POST":
		ticket := fmt.Sprintf("PVEVNC:%d:%d", vmid, len(f.consoles))
		port := 5900 + len(f.consoles)
		f.consoles[ticket] = fakeConsole{user, vmid, port, sub == "termproxy"}
		f.reply(w, 200, map[string]any{"port": strconv.Itoa(port), "ticket": ticket, "user": user, "upid": upid}, "")
	case sub == "firewall/options" && r.Method == "GET":
		f.reply(w, 200, map[string]any{"enable": 0, "policy_in": "DROP"}, "")
	case sub == "firewall/options" && r.Method == "PUT":
		f.reply(w, 200, nil, "")
	case sub == "firewall/rules" && r.Method == "GET":
		out := []map[string]any{}
		for i, r := range f.firewall[vmid] {
			m := map[string]any{"pos": i, "enable": 1}
			for k, v := range r {
				m[k] = v
			}
			m["enable"], _ = strconv.Atoi(r["enable"])
			out = append(out, m)
		}
		f.reply(w, 200, out, "")
	case sub == "firewall/rules" && r.Method == "POST":
		rule := map[string]string{}
		for k, v := range r.Form {
			rule[k] = v[0]
		}
		f.firewall[vmid] = append([]map[string]string{rule}, f.firewall[vmid]...)
		f.reply(w, 200, nil, "")
	case strings.HasPrefix(sub, "firewall/rules/") && r.Method == "DELETE":
		pos, _ := strconv.Atoi(rest[2])
		if pos >= len(f.firewall[vmid]) {
			f.reply(w, 500, nil, "no rule")
			return
		}
		f.firewall[vmid] = slices.Delete(f.firewall[vmid], pos, pos+1)
		f.reply(w, 200, nil, "")
	case sub == "template" && r.Method == "POST":
		g.template = 1
		f.reply(w, 200, nil, "")
	case sub == "resize":
		g.config["resized"] = r.Form.Get("disk") + " " + r.Form.Get("size")
		f.reply(w, 200, nil, "")
	case sub == "unlink" && r.Method == "PUT":
		delete(g.config, r.Form.Get("idlist"))
		f.reply(w, 200, nil, "")
	case sub == "migrate" && r.Method == "POST":
		if user != "root@pam" {
			deny()
			return
		}
		g.node = r.Form.Get("target")
		f.reply(w, 200, upid, "")
	case sub == "snapshot" && r.Method == "GET":
		f.reply(w, 200, []map[string]any{{"name": "current"}, {"name": "base", "snaptime": 1}}, "")
	case strings.HasPrefix(sub, "snapshot"):
		f.reply(w, 200, upid, "")
	case sub == "" && r.Method == "DELETE":
		delete(f.guests, vmid)
		f.reply(w, 200, upid, "")
	default:
		f.reply(w, 501, nil, "fake PVE: guest "+sub+" not implemented")
	}
}

// consoleWS is vncwebsocket: the ticket must belong to this guest, port
// and token user. A text console wants "user:ticket\n" first and answers
// "OK", then echoes input frames; a graphical one sends an RFB greeting
// and echoes everything.
func (f *fakePVE) consoleWS(w http.ResponseWriter, r *http.Request) {
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "PVEAPIToken=")
	id, secret, _ := strings.Cut(auth, "=")
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api2/json"), "/"), "/")
	vmid, _ := strconv.Atoi(parts[3])
	port, _ := strconv.Atoi(r.URL.Query().Get("port"))
	ticket := r.URL.Query().Get("vncticket")
	f.mu.Lock()
	tok, ok := f.tokens[id]
	c, found := f.consoles[ticket]
	delete(f.consoles, ticket)
	f.mu.Unlock()
	if !ok || tok.secret != secret || !found || c.user != tok.user || c.vmid != vmid || c.port != port {
		f.reply(w, 401, nil, "invalid ticket")
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"binary"}})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := r.Context()
	if c.term {
		_, login, err := conn.Read(ctx)
		if err != nil || string(login) != c.user+":"+ticket+"\n" {
			conn.Close(websocket.StatusPolicyViolation, "bad login")
			return
		}
		_ = conn.Write(ctx, websocket.MessageBinary, []byte("OK"))
	} else {
		_ = conn.Write(ctx, websocket.MessageBinary, []byte("RFB 003.008\n"))
	}
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageBinary {
			conn.Close(websocket.StatusUnsupportedData, "binary only")
			return
		}
		if c.term {
			if !strings.HasPrefix(string(data), "0:") {
				continue // resize, ping
			}
			data = []byte("echo " + string(data))
		}
		if conn.Write(ctx, websocket.MessageBinary, data) != nil {
			return
		}
	}
}

func (f *fakePVE) reply(w http.ResponseWriter, code int, data any, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	body := map[string]any{"data": data}
	if msg != "" {
		body["message"] = msg
	}
	_ = json.NewEncoder(w).Encode(body)
}

// as calls the fake directly with a token, bypassing the panel.
func (f *fakePVE) as(tokenID, secret, method, path string, form url.Values) int {
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req, _ := http.NewRequest(method, f.srv.URL+"/api2/json"+path, body)
	req.Header.Set("Authorization", "PVEAPIToken="+tokenID+"="+secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := f.srv.Client().Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func (f *fakePVE) tokenOf(user string) (string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, t := range f.tokens {
		if t.user == user {
			return id, t.secret
		}
	}
	return "", ""
}

// storageAdmin serves /storage and /nodes/{n}/disks for root.
func (f *fakePVE) storageAdmin(w http.ResponseWriter, r *http.Request, parts []string) {
	if f.storageCfg == nil {
		f.storageCfg = map[string]url.Values{}
		f.zfs = map[string]url.Values{}
	}
	upid := func(kind string) string { return fmt.Sprintf("UPID:%s:0:0:0:%s:0:root@pam:", parts[1], kind) }
	switch {
	case len(parts) == 1 && r.Method == "GET":
		out := []map[string]any{}
		for _, st := range f.storage {
			out = append(out, map[string]any{"storage": st, "type": fakeStorageInfo[st][0], "content": fakeStorageInfo[st][1]})
		}
		for id, v := range f.storageCfg {
			out = append(out, map[string]any{"storage": id, "type": v.Get("type"), "content": v.Get("content"), "nodes": v.Get("nodes")})
		}
		f.reply(w, 200, out, "")
	case len(parts) == 1 && r.Method == "POST":
		id := r.Form.Get("storage")
		if _, dup := f.storageCfg[id]; dup || slices.Contains(f.storage, id) {
			f.reply(w, 400, nil, "storage ID '"+id+"' already defined")
			return
		}
		f.storageCfg[id] = r.Form
	case len(parts) == 2 && r.Method == "PUT":
		v, ok := f.storageCfg[parts[1]]
		if !ok {
			f.reply(w, 404, nil, "no such storage")
			return
		}
		for k := range r.Form {
			v.Set(k, r.Form.Get(k))
		}
	case len(parts) == 2 && r.Method == "DELETE":
		if _, ok := f.storageCfg[parts[1]]; !ok {
			f.reply(w, 404, nil, "no such storage")
			return
		}
		delete(f.storageCfg, parts[1])
	case len(parts) == 4 && parts[3] == "list":
		f.reply(w, 200, []map[string]any{
			{"devpath": "/dev/sda", "used": "LVM", "size": 500 << 30, "health": "PASSED", "type": "ssd", "model": "Samsung SSD"},
			{"devpath": "/dev/sdb", "used": "", "size": 1 << 40, "health": "PASSED", "type": "hdd"},
			{"devpath": "/dev/sdc", "used": "partitions", "size": 1 << 40, "health": "PASSED", "type": "hdd"},
			{"devpath": "/dev/sdd", "used": "", "mounted": 1, "size": 1 << 40, "health": "PASSED", "type": "hdd"},
		}, "")
		return
	case len(parts) == 4 && parts[3] == "smart":
		f.reply(w, 200, map[string]any{"health": "PASSED", "type": "ata", "attributes": []any{}}, "")
		return
	case len(parts) == 4 && parts[3] == "wipedisk" && r.Method == "PUT":
		f.wiped = append(f.wiped, r.Form.Get("disk"))
		f.reply(w, 200, upid("wipedisk"), "")
		return
	case len(parts) == 4 && parts[3] == "zfs" && r.Method == "GET":
		out := []map[string]any{}
		for n := range f.zfs {
			out = append(out, map[string]any{"name": n, "health": "ONLINE", "size": 1 << 40, "alloc": 0, "free": 1 << 40})
		}
		f.reply(w, 200, out, "")
		return
	case len(parts) == 4 && parts[3] == "zfs" && r.Method == "POST":
		f.zfs[r.Form.Get("name")] = r.Form
		f.reply(w, 200, upid("zfscreate"), "")
		return
	case len(parts) == 5 && parts[3] == "zfs" && r.Method == "GET":
		if _, ok := f.zfs[parts[4]]; !ok {
			f.reply(w, 404, nil, "no such pool")
			return
		}
		f.reply(w, 200, map[string]any{"name": parts[4], "state": "ONLINE", "children": []any{}}, "")
		return
	case len(parts) == 5 && parts[3] == "zfs" && r.Method == "DELETE":
		delete(f.zfs, parts[4])
		f.reply(w, 200, upid("zfsremove"), "")
		return
	default:
		f.reply(w, 501, nil, "fake: not implemented")
		return
	}
	if r.Method != "GET" {
		f.reply(w, 200, nil, "")
	}
}
