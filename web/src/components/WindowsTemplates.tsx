import { useEffect, useMemo, useState } from "react";
import { Check as CheckIcon, Copy, Download, Loader2, Lock, LockOpen } from "lucide-react";
import { pve, type WinProfile } from "@/lib/pveapi";
import { api } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { NumberInput } from "@/components/ui/number-input";
import { Select } from "@/components/ui/select";
import { VLANInput, vlanNum } from "@/components/VLANField";
import { AppPicker, useWinApps } from "@/components/WindowsManage";
import { Field, FormDialog, StorageSelect, downloadVirtio, storagesFor, useChoices } from "@/components/PVEForms";

// Windows cloud-init templates. A build installs Windows unattended from
// the admin's ISO and runs servermgr's prepare script, which tunes Windows
// for Proxmox, applies a profile, installs Cloudbase-Init and runs Sysprep;
// servermgr then makes the VM a template. An existing Windows VM can run
// the same script from a CD and be made a template afterwards.

export const winProfiles: { id: WinProfile; name: string; icon: typeof Lock; about: string }[] = [
  {
    id: "standard",
    name: "Standard",
    icon: LockOpen,
    about: "Windows defaults with Proxmox tuning, Remote Desktop (NLA) and ping allowed.",
  },
  {
    id: "hardened",
    name: "Hardened",
    icon: Lock,
    about:
      "Locked down: inbound firewall closed except Remote Desktop (NLA, TLS only), TLS 1.2+, SMB signing, NTLMv2 only, LSA protection, auditing, account lockout, Defender attack-surface rules, automatic updates, unused services off.",
  },
];

export const winTuning =
  "Both get the VirtIO drivers and guest agent, the High performance power plan, TRIM, no hibernation or device encryption, and Cloudbase-Init for the Administrator password, hostname and network.";

// Microsoft's generic Pro setup key: it installs Pro from the multi-edition
// ISO without activating it. The admin's own key activates Windows.
const GENERIC_PRO_KEY = "VK7JG-NPHTM-C97JM-9MPGT-3V66T";

// downloadISO has Proxmox download an ISO into storage and waits for it
// (up to two hours: Windows ISOs are large).
async function downloadISO(cluster: string, node: string, storage: string, url: string, filename: string) {
  const r = await api.pveDownloadURL(cluster, node, storage, { url, content: "iso", filename });
  await waitDownload(cluster, node, r.upid);
}

async function waitDownload(cluster: string, node: string, upid: string) {
  for (let i = 0; i < 2400; i++) {
    await new Promise((res) => setTimeout(res, 3000));
    const t = await api.pveNodeTask(cluster, node, upid);
    if (t.status === "stopped") {
      if (t.exitstatus !== "OK") throw new Error(`Download failed: ${t.exitstatus}`);
      return;
    }
  }
  throw new Error("The download is still running; reload in a while.");
}

// isoImage is what an ISO implies for the image: evaluation ISOs hold one
// edition and take no key; Microsoft's multi-edition Windows 10/11 ISO
// (Win11_25H2_Hungarian_x64.iso) has Pro at 6.
function isoImage(edition: WinEditionCard, iso: string): { index?: number; key?: string } {
  const file = iso.split("/").pop() ?? iso;
  if (/eval/i.test(file)) return { index: edition.index, key: "" };
  if (edition.client && /^Win1\d_/i.test(file)) return { index: 6 };
  return {};
}

export type WinEditionCard = { id: string; name: string; iso: RegExp; index: number; client: boolean; disk: number };

