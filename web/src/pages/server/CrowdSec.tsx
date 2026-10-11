import { useEffect, useState } from "react";
import { Link } from "wouter";
import { AlertTriangle, Download, Plus, RefreshCw, ShieldCheck, ShieldOff, Trash2, Wrench, X } from "lucide-react";
import { api, type CsAlert, type Job } from "@/lib/api";
import { useAgentState, waitJob } from "@/lib/ops";
import { useAccess } from "@/lib/access";
import { timeAgo } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { OpStatus, Offline } from "@/components/OpStatus";

type Collection = { name: string; status: string; local_version: string };
type CS = {
  installed: boolean;
  engine_active?: boolean;
  bouncer_active?: boolean;
  fail2ban_active?: boolean;
  lapi_ok?: boolean;
  lapi_error?: string;
  version?: string;
  trust_lan?: boolean;
  collections?: Collection[];
};

// Machines on the central LAPI are named <slug>-<first 6 of the server id>.
const machineSuffix = (serverId: string) => "-" + serverId.slice(0, 6);

export function ServerCrowdSec({ serverId, online, onOpenFail2ban }: { serverId: string; online: boolean; onOpenFail2ban: () => void }) {
  const { data, setData, busy, error, job, run } = useAgentState<CS>(serverId, "crowdsec.status", online);
  const { can } = useAccess();
  const [panelBusy, setPanelBusy] = useState("");
  const [panelErr, setPanelErr] = useState("");
  const [panelJob, setPanelJob] = useState<Job | null>(null);
  const [trustLan, setTrustLan] = useState(true);
  const [newCol, setNewCol] = useState("");
  const [alerts, setAlerts] = useState<CsAlert[] | null>(null);
  const disabled = !online || !!busy || !!panelBusy || !can("server.security", serverId);

  useEffect(() => {
    if (data?.trust_lan !== undefined) setTrustLan(data.trust_lan);
  }, [data?.trust_lan]);

  useEffect(() => {
    if (!data?.installed) return;
    api
      .crowdsec()
      .then((o) => setAlerts((o.alerts ?? []).filter((a) => a.machine_id.endsWith(machineSuffix(serverId)))))
      .catch(() => setAlerts([]));
  }, [data?.installed, serverId]);

  // Install/remove go through the panel, which creates or deletes this
  // server's machine and bouncer on the central LAPI.
  async function viaPanel(label: string, start: () => Promise<Job>, isStatus = true) {
    setPanelBusy(label);
    setPanelErr("");
    try {
      const j = await waitJob(await start(), setPanelJob);
      if (j.status === "failed") throw new Error(j.error || "operation failed");
      if (isStatus && j.result != null) setData(j.result as CS);
      if (!isStatus) void run("crowdsec.status");
    } catch (e) {
      setPanelErr((e as Error).message);
    } finally {
      setPanelBusy("");
    }
  }
  const disableF2b = () => {
    if (confirm("Stop and disable Fail2ban on this server? CrowdSec keeps protecting SSH.")) {
      void viaPanel("disable fail2ban", () => api.runJob(serverId, "services.action", { name: "fail2ban", action: "disable" }), false);
    }
  };
  const install = () => viaPanel("install", () => api.crowdsecInstall(serverId, trustLan));
  const status = <OpStatus busy={busy || panelBusy || null} error={error || panelErr} job={panelBusy || panelErr ? panelJob : job} />;

  if (data && !data.installed) {
    return (
      <Card className="flex flex-col items-center gap-3 p-10 text-center">
        <ShieldOff className="size-8 text-muted-foreground" />
        <div className="font-medium">CrowdSec is not installed</div>
        <p className="max-w-md text-sm text-muted-foreground">
          Installs the CrowdSec engine and the nftables firewall bouncer, connected to the panel's central LAPI. Attacks detected
          here are banned on every server, and bans from other servers apply here. Collections are picked from what runs on this
          server (SSH, nginx, MariaDB, WordPress…).
        </p>
        {data.fail2ban_active && <Fail2banNote onOpen={onOpenFail2ban} onDisable={disableF2b} disabled={disabled} />}
        <TrustLan value={trustLan} onChange={setTrustLan} disabled={disabled} />
        <Button variant="brand" disabled={disabled} onClick={install}>
          <Download /> Install CrowdSec
        </Button>
        <div className="w-full max-w-xl text-left">{status}</div>
      </Card>
    );
  }

  const healthy = data?.engine_active && data?.bouncer_active && data?.lapi_ok;
  return (
    <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_22rem]">
      <div className="grid content-start gap-4">
        <Card>
          <CardHeader className="flex-row flex-wrap items-start justify-between gap-3">
            <div>
              <CardTitle className="flex items-center gap-2">
                CrowdSec
                {data && (healthy ? <Badge variant="success">protected</Badge> : <Badge variant="destructive">needs attention</Badge>)}
              </CardTitle>
              <CardDescription>
                {data ? `Engine ${data.version ?? ""} · reports to the central LAPI` : "Loading…"} ·{" "}
                <Link href="/security" className="underline-offset-2 hover:underline">
                  decisions &amp; alerts
                </Link>
              </CardDescription>
            </div>
            <Button variant="ghost" size="sm" disabled={!online || !!busy} onClick={() => run("crowdsec.status")} aria-label="Refresh">
              <RefreshCw />
            </Button>
          </CardHeader>
          <CardContent className="grid gap-4">
            {!online && <Offline />}
            {data?.fail2ban_active && <Fail2banNote onOpen={onOpenFail2ban} onDisable={disableF2b} disabled={disabled} />}
            {data && (
              <div className="grid gap-2 sm:grid-cols-3">
                <Health label="Security engine" ok={data.engine_active} hint="crowdsec.service" />
                <Health label="Firewall bouncer" ok={data.bouncer_active} hint="nftables" />
                <Health label="Central LAPI" ok={data.lapi_ok} hint={data.lapi_ok ? "connected" : data.lapi_error || "unreachable"} />
              </div>
            )}
            {data && !healthy && (
              <Button variant="secondary" className="w-fit" disabled={disabled} onClick={install}>
                <Wrench /> Repair (re-register with the LAPI)
              </Button>
            )}
            {status}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Recent alerts from this server</CardTitle>
            <CardDescription>Last 7 days. Each alert creates a ban shared by all servers.</CardDescription>
          </CardHeader>
          <CardContent>
            {alerts === null ? (
              <p className="text-sm text-muted-foreground">Loading…</p>
            ) : alerts.length === 0 ? (
              <p className="text-sm text-muted-foreground">No attacks detected.</p>
            ) : (
              <ul className="divide-y text-sm">
                {alerts.slice(0, 20).map((a) => (
                  <li key={a.id} className="flex flex-wrap items-center justify-between gap-2 py-2">
                    <span className="font-mono text-xs">{a.scenario}</span>
                    <span className="font-mono text-xs">
                      {a.source.value}
                      {a.source.cn ? ` · ${a.source.cn}` : ""}
                    </span>
                    <span className="text-xs text-muted-foreground">{timeAgo(a.created_at)}</span>
                  </li>
                ))}
              </ul>
            )}
          </CardContent>
        </Card>
      </div>

      <div className="grid content-start gap-4">
        <Card>
          <CardHeader>
            <CardTitle>Collections</CardTitle>
            <CardDescription>Parsers and scenarios from the CrowdSec Hub.</CardDescription>
          </CardHeader>
          <CardContent className="grid gap-3">
            <ul className="grid gap-1">
              {data?.collections?.map((c) => (
                <li key={c.name} className="flex items-center justify-between gap-2 rounded-md border py-1 pr-1 pl-2 font-mono text-xs">
                  <span className="truncate" title={`${c.name} ${c.local_version}`}>
                    {c.name}
                  </span>
                  {c.name !== "crowdsecurity/linux" && (
                    <Button
                      variant="ghost"
                      size="icon"
                      className="size-6"
                      aria-label={`Remove ${c.name}`}
                      title="Remove"
                      disabled={disabled}
                      onClick={() => run("crowdsec.hub", { action: "remove", items: c.name })}
                    >
                      <X className="size-3.5" />
                    </Button>
                  )}
                </li>
              ))}
            </ul>
            <form
              className="flex gap-2"
              onSubmit={async (e) => {
                e.preventDefault();
                if (await run("crowdsec.hub", { action: "install", items: newCol.trim() })) setNewCol("");
              }}
            >
              <Input
                placeholder="crowdsecurity/wordpress"
                value={newCol}
                onChange={(e) => setNewCol(e.target.value)}
                aria-label="Collection to install"
                pattern="[a-z0-9_\-]+/[a-z0-9_.\-]+"
                required
              />
              <Button type="submit" variant="secondary" disabled={disabled || !newCol.trim()} aria-label="Install collection">
                <Plus />
              </Button>
            </form>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Settings</CardTitle>
          </CardHeader>
          <CardContent className="grid gap-3">
            <TrustLan value={trustLan} onChange={setTrustLan} disabled={disabled} />
            {data && trustLan !== data.trust_lan && (
              <Button variant="secondary" disabled={disabled} onClick={install}>
                <ShieldCheck /> Apply
              </Button>
            )}
            <Button
              variant="outline"
              className="text-destructive"
              disabled={disabled}
              onClick={() => {
                if (confirm("Remove CrowdSec from this server? Shared bans will no longer be enforced here.")) {
                  void viaPanel("remove", () => api.crowdsecRemove(serverId));
                }
              }}
            >
              <Trash2 /> Remove CrowdSec
            </Button>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}

