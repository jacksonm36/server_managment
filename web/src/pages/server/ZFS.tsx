import { useMemo, useState } from "react";
import { Camera, Download, HardDrive, Pencil, Plus, RefreshCw, Search, Trash2 } from "lucide-react";
import { useAgentState } from "@/lib/ops";
import { useAccess } from "@/lib/access";
import { formatBytes } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input, Label } from "@/components/ui/input";
import { Select, Textarea } from "@/components/ui/select";
import { OpStatus, Offline } from "@/components/OpStatus";

// ZFS on a Linux server (a Proxmox node with the agent included): pools
// with their device tree, datasets and their properties, snapshots.

type Num = number | string;
type Vdev = { name: string; type?: string; state?: string; read?: Num; write?: Num; cksum?: Num; msg?: string; children?: Vdev[] };
type Pool = {
  name: string;
  health: string;
  size: Num;
  alloc: Num;
  free: Num;
  frag?: Num;
  cap?: Num;
  dedup?: Num;
  readonly?: boolean | string;
  scan?: string;
  errors?: string;
  status?: string;
  action?: string;
  vdevs?: Vdev[];
  root?: boolean;
};
type Dataset = {
  name: string;
  type: string;
  used: Num;
  avail: Num;
  refer: Num;
  mountpoint?: string;
  compression?: string;
  compressratio?: number | string;
  recordsize?: Num;
  volsize?: Num;
  quota?: Num;
  reservation?: Num;
  atime?: string;
  sync?: string;
  dedup?: string;
  encryption?: string;
  keystatus?: string;
  readonly?: string;
  mounted?: string | boolean;
  [k: string]: unknown;
};
type Status = {
  installed: boolean;
  pools?: Pool[];
  datasets?: Dataset[];
  importable?: { name: string; id: string; state: string }[];
  arc?: { size: Num; max: Num; hits: Num; misses: Num } | null;
};
type Snapshot = { name: string; dataset: string; used: Num; refer: Num; creation: Num };

const PROPS = [
  "compression",
  "atime",
  "relatime",
  "recordsize",
  "volblocksize",
  "quota",
  "refquota",
  "reservation",
  "refreservation",
  "sync",
  "dedup",
  "xattr",
  "acltype",
  "mountpoint",
  "readonly",
  "canmount",
  "copies",
  "primarycache",
  "secondarycache",
  "logbias",
  "snapdir",
  "com.sun:auto-snapshot",
];

const n = (v: unknown) => Number(v) || 0;
const bytes = (v: unknown) => (v === undefined || v === null || v === "" || v === "-" ? "—" : formatBytes(n(v)));

function healthVariant(h?: string): "success" | "warning" | "destructive" | "outline" {
  if (!h) return "outline";
  if (h === "ONLINE") return "success";
  if (h === "DEGRADED" || h === "OFFLINE" || h === "REMOVED") return "warning";
  return "destructive";
}

