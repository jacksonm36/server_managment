import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Activity, Database, Disc3, Download, Eraser, FolderOpen, HardDrive, Layers, Pencil, Plus, RefreshCw, Trash2, Upload } from "lucide-react";
import {
  api,
  type PVECluster,
  type PVEDisk,
  type PVEISO,
  type PVENodeStorage,
  type PVESMART,
  type PVEStorageConfig,
  type PVEStorageOverview,
  type PVEStorageReq,
  type PVEVolume,
  type PVEZDetail,
  type PVEZNode,
  type PVEZPool,
} from "@/lib/api";
import { formatBytes, pct } from "@/lib/utils";
import { useLive } from "@/lib/live";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label } from "@/components/ui/input";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Select } from "@/components/ui/select";
import { useAction } from "@/pages/ProxmoxGuestTabs";
import { UploadDialog } from "@/pages/ProxmoxAdmin";
import { ISOLibrary } from "@/components/ISOLibrary";
import { ZFS } from "@/pages/server/ZFS";

// Cluster storage (platform admins): storage definitions and their usage,
// what is on each storage, the node's physical disks with SMART health,
// and ZFS pools.

const contentTypes = ["images", "rootdir", "iso", "vztmpl", "backup", "snippets", "import"];
const contentLabel: Record<string, string> = {
  images: "Disk images",
  rootdir: "Containers",
  iso: "ISO images",
  vztmpl: "CT templates",
  backup: "Backups",
  snippets: "Snippets",
  import: "Import",
};
const blockTypes = ["lvmthin", "zfspool", "lvm", "rbd", "zfs", "iscsi", "iscsidirect"];
const raidMin: Record<string, number> = { single: 1, mirror: 2, raid10: 4, raidz: 3, raidz2: 4, raidz3: 5 };

const diskFree = (d: PVEDisk) => (!d.used || d.used === "partitions") && !d.mounted;

// Polls a node task until it ends; resolves with an error message, or "".
async function waitTask(cluster: string, node: string, upid: string): Promise<string> {
  for (let i = 0; i < 600; i++) {
    const t = await api.pveNodeTask(cluster, node, upid);
    if (t.status !== "running") return t.exitstatus === "OK" ? "" : t.exitstatus || "task failed";
    await new Promise((r) => setTimeout(r, 1500));
  }
  return "";
}

export function ProxmoxStorage() {
  const [clusters, setClusters] = useState<PVECluster[] | null>(null);
  const [cur, setCur] = useState("");
  const [err, setErr] = useState("");
  useEffect(() => {
    api
      .pveClusters()
      .then((c) => {
        setClusters(c);
        setCur(c[0]?.id ?? "");
      })
      .catch((e) => setErr((e as Error).message));
  }, []);

  return (
    <div className="grid gap-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Storage</h1>
        <p className="text-sm text-muted-foreground">Storages, ISO images and backups, physical disks and ZFS pools on your Proxmox clusters.</p>
      </div>
      {err && <p className="text-sm text-destructive">{err}</p>}
      {clusters && clusters.length === 0 && <Card className="p-8 text-center text-sm text-muted-foreground">Add a Proxmox cluster first.</Card>}
      {clusters && clusters.length > 1 && (
        <div role="tablist" className="-mx-1 flex gap-1 overflow-x-auto border-b px-1">
          {clusters.map((c) => (
            <button
              key={c.id}
              role="tab"
              aria-selected={cur === c.id}
              onClick={() => setCur(c.id)}
              className={`-mb-px cursor-pointer border-b-2 px-3 py-2 text-sm whitespace-nowrap ${cur === c.id ? "border-brand text-foreground" : "border-transparent text-muted-foreground hover:text-foreground"}`}
            >
              {c.name}
            </button>
          ))}
        </div>
      )}
      {cur && <ClusterStorage key={cur} cluster={cur} />}
    </div>
  );
}

