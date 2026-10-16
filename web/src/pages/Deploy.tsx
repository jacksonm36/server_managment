import { useCallback, useEffect, useMemo, useState } from "react";
import { Link } from "wouter";
import { CheckCircle2, Copy as CloneIcon, Loader2, Plus, RefreshCw, Rocket, Ruler, Search, XCircle } from "lucide-react";
import { api, type PVECluster, type PVEImage, type PVEProvision, type PVEResources } from "@/lib/api";
import { useAccess } from "@/lib/access";
import { timeAgo } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { DeployDialog } from "@/components/DeployDialog";
import { ImageTemplateDialog } from "@/components/ImageTemplateDialog";
import { FlavorsDialog } from "@/components/Flavors";
import { ProvisionDialog } from "@/pages/Proxmox";
import { ContainerCatalog, WindowsCatalog } from "@/pages/DeployCatalogs";
import { DistroIcon, iconKey } from "@/components/DistroIcon";

// Deploy is the cloud-init deployer: an image catalog to deploy new VMs
// from (servermgr builds and reuses a template per image and node), the
// cluster's own templates, and every deployment with its log.

type Tab = "catalog" | "windows" | "containers" | "templates" | "deployments";

export function Deploy() {
  const ac = useAccess();
  const admin = ac.can("proxmox.manage");
  const [clusters, setClusters] = useState<PVECluster[] | null>(null);
  const [cur, setCur] = useState("");
  const [tab, setTab] = useState<Tab>(admin ? "catalog" : "templates");
  const [res, setRes] = useState<PVEResources | null>(null);
  const [provs, setProvs] = useState<PVEProvision[]>([]);
  const [err, setErr] = useState("");
  const [deploying, setDeploying] = useState<string | null>(null); // image id, "" = choose in the dialog
  const [fromImage, setFromImage] = useState(false);
  const [flavors, setFlavors] = useState(false);
  const [newVM, setNewVM] = useState<number | null>(null); // template vmid

  useEffect(() => {
    api
      .pveClusters()
      .then((c) => {
        setClusters(c);
        setCur((x) => x || c[0]?.id || "");
      })
      .catch((e) => setErr((e as Error).message));
  }, []);
  const load = useCallback(async () => {
    if (!cur) return;
    try {
      const [r, p] = await Promise.all([api.pveResources(cur), ac.can("proxmox.operate") ? api.pveProvisions() : Promise.resolve([])]);
      setRes(r);
      setProvs(p);
      setErr("");
    } catch (e) {
      setErr((e as Error).message);
    }
  }, [cur, ac]);
  const active = provs.some((p) => p.status === "running");
  useEffect(() => {
    void load();
    const t = setInterval(load, active ? 3000 : 15000);
    return () => clearInterval(t);
  }, [load, active]);

  const cluster = clusters?.find((c) => c.id === cur);
  const templates = useMemo(() => (res?.guests ?? []).filter((g) => g.template && g.type === "qemu").sort((a, b) => a.vmid - b.vmid), [res]);
  const started = (t: Tab = "deployments") => {
    setDeploying(null);
    setFromImage(false);
    setNewVM(null);
    setTab(t);
    void load();
  };
  const tabs: [Tab, string, boolean][] = [
    ["catalog", "Linux cloud images", admin],
    ["windows", "Windows", ac.can("proxmox.guests")],
    ["containers", "Containers (LXC)", ac.can("proxmox.guests")],
    ["templates", `Templates${templates.length ? ` (${templates.length})` : ""}`, true],
    ["deployments", `Deployments${active ? ` (${provs.filter((p) => p.status === "running").length} running)` : ""}`, ac.can("proxmox.operate")],
  ];

  return (
    <div className="grid gap-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Deploy</h1>
          <p className="text-sm text-muted-foreground">
            Linux VMs from cloud images with cloud-init, Windows cloud-init templates (Standard or Hardened), and containers from OS templates or TurnKey appliances.
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {clusters && clusters.length > 1 && (
            <Select value={cur} onChange={(e) => setCur(e.target.value)} aria-label="Cluster" className="w-44">
              {clusters.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </Select>
          )}
          {admin && (
            <>
              <Button size="sm" variant="ghost" onClick={() => setFlavors(true)}>
                <Ruler /> Flavors
              </Button>
              <Button size="sm" variant="brand" onClick={() => setDeploying("")} disabled={!cur}>
                <Rocket /> Deploy a VM
              </Button>
            </>
          )}
        </div>
      </div>
      {err && <p className="text-sm text-destructive">{err}</p>}
      {clusters && clusters.length === 0 && (
        <Card className="p-8 text-center text-sm text-muted-foreground">
          No Proxmox cluster yet. <Link href="/proxmox" className="text-brand hover:underline">Add one on the Proxmox page</Link> first.
        </Card>
      )}

      {cluster && (
        <>
          <nav className="-mx-1 flex gap-1 overflow-x-auto border-b px-1 text-sm" aria-label="Deploy sections">
            {tabs
              .filter(([, , show]) => show)
              .map(([id, label]) => (
                <button
                  key={id}
                  onClick={() => setTab(id)}
                  aria-current={tab === id ? "page" : undefined}
                  className={`-mb-px cursor-pointer border-b-2 px-3 py-2 whitespace-nowrap ${tab === id ? "border-brand text-foreground" : "border-transparent text-muted-foreground hover:text-foreground"}`}
                >
                  {label}
                </button>
              ))}
          </nav>

          {tab === "catalog" && admin && <Catalog res={res} onDeploy={(id) => setDeploying(id)} />}
          {tab === "windows" && <WindowsCatalog cluster={cluster.id} res={res} onStarted={() => started()} onDeploy={setNewVM} />}
          {tab === "containers" && <ContainerCatalog cluster={cluster.id} res={res} />}
          {tab === "templates" && (
            <Card className="p-4">
              <div className="mb-3 flex flex-wrap items-start justify-between gap-3">
                <div>
                  <h2 className="font-semibold">Templates on {cluster.name}</h2>
                  <p className="text-sm text-muted-foreground">Cloud-init VM templates. A new VM is a clone with your cloud-init settings.</p>
                </div>
                {admin && !res?.tenant && (
                  <Button size="sm" variant="secondary" onClick={() => setFromImage(true)}>
                    <Plus /> Template from image
                  </Button>
                )}
              </div>
              {templates.length === 0 ? (
                <p className="text-sm text-muted-foreground">No VM templates on this cluster yet.{admin ? " Deploy from the catalog, or build one from an image." : ""}</p>
              ) : (
                <ul className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
                  {templates.map((t) => (
                    <li key={t.id} className="flex items-center justify-between gap-3 rounded-lg border p-3">
                      <div className="min-w-0">
                        <div className="truncate font-medium">{t.name}</div>
                        <div className="font-mono text-xs text-muted-foreground">
                          {t.vmid} · {t.node}
                          {t.tags ? ` · ${t.tags.split(";").filter((x) => x !== "servermgr").join(", ")}` : ""}
                        </div>
                      </div>
                      {ac.can("proxmox.guests") && (
                        <Button size="sm" variant="brand" onClick={() => setNewVM(t.vmid)}>
                          <CloneIcon /> New VM
                        </Button>
                      )}
                    </li>
                  ))}
                </ul>
              )}
            </Card>
          )}
          {tab === "deployments" && <Deployments provs={provs} clusters={clusters ?? []} onRefresh={load} />}
        </>
      )}

      {cluster && deploying !== null && <DeployDialog cluster={cluster.id} guests={res?.guests ?? []} preset={deploying} onClose={() => setDeploying(null)} onStarted={() => started()} />}
      {cluster && fromImage && <ImageTemplateDialog cluster={cluster.id} onClose={() => setFromImage(false)} onStarted={() => started()} />}
      {flavors && <FlavorsDialog onClose={() => setFlavors(false)} />}
      {cluster &&
        newVM !== null &&
        (() => {
          const t = templates.find((x) => x.vmid === newVM);
          return t ? (
            <ProvisionDialog
              cluster={cluster}
              template={t}
              nodes={(res?.nodes ?? []).map((n) => n.node)}
              storages={[...new Set((res?.storage ?? []).map((x) => x.storage ?? ""))].filter(Boolean).sort()}
              tenant={res?.tenant}
              onClose={() => setNewVM(null)}
              onStarted={() => started()}
            />
          ) : null;
        })()}
    </div>
  );
}

function Catalog({ res, onDeploy }: { res: PVEResources | null; onDeploy: (image: string) => void }) {
  const [images, setImages] = useState<PVEImage[] | null>(null);
  const [q, setQ] = useState("");
  const [vendor, setVendor] = useState("");
  useEffect(() => {
    api
      .pveImages()
      .then(setImages)
      .catch(() => setImages([]));
  }, []);
  const vendors = useMemo(() => [...new Set((images ?? []).map((i) => i.vendor))], [images]);
  // Nodes that already hold a template built from each image.
  const built = useMemo(() => {
    const m = new Map<string, string[]>();
    for (const g of res?.guests ?? []) {
      if (!g.template) continue;
      for (const t of (g.tags ?? "").split(";")) m.set(t, [...(m.get(t) ?? []), g.node]);
    }
    return m;
  }, [res]);
  const shown = (images ?? []).filter((i) => (!vendor || i.vendor === vendor) && (!q || `${i.name} ${i.vendor} ${i.id}`.toLowerCase().includes(q.toLowerCase())));

  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative w-full sm:w-64">
          <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search images…" className="pl-8" aria-label="Search images" />
        </div>
        <div className="flex flex-wrap gap-1">
          {["", ...vendors].map((v) => (
            <button
              key={v || "all"}
              onClick={() => setVendor(v)}
              aria-pressed={vendor === v}
              className={`cursor-pointer rounded-full border px-3 py-1 text-xs ${vendor === v ? "border-brand bg-brand/15 text-foreground" : "text-muted-foreground hover:text-foreground"}`}
            >
              {v || "All"}
            </button>
          ))}
        </div>
      </div>
      {images === null ? (
        <p className="text-sm text-muted-foreground">Loading the catalog…</p>
      ) : shown.length === 0 ? (
        <p className="text-sm text-muted-foreground">No image matches.</p>
      ) : (
        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4 min-[1800px]:grid-cols-5 min-[2300px]:grid-cols-6">
          {shown.map((i) => {
            const on = built.get(i.id) ?? [];
            return (
              <Card key={i.id} className="flex flex-col gap-3 p-4">
                <div className="flex items-start gap-3">
                  <DistroIcon k={iconKey(i.id, i.vendor)} label={i.vendor} />
                  <div className="min-w-0">
                    <div className="truncate font-medium">{i.name}</div>
                    <div className="text-xs text-muted-foreground">{i.vendor}</div>
                  </div>
                </div>
                <div className="flex flex-wrap gap-1">
                  <Badge variant="outline">cloud-init</Badge>
                  <Badge variant="outline">user {i.user}</Badge>
                  {on.length > 0 ? (
                    <Badge variant="success" title={`Template on ${on.join(", ")}: deploys skip the download`}>
                      ready on {on.length === 1 ? on[0] : `${on.length} nodes`}
                    </Badge>
                  ) : (
                    <Badge variant="outline" title="The first deploy downloads the image and builds a template">
                      first deploy builds it
                    </Badge>
                  )}
                </div>
                <Button size="sm" variant="brand" className="mt-auto" onClick={() => onDeploy(i.id)}>
                  <Rocket /> Deploy
                </Button>
              </Card>
            );
          })}
        </div>
      )}
    </div>
  );
}

