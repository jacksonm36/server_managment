#!/usr/bin/env python3
"""A small mock of the Proxmox VE API for developing and testing servermgr
without a real cluster. HTTPS with a self-signed certificate, API-token
auth, and the endpoints servermgr uses, with in-memory state.

  scripts/mock-pve.py --port 18006 --token 'root@pam!servermgr=SECRET'
      [--guest-ip 10.247.11.50]   # IP the guest agent reports for new VMs

Tasks finish after ~2 s. Not for production use.

Tenant isolation is mocked too: pools, users, API tokens and ACLs. A
token other than --token only sees and touches guests in pools its user
has an ACL on (plus templates with an ACL on /vms/<id>), like Proxmox.
"""
import argparse, json, os, re, ssl, subprocess, tempfile, threading, time, urllib.parse
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
        n = int(self.headers.get("Content-Length") or 0)
        form = {k: v[0] for k, v in urllib.parse.parse_qs(self.rfile.read(n).decode() if n else "").items()}
        q = {k: v[0] for k, v in urllib.parse.parse_qs(u.query).items()}
        with lock:
            return self.route(method, p, form, q)

    def route(self, method, p, form, q):
        user, root = self.user, self.user == "root@pam"
        deny = lambda: self.send(403, err="Permission check failed")
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
