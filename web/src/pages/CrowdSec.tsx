import { useCallback, useEffect, useMemo, useState } from "react";
import { Link } from "wouter";
import { Ban, Download, Link2, Plus, RefreshCw, ShieldAlert, Undo2, X } from "lucide-react";
import { api, type CrowdSecOverview, type CsDecision, type Server } from "@/lib/api";
import { waitJob } from "@/lib/ops";
import { canOperate, isAdmin, useRole } from "@/lib/role";
import { timeAgo } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label } from "@/components/ui/input";
import { Select } from "@/components/ui/select";

type Row = CsDecision & { machine: string };

// Central CrowdSec: one LAPI on the panel host, every server reports to it
// and enforces the shared decisions with its firewall bouncer.
export function CrowdSec() {
  const role = useRole();
  const [ov, setOv] = useState<CrowdSecOverview | null>(null);
  const [servers, setServers] = useState<Server[]>([]);
  const [err, setErr] = useState("");
  const [msg, setMsg] = useState("");
  const [filter, setFilter] = useState("");
  const [installing, setInstalling] = useState<Record<string, string>>({});

  const load = useCallback(async () => {
    try {
      const [o, s] = await Promise.all([api.crowdsec(), api.servers()]);
      setOv(o);
      setServers(s);
    } catch (e) {
      setErr((e as Error).message);
    }
  }, []);
  useEffect(() => {
    void load();
    const t = setInterval(load, 15000);
    return () => clearInterval(t);
  }, [load]);

  async function act(f: () => Promise<unknown>, ok = "") {
    setErr("");
    setMsg("");
    try {
      await f();
      if (ok) setMsg(ok);
      await load();
      return true;
    } catch (e) {
      setErr((e as Error).message);
      return false;
    }
  }

  const rows: Row[] = useMemo(
    () => (ov?.decisions ?? []).flatMap((a) => (a.decisions ?? []).map((d) => ({ ...d, machine: a.machine_id }))),
    [ov],
  );
  const shown = rows.filter((r) => !filter || `${r.value} ${r.scenario} ${r.origin}`.toLowerCase().includes(filter.toLowerCase()));

  // Machine/bouncer names end in "-<first 6 chars of the server id>".
  const serverFor = (machine: string) => servers.find((s) => machine.endsWith("-" + s.id.slice(0, 6)));
  const nameFor = (machine: string) => serverFor(machine)?.name ?? (machine === "localhost" ? "panel" : machine);

  async function install(s: Server) {
    setInstalling((m) => ({ ...m, [s.id]: "installing…" }));
    try {
      const j = await waitJob(await api.crowdsecInstall(s.id, true));
      setInstalling((m) => ({ ...m, [s.id]: j.status === "failed" ? `failed: ${j.error}` : "" }));
    } catch (e) {
      setInstalling((m) => ({ ...m, [s.id]: `failed: ${(e as Error).message}` }));
    }
    void load();
  }

  if (ov && !ov.available) {
    return (
      <div className="grid gap-6">
        <Header onRefresh={load} />
        <Card className="p-8 text-center text-sm">
          <ShieldAlert className="mx-auto mb-3 size-8 text-warning" />
          <p className="font-medium">The central CrowdSec LAPI is not reachable.</p>
          <p className="mt-1 text-muted-foreground">{ov.error}</p>
          <p className="mt-3 text-muted-foreground">
            On the panel host run <code className="rounded bg-muted px-1">scripts/crowdsec-central.sh</code> to start it.
          </p>
        </Card>
      </div>
    );
  }

  const machines = ov?.machines ?? [];
  const bouncers = ov?.bouncers ?? [];
  const protectedCount = servers.filter((s) => machines.some((m) => serverFor(m.machineId)?.id === s.id)).length;
  const operate = canOperate(role);

  return (
    <div className="grid gap-6">
      <Header onRefresh={load} lapi={ov?.lapi_url} />
      {(err || msg) && (
        <p className={`rounded-lg border px-4 py-2 text-sm ${err ? "border-destructive/30 bg-destructive/10 text-destructive" : "border-success/30 bg-success/10 text-success"}`}>
          {err || msg}
        </p>
      )}

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Stat label="Protected servers" value={ov ? `${protectedCount} / ${servers.length}` : "…"} />
        <Stat label="Active bans" value={ov ? rows.length : "…"} />
        <Stat label="Alerts (7 days)" value={ov ? (ov.alerts ?? []).length : "…"} />
        <Stat label="Firewall bouncers" value={ov ? bouncers.filter((b) => !b.revoked).length : "…"} />
      </div>

      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_22rem]">
        <div className="grid min-w-0 content-start gap-4">
          <Card>
            <CardHeader className="flex-row flex-wrap items-start justify-between gap-3">
              <div>
                <CardTitle>Active bans</CardTitle>
                <CardDescription>Enforced by the firewall bouncer on every server.</CardDescription>
              </div>
              <Input className="w-48" placeholder="Filter…" value={filter} onChange={(e) => setFilter(e.target.value)} aria-label="Filter bans" />
            </CardHeader>
            <CardContent>
              {!ov ? (
                <p className="text-sm text-muted-foreground">Loading…</p>
              ) : shown.length === 0 ? (
                <p className="text-sm text-muted-foreground">{rows.length ? "No bans match." : "Nothing is banned right now."}</p>
              ) : (
                <div className="overflow-x-auto">
                  <table className="w-full text-sm">
                    <thead className="text-left text-xs text-muted-foreground">
                      <tr>
                        <th className="py-2 pr-3 font-normal">Target</th>
                        <th className="py-2 pr-3 font-normal">Reason</th>
                        <th className="py-2 pr-3 font-normal">Detected by</th>
                        <th className="py-2 pr-3 font-normal">Expires in</th>
                        <th />
                      </tr>
                    </thead>
                    <tbody className="divide-y">
                      {shown.slice(0, 200).map((d) => (
                        <tr key={d.id}>
                          <td className="py-2 pr-3 font-mono text-xs">
                            {d.value}
                            {d.scope.toLowerCase() !== "ip" && <span className="ml-1 text-muted-foreground">({d.scope.toLowerCase()})</span>}
                          </td>
                          <td className="max-w-64 truncate py-2 pr-3 text-xs" title={d.scenario}>
                            {d.scenario}
                          </td>
                          <td className="py-2 pr-3 text-xs">
                            {d.origin === "crowdsec" ? nameFor(d.machine) : <Badge variant="outline">{d.origin === "cscli" ? "manual" : d.origin}</Badge>}
                          </td>
                          <td className="py-2 pr-3 text-xs tabular">{fmtDuration(d.duration)}</td>
                          <td className="py-2 text-right">
                            {operate && (
                              <Button
                                variant="ghost"
                                size="icon"
                                className="size-7"
                                title="Unban everywhere"
                                aria-label={`Unban ${d.value}`}
                                onClick={() => act(() => api.crowdsecUnban(d.id), `Unbanned ${d.value}.`)}
                              >
                                <Undo2 className="size-4" />
                              </Button>
                            )}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>Servers</CardTitle>
              <CardDescription>Each server runs the CrowdSec engine and an nftables bouncer connected to this LAPI.</CardDescription>
            </CardHeader>
            <CardContent>
              <ul className="divide-y text-sm">
                {servers.map((s) => {
                  const m = machines.find((x) => serverFor(x.machineId)?.id === s.id);
                  const b = bouncers.find((x) => serverFor(x.name)?.id === s.id);
                  const state = installing[s.id];
                  return (
                    <li key={s.id} className="flex flex-wrap items-center justify-between gap-2 py-2.5">
                      <Link href={`/servers/${s.id}#crowdsec`} className="font-medium hover:underline">
                        {s.name}
                      </Link>
                      {m ? (
                        <span className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                          <Badge variant={fresh(m.last_heartbeat, 120) ? "success" : "warning"}>
                            engine {m.last_heartbeat ? timeAgo(m.last_heartbeat) : "never seen"}
                          </Badge>
                          <Badge variant={fresh(b?.last_pull, 120) ? "success" : "warning"}>
                            bouncer {b?.last_pull ? timeAgo(b.last_pull) : "never pulled"}
                          </Badge>
                          {m.version && <span className="hidden sm:inline">{m.version.split("-")[0]}</span>}
                        </span>
                      ) : state ? (
                        <span className={`text-xs ${state.startsWith("failed") ? "text-destructive" : "text-muted-foreground"}`}>{state}</span>
                      ) : (
                        <Button size="sm" variant="secondary" disabled={!operate || !s.online} onClick={() => install(s)}>
                          <Download /> Install
                        </Button>
                      )}
                    </li>
                  );
                })}
              </ul>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>Recent alerts</CardTitle>
              <CardDescription>Attacks detected in the last 7 days.</CardDescription>
            </CardHeader>
            <CardContent>
              {(ov?.alerts ?? []).length === 0 ? (
                <p className="text-sm text-muted-foreground">No alerts.</p>
              ) : (
                <ul className="divide-y text-sm">
                  {(ov?.alerts ?? []).slice(0, 50).map((a) => (
                    <li key={a.id} className="grid gap-1 py-2 sm:grid-cols-[1fr_auto_auto_auto] sm:items-center sm:gap-3">
                      <span className="truncate font-mono text-xs" title={a.scenario}>
                        {a.scenario}
                      </span>
                      <span className="font-mono text-xs">
                        {a.source.value}
                        {a.source.cn ? ` · ${a.source.cn}` : ""}
                      </span>
                      <span className="text-xs">{nameFor(a.machine_id)}</span>
                      <span className="text-xs text-muted-foreground">{timeAgo(a.created_at)}</span>
                    </li>
                  ))}
                </ul>
              )}
            </CardContent>
          </Card>
        </div>

        <div className="grid content-start gap-4">
          {operate && <BanForm onBan={(b) => act(() => api.crowdsecBan(b), `Banned ${b.value} on all servers for ${b.duration}.`)} />}
          <Allowlist
            items={ov?.allowlist?.items ?? []}
            editable={isAdmin(role)}
            onChange={(action, v) => act(() => api.crowdsecAllow(action, v), action === "add" ? `${v} will never be banned.` : `${v} removed from the allowlist.`)}
          />
          {isAdmin(role) && <Enroll onEnroll={(k, n) => act(async () => setMsg((await api.crowdsecEnroll(k, n)).result))} />}
        </div>
      </div>
    </div>
  );
}

function Header({ onRefresh, lapi }: { onRefresh: () => void; lapi?: string }) {
  return (
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Security</h1>
        <p className="text-sm text-muted-foreground">
          Central CrowdSec: an attack seen on one server gets the attacker banned on all of them.
          {lapi && <span className="ml-1 font-mono text-xs">LAPI {lapi}</span>}
        </p>
      </div>
      <Button variant="ghost" size="sm" onClick={onRefresh} aria-label="Refresh">
        <RefreshCw />
      </Button>
    </div>
  );
}

function Stat({ label, value }: { label: string; value: string | number }) {
  return (
    <Card className="p-4">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className="tabular mt-1 text-2xl font-semibold">{value}</div>
    </Card>
  );
}

function BanForm({ onBan }: { onBan: (b: { type: string; value: string; duration: string; reason: string }) => Promise<boolean> }) {
  const [type, setType] = useState("ip");
  const [value, setValue] = useState("");
  const [duration, setDuration] = useState("4h");
  const [reason, setReason] = useState("");
  const ph = type === "ip" ? "203.0.113.7" : type === "range" ? "203.0.113.0/24" : "CN";
  return (
    <Card>
      <CardHeader>
        <CardTitle>Ban</CardTitle>
        <CardDescription>Applies to every server with CrowdSec.</CardDescription>
      </CardHeader>
      <CardContent>
        <form
          className="grid gap-3"
          onSubmit={async (e) => {
            e.preventDefault();
            if (await onBan({ type, value: value.trim(), duration, reason: reason.trim() })) {
              setValue("");
              setReason("");
            }
          }}
        >
          <div className="grid grid-cols-2 gap-2">
            <Select value={type} onChange={(e) => setType(e.target.value)} aria-label="Ban type">
              <option value="ip">IP address</option>
              <option value="range">IP range</option>
              <option value="country">Country</option>
            </Select>
            <Select value={duration} onChange={(e) => setDuration(e.target.value)} aria-label="Duration">
              {[
                ["1h", "1 hour"],
                ["4h", "4 hours"],
                ["24h", "1 day"],
                ["7d", "7 days"],
                ["30d", "30 days"],
                ["365d", "1 year"],
              ].map(([v, l]) => (
                <option key={v} value={v}>
                  {l}
                </option>
              ))}
            </Select>
          </div>
          <Input placeholder={ph} value={value} onChange={(e) => setValue(e.target.value)} aria-label="Value to ban" required />
          <Input placeholder="Reason (optional)" value={reason} maxLength={80} onChange={(e) => setReason(e.target.value)} aria-label="Reason" />
          {type === "country" && <p className="text-xs text-muted-foreground">Two-letter country code. Uses CrowdSec's GeoIP lookup.</p>}
          <Button type="submit" variant="destructive" disabled={!value.trim()}>
            <Ban /> Ban everywhere
          </Button>
        </form>
      </CardContent>
    </Card>
  );
}

function Allowlist({
  items,
  editable,
  onChange,
}: {
  items: { value: string }[];
  editable: boolean;
  onChange: (action: "add" | "remove", v: string) => Promise<boolean>;
}) {
  const [v, setV] = useState("");
  return (
    <Card>
      <CardHeader>
        <CardTitle>Allowlist</CardTitle>
        <CardDescription>Never banned. Always includes the panel host and the admin who installed CrowdSec.</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3">
        <ul className="flex flex-wrap gap-2">
          {items.map((i) => (
            <li key={i.value} className="flex items-center gap-1 rounded-md border py-0.5 pr-0.5 pl-2 font-mono text-xs">
              {i.value}
              {editable && (
                <Button
                  variant="ghost"
                  size="icon"
                  className="size-6"
                  aria-label={`Remove ${i.value}`}
                  title="Remove"
                  onClick={() => {
                    if (confirm(`Remove ${i.value} from the allowlist? It could then be banned.`)) void onChange("remove", i.value);
                  }}
                >
                  <X className="size-3.5" />
                </Button>
              )}
            </li>
          ))}
          {items.length === 0 && <li className="text-sm text-muted-foreground">Empty.</li>}
        </ul>
        {editable && (
          <form
            className="flex gap-2"
            onSubmit={async (e) => {
              e.preventDefault();
              if (await onChange("add", v.trim())) setV("");
            }}
          >
            <Input placeholder="IP or CIDR" value={v} onChange={(e) => setV(e.target.value)} aria-label="IP or CIDR to allow" required />
            <Button type="submit" variant="secondary" aria-label="Add to allowlist" disabled={!v.trim()}>
              <Plus />
            </Button>
          </form>
        )}
      </CardContent>
    </Card>
  );
}

function Enroll({ onEnroll }: { onEnroll: (key: string, name: string) => Promise<boolean> }) {
  const [key, setKey] = useState("");
  const [name, setName] = useState("servermgr");
  return (
    <Card>
      <CardHeader>
        <CardTitle>CrowdSec Console</CardTitle>
        <CardDescription>
          Optional: link the LAPI to app.crowdsec.net for community blocklists and dashboards. Paste the enroll key from the
          Console here. It is sent to CrowdSec only and never stored or logged.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form
          className="grid gap-3"
          onSubmit={async (e) => {
            e.preventDefault();
            if (await onEnroll(key.trim(), name.trim())) setKey("");
          }}
        >
          <div className="grid gap-1.5">
            <Label htmlFor="cs-key" className="text-xs">
              Enroll key
            </Label>
            <Input id="cs-key" type="password" autoComplete="off" value={key} onChange={(e) => setKey(e.target.value)} required />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="cs-name" className="text-xs">
              Instance name
            </Label>
            <Input id="cs-name" value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <Button type="submit" variant="secondary" disabled={!key.trim()}>
            <Link2 /> Enroll
          </Button>
        </form>
      </CardContent>
    </Card>
  );
}

function fresh(iso: string | undefined, maxAgeSec: number) {
  return !!iso && Date.now() - Date.parse(iso) < maxAgeSec * 1000;
}

// cscli durations look like "3h59m12.345s"; drop the seconds when hours are shown.
function fmtDuration(d: string) {
  const m = /^(?:(\d+)h)?(?:(\d+)m)?(?:([\d.]+)s)?$/.exec(d);
  if (!m) return d;
  const h = Number(m[1] ?? 0);
  const min = Number(m[2] ?? 0);
  if (h >= 48) return `${Math.floor(h / 24)}d ${h % 24}h`;
  if (h > 0) return `${h}h ${min}m`;
  if (min > 0) return `${min}m`;
  return `${Math.round(Number(m[3] ?? 0))}s`;
}