function Deployments({ provs, clusters, onRefresh }: { provs: PVEProvision[]; clusters: PVECluster[]; onRefresh: () => void }) {
  const [open, setOpen] = useState<string | null>(null);
  const name = (id: string) => clusters.find((c) => c.id === id)?.name ?? "";
  return (
    <Card className="p-4">
      <div className="mb-3 flex items-center justify-between gap-2">
        <div>
          <h2 className="font-semibold">Deployments</h2>
          <p className="text-sm text-muted-foreground">VMs and templates created from servermgr, newest first.</p>
        </div>
        <Button size="icon" variant="ghost" aria-label="Refresh" onClick={onRefresh}>
          <RefreshCw />
        </Button>
      </div>
      {provs.length === 0 ? (
        <p className="text-sm text-muted-foreground">Nothing deployed yet.</p>
      ) : (
        <ul className="divide-y text-sm">
          {provs.map((p) => {
            const last = p.log.trim().split("\n").pop()?.replace(/^\d\d:\d\d:\d\d /, "") ?? "";
            return (
              <li key={p.id} className="py-2">
                <button className="flex w-full cursor-pointer items-center gap-3 text-left" onClick={() => setOpen(open === p.id ? null : p.id)} aria-expanded={open === p.id}>
                  {p.status === "running" ? (
                    <Loader2 className="size-4 shrink-0 animate-spin text-warning" />
                  ) : p.status === "succeeded" ? (
                    <CheckCircle2 className="size-4 shrink-0 text-success" />
                  ) : (
                    <XCircle className="size-4 shrink-0 text-destructive" />
                  )}
                  <span className="min-w-0 flex-1">
                    <span className="flex flex-wrap items-center gap-2">
                      <span className="font-medium">{p.name}</span>
                      {p.vmid && <span className="font-mono text-xs text-muted-foreground">VM {p.vmid}</span>}
                      {clusters.length > 1 && <span className="text-xs text-muted-foreground">{name(p.cluster_id)}</span>}
                    </span>
                    <span className="block truncate text-xs text-muted-foreground">{last}</span>
                  </span>
                  <span className="flex shrink-0 items-center gap-2">
                    {p.server_id && (
                      <Link href={`/servers/${p.server_id}`} className="text-xs text-brand hover:underline" onClick={(e) => e.stopPropagation()}>
                        server
                      </Link>
                    )}
                    <span className="hidden text-xs text-muted-foreground sm:inline">
                      {timeAgo(p.created_at)} · {p.created_by}
                    </span>
                  </span>
                </button>
                {open === p.id && <pre className="mt-2 max-h-72 overflow-auto rounded-md bg-muted p-3 font-mono text-xs whitespace-pre-wrap">{p.log}</pre>}
              </li>
            );
          })}
        </ul>
      )}
    </Card>
  );
}
