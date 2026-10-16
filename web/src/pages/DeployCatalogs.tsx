import { useEffect, useMemo, useState } from "react";
import { Box, Copy as CloneIcon, Download, ExternalLink, Hammer, Loader2, Lock, LockOpen, Plus, Search } from "lucide-react";
import { api, type PVEResources } from "@/lib/api";
import { useAccess } from "@/lib/access";
import { pve, type PVEAppliance } from "@/lib/pveapi";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { CreateGuestDialog, useChoices, type GuestPreset } from "@/components/PVEForms";
import { DistroIcon, iconKey } from "@/components/DistroIcon";
import { WindowsBuildDialog, type WinEditionCard } from "@/components/WindowsTemplates";

// The Deploy page's Windows and container catalogs. Windows has no
// downloadable cloud image: servermgr builds cloud-init templates (Standard
// and Hardened) from the admin's installer ISO, or sets up a VM the way each
// edition wants it (UEFI, TPM 2.0, VirtIO with the driver ISO) for a manual
// install. Containers come from Proxmox's template catalog: plain OS
// templates and TurnKey Linux appliances.

const EVAL_CENTER = "https://www.microsoft.com/en-us/evalcenter";

const editions: { id: string; name: string; sub: string; preset: GuestPreset; build: WinEditionCard }[] = [
  ["win11", "Windows 11", "Desktop · UEFI, Secure Boot, TPM 2.0", "win11", /win(dows)?[ _-]?11|win11/i, 64, 1, true],
  ["win10", "Windows 10", "Desktop · UEFI, TPM 2.0", "win10", /win(dows)?[ _-]?10|win10/i, 64, 1, true],
  ["ws2025", "Windows Server 2025", "Server · UEFI, Secure Boot, TPM 2.0", "win11", /2025/, 64, 2, false],
  ["ws2022", "Windows Server 2022", "Server · UEFI, Secure Boot, TPM 2.0", "win11", /2022/, 64, 2, false],
  ["ws2019", "Windows Server 2019", "Server · UEFI", "win10", /2019/, 48, 2, false],
].map(([id, name, sub, ostype, iso, disk, index, client]) => ({
  id: id as string,
  name: name as string,
  sub: sub as string,
  preset: { ostype: ostype as string, iso: iso as RegExp, memory: "4096", size: String(disk) },
  build: { id: id as string, name: name as string, iso: iso as RegExp, disk: disk as number, index: index as number, client: client as boolean },
}));

