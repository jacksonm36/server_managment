import { useEffect, useState } from "react";
import { CheckCircle2, Circle, LoaderCircle } from "lucide-react";
import { api, type Progress as ProgressData, type FeatureStatus } from "@/lib/api";
import { cn, timeAgo } from "@/lib/utils";
import { Card } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";

const REFRESH_MS = 5000;

// done counts fully, in-progress counts half.
function score(features: { status: FeatureStatus }[]) {
  if (!features.length) return 0;
  const pts = features.reduce((n, f) => n + (f.status === "done" ? 1 : f.status === "in-progress" ? 0.5 : 0), 0);
  return Math.round((100 * pts) / features.length);
}

function Bar({ value, className }: { value: number; className?: string }) {
  return (
    <div className={cn("h-2 overflow-hidden rounded-full bg-muted", className)}>
      <div className="h-full rounded-full bg-brand transition-[width] duration-700" style={{ width: `${value}%` }} />
    </div>
  );
}

function StatusIcon({ status }: { status: FeatureStatus }) {
  if (status === "done") return <CheckCircle2 className="size-4 shrink-0 text-success" />;
  if (status === "in-progress") return <LoaderCircle className="size-4 shrink-0 animate-spin text-warning" />;
  return <Circle className="size-4 shrink-0 text-muted-foreground/50" />;
}

export function Progress() {
  const [data, setData] = useState<ProgressData | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    let alive = true;
    const load = () =>
      api
        .progress()
        .then((d) => alive && (setData(d), setError("")))
        .catch((e) => alive && setError(e.message));
    void load();
    const t = setInterval(load, REFRESH_MS);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, []);

  if (!data) return <p className="text-sm text-muted-foreground">{error || "Loading…"}</p>;

  const all = data.phases.flatMap((p) => p.features);
  const overall = score(all);
  const done = all.filter((f) => f.status === "done").length;
  const doing = all.filter((f) => f.status === "in-progress").length;

  return (
    <div className="grid gap-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Build progress</h1>
        <p className="text-sm text-muted-foreground">
          Updated {timeAgo(data.updated_at)} · refreshes every {REFRESH_MS / 1000}s
          {error && <span className="text-destructive"> · {error}</span>}
        </p>
      </div>

      <Card className="p-5">
        <div className="mb-3 flex items-end justify-between gap-4">
          <div>
            <div className="text-sm text-muted-foreground">Overall</div>
            <div className="tabular text-4xl font-semibold">{overall}%</div>
          </div>
          <div className="text-right text-sm text-muted-foreground">
            {done} done · {doing} in progress · {all.length - done - doing} to do
          </div>
        </div>
        <Bar value={overall} className="h-3" />
      </Card>

      <div className="grid gap-4 lg:grid-cols-2">
        {data.phases.map((p) => {
          const s = score(p.features);
          return (
            <Card key={p.name} className="p-5">
              <div className="mb-1 flex items-center justify-between gap-3">
                <div className="font-semibold">{p.name}</div>
                <Badge variant={s === 100 ? "success" : s > 0 ? "warning" : "outline"}>{s}%</Badge>
              </div>
              {p.description && <p className="mb-3 text-sm text-muted-foreground">{p.description}</p>}
              <Bar value={s} className="mb-4" />
              <ul className="grid gap-2 text-sm">
                {p.features.map((f) => (
                  <li key={f.name} className="flex items-center gap-2.5">
                    <StatusIcon status={f.status} />
                    <span className={cn(f.status === "todo" && "text-muted-foreground")}>{f.name}</span>
                  </li>
                ))}
              </ul>
            </Card>
          );
        })}
      </div>
    </div>
  );
}
