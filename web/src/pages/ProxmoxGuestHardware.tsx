import { useEffect, useMemo, useState, type FormEvent } from "react";
import { ArrowDown, ArrowUp, Disc, HardDrive, Network, Plus, Save, Trash2, Unplug } from "lucide-react";
import { api, type PVEHardwareOptions, type PVEHardwareReq } from "@/lib/api";
import { useAccess } from "@/lib/access";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label } from "@/components/ui/input";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Select, Textarea } from "@/components/ui/select";
import { useAction, type TabProps } from "@/pages/ProxmoxGuestTabs";

// The guest's hardware and options, editable: CPU and memory, disks,
// network cards, CD drive, boot order, name, tags and notes.

const diskKey = /^(scsi|virtio|sata|ide)\d+$|^(rootfs|mp\d+)$/;
const unusedKey = /^unused\d+$/;
const netKey = /^net\d+$/;
const cpuTypes = ["host", "x86-64-v2-AES", "x86-64-v3", "x86-64-v4", "kvm64", "qemu64", "max"];
const nicModels = ["virtio", "e1000", "e1000e", "vmxnet3", "rtl8139"];
const byNum = (a: string, b: string) => a.localeCompare(b, undefined, { numeric: true });

const str = (v: unknown) => (v === undefined || v === null ? "" : String(v));
const sizeOf = (v: string) => /(?:^|,)size=([^,]+)/.exec(v)?.[1] ?? "";
const isCD = (v: string) => v.includes("media=cdrom");
// Parses "virtio=AA:..,bridge=vmbr0,tag=5,firewall=1" into fields.
function parseNIC(v: string) {
  const out: Record<string, string> = {};
  for (const p of v.split(",")) {
    const [k, val = ""] = p.split("=");
    if (nicModels.includes(k)) {
      out.model = k;
      out.mac = val;
    } else out[k] = val;
  }
  return out;
}

