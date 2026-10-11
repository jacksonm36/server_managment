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

## In progress (2026-10-11)

### Batch 1: 21 Docker apps

Templates written and image tags checked on Docker Hub / ghcr. Store got search and category chips.

- **Apps:** actual, changedetection, code-server, dozzle, gitea, gotify, grafana, it-tools, jellyfin, matomo, metabase, n8n, navidrome, ntfy, paperless-ngx, pgadmin, portainer, stirling-pdf, uptime-kuma, vikunja, wikijs.
- **Remaining:**
  1. Test-install each through the panel on a sandbox with Docker and check the site answers.
  2. Watch Vikunja's volume permissions, Matomo behind the HTTPS proxy, and n8n's secure cookie.
  3. Then merge to main.

### Batch 2: "all Softaculous apps" (owner's request), mostly PHP

The cloud sandbox cannot download from GitHub release assets or project sites (network policy), so checksums cannot be pinned from there.

**Agreed design:** the panel refreshes the catalog itself, since the panel host has internet.

1. **Sources list:** add `panel/internal/catalog/sources.yaml` with per-app metadata (id, name, Softaculous category, description, homepage, php_min, database, installer_path, notes) and a source:
   - `github: owner/repo` + `asset` regex (latest non-prerelease release asset); or
   - a fixed download URL (follow redirects; the final URL must end in a known archive suffix).
2. **Admin action** "Refresh catalog" (plus a weekly schedule) runs these steps per source:
   1. resolve the latest version;
   2. download it (size cap ~300 MB);
   3. compute the sha256;
   4. inspect the archive to find the web root: the shallowest dir with `index.php`. If that dir is `public/` next to `vendor/` or `artisan`, use `into: "."` with the matching `strip`;
   5. reject archives with `composer.json` but no `vendor/`.
3. **Storage:** results go in a DB table (new migration). The catalog merges embedded templates with refreshed ones. Failures show per app with a reason.
4. **Install:** generated apps are "wizard" installs: `site.create` + `db.create` + `fetch`. The credentials dialog shows the DB name, user and password and the installer URL.
5. **Candidate apps:**
   - **Blogs:** Serendipity, Dotclear, b2evolution, Textpattern, Chyrp Lite, HTMLy, FlatPress, Bludit, PluXml.
   - **Micro blogs:** Friendica, Shaarli.
   - **CMS:** Concrete CMS, Backdrop, TYPO3, ProcessWire, MODX, Grav, e107, XOOPS, PHPFusion, CMS Made Simple, ImpressCMS, Microweber, WonderCMS, GetSimple CE, phpwcms, Geeklog.
   - **Wikis:** MediaWiki, DokuWiki, Tiki, PmWiki, WikkaWiki, WackoWiki, YesWiki.
   - **Galleries:** Piwigo, Zenphoto, Coppermine, Lychee.
   - **Forums:** phpBB, SMF, MyBB, FluxBB/PunBB, Flarum (needs vendor), Vanilla.
   - **E-commerce / ERP:** PrestaShop, OpenCart, Magento (heavy), Dolibarr, FrontAccounting, Invoice Ninja, Akaunting, Crater, FOSSBilling, Bagisto.
   - **Project management and support:** Kanboard, Leantime, Taiga (no), osTicket, FreeScout, UVdesk, LimeSurvey, Moodle, Chamilo.
   - **Files and RSS:** FileGator, Tiny File Manager, YOURLS, Polr, PrivateBin, FreshRSS, Tiny Tiny RSS, Selfoss.
   - **Mail:** Roundcube, phpList, Mautic.
   - **Other:** Matomo (PHP), Snipe-IT, phpIPAM, Monica, Firefly III, Shlink, BookStack.

## Owner preferences

- Wants frequent, concrete progress updates.
- Wants a modern, polished UI and performance.
- No Bitwarden integration.
- Never put credentials, tokens or personal data in the repository or chat.
- Ask before touching real servers beyond deploying this app.
