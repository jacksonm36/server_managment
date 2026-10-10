import { useEffect, useMemo, useState } from "react";
import { Link } from "wouter";
import { AlertTriangle, ArrowRight, BellRing, CheckCircle2, Cpu, HardDrive, MemoryStick, Server as ServerIcon, ServerOff, XCircle } from "lucide-react";
import { api, type AlertEvent, type AuditEntry, type JobSummary, type Role, type Server } from "@/lib/api";
import { useLive } from "@/lib/live";
import { outdated, useAgentBuild } from "@/lib/agentBuild";
import { pct, timeAgo } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Meter } from "@/components/LiveChart";
import { AddServerDialog } from "@/components/AddServerDialog";

function Tile({ icon, label, value, sub, tone = "default", href }: { icon: React.ReactNode; label: string; value: string | number; sub?: string; tone?: "default" | "good" | "warn" | "bad"; href?: string }) {
  const toneCls = { default: "text-brand bg-brand/10", good: "text-success bg-success/10", warn: "text-warning bg-warning/10", bad: "text-destructive bg-destructive/10" }[tone];
  const body = (
    <Card className="flex h-full items-center gap-4 p-4 transition-colors hover:bg-accent/30">
      <span className={`flex size-10 shrink-0 items-center justify-center rounded-lg [&_svg]:size-5 ${toneCls}`}>{icon}</span>
      <div className="min-w-0">
        <div className="text-xs text-muted-foreground">{label}</div>
        <div className="tabular text-2xl font-semibold leading-tight">{value}</div>
        {sub && <div className="truncate text-xs text-muted-foreground">{sub}</div>}
      </div>
    </Card>
  );
  return href ? (
    <Link href={href} className="block rounded-xl outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50">
      {body}
    </Link>
  ) : (
    body
  );
}

const worst = (s: Server) => (s.latest ? Math.max(s.latest.cpu, pct(s.latest.mem_used, s.latest.mem_total), pct(s.latest.disk_used, s.latest.disk_total)) : -1);

