import { useEffect, useMemo, useState, type ReactNode } from "react";
import { Box, Cpu, LayoutTemplate, Monitor, Server } from "lucide-react";
import { api, type PVEGuest, type PVENodeRRDPoint, type PVENodeStatus, type PVETask, type PVETimeframe } from "@/lib/api";
import { formatBytes, formatUptime, pct, timeAgo } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { HistoryChart } from "@/components/HistoryChart";

// A node's page, in parts: a header with what the node is (and room for
// actions), usage rings, performance history, its guests and recent
// tasks. PVENodeView puts them together read-only; NodeAdmin adds actions
// and management tabs around the same parts.

const timeframes: { tf: PVETimeframe; label: string }[] = [
  { tf: "hour", label: "Hour" },
  { tf: "day", label: "Day" },
  { tf: "week", label: "Week" },
  { tf: "month", label: "Month" },
];
const rate = (v: number) => `${formatBytes(v)}/s`;
const dot = (s: string) => (s === "running" ? "bg-success" : s === "stopped" ? "bg-muted-foreground/50" : "bg-warning");

export type NodeTask = PVETask & { id?: string };

// useNodeStatus polls the node's status and recent tasks.
export function useNodeStatus(cluster: string, node: string, online: boolean) {
  const [st, setSt] = useState<PVENodeStatus | null>(null);
  const [err, setErr] = useState("");
  const [tasks, setTasks] = useState<NodeTask[]>([]);
  useEffect(() => {
    if (!online) return;
    const get = () => {
      api
        .pveNodeStatus(cluster, node)
        .then((s) => {
          setSt(s);
          setErr("");
        })
        .catch((e) => setErr((e as Error).message));
      api
        .pveNodeTasks(cluster, node)
        .then(setTasks)
        .catch(() => setTasks([]));
    };
    get();
    const t = setInterval(get, 10000);
    return () => clearInterval(t);
  }, [cluster, node, online]);
  return { st, err, tasks };
}

export function PVENodeView({
  cluster,
  node,
  online,
  guests = [],
  onGuest,
}: {
  cluster: string;
  node: string;
  online: boolean;
  guests?: PVEGuest[];
  onGuest?: (g: PVEGuest) => void;
}) {
  const { st, err, tasks } = useNodeStatus(cluster, node, online);
  if (!online)
    return (
      <div className="grid gap-4">
        <NodeHeader node={node} st={null} online={false} />
        <Card className="p-6 text-sm text-muted-foreground">{node} is offline, so there is nothing live to show.</Card>
      </div>
    );
  return (
    <div className="grid gap-4">
      <NodeHeader node={node} st={st} online />
      {err && <p className="text-sm text-destructive">{err}</p>}
      {st && <NodeRings st={st} />}
      <NodeSummary cluster={cluster} node={node} guests={guests} onGuest={onGuest} tasks={tasks} />
    </div>
  );
}