export function HardwareTab({ cluster, guest, detail, onChanged }: TabProps) {
  const ac = useAccess();
  const edit = ac.can("proxmox.guests");
  const cfg = detail.config;
  const vm = detail.type === "qemu";
  const [opts, setOpts] = useState<PVEHardwareOptions | null>(null);
  const { busy, run, note } = useAction();
  useEffect(() => {
    if (edit) void api.pveHardwareOptions(cluster, guest).then(setOpts).catch(() => setOpts({ storages: [], bridges: [], isos: [] }));
  }, [cluster, guest, edit]);

  const save = (label: string, f: () => Promise<unknown>, done: string) => run(label, f, done).then((ok) => ok && onChanged());

  const disks = Object.keys(cfg)
    .filter((k) => (diskKey.test(k) && !isCD(str(cfg[k]))) || unusedKey.test(k))
    .sort(byNum);
  const nics = Object.keys(cfg).filter((k) => netKey.test(k)).sort(byNum);
  const cd = Object.keys(cfg).find((k) => diskKey.test(k) && isCD(str(cfg[k])) && !str(cfg[k]).includes("cloudinit"));

  return (
    <div className="grid gap-4">
      {note}
      {busy && <p className="text-sm text-muted-foreground">{busy}…</p>}
      <div className="grid gap-4 lg:grid-cols-2">
        <CpuMemCard cfg={cfg} vm={vm} edit={edit} onSave={(h) => save("Saving CPU and memory", () => api.pveHardware(cluster, guest, h), "CPU and memory saved.")} />
        <OptionsCard cfg={cfg} vm={vm} edit={edit} onSave={(h) => save("Saving options", () => api.pveHardware(cluster, guest, h), "Options saved.")} />
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <HardDrive className="size-4" /> Disks
          </CardTitle>
          <CardDescription>Disks can only grow. Detaching keeps the data as an unused disk; deleting destroys it.</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-3">
          <table className="w-full text-sm">
            <tbody className="divide-y">
              {disks.map((k) => {
                const v = str(cfg[k]);
                const unused = unusedKey.test(k);
                return (
                  <tr key={k}>
                    <td className="py-2 pr-3 font-mono text-xs">{k}</td>
                    <td className="py-2 pr-3 font-mono text-xs break-all text-muted-foreground">{v.split(",")[0]}</td>
                    <td className="py-2 pr-3 whitespace-nowrap">{unused ? <Badge variant="outline">unused</Badge> : sizeOf(v)}</td>
                    <td className="py-2 text-right whitespace-nowrap">
                      {edit && !unused && (
                        <Button
                          size="sm"
                          variant="ghost"
                          disabled={!!busy}
                          onClick={() => {
                            const n = Number(prompt(`Grow ${k} by how many GB?`, "10"));
                            if (n > 0) void save(`Growing ${k}`, () => api.pveDiskResize(cluster, guest, k, n), `${k} grown by ${n} GB.`);
                          }}
                        >
                          <Plus /> Grow
                        </Button>
                      )}
                      {edit && !unused && k !== "rootfs" && (
                        <Button
                          size="sm"
                          variant="ghost"
                          disabled={!!busy}
                          onClick={() => confirm(`Detach ${k}? Its data is kept as an unused disk.`) && void save(`Detaching ${k}`, () => api.pveDiskRemove(cluster, guest, k, false), `${k} detached.`)}
                        >
                          <Unplug /> Detach
                        </Button>
                      )}
                      {edit && k !== "rootfs" && (vm || unused) && (
                        <Button
                          size="sm"
                          variant="ghost"
                          className="text-destructive"
                          disabled={!!busy}
                          onClick={() => {
                            const c = prompt(`This deletes ${k} and all its data. Type ${k} to confirm.`);
                            if (c) void save(`Deleting ${k}`, () => api.pveDiskRemove(cluster, guest, k, true, c), `${k} deleted.`);
                          }}
                        >
                          <Trash2 /> Delete
                        </Button>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          {edit && opts && <AddDisk vm={vm} storages={opts.storages} busy={!!busy} onAdd={(d) => save("Adding disk", () => api.pveDiskAdd(cluster, guest, d), "Disk added.")} />}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Network className="size-4" /> Network
          </CardTitle>
        </CardHeader>
        <CardContent className="grid gap-3">
          {nics.map((k) => (
            <NICRow
              key={k}
              id={k}
              value={str(cfg[k])}
              vm={vm}
              edit={edit}
              bridges={opts?.bridges ?? []}
              busy={!!busy}
              onSave={(n) => save(`Saving ${k}`, () => api.pveNIC(cluster, guest, k, n), `${k} saved.`)}
              onDelete={() => confirm(`Remove ${k}?`) && void save(`Removing ${k}`, () => api.pveNICDelete(cluster, guest, k), `${k} removed.`)}
            />
          ))}
          {edit && opts && (
            <NICRow
              id={`net${nextFree(nics, "net")}`}
              value=""
              vm={vm}
              edit
              isNew
              bridges={opts.bridges}
              busy={!!busy}
              onSave={(n) => save("Adding network card", () => api.pveNIC(cluster, guest, `net${nextFree(nics, "net")}`, n), "Network card added.")}
            />
          )}
        </CardContent>
      </Card>

      {vm && (
        <div className="grid gap-4 lg:grid-cols-2">
          <CDCard
            current={cd ? str(cfg[cd]).split(",")[0] : ""}
            isos={opts?.isos ?? []}
            edit={edit}
            busy={!!busy}
            onSet={(iso) => save(iso ? "Inserting ISO" : "Ejecting", () => api.pveCDROM(cluster, guest, iso), iso ? "ISO inserted." : "Ejected.")}
          />
          <BootCard
            cfg={cfg}
            edit={edit}
            busy={!!busy}
            onSave={(boot) => save("Saving boot order", () => api.pveHardware(cluster, guest, { boot }), "Boot order saved.")}
          />
        </div>
      )}
    </div>
  );
}

function nextFree(keys: string[], prefix: string) {
  for (let i = 0; ; i++) if (!keys.includes(`${prefix}${i}`)) return i;
}

function CpuMemCard({ cfg, vm, edit, onSave }: { cfg: Record<string, string | number>; vm: boolean; edit: boolean; onSave: (h: PVEHardwareReq) => void }) {
  const [f, setF] = useState({
    cores: str(cfg.cores || 1),
    sockets: str(cfg.sockets || 1),
    cpu: str(cfg.cpu || (vm ? "kvm64" : "")).split(",")[0],
    cpulimit: str(cfg.cpulimit || 0),
    memory: str(cfg.memory || 512),
    balloon: str(cfg.balloon ?? ""),
    swap: str(cfg.swap ?? 512),
  });
  const set = (k: keyof typeof f) => (e: { target: { value: string } }) => setF((x) => ({ ...x, [k]: e.target.value }));
  function submit(e: FormEvent) {
    e.preventDefault();
    const h: PVEHardwareReq = { cores: Number(f.cores), memory: Number(f.memory), cpulimit: Number(f.cpulimit) };
    if (vm) {
      h.sockets = Number(f.sockets);
      if (f.cpu) h.cpu = f.cpu;
      if (f.balloon !== "") h.balloon = Number(f.balloon);
    } else h.swap = Number(f.swap);
    onSave(h);
  }
  return (
    <Card>
      <CardHeader>
        <CardTitle>CPU and memory</CardTitle>
        <CardDescription>Changes to a running guest may need a restart to apply.</CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={submit} className="grid gap-3 sm:grid-cols-2">
          <Field label="Cores">
            <Input type="number" min={1} max={512} value={f.cores} onChange={set("cores")} disabled={!edit} />
          </Field>
          {vm && (
            <Field label="Sockets">
              <Input type="number" min={1} max={8} value={f.sockets} onChange={set("sockets")} disabled={!edit} />
            </Field>
          )}
          {vm && (
            <Field label="CPU type">
              <Select value={f.cpu} onChange={set("cpu")} disabled={!edit}>
                {[...new Set([f.cpu, ...cpuTypes])].filter(Boolean).map((c) => (
                  <option key={c}>{c}</option>
                ))}
              </Select>
            </Field>
          )}
          <Field label="CPU limit (0 = none)">
            <Input type="number" min={0} step="0.1" value={f.cpulimit} onChange={set("cpulimit")} disabled={!edit} />
          </Field>
          <Field label="Memory (MiB)">
            <Input type="number" min={16} step={256} value={f.memory} onChange={set("memory")} disabled={!edit} />
          </Field>
          {vm ? (
            <Field label="Minimum memory (MiB, 0 = no ballooning)">
              <Input type="number" min={0} step={256} value={f.balloon} placeholder="same as memory" onChange={set("balloon")} disabled={!edit} />
            </Field>
          ) : (
            <Field label="Swap (MiB)">
              <Input type="number" min={0} step={256} value={f.swap} onChange={set("swap")} disabled={!edit} />
            </Field>
          )}
          {edit && (
            <div className="sm:col-span-2">
              <Button size="sm" variant="brand">
                <Save /> Save
              </Button>
            </div>
          )}
        </form>
      </CardContent>
    </Card>
  );
}

function OptionsCard({ cfg, vm, edit, onSave }: { cfg: Record<string, string | number>; vm: boolean; edit: boolean; onSave: (h: PVEHardwareReq) => void }) {
  const truthy = (v: unknown) => ["1", "yes", "on", "true"].includes(str(v).split(",")[0].replace("enabled=", ""));
  const [f, setF] = useState({
    name: str(vm ? cfg.name : cfg.hostname),
    tags: str(cfg.tags).split(/[;, ]/).filter(Boolean).join(", "),
    description: str(cfg.description),
    onboot: truthy(cfg.onboot),
    protection: truthy(cfg.protection),
    agent: truthy(cfg.agent),
  });
  function submit(e: FormEvent) {
    e.preventDefault();
    const h: PVEHardwareReq = {
      name: f.name,
      tags: f.tags
        .split(/[;,\s]+/)
        .map((t) => t.trim().toLowerCase())
        .filter(Boolean),
      description: f.description,
      onboot: f.onboot,
      protection: f.protection,
    };
    if (vm) h.agent = f.agent;
    onSave(h);
  }
  const check = (k: "onboot" | "protection" | "agent", label: string) => (
    <label className="flex items-center gap-2 text-sm">
      <input type="checkbox" checked={f[k]} disabled={!edit} onChange={(e) => setF((x) => ({ ...x, [k]: e.target.checked }))} /> {label}
    </label>
  );
  return (
    <Card>
      <CardHeader>
        <CardTitle>Options</CardTitle>
      </CardHeader>
      <CardContent>
        <form onSubmit={submit} className="grid gap-3">
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Name">
              <Input value={f.name} onChange={(e) => setF((x) => ({ ...x, name: e.target.value }))} disabled={!edit} />
            </Field>
            <Field label="Tags (comma separated)">
              <Input value={f.tags} onChange={(e) => setF((x) => ({ ...x, tags: e.target.value }))} disabled={!edit} placeholder="web, prod" />
            </Field>
          </div>
          <Field label="Notes">
            <Textarea rows={3} value={f.description} onChange={(e) => setF((x) => ({ ...x, description: e.target.value }))} disabled={!edit} />
          </Field>
          <div className="flex flex-wrap gap-4">
            {check("onboot", "Start at boot")}
            {check("protection", "Protection (blocks deletion)")}
            {vm && check("agent", "QEMU guest agent")}
          </div>
          {edit && (
            <div>
              <Button size="sm" variant="brand">
                <Save /> Save
              </Button>
            </div>
          )}
        </form>
      </CardContent>
    </Card>
  );
}

function AddDisk({ vm, storages, busy, onAdd }: { vm: boolean; storages: string[]; busy: boolean; onAdd: (d: { bus?: string; storage: string; size_gb: number; path?: string }) => void }) {
  const [bus, setBus] = useState("scsi");
  const [storage, setStorage] = useState(storages[0] ?? "");
  const [size, setSize] = useState("10");
  const [path, setPath] = useState("/data");
  if (!storages.length) return <p className="text-xs text-muted-foreground">No storage for disks is available to you.</p>;
  return (
    <form
      className="flex flex-wrap items-end gap-2 border-t pt-3"
      onSubmit={(e) => {
        e.preventDefault();
        onAdd(vm ? { bus, storage, size_gb: Number(size) } : { storage, size_gb: Number(size), path });
      }}
    >
      {vm ? (
        <Field label="Bus" className="w-28">
          <Select value={bus} onChange={(e) => setBus(e.target.value)}>
            <option>scsi</option>
            <option>virtio</option>
            <option>sata</option>
          </Select>
        </Field>
      ) : (
        <Field label="Mount point" className="w-36">
          <Input value={path} onChange={(e) => setPath(e.target.value)} />
        </Field>
      )}
      <Field label="Storage" className="w-40">
        <Select value={storage} onChange={(e) => setStorage(e.target.value)}>
          {storages.map((s) => (
            <option key={s}>{s}</option>
          ))}
        </Select>
      </Field>
      <Field label="Size (GB)" className="w-28">
        <Input type="number" min={1} value={size} onChange={(e) => setSize(e.target.value)} />
      </Field>
      <Button size="sm" variant="secondary" disabled={busy}>
        <Plus /> Add disk
      </Button>
    </form>
  );
}

function NICRow(p: {
  id: string;
  value: string;
  vm: boolean;
  edit: boolean;
  isNew?: boolean;
  bridges: string[];
  busy: boolean;
  onSave: (n: { model?: string; bridge: string; vlan?: number; firewall?: boolean; rate?: number; name?: string; ip?: string; gw?: string }) => void;
  onDelete?: () => void;
}) {
  const cur = useMemo(() => parseNIC(p.value), [p.value]);
  const [f, setF] = useState({
    model: cur.model || "virtio",
    bridge: cur.bridge || p.bridges[0] || "",
    vlan: cur.tag || "",
    firewall: cur.firewall === "1",
    rate: cur.rate || "",
    name: cur.name || p.id.replace("net", "eth"),
    ip: cur.ip || "dhcp",
    gw: cur.gw || "",
  });
  const [open, setOpen] = useState(false);
  const set = (k: keyof typeof f) => (e: { target: { value: string } }) => setF((x) => ({ ...x, [k]: e.target.value }));
  if (!open)
    return (
      <div className="flex flex-wrap items-center gap-2 text-sm">
        {p.isNew ? (
          <Button size="sm" variant="secondary" disabled={p.busy || !p.bridges.length} onClick={() => setOpen(true)}>
            <Plus /> Add network card
          </Button>
        ) : (
          <>
            <span className="font-mono text-xs">{p.id}</span>
            <span className="font-mono text-xs break-all text-muted-foreground">{p.value}</span>
            {p.edit && (
              <span className="ml-auto flex gap-1">
                <Button size="sm" variant="ghost" disabled={p.busy} onClick={() => setOpen(true)}>
                  Edit
                </Button>
                <Button size="sm" variant="ghost" className="text-destructive" disabled={p.busy} onClick={p.onDelete}>
                  <Trash2 />
                </Button>
              </span>
            )}
          </>
        )}
      </div>
    );
  return (
    <form
      className="flex flex-wrap items-end gap-2 rounded-lg border p-3"
      onSubmit={(e) => {
        e.preventDefault();
        const n = { bridge: f.bridge, vlan: Number(f.vlan) || 0, firewall: f.firewall, rate: Number(f.rate) || 0 };
        p.onSave(p.vm ? { ...n, model: f.model } : { ...n, name: f.name, ip: f.ip, gw: f.gw });
        setOpen(false);
      }}
    >
      <span className="w-full font-mono text-xs">{p.id}</span>
      {p.vm ? (
        <Field label="Model" className="w-32">
          <Select value={f.model} onChange={set("model")}>
            {nicModels.map((m) => (
              <option key={m}>{m}</option>
            ))}
          </Select>
        </Field>
      ) : (
        <Field label="Interface" className="w-24">
          <Input value={f.name} onChange={set("name")} />
        </Field>
      )}
      <Field label="Bridge" className="w-32">
        <Select value={f.bridge} onChange={set("bridge")}>
          {[...new Set([f.bridge, ...p.bridges])].filter(Boolean).map((b) => (
            <option key={b}>{b}</option>
          ))}
        </Select>
      </Field>
      <Field label="VLAN" className="w-20">
        <Input type="number" min={0} max={4094} value={f.vlan} onChange={set("vlan")} placeholder="none" />
      </Field>
      {!p.vm && (
        <>
          <Field label="IPv4" className="w-40">
            <Input value={f.ip} onChange={set("ip")} placeholder="dhcp or 10.0.0.5/24" />
          </Field>
          <Field label="Gateway" className="w-32">
            <Input value={f.gw} onChange={set("gw")} />
          </Field>
        </>
      )}
      <Field label="Rate (MB/s)" className="w-24">
        <Input type="number" min={0} value={f.rate} onChange={set("rate")} placeholder="no limit" />
      </Field>
      <label className="flex h-9 items-center gap-2 text-sm">
        <input type="checkbox" checked={f.firewall} onChange={(e) => setF((x) => ({ ...x, firewall: e.target.checked }))} /> Firewall
      </label>
      <Button size="sm" variant="brand" disabled={p.busy}>
        <Save /> Save
      </Button>
      <Button size="sm" variant="ghost" type="button" onClick={() => setOpen(false)}>
        Cancel
      </Button>
    </form>
  );
}

function CDCard({ current, isos, edit, busy, onSet }: { current: string; isos: string[]; edit: boolean; busy: boolean; onSet: (iso: string) => void }) {
  const [picked, setIso] = useState(current && current !== "none" ? current : "");
  // The ISO list loads after the card first renders.
  const iso = picked || isos[0] || "";
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Disc className="size-4" /> CD/DVD drive
        </CardTitle>
        <CardDescription>{current && current !== "none" ? `Inserted: ${current}` : "Empty"}</CardDescription>
      </CardHeader>
      {edit && (
        <CardContent className="flex flex-wrap items-end gap-2">
          {isos.length ? (
            <Select value={iso} onChange={(e) => setIso(e.target.value)} className="min-w-0 flex-1">
              {isos.map((i) => (
                <option key={i}>{i}</option>
              ))}
            </Select>
          ) : (
            <p className="text-xs text-muted-foreground">No ISO images on storages available to you.</p>
          )}
          {isos.length > 0 && (
            <Button size="sm" variant="secondary" disabled={busy || !iso} onClick={() => onSet(iso)}>
              Insert
            </Button>
          )}
          {current && current !== "none" && (
            <Button size="sm" variant="ghost" disabled={busy} onClick={() => onSet("")}>
              Eject
            </Button>
          )}
        </CardContent>
      )}
    </Card>
  );
}

function BootCard({ cfg, edit, busy, onSave }: { cfg: Record<string, string | number>; edit: boolean; busy: boolean; onSave: (boot: string[]) => void }) {
  const devices = Object.keys(cfg)
    .filter((k) => (diskKey.test(k) && !str(cfg[k]).includes("cloudinit")) || netKey.test(k))
    .filter((k) => k !== "rootfs" && !k.startsWith("mp"))
    .sort(byNum);
  const current = (/order=([^,]+)/.exec(str(cfg.boot))?.[1] ?? "").split(";").filter((d) => devices.includes(d));
  const [order, setOrder] = useState<string[]>(current.length ? current : devices.slice(0, 1));
  const move = (i: number, d: number) =>
    setOrder((o) => {
      const n = [...o];
      [n[i], n[i + d]] = [n[i + d], n[i]];
      return n;
    });
  return (
    <Card>
      <CardHeader>
        <CardTitle>Boot order</CardTitle>
        <CardDescription>Devices tried in this order; unticked ones are skipped.</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-1">
        {[...order, ...devices.filter((d) => !order.includes(d))].map((d) => {
          const i = order.indexOf(d);
          return (
            <div key={d} className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={i >= 0}
                disabled={!edit}
                onChange={(e) => setOrder((o) => (e.target.checked ? [...o, d] : o.filter((x) => x !== d)))}
              />
              <span className="w-5 text-xs text-muted-foreground">{i >= 0 ? i + 1 : ""}</span>
              <span className="font-mono text-xs">{d}</span>
              <span className="truncate text-xs text-muted-foreground">{str(cfg[d]).split(",")[0]}</span>
              {edit && i >= 0 && (
                <span className="ml-auto flex">
                  <Button size="icon" variant="ghost" className="size-7" disabled={i === 0} onClick={() => move(i, -1)} aria-label="Up">
                    <ArrowUp className="size-3.5" />
                  </Button>
                  <Button size="icon" variant="ghost" className="size-7" disabled={i === order.length - 1} onClick={() => move(i, 1)} aria-label="Down">
                    <ArrowDown className="size-3.5" />
                  </Button>
                </span>
              )}
            </div>
          );
        })}
        {edit && (
          <div className="mt-2">
            <Button size="sm" variant="brand" disabled={busy || !order.length} onClick={() => onSave(order)}>
              <Save /> Save boot order
            </Button>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function Field({ label, className = "", children }: { label: string; className?: string; children: React.ReactNode }) {
  return (
    <div className={`grid gap-1.5 ${className}`}>
      <Label className="text-xs text-muted-foreground">{label}</Label>
      {children}
    </div>
  );
}

// ---- clone, migrate, convert to template ----

export function GuestMoveDialog({
  mode,
  cluster,
  guest,
  name,
  running,
  onClose,
  onDone,
}: {
  mode: "clone" | "migrate";
  cluster: string;
  guest: TabProps["guest"];
  name: string;
  running: boolean;
  onClose: () => void;
  onDone: (text: string) => void;
}) {
  const ac = useAccess();
  const platform = ac.can("proxmox.manage");
  const [nodes, setNodes] = useState<string[]>([]);
  const [storages, setStorages] = useState<string[]>([]);
  const [f, setF] = useState({ name: `${name}-copy`, full: true, target: "", storage: "", online: running });
  const { busy, run, note } = useAction();
  useEffect(() => {
    void api
      .pveResources(cluster)
      .then((r) => setNodes((r.nodes ?? []).filter((n) => n.status === "online" && n.node !== guest.node).map((n) => n.node)))
      .catch(() => {});
    void api
      .pveHardwareOptions(cluster, guest)
      .then((o) => setStorages(o.storages))
      .catch(() => {});
  }, [cluster, guest]);
  useEffect(() => {
    if (mode === "migrate" && !f.target && nodes[0]) setF((x) => ({ ...x, target: nodes[0] }));
  }, [mode, nodes, f.target]);
  async function submit(e: FormEvent) {
    e.preventDefault();
    const ok =
      mode === "clone"
        ? await run("Starting clone", () => api.pveClone(cluster, guest, { name: f.name, full: f.full, target: f.target || undefined, storage: f.storage || undefined }))
        : await run("Starting migration", () => api.pveMigrate(cluster, guest, f.target, f.online));
    if (ok) onDone(mode === "clone" ? `Cloning to ${f.name}; follow it under Tasks.` : `Migrating to ${f.target}; follow it under Tasks.`);
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{mode === "clone" ? `Clone ${name}` : `Migrate ${name}`}</DialogTitle>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-3">
          {mode === "clone" && (
            <>
              <Field label="New name">
                <Input value={f.name} onChange={(e) => setF((x) => ({ ...x, name: e.target.value }))} required />
              </Field>
              <Field label="Storage">
                <Select value={f.storage} onChange={(e) => setF((x) => ({ ...x, storage: e.target.value }))}>
                  {platform && <option value="">same as the source</option>}
                  {storages.map((s) => (
                    <option key={s}>{s}</option>
                  ))}
                </Select>
              </Field>
              {platform && (
                <>
                  <Field label="Node">
                    <Select value={f.target} onChange={(e) => setF((x) => ({ ...x, target: e.target.value }))}>
                      <option value="">same node ({guest.node})</option>
                      {nodes.map((n) => (
                        <option key={n}>{n}</option>
                      ))}
                    </Select>
                  </Field>
                  <label className="flex items-center gap-2 text-sm">
                    <input type="checkbox" checked={f.full} onChange={(e) => setF((x) => ({ ...x, full: e.target.checked }))} /> Full clone (a linked clone needs a
                    template)
                  </label>
                </>
              )}
            </>
          )}
          {mode === "migrate" && (
            <>
              <Field label="Target node">
                <Select value={f.target} onChange={(e) => setF((x) => ({ ...x, target: e.target.value }))}>
                  {nodes.map((n) => (
                    <option key={n}>{n}</option>
                  ))}
                </Select>
              </Field>
              {running && (
                <label className="flex items-center gap-2 text-sm">
                  <input type="checkbox" checked={f.online} onChange={(e) => setF((x) => ({ ...x, online: e.target.checked }))} />
                  {guest.type === "qemu" ? "Live migration (keeps it running)" : "Restart the container on the new node"}
                </label>
              )}
              {!nodes.length && <p className="text-sm text-muted-foreground">There is no other online node.</p>}
            </>
          )}
          {note}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button variant="brand" disabled={!!busy || (mode === "migrate" && !f.target)}>
              {busy || (mode === "clone" ? "Clone" : "Migrate")}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
