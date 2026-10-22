import { useEffect, useMemo, useState } from "react";
import { VLANInput, vlanNum } from "@/components/VLANField";
import { Rocket } from "lucide-react";
import { api, type PVEGuest, type PVEImage, type PVETemplateTargets } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input, Label } from "@/components/ui/input";
import { NumberInput } from "@/components/ui/number-input";
import { Select } from "@/components/ui/select";
import { FlavorPicker, useFlavors } from "@/components/Flavors";
import { CloudInitFields, ciOpts, emptyCI, type CIForm } from "@/components/CloudInitFields";
import { DeployApps } from "@/components/WindowsManage";

// DeployDialog creates a VM from a catalog image in one step: servermgr
// reuses the image's template on the node, or builds it first, then
// clones it with cloud-init and (optionally) enrolls the VM.
export function DeployDialog({
  cluster,
  guests,
  preset = "",
  onClose,
  onStarted,
}: {
  cluster: string;
  guests: PVEGuest[];
  preset?: string; // image picked beforehand
  onClose: () => void;
  onStarted: () => void;
}) {
  const [images, setImages] = useState<PVEImage[]>([]);
  const [apps, setApps] = useState<string[]>([]);
  const flavors = useFlavors();
  const [image, setImage] = useState(preset);
  const [count, setCount] = useState(1);
  const [ci, setCI] = useState<CIForm>(emptyCI);
  const [node, setNode] = useState("");
  const [t, setT] = useState<PVETemplateTargets | null>(null);
  // undefined until picked: then the second flavor (usually "medium").
  const [flavorPick, setFlavor] = useState<number | null>();
  const [f, setF] = useState({ name: "", cores: 2, memory_mb: 2048, disk_gb: 20, image_storage: "", disk_storage: "", bridge: "", vlan: "", ipMode: "dhcp", ip: "", gateway: "", enroll: true });
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api
      .pveImages()
      .then(setImages)
      .catch((e) => setErr((e as Error).message));
  }, []);
  useEffect(() => {
    api
      .pveTemplateTargets(cluster, node)
      .then((x) => {
        setT(x);
        if (!node && x.nodes[0]) setNode(x.nodes[0]);
        setF((v) => ({
          ...v,
          image_storage: x.image_storages.includes(v.image_storage) ? v.image_storage : (x.image_storages[0] ?? ""),
          disk_storage: x.disk_storages.includes(v.disk_storage) ? v.disk_storage : (x.disk_storages[0] ?? ""),
          bridge: x.bridges.includes(v.bridge) ? v.bridge : (x.bridges[0] ?? ""),
        }));
      })
      .catch((e) => setErr((e as Error).message));
  }, [cluster, node]);

  const flavor = flavorPick === undefined ? ((flavors[1] ?? flavors[0])?.id ?? null) : flavorPick;
  const vendors = useMemo(() => [...new Set(images.map((i) => i.vendor))], [images]);
  // The template servermgr built from this image on this node, if any.
  const existing = guests.find((g) => g.template && g.type === "qemu" && g.node === node && (g.tags ?? "").split(";").includes(image));
  const picked = flavors.find((x) => x.id === flavor);

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[92vh] overflow-y-auto sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>Deploy a VM</DialogTitle>
          <DialogDescription>
            Pick an operating system and a size. servermgr reuses that image's template on the node, or downloads the image and builds the template first,
            then creates and starts the VM with cloud-init: the panel's SSH key, your keys and settings.
          </DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={async (e) => {
            e.preventDefault();
            setBusy(true);
            setErr("");
            try {
              await api.pveDeploy(cluster, {
                image,
                node,
                name: f.name.trim(),
                ...(picked ? { flavor_id: picked.id } : { cores: f.cores, memory_mb: f.memory_mb, disk_gb: f.disk_gb }),
                image_storage: f.image_storage,
                disk_storage: f.disk_storage,
                bridge: f.bridge,
                vlan: vlanNum(f.vlan),
                ip: f.ipMode === "dhcp" ? "dhcp" : f.ip.trim(),
                gateway: f.ipMode === "dhcp" ? "" : f.gateway.trim(),
                enroll: f.enroll,
                apps: f.enroll ? apps : [],
                count,
                ...ciOpts(ci),
              });
              onStarted();
            } catch (e) {
              setErr((e as Error).message);
            } finally {
              setBusy(false);
            }
          }}
        >
          <div className="grid max-h-56 gap-3 overflow-y-auto pr-1">
            {vendors.map((v) => (
              <div key={v}>
                <div className="mb-1 text-xs font-medium text-muted-foreground">{v}</div>
                <div className="grid gap-2 sm:grid-cols-3">
                  {images
                    .filter((i) => i.vendor === v)
                    .map((i) => (
                      <button
                        type="button"
                        key={i.id}
                        onClick={() => setImage(i.id)}
                        className={`rounded-md border px-3 py-2 text-left text-sm transition-colors ${image === i.id ? "border-brand bg-brand/10" : "hover:bg-accent"}`}
                        aria-pressed={image === i.id}
                      >
                        {i.name}
                      </button>
                    ))}
                </div>
              </div>
            ))}
          </div>

          <FlavorPicker flavors={flavors} picked={flavor} onPick={(x) => setFlavor(x ? x.id : null)} />
          {flavor === null && (
            <div className="grid grid-cols-3 gap-3">
              <div className="grid gap-1.5">
                <Label htmlFor="dp-cores">vCPUs</Label>
                <NumberInput id="dp-cores" min={1} max={128} value={f.cores} onValue={(n) => setF({ ...f, cores: n })} />
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="dp-mem">Memory (MB)</Label>
                <NumberInput id="dp-mem" min={256} value={f.memory_mb} onValue={(n) => setF({ ...f, memory_mb: n })} />
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="dp-disk">Disk (GB)</Label>
                <NumberInput id="dp-disk" min={0} value={f.disk_gb} onValue={(n) => setF({ ...f, disk_gb: n })} />
              </div>
            </div>
          )}

          <div className="grid gap-3 sm:grid-cols-3">
            <div className="grid gap-1.5">
              <Label htmlFor="dp-name">Hostname</Label>
              <Input id="dp-name" value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} placeholder="web-03" pattern="[a-z0-9]([a-z0-9\-]{0,61}[a-z0-9])?" required />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="dp-node">Node</Label>
              <Select id="dp-node" value={node} onChange={(e) => setNode(e.target.value)}>
                {t?.nodes.map((n) => (
                  <option key={n}>{n}</option>
                ))}
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="dp-bridge">Network</Label>
              <div className="flex gap-2">
                <Select id="dp-bridge" value={f.bridge} onChange={(e) => setF({ ...f, bridge: e.target.value })}>
                  {t?.bridges.map((b) => (
                    <option key={b}>{b}</option>
                  ))}
                </Select>
                <div className="w-28 shrink-0">
                  <VLANInput value={f.vlan} onChange={(v) => setF({ ...f, vlan: v })} />
                </div>
              </div>
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="dp-dstore">Disk storage</Label>
              <Select id="dp-dstore" value={f.disk_storage} onChange={(e) => setF({ ...f, disk_storage: e.target.value })}>
                {t?.disk_storages.map((s) => (
                  <option key={s}>{s}</option>
                ))}
              </Select>
            </div>
            {!existing && (
              <div className="grid gap-1.5 sm:col-span-2">
                <Label htmlFor="dp-istore">Download the image to</Label>
                <Select id="dp-istore" value={f.image_storage} onChange={(e) => setF({ ...f, image_storage: e.target.value })}>
                  {t?.image_storages.map((s) => (
                    <option key={s}>{s}</option>
                  ))}
                  {t?.file_storages.map((s) => (
                    <option key={s} value={s}>
                      {s} (needs Import content)
                    </option>
                  ))}
                </Select>
              </div>
            )}
          </div>
          {image && (
            <p className="text-xs text-muted-foreground">
              {existing
                ? `Reuses template ${existing.name} (${existing.vmid}) on ${node}.`
                : `No template from this image on ${node} yet: it is built first and kept for the next deploy.`}
            </p>
          )}

          <div className="grid gap-1.5">
            <Label>Address</Label>
            <div className="flex flex-wrap gap-3 text-sm">
              {["dhcp", "static"].map((m) => (
                <label key={m} className="flex items-center gap-1.5">
                  <input type="radio" name="dp-ipmode" className="accent-brand" checked={f.ipMode === m} onChange={() => setF({ ...f, ipMode: m })} />{" "}
                  {m === "dhcp" ? "DHCP" : "Static"}
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
          <details className="group rounded-lg border px-3 py-2" open={!!preset}>
            <summary className="cursor-pointer text-sm font-medium select-none">Cloud-init: user, SSH keys, DNS, tags</summary>
            <div className="mt-3">
              <CloudInitFields value={ci} onChange={setCI} defaultUser={images.find((i) => i.id === image)?.user} idPrefix="dp-ci" />
            </div>
          </details>
          <div className="grid gap-1.5 sm:w-48">
            <Label htmlFor="dp-count">How many</Label>
            <NumberInput id="dp-count" min={1} max={10} value={count} onValue={(n) => setCount(Math.max(1, Math.min(10, n || 1)))} />
            {count > 1 && <span className="text-xs text-muted-foreground">Named {f.name || "name"}-1 to -{count}; needs DHCP.</span>}
          </div>
          <label className="flex items-start gap-2 text-sm">
            <input type="checkbox" className="mt-1 accent-brand" checked={f.enroll} onChange={(e) => setF({ ...f, enroll: e.target.checked })} />
            <span>
              Enroll into servermgr
              <span className="block text-xs text-muted-foreground">Installs the agent, links the VM to its server and prepares it for Ansible.</span>
            </span>
          </label>
          <DeployApps os="linux" enroll={f.enroll} picked={apps} onChange={setApps} />
          {err && <p className="text-sm text-destructive">{err}</p>}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" variant="brand" disabled={busy || !image || !f.disk_storage || (!existing && !f.image_storage)}>
              <Rocket /> {busy ? "Starting…" : count > 1 ? `Deploy ${count} VMs` : "Deploy"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
