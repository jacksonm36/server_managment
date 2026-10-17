import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link } from "wouter";
import {
  Camera,
  PanelLeftClose,
  PanelLeftOpen,
  Pencil,
  Plus,
  RefreshCw,
  Rocket,
  ShieldCheck,
  Trash2,
  Undo2,
} from "lucide-react";
import { api, type PVECluster, type PVEGuest, type PVEGuestRef, type PVEResources, type PVESnapshot, type PVETenantInfo } from "@/lib/api";
import { useAccess, type Access } from "@/lib/access";
import { formatBytes } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input, Label } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { FlavorPicker, useFlavors } from "@/components/Flavors";
import { CloudInitFields, ciOpts, emptyCI, type CIForm } from "@/components/CloudInitFields";
import { PVEOverview } from "@/components/PVEOverview";
import { VLANInput, vlanNum } from "@/components/VLANField";
import { ComputeTree } from "@/components/ComputeTree";
import { PVENodeView } from "@/components/PVENodeView";
import { CreateGuestDialog } from "@/components/PVEForms";
import { DatacenterView, NodeAdmin } from "@/pages/ProxmoxAdmin";
import { ProxmoxGuest } from "@/pages/ProxmoxGuest";

export function Proxmox() {
  const ac = useAccess();
  const [clusters, setClusters] = useState<PVECluster[] | null>(null);
  const [cur, setCur] = useState("");
  const [editing, setEditing] = useState<PVECluster | "new" | null>(null);
  const [err, setErr] = useState("");

  const load = useCallback(async () => {
    try {
      const c = await api.pveClusters();
      setClusters(c);
      setCur((x) => (c.some((k) => k.id === x) ? x : (c[0]?.id ?? "")));
    } catch (e) {
      setErr((e as Error).message);
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);
  const current = clusters?.find((c) => c.id === cur);

  return (
    <div className="grid gap-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Proxmox VE</h1>
          <p className="text-sm text-muted-foreground">VMs and containers on your Proxmox clusters, and new VMs that join servermgr on their own.</p>
        </div>
        {ac.can("proxmox.manage") && (
          <Button size="sm" variant="secondary" onClick={() => setEditing("new")}>
            <Plus /> Add cluster
          </Button>
        )}
      </div>
      {err && <p className="text-sm text-destructive">{err}</p>}
      {clusters && clusters.length === 0 && (
        <Card className="p-8 text-center text-sm text-muted-foreground">
          No Proxmox cluster yet. {ac.can("proxmox.manage") ? "Add one with an API token (Datacenter → Permissions → API Tokens)." : "Ask an admin to add one."}
        </Card>
      )}
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
      {current && <ClusterView key={current.id} cluster={current} ac={ac} onEdit={() => setEditing(current)} onDeleted={load} />}
      {editing && (
        <ClusterDialog
          cluster={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={(id) => {
            setEditing(null);
            // Load the list first: a new cluster isn't in it yet.
            void load().then(() => setCur(id));
          }}
        />
      )}
    </div>
  );
}

const TREE_KEY = "sm.pve.tree-width";
const TREE_HIDE_KEY = "sm.pve.tree-hidden";

// Until someone drags it, the guest list takes a share of the window.
const autoTreeW = () => Math.round(Math.min(360, Math.max(220, window.innerWidth * 0.14)));

function ClusterView({ cluster, ac, onEdit, onDeleted }: { cluster: PVECluster; ac: Access; onEdit: () => void; onDeleted: () => void }) {
  const [res, setRes] = useState<PVEResources | null>(null);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [node, setNode] = useState(""); // picked in the compute tree; "" = the whole cluster
  const [guest, setGuest] = useState<PVEGuest | null>(null); // picked in the tree: shown in the pane
  const [view, setView] = useState<"overview" | "datacenter">("overview");
  const [create, setCreate] = useState<"qemu" | "lxc" | null>(null);
  const [fixedW, setTreeW] = useState<number | null>(() => {
    try {
      const w = Number(localStorage.getItem(TREE_KEY));
      return w ? Math.min(520, Math.max(200, w)) : null;
    } catch {
      return null;
    }
  });
  const [autoW, setAutoW] = useState(autoTreeW);
  useEffect(() => {
    const on = () => setAutoW(autoTreeW());
    window.addEventListener("resize", on);
    return () => window.removeEventListener("resize", on);
  }, []);
  const treeW = fixedW ?? autoW;
  const [treeHidden, setTreeHidden] = useState(() => {
    try {
      return localStorage.getItem(TREE_HIDE_KEY) === "1";
    } catch {
      return false;
    }
  });
  const toggleTree = () => {
    const h = !treeHidden;
    setTreeHidden(h);
    try {
      if (h) localStorage.setItem(TREE_HIDE_KEY, "1");
      else localStorage.removeItem(TREE_HIDE_KEY);
    } catch {
      /* private window */
    }
  };
  const pane = useRef<HTMLDivElement>(null);

  const load = useCallback(async () => {
    try {
      setRes(await api.pveResources(cluster.id));
    } catch (e) {
      setMsg({ ok: false, text: (e as Error).message });
    }
  }, [cluster.id]);
  useEffect(() => {
    void load();
    const t = setInterval(load, 10000);
    return () => clearInterval(t);
  }, [load]);

  const nodeInfo = res?.nodes?.find((n) => n.node === node);
  const onlineNodes = useMemo(() => (res?.nodes ?? []).filter((n) => n.status === "online").map((n) => n.node).sort(), [res]);
  // Where new guests can go: online nodes, or (for a tenant, who sees no
  // nodes) the nodes its guests run on.
  const createNodes = useMemo(() => (onlineNodes.length ? onlineNodes : [...new Set((res?.guests ?? []).map((g) => g.node))].sort()), [onlineNodes, res]);
  const manage = ac.can("proxmox.manage");

  const pick = (g: PVEGuest | null, n = "") => {
    setGuest(g);
    setNode(n);
    // On a phone the pane is under the tree.
    if (window.matchMedia("(max-width: 1023px)").matches) pane.current?.scrollIntoView({ behavior: "smooth", block: "start" });
  };
  // Dragging the tree's right edge sets its width (kept per browser).
  const resize = (e: React.PointerEvent) => {
    e.preventDefault();
    const x0 = e.clientX;
    const w0 = treeW;
    let w = w0;
    const move = (m: PointerEvent) => {
      w = Math.min(520, Math.max(200, w0 + m.clientX - x0));
      setTreeW(w);
    };
    const up = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
      document.body.style.removeProperty("cursor");
      document.body.style.removeProperty("user-select");
      try {
        localStorage.setItem(TREE_KEY, String(w));
      } catch {
        /* private window */
      }
    };
    document.body.style.cursor = "col-resize";
    document.body.style.userSelect = "none";
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  };

  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <span className="font-medium">{cluster.name}</span>
          {cluster.url && <span className="font-mono text-xs text-muted-foreground">{cluster.url}</span>}
          {res?.version && <Badge variant="outline">PVE {res.version}</Badge>}
          {cluster.fingerprint && (
            <Badge variant="success" className="gap-1" title={`Pinned certificate SHA-256 ${cluster.fingerprint}`}>
              <ShieldCheck className="size-3" /> pinned
            </Badge>
          )}
          {res?.tenant && (
            <Badge variant="success" className="gap-1" title="Your organization's own pool, isolated by Proxmox">
              <ShieldCheck className="size-3" /> pool {res.tenant.pool}
            </Badge>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-1">
          {ac.can("proxmox.guests") && createNodes.length > 0 && (
            <>
              <Button size="sm" variant="secondary" onClick={() => setCreate("qemu")} title="A VM that boots from an ISO">
                <Plus /> Create VM
              </Button>
              <Button size="sm" variant="secondary" onClick={() => setCreate("lxc")} title="A container from a template">
                <Plus /> Create CT
              </Button>
            </>
          )}
          {ac.can("proxmox.guests") && (
            <Link href="/deploy" className="inline-flex h-8 items-center gap-1.5 rounded-md px-3 text-sm font-medium text-muted-foreground hover:bg-accent hover:text-foreground">
              <Rocket className="size-4" /> Deploy
            </Link>
          )}
          <Button
            size="icon"
            variant="ghost"
            className="size-8"
            aria-label={treeHidden ? "Show guest list" : "Hide guest list"}
            aria-pressed={!treeHidden}
            title={treeHidden ? "Show guest list" : "Hide guest list"}
            onClick={toggleTree}
          >
            {treeHidden ? <PanelLeftOpen className="size-4" /> : <PanelLeftClose className="size-4" />}
          </Button>
          <Button size="icon" variant="ghost" className="size-8" aria-label="Refresh" onClick={load}>
            <RefreshCw className="size-4" />
          </Button>
          {manage && (
            <>
              <Button size="icon" variant="ghost" className="size-8" aria-label="Edit cluster" onClick={onEdit}>
                <Pencil className="size-4" />
              </Button>
              <Button
                size="icon"
                variant="ghost"
                className="size-8 text-destructive"
                aria-label="Remove cluster"
                onClick={() => {
                  if (confirm(`Remove "${cluster.name}" from servermgr? Nothing on Proxmox is changed.`)) void api.deletePVE(cluster.id).then(onDeleted);
                }}
              >
                <Trash2 className="size-4" />
              </Button>
            </>
          )}
        </div>
      </div>
      {res?.error && <p className="rounded-lg border border-destructive/30 bg-destructive/10 px-4 py-2 text-sm text-destructive">{res.error}</p>}
      {msg && (
        <p className={`rounded-lg border px-4 py-2 text-sm ${msg.ok ? "border-success/30 bg-success/10 text-success" : "border-destructive/30 bg-destructive/10 text-destructive"}`}>{msg.text}</p>
      )}

      <div
        className={`grid grid-cols-[minmax(0,1fr)] gap-4 lg:items-start ${treeHidden ? "" : "lg:grid-cols-[var(--tree-w)_minmax(0,1fr)]"}`}
        style={{ "--tree-w": `${treeW}px` } as React.CSSProperties}
      >
        {res && !res.error && !treeHidden && (
          <div className="relative lg:sticky lg:top-20">
            <Card className="max-h-[60vh] overflow-y-auto p-3 lg:max-h-[calc(100vh-6rem)]">
              <ComputeTree
                cluster={cluster.id}
                name={cluster.name}
                res={res}
                selected={node}
                onSelect={(n) => pick(null, n)}
                guest={guest?.id ?? ""}
                onGuest={(g) => pick(g, g.node)}
              />
            </Card>
            <div
              role="separator"
              aria-orientation="vertical"
              aria-label="Resize the guest list"
              aria-valuenow={treeW}
              tabIndex={0}
              onPointerDown={resize}
              onDoubleClick={() => {
                setTreeW(null);
                try {
                  localStorage.removeItem(TREE_KEY);
                } catch {
                  /* private window */
                }
              }}
              title="Drag to resize, double-click to fit the window"
              onKeyDown={(e) => {
                const d = e.key === "ArrowLeft" ? -16 : e.key === "ArrowRight" ? 16 : 0;
                if (!d) return;
                e.preventDefault();
                const w = Math.min(520, Math.max(200, treeW + d));
                setTreeW(w);
                try {
                  localStorage.setItem(TREE_KEY, String(w));
                } catch {
                  /* private window */
                }
              }}
              className="group absolute top-0 -right-3 hidden h-full w-3 cursor-col-resize items-center justify-center outline-none lg:flex"
            >
              <span className="h-12 w-1 rounded-full bg-border transition-colors group-hover:bg-brand group-focus-visible:bg-brand" />
            </div>
          </div>
        )}
        <div ref={pane} className={`grid min-w-0 scroll-mt-20 grid-cols-[minmax(0,1fr)] gap-4 ${treeHidden ? "" : "lg:col-start-2"}`}>
          {res?.tenant && !guest && !node && <TenantUsageCard t={res.tenant} />}

          {res &&
            !res.error &&
            (guest ? (
              <Card className="p-4 sm:p-5">
                <ProxmoxGuest key={guest.id} cluster={cluster.id} node={guest.node} type={guest.type} vmid={String(guest.vmid)} embedded onGone={() => (pick(null), void load())} />
              </Card>
            ) : nodeInfo ? (
              manage && nodeInfo.status === "online" ? (
                <NodeAdmin key={node} cluster={cluster.id} node={node} nodes={onlineNodes} guests={(res.guests ?? []).filter((g) => g.node === node)} onGuest={(g) => pick(g, g.node)} />
              ) : (
                <PVENodeView
                  key={node}
                  cluster={cluster.id}
                  node={node}
                  online={nodeInfo.status === "online"}
                  guests={(res.guests ?? []).filter((g) => g.node === node)}
                  onGuest={(g) => pick(g, g.node)}
                />
              )
            ) : (
              <>
                {manage && (
                  <div className="flex w-fit gap-1 rounded-lg border p-0.5" role="group" aria-label="Cluster view">
                    {(
                      [
                        ["overview", "Overview"],
                        ["datacenter", "Datacenter"],
                      ] as const
                    ).map(([id, label]) => (
                      <button
                        key={id}
                        className={`cursor-pointer rounded-md px-3 py-1 text-sm ${view === id ? "bg-accent text-foreground" : "text-muted-foreground hover:text-foreground"}`}
                        aria-pressed={view === id}
                        onClick={() => setView(id)}
                      >
                        {label}
                      </button>
                    ))}
                  </div>
                )}
                {manage && view === "datacenter" ? <DatacenterView cluster={cluster} res={res} /> : <PVEOverview res={res} onGuest={(g) => pick(g, g.node)} />}
              </>
            ))}
        </div>
      </div>

      {create && (
        <CreateGuestDialog
          cluster={cluster.id}
          nodes={createNodes}
          type={create}
          onClose={() => {
            setCreate(null);
            void load();
          }}
        />
      )}
    </div>
  );
}

function TenantUsageCard({ t }: { t: PVETenantInfo }) {
  const rows: { label: string; used: number; max: number; fmt: (n: number) => string }[] = [
    { label: "Guests", used: t.usage.guests, max: t.quota.guests, fmt: String },
    { label: "vCPUs", used: t.usage.cores, max: t.quota.cores, fmt: String },
    { label: "Memory", used: t.usage.memory_mb, max: t.quota.memory_mb, fmt: (n) => formatBytes(n * 1024 * 1024) },
    { label: "Disk", used: t.usage.disk_gb, max: t.quota.disk_gb, fmt: (n) => `${n} GB` },
  ];
  return (
    <Card className="p-4">
      <div className="flex flex-wrap items-center justify-between gap-2 text-sm">
        <span className="font-medium">{t.name}</span>
        <span className="text-xs text-muted-foreground">
          Storage: {t.storages.join(", ") || "none"} · Network: {t.bridges.join(", ") || "none"}
        </span>
      </div>
      <div className="grid gap-x-6 sm:grid-cols-2 lg:grid-cols-4">
        {rows.map((r) => (
          <Meter
            key={r.label}
            label={r.label}
            value={r.max > 0 ? (r.used / r.max) * 100 : 0}
            text={r.max > 0 ? `${r.fmt(r.used)} of ${r.fmt(r.max)}` : `${r.fmt(r.used)} (no limit)`}
          />
        ))}
      </div>
    </Card>
  );
}

function Meter({ label, value, text }: { label: string; value: number; text: string }) {
  const v = Math.max(0, Math.min(100, value));
  return (
    <div className="mt-2">
      <div className="flex justify-between text-xs text-muted-foreground">
        <span>{label}</span>
        <span>{text}</span>
      </div>
      <div className="mt-1 h-1.5 rounded-full bg-muted">
        <div className={`h-1.5 rounded-full ${v > 85 ? "bg-destructive" : v > 65 ? "bg-warning" : "bg-brand"}`} style={{ width: `${v}%` }} />
      </div>
    </div>
  );
}

function ClusterDialog({ cluster, onClose, onSaved }: { cluster: PVECluster | null; onClose: () => void; onSaved: (id: string) => void }) {
  const [f, setF] = useState({ name: cluster?.name ?? "", url: cluster?.url ?? "https://", token_id: cluster?.token_id ?? "", token_secret: "" });
  const [probe, setProbe] = useState<{ fingerprint: string; subject: string } | null>(cluster ? { fingerprint: cluster.fingerprint ?? "", subject: "" } : null);
  const [trusted, setTrusted] = useState(!!cluster);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const set = (k: keyof typeof f) => (e: React.ChangeEvent<HTMLInputElement>) => setF({ ...f, [k]: e.target.value });

  async function fetchCert() {
    setErr("");
    setBusy(true);
    try {
      setProbe(await api.pveProbe(f.url.trim()));
      setTrusted(false);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{cluster ? `Edit ${cluster.name}` : "Add a Proxmox cluster"}</DialogTitle>
          <DialogDescription>
            Use an API token (Datacenter → Permissions → API Tokens), for example with the PVEVMAdmin role. The secret is encrypted and
            never shown again.
          </DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-3"
          onSubmit={async (e) => {
            e.preventDefault();
            if (!probe || !trusted) return setErr("Fetch and confirm the certificate fingerprint first.");
            setBusy(true);
            setErr("");
            try {
              const r = await api.savePVE(cluster?.id ?? "", { ...f, fingerprint: probe.fingerprint });
              onSaved(r.cluster.id);
            } catch (e) {
              setErr((e as Error).message);
            } finally {
              setBusy(false);
            }
          }}
        >
          <div className="grid gap-1.5">
            <Label htmlFor="pv-name">Name</Label>
            <Input id="pv-name" value={f.name} onChange={set("name")} placeholder="homelab" required />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="pv-url">API URL</Label>
            <div className="flex gap-2">
              <Input id="pv-url" value={f.url} onChange={set("url")} placeholder="https://pve1.lan:8006" required />
              <Button type="button" variant="secondary" disabled={busy || f.url.length < 10} onClick={fetchCert}>
                Fetch certificate
              </Button>
            </div>
          </div>
          {probe && (
            <div className="grid gap-2 rounded-lg border p-3 text-sm">
              <div className="text-xs text-muted-foreground">Certificate SHA-256 fingerprint{probe.subject ? ` (${probe.subject})` : ""}</div>
              <code className="font-mono text-xs break-all">{probe.fingerprint}</code>
              <label className="flex items-start gap-2 text-xs">
                <input type="checkbox" className="mt-0.5" checked={trusted} onChange={(e) => setTrusted(e.target.checked)} />
                <span>
                  It matches the node's certificate (Proxmox: node → System → Certificates, or <code>pvenode cert info</code>). servermgr
                  will only ever talk to a server presenting this exact certificate.
                </span>
              </label>
            </div>
          )}
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="grid gap-1.5">
              <Label htmlFor="pv-tid">Token ID</Label>
              <Input id="pv-tid" value={f.token_id} onChange={set("token_id")} placeholder="servermgr@pve!panel" required />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="pv-sec">Token secret</Label>
              <Input
                id="pv-sec"
                type="password"
                autoComplete="off"
                value={f.token_secret}
                onChange={set("token_secret")}
                placeholder={cluster ? "unchanged" : ""}
                required={!cluster}
              />
            </div>
          </div>
          {err && <p className="text-sm text-destructive">{err}</p>}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" variant="brand" disabled={busy || !probe || !trusted}>
              Test &amp; save
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

export function SnapshotsDialog({ cluster, guest, ac, onClose }: { cluster: PVECluster; guest: PVEGuestRef & { name: string }; ac: Access; onClose: () => void }) {
  const [list, setList] = useState<PVESnapshot[] | null>(null);
  const [name, setName] = useState("");
  const [desc, setDesc] = useState("");
  const [vmstate, setVmstate] = useState(false);
  const [busy, setBusy] = useState("");
  const [err, setErr] = useState("");
  const load = useCallback(() => api.pveSnapshots(cluster.id, guest).then(setList).catch((e) => setErr((e as Error).message)), [cluster.id, guest]);
  useEffect(() => {
    void load();
  }, [load]);

  async function act(label: string, f: () => Promise<unknown>) {
    setBusy(label);
    setErr("");
    try {
      await f();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy("");
      void load();
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>Snapshots of {guest.name}</DialogTitle>
          <DialogDescription>
            {guest.type === "qemu" ? "VM" : "Container"} {guest.vmid} on {guest.node}
          </DialogDescription>
        </DialogHeader>
        {list === null ? (
          <p className="text-sm text-muted-foreground">Loading…</p>
        ) : list.length === 0 ? (
          <p className="text-sm text-muted-foreground">No snapshots.</p>
        ) : (
          <ul className="divide-y text-sm">
            {[...list]
              .sort((a, b) => (b.snaptime ?? 0) - (a.snaptime ?? 0))
              .map((s) => (
                <li key={s.name} className="flex items-center justify-between gap-2 py-2">
                  <div className="min-w-0">
                    <div className="font-medium">
                      {s.name}
                      {s.vmstate ? <span className="ml-2 text-xs text-muted-foreground">with RAM</span> : null}
                    </div>
                    <div className="truncate text-xs text-muted-foreground">
                      {s.snaptime ? new Date(s.snaptime * 1000).toLocaleString() : ""} {s.description}
                    </div>
                  </div>
                  <div className="flex gap-1">
                    {ac.can("proxmox.guests") && (
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={!!busy}
                        onClick={() => confirm(`Roll ${guest.name} back to "${s.name}"? Changes since then are lost.`) && act("rollback", () => api.pveRollback(cluster.id, guest, s.name))}
                      >
                        <Undo2 /> Roll back
                      </Button>
                    )}
                    {ac.can("proxmox.operate") && (
                      <Button
                        size="icon"
                        variant="ghost"
                        className="size-8 text-destructive"
                        aria-label={`Delete snapshot ${s.name}`}
                        disabled={!!busy}
                        onClick={() => confirm(`Delete snapshot "${s.name}"?`) && act("delete", () => api.pveDeleteSnapshot(cluster.id, guest, s.name))}
                      >
                        <Trash2 className="size-4" />
                      </Button>
                    )}
                  </div>
                </li>
              ))}
          </ul>
        )}
        {ac.can("proxmox.operate") && (
          <form
            className="grid gap-2 border-t pt-3"
            onSubmit={(e) => {
              e.preventDefault();
              void act("create", async () => {
                await api.pveSnapshot(cluster.id, guest, { name, description: desc, vmstate });
                setName("");
                setDesc("");
              });
            }}
          >
            <Label>New snapshot</Label>
            <div className="grid gap-2 sm:grid-cols-2">
              <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="before-upgrade" pattern="[A-Za-z][A-Za-z0-9_\-]{1,39}" required aria-label="Snapshot name" />
              <Input value={desc} onChange={(e) => setDesc(e.target.value)} placeholder="Description (optional)" maxLength={200} aria-label="Description" />
            </div>
            {guest.type === "qemu" && (
              <label className="flex items-center gap-2 text-xs">
                <input type="checkbox" checked={vmstate} onChange={(e) => setVmstate(e.target.checked)} /> Include RAM (slower, resumes exactly where it was)
              </label>
            )}
            <div className="flex justify-end">
              <Button type="submit" variant="secondary" disabled={!!busy}>
                {busy ? (
                  `${busy}…`
                ) : (
                  <>
                    <Camera /> Take snapshot
                  </>
                )}
              </Button>
            </div>
          </form>
        )}
        {err && <p className="text-sm text-destructive">{err}</p>}
      </DialogContent>
    </Dialog>
  );
}

export function DeleteDialog({ cluster, guest, onClose, onDone }: { cluster: string; guest: PVEGuestRef & { name: string }; onClose: () => void; onDone: (msg: string) => void }) {
  const [confirmName, setConfirmName] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Delete {guest.name}?</DialogTitle>
          <DialogDescription>
            Destroys {guest.type === "qemu" ? "VM" : "container"} {guest.vmid} and its disks on {guest.node}. This cannot be undone.
          </DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-3"
          onSubmit={async (e) => {
            e.preventDefault();
            setBusy(true);
            setErr("");
            try {
              await api.pveDeleteGuest(cluster, guest, confirmName);
              onDone(`${guest.name} deleted.`);
            } catch (e) {
              setErr((e as Error).message);
            } finally {
              setBusy(false);
            }
          }}
        >
          <Label htmlFor="pv-del">
            Type <code className="rounded bg-muted px-1">{guest.name}</code> to confirm
          </Label>
          <Input id="pv-del" value={confirmName} onChange={(e) => setConfirmName(e.target.value)} autoComplete="off" />
          {err && <p className="text-sm text-destructive">{err}</p>}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" variant="destructive" disabled={busy || confirmName !== guest.name}>
              <Trash2 /> Delete
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

export function ProvisionDialog({
  cluster,
  template,
  nodes,
  storages,
  tenant,
  onClose,
  onStarted,
}: {
  cluster: PVECluster;
  template: PVEGuest;
  nodes: string[];
  storages: string[];
  tenant?: PVETenantInfo;
  onClose: () => void;
  onStarted: () => void;
}) {
  const [f, setF] = useState({
    name: "",
    target: "",
    cores: 2,
    memory_mb: (template.tags ?? "").split(/[;, ]/).includes("windows") ? 4096 : 2048,
    disk_gb: 0,
    full_clone: true,
    ipMode: "dhcp",
    ip: "",
    gateway: "",
    enroll: true,
    storage: tenant?.storages.length === 1 ? tenant.storages[0] : "",
    bridge: tenant?.bridges.length === 1 ? tenant.bridges[0] : "",
    vlan: "",
  });
  const storageChoices = tenant ? tenant.storages : storages;
  const flavors = useFlavors();
  const [flavor, setFlavor] = useState<number | null>(null);
  const [ci, setCI] = useState<CIForm>(emptyCI);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  // Windows templates use Cloudbase-Init: Administrator password, hostname
  // and network; no SSH keys or agent enrollment.
  const win = (template.tags ?? "").split(/[;, ]/).includes("windows");
  const [showPw, setShowPw] = useState(false);
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[92vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>New VM from {template.name}</DialogTitle>
          <DialogDescription>
            {win
              ? "Clones the Windows template and starts it. Cloudbase-Init sets the Administrator password, the computer name and the network on first boot, then Windows restarts once."
              : "Clones the template, sets cloud-init (the panel's SSH key, your user, keys and network), starts it and, if enrolled, installs the servermgr agent over SSH. The template needs cloud-init and qemu-guest-agent (for DHCP)."}
          </DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-3"
          onSubmit={async (e) => {
            e.preventDefault();
            setBusy(true);
            setErr("");
            try {
              await api.pveProvision(cluster.id, {
                template_vmid: template.vmid,
                node: template.node,
                target: f.target,
                name: f.name.trim(),
                cores: f.cores,
                memory_mb: f.memory_mb,
                disk_gb: f.disk_gb,
                full_clone: f.full_clone,
                ip: f.ipMode === "dhcp" ? "dhcp" : f.ip.trim(),
                gateway: f.ipMode === "dhcp" ? "" : f.gateway.trim(),
                enroll: win ? false : f.enroll,
                storage: f.storage,
                bridge: f.bridge.trim(),
                vlan: vlanNum(f.vlan),
                ...(win ? { cipassword: ci.cipassword, nameserver: ci.nameserver.trim(), tags: ciOpts(ci).tags } : ciOpts(ci)),
              });
              onStarted();
            } catch (e) {
              setErr((e as Error).message);
            } finally {
              setBusy(false);
            }
          }}
        >
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="grid gap-1.5">
              <Label htmlFor="pp-name">{win ? "Computer name" : "Hostname"}</Label>
              <Input
                id="pp-name"
                value={f.name}
                onChange={(e) => setF({ ...f, name: e.target.value })}
                placeholder={win ? "app-win-01" : "web-03"}
                pattern="[a-z0-9]([a-z0-9\-]*[a-z0-9])?"
                maxLength={win ? 15 : 63}
                title={win ? "At most 15 characters: lowercase letters, digits, dashes" : undefined}
                required
              />
            </div>
            {!tenant && (
              <div className="grid gap-1.5">
                <Label htmlFor="pp-node">Node</Label>
                <Select id="pp-node" value={f.target} onChange={(e) => setF({ ...f, target: e.target.value })}>
                  <option value="">{template.node} (same as template)</option>
                  {nodes
                    .filter((n) => n !== template.node)
                    .map((n) => (
                      <option key={n} value={n}>
                        {n}
                      </option>
                    ))}
                </Select>
              </div>
            )}
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="grid gap-1.5">
              <Label htmlFor="pp-storage">Disk storage</Label>
              <Select id="pp-storage" value={f.storage} required={!!tenant} onChange={(e) => setF({ ...f, storage: e.target.value })}>
                {tenant ? <option value="">Pick one…</option> : <option value="">Same as the template</option>}
                {storageChoices.map((x) => (
                  <option key={x} value={x}>
                    {x}
                  </option>
                ))}
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="pp-bridge">Network bridge</Label>
              {tenant ? (
                <Select id="pp-bridge" value={f.bridge} required onChange={(e) => setF({ ...f, bridge: e.target.value })}>
                  <option value="">Pick one…</option>
                  {tenant.bridges.map((x) => (
                    <option key={x} value={x}>
                      {x}
                    </option>
                  ))}
                </Select>
              ) : (
                <Input id="pp-bridge" value={f.bridge} onChange={(e) => setF({ ...f, bridge: e.target.value })} placeholder="Same as the template (e.g. vmbr0)" />
              )}
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="pp-vlan">VLAN</Label>
              <VLANInput id="pp-vlan" value={f.vlan} onChange={(v) => setF({ ...f, vlan: v })} allowed={tenant ? (tenant.vlans ?? []) : undefined} />
            </div>
          </div>
          <FlavorPicker
            flavors={flavors}
            picked={flavor}
            onPick={(x) => {
              setFlavor(x ? x.id : null);
              if (x) setF({ ...f, cores: x.cores, memory_mb: x.memory_mb, disk_gb: x.disk_gb });
            }}
          />
          <div className="grid grid-cols-3 gap-3">
            <Num id="pp-cores" label="vCPUs" value={f.cores} min={1} max={128} onChange={(v) => {
              setFlavor(null);
              setF({ ...f, cores: v });
            }} />
            <Num id="pp-mem" label="Memory (MB)" value={f.memory_mb} min={256} max={1048576} step={256} onChange={(v) => {
              setFlavor(null);
              setF({ ...f, memory_mb: v });
            }} />
            <Num id="pp-disk" label="Disk (GB, 0 = template's)" value={f.disk_gb} min={0} max={10000} onChange={(v) => {
              setFlavor(null);
              setF({ ...f, disk_gb: v });
            }} />
          </div>
          <div className="grid gap-1.5">
            <Label>Network</Label>
            <div className="flex flex-wrap gap-3 text-sm">
              {["dhcp", "static"].map((m) => (
                <label key={m} className="flex items-center gap-1.5">
                  <input type="radio" name="ipmode" checked={f.ipMode === m} onChange={() => setF({ ...f, ipMode: m })} /> {m === "dhcp" ? "DHCP" : "Static"}
                </label>
              ))}
            </div>
            {f.ipMode === "static" && (
              <div className="grid gap-2 sm:grid-cols-2">
                <Input value={f.ip} onChange={(e) => setF({ ...f, ip: e.target.value })} placeholder="10.0.0.50/24" required aria-label="IP/CIDR" />
                <Input value={f.gateway} onChange={(e) => setF({ ...f, gateway: e.target.value })} placeholder="Gateway 10.0.0.1" aria-label="Gateway" />
              </div>
            )}
          </div>
          {!tenant && (
            <label className="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={f.full_clone} onChange={(e) => setF({ ...f, full_clone: e.target.checked })} /> Full clone (independent of the
              template)
            </label>
          )}
          {win ? (
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="grid gap-1.5">
                <Label htmlFor="pp-winpw">Administrator password</Label>
                <div className="flex gap-2">
                  <Input
                    id="pp-winpw"
                    type={showPw ? "text" : "password"}
                    autoComplete="new-password"
                    value={ci.cipassword}
                    onChange={(e) => setCI({ ...ci, cipassword: e.target.value })}
                    minLength={12}
                    required
                  />
                  <Button type="button" variant="ghost" size="sm" onClick={() => setShowPw((x) => !x)}>
                    {showPw ? "Hide" : "Show"}
                  </Button>
                </div>
                <p className="text-xs text-muted-foreground">At least 12 characters, with three of: lower case, upper case, digits, symbols.</p>
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="pp-windns">DNS servers</Label>
                <Input id="pp-windns" value={ci.nameserver} onChange={(e) => setCI({ ...ci, nameserver: e.target.value })} placeholder="From DHCP, or 1.1.1.1 9.9.9.9" />
              </div>
            </div>
          ) : (
            <>
              <details className="rounded-lg border px-3 py-2">
                <summary className="cursor-pointer text-sm font-medium select-none">Cloud-init: user, SSH keys, DNS, tags</summary>
                <div className="mt-3">
                  <CloudInitFields value={ci} onChange={setCI} idPrefix="pp-ci" />
                </div>
              </details>
              <label className="flex items-start gap-2 text-sm">
                <input type="checkbox" className="mt-1" checked={f.enroll} onChange={(e) => setF({ ...f, enroll: e.target.checked })} />
                <span>
                  Enroll into servermgr
                  <span className="block text-xs text-muted-foreground">Installs the agent, links the VM to its server and prepares it for Ansible.</span>
                </span>
              </label>
            </>
          )}
          {err && <p className="text-sm text-destructive">{err}</p>}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" variant="brand" disabled={busy}>
              Create VM
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function Num(p: { id: string; label: string; value: number; min: number; max: number; step?: number; onChange: (v: number) => void }) {
  return (
    <div className="grid gap-1.5">
      <Label htmlFor={p.id} className="text-xs">
        {p.label}
      </Label>
      <Input id={p.id} type="number" value={p.value} min={p.min} max={p.max} step={p.step ?? 1} onChange={(e) => p.onChange(Number(e.target.value))} />
    </div>
  );
}
