import { useEffect, useState } from "react";
import { Archive, ArchiveRestore, RefreshCw, Save, Trash2 } from "lucide-react";
import { useAgentState } from "@/lib/ops";
import { formatBytes, timeAgo } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Label } from "@/components/ui/input";
import { NumberInput } from "@/components/ui/number-input";
import { Select } from "@/components/ui/select";
import { OpStatus, Offline } from "@/components/OpStatus";

type Policy = { enabled: boolean; every: "daily" | "weekly"; hour: number; keep: number; databases: string[]; last_run?: number; last_error?: string };
type BackupFile = { name: string; size: number; created: number };
type SiteBackups = { domain: string; policy: Policy; backups: BackupFile[] };
type State = { sites: SiteBackups[]; databases: string[] | null; free_bytes: number; root: string };

export function Backups({ serverId, online }: { serverId: string; online: boolean }) {
  const { data, busy, error, job, run } = useAgentState<State>(serverId, "backup.list", online);
  const disabled = !online || !!busy;

  return (
    <div className="grid gap-4">
      <Card>
        <CardHeader className="flex-row flex-wrap items-start justify-between gap-3">
          <div>
            <CardTitle>Backups</CardTitle>
            <CardDescription>
              {data ? `Stored in ${data.root} · ${formatBytes(data.free_bytes)} free` : "Loading…"}
            </CardDescription>
          </div>
          <Button variant="ghost" size="sm" disabled={disabled} onClick={() => run("backup.list")} aria-label="Refresh">
            <RefreshCw />
          </Button>
        </CardHeader>
        <CardContent className="grid gap-3">
          {!online && <Offline />}
          {data?.sites.length === 0 && <p className="text-sm text-muted-foreground">No sites yet. Backups are per site; create one on the Sites tab.</p>}
          <OpStatus busy={busy} error={error} job={job} />
        </CardContent>
      </Card>
      {data?.sites.map((s) => (
        <SiteCard key={s.domain} s={s} databases={data.databases ?? []} disabled={disabled} run={run} />
      ))}
    </div>
  );
}

function SiteCard({
  s,
  databases,
  disabled,
  run,
}: {
  s: SiteBackups;
  databases: string[];
  disabled: boolean;
  run: (op: string, p?: Record<string, string>) => Promise<unknown>;
}) {
  const [pol, setPol] = useState<Policy>(s.policy);
  useEffect(() => setPol(s.policy), [s.policy]);
  const dirty = JSON.stringify(pol) !== JSON.stringify(s.policy);

  const toggleDB = (d: string) =>
    setPol({ ...pol, databases: pol.databases.includes(d) ? pol.databases.filter((x) => x !== d) : [...pol.databases, d] });

  return (
    <Card>
      <CardHeader className="flex-row flex-wrap items-start justify-between gap-3">
        <div>
          <CardTitle className="flex items-center gap-2">
            {s.domain}
            {s.policy.enabled ? <Badge variant="success">scheduled {s.policy.every}</Badge> : <Badge variant="outline">manual only</Badge>}
          </CardTitle>
          <CardDescription>
            {s.backups.length} backup(s)
            {s.policy.last_run ? ` · last scheduled run ${timeAgo(new Date(s.policy.last_run * 1000).toISOString())}` : ""}
            {s.policy.last_error && <span className="text-destructive"> · last run failed: {s.policy.last_error}</span>}
          </CardDescription>
        </div>
        <Button variant="brand" size="sm" disabled={disabled} onClick={() => run("backup.create", { domain: s.domain, databases: pol.databases.join(" ") })}>
          <Archive /> Back up now
        </Button>
      </CardHeader>
      <CardContent className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_20rem]">
        <div className="grid content-start gap-2">
          {s.backups.length === 0 ? (
            <p className="text-sm text-muted-foreground">No backups yet.</p>
          ) : (
            <ul className="grid gap-2">
              {s.backups.map((b) => (
                <li key={b.name} className="flex items-center gap-3 rounded-lg border px-3 py-2">
                  <div className="min-w-0 flex-1">
                    <div className="truncate font-mono text-xs">{b.name}</div>
                    <div className="text-xs text-muted-foreground">
                      {new Date(b.created * 1000).toLocaleString()} · {formatBytes(b.size)}
                    </div>
                  </div>
                  <Button
                    variant="ghost"
                    size="sm"
                    disabled={disabled}
                    onClick={() => {
                      if (!confirm(`Restore ${s.domain} from ${b.name}?\n\nThe site's current files are replaced (logs are kept).`)) return;
                      const dbs = pol.databases.length > 0 && confirm("Also restore the databases in this backup? Current data in them is overwritten.");
                      void run("backup.restore", { domain: s.domain, file: b.name, files: "true", databases: String(dbs) });
                    }}
                  >
                    <ArchiveRestore /> Restore
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    aria-label={`Delete ${b.name}`}
                    disabled={disabled}
                    onClick={() => confirm(`Delete backup ${b.name}?`) && run("backup.delete", { domain: s.domain, file: b.name })}
                  >
                    <Trash2 className="text-destructive" />
                  </Button>
                </li>
              ))}
            </ul>
          )}
        </div>

        <form
          className="grid content-start gap-3 rounded-lg border p-4"
          onSubmit={(e) => {
            e.preventDefault();
            void run("backup.policy", {
              domain: s.domain,
              enabled: String(pol.enabled),
              every: pol.every,
              hour: String(pol.hour),
              keep: String(pol.keep),
              databases: pol.databases.join(" "),
            });
          }}
        >
          <label className="flex cursor-pointer items-center gap-2 text-sm font-medium">
            <input type="checkbox" className="accent-[var(--color-brand)]" checked={pol.enabled} onChange={(e) => setPol({ ...pol, enabled: e.target.checked })} />
            Automatic backups
          </label>
          <div className="grid grid-cols-3 gap-2">
            <div className="grid gap-1.5">
              <Label className="text-xs">Every</Label>
              <Select value={pol.every} onChange={(e) => setPol({ ...pol, every: e.target.value as Policy["every"] })}>
                <option value="daily">Day</option>
                <option value="weekly">Week</option>
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label className="text-xs">At hour</Label>
              <NumberInput min={0} max={23} value={pol.hour} onValue={(n) => setPol({ ...pol, hour: n })} />
            </div>
            <div className="grid gap-1.5">
              <Label className="text-xs">Keep</Label>
              <NumberInput min={1} max={365} value={pol.keep} onValue={(n) => setPol({ ...pol, keep: n })} />
            </div>
          </div>
          <div className="grid gap-1.5">
            <Label className="text-xs">Include databases</Label>
            {databases.length === 0 ? (
              <p className="text-xs text-muted-foreground">No databases on this server.</p>
            ) : (
              <div className="flex flex-wrap gap-3">
                {databases.map((d) => (
                  <label key={d} className="flex cursor-pointer items-center gap-1.5 font-mono text-xs">
                    <input type="checkbox" className="accent-[var(--color-brand)]" checked={pol.databases.includes(d)} onChange={() => toggleDB(d)} />
                    {d}
                  </label>
                ))}
              </div>
            )}
          </div>
          <Button type="submit" variant="secondary" size="sm" disabled={disabled || !dirty}>
            <Save /> Save schedule
          </Button>
        </form>
      </CardContent>
    </Card>
  );
}
