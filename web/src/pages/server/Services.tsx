import { useMemo, useState } from "react";
import { FileText, Play, Power, PowerOff, RefreshCw, RotateCw, Search, Square } from "lucide-react";
import { useAgentState } from "@/lib/ops";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { OpStatus, Offline } from "@/components/OpStatus";

type Service = { name: string; description: string; load: string; active: string; sub: string; enabled: string };

const SELF = "servermgr-agent.service";

export function Services({ serverId, online, onLogs }: { serverId: string; online: boolean; onLogs: (unit: string) => void }) {
  const { data, busy, error, job, run } = useAgentState<{ services: Service[] }>(serverId, "services.list", online);
  const [q, setQ] = useState("");
  const [filter, setFilter] = useState("running");
  const disabled = !online || !!busy;

  const list = useMemo(() => {
    const all = data?.services ?? [];
    const ql = q.toLowerCase();
    return all.filter((s) => {
      if (ql && !s.name.toLowerCase().includes(ql) && !s.description.toLowerCase().includes(ql)) return false;
      if (filter === "running") return s.sub === "running";
      if (filter === "failed") return s.active === "failed";
      if (filter === "enabled") return s.enabled === "enabled";
      return true;
    });
  }, [data, q, filter]);
  const failed = data?.services.filter((s) => s.active === "failed").length ?? 0;

  const act = (s: Service, action: string) => {
    if ((action === "stop" || action === "disable") && !confirm(`${action} ${s.name}?`)) return;
    void run("services.action", { name: s.name, action });
  };

  return (
    <Card>
      <CardHeader className="flex-row flex-wrap items-start justify-between gap-3">
        <div>
          <CardTitle className="flex items-center gap-2">
            Services {failed > 0 && <Badge variant="destructive">{failed} failed</Badge>}
          </CardTitle>
          <CardDescription>{data ? `${data.services.length} systemd services` : "Loading…"}</CardDescription>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <div className="relative">
            <Search className="pointer-events-none absolute top-2.5 left-2.5 size-4 text-muted-foreground" />
            <Input className="h-9 w-52 pl-8" placeholder="Search" value={q} onChange={(e) => setQ(e.target.value)} />
          </div>
          <Select className="w-32" value={filter} onChange={(e) => setFilter(e.target.value)} aria-label="Filter">
            <option value="running">Running</option>
            <option value="failed">Failed</option>
            <option value="enabled">Enabled</option>
            <option value="all">All</option>
          </Select>
          <Button variant="ghost" size="icon" disabled={disabled} onClick={() => run("services.list")} aria-label="Refresh">
            <RefreshCw />
          </Button>
        </div>
      </CardHeader>
      <CardContent className="grid gap-3">
        {!online && <Offline />}
        {data && list.length === 0 && <p className="text-sm text-muted-foreground">No services match.</p>}
        {list.length > 0 && (
          <div className="max-h-[36rem] overflow-auto rounded-lg border">
            <table className="w-full text-sm">
              <thead className="sticky top-0 bg-card text-left text-xs text-muted-foreground">
                <tr>
                  <th className="px-3 py-2 font-medium">Service</th>
                  <th className="px-3 py-2 font-medium">State</th>
                  <th className="hidden px-3 py-2 font-medium md:table-cell">Boot</th>
                  <th className="px-3 py-2" />
                </tr>
              </thead>
              <tbody>
                {list.map((s) => {
                  const running = s.active === "active";
                  const self = s.name === SELF;
                  return (
                    <tr key={s.name} className="border-t">
                      <td className="px-3 py-1.5">
                        <div className="font-mono text-xs">{s.name}</div>
                        <div className="truncate text-xs text-muted-foreground">{s.description}</div>
                      </td>
                      <td className="px-3 py-1.5">
                        <Badge variant={s.active === "failed" ? "destructive" : running ? "success" : "outline"}>
                          {s.active === "active" ? s.sub : s.active}
                        </Badge>
                      </td>
                      <td className="hidden px-3 py-1.5 text-xs text-muted-foreground md:table-cell">{s.enabled || "—"}</td>
                      <td className="px-1 py-1 text-right whitespace-nowrap">
                        {running ? (
                          <>
                            <Button variant="ghost" size="icon" className="size-8" title="Restart" aria-label={`Restart ${s.name}`} disabled={disabled} onClick={() => act(s, "restart")}>
                              <RotateCw />
                            </Button>
                            <Button variant="ghost" size="icon" className="size-8" title="Stop" aria-label={`Stop ${s.name}`} disabled={disabled || self} onClick={() => act(s, "stop")}>
                              <Square />
                            </Button>
                          </>
                        ) : (
                          <Button variant="ghost" size="icon" className="size-8" title="Start" aria-label={`Start ${s.name}`} disabled={disabled} onClick={() => act(s, "start")}>
                            <Play className="text-success" />
                          </Button>
                        )}
                        {s.enabled === "enabled" ? (
                          <Button variant="ghost" size="icon" className="size-8" title="Disable at boot" aria-label={`Disable ${s.name}`} disabled={disabled || self} onClick={() => act(s, "disable")}>
                            <PowerOff />
                          </Button>
                        ) : s.enabled === "disabled" ? (
                          <Button variant="ghost" size="icon" className="size-8" title="Enable at boot" aria-label={`Enable ${s.name}`} disabled={disabled} onClick={() => act(s, "enable")}>
                            <Power />
                          </Button>
                        ) : null}
                        <Button variant="ghost" size="icon" className="size-8" title="Logs" aria-label={`Logs for ${s.name}`} onClick={() => onLogs(s.name)}>
                          <FileText />
                        </Button>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
        <OpStatus busy={busy} error={error} job={job} />
      </CardContent>
    </Card>
  );
}
