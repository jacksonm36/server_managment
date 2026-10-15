# servermgr: handoff for the next Claude Code session

Written 2026-10-15. Read this first, then `README.md` and `docs/` in the repo.

## Where things are

| What | Where |
|---|---|
| Dev server | `jackson@10.0.1.240` (SSH key auth from jackson's Windows PC; keys come from the Bitwarden SSH agent, which must be unlocked) |
| Checkout | `~/servermgr` on the dev server. Git remote `github` = `git@github.com:jacksonm36/server_managment.git`; `gh` is logged in as jacksonm36 |
| Live panel | `servermgr-panel.service`; UI on http://10.0.1.240:8090 (HTTPS :8443), gRPC :9443 |
| Panel config | `~/servermgr/.env`. It holds the DB URL and admin bootstrap password: **never print it** |
| Proxmox | jackson's real cluster `proxmox2` = https://10.0.1.150:8006 (API token stored sealed in the panel DB) |
| DB backup before PR #2 | `~/backups/servermgr-pre-pr2-202610151544.dump` |

Do not touch: `~/Proxmox-uiv2`, `~/serverman`, the cloudmanager services, the `pf2opn` container. Never push or open PRs without jackson saying so.

## Branches: decision pending

- **PR #2** (https://github.com/jacksonm36/server_managment/pull/2), branch `claude/project-thread-u4wrbh`, head **4e4faef**. Another Claude session added 6 commits after 3b285b1:
  - guest hardware editing (`pvehardware.go`, `pveactions.go`, `ProxmoxGuestHardware.tsx`)
  - Storage page (`pvestorage.go`, `ProxmoxStorage.tsx`)
  - compute tree and node pages (`PVENodeView.tsx`, plus its own `pvenode.go`)
  - flavors and deploy from the image catalog
  - Terraform/OpenTofu for Proxmox (`tofupve.go`, `tofustacks.go`)

  **None of these 6 commits is deployed.**
- **`claude/pve-full-manage`**, commit **2f698f2**, built on 3b285b1. This is what the live panel runs. It contains:
  - Guests: raw-key hardware editor (`pvehw.go`, `ProxmoxHardware.tsx`), clone/migrate/template, Create VM (ISO) / Create CT.
  - Nodes: summary, shell and `upgrade` via termproxy, services, updates, network (staged, then apply/revert), disks (SMART, create ZFS/LVM/thin/dir, wipe/initgpt with typed confirm), DNS/time/hosts, syslog, tasks, power, bulk start/stop/migrate (`pvenode.go`).
  - Storage: contents, streamed upload (`proxmox.Client.Upload`), download-url, delete.
  - Datacenter: generic collections (`pvedc.go`, `ProxmoxAdmin.tsx`).
  - Client API in `web/src/lib/pveapi.ts`; tests in `pvemanage_db_test.go`; `scripts/mock-pve.py` extended.

**The two branches overlap and conflict**, starting with `pvenode.go`, which exists on both. I asked jackson to choose one of these:

1. **Merge (recommended):** keep the PR's versions where both did the same thing (hardware tab, clone/migrate, storage page, node pages). Port only what the PR lacks: the Datacenter section, node services/updates/network/disks/syslog/tasks, node shell and upgrade, Create VM/CT, ISO and template upload/download, power and bulk actions. Then test, push to PR #2 and deploy.
2. Keep both, with `claude/pve-full-manage` as a separate PR.
3. Prefer `claude/pve-full-manage` for the overlapping parts.

Wait for his answer (or ask) before merging.

Also on the dev server: `git stash list` holds jackson's uncommitted orange-theme tweaks (`App.tsx`, `index.css`) from before PR #2. Keep them; don't drop.

## Build, test, deploy (on the dev server)

```sh
cd ~/servermgr
scripts/deploy-panel.sh          # builds web + panel + agents, restarts the service, health-checks
go clean -cache                  # after Go builds: the disk is small (20G)
cd web && npx tsc -b             # type-check the UI
```

For DB-backed Go tests, Docker needs `sudo -n`:

```sh
sudo -n docker run --rm -d --name smtest-pg -e POSTGRES_USER=sm -e POSTGRES_PASSWORD=sm -e POSTGRES_DB=smtest -p 127.0.0.1:55432:5432 postgres:16-alpine
SM_TEST_DATABASE_URL=postgres://sm:sm@127.0.0.1:55432/smtest go test ./panel/... -count=1 -p 1
sudo -n docker stop smtest-pg
```

For UI testing without touching the live panel, run a throwaway panel:

```sh
SM_DATABASE_URL=postgres://sm:sm@127.0.0.1:55432/smui SM_HTTP_ADDR=127.0.0.1:18190 SM_HTTPS_ADDR=off \
SM_GRPC_ADDR=127.0.0.1:18191 SM_DATA_DIR=/tmp/smui/data SM_WEB_DIR=$PWD/web/dist \
SM_ADMIN_USER=admin SM_ADMIN_PASSWORD=<random test value> /tmp/smui/panel
python3 scripts/mock-pve.py --port 18007 --token 'root@pam!servermgr=<random>' --certdir /tmp/smui/mockcert
```

- Create the `smui` database in the test container first.
- From the Windows PC, run `ssh -N -L 18190:127.0.0.1:18190 jackson@10.0.1.240` and open http://localhost:18190 in the built-in browser.
- Clean everything up afterwards.

## Gotchas

- Never put `pkill -f <pattern>` in an ssh command line that also contains the pattern text: it kills your own ssh shell (exit 127). Use a script file or a `[.]` regex trick.
- Write commit messages to a file and use `git commit -F`.
- Git Bash on Windows rewrites `/tmp/...` paths in ssh arguments. Quote the whole remote command.
- The auto-mode permission check blocks some remote writes and DB credential reads; if blocked, tell jackson exactly what to run.
- Proxmox API facts that have tripped things up:
  - `/nodes/{n}/hosts` is POST (dns and time are PUT).
  - Disk create params are `device` (lvm, lvmthin, dir) and `devices` (zfs).
  - HA groups exist on PVE 8; HA rules on PVE 9.
  - The node shell may be refused for API tokens. This is untested on the real cluster.
- Nothing is verified against the real `proxmox2` yet; jackson can't share his panel login with Claude.

## Housekeeping

- The test Incus nodes sm-node1/2/3 were deleted on 2026-10-15 at jackson's request. The panel has 0 agent servers now.
- An old Proxmox simulator may still run on 127.0.0.1:18006 (`scripts/mock-pve.py`). It is unused, and jackson may want it stopped.
- jackson asked for the panel admin to be reset to admin/admin; Claude gave him the command (argon2id hash written to `admins`, with `must_change_password=true`). Whether he ran the corrected version is unknown. He has been warned that this is weak.
