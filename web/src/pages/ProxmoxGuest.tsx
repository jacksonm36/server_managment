import { useCallback, useEffect, useMemo, useState } from "react";
import { Link } from "wouter";
import { ArrowLeft, Camera, Pause, Play, Power, RefreshCw, RotateCw, Square } from "lucide-react";
import { api, type PVEGuestDetail, type PVEGuestRef, type PVERRDPoint, type PVETask, type PVETimeframe } from "@/lib/api";
import { useAccess } from "@/lib/access";
import { formatBytes, formatUptime } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { HistoryChart } from "@/components/HistoryChart";
import { Meter } from "@/components/LiveChart";
import { SnapshotsDialog } from "@/pages/Proxmox";

const timeframes: { tf: PVETimeframe; label: string }[] = [
  { tf: "hour", label: "Hour" },
  { tf: "day", label: "Day" },
  { tf: "week", label: "Week" },
  { tf: "month", label: "Month" },
  { tf: "year", label: "Year" },
];

// Config keys that describe virtual hardware; everything else is an option.
const hardwareKey = /^(cores|sockets|cpu|cpulimit|cpuunits|vcpus|memory|balloon|swap|bios|machine|scsihw|vga|arch|rootfs|efidisk\d|tpmstate\d|(scsi|virtio|sata|ide|net|mp|unused|usb|hostpci|serial)\d+)$/;
const hiddenKey = new Set(["digest", "description", "meta", "vmgenid", "smbios1"]);

const rate = (v: number) => `${formatBytes(v)}/s`;
const took = (s: number) => (s < 60 ? `${s}s` : formatUptime(s));

// Proxmox stores sshkeys URL-encoded.
function shown(k: string, v: string | number) {
  if (k !== "sshkeys") return String(v);
  try {
    return decodeURIComponent(String(v));
  } catch {
    return String(v);
  }
}

