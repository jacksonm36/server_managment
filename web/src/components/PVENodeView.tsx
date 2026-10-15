import { useEffect, useMemo, useState } from "react";
import { api, type PVENodeRRDPoint, type PVENodeStatus, type PVETask, type PVETimeframe } from "@/lib/api";
import { formatBytes, formatUptime, pct } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { HistoryChart } from "@/components/HistoryChart";

// PVENodeView shows one node: what it runs, how loaded it is now, its
// history and its recent tasks.

const timeframes: { tf: PVETimeframe; label: string }[] = [
  { tf: "hour", label: "Hour" },
  { tf: "day", label: "Day" },
  { tf: "week", label: "Week" },
  { tf: "month", label: "Month" },
];
const rate = (v: number) => `${formatBytes(v)}/s`;

export function PVENodeView({ cluster, node, online }: { cluster: string; node: string; online: boolean }) {
  const [st, setSt] = useState<PVENodeStatus | null>(null);
  const [err, setErr] = useState("");
  const [tf, setTf] = useState<PVETimeframe>("hour");
  const [rrd, setRrd] = useState<PVENodeRRDPoint[]>([]);
  const [tasks, setTasks] = useState<(PVETask & { id?: string })[]>([]);

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
  useEffect(() => {
    if (!online) return;
    const get = () =>
      api
        .pveNodeRRD(cluster, node, tf)
        .then(setRrd)
        .catch(() => setRrd([]));
    void get();
    const t = setInterval(get, tf === "hour" ? 60000 : 300000);
    return () => clearInterval(t);
  }, [cluster, node, tf, online]);

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

  if (!online) return <Card className="p-6 text-sm text-muted-foreground">{node} is offline.</Card>;
  if (err) return <p className="text-sm text-destructive">{err}</p>;
  if (!st) return <p className="text-sm text-muted-foreground">Loading {node}…</p>;

  const meters = [
    { label: "CPU", v: (st.cpu ?? 0) * 100, sub: `${st.cpuinfo?.cpus ?? "?"} threads${st.wait ? ` · IO wait ${((st.wait ?? 0) * 100).toFixed(1)}%` : ""}` },
    st.memory && { label: "Memory", v: pct(st.memory.used, st.memory.total), sub: `${formatBytes(st.memory.used)} of ${formatBytes(st.memory.total)}` },
    st.swap && st.swap.total > 0 && { label: "Swap", v: pct(st.swap.used, st.swap.total), sub: `${formatBytes(st.swap.used)} of ${formatBytes(st.swap.total)}` },
    st.rootfs && { label: "Root disk", v: pct(st.rootfs.used, st.rootfs.total), sub: `${formatBytes(st.rootfs.used)} of ${formatBytes(st.rootfs.total)}` },
  ].filter(Boolean) as { label: string; v: number; sub: string }[];

  return (
    <div className="grid gap-4">
      <Card className="p-5">
        <div className="flex flex-wrap items-center gap-2">
          <h2 className="text-lg font-semibold">{node}</h2>
          <Badge variant="success" className="gap-1.5">
            <span className="size-1.5 rounded-full bg-current" /> online
          </Badge>
          {st.uptime ? <span className="text-sm text-muted-foreground">up {formatUptime(st.uptime)}</span> : null}
        </div>
        <dl className="mt-3 grid gap-x-6 gap-y-1 text-sm sm:grid-cols-2">
          {st.cpuinfo?.model && <Info k="CPU" v={`${st.cpuinfo.model}${st.cpuinfo.sockets ? ` · ${st.cpuinfo.sockets} socket${st.cpuinfo.sockets > 1 ? "s" : ""}` : ""}`} />}
          {st.pveversion && <Info k="Proxmox" v={st.pveversion.replace(/^pve-manager\//, "")} />}
          {st.kversion && <Info k="Kernel" v={st.kversion.split(" ").slice(0, 2).join(" ")} />}
          {st.loadavg && <Info k="Load" v={st.loadavg.join(" · ")} />}
          {st["boot-info"]?.mode && <Info k="Boot" v={st["boot-info"].mode.toUpperCase()} />}
        </dl>
      </Card>

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        {meters.map((m) => (
          <Card key={m.label} className="p-4">
            <div className="text-xs font-semibold tracking-wider text-muted-foreground uppercase">{m.label}</div>
            <div className="tabular mt-1 text-2xl font-bold">{Math.round(m.v)}%</div>
            <div className="mt-2 h-1.5 rounded-full bg-muted">
              <div className={`h-1.5 rounded-full ${m.v > 90 ? "bg-destructive" : m.v > 75 ? "bg-warning" : "bg-brand"}`} style={{ width: `${Math.min(100, m.v)}%` }} />
            </div>
            <div className="mt-1.5 truncate text-xs text-muted-foreground">{m.sub}</div>
          </Card>
        ))}
      </div>

      <div className="flex flex-wrap items-center justify-between gap-2">
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

      <Card>
        <CardHeader>
          <CardTitle>Recent tasks on {node}</CardTitle>
        </CardHeader>
        <CardContent>
          {tasks.length === 0 ? (
            <p className="text-sm text-muted-foreground">No tasks.</p>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead className="text-left text-xs text-muted-foreground">
                  <tr>
                    <th className="py-2 pr-3 font-normal">Started</th>
                    <th className="py-2 pr-3 font-normal">Task</th>
                    <th className="hidden py-2 pr-3 font-normal sm:table-cell">By</th>
                    <th className="py-2 font-normal">Result</th>
                  </tr>
                </thead>
                <tbody className="divide-y">
                  {tasks.slice(0, 15).map((t) => (
                    <tr key={t.upid}>
                      <td className="tabular py-2 pr-3 whitespace-nowrap">{new Date(t.starttime * 1000).toLocaleString()}</td>
                      <td className="py-2 pr-3">
                        {t.type}
                        {t.id ? <span className="text-muted-foreground"> · {t.id}</span> : null}
                      </td>
                      <td className="hidden py-2 pr-3 text-xs text-muted-foreground sm:table-cell">{t.user}</td>
                      <td className="py-2">
                        <Badge variant={!t.status ? "warning" : t.status === "OK" ? "success" : t.status.startsWith("WARNINGS") ? "warning" : "destructive"}>
                          {t.status || "running"}
                        </Badge>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

function Info({ k, v }: { k: string; v: string }) {
  return (
    <div className="flex min-w-0 gap-2">
      <dt className="w-16 shrink-0 text-muted-foreground">{k}</dt>
      <dd className="truncate" title={v}>
        {v}
      </dd>
    </div>
  );
}
