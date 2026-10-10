import { useCallback, useEffect, useState } from "react";
import { CalendarClock, Pause, Pencil, Play, Plus, Trash2, Zap } from "lucide-react";
import { api, type Playbook, type Schedule } from "@/lib/api";
import { CATALOG, clean, defaults, missing, opDef } from "@/lib/catalog";
import { useLive } from "@/lib/live";
import { timeAgo } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input, Label } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { OpForm } from "@/components/OpForm";
import { ServerPicker } from "@/components/ServerPicker";
import { stepLabel } from "./Playbooks";

const PRESETS = [
  { label: "Every hour", v: "0 * * * *" },
  { label: "Daily at 03:00", v: "0 3 * * *" },
  { label: "Weekdays at 06:00", v: "0 6 * * 1-5" },
  { label: "Sundays at 04:00", v: "0 4 * * 0" },
  { label: "1st of the month 05:00", v: "0 5 1 * *" },
];

export function Schedules() {
  const [list, setList] = useState<Schedule[]>([]);
  const [playbooks, setPlaybooks] = useState<Playbook[]>([]);
  const [editing, setEditing] = useState<Schedule | null>(null);
  const [msg, setMsg] = useState("");
  const servers = useLive((s) => s.servers);
  const reload = useCallback(() => api.schedules().then(setList), []);
  useEffect(() => {
    void reload();
    void api.playbooks().then(setPlaybooks);
  }, [reload]);

  const blank: Schedule = {
    id: "",
    name: "",
    cron: "0 3 * * *",
    kind: "op",
    op: "packages.check_updates",
    params: {},
    playbook_id: null,
    server_ids: [],
    all_servers: true,
    enabled: true,
    created_by: "",
    last_run: null,
    last_result: "",
    next_run: null,
  };

  const what = (s: Schedule) =>
    s.kind === "playbook" ? `Playbook: ${playbooks.find((p) => p.id === s.playbook_id)?.name ?? "?"}` : stepLabel({ op: s.op, params: s.params });
  const where = (s: Schedule) =>
    s.all_servers ? "all servers" : s.server_ids.map((id) => servers.get(id)?.name ?? "removed server").join(", ");

  async function save(s: Schedule, patch: Partial<Schedule> = {}) {
    const n = { ...s, ...patch };
    const body = { ...n, playbook_id: n.playbook_id ?? "" };
    await (n.id ? api.updateSchedule(n.id, body) : api.createSchedule(body));
    await reload();
  }

  return (
    <div className="grid gap-4">
      <div className="flex items-center justify-between gap-3">
        <p className="text-sm text-muted-foreground">Times use the panel server's clock. Offline servers are skipped and noted in the last result.</p>
        <Button variant="brand" size="sm" onClick={() => setEditing(blank)}>
          <Plus /> New schedule
        </Button>
      </div>
      {msg && <p className="rounded-md border px-3 py-2 text-sm">{msg}</p>}
      {list.length === 0 && <p className="text-sm text-muted-foreground">No schedules yet.</p>}
      {list.map((s) => (
        <Card key={s.id} className="flex flex-wrap items-center gap-3 p-4">
          <CalendarClock className={`size-5 shrink-0 ${s.enabled ? "text-brand" : "text-muted-foreground"}`} />
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-medium">{s.name}</span>
              <Badge variant="outline" className="font-mono">
                {s.cron}
              </Badge>
              {!s.enabled && <Badge variant="warning">paused</Badge>}
            </div>
            <div className="mt-1 truncate text-sm text-muted-foreground">
              {what(s)} → {where(s)}
            </div>
            <div className="mt-0.5 text-xs text-muted-foreground">
              {s.next_run && `Next ${new Date(s.next_run).toLocaleString()} · `}
              {s.last_run ? `Last ${timeAgo(s.last_run)}: ${s.last_result}` : "Not run yet"}
            </div>
          </div>
          <div className="flex gap-1">
            <Button
              variant="ghost"
              size="icon"
              title="Run now"
              aria-label="Run now"
              onClick={async () => {
                if (!confirm(`Run "${s.name}" now?`)) return;
                const r = await api.runScheduleNow(s.id).catch((e) => ({ result: (e as Error).message }));
                setMsg(`${s.name}: ${r.result}`);
                void reload();
              }}
            >
              <Zap />
            </Button>
            <Button variant="ghost" size="icon" title={s.enabled ? "Pause" : "Resume"} aria-label={s.enabled ? "Pause" : "Resume"} onClick={() => save(s, { enabled: !s.enabled })}>
              {s.enabled ? <Pause /> : <Play className="text-success" />}
            </Button>
            <Button variant="ghost" size="icon" title="Edit" aria-label="Edit" onClick={() => setEditing(s)}>
              <Pencil />
            </Button>
            <Button
              variant="ghost"
              size="icon"
              title="Delete"
              aria-label="Delete"
              onClick={async () => {
                if (!confirm(`Delete schedule "${s.name}"?`)) return;
                await api.deleteSchedule(s.id);
                void reload();
              }}
            >
              <Trash2 className="text-destructive" />
            </Button>
          </div>
        </Card>
      ))}
      {editing && (
        <ScheduleDialog
          s={editing}
          playbooks={playbooks}
          onClose={() => setEditing(null)}
          onSave={async (s) => {
            await save(s);
            setEditing(null);
          }}
        />
      )}
    </div>
  );
}

