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

### Run it as a service

```sh
scripts/deploy-panel.sh          # or: make deploy
scripts/deploy-panel.sh --pull   # pull the latest code first
```

Run it as the user that owns the checkout. The first run creates the
database and `.env`, builds everything, installs and enables
`servermgr-panel.service` and checks that the panel answers. Later runs
rebuild and restart it. An existing unit file is left as it is.

The unit it installs looks like the one on the dev server
(`/etc/systemd/system/servermgr-panel.service`):

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
curl -fsSLk --pinnedpubkey 'sha256//<panel key pin>' https://10.0.1.240:8443/install.sh | sudo sh -s -- --token <one-time> --ca-sha256 <fingerprint>
```

It downloads the agent, enrolls it and installs `servermgr-agent.service`.
Everything goes over HTTPS: curl trusts only the panel's own key (the pin),
the agent trusts only the panel CA named by `--ca-sha256`, and plain HTTP
from other machines is redirected. The panel keeps its HTTPS key in
`pki/server.key`, so the pin survives restarts.
The same script is in the repo as [`scripts/install-agent.sh`](scripts/install-agent.sh):

```sh
sudo sh scripts/install-agent.sh --panel https://10.0.1.240:8443 --pin 'sha256//<pin>' --token <one-time> --ca-sha256 <fingerprint>
sudo sh scripts/install-agent.sh --panel https://10.0.1.240:8443 --pin 'sha256//<pin>'   # update an installed agent
sudo sh scripts/install-agent.sh --uninstall
```

### Test servers with Incus

```sh
sudo incus launch images:debian/13 sm-node1 -c limits.memory=512MiB </dev/null
sudo incus exec sm-node1 -- sh -c 'apt-get update && apt-get install -y curl'
```

When scripting (e.g. over non-interactive SSH), close stdin as above:
`incus launch`/`incus init` read extra instance config from stdin when it is
not a terminal and otherwise wait forever without any output.

To run Docker inside a test container, enable nesting first:
`sudo incus config set sm-node2 security.nesting=true && sudo incus restart sm-node2`.

On hosts that also run Docker, Docker sets the iptables `FORWARD` policy to
`DROP`, which cuts Incus containers off from the internet. Allow the bridge in
`DOCKER-USER`:

```sh
sudo iptables -I DOCKER-USER -i incusbr0 -j ACCEPT
sudo iptables -I DOCKER-USER -o incusbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
```

## Supported servers

| Family | Tested on | Packages | Firewall | PHP | Notes |
| ------ | --------- | -------- | -------- | --- | ----- |
| Debian / Ubuntu | Debian 13 | apt | UFW | Debian packages + packages.sury.org | |
| RHEL family | AlmaLinux 9 (Rocky / RHEL 9-10 use the same code path) | dnf (+ EPEL) | firewalld | Remi (php84-php-fpm, ...) | nginx from AppStream; before nginx 1.25.1 the old `listen ... http2` syntax is used automatically. With SELinux enforcing, `/srv/sites` is labelled for nginx/php-fpm and nginx may proxy to local apps. |

Fail2ban bans through nftables on both families. Docker on RHEL comes from Docker's own repository.
The panel refuses to delete the firewall rule that keeps SSH reachable.

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

Panel-wide: **Activity** (audit log + task history), **Users & access**
(see below), **Account** (TOTP 2FA, recovery codes, password).

### Users & access

- **Roles** are sets of permissions. Built-in: `admin` (everything),
  `operator` (all server and automation work, but not users, panel settings,
  adding/removing servers or the SSH key library), `viewer` (read-only) and
  `none`. Make your own under Users & access → Roles; the permission list is
  in `panel/internal/api/access.go`.
- **Server groups** group servers (by customer, environment…). Filter the
  server list by group, pick a whole group in bulk actions, and point
  schedules at a group (they follow its current members).
- **User groups** give a team the same access.
- Every user has a **main role** on all servers. **Extra access** gives a
  role to a user or user group, either on all servers or on one server group.
  On a server group only the role's `server.*` permissions apply, so someone
  with `none` + "Web ops on Web servers" sees and works on those servers only.
- Admins can set a temporary password that must be changed at first sign-in,
  sign a user out everywhere and reset their 2FA. The panel refuses to lose
  its last enabled admin.
- Every API route and agent op is checked server-side; lists, live events,
  jobs and alerts only show servers the user may see.

### App store

Apps tab on a server: search and filter by category, then **Install** creates
the site (and database), installs the app and shows the generated passwords
once.

- **Built-in apps** (`panel/internal/catalog/apps/*.yaml`): WordPress, Joomla,
  Drupal, Nextcloud and the database tools are installed fully from the
  command line. About 20 Docker apps (Uptime Kuma, Gitea, n8n, Grafana,
  Portainer, Paperless-ngx, Wiki.js, Matomo, Metabase, pgAdmin, code-server,
  Jellyfin...) run with Docker Compose on a loopback port behind an nginx
  proxy site. Images are pinned to versions.
- **Installer apps** (`panel/internal/catalog/sources.yaml`, Softaculous
  style): the panel looks up each app's latest release, downloads it, pins
  its sha256 and checks it is ready to run (no `composer install` needed).
  It does this a minute after the first start and then weekly; admins can
  run it from the store ("Refresh now") and see why an app was left out.
  Installing creates the site and database and unpacks the verified
  release; you finish in the app's own web installer. Set
  `SM_GITHUB_TOKEN` (any read-only token) if the refresh hits GitHub's
  anonymous rate limit.
- **Apps written for Apache** (DokuWiki, Grav, FreshRSS, Kanboard,
  PrivateBin): nginx ignores `.htaccess`, so each of these lists the folders
  and files nginx must refuse (`deny:`), or names its own public folder
  (`public:`) so only that folder is served. PHP sites also pass
  `PATH_INFO` (`/index.php/some/path`).

### SSH key library

**Keys** in the top bar. Drop `.pub` files (and optionally your
`~/.ssh/config`) and each key is recognised: type, size, SHA256 fingerprint,
warnings for weak keys, and private keys are refused. Names are filled in from
the ssh config (the host that uses the key, or "<user> main" for a key used by
many hosts), else a unique comment, else the file name. Then:

- **Deploy** a key to any servers or a whole server group, for any login user,
  in one click (or **Add** on a server's SSH keys tab). **Remove** takes it off
  again.
- **Check servers** reads every login user's `authorized_keys` on all online
  servers, shows where each key is installed and lists keys that aren't in
  the library.

### Secrets vault

The key library, Proxmox API tokens, OpenTofu variables and state, and
notification settings are sealed by the vault: a random data key that only
lives in the panel's memory, stored wrapped by your passphrase (argon2id) and
by a recovery key shown once at setup. A copy of the disk and the database is
not enough to read them. Setting up the vault moves existing secrets into it.

After every panel restart the vault is locked: a banner asks admins to unlock
it, and until then Proxmox, OpenTofu, notifications and key deploys wait.
2FA secrets are not in the vault, so you can always sign in to unlock it.
Lose both the passphrase and the recovery key and those secrets are gone
(re-enter them).

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
SM_TEST_DATABASE_URL=postgres://sm:sm@localhost/smtest go test ./panel/...  # + access tests (wipes that DB)
scripts/check-nginx.sh           # validate every generated vhost with real nginx -t (docker)
scripts/check-mariadb.sh         # DB + backup integration tests against MariaDB (docker)
```

The panel serves the UI from `SM_WEB_DIR` when set, so `npm run build` in
`web/` goes live without a restart.

### HTTPS for the UI

The panel also serves the UI on `https://<panel>:8443` with a certificate
signed by its internal CA. Download the CA from `https://<panel>:8443/ca.crt`
(accept the warning once) and import it as a trusted root to avoid browser
warnings; over HTTPS the session cookie is marked `Secure`. Plain HTTP on
:8090 redirects everyone to HTTPS, agents included; only callers on the panel
host itself (health checks, OpenTofu's state backend) stay on HTTP.
`SM_HTTPS_REDIRECT=off` turns the redirect off.
`SM_HTTPS_ADDR=off` in `.env` disables the HTTPS listener.
