#!/usr/bin/env python3
"""A small mock of the Proxmox VE API for developing and testing servermgr
without a real cluster. HTTPS with a self-signed certificate, API-token
auth, and the endpoints servermgr uses, with in-memory state.

  scripts/mock-pve.py --port 18006 --token 'root@pam!servermgr=SECRET'
      [--guest-ip 10.247.11.50]   # IP the guest agent reports for new VMs

Tasks finish after ~2 s. Consoles work too: termproxy gives a fake serial
login prompt that echoes, vncproxy a plain RFB display. Not for production use.

Tenant isolation is mocked too: pools, users, API tokens and ACLs. A
token other than --token only sees and touches guests in pools its user
has an ACL on (plus templates with an ACL on /vms/<id>), like Proxmox.
"""
import argparse, base64, hashlib, json, os, re, secrets, socket, ssl, struct, subprocess, tempfile, threading, time, urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ap = argparse.ArgumentParser()
ap.add_argument("--port", type=int, default=18006)
ap.add_argument("--token", required=True, help="USER@REALM!ID=SECRET")
ap.add_argument("--guest-ip", default="")
ap.add_argument("--certdir", default=os.path.join(tempfile.gettempdir(), "mock-pve"))
args = ap.parse_args()

lock = threading.Lock()
NODES = {"pve1": {"maxcpu": 16, "maxmem": 64 << 30}, "pve2": {"maxcpu": 8, "maxmem": 32 << 30}}
now = lambda: int(time.time())
GUESTS = {
    100: {"type": "qemu", "node": "pve1", "name": "web-01", "status": "running", "maxcpu": 2, "maxmem": 4 << 30, "maxdisk": 32 << 30, "template": 0, "started": now() - 86400, "config": {}},
    101: {"type": "qemu", "node": "pve1", "name": "db-01", "status": "stopped", "maxcpu": 4, "maxmem": 8 << 30, "maxdisk": 100 << 30, "template": 0, "started": 0, "config": {}},
    200: {"type": "lxc", "node": "pve2", "name": "dns-ct", "status": "running", "maxcpu": 1, "maxmem": 512 << 20, "maxdisk": 8 << 30, "template": 0, "started": now() - 3600, "config": {}},
    9000: {"type": "qemu", "node": "pve1", "name": "debian-13-cloud", "status": "stopped", "maxcpu": 2, "maxmem": 2 << 30, "maxdisk": 10 << 30, "template": 1, "started": 0, "config": {}},
}
STORAGES = ["local", "local-lvm"]
BRIDGES = ["vmbr0", "vmbr1"]
POOLS = {}
USERS = {"root@pam"}
TOKENS = {}  # token id -> (user, secret); the --token one is root
ACLS = []  # (path, role, user)
SNAPS = {100: [{"name": "before-upgrade", "description": "pre apt upgrade", "snaptime": now() - 7200, "vmstate": 0}]}
TASKS = {}
CONSOLES = {}  # ticket -> (user, vmid, port, term)
BACKUPS = {"local:backup/vzdump-qemu-100-2026_10_01-02_00_00.vma.zst": {"vmid": 100, "ctime": now() - 13 * 86400, "size": 3 << 30, "notes": "nightly"},
           "local:backup/vzdump-qemu-100-2026_10_08-02_00_00.vma.zst": {"vmid": 100, "ctime": now() - 6 * 86400, "size": 3 << 30, "notes": "nightly"}}
FIREWALL = {}
IMPORTS = {}  # volid -> download-url form
ISOS = {"local:iso/debian-13.1.0-amd64-netinst.iso": 754 << 20, "local:iso/ubuntu-24.04.3-live-server-amd64.iso": 3 << 30}
CTTMPL = {"local:vztmpl/debian-13-standard_13.1-2_amd64.tar.zst": 130 << 20}
SERVICES = {s: "running" for s in ("pveproxy", "pvedaemon", "pvestatd", "pve-cluster", "corosync", "chrony", "postfix", "sshd", "cron")}
NETWORK = {n: {"vmbr0": {"type": "bridge", "cidr": "10.0.1.11/24", "gateway": "10.0.1.1", "bridge_ports": "eno1", "active": 1, "autostart": 1},
               "vmbr1": {"type": "bridge", "bridge_ports": "", "active": 1, "autostart": 1},
               "eno1": {"type": "eth", "active": 1, "autostart": 1}} for n in ("pve1", "pve2")}
NODECFG = {n: {"dns": {"search": "lab.local", "dns1": "10.0.1.1"}, "time": {"timezone": "Europe/Budapest"},
               "hosts": {"data": "127.0.0.1 localhost\n10.0.1.11 pve1.lab.local pve1\n", "digest": "0" * 40}} for n in ("pve1", "pve2")}
DC = {  # collection path -> {key: entry}
    "/cluster/backup": {"backup-nightly": {"id": "backup-nightly", "schedule": "02:00", "storage": "local", "all": 1, "mode": "snapshot", "enabled": 1}},
    "/cluster/replication": {}, "/cluster/ha/resources": {}, "/cluster/ha/groups": {}, "/cluster/firewall/rules": {},
    "/cluster/firewall/groups": {}, "/cluster/firewall/aliases": {}, "/cluster/firewall/ipset": {}, "/cluster/sdn/zones": {},
    "/cluster/sdn/vnets": {}, "/access/groups": {"admins": {"groupid": "admins", "comment": "Admins"}},
    "/access/roles": {"Administrator": {"roleid": "Administrator", "special": 1, "privs": "Sys.Audit,VM.Allocate"}},
    "/access/domains": {"pam": {"realm": "pam", "type": "pam", "comment": "Linux PAM"}, "pve": {"realm": "pve", "type": "pve", "comment": "Proxmox VE"}},
    "/storage": {"local": {"storage": "local", "type": "dir", "path": "/var/lib/vz", "content": "iso,vztmpl,backup,import"},
                 "local-lvm": {"storage": "local-lvm", "type": "lvmthin", "thinpool": "data", "vgname": "pve", "content": "images,rootdir"}},
    "/cluster/metrics/server": {},
}
DCKEY = {"/cluster/backup": "id", "/cluster/replication": "id", "/cluster/ha/resources": "sid", "/cluster/ha/groups": "group",
         "/cluster/firewall/groups": "group", "/cluster/firewall/aliases": "name", "/cluster/firewall/ipset": "name",
         "/cluster/sdn/zones": "zone", "/cluster/sdn/vnets": "vnet", "/access/groups": "groupid", "/access/roles": "roleid",
         "/access/domains": "realm", "/storage": "storage", "/cluster/metrics/server": "id"}
