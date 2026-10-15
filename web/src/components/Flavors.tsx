import { useEffect, useState, type FormEvent } from "react";
import { Pencil, Plus, Trash2 } from "lucide-react";
import { api, type PVEFlavor } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input, Label } from "@/components/ui/input";

// Flavors are named VM sizes (vCPUs, memory, disk) offered when creating
// a VM.

export const flavorSpec = (f: { cores: number; memory_mb: number; disk_gb: number }) =>
  `${f.cores} vCPU · ${f.memory_mb >= 1024 ? `${+(f.memory_mb / 1024).toFixed(1)} GB` : `${f.memory_mb} MB`}${f.disk_gb ? ` · ${f.disk_gb} GB disk` : ""}`;

export function useFlavors() {
  const [flavors, setFlavors] = useState<PVEFlavor[]>([]);
  useEffect(() => {
    api
      .pveFlavors()
      .then(setFlavors)
      .catch(() => setFlavors([]));
  }, []);
  return flavors;
}

// FlavorPicker shows the flavors as cards plus "Custom"; picking one
// calls onPick with it (null for custom).
export function FlavorPicker({ flavors, picked, onPick }: { flavors: PVEFlavor[]; picked: number | null; onPick: (f: PVEFlavor | null) => void }) {
  if (flavors.length === 0) return null;
  const card = (active: boolean) => `rounded-md border px-3 py-2 text-left text-sm transition-colors ${active ? "border-brand bg-brand/10" : "hover:bg-accent"}`;
  return (
    <div className="grid gap-1.5">
      <Label>Size</Label>
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
        {flavors.map((f) => (
          <button type="button" key={f.id} className={card(picked === f.id)} aria-pressed={picked === f.id} onClick={() => onPick(f)} title={f.description}>
            <div className="font-medium">{f.name}</div>
            <div className="text-xs text-muted-foreground">{flavorSpec(f)}</div>
          </button>
        ))}
        <button type="button" className={card(picked === null)} aria-pressed={picked === null} onClick={() => onPick(null)}>
          <div className="font-medium">Custom</div>
          <div className="text-xs text-muted-foreground">Set the size yourself</div>
        </button>
      </div>
    </div>
  );
}

// FlavorsDialog lists, adds, edits and removes flavors (platform admins).
export function FlavorsDialog({ onClose }: { onClose: () => void }) {
  const [list, setList] = useState<PVEFlavor[] | null>(null);
  const [edit, setEdit] = useState<(Omit<PVEFlavor, "id"> & { id?: number }) | null>(null);
  const [err, setErr] = useState("");
  const load = () =>
    api
      .pveFlavors()
      .then(setList)
      .catch((e) => setErr((e as Error).message));
  useEffect(() => {
    void load();
  }, []);

  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (!edit) return;
    setErr("");
    try {
      await api.pveFlavorSave(edit);
      setEdit(null);
      void load();
    } catch (e) {
      setErr((e as Error).message);
    }
  };

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>Flavors</DialogTitle>
          <DialogDescription>VM sizes everyone picks from when creating a VM, tenants included. The disk size is a minimum; a bigger template disk is kept.</DialogDescription>
        </DialogHeader>
        <ul className="divide-y text-sm">
          {(list ?? []).map((f) => (
            <li key={f.id} className="flex items-center gap-2 py-2">
              <div className="min-w-0 flex-1">
                <div className="font-medium">{f.name}</div>
                <div className="text-xs text-muted-foreground">
                  {flavorSpec(f)}
                  {f.description && ` · ${f.description}`}
                </div>
              </div>
              <Button size="icon" variant="ghost" className="size-8" aria-label={`Edit ${f.name}`} onClick={() => setEdit({ ...f })}>
                <Pencil className="size-4" />
              </Button>
              <Button
                size="icon"
                variant="ghost"
                className="size-8 text-destructive"
                aria-label={`Delete ${f.name}`}
                onClick={() =>
                  confirm(`Delete the flavor ${f.name}? VMs made with it keep their size.`) &&
                  void api
                    .pveFlavorDelete(f.id)
                    .then(load)
                    .catch((e) => setErr((e as Error).message))
                }
              >
                <Trash2 className="size-4" />
              </Button>
            </li>
          ))}
        </ul>
        {edit ? (
          <form onSubmit={save} className="grid gap-3 rounded-lg border p-3">
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
              <div className="col-span-2 grid gap-1.5 sm:col-span-1">
                <Label htmlFor="fl-name">Name</Label>
                <Input id="fl-name" value={edit.name} onChange={(e) => setEdit({ ...edit, name: e.target.value })} pattern="[a-z0-9][a-z0-9._\-]{0,39}" required />
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="fl-cores">vCPUs</Label>
                <Input id="fl-cores" type="number" min={1} max={128} value={edit.cores} onChange={(e) => setEdit({ ...edit, cores: Number(e.target.value) })} />
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="fl-mem">Memory (MB)</Label>
                <Input id="fl-mem" type="number" min={256} step={256} value={edit.memory_mb} onChange={(e) => setEdit({ ...edit, memory_mb: Number(e.target.value) })} />
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="fl-disk">Disk (GB)</Label>
                <Input id="fl-disk" type="number" min={0} value={edit.disk_gb} onChange={(e) => setEdit({ ...edit, disk_gb: Number(e.target.value) })} />
              </div>
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="fl-desc">Description</Label>
              <Input id="fl-desc" value={edit.description} maxLength={200} onChange={(e) => setEdit({ ...edit, description: e.target.value })} />
            </div>
            <div className="flex justify-end gap-2">
              <Button type="button" variant="ghost" onClick={() => setEdit(null)}>
                Cancel
              </Button>
              <Button type="submit" variant="brand">
                {edit.id ? "Save" : "Add flavor"}
              </Button>
            </div>
          </form>
        ) : (
          <Button variant="secondary" className="w-fit" onClick={() => setEdit({ name: "", cores: 2, memory_mb: 2048, disk_gb: 20, description: "" })}>
            <Plus /> Add flavor
          </Button>
        )}
        {err && <p className="text-sm text-destructive">{err}</p>}
      </DialogContent>
    </Dialog>
  );
}
