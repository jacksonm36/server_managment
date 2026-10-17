import { useMemo } from "react";
import type { PVEGuest, PVEResources } from "@/lib/api";
import { formatBytes, formatUptime, pct } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";

// Cluster overview: a health score, headline counts, per-node usage and the
// guests using the most memory.

export type Health = { score: number; label: string; tone: "success" | "warning" | "destructive"; reasons: string[] };

export function clusterHealth(res: PVEResources): Health {
  const nodes = res.nodes ?? [];
  const guests = (res.guests ?? []).filter((g) => !g.template);
  let score = 100;
  const reasons: string[] = [];
  const offline = nodes.filter((n) => n.status !== "online").length;
  if (offline) {
    score -= 18 * offline;
    reasons.push(`${offline} node${offline > 1 ? "s" : ""} offline`);
  }
  if (guests.length) {
    const stopped = guests.filter((g) => g.status !== "running").length;
    const p = Math.round((12 * stopped) / guests.length);
    if (p) {
      score -= p;
      reasons.push(`${stopped} of ${guests.length} guests stopped`);
    }
  }
  const on = nodes.filter((n) => n.status === "online");
  if (on.length) {
    const cores = on.reduce((a, n) => a + n.maxcpu, 0);
    const cpu = cores ? (100 * on.reduce((a, n) => a + n.cpu * n.maxcpu, 0)) / cores : 0;
    const mem = pct(
      on.reduce((a, n) => a + n.mem, 0),
      on.reduce((a, n) => a + n.maxmem, 0),
    );
    if (cpu > 85) score -= 10;
    else if (cpu > 70) score -= 5;
    if (cpu > 70) reasons.push(`cluster CPU at ${Math.round(cpu)}%`);
    if (mem > 90) score -= 10;
    else if (mem > 80) score -= 5;
    if (mem > 80) reasons.push(`cluster memory at ${Math.round(mem)}%`);
  }
  score = Math.max(0, Math.min(100, score));
  const [label, tone] =
    score >= 90 ? ["Excellent", "success"] : score >= 75 ? ["Good", "success"] : score >= 50 ? ["Fair", "warning"] : (["Poor", "destructive"] as const);
  return { score, label, tone: tone as Health["tone"], reasons };
}

