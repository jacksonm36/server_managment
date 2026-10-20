import { useEffect, useState } from "react";
import { ArrowUpCircle, PackageSearch, Power, ShieldAlert } from "lucide-react";
import { api } from "@/lib/api";
import { useAgentState } from "@/lib/ops";
import { timeAgo } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { OpStatus, Offline } from "@/components/OpStatus";

type Pkg = { name: string; current: string; candidate: string; repository: string; security: boolean };
type Check = { count: number; security_count: number; reboot_required: boolean; updates: Pkg[] };

export function Updates({ serverId, online, windows = false }: { serverId: string; online: boolean; windows?: boolean }) {
  // Checking runs apt-get update (or a Windows Update search), so it is not
  // started automatically; the last result is loaded from job history instead.
  const { data, setData, busy, error, job, run } = useAgentState<Check>(serverId, "packages.check_updates", false);
  const [checkedAt, setCheckedAt] = useState<string | null>(null);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [dist, setDist] = useState(false);
  const [autoremove, setAutoremove] = useState(true);
  const [rebootNeeded, setRebootNeeded] = useState(false);

  useEffect(() => {
    api.jobs(serverId).then((jobs) => {
      const last = jobs.find((j) => j.op === "packages.check_updates" && j.status === "succeeded");
      if (last?.result) {
        setData(last.result as Check);
        setCheckedAt(last.finished_at);
      }
    });
  }, [serverId, setData]);

  useEffect(() => setRebootNeeded(!!data?.reboot_required), [data]);

  async function check() {
    const j = await run("packages.check_updates");
    if (j) {
      setCheckedAt(j.finished_at);
      setSelected(new Set());
    }
  }

  async function upgrade(pkgs: string[]) {
    const what = windows
      ? pkgs.length
        ? `${pkgs.length} selected update(s)`
        : "all available updates"
      : pkgs.length
        ? `${pkgs.length} selected package(s)`
        : dist
          ? "all packages (full-upgrade)"
          : "all packages";
    if (!confirm(`${windows ? "Install" : "Upgrade"} ${what}?`)) return;
    const params: Record<string, string> = { dist: String(dist), autoremove: String(autoremove) };
    if (pkgs.length) params.packages = pkgs.join(" ");
    const j = await run("packages.upgrade", params);
    if (j) {
      if ((j.result as { reboot_required?: boolean })?.reboot_required) setRebootNeeded(true);
      await check();
    }
  }

  async function reboot() {
    if (!confirm("Reboot this server now? The agent will reconnect when it is back.")) return;
    await run("system.reboot");
  }

  const toggle = (name: string) =>
    setSelected((s) => {
      const n = new Set(s);
      if (n.has(name)) n.delete(name);
      else n.add(name);
      return n;
    });

  const pkgs = data?.updates ?? [];
  const allSelected = pkgs.length > 0 && selected.size === pkgs.length;

  return (
    <Card>
      <CardHeader className="flex-row flex-wrap items-start justify-between gap-3">
        <div>
          <CardTitle>{windows ? "Windows updates" : "Package updates"}</CardTitle>
          <CardDescription>
            {checkedAt ? `Last checked ${timeAgo(checkedAt)}` : "Not checked yet"} · {windows ? "Windows Update" : "apt (Debian / Ubuntu)"}
          </CardDescription>
        </div>
        <div className="flex flex-wrap gap-2">
          {rebootNeeded && (
            <Button variant="outline" size="sm" disabled={!online || !!busy} onClick={reboot}>
              <Power /> Reboot
            </Button>
          )}
          <Button variant="secondary" size="sm" disabled={!online || !!busy} onClick={check}>
            <PackageSearch /> Check now
          </Button>
        </div>
      </CardHeader>
      <CardContent className="grid gap-4">
        {!online && <Offline />}
        {data && (
          <div className="flex flex-wrap items-center gap-2">
            <Badge variant={data.count ? "warning" : "success"}>
              {data.count ? `${data.count} ${windows ? "available" : "upgradable"}` : "Up to date"}
            </Badge>
            {data.security_count > 0 && (
              <Badge variant="destructive">
                <ShieldAlert className="size-3" /> {data.security_count} security
              </Badge>
            )}
            {rebootNeeded && <Badge variant="warning">reboot required</Badge>}
          </div>
        )}

        {pkgs.length > 0 && (
          <>
            <div className="max-h-96 overflow-auto rounded-lg border">
              <table className="w-full text-sm">
                <thead className="sticky top-0 bg-card text-left text-xs text-muted-foreground">
                  <tr>
                    <th className="w-10 px-3 py-2">
                      <input
                        type="checkbox"
                        aria-label="Select all"
                        className="accent-[var(--color-brand)]"
                        checked={allSelected}
                        onChange={() => setSelected(allSelected ? new Set() : new Set(pkgs.map((p) => p.name)))}
                      />
                    </th>
                    <th className="px-3 py-2 font-medium">{windows ? "Update" : "Package"}</th>
                    <th className="px-3 py-2 font-medium">{windows ? "Severity" : "Installed"}</th>
                    <th className="px-3 py-2 font-medium">{windows ? "Title" : "Available"}</th>
                    <th className="hidden px-3 py-2 font-medium md:table-cell">{windows ? "Category" : "Repository"}</th>
                  </tr>
                </thead>
                <tbody>
                  {pkgs.map((p) => (
                    <tr key={p.name} className="border-t hover:bg-accent/40" onClick={() => toggle(p.name)}>
                      <td className="px-3 py-1.5">
                        <input
                          type="checkbox"
                          aria-label={`Select ${p.name}`}
                          className="accent-[var(--color-brand)]"
                          checked={selected.has(p.name)}
                          onChange={() => toggle(p.name)}
                          onClick={(e) => e.stopPropagation()}
                        />
                      </td>
                      <td className="px-3 py-1.5 font-mono text-xs">
                        {p.name} {p.security && <Badge variant="destructive" className="ml-1 px-1 py-0">security</Badge>}
                      </td>
                      <td className="px-3 py-1.5 font-mono text-xs text-muted-foreground">{p.current}</td>
                      <td className={windows ? "px-3 py-1.5 text-xs" : "px-3 py-1.5 font-mono text-xs"}>{p.candidate}</td>
                      <td className="hidden px-3 py-1.5 text-xs text-muted-foreground md:table-cell">{p.repository}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </>
        )}

        {data && (
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className={windows ? "hidden" : "flex flex-wrap gap-4 text-sm"}>
              <label className="flex cursor-pointer items-center gap-2">
                <input type="checkbox" className="accent-[var(--color-brand)]" checked={dist} onChange={(e) => setDist(e.target.checked)} />
                Full upgrade <span className="text-muted-foreground">(allow new/removed dependencies)</span>
              </label>
              <label className="flex cursor-pointer items-center gap-2">
                <input
                  type="checkbox"
                  className="accent-[var(--color-brand)]"
                  checked={autoremove}
                  onChange={(e) => setAutoremove(e.target.checked)}
                />
                Autoremove
              </label>
            </div>
            <div className="flex flex-wrap gap-2">
              {selected.size > 0 && (
                <Button variant="secondary" size="sm" disabled={!online || !!busy} onClick={() => upgrade([...selected])}>
                  <ArrowUpCircle /> {windows ? "Install" : "Upgrade"} selected ({selected.size})
                </Button>
              )}
              <Button variant="brand" size="sm" disabled={!online || !!busy} onClick={() => upgrade([])}>
                <ArrowUpCircle /> {windows ? "Install all" : "Upgrade all"}
              </Button>
            </div>
          </div>
        )}

        <OpStatus busy={busy} error={error} job={job} />
      </CardContent>
    </Card>
  );
}