export function Dashboard({ role }: { role: Role }) {
  const map = useLive((s) => s.servers);
  const jobTick = useLive((s) => s.jobTick);
  const servers = useMemo(() => [...map.values()], [map]);
  const build = useAgentBuild();
  const [alerts, setAlerts] = useState<AlertEvent[]>([]);
  const [jobs, setJobs] = useState<JobSummary[]>([]);
  const [audit, setAudit] = useState<AuditEntry[] | null>(null);

  useEffect(() => {
    const load = () => api.firingAlerts().then(setAlerts).catch(() => {});
    void load();
    const t = setInterval(load, 15000);
    return () => clearInterval(t);
  }, []);
  useEffect(() => {
    void api.recentJobs().then(setJobs).catch(() => {});
  }, [jobTick]);
  useEffect(() => {
    if (role !== "viewer") void api.audit({ limit: 8 }).then(setAudit).catch(() => setAudit(null));
  }, [role]);

  const online = servers.filter((s) => s.online);
  const offline = servers.filter((s) => !s.online);
  const stale = servers.filter((s) => outdated(build, s.agent_version));
  const dayAgo = Date.now() - 86400_000;
  const failed = jobs.filter((j) => j.status === "failed" && new Date(j.created_at).getTime() > dayAgo);
  const busiest = [...online].sort((a, b) => worst(b) - worst(a)).slice(0, 6);

  if (servers.length === 0) {
    return (
      <div className="grid gap-6">
        <h1 className="text-2xl font-semibold tracking-tight">Dashboard</h1>
        <Card className="flex flex-col items-center gap-3 p-12 text-center">
          <ServerIcon className="size-8 text-brand" />
          <div className="text-lg font-medium">Welcome to servermgr</div>
          <p className="max-w-md text-sm text-muted-foreground">
            Add your first server: you get a one-line install command, the agent connects out to this panel, and the server shows up here with live stats.
          </p>
          {role === "admin" && <AddServerDialog />}
        </Card>
      </div>
    );
  }

  return (
    <div className="grid gap-6">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Dashboard</h1>
          <p className="text-sm text-muted-foreground">Fleet health at a glance.</p>
        </div>
        <Link href="/servers" className="flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
          All servers <ArrowRight className="size-4" />
        </Link>
      </div>

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Tile icon={<ServerIcon />} label="Servers online" value={`${online.length}/${servers.length}`} sub={offline.length ? `${offline.length} offline` : "all connected"} tone={offline.length ? "warn" : "good"} href="/servers" />
        <Tile icon={<BellRing />} label="Alerts firing" value={alerts.length} sub={alerts.length ? alerts[0].rule_name : "all clear"} tone={alerts.length ? "bad" : "good"} href="/automation#alerts" />
        <Tile icon={<XCircle />} label="Failed tasks (24 h)" value={failed.length} sub={failed.length ? `${failed[0].op} on ${failed[0].server_name}` : "none"} tone={failed.length ? "warn" : "good"} href="/activity" />
        <Tile
          icon={<AlertTriangle />}
          label="Agents to update"
          value={stale.length}
          sub={stale.length ? `newest is ${build?.version}` : "all up to date"}
          tone={stale.length ? "warn" : "good"}
          href="/servers"
        />
      </div>

      <div className="grid gap-4 lg:grid-cols-3">
        <Card className="lg:col-span-2">
          <CardHeader>
            <CardTitle>Busiest servers</CardTitle>
          </CardHeader>
          <CardContent className="grid gap-3">
            {busiest.length === 0 && <p className="text-sm text-muted-foreground">No servers online.</p>}
            {busiest.map((s) => {
              const m = s.latest;
              return (
                <Link key={s.id} href={`/servers/${s.id}`} className="grid gap-2 rounded-lg border p-3 transition-colors hover:bg-accent/30 sm:grid-cols-[10rem_1fr_1fr_1fr] sm:items-center sm:gap-4">
                  <div className="min-w-0">
                    <div className="truncate font-medium">{s.name}</div>
                    <div className="truncate text-xs text-muted-foreground">{s.os_pretty}</div>
                  </div>
                  {m ? (
                    [
                      { icon: <Cpu />, label: "CPU", v: m.cpu },
                      { icon: <MemoryStick />, label: "RAM", v: pct(m.mem_used, m.mem_total) },
                      { icon: <HardDrive />, label: "Disk", v: pct(m.disk_used, m.disk_total) },
                    ].map((x) => (
                      <div key={x.label} className="grid gap-1">
                        <div className="flex items-center justify-between text-xs text-muted-foreground [&_svg]:size-3.5">
                          <span className="flex items-center gap-1">
                            {x.icon}
                            {x.label}
                          </span>
                          <span className="tabular text-foreground">{x.v.toFixed(0)}%</span>
                        </div>
                        <Meter value={x.v} />
                      </div>
                    ))
                  ) : (
                    <span className="text-xs text-muted-foreground sm:col-span-3">Waiting for metrics…</span>
                  )}
                </Link>
              );
            })}
          </CardContent>
        </Card>

        <div className="grid content-start gap-4">
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                {alerts.length ? <BellRing className="size-4 text-destructive" /> : <CheckCircle2 className="size-4 text-success" />} Alerts
              </CardTitle>
            </CardHeader>
            <CardContent className="grid gap-2">
              {alerts.length === 0 && <p className="text-sm text-muted-foreground">Nothing is firing.</p>}
              {alerts.slice(0, 5).map((a) => (
                <Link key={a.id} href="/automation#alerts" className="flex items-center justify-between gap-2 rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm">
                  <span className="min-w-0 truncate">
                    {a.rule_name} · <span className="font-medium">{a.server_name}</span>
                  </span>
                  <span className="shrink-0 text-xs text-muted-foreground">{timeAgo(a.started_at)}</span>
                </Link>
              ))}
            </CardContent>
          </Card>

          {offline.length > 0 && (
            <Card>
              <CardHeader>
                <CardTitle className="flex items-center gap-2">
                  <ServerOff className="size-4 text-warning" /> Offline
                </CardTitle>
              </CardHeader>
              <CardContent className="grid gap-1.5 text-sm">
                {offline.map((s) => (
                  <Link key={s.id} href={`/servers/${s.id}`} className="flex justify-between gap-2 hover:text-brand">
                    <span className="truncate">{s.name}</span>
                    <span className="shrink-0 text-xs text-muted-foreground">seen {timeAgo(s.last_seen_at)}</span>
                  </Link>
                ))}
              </CardContent>
            </Card>
          )}
        </div>
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader className="flex-row items-center justify-between">
            <CardTitle>Recent tasks</CardTitle>
            <Link href="/activity" className="text-xs text-muted-foreground hover:text-foreground">
              View all
            </Link>
          </CardHeader>
          <CardContent>
            <ul className="grid gap-1.5 text-sm">
              {jobs
                .filter((j) => !/(\.status|\.list|\.read|^system\.info)$/.test(j.op))
                .slice(0, 8)
                .map((j) => (
                  <li key={j.id} className="flex items-center gap-2">
                    <Badge variant={j.status === "succeeded" ? "success" : j.status === "failed" ? "destructive" : "warning"} className="w-20 justify-center">
                      {j.status}
                    </Badge>
                    <span className="min-w-0 flex-1 truncate">
                      <span className="font-mono text-xs">{j.op}</span> <span className="text-muted-foreground">on {j.server_name}</span>
                    </span>
                    <span className="shrink-0 text-xs text-muted-foreground">{timeAgo(j.created_at)}</span>
                  </li>
                ))}
              {jobs.length === 0 && <li className="text-muted-foreground">No tasks yet.</li>}
            </ul>
          </CardContent>
        </Card>
        {audit && (
          <Card>
            <CardHeader className="flex-row items-center justify-between">
              <CardTitle>Recent activity</CardTitle>
              <Link href="/activity" className="text-xs text-muted-foreground hover:text-foreground">
                Audit log
              </Link>
            </CardHeader>
            <CardContent>
              <ul className="grid gap-1.5 text-sm">
                {audit.map((e) => (
                  <li key={e.id} className="flex items-center gap-2">
                    {e.success ? <CheckCircle2 className="size-3.5 shrink-0 text-success" /> : <XCircle className="size-3.5 shrink-0 text-destructive" />}
                    <span className="min-w-0 flex-1 truncate">
                      <span className="font-medium">{e.actor || "—"}</span> <span className="text-muted-foreground">{e.action}</span> {e.target}
                    </span>
                    <span className="shrink-0 text-xs text-muted-foreground">{timeAgo(e.ts)}</span>
                  </li>
                ))}
              </ul>
            </CardContent>
          </Card>
        )}
      </div>
    </div>
  );
}
