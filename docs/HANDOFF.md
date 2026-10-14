# servermgr: handoff notes for the next developer (human or AI)

Read this first, then `README.md`. It says what exists, how to build and test
it, conventions, and what is in progress.

## What this is

A self-hosted server control panel (in the style of CloudPanel/aaPanel/Softaculous):

- **panel/**: Go HTTP API and web UI host, PostgreSQL (pgx v5), gRPC server for agents.
- **agent/**: Go agent on each managed server. It dials out to the panel over gRPC with mTLS and runs typed ops (no free-form shell from the panel).
- **web/**: React 19 + TypeScript + Vite + Tailwind v4 + wouter. Built into `web/dist` and embedded in the panel binary.
- **proto/** and **gen/**: the agent protocol.
- **Target distros:** Ubuntu 24.04, Debian 12/13, RHEL/Alma 9.

## Build, run and deploy

```sh
make all                       # UI + panel + agents into bin/
scripts/deploy-panel.sh --pull # on the panel host: pull, build, (re)start the systemd service
scripts/install-agent.sh       # agent installer (the same file is served as /install.sh)
```

- **Panel configuration:** env vars in `.env`: `SM_DATABASE_URL`, `SM_HTTP_ADDR`, `SM_HTTPS_ADDR`, `SM_GRPC_ADDR`, `SM_PUBLIC_HOST`, `SM_DATA_DIR`, `SM_ADMIN_PASSWORD`, `SM_AGENT_BIN_DIR`.
- **Never commit:** `.env`, `data/`, keys or certs. The repository is public.

## Tests

```sh
go vet ./panel/... ./agent/...
go test ./agent/... ./panel/...
SM_TEST_DATABASE_URL=postgres://sm:sm@localhost/smtest go test ./panel/...   # DB tests (wipe that DB)
cd web && npx tsc -b && npm run build   # then: git checkout web/dist/.gitkeep
```

- **No formatter in the web project:** don't run prettier over existing files. The code uses long lines (~150 characters).
- **Go:** gofmt.

### End-to-end in a sandbox (panel, real agent, browser)

1. Start Postgres. Run the panel with `SM_HTTPS_ADDR=off` on 127.0.0.1.
2. Create a join token: POST `/api/v1/join-tokens` with header `X-Requested-With: servermgr`. Mutating requests need that header.
3. Run `agent enroll --panel URL --token T --ca-sha256 H --state-dir DIR`, then `agent run --state-dir DIR`.
4. Run a job with POST `/api/v1/servers/{id}/jobs` `{"op": "...", "params": {...}}` and poll GET `/api/v1/jobs/{id}`.
5. Browser checks use Playwright. Log in through `#username` / `#password`.

## Key code map

| Area | Where |
| --- | --- |
| Routes | `panel/internal/api/api.go` |
| Permissions (RBAC) catalog, `need`/`needOn` | `panel/internal/api/access.go` |
| Job dispatch (`API.Dispatch`), op → permission | `panel/internal/api/dispatch.go`, `opPerm` in access.go |
| Secrets vault (data key in memory, passphrase + recovery key) | `panel/internal/vault`, `panel/internal/api/vault.go` |
| SSH key library | `panel/internal/api/sshkeys.go`, `web/src/pages/Keys.tsx` |
| DB migrations (embedded, numbered) | `panel/internal/store/migrations/` |
| Agent ops | `agent/internal/ops/*.go` (`ops.Run` switch in ops.go) |
| Live job console + jobs tray (lower right corner) | `web/src/components/JobResults.tsx`, `JobsDock.tsx`; `GET /api/v1/jobs?active=1` |
| Bulk actions catalog (UI) | `web/src/lib/catalog.ts` |
| Tenants, Proxmox isolation setup | `panel/internal/api/tenants.go`, `web/src/pages/Tenants.tsx` |
| Proxmox routes, per-tenant scope (`pveScope`, `pveGuest`) | `panel/internal/api/proxmox.go` |
| Proxmox guest detail, RRD, tasks | `panel/internal/api/pveguest.go`, `web/src/pages/ProxmoxGuest.tsx`, `web/src/components/HistoryChart.tsx` |
| Proxmox console relay and window | `panel/internal/api/pveconsole.go`, `web/src/components/GuestConsole.tsx` |

## App store (one-click apps)

- **Templates:** `panel/internal/catalog/apps/*.yaml`, embedded and validated at start-up in `catalog.go`.
- **Install handler:** `panel/internal/api/apps.go`. **UI:** `web/src/pages/server/Apps.tsx`.

### Two kinds of app

1. **`kind: php` (default):** the panel builds a playbook:
   1. `site.create` (nginx + PHP-FPM site in `/srv/sites/<domain>`, web root `public/`);
   2. optionally `db.create` (MariaDB);
   3. `app.deploy` with typed steps run by the agent (`agent/internal/ops/apps.go`):
      - `fetch` (url + **pinned sha256**, `strip`, `into`, optional `as`). Archive type comes from the URL suffix: .zip / .tar.gz / .tgz / .tar.bz2.
      - `php` (CLI args, `${vars}`), `wp`, `write`, `remove`.
   4. optionally `site.tls`.

   Notes:
   - Vars: `site_url`, `title`, `secret_32`, `secret_64`, `db_*`, `admin_*`.
   - PHP-FPM runs with `open_basedir` = site home and exec/system/proc_open disabled.
2. **`kind: compose`:** a Docker compose app published on `127.0.0.1:${port}`, with an nginx proxy site in front. The proxy forwards websockets and allows 64 MB uploads.
   - **Vars:** `port`, `domain`, `site_url`, `secret_16`, `secret_32`, `secret_64`, `title`, `admin_user`, `admin_email`, `admin_password`.
   - Form-supplied values (`title`, `admin_*`) must be inside double quotes. Start-up validation enforces this, and the values are escaped.
   - `admin: true` asks for an admin user and email and generates a password. `login: email` (sign in with the email) and `login: password` (generated password only) change what is asked.

Template fields: id, name, category, description, version, homepage, notes (shown after install), protect (HTTP basic auth on by default), min_ram_mb, needs_docker, admin_path.

## App store status (2026-10-11)

### Batch 1: Docker apps

21 templates in `apps/`. Image tags were checked on Docker Hub / ghcr.

- **Test:** `compose_test.go` checks their shape (pinned images, one loopback port, no stray keys).
- **Installed and answering through the panel on a sandbox:** all 19 Docker Hub apps (everything except paperless-ngx and wikijs).
- **Fixed while testing:**
  - pgAdmin needs `PGADMIN_LISTEN_ADDRESS` on hosts without IPv6.
  - Vikunja needs `user: "0:0"` because named volumes are root-owned.
- **Not testable in the cloud sandbox:** paperless-ngx and wikijs (ghcr blobs are blocked there).

### Batch 2: installer apps (Softaculous style)

Implemented as the in-panel refresher.

- **Code:**
  - `panel/internal/catalog/refresh.go` with `sources.yaml`.
  - `panel/internal/api/catalog_refresh.go`: setting `catalog_pinned`, routes `GET /api/v1/apps/catalog` and `POST /api/v1/apps/catalog/refresh` (settings.manage).
  - The refresh card is in the store.
- **Sources:**
  - GitHub releases (API, falling back to the release pages when rate limited).
  - Fixed URLs (redirect followed and pinned).
  - `scrape` steps over download index pages, used for MediaWiki and phpBB.
- **Checks:**
  - The final URL must be https.
  - The web root is found from the shallowest index.php; a Laravel-style `public/` goes in with `into: "."`.
  - A composer.json without vendor/ is refused unless `allow_no_vendor`.
- **Install:** generated apps are `Wizard` installs. The credentials dialog shows "Finish setup at" (`installer_url`).
- **Tested in the sandbox:**
  - A pinned demo release installed end to end: site, fetch, PHP answered.
  - The new nginx rule denies `.sqlite`/`.sql`/`.env`/`.log`/`.yml`... on PHP sites.
  - A real refresh from the cloud fails every source (network policy), which exercises the "kept last good / error" paths.
- **Real sources:** each entry in sources.yaml has NOT been verified against the live internet. The first refresh on the real panel shows which ones fail and why; fix their `github`/`asset`/`scrape` there.
- **Apps that rely on .htaccess** (added 2026-10-11, batch 3): a source can list `deny:` paths under the web root (`data/` folder, `install.php` file, `user/*.md,twig` by extension; rendered as nginx locations ahead of the PHP location) and/or `public:` (its own public folder: the release goes to `app/` and the site serves `app/<public>`). `protect: true` turns on the site password by default (Kanboard starts with admin/admin). These become a typed `nginx` app step (`agent/internal/ops/apps.go` `applyNginxStep`, `web.go` `denyLocation`); agents older than this fail the step ("step 1 is empty"), so the install rolls back instead of exposing data. Added: DokuWiki, Grav, FreshRSS, Kanboard, PrivateBin. Tested in the sandbox with demo archives of the same layouts (data/ denied with 403, PATH_INFO works, app/p web root). Still left out: PluXml and WonderCMS (public uploads share a folder with private data) and Roundcube (installer needs config).
- **PHP sites pass PATH_INFO** now (`location ~ [^/]\.php(/|$)` with `fastcgi_split_path_info`), needed by FreshRSS's API and others.

### Agent changes (agents need updating)

- nginx sites listen on `[::]` only when the host has IPv6. On hosts without IPv6, the IPv6 listen in the distro default site is commented out.
- PHP sites deny common data-file extensions.
- The `nginx` app step (per-app deny rules and web root) and PATH_INFO for PHP sites.

## Tenants and Proxmox isolation (2026-10-14)

Part of bringing CloudManager (a separate Proxmox manager) into servermgr. Tenants come first so every ported feature is per tenant.

- **Model:** `tenants` (quotas, optional server group), `tenant_clusters` (pool, allowed storages, bridges and templates, sealed Proxmox token), `admins.tenant_id`. Migration 015.
- **Panel side:** `loadAccess` clamps a tenant user's permissions to `tenantPerms` (proxmox view/operate/console/guests, audit.view), whatever their roles say. Server permissions only apply to the tenant's server group. Audit entries and provisioning runs carry `tenant_id`, and tenants only see their own.
- **Proxmox side:** setup creates the pool and the Proxmox user `sm-<slug>@pve` (no password). It grants exactly PVEVMAdmin and PVEPoolAdmin on the pool, PVEDatastoreUser on the storages, PVESDNUser on `/sdn/zones/localnetwork/<bridge>` and PVETemplateUser on the templates. It issues a privsep=0 token, then checks the token sees nothing outside the pool. Tenant requests use that token, and `pveGuest` also checks the pool. Anything outside it is a 404.
- **Not handled:** deleting a whole cluster from servermgr leaves the tenants' Proxmox users on it (remove access per tenant first). Moving a user into or out of a tenant ends their sessions.
- **Guest page** (`/proxmox/<cluster>/<node>/<type>/<vmid>`): live status every 5 s, RRD graphs (CPU, memory, network, disk I/O; hour to year), hardware and options from the config with `cipassword` masked, guest IPs, recent tasks with their logs. All of it goes through `pveGuest`; a task log is only served when the UPID names this guest.
- **Console** (Console button on a running guest, needs `proxmox.console`): `POST …/console` opens vncproxy or termproxy and returns a one-time session id. It is bound to the user who opened it and expires after 30 s. The browser then opens `GET /api/v1/proxmox-console/{sid}`, and the panel relays it to Proxmox's vncwebsocket with the scope's token. VMs default to noVNC (the VNC ticket goes to the browser as its password), or to the serial port when `vga: serialN`. Containers and serial ports use xterm.js; the panel does the termproxy login itself, so that ticket stays server-side. Opening and closing are audit-logged.
- **Tests:** `TestTenantIsolation` runs against an in-memory Proxmox (`pvefake_test.go`) that enforces ACLs. `scripts/mock-pve.py` also mocks pools, users, tokens and ACLs.

## Owner preferences

- Wants frequent, concrete progress updates.
- Wants a modern, polished UI and performance.
- No Bitwarden integration.
- Never put credentials, tokens or personal data in the repository or chat.
- Ask before touching real servers beyond deploying this app.
