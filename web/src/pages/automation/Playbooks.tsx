import { useCallback, useEffect, useState } from "react";
import { ArrowDown, ArrowUp, Copy, ListOrdered, Pencil, Play, Plus, Trash2 } from "lucide-react";
import { api, type Playbook, type PlaybookRun, type Step } from "@/lib/api";
import { CATALOG, clean, defaults, missing, opDef } from "@/lib/catalog";
import { useLive } from "@/lib/live";
import { timeAgo } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label } from "@/components/ui/input";
import { Select, Textarea } from "@/components/ui/select";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { OpForm } from "@/components/OpForm";
import { ServerPicker } from "@/components/ServerPicker";

export const stepLabel = (s: Step) => {
  const d = opDef(s.op);
  const extra = Object.entries(s.params ?? {})
    .filter(([, v]) => v && v !== "false")
    .map(([k, v]) => (v === "true" ? k : `${k}=${v.length > 24 ? v.slice(0, 21) + "…" : v}`))
    .join(" ");
  return `${d?.label ?? s.op}${extra ? ` (${extra})` : ""}`;
};

export function Playbooks() {
  const [list, setList] = useState<Playbook[]>([]);
  const [editing, setEditing] = useState<Playbook | null>(null);
  const [running, setRunning] = useState<Playbook | null>(null);
  const [error, setError] = useState("");
  const reload = useCallback(() => api.playbooks().then(setList).catch((e) => setError(e.message)), []);
  useEffect(() => {
    void reload();
  }, [reload]);

  const blank: Playbook = { id: "", name: "", description: "", steps: [], builtin: false, created_at: "", updated_at: "" };

  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <DefaultBaseline playbooks={list} />
        <Button variant="brand" size="sm" onClick={() => setEditing(blank)}>
          <Plus /> New playbook
        </Button>
      </div>
      {error && <p className="text-sm text-destructive">{error}</p>}
      <div className="grid gap-4 lg:grid-cols-2">
        {list.map((p) => (
          <Card key={p.id}>
            <CardHeader className="flex-row items-start justify-between gap-3">
              <div className="min-w-0">
                <CardTitle className="flex items-center gap-2">
                  {p.name}
                  {p.builtin && <Badge variant="outline">template</Badge>}
                </CardTitle>
                {p.description && <CardDescription>{p.description}</CardDescription>}
              </div>
              <Button variant="brand" size="sm" onClick={() => setRunning(p)}>
                <Play /> Run
              </Button>
            </CardHeader>
            <CardContent className="grid gap-3">
              <ol className="grid gap-1 text-sm">
                {p.steps.map((s, i) => (
                  <li key={i} className="flex gap-2">
                    <span className="w-5 shrink-0 text-right text-muted-foreground tabular">{i + 1}.</span>
                    <span className="min-w-0 truncate">{stepLabel(s)}</span>
                    {s.continue_on_error && <Badge variant="outline" className="shrink-0">continue on error</Badge>}
                  </li>
                ))}
              </ol>
              <div className="flex gap-1">
                <Button variant="ghost" size="sm" onClick={() => setEditing({ ...p, id: "", builtin: false, name: `${p.name} (copy)` })}>
                  <Copy /> Duplicate
                </Button>
                {!p.builtin && (
                  <>
                    <Button variant="ghost" size="sm" onClick={() => setEditing(p)}>
                      <Pencil /> Edit
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={async () => {
                        if (!confirm(`Delete playbook ${p.name}?`)) return;
                        await api.deletePlaybook(p.id).catch((e) => setError(e.message));
                        void reload();
                      }}
                    >
                      <Trash2 className="text-destructive" /> Delete
                    </Button>
                  </>
                )}
              </div>
            </CardContent>
          </Card>
        ))}
      </div>
      {editing && (
        <PlaybookEditor
          pb={editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            void reload();
          }}
        />
      )}
      {running && <RunDialog pb={running} onClose={() => setRunning(null)} />}
    </div>
  );
}