DCITEMS = {}  # "<path>/<key>" -> list of items
OPTIONS = {"/cluster/options": {"keyboard": "en-us", "console": "xtermjs"}, "/cluster/firewall/options": {"enable": 0, "policy_in": "DROP", "policy_out": "ACCEPT"}}
STORAGE_INFO = {"local": ("dir", "iso,vztmpl,backup,import"), "local-lvm": ("lvmthin", "images,rootdir")}  # vmid -> {"options": {...}, "rules": [...]}


def task(node, kind, vmid, fn=None):
    upid = f"UPID:{node}:{os.getpid():08X}:{len(TASKS):08X}:{now():08X}:{kind}:{vmid}:root@pam!servermgr:"
    TASKS[upid] = {"status": "running", "exitstatus": None, "type": kind, "id": str(vmid), "node": node, "starttime": now()}

    def finish():
        time.sleep(2)
        with lock:
            err = None
            try:
                if fn:
                    fn()
            except Exception as e:  # task failure
                err = str(e)
            TASKS[upid].update(status="stopped", exitstatus="OK" if err is None else err)

    threading.Thread(target=finish, daemon=True).start()
    return upid


def can(user, path):
    return user == "root@pam" or any(u == user and (p == "/" or p == path) for p, _, u in ACLS)


def can_guest(user, vmid, admin=True):
    g = GUESTS.get(vmid)
    if not g:
        return False
    if g.get("pool") and can(user, "/pool/" + g["pool"]):
        return True
    return (not admin and can(user, f"/vms/{vmid}")) or user == "root@pam"


