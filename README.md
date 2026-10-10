# servermgr

A self-hosted control panel for managing Linux servers, in the spirit of
CloudPanel / aaPanel but built around a lightweight agent on each managed
server.

```
 browser ──HTTP──▶ panel (Go) ──▶ PostgreSQL
                     ▲
                     │ gRPC over mTLS (agent dials out, port 9443)
                     │
          agent (Go) on each managed server
```

- **panel/**: HTTP JSON API and UI (`:8090`), agent enrollment, an internal CA,
  and a gRPC listener with mutual TLS (`:9443`). Connected agents are tracked in
  memory, and the last 15 minutes of metrics are kept per server.
- **agent/**: a static Go binary. It enrolls once with a one-time join token,
  generates its own key and CSR, and gets the CSR signed by the panel CA. After
  that it keeps a single outbound mTLS stream open, sends metrics every 5 s and
  runs jobs. Managed servers need no inbound ports.
- **proto/**: the `agent.v1.AgentService` contract (`Connect` bidi stream).
  Generated code is in `gen/`.
- **web/**: React + TypeScript + Vite + Tailwind v4 with shadcn/ui-style
  components. It is built into `web/dist` and embedded in the panel binary.

## Security model

- Agents authenticate with client certificates issued by the panel's internal
  CA (`data/pki/`). The agent's server ID is the certificate CommonName.
  Deleting a server in the UI makes the panel refuse that agent.
- The install command carries the CA's SHA-256 fingerprint. The agent refuses
  to enroll if the CA it receives doesn't match.
- Join tokens are single-use, expire after 1 hour and are stored only as
  SHA-256 hashes.
- The admin password is hashed with argon2id. Sessions use an HttpOnly,
  SameSite=Strict cookie, and mutating API calls also require an
  `X-Requested-With` header. Failed logins and enrollments are rate-limited
  per IP.
- TLS 1.3 only on the gRPC port.

## Running on a dev server (Debian 13 / Ubuntu)

Requirements: Go 1.24+ (the toolchain auto-downloads 1.26 because of gRPC),
Node 20.19+, PostgreSQL, and protoc (only needed to regenerate `gen/`).

```sh
./scripts/setup-db.sh 10.0.1.240   # creates DB + role, writes .env (secrets stay in .env)
make web panel agent               # UI, panel, agent binaries into bin/
make run                           # or use the systemd unit below
```

On first start the panel creates the admin user from `SM_ADMIN_USER` /
`SM_ADMIN_PASSWORD` in `.env`. After that you can remove `SM_ADMIN_PASSWORD`
from `.env`.

Systemd unit used on the dev server (`/etc/systemd/system/servermgr-panel.service`):

```ini
[Service]
User=jackson
WorkingDirectory=/home/jackson/servermgr
EnvironmentFile=/home/jackson/servermgr/.env
ExecStart=/home/jackson/servermgr/bin/panel
Restart=on-failure
```

### Adding a server

In the UI, click **Add server**. That gives a one-line command like:

```sh
curl -fsSL http://10.0.1.240:8090/install.sh | sudo sh -s -- --token <one-time> --ca-sha256 <fingerprint>
```

It downloads the agent, enrolls it and installs `servermgr-agent.service`.

### Test servers with Incus

```sh
sudo incus launch images:debian/13 sm-node1 -c limits.memory=512MiB
sudo incus exec sm-node1 -- sh -c 'apt-get update && apt-get install -y curl'
```

On hosts that also run Docker, Docker sets the iptables `FORWARD` policy to
`DROP`, which cuts Incus containers off from the internet. Allow the bridge in
`DOCKER-USER`:

```sh
sudo iptables -I DOCKER-USER -i incusbr0 -j ACCEPT
sudo iptables -I DOCKER-USER -o incusbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
```

## Features

Per server (tabs on the server page):

| Tab       | What it does                                                                                     |
| --------- | ------------------------------------------------------------------------------------------------ |
| Overview  | Live CPU / memory / disk / load, uptime                                                          |
| Sites     | nginx + PHP-FPM install; static, PHP (per-site version) and reverse-proxy sites; Let's Encrypt / self-signed TLS |
| Databases | MariaDB install and hardening; databases, users, grants, password resets                         |
| Files     | File manager confined to `/srv/sites` (edit, upload ≤ 2 MiB, download, rename, chmod)             |
| Cron      | Managed `/etc/cron.d` jobs with per-job output and run-now                                       |
| Backups   | Per-site files + database dumps, schedule, retention, restore                                    |
| Updates   | apt check / upgrade (all, selected, full-upgrade), reboot                                        |
| SSH keys  | authorized_keys per user                                                                         |
| Firewall  | UFW status, enable (SSH is always allowed first), rules                                          |
| Fail2ban  | Install with an sshd jail, settings, ban / unban                                                 |
| Tasks     | Every job run on the server, with logs                                                           |

Panel-wide: **Activity** (audit log + task history), **Progress** (build
progress from `progress.json`), **Account** (TOTP 2FA, recovery codes, password).

### Hosting layout on managed servers

```
/srv/sites/<domain>/{public,logs,tmp}        owner s_<domain>:www-data, public is setgid
/etc/nginx/sites-available/servermgr-<domain>.conf
/etc/php/<ver>/fpm/pool.d/servermgr-<domain>.conf   per-site pool and Linux user
/etc/servermgr-agent/sites/<domain>.json      site registry (source of truth)
/etc/servermgr-agent/certs/<domain>/          TLS certificates
/var/backups/servermgr/<domain>/              backups
/etc/cron.d/servermgr-<id>, /var/log/servermgr-cron/<id>.log
```

Every nginx change is checked with `nginx -t` and rolled back if it fails;
reloads are graceful.

### Secrets

- Database passwords are generated in the browser and forwarded to the agent,
  but stored as `[redacted]` in job history and the audit log. File contents
  are stored only as their size.
- TOTP secrets are AES-GCM encrypted with `data/secret.key`.

## Development

```sh
make web panel agent             # build everything
scripts/deploy-agents.sh         # push the agent into the Incus test containers
go test ./agent/... ./panel/...  # unit tests
scripts/check-nginx.sh           # validate every generated vhost with real nginx -t (docker)
scripts/check-mariadb.sh         # DB + backup integration tests against MariaDB (docker)
```

The panel serves the UI from `SM_WEB_DIR` when set, so `npm run build` in
`web/` goes live without a restart.

### HTTPS for the UI

The panel also serves the UI on `https://<panel>:8443` with a certificate
signed by its internal CA. Download the CA from `http://<panel>:8090/ca.crt`
and import it as a trusted root to avoid browser warnings; over HTTPS the
session cookie is marked `Secure`. Plain HTTP on :8090 stays available for
agent installs (the install command pins the CA fingerprint).
`SM_HTTPS_ADDR=off` in `.env` disables the HTTPS listener.