export function PVEOverview({ res, onGuest }: { res: PVEResources; onGuest: (g: PVEGuest) => void }) {
  const health = useMemo(() => clusterHealth(res), [res]);
  const nodes = res.nodes ?? [];
  const all = res.guests ?? [];
  const guests = all.filter((g) => !g.template);
  const running = guests.filter((g) => g.status === "running");
  const templates = all.filter((g) => g.template).length;
  const hottest = [...running].sort((a, b) => b.mem - a.mem).slice(0, 6);
  const perNode = (n: string) => {
    const g = guests.filter((x) => x.node === n);
    return { run: g.filter((x) => x.status === "running").length, stop: g.filter((x) => x.status !== "running").length };
  };

  return (
    <div className="grid gap-4">
      <Card className="p-5">
        <div className="mb-3 text-xs font-semibold tracking-wider text-muted-foreground uppercase">Health score</div>
        <div className="flex flex-wrap items-center gap-5">
          <ScoreRing score={health.score} tone={health.tone} />
          <div className="grid min-w-0 flex-1 gap-2">
            <Badge variant={health.tone === "destructive" ? "destructive" : health.tone} className="w-fit gap-1.5">
              <span className="size-1.5 rounded-full bg-current" /> {health.label}
            </Badge>
            <p className="text-sm text-muted-foreground">
              {health.reasons.length ? `Points off for: ${health.reasons.join(", ")}.` : "All nodes online, guests running and headroom on CPU and memory."}
            </p>
            <p className="text-xs text-muted-foreground/70">
              Starts at 100 and loses 18 per offline node, up to 12 for the share of stopped guests, and 5 or 10 each when cluster CPU is above 70/85% or memory
              above 80/90%.
            </p>
          </div>
        </div>
      </Card>

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        {nodes.length > 0 && <Stat label="Nodes" value={`${nodes.filter((n) => n.status === "online").length}/${nodes.length}`} sub="online" />}
        <Stat label="Running" value={running.length} sub={`of ${guests.length} guests`} />
        <Stat label="Stopped" value={guests.length - running.length} sub="not running" />
        <Stat label="Templates" value={templates} sub="QEMU / LXC" />
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        {nodes.length > 0 && (
          <Card className="p-5">
            <div className="mb-3 text-xs font-semibold tracking-wider text-muted-foreground uppercase">Nodes</div>
            <div className="grid gap-5">
              {nodes.map((n) => {
                const c = perNode(n.node);
                return (
                  <div key={n.node}>
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-semibold">{n.node}</span>
                      <Badge variant={n.status === "online" ? "success" : "destructive"} className="gap-1.5">
                        <span className="size-1.5 rounded-full bg-current" /> {n.status}
                      </Badge>
                      {n.uptime > 0 && <span className="text-sm text-muted-foreground">{formatUptime(n.uptime)}</span>}
                      <span className="ml-auto text-xs text-muted-foreground">
                        {c.run} running · {c.stop} stopped
                      </span>
                    </div>
                    <Bar label="CPU" sub={`${n.maxcpu} cores`} v={n.cpu * 100} />
                    <Bar label="RAM" sub={`${formatBytes(n.mem)} / ${formatBytes(n.maxmem)}`} v={pct(n.mem, n.maxmem)} />
                    <Bar label="Disk" sub={`${formatBytes(n.disk)} / ${formatBytes(n.maxdisk)}`} v={pct(n.disk, n.maxdisk)} />
                  </div>
                );
              })}
            </div>
          </Card>
        )}
        <Card className={`p-5 ${nodes.length ? "" : "lg:col-span-2"}`}>
          <div className="mb-3 text-xs font-semibold tracking-wider text-muted-foreground uppercase">Hottest guests</div>
          {hottest.length === 0 ? (
            <p className="text-sm text-muted-foreground">Nothing running.</p>
          ) : (
            <table className="w-full text-sm">
              <thead>
                <tr className="bg-muted/60 text-left text-xs font-semibold tracking-wider text-muted-foreground uppercase">
                  <th className="rounded-l-md px-2 py-1.5">Guest</th>
                  <th className="px-2 py-1.5">Node</th>
                  <th className="px-2 py-1.5">RAM</th>
                  <th className="rounded-r-md px-2 py-1.5">CPU</th>
                </tr>
              </thead>
              <tbody className="divide-y">
                {hottest.map((g) => (
                  <tr key={g.id}>
                    <td className="px-2 py-2">
                      <button className="cursor-pointer hover:text-brand hover:underline" onClick={() => onGuest(g)}>
                        {g.name}
                      </button>
                      <span className="text-muted-foreground"> · {g.vmid}</span>
                    </td>
                    <td className="px-2 py-2 text-muted-foreground">{g.node}</td>
                    <td className="tabular px-2 py-2">{formatBytes(g.mem)}</td>
                    <td className="tabular px-2 py-2">{Math.round(g.cpu * 100)}%</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </Card>
      </div>
    </div>
  );
}

function ScoreRing({ score, tone }: { score: number; tone: Health["tone"] }) {
  const r = 34;
  const c = 2 * Math.PI * r;
  const color = tone === "success" ? "text-brand" : tone === "warning" ? "text-warning" : "text-destructive";
  return (
    <div className="relative size-24 shrink-0">
      <svg viewBox="0 0 84 84" className="size-full -rotate-90">
        <circle cx="42" cy="42" r={r} fill="none" strokeWidth="9" className="stroke-muted" />
        <circle
          cx="42"
          cy="42"
          r={r}
          fill="none"
          strokeWidth="9"
          strokeLinecap="round"
          stroke="currentColor"
          className={`${color} transition-[stroke-dashoffset] duration-700`}
          strokeDasharray={c}
          strokeDashoffset={c * (1 - score / 100)}
        />
      </svg>
      <div className="absolute inset-0 flex flex-col items-center justify-center">
        <span className="tabular text-2xl leading-none font-bold">{score}</span>
        <span className="text-[10px] text-muted-foreground">/ 100</span>
      </div>
    </div>
  );
}

function Stat({ label, value, sub }: { label: string; value: string | number; sub: string }) {
  return (
    <Card className="p-4">
      <div className="text-xs font-semibold tracking-wider text-muted-foreground uppercase">{label}</div>
      <div className="tabular mt-1 text-2xl font-bold">{value}</div>
      <div className="text-xs text-muted-foreground">{sub}</div>
    </Card>
  );
}

function Bar({ label, sub, v }: { label: string; sub: string; v: number }) {
  const w = Math.max(0, Math.min(100, v));
  return (
    <div className="mt-2 grid grid-cols-[7.5rem_1fr_2.5rem] items-center gap-3 text-xs">
      <div>
        <div>{label}</div>
        <div className="truncate text-[10px] text-muted-foreground">{sub}</div>
      </div>
      <div className="h-1.5 rounded-full bg-muted">
        <div className={`h-1.5 rounded-full ${w > 90 ? "bg-destructive" : w > 75 ? "bg-warning" : "bg-brand"}`} style={{ width: `${w}%` }} />
      </div>
      <span className="tabular text-right text-muted-foreground">{Math.round(w)}%</span>
    </div>
  );
}
