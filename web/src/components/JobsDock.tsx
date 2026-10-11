import { useEffect, useMemo, useRef, useState } from "react";
import { Check, ChevronDown, CircleX, ListChecks, LoaderCircle, X } from "lucide-react";
import { api, type JobSummary } from "@/lib/api";
import { useLive } from "@/lib/live";
import { opDef } from "@/lib/catalog";
import { JobResults } from "@/components/JobResults";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";

const DISMISSED_KEY = "servermgr.jobs-dock.dismissed";

const done = (j: JobSummary) => j.status === "succeeded" || j.status === "failed";

export function opLabel(op: string) {
  const def = opDef(op);
  if (def) return def.label;
  const s = op.replace(/[._]/g, " ");
  return s.charAt(0).toUpperCase() + s.slice(1);
}

/** Jobs started together (one bulk run or key deploy) shown as one entry. */
type Run = { key: string; op: string; createdBy: string; createdAt: string; jobs: JobSummary[] };

function groupRuns(jobs: JobSummary[]): Run[] {
  const runs: Run[] = [];
  const sorted = [...jobs].sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at));
  for (const j of sorted) {
    const t = Date.parse(j.created_at);
    const run = runs.find(
      (r) =>
        r.op === j.op &&
        r.createdBy === j.created_by &&
        Math.abs(Date.parse(r.createdAt) - t) < 3000 &&
        !r.jobs.some((x) => x.server_id === j.server_id),
    );
    if (run) run.jobs.push(j);
    else runs.push({ key: j.id, op: j.op, createdBy: j.created_by, createdAt: j.created_at, jobs: [j] });
  }
  return runs;
}

function loadDismissed(): Set<string> {
  try {
    return new Set(JSON.parse(localStorage.getItem(DISMISSED_KEY) ?? "[]") as string[]);
  } catch {
    return new Set();
  }
}

function saveDismissed(s: Set<string>) {
  try {
    localStorage.setItem(DISMISSED_KEY, JSON.stringify([...s].slice(-300)));
  } catch {
    // Private mode or blocked storage: dismissals last until reload.
  }
}

/**
 * The jobs tray in the lower right corner: everything running now, plus your
 * own jobs that finished recently. Click one to reopen its live output.
 */
export function JobsDock({ me }: { me: string }) {
  const [jobs, setJobs] = useState<JobSummary[]>([]);
  const [open, setOpen] = useState(false);
  const [viewing, setViewing] = useState<string | null>(null);
  const [dismissed, setDismissed] = useState(loadDismissed);
  const jobTick = useLive((s) => s.jobTick);
  const anyRunning = jobs.some((j) => !done(j));

  // Refresh on job events (debounced), and every few seconds while jobs run.
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => {
    clearTimeout(timer.current);
    timer.current = setTimeout(() => {
      api
        .activeJobs()
        .then(setJobs)
        .catch(() => {});
    }, 400);
    return () => clearTimeout(timer.current);
  }, [jobTick]);
  useEffect(() => {
    if (!anyRunning) return;
    const t = setInterval(() => {
      api
        .activeJobs()
        .then(setJobs)
        .catch(() => {});
    }, 4000);
    return () => clearInterval(t);
  }, [anyRunning]);

  const runs = useMemo(
    () =>
      groupRuns(jobs.filter((j) => !done(j) || (j.created_by === me && !dismissed.has(j.id)))).filter((r) => r.jobs.length > 0),
    [jobs, me, dismissed],
  );
  const running = runs.filter((r) => r.jobs.some((j) => !done(j))).length;
  const failed = runs.filter((r) => r.jobs.every(done) && r.jobs.some((j) => j.status === "failed")).length;

  // Keep the run being viewed even after it is dismissed or ages out.
  const viewed = useRef<Run | null>(null);
  const live = runs.find((r) => r.key === viewing);
  if (live) viewed.current = live;
  const view = viewing ? (live ?? viewed.current) : null;

  function dismiss(keys: string[]) {
    const next = new Set(dismissed);
    runs.filter((r) => keys.includes(r.key)).forEach((r) => r.jobs.forEach((j) => next.add(j.id)));
    setDismissed(next);
    saveDismissed(next);
  }

  if (runs.length === 0 && !view) return null;

  return (
    <>
      {runs.length > 0 && (
        <div className="fixed right-4 bottom-4 z-40 flex w-[min(24rem,calc(100vw-2rem))] flex-col items-end gap-2">
          {open && (
            <div className="w-full overflow-hidden rounded-xl border bg-popover text-popover-foreground shadow-2xl">
              <div className="flex items-center justify-between border-b px-3 py-2 text-sm font-medium">
                Jobs
                <button
                  onClick={() => setOpen(false)}
                  className="cursor-pointer rounded p-1 text-muted-foreground hover:text-foreground"
                  aria-label="Hide jobs"
                >
                  <ChevronDown className="size-4" />
                </button>
              </div>
              <ul className="max-h-80 divide-y overflow-y-auto">
                {runs.map((r) => (
                  <RunRow key={r.key} run={r} me={me} onOpen={() => setViewing(r.key)} onDismiss={() => dismiss([r.key])} />
                ))}
              </ul>
              {runs.length - running > 0 && (
                <div className="flex justify-end border-t px-3 py-1.5">
                  <button
                    className="cursor-pointer text-xs text-muted-foreground hover:text-foreground"
                    onClick={() => dismiss(runs.filter((r) => r.jobs.every(done)).map((r) => r.key))}
                  >
                    Clear finished
                  </button>
                </div>
              )}
            </div>
          )}
          <button
            onClick={() => setOpen(!open)}
            className="flex cursor-pointer items-center gap-2 rounded-full border bg-popover px-4 py-2 text-sm font-medium shadow-xl hover:border-brand/60"
            aria-expanded={open}
          >
            {running > 0 ? (
              <LoaderCircle className="size-4 animate-spin text-brand" />
            ) : failed > 0 ? (
              <CircleX className="size-4 text-destructive" />
            ) : (
              <ListChecks className="size-4 text-success" />
            )}
            {running > 0 ? `${running} running` : `${runs.length} finished`}
            {running > 0 && runs.length > running && <span className="text-muted-foreground">· {runs.length - running} finished</span>}
          </button>
        </div>
      )}
      {view && <RunDialog run={view} onClose={() => setViewing(null)} />}
    </>
  );
}

