import { lazy, Suspense, useEffect, useMemo, useState } from "react";
import { Link, useLocation } from "wouter";
import {
  Activity,
  Archive,
  ArrowLeft,
  Clock,
  Cog,
  Database,
  FolderOpen,
  Globe,
  Info,
  KeyRound,
  ListTodo,
  PackageCheck,
  PackageSearch,
  ScrollText,
  RefreshCw,
  Shield,
  ShieldBan,
  SquareTerminal,
  Trash2,
} from "lucide-react";
import { api, type Job } from "@/lib/api";
import { loadMetricsHistory, useLive } from "@/lib/live";
import { formatBytes, formatUptime, pct, timeAgo } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { LiveChart } from "@/components/LiveChart";
import { StatusBadge } from "./Servers";
import { Updates } from "./server/Updates";
import { Sites } from "./server/Sites";
// xterm.js is large; load it only when the Terminal tab opens.
const Terminal = lazy(() => import("./server/Terminal").then((m) => ({ default: m.Terminal })));
import { Databases } from "./server/Databases";
import { Files } from "./server/Files";
import { Cron } from "./server/Cron";
import { Services } from "./server/Services";
import { Logs } from "./server/Logs";
import { Backups } from "./server/Backups";
import { SSHKeys } from "./server/SSHKeys";
import { Firewall } from "./server/Firewall";
import { Fail2ban } from "./server/Fail2ban";

const EMPTY: never[] = [];

export function ServerDetail({ id }: { id: string }) {
  const server = useLive((s) => s.servers.get(id));
  const metrics = useLive((s) => s.metrics.get(id) ?? EMPTY);
  const loaded = useLive((s) => s.servers.size > 0);
  const [, navigate] = useLocation();
  const [tab, setTab] = useTab();
  const [logUnit, setLogUnit] = useState("");

  useEffect(() => {
    void loadMetricsHistory(id);
  }, [id]);

  const series = useMemo(
    () => ({
      cpu: metrics.map((m) => ({ ts: m.ts, v: m.cpu })),
      mem: metrics.map((m) => ({ ts: m.ts, v: pct(m.mem_used, m.mem_total) })),
      disk: metrics.map((m) => ({ ts: m.ts, v: pct(m.disk_used, m.disk_total) })),
    }),
    [metrics],
  );

  if (!server) {
    return (
      <div className="grid gap-4">
        <BackLink />
        <p className="text-muted-foreground">{loaded ? "Server not found." : "Loading…"}</p>
      </div>
    );
  }

  const m = server.online ? server.latest : null;

  async function remove() {
    if (!confirm(`Remove ${server!.name}? Its agent will be refused on the next connect.`)) return;
    await api.deleteServer(id);
    navigate("/");
  }

  return (
    <div className="grid gap-6">
      <BackLink />
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0">
          <div className="flex items-center gap-3">
            <h1 className="truncate text-2xl font-semibold tracking-tight">{server.name}</h1>
            <StatusBadge online={server.online} />
          </div>
          <p className="mt-1 text-sm text-muted-foreground">
            {server.os_pretty || "unknown OS"} · {server.kernel} · {server.arch} · {server.cpu_count} vCPU · agent{" "}
            {server.agent_version || "?"}
          </p>
        </div>
        <Button variant="outline" size="sm" onClick={remove}>
          <Trash2 /> Remove
        </Button>
      </div>

      <Tabs tab={tab} setTab={setTab} />

      {tab === "overview" && (
        <>
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
            <Stat label="Uptime" value={m ? formatUptime(m.uptime) : "—"} />
            <Stat
              label="Load (1 / 5 / 15)"
              value={m ? `${m.load1.toFixed(2)} / ${m.load5.toFixed(2)} / ${m.load15.toFixed(2)}` : "—"}
            />
            <Stat label="Memory" value={m ? `${formatBytes(m.mem_used)} / ${formatBytes(m.mem_total)}` : "—"} />
            <Stat label="Disk /" value={m ? `${formatBytes(m.disk_used)} / ${formatBytes(m.disk_total)}` : "—"} />
          </div>

          <div className="grid gap-4 lg:grid-cols-3">
            <LiveChart title="CPU" points={series.cpu} detail={`${server.cpu_count} vCPU`} />
            <LiveChart
              title="Memory"
              points={series.mem}
              color="var(--color-success)"
              detail={m ? formatBytes(m.mem_total) : undefined}
            />
            <LiveChart
              title="Disk /"
              points={series.disk}
              color="var(--color-warning)"
              detail={m ? formatBytes(m.disk_total) : undefined}
            />
          </div>

          {!server.online && (
            <p className="text-sm text-muted-foreground">Agent offline · last seen {timeAgo(server.last_seen_at)}</p>
          )}
        </>
      )}
      {tab === "terminal" && (
        <Suspense fallback={<p className="text-sm text-muted-foreground">Loading terminal…</p>}>
          <Terminal serverId={id} online={server.online} />
        </Suspense>
      )}
      {tab === "sites" && <Sites serverId={id} online={server.online} />}
      {tab === "databases" && <Databases serverId={id} online={server.online} />}
      {tab === "files" && <Files serverId={id} online={server.online} />}
      {tab === "services" && (
        <Services
          serverId={id}
          online={server.online}
          onLogs={(u) => {
            setLogUnit(u);
            setTab("logs");
          }}
        />
      )}
      {tab === "logs" && <Logs serverId={id} online={server.online} unit={logUnit} />}
      {tab === "cron" && <Cron serverId={id} online={server.online} />}
      {tab === "backups" && <Backups serverId={id} online={server.online} />}
      {tab === "updates" && <Updates serverId={id} online={server.online} />}
      {tab === "ssh" && <SSHKeys serverId={id} online={server.online} />}
      {tab === "firewall" && <Firewall serverId={id} online={server.online} />}
      {tab === "fail2ban" && <Fail2ban serverId={id} online={server.online} />}
      {tab === "tasks" && <Jobs serverId={id} online={server.online} />}
    </div>
  );
}

