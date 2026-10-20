import { useCallback, useEffect, useMemo, useState } from "react";
import { Link, useLocation } from "wouter";
import { AppWindow, ArrowLeft, Camera, Copy, ExternalLink, KeyRound, LayoutTemplate, Loader2, MonitorPlay, MoveRight, Pause, Play, Power, RefreshCw, RotateCw, Server as ServerIcon, Square, Trash2 } from "lucide-react";
import { api, type PVEGuestDetail, type PVEGuestRef, type PVERRDPoint, type PVETask, type PVETimeframe } from "@/lib/api";
import { useAccess } from "@/lib/access";
import { setPending, usePending } from "@/lib/pending";
import { actionWord, PowerBanner, type PowerState } from "@/components/PowerBanner";
import { formatBytes, formatUptime } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { HistoryChart } from "@/components/HistoryChart";
import { ConsolePreview, GuestConsole } from "@/components/GuestConsole";
import { NetSummary, NetworkCard } from "@/components/GuestNetworks";
import { Meter } from "@/components/LiveChart";
import { DeleteDialog, SnapshotsDialog } from "@/pages/Proxmox";
import { BackupsTab, CloudInitTab, FirewallTab } from "@/pages/ProxmoxGuestTabs";
import { GuestMoveDialog, HardwareTab } from "@/pages/ProxmoxGuestHardware";
import { ConfigTab } from "@/pages/ProxmoxGuestConfig";
import { WindowsAdminDialog, WindowsTemplateDialog } from "@/components/WindowsTemplates";
import { WindowsTab } from "@/components/WindowsManage";

type Tab = "overview" | "hardware" | "config" | "windows" | "backups" | "cloudinit" | "firewall" | "tasks";

const timeframes: { tf: PVETimeframe; label: string }[] = [
  { tf: "hour", label: "Hour" },
  { tf: "day", label: "Day" },
  { tf: "week", label: "Week" },
  { tf: "month", label: "Month" },
  { tf: "year", label: "Year" },
];

// Config keys that describe virtual hardware; everything else is an option.
const hardwareKey =
  /^(cores|sockets|cpu|cpulimit|cpuunits|vcpus|memory|balloon|swap|bios|machine|scsihw|vga|arch|rootfs|efidisk\d|tpmstate\d|(scsi|virtio|sata|ide|net|mp|unused|usb|hostpci|serial)\d+)$/;
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