export function WindowsBuildDialog({
  cluster,
  edition,
  nodes,
  node: node0,
  onClose,
  onStarted,
}: {
  cluster: string;
  edition: WinEditionCard;
  nodes: string[];
  node: string;
  onClose: () => void;
  onStarted: () => void;
}) {
  const [node, setNode] = useState(node0);
  const [reload, setReload] = useState(0);
  const { c, err } = useChoices(cluster, node, reload);
  const [dl, setDl] = useState("");
  const [winDl, setWinDl] = useState("");
  const [winBusy, setWinBusy] = useState(false);
  // Windows 10/11 come as Microsoft's retail ISO (Pro, any language) or
  // the Enterprise evaluation; servers only as the evaluation.
  const [winSrc, setWinSrc] = useState<"retail" | "eval">(edition.client ? "retail" : "eval");
  const base = (i: string) => i.split("/").pop() ?? i;
  const [f, setF] = useState({
    iso: "",
    drivers: "",
    storage: "",
    bridge: "",
    vlan: "",
    prefix: edition.id,
    cores: 2,
    memory: 4096,
    disk: edition.disk,
    index: edition.index,
    key: "",
    locale: "en-US",
    keyboard: "en-US",
    timezone: "UTC",
  });
  const [profiles, setProfiles] = useState<WinProfile[]>(["standard", "hardened"]);
  const catalog = useWinApps();
  const [apps, setApps] = useState<string[]>([]);
  const up = <K extends keyof typeof f>(k: K) => (v: (typeof f)[K]) => setF((x) => ({ ...x, [k]: v }));
  useEffect(() => {
    if (!c) return;
    setF((x) => ({
      ...x,
      ...(x.iso ? {} : isoImage(edition, c.isos.find((i) => edition.iso.test(base(i)) && !/virtio/i.test(i)) || "")),
      iso: x.iso || c.isos.find((i) => edition.iso.test(base(i)) && !/virtio/i.test(i)) || "",
      drivers: x.drivers || c.isos.find((i) => /virtio-win/i.test(i)) || "",
      storage: x.storage || (storagesFor(c, "images")[0]?.storage ?? ""),
      bridge: x.bridge || c.bridges[0] || "",
    }));
  }, [c, edition]);
  const isoStorage = storagesFor(c, "iso")[0]?.storage ?? "";
  const pickISO = (iso: string) => setF((x) => ({ ...x, ...isoImage(edition, iso), iso }));
  const toggle = (p: WinProfile) => setProfiles((l) => (l.includes(p) ? l.filter((x) => x !== p) : [...l, p]));

  return (
    <FormDialog
      wide
      title={`Build ${edition.name} templates`}
      description={
        <>
          servermgr installs Windows unattended in a new VM, runs its prepare script and turns the VM into a cloud-init template. Each build takes
          30 to 60 minutes; follow it under Deployments or in the VM's console. The VM needs internet access to fetch Cloudbase-Init.
        </>
      }
      submit={profiles.length === 2 ? "Build both templates" : "Build template"}
      onClose={onClose}
      onSubmit={async () => {
        if (!profiles.length) throw new Error("Pick Standard, Hardened or both.");
        await pve.windowsBuild(cluster, {
          edition: edition.id,
          node,
          iso: f.iso,
          drivers_iso: f.drivers,
          disk_storage: f.storage,
          bridge: f.bridge,
          vlan: vlanNum(f.vlan),
          cores: f.cores,
          memory_mb: f.memory,
          disk_gb: f.disk,
          image_index: f.index,
          product_key: f.key.trim(),
          locale: f.locale.trim(),
          keyboard: f.keyboard.trim(),
          timezone: f.timezone.trim(),
          profiles,
          name_prefix: f.prefix.trim(),
          apps,
        });
        onStarted();
      }}
    >
      {err && <p className="text-sm text-destructive">{err}</p>}
      <div className="grid gap-2 sm:grid-cols-2">
        {winProfiles.map((p) => {
          const on = profiles.includes(p.id);
          return (
            <button
              type="button"
              key={p.id}
              onClick={() => toggle(p.id)}
              aria-pressed={on}
              className={`flex gap-3 rounded-lg border p-3 text-left text-sm transition ${on ? "border-brand bg-brand/5" : "opacity-70 hover:opacity-100"}`}
            >
              <span className={`mt-0.5 flex size-5 shrink-0 items-center justify-center rounded border ${on ? "border-brand bg-brand text-white" : ""}`}>
                {on && <CheckIcon className="size-3.5" />}
              </span>
              <span className="grid gap-1">
                <span className="flex items-center gap-1.5 font-medium">
                  <p.icon className="size-4" /> {p.name}
                  <span className="font-mono text-xs font-normal text-muted-foreground">
                    {f.prefix || edition.id}-{p.id}
                  </span>
                </span>
                <span className="text-xs text-muted-foreground">{p.about}</span>
              </span>
            </button>
          );
        })}
      </div>
      <p className="text-xs text-muted-foreground">{winTuning}</p>
      {catalog.length > 0 && (
        <div className="rounded-lg border px-3 py-2">
          <p className="text-sm font-medium">
            Applications to preinstall {apps.length > 0 && <span className="text-muted-foreground">({apps.length})</span>}
          </p>
          <p className="mt-1 mb-2 text-xs text-muted-foreground">
            Installed during the build from each vendor&apos;s own download, after Windows checks the publisher&apos;s signature. Every VM from the template has
            them.
          </p>
          <AppPicker apps={catalog} picked={apps} onChange={setApps} />
        </div>
      )}
      <div className="grid gap-3 sm:grid-cols-3">
        <Field label="Node">
          <Select value={node} onChange={(e) => setNode(e.target.value)}>
            {(nodes.length ? nodes : [node]).map((n) => (
              <option key={n}>{n}</option>
            ))}
          </Select>
        </Field>
        <Field label="Template name prefix">
          <Input value={f.prefix} onChange={(e) => up("prefix")(e.target.value.toLowerCase())} pattern="[a-z0-9]([a-z0-9\-]*[a-z0-9])?" required />
        </Field>
        <Field label="Network bridge · VLAN" hint="VLAN tag for the build VM and the templates' NIC; empty = untagged.">
          <div className="flex gap-2">
            <Select className="min-w-0 flex-1" value={f.bridge} onChange={(e) => up("bridge")(e.target.value)} required>
              {(c?.bridges ?? []).map((b) => (
                <option key={b}>{b}</option>
              ))}
            </Select>
            <div className="w-24 shrink-0">
              <VLANInput value={f.vlan} onChange={(v) => up("vlan")(v)} />
            </div>
          </div>
        </Field>
      </div>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field
          label="Windows installer ISO"
          hint={
            winDl ||
            (!f.iso && isoStorage
              ? winSrc === "retail"
                ? `No ISO yet? Download Microsoft's current ${edition.name} ISO in ${f.locale || "en-US"} (set under Hardware, edition and language). It installs Pro; activate with your key.`
                : `No ISO yet? Download the English evaluation ISO straight from Microsoft (${edition.client ? "90" : "180"} days; activate later with your own key).`
              : c && !c.isos.length
                ? "No ISOs on this node yet: upload one on the Storage page."
                : undefined)
          }
        >
          <div className="flex flex-wrap gap-2">
            <Select className={!f.iso && isoStorage && edition.client ? "basis-full" : "flex-1"} value={f.iso} onChange={(e) => pickISO(e.target.value)} required>
              <option value="">Pick the ISO…</option>
              {c?.isos
                .filter((i) => !/virtio-win/i.test(i))
                .map((i) => (
                  <option key={i} value={i}>
                    {base(i)}
                  </option>
                ))}
            </Select>
            {!f.iso && isoStorage && edition.client && (
              <Select className="w-auto flex-1" value={winSrc} onChange={(e) => setWinSrc(e.target.value as "retail" | "eval")} disabled={winBusy} aria-label="Which ISO">
                <option value="retail">Pro, from Microsoft</option>
                <option value="eval">Enterprise evaluation</option>
              </Select>
            )}
            {!f.iso && isoStorage && (
              <Button
                type="button"
                size="sm"
                variant="secondary"
                disabled={winBusy}
                title={`Download the ${edition.name} ${winSrc === "retail" ? "" : "evaluation "}ISO from Microsoft into ${isoStorage}`}
                onClick={async () => {
                  setWinDl("Looking up Microsoft's download…");
                  setWinBusy(true);
                  try {
                    if (winSrc === "retail") {
                      const r = await pve.windowsISO(cluster, node, { edition: edition.id, locale: f.locale.trim(), storage: isoStorage });
                      if (r.upid) {
                        setWinDl(`Downloading ${r.file} (about 5–7 GB)…`);
                        await waitDownload(cluster, node, r.upid);
                      }
                      const generic = !f.key.trim();
                      setF((x) => ({ ...x, iso: r.volid, index: r.index, key: generic ? GENERIC_PRO_KEY : x.key }));
                      setWinDl(generic ? "Ready. It installs Pro with Microsoft's generic setup key; enter your own key under Hardware, edition and language to activate." : "");
                      setReload((n) => n + 1);
                      return;
                    }
                    const e = (await api.pveISOs()).find((x) => x.windows === edition.id);
                    if (!e?.url || !e.file) throw new Error(e?.error || `No Microsoft download is known for ${edition.name}.`);
                    setWinDl(`Downloading ${e.file} (about 5–7 GB)…`);
                    await downloadISO(cluster, node, isoStorage, e.url, e.file);
                    setWinDl("");
                    setReload((n) => n + 1);
                  } catch (x) {
                    setWinDl((x as Error).message);
                  } finally {
                    setWinBusy(false);
                  }
                }}
              >
                {winBusy ? <Loader2 className="animate-spin" /> : <Download />} Download
              </Button>
            )}
          </div>
        </Field>
        <Field label="Drivers ISO (virtio-win)" hint={dl || undefined}>
          <div className="flex gap-2">
            <Select value={f.drivers} onChange={(e) => up("drivers")(e.target.value)} required>
              <option value="">Pick the drivers ISO…</option>
              {c?.isos.map((i) => (
                <option key={i} value={i}>
                  {base(i)}
                </option>
              ))}
            </Select>
            {!f.drivers && isoStorage && (
              <Button
                type="button"
                size="sm"
                variant="secondary"
                disabled={!!dl}
                onClick={async () => {
                  setDl("Downloading virtio-win.iso…");
                  try {
                    await downloadVirtio(cluster, node, isoStorage);
                    setDl("");
                  } catch (e) {
                    setDl((e as Error).message);
                  }
                  setReload((n) => n + 1);
                }}
              >
                {dl ? <Loader2 className="animate-spin" /> : <Download />} Download
              </Button>
            )}
          </div>
        </Field>
      </div>
      <Field label="Disk storage" hint="Holds the system disk, EFI vars, TPM state and the cloud-init drive.">
        <StorageSelect c={c} content="images" value={f.storage} onChange={up("storage")} />
      </Field>
      <details className="rounded-lg border px-3 py-2">
        <summary className="cursor-pointer text-sm font-medium select-none">Hardware, edition and language</summary>
        <div className="mt-3 grid gap-3">
          <div className="grid gap-3 sm:grid-cols-3">
            <Field label="vCPUs">
              <NumberInput min={1} max={64} value={f.cores} onValue={(n) => up("cores")(n)} />
            </Field>
            <Field label="Memory (MB)">
              <NumberInput min={2048} value={f.memory} onValue={(n) => up("memory")(n)} />
            </Field>
            <Field label="Disk (GB)" hint="Deploys can grow it; Windows extends itself.">
              <NumberInput min={32} value={f.disk} onValue={(n) => up("disk")(n)} />
            </Field>
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field
              label="Image index"
              hint={
                edition.client
                  ? "1 for the Enterprise evaluation ISO. Multi-edition ISOs differ (Pro is often 6)."
                  : "Evaluation ISOs: 1 Standard Core, 2 Standard (Desktop Experience), 3 Datacenter Core, 4 Datacenter."
              }
            >
              <NumberInput min={1} max={32} value={f.index} onValue={(n) => up("index")(n)} />
            </Field>
            <Field
              label="Product key"
              hint={edition.client ? "Optional: your own key, installed after Setup; the image index picks the edition. Evaluation ISOs take none." : "Leave empty for evaluation ISOs."}
            >
              <Input value={f.key} onChange={(e) => up("key")(e.target.value.toUpperCase())} placeholder="XXXXX-XXXXX-XXXXX-XXXXX-XXXXX" />
            </Field>
          </div>
          <div className="grid gap-3 sm:grid-cols-3">
            <Field label="Language" hint="Must be on the ISO.">
              <Input value={f.locale} onChange={(e) => up("locale")(e.target.value)} placeholder="en-US" />
            </Field>
            <Field label="Keyboard">
              <Input value={f.keyboard} onChange={(e) => up("keyboard")(e.target.value)} placeholder="hu-HU" />
            </Field>
            <Field label="Time zone">
              <Input value={f.timezone} onChange={(e) => up("timezone")(e.target.value)} placeholder="Central Europe Standard Time" />
            </Field>
          </div>
        </div>
      </details>
    </FormDialog>
  );
}