export function WindowsCatalog({
  cluster,
  res,
  onStarted,
  onDeploy,
}: {
  cluster: string;
  res: PVEResources | null;
  onStarted: () => void;
  onDeploy: (vmid: number) => void;
}) {
  const ac = useAccess();
  const admin = ac.can("proxmox.manage") && !res?.tenant;
  const [build, setBuild] = useState<WinEditionCard | null>(null);
  // Templates servermgr built, by edition and profile.
  const built = useMemo(() => {
    const m: Record<string, { vmid: number; name: string; profile: string }[]> = {};
    for (const g of res?.guests ?? []) {
      const tags = (g.tags ?? "").split(/[;, ]/);
      if (!g.template || !tags.includes("windows")) continue;
      const ed = tags.find((t) => editions.some((e) => e.id === t)) ?? "other";
      const profile = tags.find((t) => t.startsWith("win-"))?.slice(4) ?? "";
      (m[ed] ??= []).push({ vmid: g.vmid, name: g.name, profile });
    }
    return m;
  }, [res]);
  const nodes = useMemo(() => (res?.nodes ?? []).filter((n) => n.status === "online").map((n) => n.node), [res]);
  const [node, setNode] = useState("");
  useEffect(() => setNode((x) => x || nodes[0] || ""), [nodes]);
  const { c } = useChoices(cluster, node);
  const [open, setOpen] = useState<(typeof editions)[number] | null>(null);
  const drivers = c?.isos.find((i) => /virtio-win/i.test(i));
  const base = (i: string) => i.split("/").pop() ?? i;

  return (
    <div className="grid gap-4">
      <Card className="flex flex-wrap items-center justify-between gap-3 p-4 text-sm">
        <div className="grid gap-1">
          <p>
            Bring the installer ISO: upload it on the Storage page, or download an evaluation from{" "}
            <a href={EVAL_CENTER} target="_blank" rel="noreferrer" className="text-brand hover:underline">
              Microsoft's Evaluation Center <ExternalLink className="inline size-3" />
            </a>
            . <b>Build templates</b> installs Windows unattended and makes a Standard and a Hardened cloud-init template; new VMs from them get their
            Administrator password, hostname and IP from cloud-init. <b>Set up VM</b> is for installing by hand.
          </p>
          <p className="text-xs text-muted-foreground">
            Drivers on {node || "this node"}: {drivers ? <span className="font-mono">{base(drivers)}</span> : "not downloaded yet (the setup dialog can fetch it)"}
          </p>
        </div>
        {nodes.length > 1 && (
          <Select value={node} onChange={(e) => setNode(e.target.value)} aria-label="Node" className="w-40">
            {nodes.map((n) => (
              <option key={n}>{n}</option>
            ))}
          </Select>
        )}
      </Card>
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
        {editions.map((e) => {
          const iso = c?.isos.find((i) => e.preset.iso!.test(base(i)));
          return (
            <Card key={e.id} className="flex flex-col gap-3 p-4">
              <div className="flex items-start gap-3">
                <DistroIcon k="windows" label={e.name} />
                <div className="min-w-0">
                  <div className="truncate font-medium">{e.name}</div>
                  <div className="text-xs text-muted-foreground">{e.sub}</div>
                </div>
              </div>
              <div className="flex flex-wrap gap-1">
                <Badge variant="outline">{e.preset.ostype}</Badge>
                <Badge variant="outline">VirtIO</Badge>
                <Badge variant="outline">cloud-init</Badge>
                {iso ? (
                  <Badge variant="success" title={base(iso)}>
                    ISO found
                  </Badge>
                ) : (
                  <Badge variant="outline">no ISO yet</Badge>
                )}
              </div>
              {(built[e.id] ?? []).length > 0 && (
                <ul className="grid gap-1 text-sm">
                  {built[e.id].map((t) => (
                    <li key={t.vmid} className="flex items-center justify-between gap-2 rounded-md border px-2 py-1">
                      <span className="flex min-w-0 items-center gap-1.5">
                        {t.profile === "hardened" ? <Lock className="size-3.5 text-brand" /> : <LockOpen className="size-3.5 text-muted-foreground" />}
                        <span className="truncate">{t.name}</span>
                        <span className="font-mono text-xs text-muted-foreground">{t.vmid}</span>
                      </span>
                      {ac.can("proxmox.guests") && (
                        <Button size="sm" variant="ghost" className="h-7" onClick={() => onDeploy(t.vmid)}>
                          <CloneIcon /> New VM
                        </Button>
                      )}
                    </li>
                  ))}
                </ul>
              )}
              <div className="mt-auto flex flex-wrap gap-2">
                {admin && (
                  <Button size="sm" variant="brand" onClick={() => setBuild(e.build)} disabled={!node}>
                    <Hammer /> Build templates
                  </Button>
                )}
                <Button size="sm" variant={admin ? "secondary" : "brand"} onClick={() => setOpen(e)} disabled={!node}>
                  <Plus /> Set up VM
                </Button>
              </div>
            </Card>
          );
        })}
      </div>
      {open && (
        <CreateGuestDialog
          cluster={cluster}
          nodes={nodes}
          type="qemu"
          preset={{
            ...open.preset,
            node,
            bios: "ovmf",
            cores: "2",
            title: `New ${open.name} VM`,
            description: "UEFI with Secure Boot keys, TPM 2.0, VirtIO disk and network. Attach the installer and drivers ISOs, then install in the console.",
          }}
          onClose={() => setOpen(null)}
        />
      )}
      {build && (
        <WindowsBuildDialog
          cluster={cluster}
          edition={build}
          nodes={nodes}
          node={node}
          onClose={() => setBuild(null)}
          onStarted={() => {
            setBuild(null);
            onStarted();
          }}
        />
      )}
    </div>
  );
}

