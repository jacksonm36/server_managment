import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useLocation } from "wouter";
import { Cpu, Disc3, HardDrive, ListOrdered, Network, Pencil, Plus, Scaling, StickyNote, Trash2, Truck } from "lucide-react";
import { type PVEGuestDetail, type PVEGuestRef } from "@/lib/api";
import { pve, ranMsg, type PVEChoices } from "@/lib/pveapi";
import { formatBytes } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input, Label } from "@/components/ui/input";
import { Select, Textarea } from "@/components/ui/select";

// Guest hardware and options, editable: CPU and memory, disks (add,
// resize, move, detach), network cards, CD/DVD drives, boot order, notes,
// tags and any other configuration key. Also clone, migrate, convert to
// template and the create-VM/CT dialog.

const hardwareKey =
  /^(cores|sockets|cpu|cpulimit|cpuunits|vcpus|memory|balloon|swap|bios|machine|scsihw|vga|arch|rootfs|efidisk\d|tpmstate\d|(scsi|virtio|sata|ide|net|mp|unused|usb|hostpci|serial)\d+)$/;
const hiddenKey = new Set(["digest", "description", "meta", "vmgenid", "smbios1", "lock"]);
const qemuDisk = /^(scsi|virtio|sata|ide)\d+$/;
const lxcDisk = /^(rootfs|mp\d+)$/;

type Cfg = Record<string, string | number>;

export function isCdrom(v: string | number) {
  return String(v).includes("media=cdrom");
}
function isDisk(type: string, k: string, v: string | number) {
  return type === "lxc" ? lxcDisk.test(k) : qemuDisk.test(k) && !isCdrom(v);
}
function nextSlot(cfg: Cfg, prefix: string, max: number, from = 0) {
  for (let i = from; i <= max; i++) if (!(`${prefix}${i}` in cfg)) return `${prefix}${i}`;
  return "";
}
function sizeOf(v: string | number) {
  return /(?:^|,)size=([^,]+)/.exec(String(v))?.[1] ?? "";
}
const errText = (e: unknown) => (e as Error).message;

export function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <div className="grid gap-1.5">
      <Label>{label}</Label>
      {children}
      {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
    </div>
  );
}

// FormDialog is the frame every form here uses: title, fields, error and
// a submit button that shows progress.
export function FormDialog({
  title,
  description,
  children,
  submit,
  onSubmit,
  onClose,
  danger,
  wide,
}: {
  title: string;
  description?: ReactNode;
  children: ReactNode;
  submit: string;
  onSubmit: () => Promise<string | void>;
  onClose: () => void;
  danger?: boolean;
  wide?: boolean;
}) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className={wide ? "sm:max-w-2xl" : undefined}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          {description && <DialogDescription>{description}</DialogDescription>}
        </DialogHeader>
        <form
          className="grid max-h-[70vh] gap-4 overflow-y-auto px-0.5"
          onSubmit={async (e) => {
            e.preventDefault();
            setBusy(true);
            setErr("");
            try {
              await onSubmit();
              onClose();
            } catch (x) {
              setErr(errText(x));
            } finally {
              setBusy(false);
            }
          }}
        >
          {children}
          {err && <p className="text-sm text-destructive">{err}</p>}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" variant={danger ? "destructive" : "brand"} disabled={busy}>
              {busy ? "Working…" : submit}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// useChoices loads what a node offers (storages, ISOs, bridges…).
export function useChoices(cluster: string, node: string) {
  const [c, setC] = useState<PVEChoices | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => {
    if (!node) return;
    setC(null);
    pve
      .choices(cluster, node)
      .then(setC)
      .catch((e) => setErr(errText(e)));
  }, [cluster, node]);
  return { c, err };
}

const storagesFor = (c: PVEChoices | null, content: string) => (c?.storages ?? []).filter((s) => s.content.split(",").includes(content));

function StorageSelect({ c, content, value, onChange }: { c: PVEChoices | null; content: string; value: string; onChange: (v: string) => void }) {
  const list = storagesFor(c, content);
  return (
    <Select value={value} onChange={(e) => onChange(e.target.value)} required>
      <option value="">{c ? (list.length ? "Pick a storage…" : "No storage for this content") : "Loading…"}</option>
      {list.map((s) => (
        <option key={s.storage} value={s.storage}>
          {s.storage} ({s.type}, {formatBytes(s.avail)} free)
        </option>
      ))}
    </Select>
  );
}

type Dlg =
  | { k: "cpu" }
  | { k: "disk" }
  | { k: "nic" }
  | { k: "cdrom"; key?: string }
  | { k: "boot" }
  | { k: "raw"; key?: string }
  | { k: "notes" }
  | { k: "resize"; key: string }
  | { k: "move"; key: string };

