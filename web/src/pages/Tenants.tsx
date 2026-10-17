import { useEffect, useState } from "react";
import { KeyRound, Pencil, Plus, ShieldCheck, ShieldOff, Trash2 } from "lucide-react";
import { ApiError, api, type PVECluster, type ServerGroup, type Tenant, type TenantCluster, type TenantOptions } from "@/lib/api";
import { timeAgo } from "@/lib/utils";
import { GroupChip } from "@/components/GroupChip";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input, Label } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";

type Act = (f: () => Promise<unknown>) => Promise<boolean>;

const limit = (n: number, unit = "") => (n > 0 ? `${n}${unit}` : "no limit");

// parseVLANs reads "20, 30-35" into [20, 30, 31, …, 35]; anything that is
// not a tag 1-4094 is an error.
function parseVLANs(text: string): number[] {
  const out = new Set<number>();
  for (const part of text.split(/[\s,;]+/).filter(Boolean)) {
    const m = /^(\d{1,4})(?:-(\d{1,4}))?$/.exec(part);
    const a = m ? Number(m[1]) : NaN;
    const b = m?.[2] ? Number(m[2]) : a;
    if (!m || a < 1 || b > 4094 || b < a || b - a > 255) throw new Error(`VLAN "${part}": use tags 1-4094, like 20 or 30-35`);
    for (let v = a; v <= b; v++) out.add(v);
  }
  return [...out].sort((x, y) => x - y);
}

/** Tenants: customers or teams kept apart, each with its own Proxmox pool and token. */
export function TenantsTab({ tenants, serverGroups, act }: { tenants: Tenant[]; serverGroups: ServerGroup[]; act: Act }) {
  const [clusters, setClusters] = useState<PVECluster[]>([]);
  const [editing, setEditing] = useState<Tenant | "new" | null>(null);
  const [isolating, setIsolating] = useState<{ tenant: Tenant; cluster: PVECluster } | null>(null);
  useEffect(() => {
    api.pveClusters().then(setClusters).catch(() => setClusters([]));
  }, []);

  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="max-w-3xl text-sm text-muted-foreground">
          A tenant's users only reach the tenant's own Proxmox pool and the servers of its server group, whatever their roles say. On each cluster
          the tenant gets a Proxmox user and API token that Proxmox itself limits to that pool, so isolation does not rely on servermgr alone.
        </p>
        <Button variant="brand" onClick={() => setEditing("new")}>
          <Plus /> New tenant
        </Button>
      </div>
      {tenants.length === 0 && <p className="text-sm text-muted-foreground">No tenants yet.</p>}
      <div className="grid gap-3 lg:grid-cols-2">
        {tenants.map((t) => {
          const group = serverGroups.find((g) => g.id === t.server_group_id);
          return (
            <Card key={t.id} className="grid gap-3 p-4">
              <div className="flex items-start justify-between gap-2">
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-medium">{t.name}</span>
                    <span className="font-mono text-xs text-muted-foreground">{t.slug}</span>
                    <Badge variant="outline">{t.users === 1 ? "1 user" : `${t.users} users`}</Badge>
                  </div>
                  {t.description && <p className="mt-1 text-sm text-muted-foreground">{t.description}</p>}
                </div>
                <div className="flex shrink-0">
                  <Button variant="ghost" size="icon" title="Edit" aria-label="Edit" onClick={() => setEditing(t)}>
                    <Pencil />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    title={t.users ? "Move or delete its users first" : "Delete"}
                    aria-label="Delete"
                    disabled={t.users > 0}
                    onClick={() =>
                      confirm(`Delete tenant ${t.name}? Its Proxmox users and tokens are removed; pools and guests stay.`) &&
                      act(() => api.deleteTenant(t.id))
                    }
                  >
                    <Trash2 className="text-destructive" />
                  </Button>
                </div>
              </div>
              <div className="grid gap-1 text-xs text-muted-foreground">
                <div>
                  Servers: {group ? <GroupChip group={group} /> : <span>none (set a server group to give them servers)</span>}
                </div>
                <div>
                  Limits: {limit(t.max_guests, " guests")} · {limit(t.max_cores, " vCPUs")} · {t.max_memory_mb > 0 ? `${Math.round(t.max_memory_mb / 1024)} GB RAM` : "no RAM limit"} ·{" "}
                  {t.max_disk_gb > 0 ? `${t.max_disk_gb} GB disk` : "no disk limit"}
                </div>
              </div>
              <div className="grid gap-1.5">
                {clusters.length === 0 && <p className="text-xs text-muted-foreground">No Proxmox clusters yet.</p>}
                {clusters.map((c) => {
                  const tc = t.clusters.find((x) => x.cluster_id === c.id);
                  return (
                    <div key={c.id} className="flex flex-wrap items-center justify-between gap-2 rounded-md border px-3 py-2 text-sm">
                      <span className="flex min-w-0 flex-wrap items-center gap-2">
                        <span className="font-medium">{c.name}</span>
                        {tc?.set_up_at ? (
                          <Badge variant="success" className="gap-1" title={`${tc.pve_user}, set up ${timeAgo(tc.set_up_at)}`}>
                            <ShieldCheck className="size-3" /> pool {tc.pool}
                          </Badge>
                        ) : (
                          <Badge variant="outline" className="gap-1">
                            <ShieldOff className="size-3" /> no access
                          </Badge>
                        )}
                      </span>
                      <Button size="sm" variant={tc ? "outline" : "secondary"} onClick={() => setIsolating({ tenant: t, cluster: c })}>
                        {tc ? "Change" : "Give access"}
                      </Button>
                    </div>
                  );
                })}
              </div>
            </Card>
          );
        })}
      </div>
      {editing && <TenantDialog tenant={editing === "new" ? undefined : editing} serverGroups={serverGroups} act={act} onClose={() => setEditing(null)} />}
      {isolating && (
        <IsolationDialog
          tenant={isolating.tenant}
          cluster={isolating.cluster}
          current={isolating.tenant.clusters.find((x) => x.cluster_id === isolating.cluster.id)}
          act={act}
          onClose={() => setIsolating(null)}
        />
      )}
    </div>
  );
}

