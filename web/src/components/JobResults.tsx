import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { Link } from "wouter";
import { Check, ChevronDown, ChevronRight, CircleX, Copy, LoaderCircle } from "lucide-react";
import { api, type Job } from "@/lib/api";
import { Badge } from "@/components/ui/badge";

export type StartedJob = { server_id: string; job_id?: string; error?: string };

const finished = (j?: Job) => j?.status === "succeeded" || j?.status === "failed";

/**
 * Live per-server results of jobs started on several servers: a status row
 * for each and a console that streams the job's output while it runs.
 */
export function JobResults({
  results,
  name,
  okLabel = "succeeded",
  onDone,
  onNavigate,
}: {
  results: StartedJob[];
  name: (serverId: string) => string;
  okLabel?: string;
  onDone?: (jobs: Record<string, Job>) => void;
  onNavigate?: () => void;
}) {
  const [jobs, setJobs] = useState<Record<string, Job>>({});
  const [open, setOpen] = useState<Record<string, boolean>>(() => Object.fromEntries(results.map((r) => [r.server_id, results.length <= 3])));
  const doneRef = useRef(onDone);
  doneRef.current = onDone;

  // Poll unfinished jobs about once a second until all are done.
  useEffect(() => {
    const ids = results.filter((r) => r.job_id).map((r) => r.job_id!);
    const latest: Record<string, Job> = {};
    let stop = false;
    let timer: ReturnType<typeof setTimeout>;
    const tick = async () => {
      const pending = ids.filter((id) => !finished(latest[id]));
      const got = await Promise.all(pending.map((id) => api.job(id).catch(() => null)));
      if (stop) return;
      got.forEach((j) => j && (latest[j.id] = j));
      setJobs({ ...latest });
      if (ids.every((id) => finished(latest[id]))) {
        doneRef.current?.(latest);
        return;
      }
      timer = setTimeout(tick, 1000);
    };
    void tick();
    return () => {
      stop = true;
      clearTimeout(timer);
    };
  }, [results]);

  // Open a server's console once when its job fails; closing it again sticks.
  const autoOpened = useRef(new Set<string>());
  useEffect(() => {
    const fresh = results
      .filter((r) => (r.error || (r.job_id && jobs[r.job_id]?.status === "failed")) && !autoOpened.current.has(r.server_id))
      .map((r) => r.server_id);
    if (fresh.length === 0) return;
    fresh.forEach((id) => autoOpened.current.add(id));
    setOpen((o) => ({
      ...o,
      ...Object.fromEntries(fresh.map((id) => [id, true])),
    }));
  }, [jobs, results]);

  const status = (r: StartedJob) => (r.error ? "failed" : (jobs[r.job_id!]?.status ?? "queued"));
  const counts = results.reduce<Record<string, number>>((m, r) => ((m[status(r)] = (m[status(r)] ?? 0) + 1), m), {});
  const allDone = results.every((r) => r.error || finished(jobs[r.job_id!]));

  return (
    <div className="grid gap-2">
      <p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
        {!allDone && <LoaderCircle className="size-3.5 animate-spin text-brand" />}
        {(["running", "queued", "succeeded", "failed"] as const)
          .filter((s) => counts[s])
          .map((s) => (
            <span key={s}>
              {counts[s]} {s === "succeeded" ? okLabel : s}
            </span>
          ))}
        {results.length > 1 && (
          <button
            className="ml-auto cursor-pointer hover:text-foreground"
            onClick={() => {
              const anyClosed = results.some((r) => !open[r.server_id]);
              setOpen(Object.fromEntries(results.map((r) => [r.server_id, anyClosed])));
            }}
          >
            {results.some((r) => !open[r.server_id]) ? "Show all output" : "Hide all output"}
          </button>
        )}
      </p>
      <ul className="grid gap-2">
        {results.map((r) => (
          <ResultRow
            key={r.server_id}
            r={r}
            job={r.job_id ? jobs[r.job_id] : undefined}
            status={status(r)}
            name={name(r.server_id)}
            okLabel={okLabel}
            open={!!open[r.server_id]}
            onToggle={() => setOpen((o) => ({ ...o, [r.server_id]: !o[r.server_id] }))}
            onNavigate={onNavigate}
          />
        ))}
      </ul>
    </div>
  );
}

