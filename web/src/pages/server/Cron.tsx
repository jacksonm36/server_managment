import { useState } from "react";
import { Clock, Pause, Pencil, Play, Plus, RefreshCw, Trash2 } from "lucide-react";
import { useAgentState } from "@/lib/ops";
import { timeAgo } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { OpStatus, Offline } from "@/components/OpStatus";

type Job = {
  id: string;
  schedule: string;
  user: string;
  command: string;
  comment: string;
  enabled: boolean;
  created_at: string;
  last_run?: number;
  last_output?: string;
};
type State = { jobs: Job[]; users: string[]; cron_installed: boolean };

const PRESETS = [
  { label: "Every 5 minutes", v: "*/5 * * * *" },
  { label: "Every 15 minutes", v: "*/15 * * * *" },
  { label: "Hourly", v: "@hourly" },
  { label: "Daily at 03:00", v: "0 3 * * *" },
  { label: "Weekly (Sun 04:00)", v: "0 4 * * 0" },
  { label: "Monthly (1st 05:00)", v: "0 5 1 * *" },
  { label: "At reboot", v: "@reboot" },
];

function describe(s: string): string {
  return PRESETS.find((p) => p.v === s)?.label ?? s;
}

export function Cron({ serverId, online }: { serverId: string; online: boolean }) {
  const { data, busy, error, job, run } = useAgentState<State>(serverId, "cron.list", online);
  const [editing, setEditing] = useState<Job | "new" | null>(null);
  const [output, setOutput] = useState<string | null>(null);
  const disabled = !online || !!busy;

  const save = (j: Job, patch: Partial<Job>) => {
    const n = { ...j, ...patch };
    return run("cron.save", { id: n.id, schedule: n.schedule, user: n.user, command: n.command, comment: n.comment, enabled: String(n.enabled) });
  };

  return (
    <div className="grid gap-4">
      <Card>
        <CardHeader className="flex-row flex-wrap items-start justify-between gap-3">
          <div>
            <CardTitle>Cron jobs</CardTitle>
            <CardDescription>Scheduled commands. Output is kept per job (last 1 MiB).</CardDescription>
          </div>
          <div className="flex gap-2">
            <Button variant="ghost" size="sm" disabled={disabled} onClick={() => run("cron.list")} aria-label="Refresh">
              <RefreshCw />
            </Button>
            <Button variant="brand" size="sm" disabled={disabled || !data} onClick={() => setEditing("new")}>
              <Plus /> Add job
            </Button>
          </div>
        </CardHeader>
        <CardContent className="grid gap-3">
          {!online && <Offline />}
          {data && !data.cron_installed && (
            <p className="rounded-md border border-warning/30 bg-warning/10 px-3 py-2 text-sm text-warning">
              The cron daemon is not installed on this server, so jobs won't run on schedule (Run now still works).
            </p>
          )}
          {data?.jobs.length === 0 && <p className="text-sm text-muted-foreground">No cron jobs yet.</p>}
          {data?.jobs.map((j) => (
            <div key={j.id} className="flex flex-wrap items-center gap-3 rounded-lg border px-4 py-3">
              <Clock className={`size-4 shrink-0 ${j.enabled ? "text-brand" : "text-muted-foreground"}`} />
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-medium">{j.comment || describe(j.schedule)}</span>
                  <Badge variant="outline" className="font-mono">
                    {j.schedule}
                  </Badge>
                  <Badge variant="outline">as {j.user}</Badge>
                  {!j.enabled && <Badge variant="warning">paused</Badge>}
                </div>
                <div className="mt-1 truncate font-mono text-xs text-muted-foreground" title={j.command}>
                  {j.command}
                </div>
                <div className="mt-0.5 text-xs text-muted-foreground">
                  {j.last_run ? (
                    <button className="cursor-pointer hover:text-foreground" onClick={() => setOutput(j.last_output ?? "")}>
                      Last output {timeAgo(new Date(j.last_run * 1000).toISOString())} — view
                    </button>
                  ) : (
                    "Not run yet"
                  )}
                </div>
              </div>
              <div className="flex gap-1">
                <Button variant="ghost" size="icon" title="Run now" aria-label="Run now" disabled={disabled} onClick={() => run("cron.run", { id: j.id })}>
                  <Play />
                </Button>
                <Button
                  variant="ghost"
                  size="icon"
                  title={j.enabled ? "Pause" : "Resume"}
                  aria-label={j.enabled ? "Pause" : "Resume"}
                  disabled={disabled}
                  onClick={() => save(j, { enabled: !j.enabled })}
                >
                  {j.enabled ? <Pause /> : <Play className="text-success" />}
                </Button>
                <Button variant="ghost" size="icon" title="Edit" aria-label="Edit" disabled={disabled} onClick={() => setEditing(j)}>
                  <Pencil />
                </Button>
                <Button
                  variant="ghost"
                  size="icon"
                  title="Delete"
                  aria-label="Delete"
                  disabled={disabled}
                  onClick={() => confirm(`Delete this cron job?\n\n${j.command}`) && run("cron.delete", { id: j.id })}
                >
                  <Trash2 className="text-destructive" />
                </Button>
              </div>
            </div>
          ))}
          {output !== null && (
            <div className="grid gap-2">
              <div className="flex items-center justify-between text-sm">
                <span className="font-medium">Recent output</span>
                <Button variant="ghost" size="sm" onClick={() => setOutput(null)}>
                  Close
                </Button>
              </div>
              <pre className="max-h-72 overflow-auto rounded-lg border bg-background/60 p-3 font-mono text-xs leading-relaxed text-muted-foreground">
                {output || "(empty)"}
              </pre>
            </div>
          )}
          <OpStatus busy={busy} error={error} job={job} />
        </CardContent>
      </Card>

      {editing && data && (
        <CronDialog
          job={editing === "new" ? null : editing}
          users={data.users}
          busy={!!busy}
          onClose={() => setEditing(null)}
          onSubmit={async (j) => {
            if (await save(j, {})) setEditing(null);
          }}
        />
      )}
    </div>
  );
}

