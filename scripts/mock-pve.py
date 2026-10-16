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
ISOS = {"debian-13.1.0-amd64-netinst.iso", "ubuntu-24.04.3-live-server-amd64.iso", "SERVER_EVAL_x64FRE_en-us_2022.iso"}

# What a Windows template build's prepare script logs, one line every few
# seconds after the VM starts; Sysprep then shuts the VM down.
WINLOG = ["servermgr prepare: Microsoft Windows Server 2022 Standard Evaluation 10.0.20348, profile {profile}",
          "installing VirtIO drivers and the QEMU guest agent from E:\\", "VirtIO guest tools: exit code 0", "tuning Windows for a Proxmox VM",
          "enabling Remote Desktop with Network Level Authentication", "hardening: firewall", "hardening: TLS (1.2 and 1.3 only, no RC4 or 3DES)",
          "downloading Cloudbase-Init from https://cloudbase.it/downloads/CloudbaseInitSetup_Stable_x64.msi",
          "signature OK: CN=Cloudbase Solutions SRL", "configuring Cloudbase-Init for Proxmox (config drive)",
          "cleaning up the component store (this can take a while)", "SERVERMGR-SYSPREP running Sysprep; the VM shuts down when it is done"]
DISKS = {n: [{"devpath": "/dev/nvme0n1", "model": "Samsung SSD 990 PRO 1TB", "serial": f"S7KX{i}0001", "size": 1000 << 30, "type": "nvme", "health": "PASSED", "wearout": 97, "used": "LVM", "gpt": 1, "rpm": 0},
             {"devpath": "/dev/sda", "model": "WDC WD40EFRX", "serial": f"WD-{i}A1", "size": 4000 << 30, "type": "hdd", "health": "PASSED", "wearout": "N/A", "used": "", "gpt": 0, "rpm": 5400},
             {"devpath": "/dev/sdb", "model": "WDC WD40EFRX", "serial": f"WD-{i}A2", "size": 4000 << 30, "type": "hdd", "health": "PASSED", "wearout": "N/A", "used": "partitions", "gpt": 1, "rpm": 5400},
             {"devpath": "/dev/sdc", "model": "ST4000VN008", "serial": f"ZD-{i}C3", "size": 4000 << 30, "type": "hdd", "health": "FAILED", "wearout": "N/A", "used": "", "gpt": 0, "rpm": 5900}]
         for i, n in enumerate(NODES)}
