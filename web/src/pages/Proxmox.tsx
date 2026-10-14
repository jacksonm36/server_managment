import { useCallback, useEffect, useMemo, useState } from "react";
import { Link } from "wouter";
import {
  Camera,
  Copy as CloneIcon,
  Pause,
  Pencil,
  Play,
  Plus,
  Power,
  RefreshCw,
  RotateCw,
  ShieldCheck,
  Square,
  Trash2,
  Undo2,
} from "lucide-react";
import { api, type PVECluster, type PVEGuest, type PVEProvision, type PVEResources, type PVESnapshot, type PVETenantInfo } from "@/lib/api";
import { useAccess, type Access } from "@/lib/access";
import { formatBytes, formatUptime, pct, timeAgo } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input, Label } from "@/components/ui/input";
import { Select } from "@/components/ui/select";

const statusVariant = (s: string) => (s === "running" || s === "online" ? "success" : s === "stopped" ? "outline" : "warning");

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
      {cur && clusters && (
        <ClusterView
          key={cur}
          cluster={clusters.find((c) => c.id === cur)!}
          ac={ac}
          onEdit={() => setEditing(clusters.find((c) => c.id === cur)!)}
          onDeleted={load}
        />
      )}
      {editing && (
        <ClusterDialog
          cluster={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={(id) => {
            setEditing(null);
            setCur(id);
            void load();
          }}
        />
      )}
    </div>
  );
}