export function HardwareTab({
  cluster,
  guest,
  detail,
  canEdit,
  onChanged,
}: {
  cluster: string;
  guest: PVEGuestRef;
  detail: PVEGuestDetail;
  canEdit: boolean;
  onChanged: () => void;
}) {
  const cfg = detail.config as Cfg;
  const digest = typeof cfg.digest === "string" ? cfg.digest : undefined;
  const [dlg, setDlg] = useState<Dlg | null>(null);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const { c } = useChoices(cluster, dlg ? guest.node : "");
  const qemu = guest.type === "qemu";

  const hw = Object.entries(cfg)
    .filter(([k]) => hardwareKey.test(k))
    .sort(([a], [b]) => a.localeCompare(b, undefined, { numeric: true }));
  const opts = Object.entries(cfg)
    .filter(([k]) => !hardwareKey.test(k) && !hiddenKey.has(k))
    .sort(([a], [b]) => a.localeCompare(b));
  const desc = typeof cfg.description === "string" ? cfg.description : "";

  const apply = async (set: Record<string, string>, del: string[] = [], done = "Saved.") => {
    const r = await pve.config(cluster, guest, set, del, digest);
    setMsg({ ok: true, text: ranMsg(r, done) });
    onChanged();
  };
  const remove = async (k: string, v: string | number) => {
    const what = k.startsWith("unused")
      ? `Destroy ${k} (${v})? The disk image is deleted and cannot be recovered.`
      : isDisk(guest.type, k, v)
        ? `Detach ${k}? It stays on the storage as an unused disk until you remove that too.`
        : `Remove ${k} from the configuration?`;
    if (!confirm(what)) return;
    try {
      await apply({}, [k], `${k} removed.`);
    } catch (e) {
      setMsg({ ok: false, text: errText(e) });
    }
  };

  const rowActions = (k: string, v: string | number) =>
    canEdit && (
      <div className="flex shrink-0 gap-0.5">
        {isDisk(guest.type, k, v) && (
          <>
            <IconBtn label={`Resize ${k}`} onClick={() => setDlg({ k: "resize", key: k })}>
              <Scaling />
            </IconBtn>
            <IconBtn label={`Move ${k} to another storage`} onClick={() => setDlg({ k: "move", key: k })}>
              <Truck />
            </IconBtn>
          </>
        )}
        {qemu && isCdrom(v) && (
          <IconBtn label={`Change the ISO in ${k}`} onClick={() => setDlg({ k: "cdrom", key: k })}>
            <Disc3 />
          </IconBtn>
        )}
        {String(v) !== "••••••" && (
          <IconBtn label={`Edit ${k}`} onClick={() => setDlg({ k: "raw", key: k })}>
            <Pencil />
          </IconBtn>
        )}
        {k !== "rootfs" && k !== "memory" && k !== "cores" && (
          <IconBtn label={`Remove ${k}`} className="text-destructive" onClick={() => remove(k, v)}>
            <Trash2 />
          </IconBtn>
        )}
      </div>
    );

  const done = () => setDlg(null);
  return (
    <div className="grid gap-4">
      {msg && <p className={`text-sm ${msg.ok ? "text-success" : "text-destructive"}`}>{msg.text}</p>}
      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader className="gap-3">
            <div>
              <CardTitle>Hardware</CardTitle>
              <CardDescription>Changes to a running guest apply at once where Proxmox can hot-plug them, otherwise at the next start.</CardDescription>
            </div>
            {canEdit && (
              <div className="flex flex-wrap gap-1.5">
                <Button size="sm" variant="secondary" onClick={() => setDlg({ k: "cpu" })}>
                  <Cpu /> CPU &amp; memory
                </Button>
                <Button size="sm" variant="secondary" onClick={() => setDlg({ k: "disk" })}>
                  <HardDrive /> {qemu ? "Add disk" : "Add mount point"}
                </Button>
                <Button size="sm" variant="secondary" onClick={() => setDlg({ k: "nic" })}>
                  <Network /> Add network
                </Button>
                {qemu && (
                  <>
                    <Button size="sm" variant="secondary" onClick={() => setDlg({ k: "cdrom" })}>
                      <Disc3 /> Add CD/DVD
                    </Button>
                    <Button size="sm" variant="secondary" onClick={() => setDlg({ k: "boot" })}>
                      <ListOrdered /> Boot order
                    </Button>
                  </>
                )}
                <Button size="sm" variant="ghost" onClick={() => setDlg({ k: "raw" })}>
                  <Plus /> Other setting
                </Button>
              </div>
            )}
          </CardHeader>
          <CardContent>
            <Rows rows={hw} actions={rowActions} />
          </CardContent>
        </Card>
        <div className="grid content-start gap-4">
          <Card>
            <CardHeader>
              <CardTitle>Options</CardTitle>
            </CardHeader>
            <CardContent>
              <Rows rows={opts} actions={rowActions} />
            </CardContent>
          </Card>
          <Card>
            <CardHeader className="flex-row items-start justify-between gap-3">
              <CardTitle>Notes</CardTitle>
              {canEdit && (
                <Button size="sm" variant="ghost" onClick={() => setDlg({ k: "notes" })}>
                  <StickyNote /> Edit
                </Button>
              )}
            </CardHeader>
            <CardContent>
              {desc.trim() ? <pre className="text-sm whitespace-pre-wrap">{desc}</pre> : <p className="text-sm text-muted-foreground">No notes.</p>}
            </CardContent>
          </Card>
        </div>
      </div>

      {dlg?.k === "cpu" && <CpuDialog cfg={cfg} qemu={qemu} onClose={done} apply={apply} />}
      {dlg?.k === "disk" && <AddDiskDialog cfg={cfg} qemu={qemu} c={c} onClose={done} apply={apply} />}
      {dlg?.k === "nic" && <AddNicDialog cfg={cfg} qemu={qemu} c={c} onClose={done} apply={apply} />}
      {dlg?.k === "cdrom" && <CdromDialog cfg={cfg} slot={dlg.key} c={c} onClose={done} apply={apply} />}
      {dlg?.k === "boot" && <BootDialog cfg={cfg} onClose={done} apply={apply} />}
      {dlg?.k === "raw" && <RawDialog cfg={cfg} fixedKey={dlg.key} onClose={done} apply={apply} />}
      {dlg?.k === "notes" && <NotesDialog initial={desc} onClose={done} apply={apply} />}
      {dlg?.k === "resize" && (
        <ResizeDialog
          disk={dlg.key}
          current={sizeOf(cfg[dlg.key])}
          onClose={done}
          run={async (size) => {
            const r = await pve.resize(cluster, guest, dlg.key, size);
            setMsg({ ok: true, text: ranMsg(r, `${dlg.key} resized.`) });
            onChanged();
          }}
        />
      )}
      {dlg?.k === "move" && (
        <MoveDiskDialog
          disk={dlg.key}
          c={c}
          content={qemu ? "images" : "rootdir"}
          onClose={done}
          run={async (storage, del) => {
            const r = await pve.moveDisk(cluster, guest, dlg.key, storage, del);
            setMsg({ ok: true, text: ranMsg(r, `${dlg.key} moved.`, `Moving ${dlg.key}; follow it under Tasks.`) });
            onChanged();
          }}
        />
      )}
    </div>
  );
}

