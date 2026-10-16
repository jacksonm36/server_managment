import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useLocation } from "wouter";
import { api } from "@/lib/api";
import { useAccess } from "@/lib/access";
import { pve, type PVEChoices } from "@/lib/pveapi";
import { formatBytes } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input, Label } from "@/components/ui/input";
import { Select, Textarea } from "@/components/ui/select";

// Shared form pieces for the Proxmox admin screens, and the dialog that
// creates a VM from an ISO or a container from a template.

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
export function useChoices(cluster: string, node: string, reload = 0) {
  const [c, setC] = useState<PVEChoices | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => {
    if (!node) return;
    setC(null);
    pve
      .choices(cluster, node)
      .then(setC)
      .catch((e) => setErr(errText(e)));
  }, [cluster, node, reload]);
  return { c, err };
}

export const storagesFor = (c: PVEChoices | null, content: string) => (c?.storages ?? []).filter((s) => s.content.split(",").includes(content));

export function StorageSelect({ c, content, value, onChange }: { c: PVEChoices | null; content: string; value: string; onChange: (v: string) => void }) {
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

export function Check({ label, v, set }: { label: string; v: boolean; set: (b: boolean) => void }) {
  return (
    <label className="flex cursor-pointer items-center gap-2 text-sm">
      <input type="checkbox" checked={v} onChange={(e) => set(e.target.checked)} className="size-4 accent-[var(--color-brand)]" />
      {label}
    </label>
  );
}

// GuestPreset fills the create form, e.g. from a Windows edition or a
// container template picked in the Deploy catalog.
export type GuestPreset = {
  node?: string;
  title?: string;
  description?: string;
  name?: string;
  ostype?: string;
  bios?: string;
  cores?: string;
  memory?: string;
  size?: string;
  template?: string;
  iso?: RegExp; // pick the first ISO whose name matches
};

// The Windows paravirtual drivers (disk, network, balloon, guest agent),
// published by the Fedora project; Windows Setup needs them to see a
// VirtIO disk.
export const VIRTIO_WIN_URL = "https://fedorapeople.org/groups/virt/virtio-win/direct-downloads/stable-virtio/virtio-win.iso";

// downloadVirtio has the node download virtio-win.iso into storage and
// waits for Proxmox to finish.
export async function downloadVirtio(cluster: string, node: string, storage: string) {
  const r = await api.pveDownloadURL(cluster, node, storage, { url: VIRTIO_WIN_URL, content: "iso" });
  for (let i = 0; i < 300; i++) {
    await new Promise((res) => setTimeout(res, 3000));
    const t = await api.pveNodeTask(cluster, node, r.upid);
    if (t.status === "stopped") {
      if (t.exitstatus !== "OK") throw new Error(`Download failed: ${t.exitstatus}`);
      return;
    }
  }
  throw new Error("The download is still running; reload in a while.");
}

export function CreateGuestDialog({
  cluster,
  nodes,
  type,
  preset,
  onClose,
}: {
  cluster: string;
  nodes: string[];
  type: "qemu" | "lxc";
  preset?: GuestPreset;
  onClose: () => void;
}) {
  const [, nav] = useLocation();
  const [node, setNode] = useState(preset?.node || nodes[0] || "");
  const [reload, setReload] = useState(0);
  const { c, err } = useChoices(cluster, node, reload);
  const ac = useAccess();
  const [dl, setDl] = useState("");
  const qemu = type === "qemu";
  const [f, setF] = useState({
    vmid: "",
    name: preset?.name ?? "",
    iso: "",
    drivers: "",
    ostype: preset?.ostype ?? "l26",
    bios: preset?.bios ?? "seabios",
    cores: preset?.cores ?? "2",
    sockets: "1",
    memory: preset?.memory ?? "2048",
    storage: "",
    size: preset?.size ?? (qemu ? "32" : "8"),
    bridge: "",
    tag: "",
    ip: "dhcp",
    gw: "",
    template: preset?.template ?? "",
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
      iso: x.iso || (preset?.iso && c.isos.find((i) => preset.iso!.test(i.split("/").pop() ?? ""))) || "",
      drivers: x.drivers || c.isos.find((i) => /virtio-win/i.test(i)) || "",
      storage: x.storage || (storagesFor(c, qemu ? "images" : "rootdir")[0]?.storage ?? ""),
    }));
  }, [c, preset]);
  const win = qemu && f.ostype.startsWith("win");
  const isoStorage = storagesFor(c, "iso")[0]?.storage ?? "";
  // fetchDrivers has the node download virtio-win.iso, then reloads the
  // ISO list once Proxmox has it.
  const fetchDrivers = async () => {
    setDl("Downloading virtio-win.iso…");
    try {
      await downloadVirtio(cluster, node, isoStorage);
      setDl("");
    } catch (e) {
      setDl(errText(e));
    }
    setReload((n) => n + 1);
  };
  const diskContent = qemu ? "images" : "rootdir";
  const nodeList = useMemo(() => (nodes.length ? nodes : [node]), [nodes, node]);

  return (
    <FormDialog
      wide
      title={preset?.title ?? (qemu ? "Create virtual machine" : "Create container")}
      description={
        preset?.description ?? (qemu ? "Boots from an ISO; install the OS in the console." : "From a container template; download templates on the Deploy page.")
      }
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
            ...(win && f.drivers ? { ide0: `${f.drivers},media=cdrom` } : {}),
            cpu: "x86-64-v2-AES",
            net0: net.join(","),
            bios: f.bios,
          });
          if (f.agent) p.agent = "1";
          if (f.bios === "ovmf") {
            p.machine = "q35";
            p.efidisk0 = `${f.storage}:1,efitype=4m,pre-enrolled-keys=1`;
          }
          if (win) {
            p.tpmstate0 = `${f.storage}:1,version=v2.0`;
            p.vga = "std";
          }
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
      ) : null}
      {win && (
        <Field label="Drivers ISO (virtio-win)" hint="Windows Setup needs it to see the disk: Load driver, then vioscsi, your Windows version, amd64">
          <div className="flex gap-2">
            <Select value={f.drivers} onChange={(e) => up("drivers")(e.target.value)}>
              <option value="">None</option>
              {c?.isos.map((i) => (
                <option key={i} value={i}>
                  {i.split("/").pop()}
                </option>
              ))}
            </Select>
            {!f.drivers && ac.can("proxmox.manage") && isoStorage && (
              <Button type="button" size="sm" variant="secondary" disabled={!!dl && dl.endsWith("…")} onClick={fetchDrivers}>
                Download
              </Button>
            )}
          </div>
          {dl && <p className="text-xs text-muted-foreground">{dl}</p>}
        </Field>
      )}
      {!qemu && (
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