// WindowsTemplateDialog turns an existing Windows VM into a template.
export function WindowsTemplateDialog({
  cluster,
  node,
  vmid,
  name,
  stopped,
  onClose,
  onDone,
}: {
  cluster: string;
  node: string;
  vmid: number;
  name: string;
  stopped: boolean;
  onClose: () => void;
  onDone: () => void;
}) {
  const { c } = useChoices(cluster, node);
  const isoStores = useMemo(() => storagesFor(c, "iso"), [c]);
  const [storage, setStorage] = useState("");
  useEffect(() => setStorage((s) => s || isoStores[0]?.storage || ""), [isoStores]);
  const [profile, setProfile] = useState<WinProfile>("hardened");
  const [cd, setCd] = useState<{ slot: string; pending: boolean } | null>(null);
  const [busy, setBusy] = useState("");
  const [err, setErr] = useState("");
  const [confirm, setConfirm] = useState("");
  const [copied, setCopied] = useState(false);
  const pname = profile === "hardened" ? "Hardened" : "Standard";
  const cmd = `powershell -ExecutionPolicy Bypass -Command "& ((Get-Volume -FileSystemLabel SMPREP).DriveLetter + ':\\servermgr-prepare.ps1') -Profile ${pname}"`;
  const run = async (what: string, f: () => Promise<void>) => {
    setBusy(what);
    setErr("");
    try {
      await f();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy("");
    }
  };

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[92vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Make {name} a Windows template</DialogTitle>
          <DialogDescription>
            Run servermgr's prepare script in the VM: it installs the drivers and guest agent, tunes Windows for Proxmox, applies the profile, installs
            Cloudbase-Init (cloud-init for Windows) and runs Sysprep, which shuts the VM down. Then make it a template here.
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-4 text-sm">
          <div className="grid gap-2 sm:grid-cols-2">
            {winProfiles.map((p) => (
              <label key={p.id} className={`flex cursor-pointer items-start gap-2 rounded-lg border p-3 ${profile === p.id ? "border-brand bg-brand/5" : ""}`}>
                <input type="radio" name="winprofile" className="mt-1" checked={profile === p.id} onChange={() => setProfile(p.id)} />
                <span className="grid gap-1">
                  <span className="flex items-center gap-1.5 font-medium">
                    <p.icon className="size-4" /> {p.name}
                  </span>
                  <span className="text-xs text-muted-foreground">{p.about}</span>
                </span>
              </label>
            ))}
          </div>
          <section className="grid gap-2 rounded-lg border p-3">
            <h3 className="font-medium">1. Put the script in the VM</h3>
            <div className="flex flex-wrap items-end gap-2">
              <Field label="ISO storage">
                <Select value={storage} onChange={(e) => setStorage(e.target.value)} className="w-48">
                  {isoStores.map((s) => (
                    <option key={s.storage}>{s.storage}</option>
                  ))}
                </Select>
              </Field>
              <Button
                variant="secondary"
                disabled={!!busy || !storage}
                onClick={() =>
                  run("cd", async () => {
                    const r = await pve.windowsPrep(cluster, node, vmid, storage);
                    setCd({ slot: r.slot, pending: r.pending });
                  })
                }
              >
                {busy === "cd" ? <Loader2 className="animate-spin" /> : <Download />} Insert script CD
              </Button>
              <a href="/api/v1/windows/prepare.ps1" className="pb-2 text-xs text-brand hover:underline">
                or download the script
              </a>
            </div>
            {cd && (
              <p className="text-xs text-success">
                The CD is in drive {cd.slot}
                {cd.pending ? (stopped ? "; Windows sees it when the VM starts." : "; restart the VM so Windows sees the new drive.") : "."}
              </p>
            )}
            <p className="text-xs text-muted-foreground">Then, in the VM, run this in an administrator PowerShell (the console's Type button can type it):</p>
            <div className="flex items-start gap-2">
              <code className="min-w-0 flex-1 rounded bg-muted px-2 py-1.5 font-mono text-xs break-all">{cmd}</code>
              <Button
                size="sm"
                variant="ghost"
                title="Copy"
                onClick={() => {
                  void navigator.clipboard?.writeText(cmd).then(() => {
                    setCopied(true);
                    setTimeout(() => setCopied(false), 1500);
                  });
                }}
              >
                {copied ? <CheckIcon /> : <Copy />}
              </Button>
            </div>
          </section>
          <section className="grid gap-2 rounded-lg border p-3">
            <h3 className="flex items-center gap-2 font-medium">
              2. Make the template {stopped ? <Badge variant="success">VM is stopped</Badge> : <Badge variant="outline">waiting for Sysprep to shut it down</Badge>}
            </h3>
            <p className="text-xs text-muted-foreground">
              Removes the CD drives, adds a cloud-init drive and converts VM {vmid}. This can't be undone; deploy new VMs from it on the Deploy page.
            </p>
            <div className="flex flex-wrap items-end gap-2">
              <Field label={`Type ${vmid} to confirm`}>
                <Input value={confirm} onChange={(e) => setConfirm(e.target.value)} className="w-32" />
              </Field>
              <Button
                variant="brand"
                disabled={!!busy || !stopped || confirm !== String(vmid)}
                onClick={() =>
                  run("tpl", async () => {
                    await pve.windowsTemplate(cluster, node, vmid, confirm, profile);
                    onDone();
                    onClose();
                  })
                }
              >
                {busy === "tpl" && <Loader2 className="animate-spin" />} Make Windows template
              </Button>
            </div>
          </section>
          {err && <p className="text-destructive">{err}</p>}
        </div>
      </DialogContent>
    </Dialog>
  );
}

// WindowsAdminDialog resets a running Windows VM's Administrator through
// the guest agent: enables it, clears a lockout and sets a new password.
export function WindowsAdminDialog({
  cluster,
  node,
  vmid,
  name,
  onClose,
  onDone,
}: {
  cluster: string;
  node: string;
  vmid: number;
  name: string;
  onClose: () => void;
  onDone: () => void;
}) {
  const [pw, setPw] = useState("");
  const [again, setAgain] = useState("");
  return (
    <FormDialog
      title={`Reset Administrator on ${name}`}
      description="Enables the built-in Administrator, clears a lockout and sets this password, through the QEMU guest agent. The VM must be running with Windows booted."
      submit="Reset Administrator"
      onClose={onClose}
      onSubmit={async () => {
        if (pw !== again) throw new Error("The two passwords differ.");
        await pve.windowsAdmin(cluster, node, vmid, pw);
        onDone();
      }}
    >
      <Field label="New password" hint="At least 12 characters, with three of: lower case, upper case, digits, symbols.">
        <Input type="password" autoComplete="new-password" minLength={12} value={pw} onChange={(e) => setPw(e.target.value)} required />
      </Field>
      <Field label="Again">
        <Input type="password" autoComplete="new-password" value={again} onChange={(e) => setAgain(e.target.value)} required />
      </Field>
    </FormDialog>
  );
}