function CronDialog(props: { job: Job | null; users: string[]; busy: boolean; onClose: () => void; onSubmit: (j: Job) => void }) {
  const j = props.job;
  const [schedule, setSchedule] = useState(j?.schedule ?? "0 3 * * *");
  const [user, setUser] = useState(j?.user ?? props.users[0] ?? "root");
  const [command, setCommand] = useState(j?.command ?? "");
  const [comment, setComment] = useState(j?.comment ?? "");

  return (
    <Dialog open onOpenChange={(o) => !o && props.onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{j ? "Edit cron job" : "Add cron job"}</DialogTitle>
          <DialogDescription>Runs with /bin/sh as the chosen user.</DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            props.onSubmit({
              id: j?.id ?? "",
              schedule: schedule.trim(),
              user,
              command: command.trim(),
              comment: comment.trim(),
              enabled: j?.enabled ?? true,
              created_at: j?.created_at ?? "",
            });
          }}
        >
          <div className="grid gap-2">
            <Label htmlFor="cr-name">Name (optional)</Label>
            <Input id="cr-name" placeholder="Nightly cache clear" value={comment} onChange={(e) => setComment(e.target.value)} maxLength={120} />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="cr-sched">Schedule</Label>
            <div className="flex gap-2">
              <Input id="cr-sched" className="font-mono" value={schedule} onChange={(e) => setSchedule(e.target.value)} required />
              <Select className="w-44" value="" onChange={(e) => e.target.value && setSchedule(e.target.value)} aria-label="Presets">
                <option value="">Presets…</option>
                {PRESETS.map((p) => (
                  <option key={p.v} value={p.v}>
                    {p.label}
                  </option>
                ))}
              </Select>
            </div>
            <p className="text-xs text-muted-foreground">minute hour day-of-month month day-of-week, server time</p>
          </div>
          <div className="grid gap-2">
            <Label htmlFor="cr-user">Run as</Label>
            <Select id="cr-user" value={user} onChange={(e) => setUser(e.target.value)}>
              {props.users.map((u) => (
                <option key={u} value={u}>
                  {u}
                </option>
              ))}
            </Select>
          </div>
          <div className="grid gap-2">
            <Label htmlFor="cr-cmd">Command</Label>
            <Input
              id="cr-cmd"
              className="font-mono"
              placeholder="php /srv/sites/example.com/public/artisan schedule:run"
              value={command}
              onChange={(e) => setCommand(e.target.value)}
              required
            />
            {command.includes("%") && <p className="text-xs text-destructive">% is not allowed (cron treats it as a newline). Put the command in a script instead.</p>}
          </div>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={props.onClose}>
              Cancel
            </Button>
            <Button type="submit" variant="brand" disabled={props.busy || !command.trim() || command.includes("%")}>
              Save
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