export function ZFS({ serverId, online }: { serverId: string; online: boolean }) {
  const { data, busy, error, job, run } = useAgentState<Status>(serverId, "zfs.status", online);
  const { can } = useAccess();
  const canAct = can("server.system", serverId);
  const disabled = !online || !!busy || !canAct;
  const [creating, setCreating] = useState(false);
  const [snapsOf, setSnapsOf] = useState<string | null>(null);

  const arc = data?.arc;
  const hitRatio = arc && n(arc.hits) + n(arc.misses) > 0 ? (100 * n(arc.hits)) / (n(arc.hits) + n(arc.misses)) : null;

  return (
    <div className="grid gap-6">
      <Card>
        <CardHeader className="flex-row flex-wrap items-start justify-between gap-3">
          <div>
            <CardTitle>ZFS</CardTitle>
            <CardDescription>
              {!data
                ? "Loading…"
                : !data.installed
                  ? "ZFS is not installed on this server."
                  : `${data.pools?.length ?? 0} pools · ${data.datasets?.length ?? 0} datasets`}
              {arc && (
                <>
                  {" "}
                  · ARC {formatBytes(n(arc.size))} of {formatBytes(n(arc.max))}
                  {hitRatio !== null && `, ${hitRatio.toFixed(1)} % hits`}
                </>
              )}
            </CardDescription>
          </div>
          <div className="flex items-center gap-2">
            {data?.installed && canAct && (
              <Button size="sm" variant="secondary" disabled={disabled} onClick={() => setCreating(true)}>
                <Plus /> Create pool
              </Button>
            )}
            <Button variant="ghost" size="icon" disabled={!online || !!busy} onClick={() => run("zfs.status")} aria-label="Refresh">
              <RefreshCw />
            </Button>
          </div>
        </CardHeader>
        <CardContent className="grid gap-3">
          {!online && <Offline />}
          <OpStatus busy={busy} error={error} job={job} />
          {data && !data.installed && (
            <p className="text-sm text-muted-foreground">
              Install the ZFS tools (<code>apt install zfsutils-linux</code> on Debian and Ubuntu) in the terminal, then refresh.
            </p>
          )}
          {data?.installed && data.pools?.length === 0 && <p className="text-sm text-muted-foreground">No pools.</p>}
          {(data?.importable?.length ?? 0) > 0 && (
            <div className="grid gap-2 rounded-lg border p-3">
              <div className="text-sm font-medium">Pools that can be imported</div>
              {data!.importable!.map((p) => (
                <div key={p.id || p.name} className="flex flex-wrap items-center justify-between gap-2 text-sm">
                  <span>
                    <span className="font-medium">{p.name}</span> <span className="text-muted-foreground">({p.id})</span>{" "}
                    <Badge variant={healthVariant(p.state)}>{p.state.toLowerCase()}</Badge>
                  </span>
                  {canAct && (
                    <Button
                      size="sm"
                      variant="secondary"
                      disabled={disabled}
                      onClick={() => confirm(`Import pool ${p.name}?`) && void run("zfs.pool", { action: "import", pool: p.name })}
                    >
                      <Download /> Import
                    </Button>
                  )}
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>

      {data?.pools?.map((p) => <PoolCard key={p.name} pool={p} run={run} disabled={disabled} canAct={canAct} />)}

      {data?.installed && (data.datasets?.length ?? 0) > 0 && (
        <Datasets datasets={data.datasets!} pools={data.pools ?? []} run={run} disabled={disabled} canAct={canAct} onSnapshots={setSnapsOf} />
      )}

      {creating && <CreatePool onClose={() => setCreating(false)} run={run} />}
      {snapsOf && <Snapshots serverId={serverId} online={online} dataset={snapsOf} canAct={canAct} onClose={() => setSnapsOf(null)} />}
    </div>
  );
}

type Run = (op: string, p?: Record<string, string>) => Promise<unknown>;

function PoolCard({ pool: p, run, disabled, canAct }: { pool: Pool; run: Run; disabled: boolean; canAct: boolean }) {
  const [adding, setAdding] = useState(false);
  const [replacing, setReplacing] = useState<string | null>(null);
  const cap = p.cap !== undefined && p.cap !== "" ? n(p.cap) : n(p.size) ? (100 * n(p.alloc)) / n(p.size) : 0;
  const scan = p.scan ?? "";
  const scrubbing = /scrub in progress/i.test(scan);
  const paused = /scrub paused/i.test(scan);
  const act = (action: string, ask?: string) => {
    if (ask && !confirm(ask)) return;
    void run("zfs.pool", { action, pool: p.name });
  };

  return (
    <Card>
      <CardHeader className="flex-row flex-wrap items-start justify-between gap-3">
        <div className="grid gap-1">
          <CardTitle className="flex items-center gap-2">
            <HardDrive className="size-4" /> {p.name} <Badge variant={healthVariant(p.health)}>{p.health.toLowerCase()}</Badge>
            {p.root && <Badge variant="outline">system pool</Badge>}
          </CardTitle>
          <CardDescription>
            {formatBytes(n(p.alloc))} of {formatBytes(n(p.size))} used · {formatBytes(n(p.free))} free
            {p.frag !== undefined && p.frag !== "" && ` · ${n(p.frag)} % fragmented`}
            {p.dedup !== undefined && p.dedup !== "" && n(p.dedup) !== 1 && ` · dedup ${n(p.dedup).toFixed(2)}x`}
          </CardDescription>
        </div>
        {canAct && (
          <div className="flex flex-wrap gap-1.5">
            {scrubbing ? (
              <>
                <Button size="sm" variant="secondary" disabled={disabled} onClick={() => act("scrub_pause")}>
                  Pause scrub
                </Button>
                <Button size="sm" variant="secondary" disabled={disabled} onClick={() => act("scrub_stop", `Stop the scrub of ${p.name}?`)}>
                  Stop scrub
                </Button>
              </>
            ) : (
              <Button size="sm" variant="secondary" disabled={disabled} onClick={() => act("scrub")}>
                {paused ? "Resume scrub" : "Scrub"}
              </Button>
            )}
            <Button size="sm" variant="ghost" disabled={disabled} onClick={() => act("trim")}>
              Trim
            </Button>
            <Button size="sm" variant="ghost" disabled={disabled} onClick={() => act("clear")}>
              Clear errors
            </Button>
            <Button
              size="sm"
              variant="ghost"
              disabled={disabled}
              onClick={() => act("upgrade", `Upgrade ${p.name} to all features of this ZFS version? Older ZFS versions may no longer import it.`)}
            >
              Upgrade
            </Button>
            <Button size="sm" variant="ghost" disabled={disabled} onClick={() => setAdding(true)}>
              <Plus /> Add devices
            </Button>
            {!p.root && (
              <>
                <Button size="sm" variant="ghost" disabled={disabled} onClick={() => act("export", `Export ${p.name}? Its datasets are unmounted and VMs using it stop working.`)}>
                  Export
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  className="text-destructive"
                  disabled={disabled}
                  onClick={() => {
                    const x = prompt(`Destroy pool ${p.name}? Every dataset, volume and snapshot on it is lost. Type ${p.name} to confirm.`);
                    if (x) void run("zfs.pool_destroy", { name: p.name, confirm: x });
                  }}
                >
                  <Trash2 /> Destroy
                </Button>
              </>
            )}
          </div>
        )}
      </CardHeader>
      <CardContent className="grid gap-3">
        <div className="h-2 overflow-hidden rounded-full bg-muted">
          <div className={`h-full ${cap >= 90 ? "bg-destructive" : cap >= 80 ? "bg-warning" : "bg-brand"}`} style={{ width: `${Math.min(cap, 100)}%` }} />
        </div>
        <div className="grid gap-1 text-sm">
          {scan && (
            <div>
              <span className="text-muted-foreground">Scan:</span> {scan}
            </div>
          )}
          {p.status && <div className="text-warning">{p.status}</div>}
          {p.action && <div className="text-muted-foreground">{p.action}</div>}
          {p.errors && (
            <div className={/^no known data errors/i.test(p.errors) ? "text-muted-foreground" : "text-destructive"}>
              <span className="text-muted-foreground">Errors:</span> {p.errors}
            </div>
          )}
        </div>
        {(p.vdevs?.length ?? 0) > 0 && (
          <div className="overflow-x-auto rounded-lg border">
            <table className="w-full text-sm">
              <thead className="text-left text-xs text-muted-foreground">
                <tr>
                  <th className="px-3 py-1.5 font-medium">Device</th>
                  <th className="px-3 py-1.5 font-medium">State</th>
                  <th className="px-3 py-1.5 text-right font-medium">Read</th>
                  <th className="px-3 py-1.5 text-right font-medium">Write</th>
                  <th className="px-3 py-1.5 text-right font-medium">Cksum</th>
                  <th className="px-3 py-1.5" />
                </tr>
              </thead>
              <tbody>
                {p.vdevs!.map((v) => (
                  <VdevRows key={v.name} v={v} depth={0} parent={null} section="" pool={p.name} run={run} disabled={disabled} canAct={canAct} onReplace={setReplacing} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </CardContent>
      {adding && <AddDevices pool={p.name} run={run} onClose={() => setAdding(false)} />}
      {replacing && <ReplaceDevice pool={p.name} device={replacing} run={run} onClose={() => setReplacing(null)} />}
    </Card>
  );
}

// Section headers in zpool status ("logs", "cache", "spares", "special",
// "dedup") hold devices that can be removed; so can top-level vdevs.
const SECTIONS = ["logs", "cache", "spares", "special", "dedup"];
const isGroup = (v: Vdev) => /^(mirror|raidz\d?|draid|replacing|spare)(-\d+)?/.test(v.name) || (v.type ?? "").match(/^(mirror|raidz|draid|replacing|spare)/) !== null;

function VdevRows({
  v,
  depth,
  parent,
  section,
  pool,
  run,
  disabled,
  canAct,
  onReplace,
}: {
  v: Vdev;
  depth: number;
  parent: Vdev | null;
  section: string;
  pool: string;
  run: Run;
  disabled: boolean;
  canAct: boolean;
  onReplace: (d: string) => void;
}) {
  const header = depth === 0 && SECTIONS.includes(v.name);
  const leaf = !header && !(v.children?.length) && !isGroup(v);
  const counts = [v.read, v.write, v.cksum];
  const sec = header ? v.name : section;
  const underMirror = !!parent && /^(mirror|replacing)/.test(parent.name);
  // Top level: directly under the pool, or directly under a section header.
  const topLevel = depth === 0 || (depth === 1 && !!section && !parent?.children?.some(isGroup) && SECTIONS.includes(parent?.name ?? ""));
  const removable = !header && (sec !== "" ? depth <= 1 || (parent && SECTIONS.includes(parent.name)) : topLevel);
  const dev = (action: string, ask?: string, extra: Record<string, string> = {}) => {
    if (ask && !confirm(ask)) return;
    void run("zfs.device", { action, pool, device: v.name, ...extra });
  };
  const offline = v.state === "OFFLINE";

  return (
    <>
      <tr className="border-t">
        <td className="px-3 py-1.5" style={{ paddingLeft: `${0.75 + depth * 1.25}rem` }}>
          <span className={header ? "text-xs font-semibold tracking-wider text-muted-foreground uppercase" : leaf ? "font-mono text-xs" : "font-medium"}>{v.name}</span>
          {v.msg && <span className="ml-2 text-xs text-muted-foreground">{v.msg}</span>}
        </td>
        <td className="px-3 py-1.5">{v.state && <Badge variant={healthVariant(v.state)}>{v.state.toLowerCase()}</Badge>}</td>
        {counts.map((c, i) => (
          <td key={i} className={`tabular px-3 py-1.5 text-right ${c !== undefined && c !== "" && c !== "0" && c !== 0 ? "font-medium text-destructive" : "text-muted-foreground"}`}>
            {header || c === undefined ? "" : String(c)}
          </td>
        ))}
        <td className="px-3 py-1.5 text-right whitespace-nowrap">
          {canAct && leaf && (
            <div className="flex justify-end gap-1">
              {offline ? (
                <Button size="sm" variant="ghost" disabled={disabled} onClick={() => dev("online")}>
                  Online
                </Button>
              ) : (
                <Button size="sm" variant="ghost" disabled={disabled} onClick={() => dev("offline", `Take ${v.name} offline? The pool runs without it until it is brought back.`)}>
                  Offline
                </Button>
              )}
              <Button size="sm" variant="ghost" disabled={disabled} onClick={() => onReplace(v.name)}>
                Replace
              </Button>
              {underMirror && (
                <Button size="sm" variant="ghost" disabled={disabled} onClick={() => dev("detach", `Detach ${v.name} from its mirror?`)}>
                  Detach
                </Button>
              )}
              {removable && (
                <Button size="sm" variant="ghost" className="text-destructive" disabled={disabled} onClick={() => dev("remove", `Remove ${v.name} from ${pool}?`)}>
                  Remove
                </Button>
              )}
            </div>
          )}
          {canAct && !leaf && !header && removable && (
            <Button size="sm" variant="ghost" className="text-destructive" disabled={disabled} onClick={() => dev("remove", `Remove ${v.name} from ${pool}? Its data moves to the other devices first.`)}>
              Remove
            </Button>
          )}
        </td>
      </tr>
      {v.children?.map((c) => (
        <VdevRows key={c.name} v={c} depth={depth + 1} parent={v} section={sec} pool={pool} run={run} disabled={disabled} canAct={canAct} onReplace={onReplace} />
      ))}
    </>
  );
}

const lines = (s: string) =>
  s
    .split(/[\n,]+/)
    .map((x) => x.trim())
    .filter(Boolean);

function AddDevices({ pool, run, onClose }: { pool: string; run: Run; onClose: () => void }) {
  const [vdev, setVdev] = useState("");
  const [devs, setDevs] = useState("");
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Add devices to {pool}</DialogTitle>
          <DialogDescription>The devices are formatted for ZFS. A data vdev cannot be taken out again on RAID-Z pools.</DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            const d = lines(devs);
            if (!d.length || !confirm(`Add ${d.join(", ")} to ${pool}? Everything on them is erased.`)) return;
            void run("zfs.device", { action: "add", pool, vdev, devices: d.join(" ") });
            onClose();
          }}
        >
          <Label className="grid gap-1.5">
            As
            <Select value={vdev} onChange={(e) => setVdev(e.target.value)}>
              <option value="">Single disk(s) (stripe)</option>
              <option value="mirror">Mirror</option>
              <option value="raidz">RAID-Z1</option>
              <option value="raidz2">RAID-Z2</option>
              <option value="raidz3">RAID-Z3</option>
              <option value="log">Log (SLOG)</option>
              <option value="cache">Cache (L2ARC)</option>
              <option value="spare">Hot spare</option>
              <option value="special">Special (metadata)</option>
            </Select>
          </Label>
          <Label className="grid gap-1.5">
            Devices, one per line
            <Textarea rows={4} className="font-mono text-xs" placeholder="/dev/disk/by-id/ata-…" value={devs} onChange={(e) => setDevs(e.target.value)} />
          </Label>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={!lines(devs).length}>
              Add
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function ReplaceDevice({ pool, device, run, onClose }: { pool: string; device: string; run: Run; onClose: () => void }) {
  const [nd, setNd] = useState("");
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Replace {device}</DialogTitle>
          <DialogDescription>
            ZFS copies the data onto the new device (resilver). Leave it empty when the failed disk was swapped for a new one in the same slot.
          </DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (!confirm(`Replace ${device} in ${pool}${nd.trim() ? ` with ${nd.trim()}` : ""}?`)) return;
            const p: Record<string, string> = { action: "replace", pool, device };
            if (nd.trim()) p.new_device = nd.trim();
            void run("zfs.device", p);
            onClose();
          }}
        >
          <Label className="grid gap-1.5">
            New device
            <Input className="font-mono text-xs" placeholder="/dev/disk/by-id/…" value={nd} onChange={(e) => setNd(e.target.value)} />
          </Label>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit">Replace</Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function CreatePool({ run, onClose }: { run: Run; onClose: () => void }) {
  const [name, setName] = useState("");
  const [vdev, setVdev] = useState("mirror");
  const [devs, setDevs] = useState("");
  const [ashift, setAshift] = useState("12");
  const [compression, setCompression] = useState("lz4");
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Create a ZFS pool</DialogTitle>
          <DialogDescription>Everything on the listed devices is erased.</DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            const d = lines(devs);
            if (!name.trim() || !d.length) return;
            const x = prompt(`Create pool ${name.trim()} on ${d.join(", ")}? All data on them is erased. Type ${name.trim()} to confirm.`);
            if (x !== name.trim()) return;
            void run("zfs.pool_create", { name: name.trim(), vdev, devices: d.join(" "), ashift, compression });
            onClose();
          }}
        >
          <Label className="grid gap-1.5">
            Name
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="tank" />
          </Label>
          <Label className="grid gap-1.5">
            Layout
            <Select value={vdev} onChange={(e) => setVdev(e.target.value)}>
              <option value="">Stripe (no redundancy)</option>
              <option value="mirror">Mirror</option>
              <option value="raidz">RAID-Z1</option>
              <option value="raidz2">RAID-Z2</option>
              <option value="raidz3">RAID-Z3</option>
            </Select>
          </Label>
          <Label className="grid gap-1.5">
            Devices, one per line
            <Textarea rows={4} className="font-mono text-xs" placeholder="/dev/disk/by-id/ata-…" value={devs} onChange={(e) => setDevs(e.target.value)} />
          </Label>
          <div className="grid grid-cols-2 gap-3">
            <Label className="grid gap-1.5">
              ashift
              <Select value={ashift} onChange={(e) => setAshift(e.target.value)}>
                <option value="9">9 (512 B sectors)</option>
                <option value="12">12 (4K, usual)</option>
                <option value="13">13 (8K)</option>
              </Select>
            </Label>
            <Label className="grid gap-1.5">
              Compression
              <Select value={compression} onChange={(e) => setCompression(e.target.value)}>
                {["lz4", "zstd", "on", "off", "gzip"].map((c) => (
                  <option key={c}>{c}</option>
                ))}
              </Select>
            </Label>
          </div>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={!name.trim() || !lines(devs).length}>
              Create
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function Datasets({
  datasets,
  pools,
  run,
  disabled,
  canAct,
  onSnapshots,
}: {
  datasets: Dataset[];
  pools: Pool[];
  run: Run;
  disabled: boolean;
  canAct: boolean;
  onSnapshots: (ds: string) => void;
}) {
  const [q, setQ] = useState("");
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<Dataset | null>(null);
  const list = useMemo(() => {
    const ql = q.toLowerCase();
    return [...datasets].sort((a, b) => a.name.localeCompare(b.name)).filter((d) => !ql || d.name.toLowerCase().includes(ql) || (d.mountpoint ?? "").toLowerCase().includes(ql));
  }, [datasets, q]);
  const poolNames = pools.map((p) => p.name);

  return (
    <Card>
      <CardHeader className="flex-row flex-wrap items-start justify-between gap-3">
        <div>
          <CardTitle>Datasets and volumes</CardTitle>
          <CardDescription>{datasets.length} in all</CardDescription>
        </div>
        <div className="flex items-center gap-2">
          <div className="relative">
            <Search className="pointer-events-none absolute top-2.5 left-2.5 size-4 text-muted-foreground" />
            <Input className="h-9 w-52 pl-8" placeholder="Search" value={q} onChange={(e) => setQ(e.target.value)} />
          </div>
          {canAct && (
            <Button size="sm" variant="secondary" disabled={disabled} onClick={() => setCreating(true)}>
              <Plus /> Create
            </Button>
          )}
        </div>
      </CardHeader>
      <CardContent>
        <div className="max-h-[36rem] overflow-auto rounded-lg border">
          <table className="w-full text-sm">
            <thead className="sticky top-0 bg-card text-left text-xs text-muted-foreground">
              <tr>
                <th className="px-3 py-2 font-medium">Name</th>
                <th className="hidden px-3 py-2 font-medium md:table-cell">Type</th>
                <th className="px-3 py-2 text-right font-medium">Used</th>
                <th className="px-3 py-2 text-right font-medium">Avail</th>
                <th className="hidden px-3 py-2 text-right font-medium lg:table-cell">Refer</th>
                <th className="hidden px-3 py-2 font-medium lg:table-cell">Compression</th>
                <th className="hidden px-3 py-2 font-medium md:table-cell">Mountpoint</th>
                <th className="hidden px-3 py-2 text-right font-medium lg:table-cell">Quota</th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody>
              {list.map((d) => {
                const depth = d.name.split("/").length - 1;
                const isRoot = poolNames.includes(d.name);
                const protectedDs = isRoot || d.mountpoint === "/";
                return (
                  <tr key={d.name} className="border-t">
                    <td className="px-3 py-1.5" style={{ paddingLeft: `${0.75 + depth * 1}rem` }}>
                      <span className="font-medium">{q ? d.name : d.name.split("/").pop()}</span>
                      {d.encryption && d.encryption !== "off" && d.encryption !== "-" && (
                        <Badge variant="outline" className="ml-2">
                          encrypted{d.keystatus && d.keystatus !== "-" ? ` · ${d.keystatus}` : ""}
                        </Badge>
                      )}
                      {d.readonly === "on" && (
                        <Badge variant="outline" className="ml-2">
                          read-only
                        </Badge>
                      )}
                    </td>
                    <td className="hidden px-3 py-1.5 text-muted-foreground md:table-cell">{d.type === "volume" ? `volume ${bytes(d.volsize)}` : d.type}</td>
                    <td className="tabular px-3 py-1.5 text-right">{bytes(d.used)}</td>
                    <td className="tabular px-3 py-1.5 text-right">{bytes(d.avail)}</td>
                    <td className="tabular hidden px-3 py-1.5 text-right lg:table-cell">{bytes(d.refer)}</td>
                    <td className="hidden px-3 py-1.5 text-muted-foreground lg:table-cell">
                      {d.compression}
                      {Number.parseFloat(String(d.compressratio)) > 1.005 ? ` (${Number.parseFloat(String(d.compressratio)).toFixed(2)}x)` : ""}
                    </td>
                    <td className="hidden max-w-56 truncate px-3 py-1.5 font-mono text-xs text-muted-foreground md:table-cell" title={d.mountpoint}>
                      {d.mountpoint && d.mountpoint !== "-" ? d.mountpoint : ""}
                    </td>
                    <td className="tabular hidden px-3 py-1.5 text-right text-muted-foreground lg:table-cell">{n(d.quota) ? formatBytes(n(d.quota)) : ""}</td>
                    <td className="px-3 py-1.5 text-right whitespace-nowrap">
                      <Button size="icon" variant="ghost" title="Snapshots" aria-label={`Snapshots of ${d.name}`} onClick={() => onSnapshots(d.name)}>
                        <Camera />
                      </Button>
                      {canAct && (
                        <>
                          <Button size="icon" variant="ghost" title="Properties" aria-label={`Properties of ${d.name}`} disabled={disabled} onClick={() => setEditing(d)}>
                            <Pencil />
                          </Button>
                          <Button
                            size="icon"
                            variant="ghost"
                            className="text-destructive"
                            title={protectedDs ? "The pool's root dataset and the system's / cannot be destroyed here" : "Destroy"}
                            aria-label={`Destroy ${d.name}`}
                            disabled={disabled || protectedDs}
                            onClick={() => {
                              const x = prompt(`Destroy ${d.name}? Its data is lost. Type ${d.name} to confirm.`);
                              if (x !== d.name) return;
                              const recursive = confirm(`Also destroy everything below ${d.name} (child datasets and snapshots)? Cancel destroys ${d.name} only, which fails if it has any.`);
                              void run("zfs.dataset", { action: "destroy", name: d.name, ...(recursive ? { recursive: "1" } : {}) });
                            }}
                          >
                            <Trash2 />
                          </Button>
                        </>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </CardContent>
      {creating && <CreateDataset parents={datasets.filter((d) => d.type === "filesystem").map((d) => d.name)} run={run} onClose={() => setCreating(false)} />}
      {editing && <EditDataset ds={editing} isRoot={poolNames.includes(editing.name)} run={run} onClose={() => setEditing(null)} />}
    </Card>
  );
}

const COMMON = ["compression", "recordsize", "quota", "atime", "sync", "mountpoint"];

function propLines(p: Record<string, string>) {
  return Object.entries(p)
    .filter(([, v]) => v.trim() !== "")
    .map(([k, v]) => `${k}=${v.trim()}`)
    .join("\n");
}

function CreateDataset({ parents, run, onClose }: { parents: string[]; run: Run; onClose: () => void }) {
  const [parent, setParent] = useState(parents[0] ?? "");
  const [name, setName] = useState("");
  const [type, setType] = useState("filesystem");
  const [size, setSize] = useState("");
  const [props, setProps] = useState<Record<string, string>>({});
  const full = parent && name.trim() ? `${parent}/${name.trim()}` : "";
  const shown = COMMON.filter((k) => type === "filesystem" || (k !== "recordsize" && k !== "mountpoint" && k !== "atime"));
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Create a dataset or volume</DialogTitle>
          <DialogDescription>Empty fields inherit from the parent.</DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (!full) return;
            const p: Record<string, string> = { action: "create", name: full, type, props: propLines(Object.fromEntries(shown.map((k) => [k, props[k] ?? ""]))) };
            if (type === "volume") p.size = size.trim();
            void run("zfs.dataset", p);
            onClose();
          }}
        >
          <div className="grid grid-cols-2 gap-3">
            <Label className="grid gap-1.5">
              Inside
              <Select value={parent} onChange={(e) => setParent(e.target.value)}>
                {parents.map((p) => (
                  <option key={p}>{p}</option>
                ))}
              </Select>
            </Label>
            <Label className="grid gap-1.5">
              Name
              <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="data" />
            </Label>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <Label className="grid gap-1.5">
              Type
              <Select value={type} onChange={(e) => setType(e.target.value)}>
                <option value="filesystem">Filesystem</option>
                <option value="volume">Volume (block device)</option>
              </Select>
            </Label>
            {type === "volume" && (
              <Label className="grid gap-1.5">
                Size
                <Input value={size} onChange={(e) => setSize(e.target.value)} placeholder="32G" />
              </Label>
            )}
          </div>
          <div className="grid grid-cols-2 gap-3">
            {shown.map((k) => (
              <Label key={k} className="grid gap-1.5">
                {k}
                <Input value={props[k] ?? ""} onChange={(e) => setProps({ ...props, [k]: e.target.value })} placeholder="inherit" />
              </Label>
            ))}
          </div>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={!full || (type === "volume" && !size.trim())}>
              Create {full}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function EditDataset({ ds, isRoot, run, onClose }: { ds: Dataset; isRoot: boolean; run: Run; onClose: () => void }) {
  const initial = Object.fromEntries(PROPS.map((k) => [k, ds[k] === undefined || ds[k] === null || ds[k] === "-" ? "" : String(ds[k])]));
  const [props, setProps] = useState<Record<string, string>>(initial);
  const [newName, setNewName] = useState(ds.name);
  const changed = PROPS.filter((k) => props[k] !== initial[k]);
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[92vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{ds.name}</DialogTitle>
          <DialogDescription>Clearing a field makes it inherit from the parent again. Sizes take units: 10G, 128K.</DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-3"
          onSubmit={async (e) => {
            e.preventDefault();
            const set = changed.filter((k) => props[k].trim() !== "");
            const inherit = changed.filter((k) => props[k].trim() === "");
            if (set.length) await run("zfs.dataset", { action: "set", name: ds.name, props: propLines(Object.fromEntries(set.map((k) => [k, props[k]]))) });
            if (inherit.length) await run("zfs.dataset", { action: "inherit", name: ds.name, props: inherit.join("\n") });
            if (newName.trim() && newName.trim() !== ds.name) {
              if (confirm(`Rename ${ds.name} to ${newName.trim()}? Anything using its old name or mountpoint must be updated.`)) {
                await run("zfs.dataset", { action: "rename", name: ds.name, new_name: newName.trim() });
              }
            }
            onClose();
          }}
        >
          {!isRoot && (
            <Label className="grid gap-1.5">
              Name
              <Input value={newName} onChange={(e) => setNewName(e.target.value)} />
            </Label>
          )}
          <div className="grid gap-3 sm:grid-cols-2">
            {PROPS.filter((k) => (ds.type === "volume" ? !["recordsize", "mountpoint", "atime", "relatime", "canmount", "snapdir", "acltype", "xattr"].includes(k) : k !== "volblocksize")).map((k) => (
              <Label key={k} className="grid gap-1.5">
                <span className={props[k] !== initial[k] ? "text-brand" : ""}>{k}</span>
                <Input value={props[k]} onChange={(e) => setProps({ ...props, [k]: e.target.value })} placeholder="inherit" />
              </Label>
            ))}
          </div>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={!changed.length && (!newName.trim() || newName.trim() === ds.name)}>
              Save
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function snapName() {
  const d = new Date();
  const p = (x: number) => String(x).padStart(2, "0");
  return `manual-${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}`;
}

function Snapshots({ serverId, online, dataset, canAct, onClose }: { serverId: string; online: boolean; dataset: string; canAct: boolean; onClose: () => void }) {
  const params = useMemo(() => ({ dataset }), [dataset]);
  const { data, busy, error, job, run } = useAgentState<{ snapshots: Snapshot[] }>(serverId, "zfs.snapshots", online, params);
  const [name, setName] = useState(snapName);
  const [recursive, setRecursive] = useState(false);
  const disabled = !online || !!busy || !canAct;
  const snaps = (data?.snapshots ?? []).filter((s) => s.dataset === dataset || s.name.startsWith(dataset + "@"));
  const short = (s: Snapshot) => s.name.slice(s.name.indexOf("@") + 1);

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[92vh] overflow-y-auto sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>Snapshots of {dataset}</DialogTitle>
          <DialogDescription>{data ? `${snaps.length} snapshots` : "Loading…"}</DialogDescription>
        </DialogHeader>
        <OpStatus busy={busy} error={error} job={job} />
        {canAct && (
          <form
            className="flex flex-wrap items-end gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              if (!name.trim()) return;
              void run("zfs.snapshot", { action: "create", name: `${dataset}@${name.trim()}`, ...(recursive ? { recursive: "1" } : {}) }).then(() => setName(snapName()));
            }}
          >
            <Label className="grid gap-1.5">
              New snapshot
              <Input className="w-64" value={name} onChange={(e) => setName(e.target.value)} />
            </Label>
            <label className="flex items-center gap-1.5 pb-2 text-sm">
              <input type="checkbox" checked={recursive} onChange={(e) => setRecursive(e.target.checked)} /> with child datasets
            </label>
            <Button type="submit" disabled={disabled || !name.trim()}>
              <Camera /> Take snapshot
            </Button>
          </form>
        )}
        {snaps.length > 0 && (
          <div className="max-h-[50vh] overflow-auto rounded-lg border">
            <table className="w-full text-sm">
              <thead className="sticky top-0 bg-card text-left text-xs text-muted-foreground">
                <tr>
                  <th className="px-3 py-2 font-medium">Snapshot</th>
                  <th className="px-3 py-2 font-medium">Created</th>
                  <th className="px-3 py-2 text-right font-medium">Used</th>
                  <th className="px-3 py-2 text-right font-medium">Refer</th>
                  <th className="px-3 py-2" />
                </tr>
              </thead>
              <tbody>
                {[...snaps]
                  .sort((a, b) => n(b.creation) - n(a.creation))
                  .map((s) => (
                    <tr key={s.name} className="border-t">
                      <td className="px-3 py-1.5 font-mono text-xs">{short(s)}</td>
                      <td className="px-3 py-1.5 text-muted-foreground">{n(s.creation) ? new Date(n(s.creation) * 1000).toLocaleString() : ""}</td>
                      <td className="tabular px-3 py-1.5 text-right">{bytes(s.used)}</td>
                      <td className="tabular px-3 py-1.5 text-right">{bytes(s.refer)}</td>
                      <td className="px-3 py-1.5 text-right whitespace-nowrap">
                        {canAct && (
                          <>
                            <Button
                              size="sm"
                              variant="ghost"
                              disabled={disabled}
                              onClick={() => {
                                if (!confirm(`Roll ${dataset} back to ${short(s)}? Changes made since then are lost.`)) return;
                                const newer = confirm(
                                  "ZFS can only roll back to the newest snapshot. If there are newer ones, should they be destroyed too? (Cancel: roll back only if this is the newest.)",
                                );
                                void run("zfs.snapshot", { action: "rollback", name: s.name, ...(newer ? { recursive: "1" } : {}) });
                              }}
                            >
                              Roll back
                            </Button>
                            <Button
                              size="sm"
                              variant="ghost"
                              disabled={disabled}
                              onClick={() => {
                                const target = prompt(`Clone ${s.name} as a new dataset named:`, `${dataset}-${short(s)}`);
                                if (target) void run("zfs.snapshot", { action: "clone", name: s.name, target: target.trim() });
                              }}
                            >
                              Clone
                            </Button>
                            <Button
                              size="icon"
                              variant="ghost"
                              className="text-destructive"
                              aria-label={`Destroy ${s.name}`}
                              disabled={disabled}
                              onClick={() => confirm(`Destroy snapshot ${s.name}?`) && void run("zfs.snapshot", { action: "destroy", name: s.name })}
                            >
                              <Trash2 />
                            </Button>
                          </>
                        )}
                      </td>
                    </tr>
                  ))}
              </tbody>
            </table>
          </div>
        )}
        {data && snaps.length === 0 && <p className="text-sm text-muted-foreground">No snapshots yet.</p>}
      </DialogContent>
    </Dialog>
  );
}