function ResultRow({
  r,
  job,
  status,
  name,
  okLabel,
  open,
  onToggle,
  onNavigate,
}: {
  r: StartedJob;
  job?: Job;
  status: string;
  name: string;
  okLabel: string;
  open: boolean;
  onToggle: () => void;
  onNavigate?: () => void;
}) {
  const running = status === "running" || status === "queued";
  const error = r.error || job?.error;
  return (
    <li className="overflow-hidden rounded-lg border">
      <div className="flex items-center gap-2 px-3 py-2 text-sm">
        <button
          onClick={onToggle}
          aria-expanded={open}
          aria-label={`${open ? "Hide" : "Show"} output for ${name}`}
          className="-ml-1 cursor-pointer rounded p-0.5 text-muted-foreground hover:text-foreground"
        >
          {open ? <ChevronDown className="size-4" /> : <ChevronRight className="size-4" />}
        </button>
        {running ? (
          <LoaderCircle className="size-4 shrink-0 animate-spin text-brand" />
        ) : status === "succeeded" ? (
          <Check className="size-4 shrink-0 text-success" />
        ) : (
          <CircleX className="size-4 shrink-0 text-destructive" />
        )}
        <Link href={`/servers/${r.server_id}#tasks`} className="min-w-0 flex-1 truncate font-medium hover:text-brand" onClick={onNavigate}>
          {name}
        </Link>
        {!open && error && <span className="max-w-[45%] truncate text-xs text-destructive">{error}</span>}
        {job && <Elapsed job={job} />}
        <Badge variant={status === "succeeded" ? "success" : status === "failed" ? "destructive" : "warning"}>
          {status === "succeeded" ? okLabel : status}
        </Badge>
      </div>
      {open && <Console job={job} error={error} running={running} />}
    </li>
  );
}

function Elapsed({ job }: { job: Job }) {
  const [now, setNow] = useState(Date.now());
  const done = !!job.finished_at;
  useEffect(() => {
    if (done) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [done]);
  const end = job.finished_at ? Date.parse(job.finished_at) : now;
  const s = Math.max(0, Math.round((end - Date.parse(job.created_at)) / 1000));
  return <span className="font-mono text-xs text-muted-foreground tabular">{s >= 60 ? `${Math.floor(s / 60)}m ${s % 60}s` : `${s}s`}</span>;
}

/** The job's output, following the end while the user is at the bottom. */
function Console({ job, error, running }: { job?: Job; error?: string; running: boolean }) {
  const ref = useRef<HTMLPreElement>(null);
  const follow = useRef(true);
  const [copied, setCopied] = useState(false);
  const log = job?.log ?? "";
  const result = !log && job?.status === "succeeded" && job.result != null ? JSON.stringify(job.result, null, 2) : "";
  const text = log || result;

  useLayoutEffect(() => {
    const el = ref.current;
    if (el && follow.current) el.scrollTop = el.scrollHeight;
  }, [text, error]);

  return (
    <div className="relative border-t bg-background/60">
      {text && (
        <button
          onClick={() => {
            void navigator.clipboard?.writeText(text + (error ? `\n${error}` : ""));
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          }}
          className="absolute top-2 right-4 cursor-pointer rounded bg-background/80 p-1 text-muted-foreground hover:text-foreground"
          aria-label="Copy output"
          title="Copy output"
        >
          {copied ? <Check className="size-3.5 text-success" /> : <Copy className="size-3.5" />}
        </button>
      )}
      <pre
        ref={ref}
        onScroll={(e) => {
          const el = e.currentTarget;
          follow.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
        }}
        className="max-h-72 overflow-auto p-3 pr-10 font-mono text-xs leading-relaxed whitespace-pre-wrap break-words text-muted-foreground"
      >
        {text}
        {!text && (
          <span className="italic">
            {running
              ? job?.status === "running"
                ? "Running, waiting for output…"
                : "Waiting for the agent to pick it up…"
              : error
                ? ""
                : "Finished with no output."}
          </span>
        )}
        {running && text && <span className="ml-0.5 inline-block h-3 w-1.5 translate-y-0.5 animate-pulse bg-brand" />}
        {error && <span className={`block text-destructive ${text ? "mt-1" : ""}`}>{error}</span>}
      </pre>
    </div>
  );
}