export function ProxmoxGuest({ cluster, node, type, vmid }: { cluster: string; node: string; type: string; vmid: string }) {
  const ac = useAccess();
  const ref = useMemo<PVEGuestRef>(() => ({ node, type: type === "lxc" ? "lxc" : "qemu", vmid: Number(vmid) }), [node, type, vmid]);
  const [g, setG] = useState<PVEGuestDetail | null>(null);
  const [err, setErr] = useState("");
  const [tf, setTf] = useState<PVETimeframe>("hour");
  const [rrd, setRrd] = useState<PVERRDPoint[]>([]);
  const [tasks, setTasks] = useState<PVETask[]>([]);
  const [busy, setBusy] = useState("");
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [snaps, setSnaps] = useState(false);
  const [log, setLog] = useState<PVETask | null>(null);

  const load = useCallback(
    () =>
      api
        .pveGuest(cluster, ref)
        .then((d) => {
          setG(d);
          setErr("");
        })
        .catch((e) => setErr((e as Error).message)),
    [cluster, ref],
  );
  const loadTasks = useCallback(() => api.pveGuestTasks(cluster, ref).then(setTasks).catch(() => {}), [cluster, ref]);
  useEffect(() => {
    void load();
    void loadTasks();
    const t = setInterval(load, 5000);
    const u = setInterval(loadTasks, 15000);
    return () => {
      clearInterval(t);
      clearInterval(u);
    };
  }, [load, loadTasks]);

  useEffect(() => {
    const get = () => api.pveGuestRRD(cluster, ref, tf).then(setRrd).catch(() => setRrd([]));
    void get();
    const t = setInterval(get, tf === "hour" ? 60000 : 300000);
    return () => clearInterval(t);
  }, [cluster, ref, tf]);

  async function power(action: string) {
    setBusy(action);
    setMsg(null);
    try {
      await api.pvePower(cluster, ref, action);
      setMsg({ ok: true, text: `${action} done.` });
    } catch (e) {
      setMsg({ ok: false, text: (e as Error).message });
    } finally {
      setBusy("");
      void load();
      void loadTasks();
    }
  }

  const charts = useMemo(() => {
    const times = rrd.map((p) => p.time);
    const col = (k: keyof PVERRDPoint, f = (v: number) => v) => rrd.map((p) => (typeof p[k] === "number" ? f(p[k] as number) : null));
    return { times, cpu: col("cpu", (v) => v * 100), mem: col("mem"), maxmem: col("maxmem"), netin: col("netin"), netout: col("netout"), dr: col("diskread"), dw: col("diskwrite") };
  }, [rrd]);

  const back = (
    <Link href="/proxmox" className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
      <ArrowLeft className="size-4" /> Proxmox
    </Link>
  );
  if (!g)
    return (
      <div className="grid gap-4">
        {back}
        {err ? <p className="text-sm text-destructive">{err}</p> : <p className="text-sm text-muted-foreground">Loading…</p>}
      </div>
    );

  const st = g.status;
  const name = String(g.config.name ?? g.config.hostname ?? st.name ?? `${g.type} ${g.vmid}`);
  const running = st.status === "running";
  const paused = st.qmpstatus === "paused";
  const cpuPct = (st.cpu ?? 0) * 100;
  const memPct = st.maxmem ? ((st.mem ?? 0) / st.maxmem) * 100 : 0;
  const hw = Object.entries(g.config).filter(([k]) => hardwareKey.test(k)).sort(([a], [b]) => a.localeCompare(b, undefined, { numeric: true }));
  const opts = Object.entries(g.config).filter(([k]) => !hardwareKey.test(k) && !hiddenKey.has(k)).sort(([a], [b]) => a.localeCompare(b));
  const desc = typeof g.config.description === "string" ? g.config.description.trim() : "";

  return (
    <div className="grid gap-6">
      {back}
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <h1 className="text-2xl font-semibold tracking-tight">{name}</h1>
            <Badge variant="outline">{g.type === "qemu" ? "VM" : "CT"} {g.vmid}</Badge>
            <Badge variant={running && !paused ? "success" : st.status === "stopped" ? "outline" : "warning"}>{paused ? "paused" : st.status}</Badge>
            {st.lock && <Badge variant="warning">locked: {st.lock}</Badge>}
          </div>
          <p className="mt-1 text-sm text-muted-foreground">
            on {g.node}
            {running && st.uptime ? ` · up ${formatUptime(st.uptime)}` : ""}
            {g.ips?.length ? ` · ${g.ips.join(", ")}` : ""}
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          {ac.can("proxmox.operate") && (
            <>
              {(!running || paused) && (
                <Button size="sm" variant="brand" disabled={!!busy} onClick={() => power(paused ? "resume" : "start")}>
                  <Play /> {paused ? "Resume" : "Start"}
                </Button>
              )}
              {running && !paused && (
                <>
                  <Button size="sm" variant="secondary" disabled={!!busy} onClick={() => power("shutdown")}>
                    <Power /> Shut down
                  </Button>
                  <Button size="sm" variant="secondary" disabled={!!busy} onClick={() => power("reboot")}>
                    <RotateCw /> Reboot
                  </Button>
                  {g.type === "qemu" && (
                    <Button size="sm" variant="secondary" disabled={!!busy} onClick={() => power("suspend")}>
                      <Pause /> Suspend
                    </Button>
                  )}
                </>
              )}
              {running && (
                <Button
                  size="sm"
                  variant="ghost"
                  className="text-destructive"
                  disabled={!!busy}
                  onClick={() => confirm(`Hard-stop ${name}? Unsaved data in the guest is lost.`) && power("stop")}
                >
                  <Square /> Stop
                </Button>
              )}
              <Button size="sm" variant="secondary" onClick={() => setSnaps(true)}>
                <Camera /> Snapshots
              </Button>
            </>
          )}
        </div>
      </div>
      {busy && <p className="text-sm text-muted-foreground">{busy}…</p>}
      {msg && <p className={`text-sm ${msg.ok ? "text-success" : "text-destructive"}`}>{msg.text}</p>}
      {err && <p className="text-sm text-destructive">Refresh failed: {err}</p>}

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Stat label="CPU" value={running ? `${cpuPct.toFixed(1)}%` : "—"} sub={`${st.cpus ?? g.config.cores ?? "?"} vCPU`} meter={running ? cpuPct : undefined} />
        <Stat label="Memory" value={running ? formatBytes(st.mem ?? 0) : "—"} sub={`of ${formatBytes(st.maxmem ?? 0)}`} meter={running ? memPct : undefined} />
        <Stat
          label="Disk"
          value={formatBytes(st.maxdisk ?? 0)}
          sub={g.type === "lxc" && st.maxdisk ? `${formatBytes(st.disk ?? 0)} used` : "allocated"}
          meter={g.type === "lxc" && st.maxdisk ? ((st.disk ?? 0) / st.maxdisk) * 100 : undefined}
        />
        <Stat label="Network" value={`↓ ${formatBytes(st.netin ?? 0)}`} sub={`↑ ${formatBytes(st.netout ?? 0)} since start`} />
      </div>

      <div className="grid gap-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-lg font-semibold">Performance</h2>
          <div className="flex gap-1 rounded-lg border p-0.5" role="group" aria-label="Time range">
            {timeframes.map((t) => (
              <button
                key={t.tf}
                className={`rounded-md px-2.5 py-1 text-sm ${tf === t.tf ? "bg-accent text-foreground" : "text-muted-foreground hover:text-foreground"}`}
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
            <HistoryChart title="CPU" times={charts.times} max={100} format={(v) => `${v.toFixed(1)}%`} series={[{ label: "CPU", color: "var(--color-brand)", values: charts.cpu }]} />
            <HistoryChart
              title="Memory"
              times={charts.times}
              format={formatBytes}
              series={[
                { label: "used", color: "#a855f7", values: charts.mem },
                { label: "total", color: "#94a3b8", values: charts.maxmem },
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
            <HistoryChart
              title="Disk I/O"
              times={charts.times}
              format={rate}
              series={[
                { label: "read", color: "#f59e0b", values: charts.dr },
                { label: "write", color: "#ef4444", values: charts.dw },
              ]}
            />
          </div>
        )}
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        <ConfigCard title="Hardware" rows={hw} />
        <ConfigCard title="Options" rows={opts} />
      </div>
      {desc && (
        <Card>
          <CardHeader>
            <CardTitle>Notes</CardTitle>
          </CardHeader>
          <CardContent>
            <pre className="text-sm whitespace-pre-wrap">{desc}</pre>
          </CardContent>
        </Card>
      )}

      <Card>
        <CardHeader className="flex-row items-center justify-between">
          <div>
            <CardTitle>Tasks</CardTitle>
            <CardDescription>Recent Proxmox tasks on this guest.</CardDescription>
          </div>
          <Button size="sm" variant="ghost" onClick={() => void loadTasks()} aria-label="Refresh tasks">
            <RefreshCw />
          </Button>
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
                    <th className="py-2 pr-3 font-normal">Took</th>
                    <th className="py-2 font-normal">Result</th>
                  </tr>
                </thead>
                <tbody className="divide-y">
                  {tasks.map((t) => (
                    <tr key={t.upid} className="cursor-pointer hover:bg-accent/40" onClick={() => setLog(t)}>
                      <td className="py-2 pr-3 whitespace-nowrap tabular">{new Date(t.starttime * 1000).toLocaleString()}</td>
                      <td className="py-2 pr-3">{t.type}</td>
                      <td className="hidden py-2 pr-3 text-xs text-muted-foreground sm:table-cell">{t.user}</td>
                      <td className="py-2 pr-3 tabular text-muted-foreground">{t.endtime ? took(Math.max(1, t.endtime - t.starttime)) : "…"}</td>
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

      {snaps && <SnapshotsDialog cluster={{ id: cluster, name: "" }} guest={{ ...ref, name }} ac={ac} onClose={() => setSnaps(false)} />}
      {log && <TaskLogDialog cluster={cluster} guest={ref} task={log} onClose={() => setLog(null)} />}
    </div>
  );
}

function Stat({ label, value, sub, meter }: { label: string; value: string; sub: string; meter?: number }) {
  return (
    <Card className="p-4">
      <div className="text-sm text-muted-foreground">{label}</div>
      <div className="tabular mt-1 text-xl font-semibold">{value}</div>
      <div className="text-xs text-muted-foreground">{sub}</div>
      {meter !== undefined && <Meter value={meter} className="mt-2" />}
    </Card>
  );
}

function ConfigCard({ title, rows }: { title: string; rows: [string, string | number][] }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>{title}</CardTitle>
      </CardHeader>
      <CardContent>
        {rows.length === 0 ? (
          <p className="text-sm text-muted-foreground">Nothing set.</p>
        ) : (
          <dl className="divide-y text-sm">
            {rows.map(([k, v]) => (
              <div key={k} className="grid grid-cols-[8rem_1fr] gap-3 py-1.5">
                <dt className="text-muted-foreground">{k}</dt>
                <dd className="min-w-0 font-mono text-xs break-all whitespace-pre-wrap">{shown(k, v)}</dd>
              </div>
            ))}
          </dl>
        )}
      </CardContent>
    </Card>
  );
}

function TaskLogDialog({ cluster, guest, task, onClose }: { cluster: string; guest: PVEGuestRef; task: PVETask; onClose: () => void }) {
  const [log, setLog] = useState<string | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => {
    api
      .pveTaskLog(cluster, guest, task.upid)
      .then((l) => setLog(l.log))
      .catch((e) => setErr((e as Error).message));
  }, [cluster, guest, task.upid]);
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-3xl">
        <DialogHeader>
          <DialogTitle>
            {task.type} · {new Date(task.starttime * 1000).toLocaleString()}
          </DialogTitle>
        </DialogHeader>
        {err && <p className="text-sm text-destructive">{err}</p>}
        <pre className="max-h-[60vh] overflow-auto rounded-md bg-muted p-3 font-mono text-xs whitespace-pre-wrap">{log ?? "Loading…"}</pre>
      </DialogContent>
    </Dialog>
  );
}