function TenantDialog({ tenant, serverGroups, act, onClose }: { tenant?: Tenant; serverGroups: ServerGroup[]; act: Act; onClose: () => void }) {
  const [f, setF] = useState({
    name: tenant?.name ?? "",
    slug: tenant?.slug ?? "",
    description: tenant?.description ?? "",
    server_group_id: tenant?.server_group_id ? String(tenant.server_group_id) : "",
    max_guests: tenant?.max_guests ?? 0,
    max_cores: tenant?.max_cores ?? 0,
    max_memory_gb: tenant ? Math.round(tenant.max_memory_mb / 1024) : 0,
    max_disk_gb: tenant?.max_disk_gb ?? 0,
  });
  const [slugEdited, setSlugEdited] = useState(!!tenant);
  const [error, setError] = useState("");
  const slugFrom = (name: string) =>
    name
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^[^a-z]+|-+$/g, "")
      .slice(0, 31);

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{tenant ? `Edit ${tenant.name}` : "New tenant"}</DialogTitle>
          <DialogDescription>Give it access to Proxmox afterwards, per cluster. Add users to it on the Users tab.</DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-3"
          onSubmit={async (e) => {
            e.preventDefault();
            setError("");
            try {
              await api.saveTenant(tenant?.id ?? null, {
                name: f.name.trim(),
                ...(tenant ? {} : { slug: f.slug.trim() }),
                description: f.description.trim(),
                server_group_id: f.server_group_id ? Number(f.server_group_id) : null,
                max_guests: f.max_guests,
                max_cores: f.max_cores,
                max_memory_mb: f.max_memory_gb * 1024,
                max_disk_gb: f.max_disk_gb,
              });
              await act(async () => {});
              onClose();
            } catch (err) {
              setError((err as Error).message);
            }
          }}
        >
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="grid gap-1.5">
              <Label htmlFor="t-name">Name</Label>
              <Input
                id="t-name"
                value={f.name}
                required
                onChange={(e) => setF({ ...f, name: e.target.value, slug: slugEdited ? f.slug : slugFrom(e.target.value) })}
                placeholder="Acme Ltd"
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="t-slug">Short name</Label>
              <Input
                id="t-slug"
                value={f.slug}
                disabled={!!tenant}
                required
                pattern="[a-z][a-z0-9\-]{1,30}"
                onChange={(e) => {
                  setSlugEdited(true);
                  setF({ ...f, slug: e.target.value });
                }}
                placeholder="acme"
              />
              <p className="text-xs text-muted-foreground">Names its Proxmox user (sm-{f.slug || "…"}@pve). Can't be changed later.</p>
            </div>
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="t-desc">Description</Label>
            <Input id="t-desc" value={f.description} maxLength={300} onChange={(e) => setF({ ...f, description: e.target.value })} />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="t-sg">Server group</Label>
            <Select id="t-sg" value={f.server_group_id} onChange={(e) => setF({ ...f, server_group_id: e.target.value })}>
              <option value="">None</option>
              {serverGroups.map((g) => (
                <option key={g.id} value={g.id}>
                  {g.name}
                </option>
              ))}
            </Select>
            <p className="text-xs text-muted-foreground">
              The servers its users can see and work on (as far as their roles allow). VMs they create and enroll join this group.
            </p>
          </div>
          <div className="grid gap-1.5">
            <Label>Limits (0 = none)</Label>
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
              <NumField label="Guests" value={f.max_guests} onChange={(v) => setF({ ...f, max_guests: v })} />
              <NumField label="vCPUs" value={f.max_cores} onChange={(v) => setF({ ...f, max_cores: v })} />
              <NumField label="RAM (GB)" value={f.max_memory_gb} onChange={(v) => setF({ ...f, max_memory_gb: v })} />
              <NumField label="Disk (GB)" value={f.max_disk_gb} onChange={(v) => setF({ ...f, max_disk_gb: v })} />
            </div>
          </div>
          {error && <p className="text-sm text-destructive">{error}</p>}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" variant="brand">
              {tenant ? "Save" : "Create"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function NumField({ label, value, onChange }: { label: string; value: number; onChange: (v: number) => void }) {
  return (
    <label className="grid gap-1 text-xs text-muted-foreground">
      {label}
      <Input type="number" min={0} value={value} onChange={(e) => onChange(Math.max(0, Number(e.target.value) || 0))} />
    </label>
  );
}

function IsolationDialog({
  tenant,
  cluster,
  current,
  act,
  onClose,
}: {
  tenant: Tenant;
  cluster: PVECluster;
  current?: TenantCluster;
  act: Act;
  onClose: () => void;
}) {
  const [opts, setOpts] = useState<TenantOptions | null>(null);
  const [pool, setPool] = useState(current?.pool ?? `tenant-${tenant.slug}`);
  const [storages, setStorages] = useState<string[]>(current?.storages ?? []);
  const [bridges, setBridges] = useState<string[]>(current?.bridges ?? []);
  const [vlans, setVlans] = useState((current?.vlans ?? []).join(", "));
  const [templates, setTemplates] = useState<number[]>(current?.templates ?? []);
  const [steps, setSteps] = useState<string[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  useEffect(() => {
    api.tenantOptions(cluster.id).then(setOpts).catch((e) => setError((e as Error).message));
  }, [cluster.id]);

  async function run(rotate: boolean) {
    setBusy(rotate ? "rotate" : "save");
    setError("");
    setSteps([]);
    try {
      const r = await api.setupTenantCluster(tenant.id, cluster.id, { pool: pool.trim(), storages, bridges, vlans: parseVLANs(vlans), templates, rotate });
      setSteps(r.steps);
      await act(async () => {});
    } catch (e) {
      setError((e as Error).message);
      if (e instanceof ApiError) setSteps(((e.data as { steps?: string[] })?.steps ?? []) as string[]);
    } finally {
      setBusy("");
    }
  }

  const toggle = <T,>(list: T[], set: (v: T[]) => void, v: T) => set(list.includes(v) ? list.filter((x) => x !== v) : [...list, v]);

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90svh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>
            {tenant.name} on {cluster.name}
          </DialogTitle>
          <DialogDescription>
            servermgr creates the pool and a Proxmox user <code>sm-{tenant.slug}@pve</code> with an API token, and grants it exactly this. Its
            users can then only reach guests in the pool. The cluster's own token needs Pool.Allocate, User.Modify and Permissions.Modify (a
            root@pam token has them).
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-4">
          <div className="grid gap-1.5">
            <Label htmlFor="iso-pool">Pool</Label>
            <Input id="iso-pool" value={pool} onChange={(e) => setPool(e.target.value)} list="iso-pools" pattern="[A-Za-z0-9][A-Za-z0-9_.\-]{1,39}" />
            <datalist id="iso-pools">{opts?.pools.map((p) => <option key={p} value={p} />)}</datalist>
            <p className="text-xs text-muted-foreground">Created if it doesn't exist. Guests already in an existing pool become the tenant's.</p>
          </div>
          <CheckList
            label="Storages it may put disks on"
            items={(opts?.storages ?? []).map((s) => ({ key: s, label: s }))}
            selected={storages}
            onToggle={(v) => toggle(storages, setStorages, v)}
          />
          <CheckList
            label="Network bridges"
            items={(opts?.bridges ?? []).map((b) => ({ key: b, label: b }))}
            selected={bridges}
            onToggle={(v) => toggle(bridges, setBridges, v)}
          />
          <div className="grid gap-1.5">
            <Label htmlFor="iso-vlans">VLAN tags it may use on those bridges</Label>
            <Input id="iso-vlans" value={vlans} onChange={(e) => setVlans(e.target.value)} placeholder="none: untagged only (e.g. 20, 30-35)" />
            <p className="text-xs text-muted-foreground">
              Proxmox enforces it too: the bridges are granted for untagged NICs only, and each tag gets its own permission, so the tenant cannot reach
              other VLANs.
            </p>
          </div>
          <CheckList
            label="Templates it may create VMs from"
            items={(opts?.templates ?? []).map((t) => ({ key: t.vmid, label: `${t.name} (${t.vmid})` }))}
            selected={templates}
            onToggle={(v) => toggle(templates, setTemplates, v)}
          />
          {steps.length > 0 && (
            <ul className="grid gap-0.5 rounded-md border bg-muted/40 p-2 font-mono text-xs">
              {steps.map((s, i) => (
                <li key={i}>✓ {s}</li>
              ))}
            </ul>
          )}
          {error && <p className="text-sm text-destructive">{error}</p>}
          <div className="flex flex-wrap justify-between gap-2">
            <div className="flex gap-2">
              {current && (
                <>
                  <Button type="button" variant="outline" disabled={!!busy} onClick={() => run(true)} title="Issue a new Proxmox token; the old one stops working">
                    <KeyRound /> {busy === "rotate" ? "Rotating…" : "Rotate token"}
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    className="text-destructive"
                    disabled={!!busy}
                    onClick={async () => {
                      if (!confirm(`Take ${tenant.name}'s access to ${cluster.name} away? Its Proxmox user and token are deleted; the pool and guests stay.`)) return;
                      if (await act(() => api.removeTenantCluster(tenant.id, cluster.id))) onClose();
                    }}
                  >
                    <Trash2 /> Remove access
                  </Button>
                </>
              )}
            </div>
            <div className="flex gap-2">
              <Button type="button" variant="ghost" onClick={onClose}>
                Close
              </Button>
              <Button type="button" variant="brand" disabled={!!busy || !opts} onClick={() => run(false)}>
                {busy === "save" ? "Setting up…" : current ? "Apply" : "Set up"}
              </Button>
            </div>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function CheckList<K extends string | number>({
  label,
  items,
  selected,
  onToggle,
}: {
  label: string;
  items: { key: K; label: string }[];
  selected: K[];
  onToggle: (k: K) => void;
}) {
  return (
    <div className="grid gap-1.5">
      <Label>{label}</Label>
      {items.length === 0 ? (
        <p className="text-xs text-muted-foreground">None found.</p>
      ) : (
        <div className="flex flex-wrap gap-x-4 gap-y-1">
          {items.map((i) => (
            <label key={String(i.key)} className="flex cursor-pointer items-center gap-2 text-sm">
              <input type="checkbox" className="accent-[var(--color-brand)]" checked={selected.includes(i.key)} onChange={() => onToggle(i.key)} />
              {i.label}
            </label>
          ))}
        </div>
      )}
    </div>
  );
}