function NotesDialog({ initial, onClose, apply }: { initial: string; onClose: () => void; apply: Apply }) {
  const [v, setV] = useState(initial);
  return (
    <FormDialog title="Notes" submit="Save" onClose={onClose} onSubmit={() => apply({ description: v }, [], "Notes saved.")}>
      <Textarea rows={10} value={v} onChange={(e) => setV(e.target.value)} placeholder="Notes shown in Proxmox's summary (Markdown)" />
    </FormDialog>
  );
}

function IconBtn({ label, onClick, children, className }: { label: string; onClick: () => void; children: ReactNode; className?: string }) {
  return (
    <Button size="icon" variant="ghost" className={`size-7 ${className ?? ""}`} aria-label={label} title={label} onClick={onClick}>
      {children}
    </Button>
  );
}

function shown(k: string, v: string | number) {
  if (k !== "sshkeys") return String(v);
  try {
    return decodeURIComponent(String(v));
  } catch {
    return String(v);
  }
}

function Rows({ rows, actions }: { rows: [string, string | number][]; actions: (k: string, v: string | number) => ReactNode }) {
  if (rows.length === 0) return <p className="text-sm text-muted-foreground">Nothing set.</p>;
  return (
    <dl className="divide-y text-sm">
      {rows.map(([k, v]) => (
        <div key={k} className="grid grid-cols-[7rem_1fr_auto] items-center gap-3 py-1.5">
          <dt className="text-muted-foreground">{k}</dt>
          <dd className="min-w-0 font-mono text-xs break-all whitespace-pre-wrap">{shown(k, v)}</dd>
          {actions(k, v)}
        </div>
      ))}
    </dl>
  );
}

type Apply = (set: Record<string, string>, del?: string[], done?: string) => Promise<void>;

function CpuDialog({ cfg, qemu, onClose, apply }: { cfg: Cfg; qemu: boolean; onClose: () => void; apply: Apply }) {
  const [cores, setCores] = useState(String(cfg.cores ?? 1));
  const [sockets, setSockets] = useState(String(cfg.sockets ?? 1));
  const [cpu, setCpu] = useState(String(cfg.cpu ?? (qemu ? "x86-64-v2-AES" : "")));
  const [memory, setMemory] = useState(String(cfg.memory ?? 512));
  const [balloon, setBalloon] = useState(cfg.balloon === undefined ? "" : String(cfg.balloon));
  const [swap, setSwap] = useState(String(cfg.swap ?? 512));
  return (
    <FormDialog
      title="CPU & memory"
      submit="Save"
      onClose={onClose}
      onSubmit={() => {
        const set: Record<string, string> = { cores, memory };
        const del: string[] = [];
        if (qemu) {
          set.sockets = sockets;
          if (cpu) set.cpu = cpu;
          if (balloon !== "") set.balloon = balloon;
          else if (cfg.balloon !== undefined) del.push("balloon");
        } else {
          set.swap = swap;
        }
        return apply(set, del, "CPU and memory saved.");
      }}
    >
      <div className="grid grid-cols-2 gap-3">
        <Field label="Cores">
          <Input type="number" min={1} max={512} value={cores} onChange={(e) => setCores(e.target.value)} required />
        </Field>
        {qemu && (
          <Field label="Sockets">
            <Input type="number" min={1} max={8} value={sockets} onChange={(e) => setSockets(e.target.value)} required />
          </Field>
        )}
        <Field label="Memory (MiB)">
          <Input type="number" min={16} value={memory} onChange={(e) => setMemory(e.target.value)} required />
        </Field>
        {qemu ? (
          <Field label="Minimum memory (MiB)" hint="Ballooning floor; 0 turns ballooning off, empty keeps the default.">
            <Input type="number" min={0} value={balloon} onChange={(e) => setBalloon(e.target.value)} />
          </Field>
        ) : (
          <Field label="Swap (MiB)">
            <Input type="number" min={0} value={swap} onChange={(e) => setSwap(e.target.value)} required />
          </Field>
        )}
      </div>
      {qemu && (
        <Field label="CPU type" hint="host passes the host CPU through (fastest, but blocks live migration between different CPUs).">
          <Input list="pve-cpu-types" value={cpu} onChange={(e) => setCpu(e.target.value)} />
          <datalist id="pve-cpu-types">
            {["host", "x86-64-v2-AES", "x86-64-v3", "x86-64-v4", "kvm64", "qemu64", "max"].map((t) => (
              <option key={t} value={t} />
            ))}
          </datalist>
        </Field>
      )}
    </FormDialog>
  );
}