function ClusterView({ cluster, ac, onEdit, onDeleted }: { cluster: PVECluster; ac: Access; onEdit: () => void; onDeleted: () => void }) {
  const [res, setRes] = useState<PVEResources | null>(null);
  const [provs, setProvs] = useState<PVEProvision[]>([]);
  const [busy, setBusy] = useState<Record<number, string>>({});
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [filter, setFilter] = useState("");
  const [snaps, setSnaps] = useState<PVEGuest | null>(null);
  const [del, setDel] = useState<PVEGuest | null>(null);
  const [newVM, setNewVM] = useState<PVEGuest | null>(null);

  const load = useCallback(async () => {
    try {
      const [r, p] = await Promise.all([api.pveResources(cluster.id), ac.can("proxmox.operate") ? api.pveProvisions() : Promise.resolve([])]);
      setRes(r);
      setProvs(p.filter((x) => x.cluster_id === cluster.id));
    } catch (e) {
      setMsg({ ok: false, text: (e as Error).message });
    }
  }, [cluster.id, ac]);
  const provisioning = provs.some((p) => p.status === "running");
  useEffect(() => {
    void load();
    const t = setInterval(load, provisioning ? 3000 : 10000);
    return () => clearInterval(t);
  }, [load, provisioning]);

  async function power(g: PVEGuest, action: string) {
    setBusy((b) => ({ ...b, [g.vmid]: action }));
    setMsg(null);
    try {
      await api.pvePower(cluster.id, g, action);
      setMsg({ ok: true, text: `${g.name}: ${action} done.` });
    } catch (e) {
      setMsg({ ok: false, text: `${g.name}: ${(e as Error).message}` });
    } finally {
      setBusy((b) => ({ ...b, [g.vmid]: "" }));
      void load();
    }
  }

  const guests = useMemo(() => (res?.guests ?? []).filter((g) => !g.template), [res]);
  const templates = useMemo(() => (res?.guests ?? []).filter((g) => g.template && g.type === "qemu"), [res]);
  const shown = guests.filter((g) => !filter || `${g.vmid} ${g.name} ${g.node} ${g.status} ${g.server_name ?? ""}`.toLowerCase().includes(filter.toLowerCase()));

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
        <div className="flex gap-1">
          <Button size="icon" variant="ghost" className="size-8" aria-label="Refresh" onClick={load}>
            <RefreshCw className="size-4" />
          </Button>
          {ac.can("proxmox.manage") && (
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

      {res?.tenant && <TenantUsageCard t={res.tenant} />}

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
        {(res?.nodes ?? []).map((n) => (
          <Card key={n.node} className="p-4">
            <div className="flex items-center justify-between">
              <span className="font-medium">{n.node}</span>
              <Badge variant={statusVariant(n.status)}>{n.status}</Badge>
            </div>
            <Meter label="CPU" value={n.cpu * 100} text={`${Math.round(n.cpu * 100)}% of ${n.maxcpu}`} />
            <Meter label="Memory" value={pct(n.mem, n.maxmem)} text={`${formatBytes(n.mem)} / ${formatBytes(n.maxmem)}`} />
            <div className="mt-2 text-xs text-muted-foreground">up {formatUptime(n.uptime)}</div>
          </Card>
        ))}
      </div>

      <Card>
        <CardHeader className="flex-row flex-wrap items-start justify-between gap-3">
          <div>
            <CardTitle>Guests</CardTitle>
            <CardDescription>{guests.length} VMs and containers</CardDescription>
          </div>
          <Input className="w-48" placeholder="Filter…" value={filter} onChange={(e) => setFilter(e.target.value)} aria-label="Filter guests" />
        </CardHeader>
        <CardContent className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="text-left text-xs text-muted-foreground">
              <tr>
                <th className="py-2 pr-3 font-normal">ID</th>
                <th className="py-2 pr-3 font-normal">Name</th>
                <th className="py-2 pr-3 font-normal">Node</th>
                <th className="py-2 pr-3 font-normal">Status</th>
                <th className="hidden py-2 pr-3 font-normal md:table-cell">CPU / Memory</th>
                <th className="py-2 font-normal" />
              </tr>
            </thead>
            <tbody className="divide-y">
              {shown.map((g) => {
                const running = g.status === "running";
                const b = busy[g.vmid];
                return (
                  <tr key={g.id}>
                    <td className="py-2 pr-3 font-mono text-xs">{g.vmid}</td>
                    <td className="py-2 pr-3">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="font-medium">{g.name}</span>
                        <Badge variant="outline">{g.type === "qemu" ? "VM" : "CT"}</Badge>
                        {g.tenant && (
                          <Badge variant="outline" title={`In pool ${g.pool}`}>
                            {g.tenant}
                          </Badge>
                        )}
                        {g.server_id && (
                          <Link href={`/servers/${g.server_id}`} className="text-xs text-brand hover:underline">
                            managed
                          </Link>
                        )}
                      </div>
                    </td>
                    <td className="py-2 pr-3 text-xs">{g.node}</td>
                    <td className="py-2 pr-3">
                      <Badge variant={statusVariant(g.status)}>{b ? `${b}…` : g.status}</Badge>
                      {running && <div className="mt-0.5 text-[11px] text-muted-foreground">up {formatUptime(g.uptime)}</div>}
                    </td>
                    <td className="hidden py-2 pr-3 text-xs text-muted-foreground md:table-cell">
                      {g.maxcpu} vCPU · {running ? `${formatBytes(g.mem)} / ` : ""}
                      {formatBytes(g.maxmem)}
                    </td>
                    <td className="py-2 text-right whitespace-nowrap">
                      {ac.can("proxmox.operate") && (
                        <span className="inline-flex gap-0.5">
                          {!running && (
                            <Icon title="Start" onClick={() => power(g, g.status === "paused" ? "resume" : "start")} disabled={!!b}>
                              <Play />
                            </Icon>
                          )}
                          {running && (
                            <>
                              <Icon title="Shut down" onClick={() => power(g, "shutdown")} disabled={!!b}>
                                <Power />
                              </Icon>
                              <Icon title="Reboot" onClick={() => power(g, "reboot")} disabled={!!b}>
                                <RotateCw />
                              </Icon>
                              {g.type === "qemu" && (
                                <Icon title="Suspend" onClick={() => power(g, "suspend")} disabled={!!b}>
                                  <Pause />
                                </Icon>
                              )}
                              <Icon
                                title="Stop (hard)"
                                className="text-destructive"
                                onClick={() => confirm(`Hard-stop ${g.name}? Unsaved data in the guest is lost.`) && power(g, "stop")}
                                disabled={!!b}
                              >
                                <Square />
                              </Icon>
                            </>
                          )}
                          <Icon title="Snapshots" onClick={() => setSnaps(g)}>
                            <Camera />
                          </Icon>
                          {ac.can("proxmox.guests") && (
                            <Icon title={running ? "Stop it before deleting" : "Delete"} className="text-destructive" disabled={running || !!b} onClick={() => setDel(g)}>
                              <Trash2 />
                            </Icon>
                          )}
                        </span>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          {res && shown.length === 0 && <p className="py-3 text-sm text-muted-foreground">No guests match.</p>}
        </CardContent>
      </Card>

      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>Templates</CardTitle>
            <CardDescription>Cloud-init VM templates. New VMs get the panel's SSH key and can enroll into servermgr automatically.</CardDescription>
          </CardHeader>
          <CardContent>
            {templates.length === 0 ? (
              <p className="text-sm text-muted-foreground">No VM templates on this cluster.</p>
            ) : (
              <ul className="divide-y text-sm">
                {templates.map((t) => (
                  <li key={t.id} className="flex items-center justify-between gap-2 py-2">
                    <span>
                      <span className="font-medium">{t.name}</span>
                      <span className="ml-2 font-mono text-xs text-muted-foreground">
                        {t.vmid} · {t.node}
                      </span>
                    </span>
                    {ac.can("proxmox.guests") && (
                      <Button size="sm" variant="brand" onClick={() => setNewVM(t)}>
                        <CloneIcon /> New VM
                      </Button>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </CardContent>
        </Card>
        {ac.can("proxmox.operate") && (
          <Card>
            <CardHeader>
              <CardTitle>Provisioning</CardTitle>
            </CardHeader>
            <CardContent>
              {provs.length === 0 ? (
                <p className="text-sm text-muted-foreground">No VMs created from servermgr yet.</p>
              ) : (
                <ul className="divide-y text-sm">
                  {provs.slice(0, 10).map((p) => (
                    <li key={p.id} className="py-2">
                      <details>
                        <summary className="flex cursor-pointer list-none items-center justify-between gap-2">
                          <span>
                            <span className="font-medium">{p.name}</span>
                            {p.vmid && <span className="ml-2 font-mono text-xs text-muted-foreground">{p.vmid}</span>}
                            {p.server_id && (
                              <Link href={`/servers/${p.server_id}`} className="ml-2 text-xs text-brand hover:underline">
                                server
                              </Link>
                            )}
                          </span>
                          <span className="flex items-center gap-2">
                            <Badge variant={p.status === "succeeded" ? "success" : p.status === "failed" ? "destructive" : "warning"}>{p.status}</Badge>
                            <span className="text-xs text-muted-foreground">{timeAgo(p.created_at)}</span>
                          </span>
                        </summary>
                        <pre className="mt-2 max-h-56 overflow-auto rounded bg-muted p-2 text-xs whitespace-pre-wrap">{p.log}</pre>
                      </details>
                    </li>
                  ))}
                </ul>
              )}
            </CardContent>
          </Card>
        )}
      </div>

      {snaps && <SnapshotsDialog cluster={cluster} guest={snaps} ac={ac} onClose={() => setSnaps(null)} />}
      {del && (
        <DeleteDialog
          cluster={cluster}
          guest={del}
          onClose={() => setDel(null)}
          onDone={(text) => {
            setDel(null);
            setMsg({ ok: true, text });
            void load();
          }}
        />
      )}
      {newVM && (
        <ProvisionDialog
          cluster={cluster}
          template={newVM}
          nodes={(res?.nodes ?? []).map((n) => n.node)}
          storages={[...new Set((res?.storage ?? []).map((x) => x.storage ?? ""))].filter(Boolean).sort()}
          tenant={res?.tenant}
          onClose={() => setNewVM(null)}
          onStarted={() => {
            setNewVM(null);
            void load();
          }}
        />
      )}
    </div>
  );
}

function Icon({ title, onClick, disabled, className = "", children }: { title: string; onClick: () => void; disabled?: boolean; className?: string; children: React.ReactNode }) {
  return (
    <Button size="icon" variant="ghost" className={`size-7 [&_svg]:size-3.5 ${className}`} title={title} aria-label={title} disabled={disabled} onClick={onClick}>
      {children}
    </Button>
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

function SnapshotsDialog({ cluster, guest, ac, onClose }: { cluster: PVECluster; guest: PVEGuest; ac: Access; onClose: () => void }) {
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

function DeleteDialog({ cluster, guest, onClose, onDone }: { cluster: PVECluster; guest: PVEGuest; onClose: () => void; onDone: (msg: string) => void }) {
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
              await api.pveDeleteGuest(cluster.id, guest, confirmName);
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

function ProvisionDialog({
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
    memory_mb: 2048,
    disk_grow_gb: 0,
    full_clone: true,
    ipMode: "dhcp",
    ip: "",
    gateway: "",
    enroll: true,
    storage: tenant?.storages.length === 1 ? tenant.storages[0] : "",
    bridge: tenant?.bridges.length === 1 ? tenant.bridges[0] : "",
  });
  const storageChoices = tenant ? tenant.storages : storages;
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>New VM from {template.name}</DialogTitle>
          <DialogDescription>
            Clones the template, sets cloud-init (root with the panel's SSH key, network), starts it and, if enrolled, installs the
            servermgr agent over SSH. The template needs cloud-init and qemu-guest-agent (for DHCP).
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
                disk_grow_gb: f.disk_grow_gb,
                full_clone: f.full_clone,
                ip: f.ipMode === "dhcp" ? "dhcp" : f.ip.trim(),
                gateway: f.ipMode === "dhcp" ? "" : f.gateway.trim(),
                enroll: f.enroll,
                storage: f.storage,
                bridge: f.bridge.trim(),
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
              <Label htmlFor="pp-name">Hostname</Label>
              <Input id="pp-name" value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} placeholder="web-03" pattern="[a-z0-9]([a-z0-9\-]*[a-z0-9])?" required />
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
          </div>
          <div className="grid grid-cols-3 gap-3">
            <Num id="pp-cores" label="vCPUs" value={f.cores} min={1} max={128} onChange={(v) => setF({ ...f, cores: v })} />
            <Num id="pp-mem" label="Memory (MB)" value={f.memory_mb} min={256} max={1048576} step={256} onChange={(v) => setF({ ...f, memory_mb: v })} />
            <Num id="pp-disk" label="Grow disk (GB)" value={f.disk_grow_gb} min={0} max={10000} onChange={(v) => setF({ ...f, disk_grow_gb: v })} />
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
          <label className="flex items-start gap-2 text-sm">
            <input type="checkbox" className="mt-1" checked={f.enroll} onChange={(e) => setF({ ...f, enroll: e.target.checked })} />
            <span>
              Enroll into servermgr
              <span className="block text-xs text-muted-foreground">Installs the agent, links the VM to its server and prepares it for Ansible.</span>
            </span>
          </label>
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