function RunRow({ run, me, onOpen, onDismiss }: { run: Run; me: string; onOpen: () => void; onDismiss: () => void }) {
  const active = run.jobs.filter((j) => !done(j)).length;
  const failed = run.jobs.filter((j) => j.status === "failed").length;
  const where = run.jobs.length === 1 ? run.jobs[0].server_name : `${run.jobs.length} servers`;
  const state = active
    ? run.jobs.length > 1
      ? `${run.jobs.length - active} of ${run.jobs.length} done`
      : "running"
    : failed
      ? `${failed} failed`
      : "succeeded";
  return (
    <li className="group flex items-center gap-2 px-3 py-2 text-sm hover:bg-accent/50">
      {active ? (
        <LoaderCircle className="size-4 shrink-0 animate-spin text-brand" />
      ) : failed ? (
        <CircleX className="size-4 shrink-0 text-destructive" />
      ) : (
        <Check className="size-4 shrink-0 text-success" />
      )}
      <button onClick={onOpen} className="min-w-0 flex-1 cursor-pointer text-left">
        <span className="block truncate font-medium">{opLabel(run.op)}</span>
        <span className="block truncate text-xs text-muted-foreground">
          {where} · {state}
          {run.createdBy !== me && ` · by ${run.createdBy}`}
        </span>
      </button>
      {!active && (
        <button
          onClick={onDismiss}
          className="cursor-pointer rounded p-1 text-muted-foreground opacity-0 group-hover:opacity-100 hover:text-foreground focus:opacity-100"
          aria-label="Dismiss"
          title="Dismiss"
        >
          <X className="size-3.5" />
        </button>
      )}
    </li>
  );
}

function RunDialog({ run, onClose }: { run: Run; onClose: () => void }) {
  const ids = run.jobs.map((j) => j.id).join(",");
  // A new array only when the set of jobs changes, so polling isn't restarted.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const results = useMemo(() => run.jobs.map((j) => ({ server_id: j.server_id, job_id: j.id })), [ids]);
  const names = new Map(run.jobs.map((j) => [j.server_id, j.server_name]));
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90svh] overflow-y-auto sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>
            {opLabel(run.op)} on {run.jobs.length} server(s)
          </DialogTitle>
          <DialogDescription>
            Started by {run.createdBy} at {new Date(run.createdAt).toLocaleTimeString()}
          </DialogDescription>
        </DialogHeader>
        <JobResults key={ids} results={results} name={(id) => names.get(id) ?? id} onNavigate={onClose} />
        <div className="flex justify-end">
          <Button variant="secondary" onClick={onClose}>
            Close
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