function AddDiskDialog({ cfg, qemu, c, onClose, apply }: { cfg: Cfg; qemu: boolean; c: PVEChoices | null; onClose: () => void; apply: Apply }) {
  const [bus, setBus] = useState("scsi");
  const [storage, setStorage] = useState("");
  const [size, setSize] = useState("32");
  const [discard, setDiscard] = useState(true);
  const [ssd, setSsd] = useState(true);
  const [iothread, setIothread] = useState(true);
  const [mp, setMp] = useState("/mnt/data");
  const [backup, setBackup] = useState(true);
  const max = { scsi: 30, virtio: 15, sata: 5, ide: 3 }[bus] ?? 30;
  const slot = qemu ? nextSlot(cfg, bus, max) : nextSlot(cfg, "mp", 255);
  return (
    <FormDialog
      title={qemu ? "Add disk" : "Add mount point"}
      description={slot ? `It becomes ${slot}.` : "No free slot on this bus."}
      submit="Add"
      onClose={onClose}
      onSubmit={() => {
        if (!slot) throw new Error("no free slot");
        let v = `${storage}:${size}`;
        if (qemu) {
          if (discard) v += ",discard=on";
          if (ssd && bus !== "virtio") v += ",ssd=1";
          if (iothread && (bus === "scsi" || bus === "virtio")) v += ",iothread=1";
        } else {
          v += `,mp=${mp}`;
          if (backup) v += ",backup=1";
        }
        return apply({ [slot]: v }, [], `${slot} added.`);
      }}
    >
      {qemu && (
        <Field label="Bus">
          <Select value={bus} onChange={(e) => setBus(e.target.value)}>
            <option value="scsi">SCSI (recommended)</option>
            <option value="virtio">VirtIO block</option>
            <option value="sata">SATA</option>
            <option value="ide">IDE</option>
          </Select>
        </Field>
      )}
      <Field label="Storage">
        <StorageSelect c={c} content={qemu ? "images" : "rootdir"} value={storage} onChange={setStorage} />
      </Field>
      <Field label="Size (GiB)">
        <Input type="number" min={1} value={size} onChange={(e) => setSize(e.target.value)} required />
      </Field>
      {qemu ? (
        <div className="flex flex-wrap gap-4 text-sm">
          <Check label="Discard (TRIM)" v={discard} set={setDiscard} />
          {bus !== "virtio" && <Check label="SSD emulation" v={ssd} set={setSsd} />}
          {(bus === "scsi" || bus === "virtio") && <Check label="IO thread" v={iothread} set={setIothread} />}
        </div>
      ) : (
        <>
          <Field label="Path in the container">
            <Input value={mp} onChange={(e) => setMp(e.target.value)} required pattern="/.*" />
          </Field>
          <Check label="Include in backups" v={backup} set={setBackup} />
        </>
      )}
    </FormDialog>
  );
}

export function Check({ label, v, set }: { label: string; v: boolean; set: (b: boolean) => void }) {
  return (
    <label className="flex cursor-pointer items-center gap-2 text-sm">
      <input type="checkbox" checked={v} onChange={(e) => set(e.target.checked)} className="size-4 accent-[var(--color-brand)]" />
      {label}
    </label>
  );
}