def resources(user="root@pam"):
    out = []
    for n, v in NODES.items():
        for s in STORAGES:
            if can(user, "/storage/" + s):
                out.append({"id": f"storage/{n}/{s}", "type": "storage", "storage": s, "node": n, "status": "available",
                            "maxdisk": 500 << 30, "disk": 100 << 30})
        if user != "root@pam":
            continue
        out.append({"id": f"node/{n}", "type": "node", "node": n, "status": "online", "maxcpu": v["maxcpu"], "cpu": 0.12,
                    "maxmem": v["maxmem"], "mem": v["maxmem"] // 3, "maxdisk": 500 << 30, "disk": 120 << 30, "uptime": 99999})
    for vmid, g in GUESTS.items():
        if not can_guest(user, vmid, admin=False):
            continue
        run = g["status"] == "running"
        out.append({"id": f"{g['type']}/{vmid}", "type": g["type"], "vmid": vmid, "node": g["node"], "name": g["name"], "pool": g.get("pool", ""),
                    "status": g["status"], "maxcpu": g["maxcpu"], "cpu": 0.05 if run else 0, "maxmem": g["maxmem"],
                    "mem": g["maxmem"] // 2 if run else 0, "maxdisk": g["maxdisk"], "disk": 0, "template": g["template"],
                    "uptime": now() - g["started"] if run else 0})
    return out


class H(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def send(self, code, data=None, err=None):
        body = json.dumps({"data": data} if err is None else {"data": None, "errors": err} if isinstance(err, dict) else {"data": None, "message": err}).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def handle_any(self, method):
        auth = self.headers.get("Authorization") or ""
        if auth == "PVEAPIToken=" + args.token:
            self.user = "root@pam"
        else:
            tid, _, sec = auth.removeprefix("PVEAPIToken=").partition("=")
            t = TOKENS.get(tid)
            if not t or t[1] != sec:
                return self.send(401, err="authentication failure")
            self.user = t[0]
        u = urllib.parse.urlparse(self.path)
        p = u.path
        if not p.startswith("/api2/json/"):
            return self.send(404, err="not found")
        p = p[len("/api2/json"):]
        if p.endswith("/vncwebsocket"):
            m = re.fullmatch(r"/nodes/([\w-]+)(?:/(?:qemu|lxc)/(\d+))?/vncwebsocket", p)
            q = {k: v[0] for k, v in urllib.parse.parse_qs(u.query).items()}
            with lock:
                c = CONSOLES.pop(q.get("vncticket", ""), None)
            if not m or not c or c[0] != self.user or c[1] != int(m.group(2) or 0) or str(c[2]) != q.get("port"):
                return self.send(401, err="invalid ticket")
            return ws_console(self, GUESTS[c[1]]["name"] if c[1] else m.group(1), q["vncticket"], c[0], c[3])
        n = int(self.headers.get("Content-Length") or 0)
        ctype = self.headers.get("Content-Type", "")
        if ctype.startswith("multipart/form-data"):
            body = self.rfile.read(n) if n else self.read_chunked()
            form = multipart_fields(body, ctype)
        else:
            form = {k: v[0] for k, v in urllib.parse.parse_qs(self.rfile.read(n).decode() if n else "").items()}
        q = {k: v[0] for k, v in urllib.parse.parse_qs(u.query).items()}
        with lock:
            return self.route(method, p, form, q)

    def read_chunked(self):
        out = b""
        while True:
            size = int(self.rfile.readline().strip() or b"0", 16)
            if size == 0:
                self.rfile.readline()
                return out
            out += self.rfile.read(size)
            self.rfile.readline()

    def route(self, method, p, form, q):
        user, root = self.user, self.user == "root@pam"
        deny = lambda: self.send(403, err="Permission check failed")
        if root and self.manage(method, p, form, q):
            return
        if p.startswith("/pools") or p.startswith("/access") or re.fullmatch(r"/nodes/[\w-]+/network", p):
            if not root:
                return deny()
            return self.admin(method, p, form)
        if method == "GET" and p == "/version":
            return self.send(200, {"version": "8.4.1", "release": "8.4", "repoid": "mock"})
        if method == "GET" and p == "/cluster/resources":
            t = q.get("type")
            res = resources(user)
            if t == "vm":
                res = [r for r in res if r["type"] in ("qemu", "lxc")]
            elif t:
                res = [r for r in res if r["type"] == t]
            return self.send(200, res)
        if method == "GET" and p == "/cluster/nextid":
            return self.send(200, str(max([i for i in GUESTS if i < 9000] + [99]) + 1))
        if method == "GET" and p == "/nodes" and root:
            return self.send(200, [{"node": n, "status": "online", "maxcpu": v["maxcpu"], "cpu": 0.12, "maxmem": v["maxmem"], "mem": v["maxmem"] // 3, "uptime": 99999} for n, v in NODES.items()])
        m = re.fullmatch(r"/nodes/([\w-]+)/tasks/([^/]+)/status", p)
        if m and method == "GET":
            t = TASKS.get(urllib.parse.unquote(m.group(2)))
            return self.send(200, t) if t else self.send(404, err="no such task")
        m = re.fullmatch(r"/nodes/([\w-]+)/tasks/([^/]+)/log", p)
        if m and method == "GET":
            t = TASKS.get(urllib.parse.unquote(m.group(2)))
            if not t:
                return self.send(404, err="no such task")
            lines = [f"{t['type']} {t['id']} on {t['node']}", "TASK OK" if t["status"] == "stopped" else "running…"]
            return self.send(200, [{"n": i + 1, "t": x} for i, x in enumerate(lines)])
        m = re.fullmatch(r"/nodes/([\w-]+)/storage", p)
        if m and method == "GET":
            return self.send(200, [{"storage": st, "active": 1, "enabled": 1, "type": STORAGE_INFO.get(st, ("dir", "backup,images"))[0],
                                    "content": STORAGE_INFO.get(st, ("dir", "backup,images"))[1]} for st in STORAGES if can(user, "/storage/" + st)])
        m = re.fullmatch(r"/nodes/([\w-]+)/storage/([\w-]+)/download-url", p)
        if m and method == "POST":
            if user != "root@pam":
                return self.send(403, err="Permission check failed")
            vol = f"{m.group(2)}:import/{form.get('filename', '')}"
            return self.send(200, task(m.group(1), "download", 0, lambda: IMPORTS.__setitem__(vol, form)))
        m = re.fullmatch(r"/nodes/([\w-]+)/storage/([\w-]+)/content(?:/(.+))?", p)
        if m:
            st, volid = m.group(2), urllib.parse.unquote(m.group(3) or "")
            if not can(user, "/storage/" + st):
                return self.send(403, err="Permission check failed")
            if method == "GET" and not volid and q.get("content") == "import":
                return self.send(200, [{"volid": v, "content": "import", "format": "qcow2"} for v in IMPORTS if v.startswith(st + ":")])
            if method == "DELETE" and volid in IMPORTS:
                IMPORTS.pop(volid)
                return self.send(200, None)
            if method == "GET" and not volid:
                vmid = int(q.get("vmid", 0))
                return self.send(200, [{"volid": v, "format": "vma.zst", "content": "backup", **b} for v, b in BACKUPS.items()
                                       if v.startswith(st + ":") and b["vmid"] == vmid and can_guest(user, vmid)])
            if method == "DELETE" and volid in BACKUPS and can_guest(user, BACKUPS[volid]["vmid"]):
                return self.send(200, task(m.group(1), "imgdel", BACKUPS[volid]["vmid"], lambda: BACKUPS.pop(volid, None)))
            return self.send(500, err="no such volume")
        m = re.fullmatch(r"/nodes/([\w-]+)/vzdump", p)
        if m and method == "POST":
            vmid, st = int(form.get("vmid", 0)), form.get("storage", "")
            if vmid not in GUESTS or not can_guest(user, vmid) or not can(user, "/storage/" + st):
                return self.send(403, err="Permission check failed")
            kind = "qemu" if GUESTS[vmid]["type"] == "qemu" else "lxc"
            vol = f"{st}:backup/vzdump-{kind}-{vmid}-{time.strftime('%Y_%m_%d-%H_%M_%S')}.{'vma' if kind == 'qemu' else 'tar'}.zst"
            return self.send(200, task(m.group(1), "vzdump", vmid, lambda: BACKUPS.__setitem__(vol, {"vmid": vmid, "ctime": now(), "size": 2 << 30, "notes": form.get("notes-template", "")})))
        m = re.fullmatch(r"/nodes/([\w-]+)/qemu", p)
        if m and method == "POST" and not form.get("force"):
            vmid = int(form.get("vmid", 0))
            src = form.get("scsi0", "").partition("import-from=")[2].split(",")[0]
            if user != "root@pam" or vmid in GUESTS or src not in IMPORTS:
                return self.send(500, err=f"unable to create VM {vmid}")
            cfg = {k: v for k, v in form.items() if k not in ("vmid", "name", "cores", "memory")}

            def create():
                GUESTS[vmid] = {"type": "qemu", "node": m.group(1), "name": form.get("name", f"vm-{vmid}"), "status": "stopped", "maxcpu": int(form.get("cores", 1)),
                                "maxmem": int(form.get("memory", 512)) << 20, "maxdisk": 3 << 30, "template": 0, "started": 0, "config": cfg}
            return self.send(200, task(m.group(1), "qmcreate", vmid, create))
        m = re.fullmatch(r"/nodes/([\w-]+)/(qemu|lxc)", p)
        if m and method == "POST" and form.get("force") == "1":
            vmid = int(form.get("vmid", 0))
            vol = form.get("archive") or form.get("ostemplate")
            if vmid not in GUESTS or not can_guest(user, vmid) or BACKUPS.get(vol, {}).get("vmid") != vmid:
                return self.send(403, err="Permission check failed")
            return self.send(200, task(m.group(1), "qmrestore", vmid))
        m = re.fullmatch(r"/nodes/([\w-]+)/tasks", p)
        if m and method == "GET":
            vmid = int(q.get("vmid", 0))
            if not can_guest(user, vmid):
                return self.send(200, [])
            out = [{"upid": u, "type": t["type"], "user": "root@pam!servermgr", "starttime": t["starttime"], "node": t["node"],
                    **({"endtime": t["starttime"] + 2, "status": t["exitstatus"]} if t["status"] == "stopped" else {})}
                   for u, t in TASKS.items() if t["node"] == m.group(1) and t["id"] == str(vmid)]
            return self.send(200, sorted(out, key=lambda t: -t["starttime"]))
        m = re.fullmatch(r"/nodes/([\w-]+)/(qemu|lxc)/(\d+)(/.*)?", p)
        if not m:
            return self.send(501, err=f"mock: {method} {p} not implemented")
        node, typ, vmid, rest = m.group(1), m.group(2), int(m.group(3)), m.group(4) or ""
        g = GUESTS.get(vmid)
        if not g or g["node"] != node or g["type"] != typ:
            return self.send(500, err=f"Configuration file 'nodes/{node}/{typ}-server/{vmid}.conf' does not exist")
        if rest == "/clone":
            if not can_guest(user, vmid, admin=False) or (not root and not can(user, "/pool/" + form.get("pool", ""))):
                return deny()
            if form.get("storage") and not can(user, "/storage/" + form["storage"]):
                return deny()
        elif not can_guest(user, vmid):
            return deny()
        if "net0" in form and not can(user, "/sdn/zones/localnetwork/" + form["net0"].partition("bridge=")[2].split(",")[0]):
            return deny()

        sm = re.fullmatch(r"/status/(start|stop|shutdown|reboot|suspend|resume)", rest)
        if sm and method == "POST":
            act = sm.group(1)
            if g["template"]:
                return self.send(500, err="you can't start a template")

            def apply():
                if act == "start" or act == "resume":
                    g["status"], g["started"] = "running", now()
                elif act in ("stop", "shutdown"):
                    g["status"] = "stopped"
                elif act == "suspend":
                    g["status"] = "paused"
            return self.send(200, task(node, f"qm{act}", vmid, apply))
        if rest == "/status/current" and method == "GET":
            r = next((x for x in resources() if x.get("vmid") == vmid), {})
            up = g["status"] == "running"
            return self.send(200, {"status": "running" if g["status"] == "paused" else g["status"], "vmid": vmid, "name": g["name"],
                                   "qmpstatus": g["status"], "cpus": g["maxcpu"], "cpu": r.get("cpu", 0), "mem": r.get("mem", 0),
                                   "maxmem": g["maxmem"], "maxdisk": g["maxdisk"], "disk": g["maxdisk"] // 3 if typ == "lxc" else 0,
                                   "uptime": now() - g["started"] if up else 0, "netin": 123456789 if up else 0, "netout": 23456789 if up else 0})
        if rest == "/rrddata" and method == "GET":
            step = {"hour": 60, "day": 1800, "week": 10800, "month": 43200, "year": 604800}.get(q.get("timeframe"), 60)
            t1, out = now() // step * step, []
            import math
            for i in range(70):
                t = t1 - (69 - i) * step
                w = (math.sin(t / (step * 7) + vmid) + 1) / 2
                out.append({"time": t, "cpu": 0.05 + 0.4 * w, "maxcpu": g["maxcpu"], "mem": g["maxmem"] * (0.3 + 0.3 * w), "maxmem": g["maxmem"],
                            "netin": 20000 + 400000 * w, "netout": 8000 + 90000 * (1 - w), "diskread": 50000 * w, "diskwrite": 120000 * (1 - w)})
            out[-1] = {"time": t1}  # Proxmox leaves the unfinished interval empty
            return self.send(200, out)
        if rest == "/interfaces" and method == "GET" and typ == "lxc":
            return self.send(200, [{"name": "lo", "inet": "127.0.0.1/8"}, {"name": "eth0", "inet": "10.247.11.20/24", "inet6": "fd00::20/64"}])
        if rest == "/config" and method == "GET":
            hw = {"sockets": 1, "cpu": "x86-64-v2-AES", "scsihw": "virtio-scsi-single", "scsi0": f"local-lvm:vm-{vmid}-disk-0,iothread=1,size={g['maxdisk'] >> 30}G",
                  "net0": "virtio=BC:24:11:00:00:01,bridge=vmbr0", "ostype": "l26", "boot": "order=scsi0", "onboot": 1, "agent": "1",
                  "ciuser": "debian", "cipassword": "$5$mock$hash", "description": "Mock guest for development."} if typ == "qemu" else {
                  "hostname": g["name"], "rootfs": f"local-lvm:vm-{vmid}-disk-0,size={g['maxdisk'] >> 30}G", "arch": "amd64",
                  "net0": "name=eth0,bridge=vmbr0,ip=dhcp", "ostype": "debian", "unprivileged": 1, "swap": 512}
            return self.send(200, {"name": g["name"], "cores": g["maxcpu"], "memory": g["maxmem"] >> 20, **hw, **g["config"]})
        if rest == "/config" and method in ("POST", "PUT"):
            g["config"].update(form)
            if "cores" in form:
                g["maxcpu"] = int(form["cores"])
            if "memory" in form:
                g["maxmem"] = int(form["memory"]) << 20
            return self.send(200, None)
        if rest == "/snapshot" and method == "GET":
            snaps = SNAPS.get(vmid, [])
            cur = {"name": "current", "description": "You are here!", "running": int(g["status"] == "running")}
            if snaps:
                cur["parent"] = snaps[-1]["name"]
            return self.send(200, snaps + [cur])
        if rest == "/snapshot" and method == "POST":
            name = form.get("snapname", "")
            if not re.fullmatch(r"[A-Za-z][\w-]{1,39}", name):
                return self.send(400, err={"snapname": "invalid format"})
            if any(s["name"] == name for s in SNAPS.get(vmid, [])):
                return self.send(500, err=f"snapshot name '{name}' already used")
            return self.send(200, task(node, "qmsnapshot", vmid, lambda: SNAPS.setdefault(vmid, []).append(
                {"name": name, "description": form.get("description", ""), "snaptime": now(), "vmstate": int(form.get("vmstate", 0))})))
        sm = re.fullmatch(r"/snapshot/([\w-]+)(/rollback)?", rest)
        if sm:
            name = sm.group(1)
            if not any(s["name"] == name for s in SNAPS.get(vmid, [])):
                return self.send(500, err=f"snapshot '{name}' does not exist")
            if sm.group(2) and method == "POST":
                return self.send(200, task(node, "qmrollback", vmid))
            if not sm.group(2) and method == "DELETE":
                return self.send(200, task(node, "qmdelsnapshot", vmid, lambda: SNAPS.__setitem__(vmid, [s for s in SNAPS[vmid] if s["name"] != name])))
        if rest == "/clone" and method == "POST" and typ == "qemu":
            newid = int(form.get("newid", 0))
            if newid in GUESTS or newid < 100:
                return self.send(500, err=f"VM {newid} already exists")
            target = form.get("target") or node

            def clone():
                GUESTS[newid] = {**{k: v for k, v in g.items() if k != "config"}, "node": target, "name": form.get("name", f"vm-{newid}"),
                                 "template": 0, "status": "stopped", "started": 0, "config": {"agent": "1"}, "pool": form.get("pool", "")}
            return self.send(200, task(node, "qmclone", vmid, clone))
        if rest in ("/vncproxy", "/termproxy") and method == "POST":
            if g["status"] != "running":
                return self.send(500, err="VM is not running")
            port, ticket = 5900 + len(CONSOLES), "PVEVNC:" + secrets.token_hex(12)
            CONSOLES[ticket] = (user, vmid, port, rest == "/termproxy")
            return self.send(200, {"port": str(port), "ticket": ticket, "user": user, "upid": task(node, "vncproxy", vmid)})
        if rest.startswith("/firewall/"):
            fw = FIREWALL.setdefault(vmid, {"options": {"enable": 0, "policy_in": "DROP", "policy_out": "ACCEPT"}, "rules": []})
            if rest == "/firewall/options":
                if method == "PUT":
                    fw["options"].update({k: (int(v) if k == "enable" else v) for k, v in form.items()})
                    return self.send(200, None)
                return self.send(200, fw["options"])
            if rest == "/firewall/rules":
                if method == "POST":
                    fw["rules"].insert(0, {k: (int(v) if k == "enable" else v) for k, v in form.items()})
                    return self.send(200, None)
                return self.send(200, [{"pos": i, **r} for i, r in enumerate(fw["rules"])])
            fm = re.fullmatch(r"/firewall/rules/(\d+)", rest)
            if fm and method == "DELETE" and int(fm.group(1)) < len(fw["rules"]):
                fw["rules"].pop(int(fm.group(1)))
                return self.send(200, None)
        if rest == "/template" and method == "POST":
            return self.send(200, task(node, "qmtemplate", vmid, lambda: g.__setitem__("template", 1)))
        if rest == "/resize" and method == "PUT":
            return self.send(200, None)
        if rest == "/agent/network-get-interfaces" and method == "GET":
            if g["status"] != "running" or not args.guest_ip or g["config"].get("agent") != "1":
                return self.send(500, err="QEMU guest agent is not running")
            return self.send(200, {"result": [
                {"name": "lo", "ip-addresses": [{"ip-address-type": "ipv4", "ip-address": "127.0.0.1", "prefix": 8}]},
                {"name": "eth0", "ip-addresses": [{"ip-address-type": "ipv4", "ip-address": args.guest_ip, "prefix": 24}]}]})
        if rest == "" and method == "DELETE":
            if g["status"] != "stopped":
                return self.send(500, err="VM is running - destroy failed")
            return self.send(200, task(node, "qmdestroy", vmid, lambda: GUESTS.pop(vmid, None)))
        return self.send(501, err=f"mock: {method} {rest} not implemented")

    def manage(self, method, p, form, q):
        """Nodes, storage and datacenter (root token only). True if handled."""
        m = re.fullmatch(r"/nodes/([\w-]+)(/.*)?", p)
        if m and m.group(1) in NODES:
            node, rest = m.group(1), m.group(2) or ""
            if rest == "/status" and method == "GET":
                v = NODES[node]
                return self.send(200, {"cpu": 0.07, "uptime": 812345, "loadavg": ["0.31", "0.42", "0.40"], "pveversion": "pve-manager/8.4.1/mock",
                                       "kversion": "Linux 6.8.12-9-pve", "cpuinfo": {"cpus": v["maxcpu"], "model": "Mock CPU @ 3.0GHz", "sockets": 1},
                                       "memory": {"total": v["maxmem"], "used": v["maxmem"] // 3, "free": v["maxmem"] * 2 // 3},
                                       "rootfs": {"total": 100 << 30, "used": 22 << 30, "avail": 78 << 30}, "swap": {"total": 8 << 30, "used": 0},
                                       "current-kernel": {"release": "6.8.12-9-pve"}, "boot-info": {"mode": "efi"}}) or True
            if rest == "/status" and method == "POST":
                return self.send(200, None) or True
            if rest == "/rrddata" and method == "GET":
                import math
                t1 = now() // 60 * 60
                return self.send(200, [{"time": t1 - (59 - i) * 60, "cpu": 0.05 + 0.2 * (math.sin(i / 5) + 1) / 2, "memused": NODES[node]["maxmem"] / 3,
                                        "memtotal": NODES[node]["maxmem"], "netin": 30000 + 2000 * i, "netout": 9000 + 500 * i, "iowait": 0.01} for i in range(60)]) or True
            if rest == "/services" and method == "GET":
                return self.send(200, [{"service": k, "name": k, "desc": f"{k} daemon", "state": v, "unit-state": "enabled"} for k, v in sorted(SERVICES.items())]) or True
            sm = re.fullmatch(r"/services/([\w@.-]+)/(start|stop|restart|reload)", rest)
            if sm and method == "POST":
                SERVICES[sm.group(1)] = "dead" if sm.group(2) == "stop" else "running"
                return self.send(200, task(node, "srv" + sm.group(2), 0)) or True
            if rest == "/apt/update" and method == "GET":
                return self.send(200, [{"Package": "pve-manager", "OldVersion": "8.4.1", "Version": "8.4.2", "Origin": "Proxmox", "Description": "Proxmox VE manager"},
                                       {"Package": "openssl", "OldVersion": "3.0.15-1", "Version": "3.0.16-1", "Origin": "Debian", "Description": "SSL toolkit"}]) or True
            if rest == "/apt/update" and method == "POST":
                return self.send(200, task(node, "aptupdate", 0)) or True
            if rest == "/apt/repositories" and method == "GET":
                return self.send(200, {"infos": [{"kind": "warning", "message": "The enterprise repository is enabled, but there is no active subscription!"}], "files": []}) or True
            if rest == "/disks/list" and method == "GET":
                return self.send(200, [{"devpath": "/dev/sda", "type": "ssd", "size": 512 << 30, "model": "Mock SSD", "serial": "MOCK1", "health": "PASSED", "used": "LVM", "wearout": 98},
                                       {"devpath": "/dev/sdb", "type": "hdd", "size": 4 << 40, "model": "Mock HDD", "serial": "MOCK2", "health": "PASSED", "wearout": "N/A"}]) or True
            if rest == "/disks/smart" and method == "GET":
                return self.send(200, {"health": "PASSED", "type": "ata", "attributes": [{"id": " 5", "name": "Reallocated_Sector_Ct", "value": 100, "worst": 100, "threshold": 10, "raw": "0"}]}) or True
            if rest in ("/disks/zfs", "/disks/lvmthin", "/disks/directory") and method == "GET":
                return self.send(200, [{"lv": "data", "vg": "pve", "lv_size": 300 << 30, "used": 40 << 30}] if rest == "/disks/lvmthin" else []) or True
            if rest == "/disks/lvm" and method == "GET":
                return self.send(200, {"children": [{"name": "pve", "size": 476 << 30, "free": 16 << 30}]}) or True
            if rest.startswith("/disks/") and method in ("POST", "PUT"):
                return self.send(200, task(node, rest.split("/")[-1], 0)) or True
            if rest in ("/dns", "/time", "/hosts"):
                k = rest[1:]
                if method == "GET":
                    return self.send(200, {**NODECFG[node][k], **({"localtime": now()} if k == "time" else {})}) or True
                NODECFG[node][k].update({a: b for a, b in form.items() if a != "digest"})
                return self.send(200, None) or True
            if rest == "/syslog" and method == "GET":
                return self.send(200, [{"n": i + 1, "t": f"Oct 15 12:{i % 60:02d}:00 {node} {q.get('service') or 'pvedaemon'}[123]: mock line {i + 1}"} for i in range(int(q.get("limit", 50)))]) or True
            if rest == "/tasks" and method == "GET" and not q.get("vmid"):
                out = [{"upid": u, "type": t["type"], "id": t["id"], "node": t["node"], "user": "root@pam!servermgr", "starttime": t["starttime"],
                        **({"endtime": t["starttime"] + 2, "status": t["exitstatus"]} if t["status"] == "stopped" else {})} for u, t in TASKS.items() if t["node"] == node]
                return self.send(200, sorted(out, key=lambda t: -t["starttime"])[:100]) or True
            if re.fullmatch(r"/tasks/[^/]+", rest) and method == "DELETE":
                return self.send(200, None) or True
            if rest in ("/startall", "/stopall", "/migrateall") and method == "POST":
                return self.send(200, task(node, rest[1:], 0)) or True
            if rest == "/termproxy" and method == "POST":
                port, ticket = 5900 + len(CONSOLES), "PVEVNC:" + secrets.token_hex(12)
                CONSOLES[ticket] = (self.user, 0, port, True)
                return self.send(200, {"port": str(port), "ticket": ticket, "user": self.user}) or True
            if rest == "/network":
                if method == "GET":
                    return self.send(200, [{"iface": k, **v} for k, v in NETWORK[node].items() if not q.get("type") or v["type"] == "bridge"]) or True
                if method == "POST":
                    NETWORK[node][form["iface"]] = {**{k: v for k, v in form.items() if k != "iface"}, "active": 0, "autostart": int(form.get("autostart", 0))}
                    return self.send(200, None) or True
                return self.send(200, task(node, "srvreload", 0) if method == "PUT" else None) or True
            nm = re.fullmatch(r"/network/([\w.-]+)", rest)
            if nm:
                if method == "PUT":
                    NETWORK[node].setdefault(nm.group(1), {}).update(form)
                elif method == "DELETE":
                    NETWORK[node].pop(nm.group(1), None)
                return self.send(200, None) or True
            if rest == "/storage" and method == "GET" and q.get("content") is None:
                return self.send(200, [{"storage": st, "active": 1, "enabled": 1, "type": STORAGE_INFO[st][0], "content": STORAGE_INFO[st][1], "shared": 0,
                                        "total": 100 << 30, "used": 30 << 30, "avail": 70 << 30} for st in STORAGES]) or True
            cm = re.fullmatch(r"/storage/([\w-]+)/(content|upload|status)", rest)
            if cm and cm.group(2) == "content" and method == "GET" and not q.get("vmid") and q.get("content") != "import":
                st, c = cm.group(1), q.get("content", "")
                vols = []
                if c in ("", "iso"):
                    vols += [{"volid": v, "content": "iso", "format": "iso", "size": sz, "ctime": now() - 86400} for v, sz in ISOS.items() if v.startswith(st + ":")]
                if c in ("", "vztmpl"):
                    vols += [{"volid": v, "content": "vztmpl", "format": "tzst", "size": sz} for v, sz in CTTMPL.items() if v.startswith(st + ":")]
                if c in ("", "backup"):
                    vols += [{"volid": v, "content": "backup", "format": "vma.zst", **b} for v, b in BACKUPS.items() if v.startswith(st + ":")]
                if c in ("", "images", "rootdir") and st == "local-lvm":
                    vols += [{"volid": f"local-lvm:vm-{i}-disk-0", "content": "images", "format": "raw", "size": g["maxdisk"], "vmid": i} for i, g in GUESTS.items()]
                return self.send(200, vols) or True
            if cm and cm.group(2) == "upload" and method == "POST":
                name = form.get("filename", "upload.iso")
                (ISOS if form.get("content") == "iso" else CTTMPL)[f"{cm.group(1)}:{'iso' if form.get('content') == 'iso' else 'vztmpl'}/{name}"] = form.get("_size", 0)
                return self.send(200, task(node, "imgcopy", 0)) or True
            vm = re.fullmatch(r"/storage/([\w-]+)/content/(.+)", rest)
            if vm and method == "DELETE":
                v = urllib.parse.unquote(vm.group(2))
                if ISOS.pop(v, None) is not None or CTTMPL.pop(v, None) is not None:
                    return self.send(200, task(node, "imgdel", 0)) or True
            if rest in ("/qemu", "/lxc") and method == "POST" and not form.get("force") and "import-from" not in form.get("scsi0", ""):
                vmid = int(form.get("vmid", 0))
                if vmid in GUESTS:
                    return self.send(500, err=f"VM {vmid} already exists") or True
                cfg = {k: v for k, v in form.items() if k not in ("vmid", "name", "hostname", "cores", "memory", "password", "start", "ostemplate")}

                def create():
                    GUESTS[vmid] = {"type": rest[1:], "node": node, "name": form.get("name") or form.get("hostname") or f"vm-{vmid}", "status": "running" if form.get("start") == "1" else "stopped",
                                    "maxcpu": int(form.get("cores", 1)), "maxmem": int(form.get("memory", 512)) << 20, "maxdisk": 32 << 30, "template": 0,
                                    "started": now(), "config": cfg, "pool": form.get("pool", "")}
                return self.send(200, task(node, "qmcreate" if rest == "/qemu" else "vzcreate", vmid, create)) or True
            gm = re.fullmatch(r"/(qemu|lxc)/(\d+)/(migrate|move_disk|move_volume|clone)", rest)
            if gm and method == "POST" and int(gm.group(2)) in GUESTS:
                vmid, g = int(gm.group(2)), GUESTS[int(gm.group(2))]
                if gm.group(3) == "migrate":
                    return self.send(200, task(node, "qmigrate", vmid, lambda: g.__setitem__("node", form["target"]))) or True
                if gm.group(3) == "clone" and gm.group(1) == "lxc":
                    newid = int(form.get("newid", 0))
                    return self.send(200, task(node, "vzclone", vmid, lambda: GUESTS.__setitem__(newid, {**g, "name": form.get("hostname", f"ct-{newid}"), "config": dict(g["config"])}))) or True
                if gm.group(3) != "clone":
                    return self.send(200, task(node, gm.group(3), vmid)) or True
            return False
        for path, coll in DC.items():
            key = DCKEY.get(path, "pos")
            if p == path:
                if method == "GET":
                    rows = list(coll.values())
                    if path == "/cluster/firewall/rules":
                        rows = [{"pos": i, **r} for i, r in enumerate(DCITEMS.get(path, []))]
                    return self.send(200, rows) or True
                if method == "POST":
                    if path == "/cluster/firewall/rules":
                        DCITEMS.setdefault(path, []).insert(0, dict(form))
                        return self.send(200, None) or True
                    k = form.get(key) or f"job-{secrets.token_hex(3)}"
                    coll[k] = {**form, key: k}
                    return self.send(200, None) or True
            em = re.fullmatch(re.escape(path) + r"/([^/]+)(?:/subnets)?(?:/([^/]+))?", p)
            if em:
                k, item = urllib.parse.unquote(em.group(1)), urllib.parse.unquote(em.group(2) or "")
                if path == "/cluster/firewall/rules":
                    rules = DCITEMS.setdefault(path, [])
                    if method == "DELETE" and int(k) < len(rules):
                        rules.pop(int(k))
                    elif method == "PUT" and int(k) < len(rules):
                        rules[int(k)].update(form)
                    return self.send(200, None) or True
                items = DCITEMS.setdefault(f"{path}/{k}", [])
                sub = p.endswith("/subnets") or path in ("/cluster/firewall/groups", "/cluster/firewall/ipset")
                if item and method == "DELETE":
                    items[:] = [x for x in items if str(x.get("cidr") or x.get("subnet") or x.get("pos")) != item]
                    return self.send(200, None) or True
                if method == "GET":
                    return self.send(200, [{"pos": i, **x} for i, x in enumerate(items)] if sub and not item else coll.get(k, {})) or True
                if method == "POST" and sub:
                    items.append(dict(form))
                    return self.send(200, None) or True
                if method == "PUT":
                    coll.setdefault(k, {key: k}).update(form)
                    return self.send(200, None) or True
                if method == "DELETE":
                    coll.pop(k, None)
                    return self.send(200, None) or True
        if p in OPTIONS:
            if method == "PUT":
                OPTIONS[p].update({k: v for k, v in form.items() if k != "digest"})
                return self.send(200, None) or True
            return self.send(200, OPTIONS[p]) or True
        if p == "/cluster/status" and method == "GET":
            return self.send(200, [{"type": "cluster", "name": "mock", "quorate": 1, "nodes": len(NODES), "version": 3}] +
                             [{"type": "node", "name": n, "online": 1, "ip": f"10.0.1.{11 + i}"} for i, n in enumerate(NODES)]) or True
        if p == "/cluster/tasks" and method == "GET":
            return self.send(200, [{"upid": u, "type": t["type"], "id": t["id"], "node": t["node"], "user": "root@pam!servermgr", "starttime": t["starttime"],
                                    "status": t.get("exitstatus", "")} for u, t in TASKS.items()]) or True
        if p == "/cluster/log" and method == "GET":
            return self.send(200, [{"uid": 1, "time": now() - 60, "node": "pve1", "user": "root@pam", "tag": "pvedaemon", "msg": "mock log entry"}]) or True
        if p == "/cluster/ha/status/current" and method == "GET":
            return self.send(200, [{"id": "quorum", "type": "quorum", "status": "OK"}]) or True
        if p == "/cluster/sdn" and method == "PUT":
            return self.send(200, task("pve1", "reloadnetworkall", 0)) or True
        return False

    def admin(self, method, p, form):
        """Pools, users, tokens, ACLs and bridges (root token only)."""
        if p == "/pools" and method == "GET":
            return self.send(200, [{"poolid": k, "comment": v} for k, v in POOLS.items()])
        if p == "/pools" and method == "POST":
            POOLS[form["poolid"]] = form.get("comment", "")
            return self.send(200, None)
        if p == "/access/users" and method == "GET":
            return self.send(200, [{"userid": u} for u in sorted(USERS)])
        if p == "/access/users" and method == "POST":
            USERS.add(form["userid"])
            return self.send(200, None)
        m = re.fullmatch(r"/access/users/([^/]+)", p)
        if m and method == "DELETE":
            u = urllib.parse.unquote(m.group(1))
            if u not in USERS:
                return self.send(500, err=f"user '{u}' does not exist")
            USERS.discard(u)
            for k in [k for k, t in TOKENS.items() if t[0] == u]:
                del TOKENS[k]
            ACLS[:] = [a for a in ACLS if a[2] != u]
            return self.send(200, None)
        m = re.fullmatch(r"/access/users/([^/]+)/token/([\w-]+)", p)
        if m:
            u, name = urllib.parse.unquote(m.group(1)), m.group(2)
            full = f"{u}!{name}"
            if method == "DELETE":
                TOKENS.pop(full, None)
                return self.send(200, None)
            if u not in USERS or full in TOKENS:
                return self.send(500, err="cannot create token")
            sec = os.urandom(16).hex()
            TOKENS[full] = (u, sec)
            return self.send(200, {"full-tokenid": full, "value": sec})
        if p == "/access/acl" and method == "GET":
            return self.send(200, [{"path": a[0], "roleid": a[1], "type": "user", "ugid": a[2], "propagate": 1} for a in ACLS])
        if p == "/access/acl" and method == "PUT":
            e = (form["path"], form["roles"], form["users"])
            if form.get("delete") == "1":
                ACLS[:] = [a for a in ACLS if a != e]
            elif e not in ACLS:
                ACLS.append(e)
            return self.send(200, None)
        if re.fullmatch(r"/nodes/[\w-]+/network", p):
            return self.send(200, [{"iface": b, "type": "bridge"} for b in BRIDGES])
        return self.send(501, err=f"mock: {method} {p} not implemented")

    def do_GET(self):
        self.handle_any("GET")

    def do_POST(self):
        self.handle_any("POST")

    def do_PUT(self):
        self.handle_any("PUT")

    def do_DELETE(self):
        self.handle_any("DELETE")


def multipart_fields(body, ctype):
    """Text fields of a multipart body; the file part yields its name and size."""
    boundary = ctype.split("boundary=", 1)[1].strip('"').encode()
    out = {}
    for part in body.split(b"--" + boundary):
        head, _, data = part.partition(b"\r\n\r\n")
        nm = re.search(rb'name="([^"]+)"', head)
        if not nm:
            continue
        data = data[:-2] if data.endswith(b"\r\n") else data
        fn = re.search(rb'filename="([^"]*)"', head)
        if fn:
            out[nm.group(1).decode()] = fn.group(1).decode()
            out["_size"] = len(data)
        else:
            out[nm.group(1).decode()] = data.decode(errors="replace")
    return out


class WS:
    """Just enough of a WebSocket server (RFC 6455) for the console."""

    def __init__(self, h):
        self.f, self.w, self.buf = h.rfile, h.wfile, b""

    def frame(self):
        b0, b1 = self.f.read(2)
        n = b1 & 0x7F
        if n == 126:
            n = struct.unpack(">H", self.f.read(2))[0]
        elif n == 127:
            n = struct.unpack(">Q", self.f.read(8))[0]
        mask = self.f.read(4) if b1 & 0x80 else b"\0\0\0\0"
        data = bytes(x ^ mask[i % 4] for i, x in enumerate(self.f.read(n)))
        if b0 & 0x0F == 8:
            raise EOFError
        return data

    def read(self, n):
        while len(self.buf) < n:
            self.buf += self.frame()
        out, self.buf = self.buf[:n], self.buf[n:]
        return out

    def send(self, data):
        n = len(data)
        head = bytes([0x82, n]) if n < 126 else bytes([0x82, 126]) + struct.pack(">H", n) if n < 65536 else bytes([0x82, 127]) + struct.pack(">Q", n)
        self.w.write(head + data)
        self.w.flush()


def ws_console(h, name, ticket, user, term):
    key = h.headers.get("Sec-WebSocket-Key", "")
    acc = base64.b64encode(hashlib.sha1((key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode()).digest()).decode()
    h.send_response(101)
    for k, v in (("Upgrade", "websocket"), ("Connection", "Upgrade"), ("Sec-WebSocket-Accept", acc), ("Sec-WebSocket-Protocol", "binary")):
        h.send_header(k, v)
    h.end_headers()
    ws = WS(h)
    try:
        if term:
            serial_console(ws, name, ticket, user)
        else:
            vnc_console(ws, name)
    except (EOFError, OSError, ValueError):
        pass
    h.close_connection = True


def serial_console(ws, name, ticket, user):
    if ws.frame() != f"{user}:{ticket}\n".encode():
        return
    ws.send(b"OK")
    ws.send(f"\r\nDebian GNU/Linux 13 {name} ttyS0 (mock)\r\n\r\n{name} login: ".encode())
    while True:
        m = ws.frame().decode(errors="replace")
        if m.startswith("0:"):
            data = m.split(":", 2)[2]
            ws.send(data.replace("\r", "\r\n").encode())


def vnc_console(ws, name):
    """RFB 3.8 without authentication: one full frame of a dark screen
    with a lighter window, then input is read and ignored."""
    W, H = 800, 600
    ws.send(b"RFB 003.008\n")
    ws.read(12)
    ws.send(bytes([1, 1]))  # one security type: None
    ws.read(1)
    ws.send(struct.pack(">I", 0))
    ws.read(1)  # ClientInit
    title = f"{name} (mock display)".encode()
    ws.send(struct.pack(">HHBBBBHHHBBB3xI", W, H, 32, 24, 0, 1, 255, 255, 255, 16, 8, 0, len(title)) + title)
    shifts, big, sent = (16, 8, 0), False, False
    while True:
        t = ws.read(1)[0]
        if t == 0:  # SetPixelFormat
            pf = ws.read(19)[3:]
            big, shifts, sent = bool(pf[2]), (pf[10], pf[11], pf[12]), False
        elif t == 2:  # SetEncodings
            n = struct.unpack(">xH", ws.read(3))[0]
            ws.read(4 * n)
        elif t == 3:  # FramebufferUpdateRequest
            inc = ws.read(9)[0]
            if sent and inc:
                continue
            px = {c: struct.pack(">I" if big else "<I", (c[0] << shifts[0]) | (c[1] << shifts[1]) | (c[2] << shifts[2]))
                  for c in ((48, 64, 92), (26, 30, 38), (12, 14, 18))}
            frame = bytearray()
            for y in range(H):
                for x in range(W):
                    inside = 140 <= y < 460 and 120 <= x < 680
                    frame += px[(48, 64, 92)] if inside and y < 172 else px[(26, 30, 38)] if inside else px[(12, 14, 18)]
            ws.send(struct.pack(">BxHHHHHi", 0, 1, 0, 0, W, H, 0) + bytes(frame))
            sent = True
        elif t == 4:  # KeyEvent
            ws.read(7)
        elif t == 5:  # PointerEvent
            ws.read(5)
        elif t == 6:  # ClientCutText
            ws.read(struct.unpack(">3xI", ws.read(7))[0])
        else:
            return


os.makedirs(args.certdir, exist_ok=True)
crt, key = os.path.join(args.certdir, "pve.crt"), os.path.join(args.certdir, "pve.key")
if not os.path.exists(crt):
    subprocess.run(["openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1", "-nodes", "-days", "365",
                    "-subj", "/CN=pve1.mock", "-keyout", key, "-out", crt], check=True, capture_output=True)
srv = ThreadingHTTPServer(("127.0.0.1", args.port), H)
ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain(crt, key)
srv.socket = ctx.wrap_socket(srv.socket, server_side=True)
print(f"mock PVE on https://127.0.0.1:{args.port}", flush=True)
srv.serve_forever()