function Health({ label, ok, hint }: { label: string; ok?: boolean; hint: string }) {
  return (
    <div className="rounded-lg border p-3">
      <div className="flex items-center gap-2 text-sm font-medium">
        <span className={`size-2 rounded-full ${ok ? "bg-success" : "bg-destructive"}`} />
        {label}
      </div>
      <div className="mt-1 truncate text-xs text-muted-foreground" title={hint}>
        {hint}
      </div>
    </div>
  );
}

function TrustLan({ value, onChange, disabled }: { value: boolean; onChange: (v: boolean) => void; disabled: boolean }) {
  return (
    <label className="flex max-w-md items-start gap-2 text-left text-sm">
      <input type="checkbox" className="mt-1" checked={value} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
      <span>
        Never ban private IPs
        <span className="block text-xs text-muted-foreground">
          10.x, 172.16–31.x and 192.168.x are trusted. Turn off if attackers could be on your own network.
        </span>
      </span>
    </label>
  );
}

function Fail2banNote({ onOpen, onDisable, disabled }: { onOpen: () => void; onDisable: () => void; disabled: boolean }) {
  return (
    <div className="flex max-w-xl items-start gap-2 rounded-lg border border-warning/30 bg-warning/10 px-3 py-2 text-left text-sm text-warning">
      <AlertTriangle className="mt-0.5 size-4 shrink-0" />
      <span>
        Fail2ban is also running here. Both will ban SSH brute force, and Fail2ban's bans stay local to this server and can hide
        attacks from CrowdSec. Keeping just CrowdSec is recommended.{" "}
        <button type="button" className="cursor-pointer font-medium underline underline-offset-2 disabled:opacity-50" disabled={disabled} onClick={onDisable}>
          Disable Fail2ban
        </button>{" "}
        ·{" "}
        <button type="button" className="cursor-pointer underline underline-offset-2" onClick={onOpen}>
          view its bans
        </button>
      </span>
    </div>
  );
}
