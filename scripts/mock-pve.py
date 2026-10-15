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
STORAGE_INFO = {"local": ("dir", "iso,vztmpl,backup,import"), "local-lvm": ("lvmthin", "images,rootdir")}
STORAGE_CFG = {"local": {"type": "dir", "path": "/var/lib/vz", "content": "iso,vztmpl,backup,import"},
               "local-lvm": {"type": "lvmthin", "vgname": "pve", "thinpool": "data", "content": "images,rootdir"}}
ISOS = {"debian-13.1.0-amd64-netinst.iso", "ubuntu-24.04.3-live-server-amd64.iso"}
DISKS = {n: [{"devpath": "/dev/nvme0n1", "model": "Samsung SSD 990 PRO 1TB", "serial": f"S7KX{i}0001", "size": 1000 << 30, "type": "nvme", "health": "PASSED", "wearout": 97, "used": "LVM", "gpt": 1, "rpm": 0},
             {"devpath": "/dev/sda", "model": "WDC WD40EFRX", "serial": f"WD-{i}A1", "size": 4000 << 30, "type": "hdd", "health": "PASSED", "wearout": "N/A", "used": "", "gpt": 0, "rpm": 5400},
             {"devpath": "/dev/sdb", "model": "WDC WD40EFRX", "serial": f"WD-{i}A2", "size": 4000 << 30, "type": "hdd", "health": "PASSED", "wearout": "N/A", "used": "partitions", "gpt": 1, "rpm": 5400},
             {"devpath": "/dev/sdc", "model": "ST4000VN008", "serial": f"ZD-{i}C3", "size": 4000 << 30, "type": "hdd", "health": "FAILED", "wearout": "N/A", "used": "", "gpt": 0, "rpm": 5900}]
         for i, n in enumerate(NODES)}