function ClusterStorage({ cluster }: { cluster: string }) {
  const [data, setData] = useState<PVEStorageOverview | null>(null);
  const [err, setErr] = useState("");
  const [node, setNode] = useState("");
  const [editing, setEditing] = useState<PVEStorageConfig | "new" | null>(null);
  const [browse, setBrowse] = useState("");
  const [diskRev, setDiskRev] = useState(0); // a ZFS change reloads the disks
  const { busy, run, note } = useAction();

  const load = useCallback(async () => {
    try {
      const d = await api.pveStorage(cluster);
      setData(d);
      setNode((n) => (n && d.nodes[n] ? n : (Object.keys(d.nodes).sort()[0] ?? "")));
      setErr("");
    } catch (e) {
      setErr((e as Error).message);
    }
  }, [cluster]);
  useEffect(() => {
    void load();
  }, [load]);

  if (err) return <p className="text-sm text-destructive">{err}</p>;
  if (!data) return <p className="text-sm text-muted-foreground">Loading…</p>;
  const nodes = Object.keys(data.nodes).sort();
  const status = new Map((data.nodes[node] ?? []).map((s) => [s.storage, s]));
  const browsing = browse ? status.get(browse) : undefined;

  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap items-center gap-3">
        <Label htmlFor="st-node" className="text-sm text-muted-foreground">
          Node
        </Label>
        <Select id="st-node" className="w-48" value={node} onChange={(e) => setNode(e.target.value)}>
          {nodes.map((n) => (
            <option key={n}>{n}</option>
          ))}
        </Select>
        <Button size="sm" variant="ghost" onClick={() => void load()}>
          <RefreshCw /> Refresh
        </Button>
        {(data.errors ?? []).map((e) => (
          <span key={e} className="text-sm text-destructive">
            {e}
          </span>
        ))}
      </div>

      <Card>
        <CardHeader className="flex flex-row flex-wrap items-start justify-between gap-3">
          <div>
            <CardTitle className="flex items-center gap-2">
              <Database className="size-4" /> Storages
            </CardTitle>
            <CardDescription>Usage is as seen from {node || "the node"}. Removing a storage only removes its definition; the data stays.</CardDescription>
          </div>
          <Button size="sm" variant="secondary" onClick={() => setEditing("new")}>
            <Plus /> Add storage
          </Button>
        </CardHeader>
        <CardContent className="grid gap-3">
          {note}
          {busy && <p className="text-sm text-muted-foreground">{busy}…</p>}
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="bg-muted/60 text-left text-xs font-semibold tracking-wider text-muted-foreground uppercase">
                  <th className="rounded-l-md px-2 py-1.5">Storage</th>
                  <th className="px-2 py-1.5">Type</th>
                  <th className="px-2 py-1.5">Content</th>
                  <th className="px-2 py-1.5">Usage</th>
                  <th className="rounded-r-md px-2 py-1.5" />
                </tr>
              </thead>
              <tbody className="divide-y">
                {data.config
                  .slice()
                  .sort((a, b) => a.storage.localeCompare(b.storage))
                  .map((c) => {
                    const s = status.get(c.storage);
                    return (
                      <tr key={c.storage} className={browse === c.storage ? "bg-brand/5" : ""}>
                        <td className="px-2 py-2">
                          <div className="font-medium">{c.storage}</div>
                          <div className="text-xs text-muted-foreground">
                            {c.disable ? "disabled" : c.nodes ? `nodes: ${c.nodes}` : "all nodes"}
                            {c.shared || c.type === "nfs" ? " · shared" : ""}
                          </div>
                        </td>
                        <td className="px-2 py-2">
                          <Badge variant="outline">{c.type}</Badge>
                        </td>
                        <td className="px-2 py-2">
                          <div className="flex flex-wrap gap-1">
                            {(c.content ?? "")
                              .split(",")
                              .filter(Boolean)
                              .map((x) => (
                                <Badge key={x} variant="outline" className="font-normal text-muted-foreground">
                                  {contentLabel[x] ?? x}
                                </Badge>
                              ))}
                          </div>
                        </td>
                        <td className="min-w-44 px-2 py-2">{s ? <Usage s={s} /> : <span className="text-xs text-muted-foreground">not on {node}</span>}</td>
                        <td className="px-2 py-2 text-right whitespace-nowrap">
                          {s && (
                            <Button size="sm" variant="ghost" onClick={() => setBrowse(browse === c.storage ? "" : c.storage)}>
                              <FolderOpen /> Browse
                            </Button>
                          )}
                          <Button size="sm" variant="ghost" onClick={() => setEditing(c)}>
                            <Pencil /> Edit
                          </Button>
                          <Button
                            size="sm"
                            variant="ghost"
                            className="text-destructive"
                            disabled={!!busy}
                            onClick={() => {
                              const x = prompt(`Remove the storage definition ${c.storage}? Its data is not deleted. Type ${c.storage} to confirm.`);
                              if (x)
                                void run(`Removing ${c.storage}`, () => api.pveStorageDelete(cluster, c.storage, x), `${c.storage} removed.`).then((ok) => {
                                  if (ok) {
                                    if (browse === c.storage) setBrowse("");
                                    void load();
                                  }
                                });
                            }}
                          >
                            <Trash2 /> Remove
                          </Button>
                        </td>
                      </tr>
                    );
                  })}
              </tbody>
            </table>
          </div>
        </CardContent>
      </Card>

      {browsing && <ContentBrowser key={`${node}/${browse}`} cluster={cluster} node={node} storage={browsing} onClose={() => setBrowse("")} />}

      {node && <DisksCard key={`d-${node}-${diskRev}`} cluster={cluster} node={node} />}
      {node && (
        <ZFSCard
          key={`z-${node}`}
          cluster={cluster}
          node={node}
          onStorageChanged={() => {
            setDiskRev((n) => n + 1);
            void load();
          }}
        />
      )}

      {editing && (
        <StorageDialog
          cluster={cluster}
          nodes={nodes}
          storage={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            void load();
          }}
        />
      )}
    </div>
  );
}