function ScheduleDialog({ s, playbooks, onClose, onSave }: { s: Schedule; playbooks: Playbook[]; onClose: () => void; onSave: (s: Schedule) => Promise<void> }) {
  const [d, setD] = useState<Schedule>({ ...s, params: { ...defaults(opDef(s.op) ?? CATALOG[0]), ...s.params } });
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const set = (p: Partial<Schedule>) => setD({ ...d, ...p });

  async function submit() {
    if (d.kind === "op") {
      const def = opDef(d.op);
      const miss = def && missing(def, d.params);
      if (miss) return setError(`${miss} is required`);
    }
    setBusy(true);
    setError("");
    try {
      await onSave({ ...d, params: d.kind === "op" && opDef(d.op) ? clean(opDef(d.op)!, d.params) : {} });
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90svh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{s.id ? "Edit schedule" : "New schedule"}</DialogTitle>
          <DialogDescription>Run an action or a playbook automatically.</DialogDescription>
        </DialogHeader>
        <div className="grid gap-4">
          <div className="grid gap-2">
            <Label htmlFor="sc-name">Name</Label>
            <Input id="sc-name" value={d.name} onChange={(e) => set({ name: e.target.value })} placeholder="Nightly update check" maxLength={100} />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="sc-cron">When</Label>
            <div className="flex gap-2">
              <Input id="sc-cron" className="font-mono" value={d.cron} onChange={(e) => set({ cron: e.target.value })} />
              <Select className="w-48" value="" onChange={(e) => e.target.value && set({ cron: e.target.value })} aria-label="Presets">
                <option value="">Presets…</option>
                {PRESETS.map((p) => (
                  <option key={p.v} value={p.v}>
                    {p.label}
                  </option>
                ))}
              </Select>
            </div>
            <p className="text-xs text-muted-foreground">minute hour day-of-month month day-of-week, or @daily / @hourly</p>
          </div>
          <div className="grid gap-2">
            <Label htmlFor="sc-kind">Run</Label>
            <Select id="sc-kind" value={d.kind} onChange={(e) => set({ kind: e.target.value as Schedule["kind"], playbook_id: d.playbook_id ?? playbooks[0]?.id ?? null })}>
              <option value="op">A single action</option>
              <option value="playbook">A playbook</option>
            </Select>
          </div>
          {d.kind === "op" ? (
            <OpForm op={d.op} params={d.params} idPrefix="sc" onChange={(op, params) => set({ op, params })} />
          ) : (
            <div className="grid gap-2">
              <Label htmlFor="sc-pb">Playbook</Label>
              <Select id="sc-pb" value={d.playbook_id ?? ""} onChange={(e) => set({ playbook_id: e.target.value })}>
                {playbooks.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </Select>
            </div>
          )}
          <div className="grid gap-2">
            <Label>On</Label>
            <label className="flex cursor-pointer items-center gap-2 text-sm">
              <input type="checkbox" className="accent-[var(--color-brand)]" checked={d.all_servers} onChange={(e) => set({ all_servers: e.target.checked })} />
              All servers, including ones added later
            </label>
            {!d.all_servers && <ServerPicker value={d.server_ids} onChange={(ids) => set({ server_ids: ids })} />}
          </div>
          {error && <p className="text-sm text-destructive">{error}</p>}
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button variant="brand" disabled={busy || !d.name.trim()} onClick={submit}>
              Save
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