ZFS = {n: {} for n in NODES}  # node -> pool -> {"devices": [...], "raidlevel": ...}


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
                    "mem": g["maxmem"] // 2 if run else 0, "maxdisk": g["maxdisk"], "disk": 0, "template": g["template"], "tags": g["config"].get("tags", ""),
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
            m = re.fullmatch(r"/nodes/[\w-]+/(?:qemu|lxc)/(\d+)/vncwebsocket", p)
            q = {k: v[0] for k, v in urllib.parse.parse_qs(u.query).items()}
            with lock:
                c = CONSOLES.pop(q.get("vncticket", ""), None)
            if not m or not c or c[0] != self.user or c[1] != int(m.group(1)) or str(c[2]) != q.get("port"):
                return self.send(401, err="invalid ticket")
            return ws_console(self, GUESTS[c[1]]["name"], q["vncticket"], c[0], c[3])
        n = int(self.headers.get("Content-Length") or 0)
        form = {k: v[0] for k, v in urllib.parse.parse_qs(self.rfile.read(n).decode() if n else "").items()}
        q = {k: v[0] for k, v in urllib.parse.parse_qs(u.query).items()}
        with lock:
            return self.route(method, p, form, q)

    def route(self, method, p, form, q):
        user, root = self.user, self.user == "root@pam"
        deny = lambda: self.send(403, err="Permission check failed")
        if p == "/storage" or p.startswith("/storage/") or re.fullmatch(r"/nodes/[\w-]+/disks/.*", p):
            if not root:
                return deny()
            return self.storage_admin(method, p, form, q)
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
            info = lambda st: STORAGE_INFO.get(st, ("dir", "backup,images"))
            on = lambda st: m.group(1) in STORAGE_CFG.get(st, {}).get("nodes", m.group(1)).split(",") and STORAGE_CFG.get(st, {}).get("disable") != "1"
            size = lambda st: (4000 << 30) if STORAGE_INFO.get(st, ("",))[0] in ("zfspool", "nfs") else (500 << 30)
            return self.send(200, [{"storage": st, "active": 1, "enabled": 1, "type": info(st)[0], "content": info(st)[1], "shared": int(info(st)[0] == "nfs"),
                                    "total": size(st), "used": size(st) // 5, "avail": size(st) - size(st) // 5} for st in STORAGES
                                   if on(st) and can(user, "/storage/" + st) and (not q.get("content") or q["content"] in info(st)[1].split(","))])
        m = re.fullmatch(r"/nodes/([\w-]+)/storage/([\w-]+)/download-url", p)
        if m and method == "POST":
            if user != "root@pam":
                return self.send(403, err="Permission check failed")
            content = form.get("content", "import")
            vol = f"{m.group(2)}:{content}/{form.get('filename', '')}"
            add = (lambda: ISOS.add(form.get("filename"))) if content == "iso" else (lambda: IMPORTS.__setitem__(vol, form))
            return self.send(200, task(m.group(1), "download", 0, add))
        m = re.fullmatch(r"/nodes/([\w-]+)/storage/([\w-]+)/content(?:/(.+))?", p)
        if m:
            st, volid = m.group(2), urllib.parse.unquote(m.group(3) or "")
            if not can(user, "/storage/" + st):
                return self.send(403, err="Permission check failed")
            if method == "GET" and not volid and q.get("content") == "iso":
                return self.send(200, [{"volid": f"{st}:iso/{n}", "content": "iso", "size": 700 << 20} for n in sorted(ISOS)
                                       if "iso" in STORAGE_INFO.get(st, ("", ""))[1]])
            if method == "GET" and not volid and root and not q.get("vmid"):
                # the storage page's browser: everything on the storage
                out = []
                if "iso" in STORAGE_INFO.get(st, ("", ""))[1]:
                    out += [{"volid": f"{st}:iso/{n}", "content": "iso", "format": "iso", "size": 700 << 20, "ctime": now() - 86400} for n in sorted(ISOS)]
                out += [{"volid": v, "content": "import", "format": "qcow2", "size": 400 << 20} for v in IMPORTS if v.startswith(st + ":")]
                out += [{"volid": v, "format": "vma.zst", "content": "backup", **b} for v, b in BACKUPS.items() if v.startswith(st + ":")]
                if "images" in STORAGE_INFO.get(st, ("", ""))[1]:
                    out += [{"volid": f"{st}:vm-{i}-disk-0", "content": "images", "format": "raw", "size": g["maxdisk"], "vmid": i} for i, g in GUESTS.items()]
                return self.send(200, out)
            if method == "DELETE" and volid.startswith(st + ":iso/") and root:
                ISOS.discard(volid.split("/", 1)[1])
                return self.send(200, None)
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
        m = re.fullmatch(r"/nodes/([\w-]+)/(status|rrddata)", p)
        if m and method == "GET" and root and m.group(1) in NODES:
            n = NODES[m.group(1)]
            if m.group(2) == "status":
                return self.send(200, {"uptime": 99999, "loadavg": ["0.42", "0.38", "0.35"], "kversion": "Linux 6.8.12-4-pve #1 SMP PREEMPT_DYNAMIC",
                                       "pveversion": "pve-manager/8.4.1/mock", "cpu": 0.12, "wait": 0.01,
                                       "cpuinfo": {"model": "AMD EPYC 7302P 16-Core Processor", "cpus": n["maxcpu"], "sockets": 1, "cores": n["maxcpu"] // 2, "mhz": "3000.000"},
                                       "memory": {"total": n["maxmem"], "used": n["maxmem"] // 3, "free": n["maxmem"] - n["maxmem"] // 3},
                                       "swap": {"total": 8 << 30, "used": 0, "free": 8 << 30},
                                       "rootfs": {"total": 100 << 30, "used": 24 << 30, "avail": 76 << 30},
                                       "boot-info": {"mode": "efi"}, "current-kernel": {"release": "6.8.12-4-pve"}})
            import math
            step = {"hour": 60, "day": 1800, "week": 10800, "month": 43200, "year": 604800}.get(q.get("timeframe"), 60)
            t1, out = now() // step * step, []
            for i in range(70):
                t = t1 - (69 - i) * step
                w = (math.sin(t / (step * 9)) + 1) / 2
                out.append({"time": t, "cpu": 0.08 + 0.3 * w, "iowait": 0.01 * w, "loadavg": 0.3 + 2 * w, "memused": n["maxmem"] * (0.3 + 0.1 * w), "memtotal": n["maxmem"],
                            "swapused": 0, "netin": 100000 + 2e6 * w, "netout": 50000 + 1e6 * (1 - w), "rootused": 24 << 30, "roottotal": 100 << 30})
            out[-1] = {"time": t1}
            return self.send(200, out)
        m = re.fullmatch(r"/nodes/([\w-]+)/tasks", p)
        if m and method == "GET":
            vmid = int(q.get("vmid", 0))
            if vmid and not can_guest(user, vmid) or not vmid and user != "root@pam":
                return self.send(200, [])
            out = [{"upid": u, "type": t["type"], "id": t["id"], "user": "root@pam!servermgr", "starttime": t["starttime"], "node": t["node"],
                    **({"endtime": t["starttime"] + 2, "status": t["exitstatus"]} if t["status"] == "stopped" else {})}
                   for u, t in TASKS.items() if t["node"] == m.group(1) and (not vmid or t["id"] == str(vmid))]
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
            out = {"name": g["name"], "cores": g["maxcpu"], "memory": g["maxmem"] >> 20, **hw, **g["config"]}
            return self.send(200, {k: v for k, v in out.items() if v is not None})
        if rest == "/resize" and method == "PUT":
            d, grow = form.get("disk", ""), int(form.get("size", "+0G").strip("+G") or 0)
            if d in ("scsi0", "rootfs"):
                g["maxdisk"] += grow << 30
            return self.send(200, task(node, "resize", vmid))
        if rest == "/unlink" and method == "PUT":
            for d in form.get("idlist", "").split(","):
                g["config"][d] = None
            return self.send(200, None)
        if rest == "/migrate" and method == "POST":
            if not root:
                return deny()
            return self.send(200, task(node, "qmigrate", vmid, lambda: g.__setitem__("node", form.get("target", node))))
        if rest == "/config" and method in ("POST", "PUT"):
            for k in form.pop("delete", "").split(","):
                if k:
                    g["config"][k] = None
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

    def storage_admin(self, method, p, form, q):
        if p == "/storage" and method == "GET":
            return self.send(200, [{"storage": k, **v} for k, v in STORAGE_CFG.items()])
        if p == "/storage" and method == "POST":
            sid = form.get("storage", "")
            if sid in STORAGE_CFG:
                return self.send(400, err=f"storage ID '{sid}' already defined")
            STORAGE_CFG[sid] = {k: v for k, v in form.items() if k != "storage"}
            STORAGE_INFO[sid] = (form.get("type"), form.get("content", ""))
            STORAGES.append(sid)
            return self.send(200, None)
        m = re.fullmatch(r"/storage/([\w.-]+)", p)
        if m:
            sid = m.group(1)
            if sid not in STORAGE_CFG:
                return self.send(404, err="no such storage")
            if method == "PUT":
                cfg = STORAGE_CFG[sid]
                for k in form.get("delete", "").split(","):
                    cfg.pop(k, None)
                cfg.update({k: v for k, v in form.items() if k != "delete"})
                STORAGE_INFO[sid] = (cfg["type"], cfg.get("content", ""))
                return self.send(200, None)
            if method == "DELETE":
                STORAGE_CFG.pop(sid)
                STORAGE_INFO.pop(sid, None)
                if sid in STORAGES:
                    STORAGES.remove(sid)
                return self.send(200, None)
        m = re.fullmatch(r"/nodes/([\w-]+)/disks/(list|smart|wipedisk|zfs)(?:/([\w.:-]+))?", p)
        if not m or m.group(1) not in NODES:
            return self.send(501, err="mock: not implemented")
        node, what, name = m.groups()
        disks = DISKS[node]
        if what == "list":
            return self.send(200, disks)
        if what == "smart":
            d = next((d for d in disks if d["devpath"] == q.get("disk")), None)
            if not d:
                return self.send(400, err="no such disk")
            if d["type"] == "nvme":
                return self.send(200, {"health": d["health"], "type": "text", "text": "Critical Warning: 0x00\nTemperature: 41 Celsius\nAvailable Spare: 100%\nPercentage Used: 3%\nPower On Hours: 8,214\nMedia and Data Integrity Errors: 0"})
            bad = d["health"] != "PASSED"
            attrs = [(1, "Raw_Read_Error_Rate", 200, 51, "0"), (5, "Reallocated_Sector_Ct", 140 if bad else 200, 140, "512" if bad else "0"),
                     (9, "Power_On_Hours", 62, 0, "27841"), (194, "Temperature_Celsius", 115, 0, "35"), (197, "Current_Pending_Sector", 200, 0, "37" if bad else "0")]
            return self.send(200, {"health": d["health"], "type": "ata", "attributes": [
                {"id": i, "name": n, "value": v, "worst": v, "threshold": t, "raw": raw, "flags": "PO--CK", "fail": "FAILING_NOW" if bad and i == 5 else "-"} for i, n, v, t, raw in attrs]})
        if what == "wipedisk" and method == "PUT":
            d = next((d for d in disks if d["devpath"] == form.get("disk")), None)
            if not d:
                return self.send(400, err="no such disk")
            return self.send(200, task(node, "wipedisk", 0, lambda: d.update(used="", gpt=0)))
        pools = ZFS[node]
        if what == "zfs" and not name and method == "GET":
            return self.send(200, [{"name": k, "health": "ONLINE", "size": v["size"], "alloc": v["size"] // 10, "free": v["size"] - v["size"] // 10, "frag": 1, "dedup": 1.0} for k, v in pools.items()])
        if what == "zfs" and not name and method == "POST":
            devs = form.get("devices", "").split(",")
            def create():
                n = len(devs)
                usable = {"single": 1, "mirror": 1, "raid10": n // 2, "raidz": n - 1, "raidz2": n - 2, "raidz3": n - 3}[form["raidlevel"]]
                pools[form["name"]] = {"devices": devs, "raidlevel": form["raidlevel"], "size": usable * (4000 << 30)}
                for d in disks:
                    if d["devpath"] in devs:
                        d["used"] = "ZFS"
                if form.get("add_storage") == "1":
                    STORAGE_CFG[form["name"]] = {"type": "zfspool", "pool": form["name"], "content": "images,rootdir", "nodes": node}
                    STORAGE_INFO[form["name"]] = ("zfspool", "images,rootdir")
                    STORAGES.append(form["name"])
            return self.send(200, task(node, "zfscreate", 0, create))
        if what == "zfs" and name and name in pools and method == "GET":
            v = pools[name]
            kids = [{"name": d.removeprefix("/dev/"), "state": "ONLINE", "read": 0, "write": 0, "cksum": 0} for d in v["devices"]]
            vdev = kids if v["raidlevel"] == "single" else [{"name": f"{v['raidlevel']}-0", "state": "ONLINE", "read": 0, "write": 0, "cksum": 0, "children": kids}]
            return self.send(200, {"name": name, "state": "ONLINE", "status": "", "action": "", "scan": "scrub repaired 0B in 00:01:12 with 0 errors", "errors": "No known data errors",
                                   "children": [{"name": name, "state": "ONLINE", "read": 0, "write": 0, "cksum": 0, "children": vdev}]})
        if what == "zfs" and name and name in pools and method == "DELETE":
            def destroy():
                v = pools.pop(name)
                for d in disks:
                    if d["devpath"] in v["devices"]:
                        d["used"] = "" if form.get("cleanup-disks") == "1" else "partitions"
                if form.get("cleanup-config") == "1" and name in STORAGE_CFG:
                    STORAGE_CFG.pop(name)
                    STORAGE_INFO.pop(name, None)
                    if name in STORAGES:
                        STORAGES.remove(name)
            return self.send(200, task(node, "zfsremove", 0, destroy))
        return self.send(404, err="not found")

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