const TABS = [
  { id: "overview", label: "Overview", icon: Activity },
  { id: "terminal", label: "Terminal", icon: SquareTerminal },
  { id: "sites", label: "Sites", icon: Globe },
  { id: "databases", label: "Databases", icon: Database },
  { id: "files", label: "Files", icon: FolderOpen },
  { id: "services", label: "Services", icon: Cog },
  { id: "logs", label: "Logs", icon: ScrollText },
  { id: "cron", label: "Cron", icon: Clock },
  { id: "backups", label: "Backups", icon: Archive },
  { id: "updates", label: "Updates", icon: PackageCheck },
  { id: "ssh", label: "SSH keys", icon: KeyRound },
  { id: "firewall", label: "Firewall", icon: Shield },
  { id: "fail2ban", label: "Fail2ban", icon: ShieldBan },
  { id: "tasks", label: "Tasks", icon: ListTodo },
] as const;
type TabId = (typeof TABS)[number]["id"];

// The active tab lives in the URL hash so it survives reloads and can be linked.
function useTab(): [TabId, (t: TabId) => void] {
  const read = (): TabId => {
    const h = window.location.hash.slice(1);
    return (TABS.find((t) => t.id === h)?.id ?? "overview") as TabId;
  };
  const [tab, setTabState] = useState<TabId>(read);
  useEffect(() => {
    const on = () => setTabState(read());
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);
  const setTab = (t: TabId) => {
    history.replaceState(null, "", t === "overview" ? window.location.pathname : `#${t}`);
    setTabState(t);
  };
  return [tab, setTab];
}

function Tabs({ tab, setTab }: { tab: TabId; setTab: (t: TabId) => void }) {
  return (
    <div role="tablist" className="-mx-1 flex gap-1 overflow-x-auto border-b px-1">
      {TABS.map(({ id, label, icon: Icon }) => (
        <button
          key={id}
          role="tab"
          aria-selected={tab === id}
          onClick={() => setTab(id)}
          className={`-mb-px flex cursor-pointer items-center gap-1.5 border-b-2 px-3 py-2 text-sm whitespace-nowrap transition-colors ${
            tab === id ? "border-brand text-foreground" : "border-transparent text-muted-foreground hover:text-foreground"
          }`}
        >
          <Icon className="size-4" /> {label}
        </button>
      ))}
    </div>
  );
}

function BackLink() {
  return (
    <Link href="/" className="flex w-fit items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground">
      <ArrowLeft className="size-4" /> All servers
    </Link>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <Card className="p-4">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className="tabular mt-1 truncate text-lg font-semibold">{value}</div>
    </Card>
  );
}

const OPS = [
  { op: "packages.check_updates", label: "Check for updates", icon: PackageSearch },
  { op: "system.info", label: "System info", icon: Info },
];

function Jobs({ serverId, online }: { serverId: string; online: boolean }) {
  const [jobs, setJobs] = useState<Job[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [error, setError] = useState("");
  const jobTick = useLive((s) => (s.lastJob?.serverId === serverId ? s.jobTick : 0));

  useEffect(() => {
    let cancelled = false;
    api.jobs(serverId).then((j) => {
      if (!cancelled) setJobs(j);
    });
    return () => {
      cancelled = true;
    };
  }, [serverId, jobTick]);

  async function run(op: string) {
    setError("");
    try {
      const j = await api.runJob(serverId, op);
      setSelected(j.id);
    } catch (e) {
      setError((e as Error).message);
    }
  }

  const current = jobs.find((j) => j.id === selected) ?? jobs[0];

  return (
    <Card>
      <CardHeader className="flex-row flex-wrap items-center justify-between gap-3">
        <div>
          <CardTitle>Tasks</CardTitle>
          <CardDescription>Run operations on this server through its agent.</CardDescription>
        </div>
        <div className="flex flex-wrap gap-2">
          {OPS.map(({ op, label, icon: Icon }) => (
            <Button key={op} variant="secondary" size="sm" disabled={!online} onClick={() => run(op)}>
              <Icon /> {label}
            </Button>
          ))}
        </div>
      </CardHeader>
      <CardContent className="grid gap-4">
        {error && <p className="text-sm text-destructive">{error}</p>}
        {jobs.length === 0 ? (
          <p className="text-sm text-muted-foreground">No tasks run yet.</p>
        ) : (
          <div className="grid gap-4 lg:grid-cols-[minmax(0,18rem)_minmax(0,1fr)]">
            <ul className="grid max-h-96 content-start gap-1 overflow-auto">
              {jobs.map((j) => (
                <li key={j.id}>
                  <button
                    onClick={() => setSelected(j.id)}
                    className={`flex w-full cursor-pointer items-center justify-between gap-2 rounded-md px-3 py-2 text-left text-sm hover:bg-accent ${
                      current?.id === j.id ? "bg-accent" : ""
                    }`}
                  >
                    <span className="truncate font-mono text-xs">{j.op}</span>
                    <JobBadge status={j.status} />
                  </button>
                </li>
              ))}
            </ul>
            {current && <JobView job={current} />}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function JobBadge({ status }: { status: Job["status"] }) {
  const variant = status === "succeeded" ? "success" : status === "failed" ? "destructive" : "warning";
  return (
    <Badge variant={variant}>
      {(status === "running" || status === "queued") && <RefreshCw className="size-3 animate-spin" />}
      {status}
    </Badge>
  );
}

type Updates = {
  count: number;
  security_count: number;
  reboot_required: boolean;
  updates: { name: string; current: string; candidate: string; security: boolean }[];
};

function JobView({ job }: { job: Job }) {
  const isUpdates = job.op === "packages.check_updates" && job.status === "succeeded" && job.result != null;
  const u = job.result as Updates;
  return (
    <div className="grid min-w-0 content-start gap-3">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <span className="font-mono">{job.op}</span>
        <JobBadge status={job.status} />
        <span className="text-xs text-muted-foreground">{new Date(job.created_at).toLocaleString()}</span>
      </div>
      {job.error && <p className="text-sm text-destructive">{job.error}</p>}
      {isUpdates && (
        <div className="grid gap-2">
          <div className="flex flex-wrap gap-2">
            <Badge variant={u.count ? "warning" : "success"}>{u.count} upgradable</Badge>
            {u.security_count > 0 && <Badge variant="destructive">{u.security_count} security</Badge>}
            {u.reboot_required && <Badge variant="warning">reboot required</Badge>}
          </div>
          {u.updates.length > 0 && (
            <div className="max-h-64 overflow-auto rounded-lg border">
              <table className="w-full text-xs">
                <thead className="sticky top-0 bg-card text-left text-muted-foreground">
                  <tr>
                    <th className="px-3 py-2 font-medium">Package</th>
                    <th className="px-3 py-2 font-medium">Installed</th>
                    <th className="px-3 py-2 font-medium">Available</th>
                  </tr>
                </thead>
                <tbody className="font-mono">
                  {u.updates.map((p) => (
                    <tr key={p.name} className="border-t">
                      <td className="px-3 py-1.5">
                        {p.name} {p.security && <span className="text-destructive">●</span>}
                      </td>
                      <td className="px-3 py-1.5 text-muted-foreground">{p.current}</td>
                      <td className="px-3 py-1.5">{p.candidate}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}
      {job.op === "system.info" && job.status === "succeeded" && job.result != null && (
        <pre className="overflow-auto rounded-lg border bg-background/60 p-3 font-mono text-xs">
          {JSON.stringify(job.result, null, 2)}
        </pre>
      )}
      {job.log && (
        <pre className="max-h-64 overflow-auto rounded-lg border bg-background/60 p-3 font-mono text-xs leading-relaxed text-muted-foreground">
          {job.log}
        </pre>
      )}
    </div>
  );
}