// NodeHeader is the node's name, state and identity, with actions on the
// right.
export function NodeHeader({ node, st, online, actions }: { node: string; st: PVENodeStatus | null; online: boolean; actions?: ReactNode }) {
  const facts = st
    ? ([
        st.pveversion && ["Proxmox VE", st.pveversion.replace(/^pve-manager\//, "").split("/")[0]],
        st.kversion && ["Kernel", st.kversion.split(" ").slice(0, 3).join(" ").replace(/^Linux /, "")],
        st.cpuinfo?.model && [
          "CPU",
          `${st.cpuinfo.model}${st.cpuinfo.sockets ? ` · ${st.cpuinfo.sockets} socket${st.cpuinfo.sockets > 1 ? "s" : ""}` : ""}${st.cpuinfo.cpus ? ` · ${st.cpuinfo.cpus} threads` : ""}`,
        ],
        st["boot-info"]?.mode && ["Boot", st["boot-info"].mode.toUpperCase()],
      ].filter(Boolean) as [string, string][])
    : [];
  return (
    <Card className="relative overflow-hidden p-5">
      <div className="pointer-events-none absolute inset-0 bg-gradient-to-br from-brand/10 via-transparent to-transparent" />
      <div className="relative flex flex-wrap items-start justify-between gap-4">
        <div className="flex min-w-0 items-start gap-3">
          <div className="grid size-11 shrink-0 place-items-center rounded-xl border border-brand/30 bg-brand/10 text-brand">
            <Server className="size-5" />
          </div>
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <h2 className="text-xl font-semibold tracking-tight">{node}</h2>
              <Badge variant={online ? "success" : "destructive"} className="gap-1.5">
                <span className="size-1.5 rounded-full bg-current" /> {online ? "online" : "offline"}
              </Badge>
              {st?.uptime ? <span className="text-sm text-muted-foreground">up {formatUptime(st.uptime)}</span> : null}
            </div>
            {facts.length > 0 && (
              <dl className="mt-2 grid gap-x-6 gap-y-0.5 text-xs sm:grid-cols-2">
                {facts.map(([k, v]) => (
                  <div key={k} className="flex min-w-0 gap-2">
                    <dt className="w-20 shrink-0 text-muted-foreground">{k}</dt>
                    <dd className="truncate" title={v}>
                      {v}
                    </dd>
                  </div>
                ))}
              </dl>
            )}
          </div>
        </div>
        {actions && <div className="flex flex-wrap gap-2">{actions}</div>}
      </div>
    </Card>
  );
}

// NodeRings shows CPU, memory, swap and root disk use as rings.
export function NodeRings({ st }: { st: PVENodeStatus }) {
  const rings = [
    {
      label: "CPU",
      v: (st.cpu ?? 0) * 100,
      sub: `${st.cpuinfo?.cpus ?? "?"} threads`,
      extra: st.loadavg ? `load ${st.loadavg.slice(0, 3).join(" ")}` : st.wait ? `IO wait ${((st.wait ?? 0) * 100).toFixed(1)}%` : "",
    },
    st.memory && { label: "Memory", v: pct(st.memory.used, st.memory.total), sub: `${formatBytes(st.memory.used)} of ${formatBytes(st.memory.total)}`, extra: "" },
    st.rootfs && { label: "Root disk", v: pct(st.rootfs.used, st.rootfs.total), sub: `${formatBytes(st.rootfs.used)} of ${formatBytes(st.rootfs.total)}`, extra: "" },
    st.swap && st.swap.total > 0 && { label: "Swap", v: pct(st.swap.used, st.swap.total), sub: `${formatBytes(st.swap.used)} of ${formatBytes(st.swap.total)}`, extra: "" },
  ].filter(Boolean) as { label: string; v: number; sub: string; extra: string }[];
  return (
    <div className="grid grid-cols-2 gap-3 xl:grid-cols-4">
      {rings.map((r) => (
        <Card key={r.label} className="flex items-center gap-3 p-4">
          <Ring v={r.v} />
          <div className="min-w-0">
            <div className="text-xs font-semibold tracking-wider text-muted-foreground uppercase">{r.label}</div>
            <div className="truncate text-sm">{r.sub}</div>
            {r.extra && <div className="truncate text-xs text-muted-foreground">{r.extra}</div>}
          </div>
        </Card>
      ))}
    </div>
  );
}

function Ring({ v }: { v: number }) {
  const r = 26;
  const c = 2 * Math.PI * r;
  const w = Math.max(0, Math.min(100, v));
  const color = w > 90 ? "text-destructive" : w > 75 ? "text-warning" : "text-brand";
  return (
    <div className="relative size-16 shrink-0">
      <svg viewBox="0 0 64 64" className="size-full -rotate-90" aria-hidden>
        <circle cx="32" cy="32" r={r} fill="none" strokeWidth="7" className="stroke-muted" />
        <circle
          cx="32"
          cy="32"
          r={r}
          fill="none"
          strokeWidth="7"
          strokeLinecap="round"
          stroke="currentColor"
          className={`${color} transition-[stroke-dashoffset] duration-700`}
          strokeDasharray={c}
          strokeDashoffset={c * (1 - w / 100)}
        />
      </svg>
      <span className="tabular absolute inset-0 grid place-items-center text-sm font-bold">{Math.round(w)}%</span>
    </div>
  );
}

// NodeSummary is the node's performance next to its guests and recent tasks.
export function NodeSummary({
  cluster,
  node,
  guests,
  onGuest,
  tasks,
}: {
  cluster: string;
  node: string;
  guests: PVEGuest[];
  onGuest?: (g: PVEGuest) => void;
  tasks: NodeTask[];
}) {
  return (
    <div className="grid gap-4 2xl:grid-cols-[minmax(0,1fr)_22rem]">
      <NodePerformance cluster={cluster} node={node} />
      <div className="grid content-start gap-4">
        <NodeGuests guests={guests} onGuest={onGuest} />
        <NodeRecentTasks tasks={tasks} />
      </div>
    </div>
  );
}

export function NodePerformance({ cluster, node }: { cluster: string; node: string }) {
  const [tf, setTf] = useState<PVETimeframe>("hour");
  const [rrd, setRrd] = useState<PVENodeRRDPoint[]>([]);
  useEffect(() => {
    const get = () =>
      api
        .pveNodeRRD(cluster, node, tf)
        .then(setRrd)
        .catch(() => setRrd([]));
    void get();
    const t = setInterval(get, tf === "hour" ? 60000 : 300000);
    return () => clearInterval(t);
  }, [cluster, node, tf]);
  const charts = useMemo(() => {
    const col = (k: keyof PVENodeRRDPoint, f = (v: number) => v) => rrd.map((p) => (typeof p[k] === "number" ? f(p[k] as number) : null));
    return {
      times: rrd.map((p) => p.time),
      cpu: col("cpu", (v) => v * 100),
      io: col("iowait", (v) => v * 100),
      mem: col("memused"),
      memtotal: col("memtotal"),
      load: col("loadavg"),
      netin: col("netin"),
      netout: col("netout"),
    };
  }, [rrd]);
  return (
    <Card className="p-4">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <h3 className="font-semibold">Performance</h3>
        <div className="flex gap-1 rounded-lg border p-0.5" role="group" aria-label="Time range">
          {timeframes.map((t) => (
            <button
              key={t.tf}
              className={`cursor-pointer rounded-md px-2.5 py-1 text-sm ${tf === t.tf ? "bg-accent text-foreground" : "text-muted-foreground hover:text-foreground"}`}
              onClick={() => setTf(t.tf)}
              aria-pressed={tf === t.tf}
            >
              {t.label}
            </button>
          ))}
        </div>
      </div>
      {rrd.length === 0 ? (
        <p className="text-sm text-muted-foreground">No performance data yet.</p>
      ) : (
        <div className="grid gap-4 lg:grid-cols-2">
          <HistoryChart
            title="CPU"
            times={charts.times}
            max={100}
            format={(v) => `${v.toFixed(1)}%`}
            series={[
              { label: "CPU", color: "var(--color-brand)", values: charts.cpu },
              { label: "IO wait", color: "#ef4444", values: charts.io },
            ]}
          />
          <HistoryChart
            title="Memory"
            times={charts.times}
            format={formatBytes}
            series={[
              { label: "used", color: "#a855f7", values: charts.mem },
              { label: "total", color: "#94a3b8", values: charts.memtotal },
            ]}
          />
          <HistoryChart
            title="Network"
            times={charts.times}
            format={rate}
            series={[
              { label: "in", color: "#22c55e", values: charts.netin },
              { label: "out", color: "#3b82f6", values: charts.netout },
            ]}
          />
          <HistoryChart title="Load average" times={charts.times} format={(v) => v.toFixed(2)} series={[{ label: "load", color: "#f59e0b", values: charts.load }]} />
        </div>
      )}
    </Card>
  );
}

// NodeGuests lists the node's guests, running first; picking one opens it.
export function NodeGuests({ guests, onGuest, limit = 12 }: { guests: PVEGuest[]; onGuest?: (g: PVEGuest) => void; limit?: number }) {
  const list = [...guests].sort((a, b) => (a.template ?? 0) - (b.template ?? 0) || Number(b.status === "running") - Number(a.status === "running") || b.mem - a.mem);
  const running = guests.filter((g) => g.status === "running" && !g.template).length;
  const shown = limit ? list.slice(0, limit) : list;
  return (
    <Card className="p-4">
      <div className="mb-2 flex items-baseline justify-between gap-2">
        <h3 className="font-semibold">Guests</h3>
        <span className="text-xs text-muted-foreground">
          {running} running · {guests.filter((g) => !g.template).length} total
        </span>
      </div>
      {guests.length === 0 ? (
        <p className="text-sm text-muted-foreground">No guests on this node.</p>
      ) : (
        <ul className="grid gap-0.5 text-sm">
          {shown.map((g) => {
            const Icon = g.template ? LayoutTemplate : g.type === "lxc" ? Box : Monitor;
            const on = g.status === "running";
            return (
              <li key={g.id}>
                <button
                  className="grid w-full cursor-pointer grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-2 rounded-md px-2 py-1.5 text-left hover:bg-accent disabled:cursor-default disabled:hover:bg-transparent"
                  onClick={() => onGuest?.(g)}
                  disabled={!onGuest}
                >
                  <Icon className="size-3.5 text-muted-foreground" />
                  <span className="flex min-w-0 items-center gap-2">
                    <span className="truncate">{g.name || g.vmid}</span>
                    {!g.template && <span className={`size-1.5 shrink-0 rounded-full ${dot(g.status)}`} />}
                    <span className="font-mono text-[11px] text-muted-foreground/70">{g.vmid}</span>
                  </span>
                  <span className="tabular flex items-center gap-2 text-xs text-muted-foreground">
                    {on ? (
                      <>
                        <span className="inline-flex items-center gap-0.5">
                          <Cpu className="size-3" />
                          {Math.round(g.cpu * 100)}%
                        </span>
                        <span>{formatBytes(g.mem)}</span>
                      </>
                    ) : (
                      <span>{g.template ? "template" : g.status}</span>
                    )}
                  </span>
                </button>
              </li>
            );
          })}
          {limit > 0 && list.length > limit && <li className="px-2 pt-1 text-xs text-muted-foreground">and {list.length - limit} more in the tree</li>}
        </ul>
      )}
    </Card>
  );
}

export function NodeRecentTasks({ tasks, limit = 8 }: { tasks: NodeTask[]; limit?: number }) {
  return (
    <Card className="p-4">
      <h3 className="mb-2 font-semibold">Recent tasks</h3>
      {tasks.length === 0 ? (
        <p className="text-sm text-muted-foreground">No tasks.</p>
      ) : (
        <ul className="grid gap-1.5 text-sm">
          {tasks.slice(0, limit).map((t) => (
            <li key={t.upid} className="flex items-center justify-between gap-2">
              <span className="min-w-0">
                <span className="block truncate">
                  {t.type}
                  {t.id ? <span className="text-muted-foreground"> · {t.id}</span> : null}
                </span>
                <span className="block truncate text-xs text-muted-foreground">
                  {timeAgo(new Date(t.starttime * 1000).toISOString())} · {t.user}
                </span>
              </span>
              <Badge variant={!t.status ? "warning" : t.status === "OK" ? "success" : t.status.startsWith("WARNINGS") ? "warning" : "destructive"} className="shrink-0">
                {t.status || "running"}
              </Badge>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}