function PlaybookEditor({ pb, onClose, onSaved }: { pb: Playbook; onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState(pb.name);
  const [description, setDescription] = useState(pb.description);
  const [steps, setSteps] = useState<Step[]>(pb.steps.map((s) => ({ ...s, params: { ...defaults(opDef(s.op) ?? CATALOG[0]), ...s.params } })));
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const update = (i: number, s: Step) => setSteps(steps.map((x, j) => (j === i ? s : x)));
  const move = (i: number, d: number) => {
    const n = [...steps];
    [n[i], n[i + d]] = [n[i + d], n[i]];
    setSteps(n);
  };

  async function save() {
    for (const [i, s] of steps.entries()) {
      const def = opDef(s.op);
      const miss = def && missing(def, s.params);
      if (miss) {
        setError(`Step ${i + 1}: ${miss} is required`);
        return;
      }
    }
    setBusy(true);
    setError("");
    try {
      const body = {
        name,
        description,
        steps: steps.map((s) => ({ op: s.op, params: opDef(s.op) ? clean(opDef(s.op)!, s.params) : s.params, continue_on_error: s.continue_on_error })),
      };
      await (pb.id ? api.updatePlaybook(pb.id, body) : api.createPlaybook(body));
      onSaved();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90svh] max-w-2xl overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{pb.id ? "Edit playbook" : "New playbook"}</DialogTitle>
          <DialogDescription>Steps run in order on each server. A failed step stops the run unless marked "continue on error".</DialogDescription>
        </DialogHeader>
        <div className="grid gap-4">
          <div className="grid gap-2">
            <Label htmlFor="pb-name">Name</Label>
            <Input id="pb-name" value={name} onChange={(e) => setName(e.target.value)} maxLength={100} />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="pb-desc">Description</Label>
            <Textarea id="pb-desc" rows={2} value={description} onChange={(e) => setDescription(e.target.value)} />
          </div>
          {steps.map((s, i) => (
            <div key={i} className="grid gap-3 rounded-lg border p-3">
              <div className="flex items-center justify-between">
                <span className="flex items-center gap-1.5 text-sm font-medium">
                  <ListOrdered className="size-4 text-brand" /> Step {i + 1}
                </span>
                <div className="flex gap-0.5">
                  <Button variant="ghost" size="icon" className="size-7" aria-label="Move up" disabled={i === 0} onClick={() => move(i, -1)}>
                    <ArrowUp />
                  </Button>
                  <Button variant="ghost" size="icon" className="size-7" aria-label="Move down" disabled={i === steps.length - 1} onClick={() => move(i, 1)}>
                    <ArrowDown />
                  </Button>
                  <Button variant="ghost" size="icon" className="size-7" aria-label="Remove step" onClick={() => setSteps(steps.filter((_, j) => j !== i))}>
                    <Trash2 className="text-destructive" />
                  </Button>
                </div>
              </div>
              <OpForm op={s.op} params={s.params} idPrefix={`step${i}`} onChange={(op, params) => update(i, { ...s, op, params })} />
              <label className="flex cursor-pointer items-center gap-2 text-sm text-muted-foreground">
                <input
                  type="checkbox"
                  className="accent-[var(--color-brand)]"
                  checked={!!s.continue_on_error}
                  onChange={(e) => update(i, { ...s, continue_on_error: e.target.checked })}
                />
                Continue with the next step if this one fails
              </label>
            </div>
          ))}
          <Button variant="outline" className="w-fit" onClick={() => setSteps([...steps, { op: CATALOG[0].op, params: defaults(CATALOG[0]) }])}>
            <Plus /> Add step
          </Button>
          {error && <p className="text-sm text-destructive">{error}</p>}
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button variant="brand" disabled={busy || !name.trim() || steps.length === 0} onClick={save}>
              Save
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function RunDialog({ pb, onClose }: { pb: Playbook; onClose: () => void }) {
  const [ids, setIds] = useState<string[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [started, setStarted] = useState(false);
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Run "{pb.name}"</DialogTitle>
          <DialogDescription>{pb.steps.length} step(s). Each server runs independently; watch progress under Runs.</DialogDescription>
        </DialogHeader>
        {started ? (
          <div className="grid gap-3">
            <p className="text-sm">Started on {ids.length} server(s). Progress is shown in the Runs tab.</p>
            <div className="flex justify-end">
              <Button variant="secondary" onClick={onClose}>
                Close
              </Button>
            </div>
          </div>
        ) : (
          <div className="grid gap-4">
            <ServerPicker value={ids} onChange={setIds} />
            {error && <p className="text-sm text-destructive">{error}</p>}
            <div className="flex justify-end gap-2">
              <Button variant="ghost" onClick={onClose}>
                Cancel
              </Button>
              <Button
                variant="brand"
                disabled={busy || ids.length === 0}
                onClick={async () => {
                  setBusy(true);
                  try {
                    await api.runPlaybook(pb.id, ids);
                    setStarted(true);
                  } catch (e) {
                    setError((e as Error).message);
                  } finally {
                    setBusy(false);
                  }
                }}
              >
                <Play /> Run on {ids.length || "…"}
              </Button>
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

export function Runs() {
  const [runs, setRuns] = useState<PlaybookRun[]>([]);
  const tick = useLive((s) => s.jobTick);
  useEffect(() => {
    api.playbookRuns().then(setRuns);
  }, [tick]);
  // Runs update through job events; also poll slowly while any is running.
  useEffect(() => {
    if (!runs.some((r) => r.status === "running")) return;
    const t = setTimeout(() => api.playbookRuns().then(setRuns), 3000);
    return () => clearTimeout(t);
  }, [runs]);

  if (runs.length === 0) return <p className="text-sm text-muted-foreground">No playbook runs yet.</p>;
  return (
    <div className="grid gap-3">
      {runs.map((r) => (
        <Card key={r.id} className="p-4">
          <div className="mb-2 flex flex-wrap items-center gap-2 text-sm">
            <span className="font-medium">{r.playbook_name}</span>
            <span className="text-muted-foreground">on</span>
            <span className="font-medium">{r.server_name}</span>
            <Badge variant={r.status === "succeeded" ? "success" : r.status === "failed" ? "destructive" : "warning"}>{r.status}</Badge>
            <span className="ml-auto text-xs text-muted-foreground">
              {r.created_by} · {timeAgo(r.created_at)}
            </span>
          </div>
          <ol className="grid gap-1 text-sm">
            {r.steps.map((s, i) => (
              <li key={i} className="flex items-center gap-2">
                <span
                  className={`size-2 shrink-0 rounded-full ${
                    s.status === "succeeded" ? "bg-success" : s.status === "failed" ? "bg-destructive" : s.status === "running" ? "animate-pulse bg-warning" : "bg-muted-foreground/40"
                  }`}
                />
                <span className="min-w-0 truncate">
                  {s.rollback && <span className="text-warning">undo: </span>}
                  {stepLabel(s)}
                </span>
                <span className="text-xs text-muted-foreground">{s.status}</span>
                {s.error && <span className="truncate text-xs text-destructive">{s.error}</span>}
              </li>
            ))}
          </ol>
        </Card>
      ))}
    </div>
  );
}

function DefaultBaseline({ playbooks }: { playbooks: Playbook[] }) {
  const [value, setValue] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  useEffect(() => {
    void api.baseline().then((b) => setValue(b.playbook_id));
  }, []);
  if (value === null) return <span />;
  return (
    <label className="flex flex-wrap items-center gap-2 text-sm">
      <span className="text-muted-foreground">Default baseline for new servers</span>
      <Select
        className="h-8 w-56"
        value={value}
        onChange={async (e) => {
          const r = await api.setBaseline(e.target.value);
          setValue(r.playbook_id);
          setSaved(true);
          setTimeout(() => setSaved(false), 1500);
        }}
      >
        <option value="">None</option>
        {playbooks.map((p) => (
          <option key={p.id} value={p.id}>
            {p.name}
          </option>
        ))}
      </Select>
      {saved && <span className="text-xs text-success">saved</span>}
    </label>
  );
}
