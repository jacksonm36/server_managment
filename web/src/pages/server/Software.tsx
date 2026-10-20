import { useEffect, useMemo, useState } from "react";
import { Download, RefreshCw, Search, Trash2 } from "lucide-react";
import { useAgentState } from "@/lib/ops";
import { useAccess } from "@/lib/access";
import { pve, type WinAppInfo } from "@/lib/pveapi";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { OpStatus, Offline } from "@/components/OpStatus";
import { AppPicker } from "@/components/WindowsManage";

type Program = { id: string; name: string; version: string; publisher: string; install_date?: string; size_kb?: number; can_uninstall: boolean };

// Software on a Windows server: what is installed (removable when it has a
// silent uninstaller) and programs from the panel's catalog, each checked
// against its publisher's signature or a SHA-256 before it runs.
export function Software({ serverId, online }: { serverId: string; online: boolean }) {
  const { data, busy, error, job, run } = useAgentState<{ programs: Program[]; summary?: string }>(serverId, "software.list", online);
  const { can } = useAccess();
  const [catalog, setCatalog] = useState<WinAppInfo[]>([]);
  const [picked, setPicked] = useState<string[]>([]);
  const [q, setQ] = useState("");
  const [programs, setPrograms] = useState<Program[]>([]);
  const disabled = !online || !!busy || !can("server.system", serverId);

  useEffect(() => {
    pve.winApps().then(setCatalog, () => setCatalog([]));
  }, []);
  // An install answers with a summary, not the list: keep the last list.
  useEffect(() => {
    if (data?.programs) setPrograms(data.programs);
  }, [data]);

  const list = useMemo(() => {
    const ql = q.toLowerCase();
    return programs.filter((p) => !ql || p.name.toLowerCase().includes(ql) || p.publisher.toLowerCase().includes(ql));
  }, [programs, q]);

  async function install() {
    const j = await run("software.install", { apps: picked.join(" ") });
    if (j) setPicked([]);
    void run("software.list");
  }

  return (
    <div className="grid gap-6">
      <Card>
        <CardHeader className="flex-row flex-wrap items-start justify-between gap-3">
          <div>
            <CardTitle>Installed programs</CardTitle>
            <CardDescription>{data || programs.length ? `${programs.length} programs` : "Loading…"}</CardDescription>
          </div>
          <div className="flex items-center gap-2">
            <div className="relative">
              <Search className="pointer-events-none absolute top-2.5 left-2.5 size-4 text-muted-foreground" />
              <Input className="h-9 w-52 pl-8" placeholder="Search" value={q} onChange={(e) => setQ(e.target.value)} />
            </div>
            <Button variant="ghost" size="icon" disabled={!online || !!busy} onClick={() => run("software.list")} aria-label="Refresh">
              <RefreshCw />
            </Button>
          </div>
        </CardHeader>
        <CardContent className="grid gap-3">
          {!online && <Offline />}
          <OpStatus busy={busy} error={error} job={job} />
          {list.length > 0 && (
            <div className="max-h-[32rem] overflow-auto rounded-lg border">
              <table className="w-full text-sm">
                <thead className="sticky top-0 bg-card text-left text-xs text-muted-foreground">
                  <tr>
                    <th className="px-3 py-2 font-medium">Program</th>
                    <th className="hidden px-3 py-2 font-medium md:table-cell">Publisher</th>
                    <th className="px-3 py-2 font-medium">Version</th>
                    <th className="px-3 py-2" />
                  </tr>
                </thead>
                <tbody>
                  {list.map((p) => (
                    <tr key={p.id} className="border-t">
                      <td className="px-3 py-1.5 font-medium">{p.name}</td>
                      <td className="hidden px-3 py-1.5 text-muted-foreground md:table-cell">{p.publisher}</td>
                      <td className="tabular px-3 py-1.5 text-muted-foreground">{p.version}</td>
                      <td className="px-3 py-1.5 text-right">
                        <Button
                          variant="ghost"
                          size="icon"
                          disabled={disabled || !p.can_uninstall}
                          title={p.can_uninstall ? `Uninstall ${p.name}` : "No silent uninstaller: remove it in the terminal or over RDP"}
                          aria-label={`Uninstall ${p.name}`}
                          onClick={() => confirm(`Uninstall ${p.name}?`) && void run("software.uninstall", { id: p.id })}
                        >
                          <Trash2 />
                        </Button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </CardContent>
      </Card>

      {can("server.system", serverId) && (
        <Card>
          <CardHeader className="flex-row flex-wrap items-start justify-between gap-3">
            <div>
              <CardTitle>Install from the catalog</CardTitle>
              <CardDescription>
                Downloaded from the vendor over HTTPS and installed silently, only if the publisher's signature (or the SHA-256) checks out.
              </CardDescription>
            </div>
            <Button disabled={disabled || picked.length === 0} onClick={() => void install()}>
              <Download /> Install {picked.length > 0 ? picked.length : ""}
            </Button>
          </CardHeader>
          <CardContent>
            {catalog.length > 0 ? <AppPicker apps={catalog} picked={picked} onChange={setPicked} /> : <p className="text-sm text-muted-foreground">Loading the catalog…</p>}
          </CardContent>
        </Card>
      )}
    </div>
  );
}