const sections: [string, string][] = [
  ["", "All"],
  ["system", "Operating systems"],
  ["turnkeylinux", "TurnKey appliances"],
];

export function ContainerCatalog({ cluster, res }: { cluster: string; res: PVEResources | null }) {
  const ac = useAccess();
  const admin = ac.can("proxmox.manage");
  const nodes = useMemo(() => (res?.nodes ?? []).filter((n) => n.status === "online").map((n) => n.node), [res]);
  const [node, setNode] = useState("");
  useEffect(() => setNode((x) => x || nodes[0] || ""), [nodes]);
  const [reload, setReload] = useState(0);
  const { c } = useChoices(cluster, node, reload);
  const [list, setList] = useState<PVEAppliance[] | null>(null);
  const [err, setErr] = useState("");
  const [q, setQ] = useState("");
  const [section, setSection] = useState("");
  const [busy, setBusy] = useState<Record<string, string>>({});
  const [create, setCreate] = useState<string | null>(null); // template volid
  useEffect(() => {
    if (!node) return;
    setList(null);
    pve
      .appliances(cluster, node)
      .then((l) => {
        setList(l);
        setErr("");
      })
      .catch((e) => {
        setList([]);
        setErr((e as Error).message);
      });
  }, [cluster, node]);

  const tmplStores = useMemo(() => (c?.storages ?? []).filter((s) => s.content.split(",").includes("vztmpl")), [c]);
  const [store, setStore] = useState("");
  useEffect(() => setStore((x) => x || tmplStores[0]?.storage || ""), [tmplStores]);
  // Downloaded templates by file name, so a catalog entry knows it's there.
  const have = useMemo(() => new Map((c?.templates ?? []).map((v) => [v.split("/").pop() ?? v, v])), [c]);
  const known = new Set((list ?? []).map((a) => a.template));
  const own = [...have.entries()].filter(([f]) => !known.has(f));
  const shown = (list ?? []).filter(
    (a) => (!section || a.section === section || (section === "system" && a.section !== "turnkeylinux")) && (!q || `${a.headline} ${a.package} ${a.description ?? ""}`.toLowerCase().includes(q.toLowerCase())),
  );

  async function download(a: PVEAppliance) {
    setBusy((b) => ({ ...b, [a.template]: "Downloading…" }));
    try {
      const r = await pve.applianceDownload(cluster, node, store, a.template);
      if (r.upid)
        for (let i = 0; i < 400; i++) {
          await new Promise((res) => setTimeout(res, 3000));
          const t = await api.pveNodeTask(cluster, node, r.upid);
          if (t.status === "stopped") {
            if (t.exitstatus !== "OK") throw new Error(t.exitstatus);
            break;
          }
        }
      setBusy((b) => ({ ...b, [a.template]: "" }));
      setReload((n) => n + 1);
    } catch (e) {
      setBusy((b) => ({ ...b, [a.template]: `Failed: ${(e as Error).message}` }));
    }
  }

  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative w-full sm:w-64">
          <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search WordPress, Nextcloud, Debian…" className="pl-8" aria-label="Search containers" />
        </div>
        <div className="flex flex-wrap gap-1">
          {sections.map(([id, label]) => (
            <button
              key={id || "all"}
              onClick={() => setSection(id)}
              aria-pressed={section === id}
              className={`cursor-pointer rounded-full border px-3 py-1 text-xs ${section === id ? "border-brand bg-brand/15 text-foreground" : "text-muted-foreground hover:text-foreground"}`}
            >
              {label}
            </button>
          ))}
        </div>
        <div className="ml-auto flex flex-wrap items-center gap-2">
          {nodes.length > 1 && (
            <Select value={node} onChange={(e) => setNode(e.target.value)} aria-label="Node" className="w-36">
              {nodes.map((n) => (
                <option key={n}>{n}</option>
              ))}
            </Select>
          )}
          {admin && tmplStores.length > 1 && (
            <Select value={store} onChange={(e) => setStore(e.target.value)} aria-label="Download to storage" className="w-40">
              {tmplStores.map((s) => (
                <option key={s.storage} value={s.storage}>
                  to {s.storage}
                </option>
              ))}
            </Select>
          )}
        </div>
      </div>
      {err && <p className="text-sm text-destructive">{err}</p>}

      {own.length > 0 && !section && !q && (
        <Card className="p-4">
          <h2 className="mb-2 text-sm font-semibold">Your templates on {node}</h2>
          <ul className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
            {own.map(([f, volid]) => (
              <li key={volid} className="flex items-center gap-3 rounded-lg border p-2">
                <DistroIcon k={iconKey(f)} label={f} size="size-8" />
                <span className="min-w-0 flex-1 truncate font-mono text-xs">{f}</span>
                {ac.can("proxmox.guests") && (
                  <Button size="sm" variant="secondary" onClick={() => setCreate(volid)}>
                    <Box /> Create
                  </Button>
                )}
              </li>
            ))}
          </ul>
        </Card>
      )}

      {list === null ? (
        <p className="text-sm text-muted-foreground">Loading the template catalog…</p>
      ) : shown.length === 0 ? (
        <p className="text-sm text-muted-foreground">{list.length ? "No template matches." : "This node offers no container templates (run pveam update on it)."}</p>
      ) : (
        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
          {shown.map((a) => {
            const volid = have.get(a.template);
            const b = busy[a.template];
            const turnkey = a.section === "turnkeylinux";
            return (
              <Card key={a.template} className="flex flex-col gap-3 p-4">
                <div className="flex items-start gap-3">
                  <DistroIcon k={iconKey(a.package, a.headline, turnkey ? "" : a.os ?? "")} label={a.headline} />
                  <div className="min-w-0">
                    <div className="truncate font-medium">{a.headline.replace(/^TurnKey\s+/i, "")}</div>
                    <div className="truncate text-xs text-muted-foreground">
                      {turnkey ? "TurnKey Linux" : "Container OS"}
                      {a.version ? ` · ${a.version}` : ""}
                    </div>
                  </div>
                </div>
                {a.description && <p className="line-clamp-2 text-xs text-muted-foreground">{a.description}</p>}
                <div className="flex flex-wrap gap-1">
                  <Badge variant="outline">LXC</Badge>
                  {a.os && <Badge variant="outline">{a.os}</Badge>}
                  {volid && <Badge variant="success">downloaded</Badge>}
                  {a.infopage && (
                    <a href={a.infopage} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-xs text-brand hover:underline">
                      about <ExternalLink className="size-3" />
                    </a>
                  )}
                </div>
                {b && <p className={`text-xs ${b.startsWith("Failed") ? "text-destructive" : "text-muted-foreground"}`}>{b}</p>}
                <div className="mt-auto">
                  {volid ? (
                    ac.can("proxmox.guests") && (
                      <Button size="sm" variant="brand" className="w-full" onClick={() => setCreate(volid)}>
                        <Box /> Create container
                      </Button>
                    )
                  ) : admin ? (
                    <Button size="sm" variant="secondary" className="w-full" disabled={!store || b === "Downloading…"} onClick={() => download(a)}>
                      {b === "Downloading…" ? <Loader2 className="animate-spin" /> : <Download />} Download
                    </Button>
                  ) : (
                    <p className="text-xs text-muted-foreground">Ask a platform admin to download it.</p>
                  )}
                </div>
              </Card>
            );
          })}
        </div>
      )}
      {create && (
        <CreateGuestDialog
          cluster={cluster}
          nodes={nodes}
          type="lxc"
          preset={{
            node,
            template: create,
            name: (create.split("/").pop() ?? "")
              .replace(/^debian-\d+-turnkey-/, "")
              .replace(/[_].*$/, "")
              .replace(/[^a-z0-9-]/gi, "-")
              .slice(0, 40),
            ...(/turnkey/.test(create) ? { memory: "1024", size: "10", description: "TurnKey appliance: its setup wizard runs on the first console login." } : {}),
          }}
          onClose={() => setCreate(null)}
        />
      )}
    </div>
  );
}