ZFS = {n: {} for n in NODES}  # node -> pool -> {"devices": [...], "raidlevel": ...}
APLINFO = [
    {"template": "debian-13-standard_13.1-2_amd64.tar.zst", "package": "debian-13-standard", "section": "system", "headline": "Debian 13 (standard)", "version": "13.1-2", "os": "debian-13", "type": "lxc", "description": "Minimal Debian system."},
    {"template": "ubuntu-24.04-standard_24.04-2_amd64.tar.zst", "package": "ubuntu-24.04-standard", "section": "system", "headline": "Ubuntu 24.04 (standard)", "version": "24.04-2", "os": "ubuntu-24.04", "type": "lxc", "description": "Minimal Ubuntu system."},
    {"template": "alpine-3.22-default_20250617_amd64.tar.xz", "package": "alpine-3.22-default", "section": "system", "headline": "Alpine Linux 3.22", "version": "3.22", "os": "alpine", "type": "lxc", "description": "Small Alpine system."},
    {"template": "debian-12-turnkey-wordpress_18.0-1_amd64.tar.gz", "package": "turnkey-wordpress", "section": "turnkeylinux", "headline": "TurnKey WordPress", "version": "18.0-1", "os": "debian-12", "type": "lxc", "infopage": "https://www.turnkeylinux.org/wordpress", "description": "Blog and content management."},
    {"template": "debian-12-turnkey-nextcloud_18.0-1_amd64.tar.gz", "package": "turnkey-nextcloud", "section": "turnkeylinux", "headline": "TurnKey Nextcloud", "version": "18.0-1", "os": "debian-12", "type": "lxc", "infopage": "https://www.turnkeylinux.org/nextcloud", "description": "File sync, share and collaboration."},
    {"template": "debian-12-turnkey-gitlab_18.0-1_amd64.tar.gz", "package": "turnkey-gitlab", "section": "turnkeylinux", "headline": "TurnKey GitLab", "version": "18.0-1", "os": "debian-12", "type": "lxc", "infopage": "https://www.turnkeylinux.org/gitlab", "description": "Git hosting, CI/CD and issue tracking."},
    {"template": "debian-12-turnkey-gitea_18.0-1_amd64.tar.gz", "package": "turnkey-gitea", "section": "turnkeylinux", "headline": "TurnKey Gitea", "version": "18.0-1", "os": "debian-12", "type": "lxc", "infopage": "https://www.turnkeylinux.org/gitea", "description": "Lightweight self-hosted Git."},
    {"template": "debian-12-turnkey-nginx-php-fastcgi_18.0-1_amd64.tar.gz", "package": "turnkey-nginx-php-fastcgi", "section": "turnkeylinux", "headline": "TurnKey Nginx PHP FastCGI", "version": "18.0-1", "os": "debian-12", "type": "lxc", "infopage": "https://www.turnkeylinux.org/nginx-php-fastcgi", "description": "Web stack: Nginx + PHP-FPM."},
    {"template": "debian-12-turnkey-lamp_18.0-1_amd64.tar.gz", "package": "turnkey-lamp", "section": "turnkeylinux", "headline": "TurnKey LAMP Stack", "version": "18.0-1", "os": "debian-12", "type": "lxc", "infopage": "https://www.turnkeylinux.org/lamp", "description": "Linux, Apache, MySQL, PHP."},
    {"template": "debian-12-turnkey-nodejs_18.0-1_amd64.tar.gz", "package": "turnkey-nodejs", "section": "turnkeylinux", "headline": "TurnKey Node.js", "version": "18.0-1", "os": "debian-12", "type": "lxc", "infopage": "https://www.turnkeylinux.org/nodejs", "description": "JavaScript runtime with a ready web server."},
    {"template": "debian-12-turnkey-mediawiki_18.0-1_amd64.tar.gz", "package": "turnkey-mediawiki", "section": "turnkeylinux", "headline": "TurnKey MediaWiki", "version": "18.0-1", "os": "debian-12", "type": "lxc", "infopage": "https://www.turnkeylinux.org/mediawiki", "description": "The wiki engine behind Wikipedia."},
    {"template": "debian-12-turnkey-odoo_18.0-1_amd64.tar.gz", "package": "turnkey-odoo", "section": "turnkeylinux", "headline": "TurnKey Odoo", "version": "18.0-1", "os": "debian-12", "type": "lxc", "infopage": "https://www.turnkeylinux.org/odoo", "description": "ERP and CRM suite."},
    {"template": "debian-12-turnkey-moodle_18.0-1_amd64.tar.gz", "package": "turnkey-moodle", "section": "turnkeylinux", "headline": "TurnKey Moodle", "version": "18.0-1", "os": "debian-12", "type": "lxc", "infopage": "https://www.turnkeylinux.org/moodle", "description": "Learning management system."},
    {"template": "debian-12-turnkey-mattermost_18.0-1_amd64.tar.gz", "package": "turnkey-mattermost", "section": "turnkeylinux", "headline": "TurnKey Mattermost", "version": "18.0-1", "os": "debian-12", "type": "lxc", "infopage": "https://www.turnkeylinux.org/mattermost", "description": "Team chat."},
    {"template": "debian-12-turnkey-openvpn_18.0-1_amd64.tar.gz", "package": "turnkey-openvpn", "section": "turnkeylinux", "headline": "TurnKey OpenVPN", "version": "18.0-1", "os": "debian-12", "type": "lxc", "infopage": "https://www.turnkeylinux.org/openvpn", "description": "VPN server."},
    {"template": "debian-12-turnkey-wireguard_18.0-1_amd64.tar.gz", "package": "turnkey-wireguard", "section": "turnkeylinux", "headline": "TurnKey WireGuard", "version": "18.0-1", "os": "debian-12", "type": "lxc", "infopage": "https://www.turnkeylinux.org/wireguard", "description": "Fast modern VPN."},
    {"template": "debian-12-turnkey-fileserver_18.0-1_amd64.tar.gz", "package": "turnkey-fileserver", "section": "turnkeylinux", "headline": "TurnKey File Server", "version": "18.0-1", "os": "debian-12", "type": "lxc", "infopage": "https://www.turnkeylinux.org/fileserver", "description": "Samba file sharing."},
    {"template": "debian-12-turnkey-postgresql_18.0-1_amd64.tar.gz", "package": "turnkey-postgresql", "section": "turnkeylinux", "headline": "TurnKey PostgreSQL", "version": "18.0-1", "os": "debian-12", "type": "lxc", "infopage": "https://www.turnkeylinux.org/postgresql", "description": "Database server."},
    {"template": "debian-12-turnkey-mysql_18.0-1_amd64.tar.gz", "package": "turnkey-mysql", "section": "turnkeylinux", "headline": "TurnKey MySQL", "version": "18.0-1", "os": "debian-12", "type": "lxc", "infopage": "https://www.turnkeylinux.org/mysql", "description": "Database server."},
    {"template": "debian-12-turnkey-redmine_18.0-1_amd64.tar.gz", "package": "turnkey-redmine", "section": "turnkeylinux", "headline": "TurnKey Redmine", "version": "18.0-1", "os": "debian-12", "type": "lxc", "infopage": "https://www.turnkeylinux.org/redmine", "description": "Project management."},
    {"template": "debian-12-turnkey-jenkins_18.0-1_amd64.tar.gz", "package": "turnkey-jenkins", "section": "turnkeylinux", "headline": "TurnKey Jenkins", "version": "18.0-1", "os": "debian-12", "type": "lxc", "infopage": "https://www.turnkeylinux.org/jenkins", "description": "Automation server."},
    {"template": "debian-12-turnkey-zoneminder_18.0-1_amd64.tar.gz", "package": "turnkey-zoneminder", "section": "turnkeylinux", "headline": "TurnKey ZoneMinder", "version": "18.0-1", "os": "debian-12", "type": "lxc", "infopage": "https://www.turnkeylinux.org/zoneminder", "description": "Video surveillance."},
    {"template": "debian-12-turnkey-core_18.0-1_amd64.tar.gz", "package": "turnkey-core", "section": "turnkeylinux", "headline": "TurnKey Core", "version": "18.0-1", "os": "debian-12", "type": "lxc", "infopage": "https://www.turnkeylinux.org/core", "description": "TurnKey's minimal base."},
]
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
            m = re.fullmatch(r"/nodes/([\w-]+)(?:/(?:qemu|lxc)/(\d+))?/vncwebsocket", p)
            q = {k: v[0] for k, v in urllib.parse.parse_qs(u.query).items()}
            with lock:
                c = CONSOLES.pop(q.get("vncticket", ""), None)
            if not m or not c or c[0] != self.user or c[1] != int(m.group(2) or 0) or str(c[2]) != q.get("port"):
                return self.send(401, err="invalid ticket")
            return ws_console(self, GUESTS[c[1]]["name"] if c[1] else m.group(1), q["vncticket"], c[0], c[3], c[4])
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
        if p == "/storage" or p.startswith("/storage/") or re.fullmatch(r"/nodes/[\w-]+/disks/(list|smart|wipedisk|zfs)(/.*)?", p):
            if not root:
                return deny()
            return self.storage_admin(method, p, form, q)
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
            if method == "GET" and not volid and q.get("content") == "vztmpl":
                return self.send(200, [{"volid": v, "content": "vztmpl", "format": "tzst", "size": n} for v, n in CTTMPL.items() if v.startswith(st + ":")])
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
            return self.send(200, [{"name": "lo", "inet": "127.0.0.1/8"}, {"name": "eth0", "hwaddr": "bc:24:11:00:00:02", "inet": "10.247.11.20/24", "inet6": "fd00::20/64"}])
        if rest == "/config" and method == "GET":
            hw = {"sockets": 1, "cpu": "x86-64-v2-AES", "scsihw": "virtio-scsi-single", "scsi0": f"local-lvm:vm-{vmid}-disk-0,iothread=1,size={g['maxdisk'] >> 30}G",
                  "net0": "virtio=BC:24:11:00:00:01,bridge=vmbr0", "ostype": "l26", "boot": "order=scsi0", "onboot": 1, "agent": "1",
                  "ciuser": "debian", "cipassword": "$5$mock$hash", "description": "Mock guest for development."} if typ == "qemu" else {
                  "hostname": g["name"], "rootfs": f"local-lvm:vm-{vmid}-disk-0,size={g['maxdisk'] >> 30}G", "arch": "amd64",
                  "net0": "name=eth0,bridge=vmbr0,hwaddr=BC:24:11:00:00:02,ip=dhcp,tag=20,firewall=1", "ostype": "debian", "unprivileged": 1, "swap": 512}
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
            out = {"port": str(port), "ticket": ticket, "user": user, "upid": task(node, "vncproxy", vmid)}
            if form.get("generate-password") == "1":
                out["password"] = secrets.token_urlsafe(6)[:8]
            CONSOLES[ticket] = (user, vmid, port, rest == "/termproxy", out.get("password", ticket))
            return self.send(200, out)
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
        if rest == "/sendkey" and method == "PUT":
            return self.send(200, None)
        if rest == "/agent/file-read" and method == "GET":
            if g["status"] != "running" or "servermgr-answer" not in (g["config"].get("ide3") or ""):
                return self.send(500, err="QEMU guest agent is not running")
            n = int((now() - g["started"]) / 3) - 3  # Windows Setup first
            if n <= 0:
                return self.send(500, err="QEMU guest agent is not running")
            profile = "Hardened" if "hardened" in (g["config"].get("tags") or "") else "Standard"
            lines = [time.strftime("%H:%M:%S ") + x.format(profile=profile) for x in WINLOG[:n]]
            if n >= len(WINLOG):
                g["status"] = "stopped"
            return self.send(200, {"content": "\ufeff" + "\r\n".join(lines) + "\r\n", "truncated": False})
        if rest == "/agent/network-get-interfaces" and method == "GET":
            if g["status"] != "running" or not args.guest_ip or str(g["config"].get("agent", "")).split(",")[0] not in ("1", "enabled=1"):
                return self.send(500, err="QEMU guest agent is not running")
            return self.send(200, {"result": [
                {"name": "lo", "ip-addresses": [{"ip-address-type": "ipv4", "ip-address": "127.0.0.1", "prefix": 8}]},
                {"name": "eth0", "hardware-address": "bc:24:11:00:00:01", "ip-addresses": [{"ip-address-type": "ipv4", "ip-address": args.guest_ip, "prefix": 24},
                                                                                          {"ip-address-type": "ipv6", "ip-address": "fe80::1", "prefix": 64}]},
                {"name": "docker0", "hardware-address": "02:42:ac:11:00:01", "ip-addresses": [{"ip-address-type": "ipv4", "ip-address": "172.17.0.1", "prefix": 16}]}]})
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
            if rest == "/aplinfo" and method == "GET":
                return self.send(200, APLINFO) or True
            if rest == "/aplinfo" and method == "POST":
                CTTMPL[f"{form.get('storage', 'local')}:vztmpl/{form.get('template', '')}"] = 200 << 20
                return self.send(200, task(node, "download", 0)) or True
            if rest == "/termproxy" and method == "POST":
                port, ticket = 5900 + len(CONSOLES), "PVEVNC:" + secrets.token_hex(12)
                CONSOLES[ticket] = (self.user, 0, port, True, ticket)
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
            cm = re.fullmatch(r"/storage/([\w-]+)/(upload|status)", rest)
            if cm and cm.group(2) == "upload" and method == "POST":
                name = form.get("filename", "upload.iso")
                if form.get("content") == "iso":
                    ISOS.add(name)
                else:
                    CTTMPL[f"{cm.group(1)}:vztmpl/{name}"] = form.get("_size", 0)
                return self.send(200, task(node, "imgcopy", 0)) or True
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


def ws_console(h, name, ticket, user, term, password):
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
            vnc_console(ws, name, password)
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


def vnc_des(password, challenge):
    """VNC authentication's response: DES with the bit-reversed password."""
    from cryptography.hazmat.decrepit.ciphers.algorithms import TripleDES
    from cryptography.hazmat.primitives.ciphers import Cipher, modes
    key = bytes(int(f"{b:08b}"[::-1], 2) for b in password.encode()[:8].ljust(8, b"\0"))
    enc = Cipher(TripleDES(key * 3), modes.ECB()).encryptor()
    return enc.update(challenge) + enc.finalize()


def vnc_console(ws, name, password):
    """RFB 3.8 with VNC authentication, like QEMU's display: one full frame
    of a dark screen with a lighter window, then input is read and ignored."""
    W, H = 800, 600
    ws.send(b"RFB 003.008\n")
    ws.read(12)
    ws.send(bytes([1, 2]))  # one security type: VNC authentication
    ws.read(1)
    challenge = secrets.token_bytes(16)
    ws.send(challenge)
    if ws.read(16) != vnc_des(password, challenge):
        reason = b"Authentication failed"
        ws.send(struct.pack(">II", 1, len(reason)) + reason)
        return
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
