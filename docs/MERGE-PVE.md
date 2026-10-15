# Handoff: merge `claude/pve-full-manage` into PR #2

Goal: one codebase with every Proxmox feature. PR #2 (branch
`claude/project-thread-u4wrbh`) is the base. From `claude/pve-full-manage`
(commit 2f698f2, built on 3b285b1) bring over only what PR #2 does not have
yet. Where both built the same thing, keep PR #2's version.

## Branch state (2026-10-15)

| Branch | Head | Built on | Contents |
|---|---|---|---|
| `claude/project-thread-u4wrbh` (PR #2) | 4e4faef | 3b285b1 + 6 commits | hardware tab, clone/migrate/template, tags/notes, Storage page (storages CRUD, content, download-url, disks + SMART + wipe, ZFS), flavors + one-step Deploy, compute tree + node page (status/RRD/tasks), Terraform for Proxmox |
| `claude/pve-full-manage` | 2f698f2 | 3b285b1 | node management, Datacenter, create VM/CT, upload, its own hardware tab and storage view |

The dev server currently runs 2f698f2 from `~/servermgr` (branch
`claude/pve-full-manage`, clean; jackson's stashed orange-theme edits are in
`git stash`, leave them).

## What to keep from each

Keep from PR #2 (drop the pve-full-manage duplicates):
- Guest hardware: `pvehardware.go`, `pveactions.go`, `web/src/pages/ProxmoxGuestHardware.tsx`.
  Drop `pvehw.go`'s handlePVEGuestConfig / Resize / MoveDisk / Clone / Migrate / Template
  and `web/src/pages/ProxmoxHardware.tsx`, and their routes
  (`PUT .../config`, `.../resize`, `.../move-disk`, and the second `.../clone|migrate|template`).
  One exception: if PR #2 lacks "move disk to another storage", port that handler only.
- Storage page: `pvestorage.go`, `web/src/pages/ProxmoxStorage.tsx`.
- Node status/RRD/tasks: PR #2's `pvenode.go` (handlePVENodeStatus / RRD / Tasks / handlePVENodeTask).
- Everything Terraform, flavors, Deploy, compute tree.

Bring over from pve-full-manage:
- `pvedc.go` whole (Datacenter: cluster status/log/options, backup jobs,
  replication, HA, pools, users, groups, roles, ACL, realms, firewall,
  SDN + apply, storage definitions, metric servers) and its routes under
  `/api/v1/proxmox/{id}/dc/...` and `/sdn/apply`.
- From its `pvenode.go`: node view/set (services, apt/updates, network,
  DNS, time, hosts), syslog, task log + stop, node power, service actions,
  apt refresh, bulk start/stop/migrate, network create/update/delete/apply,
  node disk actions (LVM, LVM-thin, directory, init GPT; PR #2 already has
  wipe and ZFS, keep those), storage status, storage upload, node shell.
- From its `pvehw.go`: handlePVECreateGuest (Create VM from ISO, Create CT)
  and handlePVEChoices, plus helpers they use (checkGuestValue, pveForm,
  auditForm, pveRun, nextVMID if not already present).
- `client.go`: `OpenNodeShell`, `Upload`, `ConsoleProxy.Base` and the
  `DialConsole` change; `GuestConsole.tsx` changes for node shells.
- `web/src/lib/api.ts`: `request` exported; `web/src/lib/pveapi.ts` whole.
- `web/src/pages/ProxmoxAdmin.tsx`: Nodes and Datacenter sections. Fit them
  into PR #2's layout: node tabs (Services, Updates, Network, Disks, DNS/time/hosts,
  System log, Tasks, Shell, Upgrade, power/bulk) go into PR #2's node view
  (`components/PVENodeView.tsx`, reached from the compute tree); Datacenter
  becomes its own page/sidebar entry next to Storage (proxmox.manage only).
  Drop ProxmoxAdmin's own Storage section (PR #2's page wins) but add its
  upload-with-progress to PR #2's content browser.
- Create VM / Create CT buttons on PR #2's guest list (Proxmox.tsx), next to
  Deploy / New VM.
- `scripts/mock-pve.py` additions and `pvemanage_db_test.go`.
- access.go: the longer `proxmox.manage` description.

## Name collisions to resolve

Both branches define these in package `api`; keep one and adapt callers:
- file `pvenode.go` (rename the incoming one, e.g. `pvenodeadmin.go`)
- `nodePath` (identical idea; keep one)
- `handlePVENodeRRD`, `handlePVENodeTasks` (keep PR #2's; PR #2 serves them
  with proxmox.view and 404s tenants via pveAdminNode)
- `handlePVEStorageContent`, `handlePVEStorageDelete` (keep PR #2's; note
  PR #2's `handlePVEStorageDelete` deletes a storage definition and
  `handlePVEVolumeDelete` deletes a volume)
- `handlePVEStorageDownload` vs PR #2's `handlePVEDownloadURL` (keep PR #2's)
- incoming `pveNodeScope` vs PR #2's `pveAdminNode`: use `pveAdminNode`
  (rejects tenants with 404) for every node and datacenter route.
- `fakePVE` config: incoming change accepts POST as well as PUT on
  `.../config`; keep it.

## Rules that must hold after the merge

- Tenants never reach node, storage, datacenter, Terraform or shell routes
  (404). `proxmox.manage` is never a tenant permission. Run
  `TestTenantIsolation` and the incoming `pvemanage_db_test.go`.
- Terraform stays platform-admins only.
- Every write is audit-logged with secrets redacted.
- Don't add credentials, tokens, hostnames or personal data to the repo (it is public).
- Format the web code at the repo's style (print width about 160). Don't run
  prettier over whole files; the default config reformats everything.
- Commit trailer and PR rules: see the repo's existing commits on PR #2.

## Testing

```sh
# Postgres 16 running; DB smtest is wiped by the tests
cd panel && gofmt -l . && go vet ./... && \
  SM_TEST_DATABASE_URL=postgres://sm:sm@localhost/smtest go test ./...
cd ../web && npx tsc -b && npx vite build && git checkout dist/.gitkeep
rm -rf scripts/__pycache__
```

UI check against the simulator: `python3 scripts/mock-pve.py --port 18006
--token 'root@pam!servermgr=MOCKSECRET123'`, start the panel with
`SM_DATABASE_URL`, `SM_ADMIN_PASSWORD`, `SM_HTTP_ADDR`, `SM_GRPC_ADDR`,
`SM_DATA_DIR`, add the mock as a cluster, and click through: guest list
(Deploy, New VM, Create VM, Create CT), guest page tabs, compute tree → node
(all tabs, shell), Storage page (upload, download, ZFS), Datacenter, and
Automation → OpenTofu → From a Proxmox stack.

OpenTofu runs need `bubblewrap` on the panel host.

## Deploy (after it's merged, tested and pushed to PR #2)

On the dev server as jackson, in `~/servermgr`:
1. Back up the DB: `pg_dump -Fc` to `~/backups/servermgr-pre-<commit>-<date>.dump`.
2. `git fetch github && git checkout pr2-deploy && git reset --hard github/claude/project-thread-u4wrbh`.
3. `scripts/deploy-panel.sh` and check the health check passes. Migrations 016 and 017 run on start.
4. `command -v bwrap` (needed for OpenTofu runs).

Don't touch the separate CloudManager install or its services.