function Usage({ s }: { s: PVENodeStorage }) {
  if (!s.total) return <span className="text-xs text-muted-foreground">{s.active ? "active" : "inactive"}</span>;
  const p = pct(s.used ?? 0, s.total);
  return (
    <div className="grid gap-1 text-xs">
      <div className="h-1.5 rounded-full bg-muted">
        <div className={`h-1.5 rounded-full ${p > 90 ? "bg-destructive" : p > 75 ? "bg-warning" : "bg-brand"}`} style={{ width: `${Math.min(100, p)}%` }} />
      </div>
      <span className="tabular text-muted-foreground">
        {formatBytes(s.used ?? 0)} of {formatBytes(s.total)} ({Math.round(p)}%)
      </span>
    </div>
  );
}

function StorageDialog({
  cluster,
  nodes,
  storage,
  onClose,
  onSaved,
}: {
  cluster: string;
  nodes: string[];
  storage: PVEStorageConfig | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [f, setF] = useState<PVEStorageReq>(() =>
    storage
      ? {
          storage: storage.storage,
          type: storage.type as PVEStorageReq["type"],
          content: (storage.content ?? "").split(",").filter(Boolean),
          nodes: storage.nodes ? storage.nodes.split(",") : [],
          disable: !!storage.disable,
        }
      : { storage: "", type: "dir", content: ["iso", "vztmpl", "backup"], nodes: [], path: "", server: "", export: "", vgname: "", thinpool: "", pool: "" },
  );
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const set = (p: Partial<PVEStorageReq>) => setF((x) => ({ ...x, ...p }));
  const block = blockTypes.includes(f.type);
  const allowed = block ? ["images", "rootdir"] : contentTypes;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr("");
    try {
      if (storage) await api.pveStorageUpdate(cluster, storage.storage, { content: f.content, nodes: f.nodes, disable: f.disable });
      else await api.pveStorageCreate(cluster, f);
      onSaved();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  const toggle = (list: string[], v: string) => (list.includes(v) ? list.filter((x) => x !== v) : [...list, v]);

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{storage ? `Edit ${storage.storage}` : "Add storage"}</DialogTitle>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4">
          {!storage && (
            <div className="grid grid-cols-2 gap-3">
              <div className="grid gap-1.5">
                <Label htmlFor="st-id">ID</Label>
                <Input id="st-id" value={f.storage} onChange={(e) => set({ storage: e.target.value })} placeholder="backup-nas" required />
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="st-type">Type</Label>
                <Select
                  id="st-type"
                  value={f.type}
                  onChange={(e) => {
                    const type = e.target.value as PVEStorageReq["type"];
                    set({ type, content: blockTypes.includes(type) ? ["images", "rootdir"] : type === "nfs" ? ["backup", "iso"] : ["iso", "vztmpl", "backup"] });
                  }}
                >
                  <option value="dir">Directory</option>
                  <option value="nfs">NFS</option>
                  <option value="lvmthin">LVM-Thin</option>
                  <option value="zfspool">ZFS pool</option>
                </Select>
              </div>
              {f.type === "dir" && (
                <div className="col-span-2 grid gap-1.5">
                  <Label htmlFor="st-path">Directory</Label>
                  <Input id="st-path" value={f.path} onChange={(e) => set({ path: e.target.value })} placeholder="/mnt/backup" required />
                </div>
              )}
              {f.type === "nfs" && (
                <>
                  <div className="grid gap-1.5">
                    <Label htmlFor="st-server">Server</Label>
                    <Input id="st-server" value={f.server} onChange={(e) => set({ server: e.target.value })} placeholder="10.0.0.5" required />
                  </div>
                  <div className="grid gap-1.5">
                    <Label htmlFor="st-export">Export</Label>
                    <Input id="st-export" value={f.export} onChange={(e) => set({ export: e.target.value })} placeholder="/srv/proxmox" required />
                  </div>
                </>
              )}
              {f.type === "lvmthin" && (
                <>
                  <div className="grid gap-1.5">
                    <Label htmlFor="st-vg">Volume group</Label>
                    <Input id="st-vg" value={f.vgname} onChange={(e) => set({ vgname: e.target.value })} placeholder="pve" required />
                  </div>
                  <div className="grid gap-1.5">
                    <Label htmlFor="st-thin">Thin pool</Label>
                    <Input id="st-thin" value={f.thinpool} onChange={(e) => set({ thinpool: e.target.value })} placeholder="data" required />
                  </div>
                </>
              )}
              {f.type === "zfspool" && (
                <div className="col-span-2 grid gap-1.5">
                  <Label htmlFor="st-pool">Pool or dataset</Label>
                  <Input id="st-pool" value={f.pool} onChange={(e) => set({ pool: e.target.value })} placeholder="tank/vmdata" required />
                </div>
              )}
            </div>
          )}
          <fieldset className="grid gap-2">
            <legend className="mb-1 text-sm font-medium">Content</legend>
            <div className="flex flex-wrap gap-x-4 gap-y-2">
              {allowed.map((c) => (
                <label key={c} className="flex cursor-pointer items-center gap-2 text-sm">
                  <input type="checkbox" checked={f.content.includes(c)} onChange={() => set({ content: toggle(f.content, c) })} className="accent-brand" />
                  {contentLabel[c]}
                </label>
              ))}
            </div>
          </fieldset>
          <fieldset className="grid gap-2">
            <legend className="mb-1 text-sm font-medium">Nodes</legend>
            <p className="text-xs text-muted-foreground">None ticked means every node.</p>
            <div className="flex flex-wrap gap-x-4 gap-y-2">
              {nodes.map((n) => (
                <label key={n} className="flex cursor-pointer items-center gap-2 text-sm">
                  <input type="checkbox" checked={(f.nodes ?? []).includes(n)} onChange={() => set({ nodes: toggle(f.nodes ?? [], n) })} className="accent-brand" />
                  {n}
                </label>
              ))}
            </div>
          </fieldset>
          {storage && (
            <label className="flex cursor-pointer items-center gap-2 text-sm">
              <input type="checkbox" checked={!!f.disable} onChange={(e) => set({ disable: e.target.checked })} className="accent-brand" />
              Disabled
            </label>
          )}
          {err && <p className="text-sm text-destructive">{err}</p>}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={busy || f.content.length === 0}>
              {busy ? "Saving…" : storage ? "Save" : "Add storage"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function ContentBrowser({ cluster, node, storage, onClose }: { cluster: string; node: string; storage: PVENodeStorage; onClose: () => void }) {
  const [vols, setVols] = useState<PVEVolume[] | null>(null);
  const [err, setErr] = useState("");
  const [filter, setFilter] = useState("");
  const [dl, setDl] = useState({ url: "", content: "iso" as "iso" | "vztmpl", filename: "" });
  const { busy, run, note } = useAction();
  const types = storage.content.split(",");
  const canDownload = types.includes("iso") || types.includes("vztmpl");
  const canUpload = canDownload || types.includes("import");
  const [uploading, setUploading] = useState(false);
  const [library, setLibrary] = useState(false);
  const [upNote, setUpNote] = useState("");

  const load = useCallback(() => {
    api
      .pveStorageContent(cluster, node, storage.storage)
      .then((v) => setVols(v.sort((a, b) => a.volid.localeCompare(b.volid))))
      .catch((e) => setErr((e as Error).message));
  }, [cluster, node, storage.storage]);
  useEffect(load, [load]);

  const shown = (vols ?? []).filter((v) => !filter || v.content === filter);
  const kinds = [...new Set((vols ?? []).map((v) => v.content))];

  const isoFiles = new Set((vols ?? []).filter((v) => v.content === "iso").map((v) => v.volid.slice(v.volid.lastIndexOf("/") + 1)));
  const fromLibrary = (x: PVEISO) => {
    setLibrary(false);
    void run(
      `Downloading ${x.file}`,
      async () => {
        const { upid } = await api.pveDownloadURL(cluster, node, storage.storage, {
          url: x.url!,
          content: "iso",
          filename: x.file,
          checksum: x.checksum,
          checksum_algorithm: x.checksum_algorithm,
          compression: x.compression,
        });
        const fail = await waitTask(cluster, node, upid);
        if (fail) throw new Error(fail);
      },
      `${x.file} downloaded${x.checksum ? " and its checksum matched" : ""}.`,
    ).then((ok) => ok && load());
  };

  const download = (e: FormEvent) => {
    e.preventDefault();
    void run(
      "Downloading",
      async () => {
        const { upid } = await api.pveDownloadURL(cluster, node, storage.storage, { url: dl.url, content: dl.content, filename: dl.filename || undefined });
        const fail = await waitTask(cluster, node, upid);
        if (fail) throw new Error(fail);
      },
      "Download finished.",
    ).then((ok) => {
      if (ok) {
        setDl((x) => ({ ...x, url: "", filename: "" }));
        load();
      }
    });
  };

  return (
    <Card>
      <CardHeader className="flex flex-row flex-wrap items-start justify-between gap-3">
        <div>
          <CardTitle className="flex items-center gap-2">
            <FolderOpen className="size-4" /> {storage.storage} on {node}
          </CardTitle>
          <CardDescription>{vols ? `${vols.length} item${vols.length === 1 ? "" : "s"}` : "Loading…"}</CardDescription>
        </div>
        <div className="flex items-center gap-2">
          {kinds.length > 1 && (
            <Select className="w-40" value={filter} onChange={(e) => setFilter(e.target.value)} aria-label="Filter content">
              <option value="">Everything</option>
              {kinds.map((k) => (
                <option key={k} value={k}>
                  {contentLabel[k] ?? k}
                </option>
              ))}
            </Select>
          )}
          {types.includes("iso") && (
            <Button size="sm" variant="secondary" onClick={() => setLibrary(true)} disabled={!!busy}>
              <Disc3 /> ISO library
            </Button>
          )}
          {canUpload && (
            <Button size="sm" variant="secondary" onClick={() => setUploading(true)}>
              <Upload /> Upload
            </Button>
          )}
          <Button size="sm" variant="ghost" onClick={onClose}>
            Close
          </Button>
        </div>
      </CardHeader>
      <CardContent className="grid gap-4">
        {err && <p className="text-sm text-destructive">{err}</p>}
        {note}
        {upNote && <p className="text-sm text-success">{upNote}</p>}
        {library && <ISOLibrary storage={storage.storage} have={isoFiles} onPick={fromLibrary} onClose={() => setLibrary(false)} />}
        {uploading && (
          <UploadDialog
            cluster={cluster}
            node={node}
            storage={storage}
            onClose={() => setUploading(false)}
            onDone={(t) => {
              setUpNote(t);
              setTimeout(load, 2000);
            }}
          />
        )}
        {busy && <p className="text-sm text-muted-foreground">{busy}…</p>}
        {canDownload && (
          <form onSubmit={download} className="grid gap-2 rounded-lg border border-dashed p-3 sm:grid-cols-[1fr_8rem_12rem_auto] sm:items-end">
            <div className="grid gap-1.5">
              <Label htmlFor="dl-url">Download from URL</Label>
              <Input id="dl-url" type="url" value={dl.url} onChange={(e) => setDl({ ...dl, url: e.target.value })} placeholder="https://cdimage.debian.org/…/debian-13-netinst.iso" required />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="dl-kind">Kind</Label>
              <Select id="dl-kind" value={dl.content} onChange={(e) => setDl({ ...dl, content: e.target.value as "iso" | "vztmpl" })}>
                {types.includes("iso") && <option value="iso">ISO image</option>}
                {types.includes("vztmpl") && <option value="vztmpl">CT template</option>}
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="dl-name">File name</Label>
              <Input id="dl-name" value={dl.filename} onChange={(e) => setDl({ ...dl, filename: e.target.value })} placeholder="from the URL" />
            </div>
            <Button type="submit" disabled={!!busy}>
              <Download /> Download
            </Button>
          </form>
        )}
        {vols && shown.length === 0 && <p className="text-sm text-muted-foreground">Nothing here.</p>}
        {shown.length > 0 && (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <tbody className="divide-y">
                {shown.map((v) => (
                  <tr key={v.volid}>
                    <td className="py-2 pr-3 font-mono text-xs break-all">{v.volid.slice(v.volid.indexOf(":") + 1)}</td>
                    <td className="py-2 pr-3">
                      <Badge variant="outline" className="font-normal text-muted-foreground">
                        {contentLabel[v.content] ?? v.content}
                      </Badge>
                    </td>
                    <td className="py-2 pr-3 text-muted-foreground">{v.vmid ? `VM ${v.vmid}` : ""}</td>
                    <td className="tabular py-2 pr-3 whitespace-nowrap">{v.size ? formatBytes(v.size) : ""}</td>
                    <td className="py-2 pr-3 text-xs whitespace-nowrap text-muted-foreground">{v.ctime ? new Date(v.ctime * 1000).toLocaleDateString() : ""}</td>
                    <td className="py-2 text-right">
                      <Button
                        size="sm"
                        variant="ghost"
                        className="text-destructive"
                        disabled={!!busy}
                        onClick={() => {
                          const x = prompt(`Delete ${v.volid}? This cannot be undone. Type the full name to confirm:\n${v.volid}`);
                          if (x) void run("Deleting", () => api.pveVolumeDelete(cluster, node, storage.storage, v.volid, x), "Deleted.").then((ok) => ok && load());
                        }}
                      >
                        <Trash2 /> Delete
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function healthBadge(h?: string) {
  if (!h || h === "UNKNOWN") return <Badge variant="outline">unknown</Badge>;
  const ok = h === "PASSED" || h === "OK" || h === "ONLINE";
  return (
    <Badge variant={ok ? "success" : "destructive"} className="gap-1.5">
      <span className="size-1.5 rounded-full bg-current" /> {h.toLowerCase()}
    </Badge>
  );
}

function DisksCard({ cluster, node }: { cluster: string; node: string }) {
  const [disks, setDisks] = useState<PVEDisk[] | null>(null);
  const [err, setErr] = useState("");
  const [smart, setSmart] = useState<{ disk: string; data: PVESMART | null; err: string } | null>(null);
  const { busy, run, note } = useAction();
  const load = useCallback(() => {
    api
      .pveDisks(cluster, node)
      .then((d) => setDisks(d.sort((a, b) => a.devpath.localeCompare(b.devpath))))
      .catch((e) => setErr((e as Error).message));
  }, [cluster, node]);
  useEffect(load, [load]);

  const openSmart = (disk: string) => {
    setSmart({ disk, data: null, err: "" });
    api
      .pveDiskSMART(cluster, node, disk)
      // Only the dialog still showing this disk takes the answer.
      .then((data) => setSmart((cur) => (cur?.disk === disk ? { disk, data, err: "" } : cur)))
      .catch((e) => setSmart((cur) => (cur?.disk === disk ? { disk, data: null, err: (e as Error).message } : cur)));
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <HardDrive className="size-4" /> Disks on {node}
        </CardTitle>
        <CardDescription>Physical disks with their SMART health. Only disks nothing uses can be wiped.</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3">
        {err && <p className="text-sm text-destructive">{err}</p>}
        {note}
        {busy && <p className="text-sm text-muted-foreground">{busy}…</p>}
        {disks && disks.length === 0 && <p className="text-sm text-muted-foreground">No disks reported.</p>}
        {disks && disks.length > 0 && (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="bg-muted/60 text-left text-xs font-semibold tracking-wider text-muted-foreground uppercase">
                  <th className="rounded-l-md px-2 py-1.5">Disk</th>
                  <th className="px-2 py-1.5">Model</th>
                  <th className="px-2 py-1.5">Size</th>
                  <th className="px-2 py-1.5">Health</th>
                  <th className="px-2 py-1.5">Used by</th>
                  <th className="rounded-r-md px-2 py-1.5" />
                </tr>
              </thead>
              <tbody className="divide-y">
                {disks.map((d) => (
                  <tr key={d.devpath}>
                    <td className="px-2 py-2">
                      <div className="font-mono text-xs">{d.devpath}</div>
                      <div className="text-xs text-muted-foreground uppercase">{d.type}</div>
                    </td>
                    <td className="px-2 py-2">
                      <div>{d.model || "—"}</div>
                      {d.serial && <div className="text-xs text-muted-foreground">{d.serial}</div>}
                    </td>
                    <td className="tabular px-2 py-2 whitespace-nowrap">{formatBytes(d.size)}</td>
                    <td className="px-2 py-2">
                      <div className="flex flex-wrap items-center gap-1.5">
                        {healthBadge(d.health)}
                        {typeof d.wearout === "number" && <span className="text-xs text-muted-foreground">{d.wearout}% life left</span>}
                      </div>
                    </td>
                    <td className="px-2 py-2 text-muted-foreground">{d.mounted ? "mounted" : d.used || "free"}</td>
                    <td className="px-2 py-2 text-right whitespace-nowrap">
                      <Button size="sm" variant="ghost" onClick={() => openSmart(d.devpath)}>
                        <Activity /> SMART
                      </Button>
                      {diskFree(d) && (
                        <Button
                          size="sm"
                          variant="ghost"
                          className="text-destructive"
                          disabled={!!busy}
                          onClick={() => {
                            const x = prompt(`Wipe ${d.devpath}? Every partition and all data on it is erased. Type ${d.devpath} to confirm.`);
                            if (x) void run(`Wiping ${d.devpath}`, () => api.pveDiskWipe(cluster, node, d.devpath, x), `${d.devpath} wiped.`).then((ok) => ok && load());
                          }}
                        >
                          <Eraser /> Wipe
                        </Button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </CardContent>
      {smart && (
        <Dialog open onOpenChange={(o) => !o && setSmart(null)}>
          <DialogContent className="sm:max-w-2xl">
            <DialogHeader>
              <DialogTitle className="flex items-center gap-2">
                SMART: {smart.disk} {smart.data && healthBadge(smart.data.health)}
              </DialogTitle>
            </DialogHeader>
            {smart.err && <p className="text-sm text-destructive">{smart.err}</p>}
            {!smart.data && !smart.err && <p className="text-sm text-muted-foreground">Reading…</p>}
            {smart.data?.text && <pre className="max-h-96 overflow-auto rounded-md bg-muted p-3 text-xs whitespace-pre-wrap">{smart.data.text}</pre>}
            {smart.data && !smart.data.text && !smart.data.attributes?.length && (
              <p className="text-sm text-muted-foreground">
                No SMART data: the disk does not report it (virtual disks and some USB or RAID controllers do not).
              </p>
            )}
            {smart.data?.attributes && smart.data.attributes.length > 0 && (
              <div className="max-h-96 overflow-auto">
                <table className="w-full text-xs">
                  <thead>
                    <tr className="text-left text-muted-foreground">
                      <th className="py-1 pr-2">ID</th>
                      <th className="py-1 pr-2">Attribute</th>
                      <th className="py-1 pr-2">Value</th>
                      <th className="py-1 pr-2">Worst</th>
                      <th className="py-1 pr-2">Thresh</th>
                      <th className="py-1">Raw</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y">
                    {smart.data.attributes.map((a) => (
                      <tr key={`${a.id}-${a.name}`} className={a.fail && a.fail !== "-" ? "text-destructive" : ""}>
                        <td className="py-1 pr-2">{a.id}</td>
                        <td className="py-1 pr-2">{a.name}</td>
                        <td className="tabular py-1 pr-2">{a.value}</td>
                        <td className="tabular py-1 pr-2">{a.worst}</td>
                        <td className="tabular py-1 pr-2">{a.threshold}</td>
                        <td className="tabular py-1">{a.raw}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </DialogContent>
        </Dialog>
      )}
    </Card>
  );
}

function ZFSCard({ cluster, node, onStorageChanged }: { cluster: string; node: string; onStorageChanged: () => void }) {
  // The node's servermgr server, if the agent is on it: full ZFS lives there.
  const agent = useLive((st) => [...st.servers.values()].find((x) => [x.name, x.hostname.split(".")[0]].some((n) => n.toLowerCase() === node.toLowerCase())));
  const [pools, setPools] = useState<PVEZPool[] | null>(null);
  const [err, setErr] = useState("");
  const [open, setOpen] = useState<{ name: string; detail: PVEZDetail | null } | null>(null);
  const [creating, setCreating] = useState(false);
  // Bumped after a pool is created through Proxmox so the agent view reloads.
  const [gen, setGen] = useState(0);
  const { busy, run, note } = useAction();
  const load = useCallback(() => {
    api
      .pveZFS(cluster, node)
      .then(setPools)
      .catch((e) => setErr((e as Error).message));
  }, [cluster, node]);
  useEffect(load, [load]);

  const show = (name: string) => {
    if (open?.name === name) return setOpen(null);
    setOpen({ name, detail: null });
    api
      .pveZFSDetail(cluster, node, name)
      .then((detail) => setOpen({ name, detail }))
      .catch((e) => setErr((e as Error).message));
  };

  const destroy = (name: string) => {
    const x = prompt(`Destroy the ZFS pool ${name}? All data on it is lost. Type ${name} to confirm.`);
    if (!x) return;
    const disks = confirm("Also wipe the pool's disks so they can be reused?");
    void run(
      `Destroying ${name}`,
      async () => {
        const { upid } = await api.pveZFSDestroy(cluster, node, name, { confirm: x, cleanup_config: true, cleanup_disks: disks });
        const fail = await waitTask(cluster, node, upid);
        if (fail) throw new Error(fail);
      },
      `${name} destroyed.`,
    ).then((ok) => {
      if (ok) {
        setOpen(null);
        load();
        setGen((g) => g + 1);
        onStorageChanged();
      }
    });
  };

  const createDialog = creating && (
    <ZFSCreateDialog
      cluster={cluster}
      node={node}
      onClose={() => setCreating(false)}
      onCreated={() => {
        setCreating(false);
        load();
        setGen((g) => g + 1);
        onStorageChanged();
      }}
    />
  );

  // With the agent on the node, the full ZFS view lives here; creating a
  // pool still goes through Proxmox so it is added as VM storage too.
  if (agent) {
    return (
      <div className="grid gap-3">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h2 className="flex items-center gap-2 font-semibold">
              <Layers className="size-4" /> ZFS on {node}
            </h2>
            <p className="text-sm text-muted-foreground">
              Pools, disks, datasets, snapshots, schedules and replication, through the servermgr agent. A pool created here is also added as VM storage.
            </p>
          </div>
          <Button size="sm" variant="secondary" onClick={() => setCreating(true)}>
            <Plus /> Create pool
          </Button>
        </div>
        {err && <p className="text-sm text-destructive">{err}</p>}
        {note}
        {busy && <p className="text-sm text-muted-foreground">{busy}…</p>}
        <ZFS key={gen} serverId={agent.id} online={agent.online} hideCreate onDestroyPool={destroy} />
        {createDialog}
      </div>
    );
  }

  return (
    <Card>
      <CardHeader className="flex flex-row flex-wrap items-start justify-between gap-3">
        <div>
          <CardTitle className="flex items-center gap-2">
            <Layers className="size-4" /> ZFS pools on {node}
          </CardTitle>
          <CardDescription>
            Create a pool from free disks and add it as VM storage in one step. Scrubs, disk replacement, datasets, snapshots, schedules and replication
            need the servermgr agent on {node} (Servers → Add server).
          </CardDescription>
        </div>
        <Button size="sm" variant="secondary" onClick={() => setCreating(true)}>
          <Plus /> Create pool
        </Button>
      </CardHeader>
      <CardContent className="grid gap-3">
        {err && <p className="text-sm text-destructive">{err}</p>}
        {note}
        {busy && <p className="text-sm text-muted-foreground">{busy}…</p>}
        {pools && pools.length === 0 && <p className="text-sm text-muted-foreground">No ZFS pools on this node.</p>}
        {(pools ?? []).map((p) => (
          <div key={p.name} className="rounded-lg border p-3">
            <div className="flex flex-wrap items-center gap-3">
              <span className="font-semibold">{p.name}</span>
              {healthBadge(p.health)}
              <span className="tabular text-sm text-muted-foreground">
                {formatBytes(p.alloc)} of {formatBytes(p.size)} used
              </span>
              <div className="ml-auto flex gap-1">
                <Button size="sm" variant="ghost" onClick={() => show(p.name)}>
                  {open?.name === p.name ? "Hide" : "Details"}
                </Button>
                <Button size="sm" variant="ghost" className="text-destructive" disabled={!!busy} onClick={() => destroy(p.name)}>
                  <Trash2 /> Destroy
                </Button>
              </div>
            </div>
            <div className="mt-2 h-1.5 rounded-full bg-muted">
              <div className="h-1.5 rounded-full bg-brand" style={{ width: `${Math.min(100, pct(p.alloc, p.size))}%` }} />
            </div>
            {open?.name === p.name && (
              <div className="mt-3 grid gap-2 text-sm">
                {!open.detail && <p className="text-muted-foreground">Loading…</p>}
                {open.detail && (
                  <>
                    {open.detail.scan && <p className="text-muted-foreground">Scan: {open.detail.scan}</p>}
                    {open.detail.errors && <p className="text-muted-foreground">Errors: {open.detail.errors}</p>}
                    <div className="rounded-md bg-muted/50 p-2 font-mono text-xs">
                      {(open.detail.children ?? []).map((c) => (
                        <VDev key={c.name} n={c} depth={0} />
                      ))}
                    </div>
                  </>
                )}
              </div>
            )}
          </div>
        ))}
      </CardContent>
      {createDialog}
    </Card>
  );
}

function VDev({ n, depth }: { n: PVEZNode; depth: number }) {
  return (
    <>
      <div className="flex gap-3 py-0.5" style={{ paddingLeft: `${depth * 1.25}rem` }}>
        <span className="min-w-32">{n.name}</span>
        <span className={n.state && n.state !== "ONLINE" ? "text-destructive" : "text-success"}>{n.state}</span>
        <span className="text-muted-foreground">
          r{n.read ?? 0} w{n.write ?? 0} c{n.cksum ?? 0}
        </span>
      </div>
      {(n.children ?? []).map((c) => (
        <VDev key={c.name} n={c} depth={depth + 1} />
      ))}
    </>
  );
}

function ZFSCreateDialog({ cluster, node, onClose, onCreated }: { cluster: string; node: string; onClose: () => void; onCreated: () => void }) {
  const [disks, setDisks] = useState<PVEDisk[] | null>(null);
  const [f, setF] = useState({ name: "", raidlevel: "mirror", ashift: 12, compression: "lz4", add_storage: true, devices: [] as string[] });
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState("");
  useEffect(() => {
    api
      .pveDisks(cluster, node)
      .then((d) => setDisks(d.filter(diskFree)))
      .catch((e) => setErr((e as Error).message));
  }, [cluster, node]);
  const min = raidMin[f.raidlevel];
  const n = f.devices.length;
  const countOK = f.raidlevel === "single" ? n === 1 : n >= min && (f.raidlevel !== "raid10" || n % 2 === 0);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setErr("");
    setBusy("Creating the pool");
    try {
      const { upid } = await api.pveZFSCreate(cluster, node, f);
      const fail = await waitTask(cluster, node, upid);
      if (fail) throw new Error(fail);
      onCreated();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy("");
    }
  };

  return (
    <Dialog open onOpenChange={(o) => !o && !busy && onClose()}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>Create a ZFS pool on {node}</DialogTitle>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4">
          <div className="grid grid-cols-2 gap-3">
            <div className="grid gap-1.5">
              <Label htmlFor="z-name">Name</Label>
              <Input id="z-name" value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} placeholder="tank" required />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="z-raid">RAID level</Label>
              <Select id="z-raid" value={f.raidlevel} onChange={(e) => setF({ ...f, raidlevel: e.target.value })}>
                {Object.entries(raidMin).map(([k, v]) => (
                  <option key={k} value={k}>
                    {k} ({k === "single" ? "1 disk" : `${v}+ disks`})
                  </option>
                ))}
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="z-comp">Compression</Label>
              <Select id="z-comp" value={f.compression} onChange={(e) => setF({ ...f, compression: e.target.value })}>
                {["lz4", "zstd", "on", "off", "gzip", "lzjb", "zle"].map((c) => (
                  <option key={c}>{c}</option>
                ))}
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="z-ashift">ashift</Label>
              <Select id="z-ashift" value={f.ashift} onChange={(e) => setF({ ...f, ashift: Number(e.target.value) })}>
                <option value={12}>12 (4K sectors, most disks)</option>
                <option value={13}>13 (8K sectors)</option>
                <option value={9}>9 (512-byte sectors)</option>
              </Select>
            </div>
          </div>
          <fieldset className="grid gap-2">
            <legend className="mb-1 text-sm font-medium">Disks</legend>
            {!disks && !err && <p className="text-sm text-muted-foreground">Loading…</p>}
            {disks && disks.length === 0 && <p className="text-sm text-muted-foreground">No free disks on {node}. Wipe one first.</p>}
            {(disks ?? []).map((d) => (
              <label key={d.devpath} className="flex cursor-pointer items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  className="accent-brand"
                  checked={f.devices.includes(d.devpath)}
                  onChange={() => setF({ ...f, devices: f.devices.includes(d.devpath) ? f.devices.filter((x) => x !== d.devpath) : [...f.devices, d.devpath] })}
                />
                <span className="font-mono text-xs">{d.devpath}</span>
                <span className="text-muted-foreground">
                  {d.model} · {formatBytes(d.size)}
                </span>
                {d.health && d.health !== "PASSED" && d.health !== "OK" && healthBadge(d.health)}
              </label>
            ))}
            {!countOK && (
              <p className="text-xs text-muted-foreground">
                {f.raidlevel === "single" ? "Pick exactly one disk." : f.raidlevel === "raid10" ? `Pick an even number of disks, at least ${min}.` : `Pick at least ${min} disks.`}
              </p>
            )}
          </fieldset>
          <label className="flex cursor-pointer items-center gap-2 text-sm">
            <input type="checkbox" className="accent-brand" checked={f.add_storage} onChange={(e) => setF({ ...f, add_storage: e.target.checked })} />
            Add as storage for VM disks and containers
          </label>
          {err && <p className="text-sm text-destructive">{err}</p>}
          {busy && <p className="text-sm text-muted-foreground">{busy}…</p>}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose} disabled={!!busy}>
              Cancel
            </Button>
            <Button type="submit" disabled={!!busy || !countOK}>
              Create pool
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