// ProxmoxGuest is a guest's page. embedded is the same view inside the
// cluster page's right pane: no back link, and a link to the full page.
export function ProxmoxGuest({
  cluster,
  node,
  type,
  vmid,
  embedded,
  onGone,
}: {
  cluster: string;
  node: string;
  type: string;
  vmid: string;
  embedded?: boolean;
  onGone?: () => void; // after a delete, instead of going back to /proxmox
}) {
  const [, navigate] = useLocation();
  const [del, setDel] = useState(false);
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
  const [console_, setConsole] = useState(false);
  const [tab, setTab] = useState<Tab>("overview");
  const [move, setMove] = useState<"clone" | "migrate" | null>(null);
  const [pwDlg, setPwDlg] = useState(false);
  const [winTpl, setWinTpl] = useState(false);
  const [winAdmin, setWinAdmin] = useState(false);

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
  const loadTasks = useCallback(
    () =>
      api
        .pveGuestTasks(cluster, ref)
        .then(setTasks)
        .catch(() => {}),
    [cluster, ref],
  );
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
    const get = () =>
      api
        .pveGuestRRD(cluster, ref, tf)
        .then(setRrd)
        .catch(() => setRrd([]));
    void get();
    const t = setInterval(get, tf === "hour" ? 60000 : 300000);
    return () => clearInterval(t);
  }, [cluster, ref, tf]);

  async function enroll() {
    setBusy("enroll");
    setMsg(null);
    try {
      const r = await api.pveEnroll(cluster, ref);
      setMsg({ ok: true, text: `Enrolled as ${r.name}: it is now a servermgr server.` });
      void load();
    } catch (e) {
      setMsg({ ok: false, text: (e as Error).message });
    } finally {
      setBusy("");
    }
  }

  const pendingKey = `${node}/${vmid}`;
  const pending = usePending(pendingKey);
  const [pw, setPw] = useState<PowerState>(null);
  const clearPw = useCallback(() => setPw(null), []);
  async function power(action: string) {
    setBusy(action);
    setMsg(null);
    setPending(pendingKey, action);
    setPw({ action, phase: "running", started: Date.now() });
    try {
      // The panel waits for Proxmox's task, so this returns when it's done.
      await api.pvePower(cluster, ref, action);
      setPw((p) => p && { ...p, phase: "done" });
    } catch (e) {
      setPw((p) => p && { ...p, phase: "failed", error: (e as Error).message });
    } finally {
      setBusy("");
      setPending(pendingKey, null);
      void load();
      void loadTasks();
    }
  }

  const charts = useMemo(() => {
    const times = rrd.map((p) => p.time);
    const col = (k: keyof PVERRDPoint, f = (v: number) => v) => rrd.map((p) => (typeof p[k] === "number" ? f(p[k] as number) : null));
    return {
      times,
      cpu: col("cpu", (v) => v * 100),
      mem: col("mem"),
      maxmem: col("maxmem"),
      netin: col("netin"),
      netout: col("netout"),
      dr: col("diskread"),
      dw: col("diskwrite"),
    };
  }, [rrd]);

  const back = embedded ? null : (
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
  const hw = Object.entries(g.config)
    .filter(([k]) => hardwareKey.test(k))
    .sort(([a], [b]) => a.localeCompare(b, undefined, { numeric: true }));
  const opts = Object.entries(g.config)
    .filter(([k]) => !hardwareKey.test(k) && !hiddenKey.has(k))
    .sort(([a], [b]) => a.localeCompare(b));
  const tabs: { id: Tab; label: string }[] = [
    { id: "overview", label: "Overview" },
    { id: "hardware", label: "Hardware" },
    { id: "config", label: "Configuration" },
    ...(g.type === "qemu" && String(g.config.ostype ?? "").startsWith("w") && ac.can("proxmox.guests") ? [{ id: "windows" as const, label: "Windows" }] : []),
    { id: "backups", label: "Backups" },
    ...(g.type === "qemu" ? [{ id: "cloudinit" as const, label: "Cloud-init" }] : []),
    { id: "firewall", label: "Firewall" },
    { id: "tasks", label: "Tasks" },
  ];
  const desc = typeof g.config.description === "string" ? g.config.description.trim() : "";

  return (
    <div className={embedded ? "grid gap-4" : "grid gap-6"}>
      {back}
      <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_20rem]">
        <div className="grid content-start gap-3">
          <div>
            <div className="flex flex-wrap items-center gap-2">
              <h1 className={`${embedded ? "text-xl" : "text-2xl"} font-semibold tracking-tight`}>{name}</h1>
              <Badge variant="outline">
                {g.type === "qemu" ? "VM" : "CT"} {g.vmid}
              </Badge>
              {g.server_id && (
                <Link href={`/servers/${g.server_id}`}>
                  <Badge variant="outline" className="cursor-pointer gap-1 hover:bg-accent" title="Open it in servermgr">
                    <ServerIcon className="size-3" /> {g.server_name}
                  </Badge>
                </Link>
              )}
              {pending ? (
                <Badge variant="warning" className="gap-1.5">
                  <span className="animate-ring size-1.5 rounded-full bg-current" />
                  {actionWord(pending).toLowerCase()}…
                </Badge>
              ) : (
                <Badge key={st.status} variant={running && !paused ? "success" : st.status === "stopped" ? "outline" : "warning"} className="animate-pop">
                  {paused ? "paused" : st.status}
                </Badge>
              )}
              {st.lock && <Badge variant="warning">locked: {st.lock}</Badge>}
              {embedded && (
                <Link
                  href={`/proxmox/${cluster}/${encodeURIComponent(node)}/${type}/${vmid}`}
                  className="ml-1 inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
                  title="Open on its own page"
                >
                  <ExternalLink className="size-3.5" /> Full page
                </Link>
              )}
            </div>
            <p className="mt-1 text-sm text-muted-foreground">
              on {g.node}
              {running && st.uptime ? ` · up ${formatUptime(st.uptime)}` : ""}
            </p>
            <NetSummary g={g} running={running} />
          </div>
          <div className="flex flex-wrap gap-2">
            {ac.can("proxmox.console") && running && (
              <Button size="sm" variant="brand" onClick={() => setConsole(true)}>
                <MonitorPlay /> Console
              </Button>
            )}
            {ac.can("proxmox.operate") && (
              <>
                {(!running || paused) && (
                  <Button size="sm" variant="brand" disabled={!!busy} onClick={() => power(paused ? "resume" : "start")}>
                    {busy === "start" || busy === "resume" ? <Loader2 className="animate-spin" /> : <Play />} {paused ? "Resume" : "Start"}
                  </Button>
                )}
                {running && !paused && (
                  <>
                    <Button size="sm" variant="secondary" disabled={!!busy} onClick={() => power("shutdown")}>
                      {busy === "shutdown" ? <Loader2 className="animate-spin" /> : <Power />} Shut down
                    </Button>
                    <Button size="sm" variant="secondary" disabled={!!busy} onClick={() => power("reboot")}>
                      {busy === "reboot" ? <Loader2 className="animate-spin" /> : <RotateCw />} Reboot
                    </Button>
                    {g.type === "qemu" && (
                      <Button size="sm" variant="secondary" disabled={!!busy} onClick={() => power("suspend")}>
                        {busy === "suspend" ? <Loader2 className="animate-spin" /> : <Pause />} Suspend
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
                    {busy === "stop" ? <Loader2 className="animate-spin" /> : <Square />} Stop
                  </Button>
                )}
                <Button size="sm" variant="secondary" onClick={() => setSnaps(true)}>
                  <Camera /> Snapshots
                </Button>
              </>
            )}
            {ac.can("proxmox.guests") && ac.can("servers.add") && g.type === "qemu" && running && !g.server_id && !String(g.config.ostype ?? "").startsWith("w") && (
              <Button
                size="sm"
                variant="secondary"
                disabled={busy === "enroll"}
                onClick={() => void enroll()}
                title="Install the servermgr agent through the QEMU guest agent; no SSH needed"
              >
                {busy === "enroll" ? <Loader2 className="animate-spin" /> : <ServerIcon />} Enroll in servermgr
              </Button>
            )}
            {ac.can("proxmox.guests") && g.type === "qemu" && running && !String(g.config.ostype ?? "").startsWith("w") && (
              <Button size="sm" variant="secondary" onClick={() => setPwDlg(true)} title="Set a user's password through the QEMU guest agent">
                <KeyRound /> Set password
              </Button>
            )}
            {ac.can("proxmox.guests") && (
              <Button size="sm" variant="secondary" onClick={() => setMove("clone")}>
                <Copy /> Clone
              </Button>
            )}
            {ac.can("proxmox.manage") && (
              <Button size="sm" variant="secondary" onClick={() => setMove("migrate")}>
                <MoveRight /> Migrate
              </Button>
            )}
            {ac.can("proxmox.manage") && st.status === "stopped" && (
              <Button
                size="sm"
                variant="ghost"
                onClick={() => {
                  const c = prompt(`Converting ${name} to a template can't be undone; it can then only be cloned. Type ${g.vmid} to confirm.`);
                  if (c)
                    void api
                      .pveMakeTemplate(cluster, ref, c)
                      .then(() => setMsg({ ok: true, text: "Converted to a template." }))
                      .catch((e) => setMsg({ ok: false, text: (e as Error).message }))
                      .finally(load);
                }}
              >
                <LayoutTemplate /> Make template
              </Button>
            )}
            {ac.can("proxmox.manage") && g.type === "qemu" && String(g.config.ostype ?? "").startsWith("w") && !g.config.template && (
              <Button size="sm" variant="ghost" onClick={() => setWinTpl(true)}>
                <AppWindow /> Windows template
              </Button>
            )}
            {ac.can("proxmox.guests") && g.type === "qemu" && String(g.config.ostype ?? "").startsWith("w") && !g.config.template && st.status === "running" && (
              <Button size="sm" variant="ghost" onClick={() => setWinAdmin(true)}>
                <KeyRound /> Reset Administrator
              </Button>
            )}
            {ac.can("proxmox.guests") && st.status === "stopped" && (
              <Button size="sm" variant="ghost" className="text-destructive" onClick={() => setDel(true)}>
                <Trash2 /> Delete
              </Button>
            )}
          </div>
        </div>
        {ac.can("proxmox.console") && running && !console_ && (
          <ConsolePreview key={`${node}/${vmid}`} cluster={cluster} guest={ref} onExpand={() => setConsole(true)} />
        )}
      </div>
      <PowerBanner
        state={pw}
        name={name}
        hint={pw?.action === "shutdown" ? "(waits for the guest OS; Stop forces it)" : undefined}
        onDismiss={clearPw}
      />
      {msg && <p className={`text-sm ${msg.ok ? "text-success" : "text-destructive"}`}>{msg.text}</p>}
      {err && <p className="text-sm text-destructive">Refresh failed: {err}</p>}

      <nav className="-mx-1 flex gap-1 overflow-x-auto border-b px-1 text-sm" aria-label="Guest sections">
        {tabs.map((t) => (
          <button
            key={t.id}
            onClick={() => setTab(t.id)}
            aria-current={tab === t.id ? "page" : undefined}
            className={`-mb-px border-b-2 px-3 py-2 whitespace-nowrap ${tab === t.id ? "border-brand text-foreground" : "border-transparent text-muted-foreground hover:text-foreground"}`}
          >
            {t.label}
          </button>
        ))}
      </nav>

      {tab === "overview" && (
        <>
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
            <Stat
              label="CPU"
              value={running ? `${cpuPct.toFixed(1)}%` : "—"}
              sub={`${st.cpus ?? g.config.cores ?? "?"} vCPU`}
              meter={running ? cpuPct : undefined}
            />
            <Stat
              label="Memory"
              value={running ? formatBytes(st.mem ?? 0) : "—"}
              sub={`of ${formatBytes(st.maxmem ?? 0)}`}
              meter={running ? memPct : undefined}
            />
            <Stat
              label="Disk"
              value={formatBytes(st.maxdisk ?? 0)}
              sub={g.type === "lxc" && st.maxdisk ? `${formatBytes(st.disk ?? 0)} used` : "allocated"}
              meter={g.type === "lxc" && st.maxdisk ? ((st.disk ?? 0) / st.maxdisk) * 100 : undefined}
            />
            <Stat label="Network" value={`↓ ${formatBytes(st.netin ?? 0)}`} sub={`↑ ${formatBytes(st.netout ?? 0)} since start`} />
          </div>

          <NetworkCard g={g} running={running} />

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
                <HistoryChart
                  title="CPU"
                  times={charts.times}
                  max={100}
                  format={(v) => `${v.toFixed(1)}%`}
                  series={[
                    {
                      label: "CPU",
                      color: "var(--color-brand)",
                      values: charts.cpu,
                    },
                  ]}
                />
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
        </>
      )}
      {tab === "hardware" && (
        <HardwareTab
          key={JSON.stringify(g.config)}
          cluster={cluster}
          guest={ref}
          detail={g}
          onChanged={() => {
            void load();
            void loadTasks();
          }}
        />
      )}
      {tab === "config" && (
        <>
          <ConfigTab
            cluster={cluster}
            guest={ref}
            detail={g}
            hardware={hw}
            other={opts}
            shown={shown}
            onHardware={() => setTab("hardware")}
            onChanged={() => {
              void load();
              void loadTasks();
            }}
          />
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
        </>
      )}
      {tab === "backups" && (
        <BackupsTab
          cluster={cluster}
          guest={ref}
          detail={g}
          onChanged={() => {
            void load();
            void loadTasks();
          }}
        />
      )}
      {tab === "cloudinit" && (
        <CloudInitTab
          key={String(g.config.digest ?? "")}
          cluster={cluster}
          guest={ref}
          detail={g}
          onChanged={() => {
            void load();
            void loadTasks();
          }}
        />
      )}
      {tab === "firewall" && (
        <FirewallTab
          cluster={cluster}
          guest={ref}
          detail={g}
          onChanged={() => {
            void load();
            void loadTasks();
          }}
        />
      )}
      {tab === "windows" && <WindowsTab cluster={cluster} guest={ref} running={running} />}
      {tab === "tasks" && (
        <>
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
                            <Badge
                              variant={!t.status ? "warning" : t.status === "OK" ? "success" : t.status.startsWith("WARNINGS") ? "warning" : "destructive"}
                            >
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
        </>
      )}

      {winTpl && (
        <WindowsTemplateDialog
          cluster={cluster}
          node={node}
          vmid={g.vmid}
          name={name}
          stopped={st.status === "stopped"}
          onClose={() => setWinTpl(false)}
          onDone={() => {
            setMsg({ ok: true, text: "Made a Windows cloud-init template; deploy it from the Deploy page." });
            void load();
          }}
        />
      )}
      {winAdmin && (
        <WindowsAdminDialog
          cluster={cluster}
          node={node}
          vmid={g.vmid}
          name={name}
          onClose={() => setWinAdmin(false)}
          onDone={() => setMsg({ ok: true, text: "Administrator is enabled, unlocked and has the new password." })}
        />
      )}
      {snaps && <SnapshotsDialog cluster={{ id: cluster, name: "" }} guest={{ ...ref, name }} ac={ac} onClose={() => setSnaps(false)} />}
      {pwDlg && (
        <SetPasswordDialog
          cluster={cluster}
          guest={ref}
          name={name}
          windows={String(g.config.ostype ?? "").startsWith("w")}
          onClose={() => setPwDlg(false)}
          onDone={(t) => setMsg({ ok: true, text: t })}
        />
      )}
      {move && (
        <GuestMoveDialog
          mode={move}
          cluster={cluster}
          guest={ref}
          name={name}
          running={running}
          onClose={() => setMove(null)}
          onDone={(text) => {
            setMove(null);
            setMsg({ ok: true, text });
            void loadTasks();
          }}
        />
      )}
      {del && (
        <DeleteDialog
          cluster={cluster}
          guest={{ ...ref, name }}
          onClose={() => setDel(false)}
          onDone={() => {
            setDel(false);
            if (onGone) onGone();
            else navigate("/proxmox");
          }}
        />
      )}
      {console_ && <GuestConsole cluster={cluster} guest={ref} name={name} sharedClip={/clipboard=vnc/.test(String(g.config.vga ?? ""))} onClose={() => setConsole(false)} />}
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
      <DialogContent className="sm:max-w-3xl">
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

// SetPasswordDialog sets a user's password inside the VM through the QEMU
// guest agent, so nothing is typed into the console and the keyboard
// layout cannot get in the way.
function SetPasswordDialog({
  cluster,
  guest,
  name,
  windows,
  onClose,
  onDone,
}: {
  cluster: string;
  guest: PVEGuestRef;
  name: string;
  windows: boolean;
  onClose: () => void;
  onDone: (text: string) => void;
}) {
  const [user, setUser] = useState(windows ? "Administrator" : "root");
  const [p1, setP1] = useState("");
  const [p2, setP2] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Set a password in {name}</DialogTitle>
        </DialogHeader>
        <form
          className="grid gap-3"
          onSubmit={async (e) => {
            e.preventDefault();
            if (p1 !== p2) return setErr("The passwords do not match.");
            setBusy(true);
            setErr("");
            try {
              await api.pveSetPassword(cluster, guest, user.trim(), p1);
              onDone(`Password of ${user.trim()} set.${windows ? " A locked-out account unlocks by itself after the lockout time (Windows: 10-15 minutes)." : ""}`);
              onClose();
            } catch (x) {
              setErr((x as Error).message);
            } finally {
              setBusy(false);
            }
          }}
        >
          <p className="text-sm text-muted-foreground">
            The QEMU guest agent sets it inside the running VM, so the console&apos;s keyboard layout does not matter. The password is not stored or
            logged.{windows ? " Windows needs at least 12 characters with three of: lower case, upper case, digits, symbols." : ""}
          </p>
          <label className="grid gap-1.5 text-sm">
            User
            <input className="h-9 rounded-md border bg-transparent px-3" value={user} onChange={(e) => setUser(e.target.value)} required />
          </label>
          <label className="grid gap-1.5 text-sm">
            New password
            <input className="h-9 rounded-md border bg-transparent px-3" type="password" autoComplete="new-password" minLength={8} value={p1} onChange={(e) => setP1(e.target.value)} required />
          </label>
          <label className="grid gap-1.5 text-sm">
            Repeat it
            <input className="h-9 rounded-md border bg-transparent px-3" type="password" autoComplete="new-password" value={p2} onChange={(e) => setP2(e.target.value)} required />
          </label>
          {err && <p className="text-sm text-destructive">{err}</p>}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" variant="brand" disabled={busy}>
              {busy ? <Loader2 className="animate-spin" /> : <KeyRound />} Set password
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