function AddNicDialog({ cfg, qemu, c, onClose, apply }: { cfg: Cfg; qemu: boolean; c: PVEChoices | null; onClose: () => void; apply: Apply }) {
  const slot = nextSlot(cfg, "net", 31);
  const [model, setModel] = useState("virtio");
  const [bridge, setBridge] = useState("");
  const [tag, setTag] = useState("");
  const [fw, setFw] = useState(true);
  const [mac, setMac] = useState("");
  const [ip, setIp] = useState("dhcp");
  const [gw, setGw] = useState("");
  useEffect(() => {
    if (!bridge && c?.bridges.length) setBridge(c.bridges[0]);
  }, [c, bridge]);
  return (
    <FormDialog
      title="Add network device"
      description={slot ? `It becomes ${slot}.` : "No free network slot."}
      submit="Add"
      onClose={onClose}
      onSubmit={() => {
        if (!slot) throw new Error("no free slot");
        const parts = qemu ? [mac ? `${model}=${mac}` : model, `bridge=${bridge}`] : [`name=eth${slot.slice(3)}`, `bridge=${bridge}`];
        if (!qemu) {
          if (mac) parts.push(`hwaddr=${mac}`);
          parts.push(`ip=${ip}`);
          if (gw && ip !== "dhcp" && ip !== "manual") parts.push(`gw=${gw}`);
        }
        if (tag) parts.push(`tag=${tag}`);
        if (fw) parts.push("firewall=1");
        return apply({ [slot]: parts.join(",") }, [], `${slot} added.`);
      }}
    >
      <div className="grid grid-cols-2 gap-3">
        {qemu && (
          <Field label="Model">
            <Select value={model} onChange={(e) => setModel(e.target.value)}>
              <option value="virtio">VirtIO (paravirtualized)</option>
              <option value="e1000">Intel E1000</option>
              <option value="e1000e">Intel E1000E</option>
              <option value="rtl8139">Realtek RTL8139</option>
              <option value="vmxnet3">VMware vmxnet3</option>
            </Select>
          </Field>
        )}
        <Field label="Bridge">
          <Select value={bridge} onChange={(e) => setBridge(e.target.value)} required>
            {!c && <option value="">Loading…</option>}
            {c?.bridges.map((b) => (
              <option key={b} value={b}>
                {b}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="VLAN tag">
          <Input type="number" min={1} max={4094} value={tag} onChange={(e) => setTag(e.target.value)} placeholder="none" />
        </Field>
        <Field label="MAC address">
          <Input value={mac} onChange={(e) => setMac(e.target.value)} placeholder="auto" pattern="([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}" />
        </Field>
        {!qemu && (
          <>
            <Field label="IPv4" hint="dhcp, manual or an address like 10.0.0.5/24">
              <Input value={ip} onChange={(e) => setIp(e.target.value)} required />
            </Field>
            <Field label="Gateway">
              <Input value={gw} onChange={(e) => setGw(e.target.value)} />
            </Field>
          </>
        )}
      </div>
      <Check label="Proxmox firewall on this interface" v={fw} set={setFw} />
    </FormDialog>
  );
}

function CdromDialog({ cfg, slot, c, onClose, apply }: { cfg: Cfg; slot?: string; c: PVEChoices | null; onClose: () => void; apply: Apply }) {
  const target = slot ?? (nextSlot(cfg, "ide", 3, 2) || nextSlot(cfg, "sata", 5));
  const cur = slot ? String(cfg[slot]).split(",")[0] : "none";
  const [iso, setIso] = useState(cur === "cdrom" ? "cdrom" : cur);
  return (
    <FormDialog
      title={slot ? `CD/DVD ${slot}` : "Add CD/DVD drive"}
      description={target ? undefined : "No free IDE or SATA slot."}
      submit="Save"
      onClose={onClose}
      onSubmit={() => apply({ [target]: `${iso},media=cdrom` }, [], `${target} set.`)}
    >
      <Field label="Media" hint="Upload ISOs on the Storage page.">
        <Select value={iso} onChange={(e) => setIso(e.target.value)}>
          <option value="none">Empty drive</option>
          <option value="cdrom">Host's physical drive</option>
          {c?.isos.map((i) => (
            <option key={i} value={i}>
              {i}
            </option>
          ))}
        </Select>
      </Field>
    </FormDialog>
  );
}

function BootDialog({ cfg, onClose, apply }: { cfg: Cfg; onClose: () => void; apply: Apply }) {
  const devices = Object.keys(cfg)
    .filter((k) => qemuDisk.test(k) || /^net\d+$/.test(k) || /^(hostpci|usb)\d+$/.test(k))
    .sort((a, b) => a.localeCompare(b, undefined, { numeric: true }));
  const current = /order=([^,]+)/.exec(String(cfg.boot ?? ""))?.[1]?.split(";") ?? [];
  const [order, setOrder] = useState<string[]>(current.filter((d) => devices.includes(d)));
  const rest = devices.filter((d) => !order.includes(d));
  const move = (i: number, d: number) =>
    setOrder((o) => {
      const n = [...o];
      [n[i], n[i + d]] = [n[i + d], n[i]];
      return n;
    });
  return (
    <FormDialog title="Boot order" submit="Save" onClose={onClose} onSubmit={() => apply({ boot: `order=${order.join(";")}` }, [], "Boot order saved.")}>
      <ol className="grid gap-1 text-sm">
        {order.map((d, i) => (
          <li key={d} className="flex items-center gap-2 rounded-md border px-2 py-1">
            <span className="w-5 text-muted-foreground">{i + 1}.</span>
            <span className="flex-1 font-mono">{d}</span>
            <span className="truncate text-xs text-muted-foreground">{String(cfg[d]).slice(0, 40)}</span>
            <Button type="button" size="sm" variant="ghost" disabled={i === 0} onClick={() => move(i, -1)}>
              ↑
            </Button>
            <Button type="button" size="sm" variant="ghost" disabled={i === order.length - 1} onClick={() => move(i, 1)}>
              ↓
            </Button>
            <Button type="button" size="sm" variant="ghost" onClick={() => setOrder((o) => o.filter((x) => x !== d))}>
              ✕
            </Button>
          </li>
        ))}
        {order.length === 0 && <li className="text-muted-foreground">Nothing boots. Add a device below.</li>}
      </ol>
      {rest.length > 0 && (
        <div className="flex flex-wrap gap-1.5">
          {rest.map((d) => (
            <Button key={d} type="button" size="sm" variant="secondary" onClick={() => setOrder((o) => [...o, d])}>
              <Plus /> {d}
            </Button>
          ))}
        </div>
      )}
    </FormDialog>
  );
}

function RawDialog({ cfg, fixedKey, onClose, apply }: { cfg: Cfg; fixedKey?: string; onClose: () => void; apply: Apply }) {
  const [key, setKey] = useState(fixedKey ?? "");
  const [value, setValue] = useState(fixedKey ? String(cfg[fixedKey] ?? "") : "");
  return (
    <FormDialog
      title={fixedKey ? `Edit ${fixedKey}` : "Set a configuration option"}
      description={
        <>
          The value goes to Proxmox as it is, in Proxmox's own format (see <span className="font-mono">qm.conf</span> /{" "}
          <span className="font-mono">pct.conf</span>).
        </>
      }
      submit="Save"
      onClose={onClose}
      onSubmit={() => apply({ [key.trim()]: value.trim() }, [], `${key} saved.`)}
    >
      {!fixedKey && (
        <Field label="Key" hint="For example onboot, protection, tags, startup, agent, vga, hostpci0, usb0.">
          <Input value={key} onChange={(e) => setKey(e.target.value)} required pattern="[a-z][a-z0-9_-]*" />
        </Field>
      )}
      <Field label="Value">
        <Input className="font-mono" value={value} onChange={(e) => setValue(e.target.value)} required />
      </Field>
    </FormDialog>
  );
}

function ResizeDialog({ disk, current, onClose, run }: { disk: string; current: string; onClose: () => void; run: (size: string) => Promise<void> }) {
  const [add, setAdd] = useState("10");
  return (
    <FormDialog title={`Grow ${disk}`} description={`Now ${current || "unknown"}. Disks only grow; grow the partition inside the guest afterwards.`} submit="Grow" onClose={onClose} onSubmit={() => run(`+${add}G`)}>
      <Field label="Add (GiB)">
        <Input type="number" min={1} value={add} onChange={(e) => setAdd(e.target.value)} required />
      </Field>
    </FormDialog>
  );
}

function MoveDiskDialog({
  disk,
  c,
  content,
  onClose,
  run,
}: {
  disk: string;
  c: PVEChoices | null;
  content: string;
  onClose: () => void;
  run: (storage: string, del: boolean) => Promise<void>;
}) {
  const [storage, setStorage] = useState("");
  const [del, setDel] = useState(true);
  return (
    <FormDialog title={`Move ${disk}`} submit="Move" onClose={onClose} onSubmit={() => run(storage, del)}>
      <Field label="Target storage">
        <StorageSelect c={c} content={content} value={storage} onChange={setStorage} />
      </Field>
      <Check label="Delete the source after copying" v={del} set={setDel} />
    </FormDialog>
  );
}

// ---- guest actions ----

export function CloneDialog({ cluster, guest, name, onClose, onDone }: { cluster: string; guest: PVEGuestRef; name: string; onClose: () => void; onDone: (m: string) => void }) {
  const { c } = useChoices(cluster, guest.node);
  const [newid, setNewid] = useState("");
  const [nm, setNm] = useState(`${name}-clone`.replace(/[^A-Za-z0-9.-]/g, "-").slice(0, 60));
  const [target, setTarget] = useState(guest.node);
  const [full, setFull] = useState(true);
  const [storage, setStorage] = useState("");
  useEffect(() => {
    if (c?.nextid && !newid) setNewid(String(c.nextid));
  }, [c, newid]);
  return (
    <FormDialog
      title={`Clone ${name}`}
      description="A full clone copies the disks; a linked clone (templates only) shares them with the template."
      submit="Clone"
      onClose={onClose}
      onSubmit={async () => {
        const r = await pve.clone(cluster, guest, { newid: Number(newid), name: nm, target, full, storage: full ? storage : "" });
        onDone(ranMsg(r, "Cloned.", `Cloning to ${newid}; follow it under Tasks.`));
      }}
    >
      <div className="grid grid-cols-2 gap-3">
        <Field label="New VMID">
          <Input type="number" min={100} value={newid} onChange={(e) => setNewid(e.target.value)} required />
        </Field>
        <Field label="Name">
          <Input value={nm} onChange={(e) => setNm(e.target.value)} required pattern="[A-Za-z0-9][A-Za-z0-9.\-]*" />
        </Field>
        <Field label="Target node">
          <Select value={target} onChange={(e) => setTarget(e.target.value)}>
            {(c?.nodes.length ? c.nodes : [guest.node]).map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Mode">
          <Select value={full ? "full" : "linked"} onChange={(e) => setFull(e.target.value === "full")}>
            <option value="full">Full clone</option>
            <option value="linked">Linked clone</option>
          </Select>
        </Field>
      </div>
      {full && (
        <Field label="Storage" hint="Empty keeps each disk on its current storage.">
          <Select value={storage} onChange={(e) => setStorage(e.target.value)}>
            <option value="">Same as source</option>
            {storagesFor(c, guest.type === "qemu" ? "images" : "rootdir").map((s) => (
              <option key={s.storage} value={s.storage}>
                {s.storage}
              </option>
            ))}
          </Select>
        </Field>
      )}
    </FormDialog>
  );
}

export function MigrateDialog({
  cluster,
  guest,
  name,
  running,
  onClose,
  onDone,
}: {
  cluster: string;
  guest: PVEGuestRef;
  name: string;
  running: boolean;
  onClose: () => void;
  onDone: (m: string) => void;
}) {
  const { c } = useChoices(cluster, guest.node);
  const others = (c?.nodes ?? []).filter((n) => n !== guest.node);
  const [target, setTarget] = useState("");
  const [online, setOnline] = useState(running);
  const [local, setLocal] = useState(false);
  useEffect(() => {
    if (!target && others.length) setTarget(others[0]);
  }, [others, target]);
  return (
    <FormDialog
      title={`Migrate ${name}`}
      description={c && others.length === 0 ? "There is no other online node to migrate to." : undefined}
      submit="Migrate"
      onClose={onClose}
      onSubmit={async () => {
        const r = await pve.migrate(cluster, guest, { target, online, with_local_disks: local });
        onDone(ranMsg(r, "Migrated.", `Migrating to ${target}; follow it under Tasks.`));
      }}
    >
      <Field label="Target node">
        <Select value={target} onChange={(e) => setTarget(e.target.value)} required>
          {others.map((n) => (
            <option key={n} value={n}>
              {n}
            </option>
          ))}
        </Select>
      </Field>
      {running && <Check label={guest.type === "qemu" ? "Live migration (keeps it running)" : "Restart mode (stop, move, start)"} v={online} set={setOnline} />}
      {guest.type === "qemu" && <Check label="Copy local disks too" v={local} set={setLocal} />}
    </FormDialog>
  );
}

// ---- create ----

export function CreateGuestDialog({
  cluster,
  nodes,
  type,
  onClose,
}: {
  cluster: string;
  nodes: string[];
  type: "qemu" | "lxc";
  onClose: () => void;
}) {
  const [, nav] = useLocation();
  const [node, setNode] = useState(nodes[0] ?? "");
  const { c, err } = useChoices(cluster, node);
  const qemu = type === "qemu";
  const [f, setF] = useState({
    vmid: "",
    name: "",
    iso: "",
    ostype: "l26",
    bios: "seabios",
    cores: "2",
    sockets: "1",
    memory: "2048",
    storage: "",
    size: qemu ? "32" : "8",
    bridge: "",
    tag: "",
    ip: "dhcp",
    gw: "",
    template: "",
    password: "",
    sshkey: "",
    swap: "512",
    nesting: true,
    agent: true,
    start: false,
  });
  const up = (k: keyof typeof f) => (v: string | boolean) => setF((x) => ({ ...x, [k]: v }));
  useEffect(() => {
    if (!c) return;
    setF((x) => ({
      ...x,
      vmid: x.vmid || String(c.nextid ?? ""),
      bridge: x.bridge || c.bridges[0] || "",
    }));
  }, [c]);
  const diskContent = qemu ? "images" : "rootdir";
  const nodeList = useMemo(() => (nodes.length ? nodes : [node]), [nodes, node]);

  return (
    <FormDialog
      wide
      title={qemu ? "Create virtual machine" : "Create container"}
      description={qemu ? "Boots from an ISO; install the OS in the console." : "From a container template; download templates on the Storage page."}
      submit="Create"
      onClose={onClose}
      onSubmit={async () => {
        const p: Record<string, string> = { vmid: f.vmid, cores: f.cores, memory: f.memory };
        const net = [qemu ? "virtio" : "name=eth0", `bridge=${f.bridge}`, "firewall=1"];
        if (f.tag) net.push(`tag=${f.tag}`);
        if (qemu) {
          Object.assign(p, {
            name: f.name,
            ostype: f.ostype,
            sockets: f.sockets,
            scsihw: "virtio-scsi-single",
            scsi0: `${f.storage}:${f.size},discard=on,iothread=1,ssd=1`,
            ide2: `${f.iso || "none"},media=cdrom`,
            boot: "order=scsi0;ide2;net0",
            cpu: "x86-64-v2-AES",
            net0: net.join(","),
            bios: f.bios,
          });
          if (f.agent) p.agent = "1";
          if (f.bios === "ovmf") {
            p.machine = "q35";
            p.efidisk0 = `${f.storage}:1,efitype=4m,pre-enrolled-keys=1`;
          }
          if (f.ostype.startsWith("win")) p.tpmstate0 = `${f.storage}:1,version=v2.0`;
        } else {
          net.push(`ip=${f.ip}`);
          if (f.gw && f.ip !== "dhcp") net.push(`gw=${f.gw}`);
          Object.assign(p, {
            hostname: f.name,
            ostemplate: f.template,
            rootfs: `${f.storage}:${f.size}`,
            swap: f.swap,
            net0: net.join(","),
            unprivileged: "1",
          });
          if (f.nesting) p.features = "nesting=1";
          if (f.password) p.password = f.password;
          if (f.sshkey.trim()) p["ssh-public-keys"] = f.sshkey.trim();
        }
        if (f.start) p.start = "1";
        const r = await pve.create(cluster, node, type, p);
        nav(`/proxmox/${cluster}/${encodeURIComponent(r.node)}/${r.type}/${r.vmid}`);
      }}
    >
      {err && <p className="text-sm text-destructive">{err}</p>}
      <div className="grid gap-3 sm:grid-cols-3">
        <Field label="Node">
          <Select value={node} onChange={(e) => setNode(e.target.value)}>
            {nodeList.map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="VMID">
          <Input type="number" min={100} value={f.vmid} onChange={(e) => up("vmid")(e.target.value)} required />
        </Field>
        <Field label={qemu ? "Name" : "Hostname"}>
          <Input value={f.name} onChange={(e) => up("name")(e.target.value)} required pattern="[A-Za-z0-9][A-Za-z0-9.\-]*" />
        </Field>
      </div>
      {qemu ? (
        <div className="grid gap-3 sm:grid-cols-3">
          <Field label="ISO image">
            <Select value={f.iso} onChange={(e) => up("iso")(e.target.value)}>
              <option value="">No media</option>
              {c?.isos.map((i) => (
                <option key={i} value={i}>
                  {i.split("/").pop()}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Guest OS">
            <Select value={f.ostype} onChange={(e) => up("ostype")(e.target.value)}>
              <option value="l26">Linux 2.6 – 6.x</option>
              <option value="win11">Windows 11 / Server 2022+</option>
              <option value="win10">Windows 10 / Server 2016–2019</option>
              <option value="other">Other</option>
            </Select>
          </Field>
          <Field label="Firmware">
            <Select value={f.bios} onChange={(e) => up("bios")(e.target.value)}>
              <option value="seabios">BIOS (SeaBIOS)</option>
              <option value="ovmf">UEFI (OVMF)</option>
            </Select>
          </Field>
        </div>
      ) : (
        <Field label="Template">
          <Select value={f.template} onChange={(e) => up("template")(e.target.value)} required>
            <option value="">{c ? (c.templates.length ? "Pick a template…" : "No templates downloaded yet") : "Loading…"}</option>
            {c?.templates.map((t) => (
              <option key={t} value={t}>
                {t.split("/").pop()}
              </option>
            ))}
          </Select>
        </Field>
      )}
      <div className="grid gap-3 sm:grid-cols-4">
        <Field label="Cores">
          <Input type="number" min={1} value={f.cores} onChange={(e) => up("cores")(e.target.value)} required />
        </Field>
        {qemu ? (
          <Field label="Sockets">
            <Input type="number" min={1} value={f.sockets} onChange={(e) => up("sockets")(e.target.value)} required />
          </Field>
        ) : (
          <Field label="Swap (MiB)">
            <Input type="number" min={0} value={f.swap} onChange={(e) => up("swap")(e.target.value)} required />
          </Field>
        )}
        <Field label="Memory (MiB)">
          <Input type="number" min={64} value={f.memory} onChange={(e) => up("memory")(e.target.value)} required />
        </Field>
        <Field label="Disk (GiB)">
          <Input type="number" min={1} value={f.size} onChange={(e) => up("size")(e.target.value)} required />
        </Field>
      </div>
      <div className="grid gap-3 sm:grid-cols-3">
        <Field label="Disk storage">
          <StorageSelect c={c} content={diskContent} value={f.storage} onChange={(v) => up("storage")(v)} />
        </Field>
        <Field label="Bridge">
          <Select value={f.bridge} onChange={(e) => up("bridge")(e.target.value)} required>
            {c?.bridges.map((b) => (
              <option key={b} value={b}>
                {b}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="VLAN tag">
          <Input type="number" min={1} max={4094} value={f.tag} onChange={(e) => up("tag")(e.target.value)} placeholder="none" />
        </Field>
      </div>
      {!qemu && (
        <>
          <div className="grid gap-3 sm:grid-cols-3">
            <Field label="IPv4" hint="dhcp or 10.0.0.5/24">
              <Input value={f.ip} onChange={(e) => up("ip")(e.target.value)} required />
            </Field>
            <Field label="Gateway">
              <Input value={f.gw} onChange={(e) => up("gw")(e.target.value)} />
            </Field>
            <Field label="Root password">
              <Input type="password" autoComplete="new-password" minLength={5} value={f.password} onChange={(e) => up("password")(e.target.value)} />
            </Field>
          </div>
          <Field label="SSH public key">
            <Textarea rows={2} value={f.sshkey} onChange={(e) => up("sshkey")(e.target.value)} placeholder="ssh-ed25519 AAAA…" />
          </Field>
        </>
      )}
      <div className="flex flex-wrap gap-4">
        {qemu ? <Check label="QEMU guest agent" v={f.agent} set={up("agent")} /> : <Check label="Nesting (Docker inside)" v={f.nesting} set={up("nesting")} />}
        <Check label="Start after creating" v={f.start} set={up("start")} />
      </div>
    </FormDialog>
  );
}

