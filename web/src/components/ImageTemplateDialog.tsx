import { useEffect, useMemo, useState } from "react";
import { Download } from "lucide-react";
import { api, type PVEImage, type PVETemplateTargets } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input, Label } from "@/components/ui/input";
import { NumberInput } from "@/components/ui/number-input";
import { Select } from "@/components/ui/select";

// ImageTemplateDialog turns an official cloud image into a VM template:
// Proxmox downloads it (checksum-verified) and servermgr builds the
// template. The build shows under Provisioning.
export function ImageTemplateDialog({ cluster, onClose, onStarted }: { cluster: string; onClose: () => void; onStarted: () => void }) {
  const [images, setImages] = useState<PVEImage[]>([]);
  const [image, setImage] = useState("");
  const [node, setNode] = useState("");
  const [t, setT] = useState<PVETemplateTargets | null>(null);
  const [form, setForm] = useState({ name: "", image_storage: "", disk_storage: "", bridge: "", cores: 2, memory_mb: 2048, disk_gb: 20 });
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
        setForm((f) => ({
          ...f,
          image_storage: x.image_storages.includes(f.image_storage) ? f.image_storage : (x.image_storages[0] ?? ""),
          disk_storage: x.disk_storages.includes(f.disk_storage) ? f.disk_storage : (x.disk_storages[0] ?? ""),
          bridge: x.bridges.includes(f.bridge) ? f.bridge : (x.bridges[0] ?? ""),
        }));
      })
      .catch((e) => setErr((e as Error).message));
  }, [cluster, node]);

  const vendors = useMemo(() => [...new Set(images.map((i) => i.vendor))], [images]);
  const picked = images.find((i) => i.id === image);
  const set = (k: keyof typeof form, v: string | number) => setForm((f) => ({ ...f, [k]: v }));

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>New template from a cloud image</DialogTitle>
          <DialogDescription>
            Proxmox downloads the vendor's current image and checks it against the vendor's checksum. servermgr then builds a cloud-init template from it.
          </DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={async (e) => {
            e.preventDefault();
            setBusy(true);
            setErr("");
            try {
              await api.pveTemplateCreate(cluster, { image, node, ...form });
              onStarted();
            } catch (e) {
              setErr((e as Error).message);
            } finally {
              setBusy(false);
            }
          }}
        >
          <div className="grid max-h-64 gap-3 overflow-y-auto pr-1">
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
                        onClick={() => {
                          setImage(i.id);
                          set("name", `${i.id.replace(/\./g, "-")}-template`);
                        }}
                        className={`rounded-md border px-3 py-2 text-left text-sm transition-colors ${
                          image === i.id ? "border-brand bg-brand/10" : "hover:bg-accent"
                        }`}
                        aria-pressed={image === i.id}
                      >
                        {i.name}
                      </button>
                    ))}
                </div>
              </div>
            ))}
          </div>
          {picked && (
            <p className="truncate text-xs text-muted-foreground" title={picked.url}>
              {picked.url} · default user {picked.user}
            </p>
          )}
          <div className="grid gap-3 sm:grid-cols-3">
            <div className="grid gap-1.5">
              <Label htmlFor="it-name">Template name</Label>
              <Input id="it-name" value={form.name} onChange={(e) => set("name", e.target.value)} pattern="[a-z0-9]([a-z0-9\-]{0,61}[a-z0-9])?" required />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="it-node">Node</Label>
              <Select id="it-node" value={node} onChange={(e) => setNode(e.target.value)}>
                {t?.nodes.map((n) => (
                  <option key={n}>{n}</option>
                ))}
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="it-bridge">Network</Label>
              <Select id="it-bridge" value={form.bridge} onChange={(e) => set("bridge", e.target.value)}>
                {t?.bridges.map((b) => (
                  <option key={b}>{b}</option>
                ))}
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="it-istore">Download to</Label>
              <Select id="it-istore" value={form.image_storage} onChange={(e) => set("image_storage", e.target.value)}>
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
            <div className="grid gap-1.5 sm:col-span-2">
              <Label htmlFor="it-dstore">Disk storage</Label>
              <Select id="it-dstore" value={form.disk_storage} onChange={(e) => set("disk_storage", e.target.value)}>
                {t?.disk_storages.map((s) => (
                  <option key={s}>{s}</option>
                ))}
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="it-cores">vCPU</Label>
              <NumberInput id="it-cores" min={1} max={128} value={form.cores} onValue={(n) => set("cores", n)} />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="it-mem">Memory (MB)</Label>
              <NumberInput id="it-mem" min={256} value={form.memory_mb} onValue={(n) => set("memory_mb", n)} />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="it-disk">Disk (GB)</Label>
              <NumberInput id="it-disk" min={0} value={form.disk_gb} onValue={(n) => set("disk_gb", n)} />
            </div>
          </div>
          {t && t.image_storages.length === 0 && (
            <p className="text-sm text-warning">
              No storage on {node} accepts downloaded images yet. Enable the Import content type on a directory storage in Proxmox (Datacenter, Storage).
            </p>
          )}
          {err && <p className="text-sm text-destructive">{err}</p>}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" variant="brand" disabled={busy || !image || !form.image_storage || !form.disk_storage}>
              <Download /> {busy ? "Starting…" : "Build template"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
