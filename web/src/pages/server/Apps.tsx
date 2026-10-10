import { useEffect, useState } from "react";
import { Check, Copy, Download, ExternalLink, LayoutGrid, Loader2, RefreshCw, Trash2, TriangleAlert } from "lucide-react";
import { api, type CatalogApp, type PlaybookRun } from "@/lib/api";
import { useAgentState } from "@/lib/ops";
import { useLive } from "@/lib/live";
import { canOperate, useRole } from "@/lib/role";
import { timeAgo } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Offline } from "@/components/OpStatus";

type SiteApp = { id: string; name: string; version: string; db?: string; installed_at: string; updated_at?: string };
type Site = { domain: string; tls: string; app?: SiteApp };
type Web = { nginx: boolean; php_versions: string[]; sites: Site[] };

const cmpVer = (a: string, b: string) => {
  const [x1, y1] = a.split(".").map(Number);
  const [x2, y2] = b.split(".").map(Number);
  return x1 - x2 || y1 - y2;
};

export function Apps({ serverId, online }: { serverId: string; online: boolean }) {
  const role = useRole();
  const { data, run } = useAgentState<Web>(serverId, "web.status", online);
  const [catalog, setCatalog] = useState<CatalogApp[]>([]);
  const [installing, setInstalling] = useState<CatalogApp | null>(null);
  const [result, setResult] = useState<{ creds: Record<string, string>; notes?: string; runId: string } | null>(null);
  const [runState, setRunState] = useState<PlaybookRun | null>(null);
  const [error, setError] = useState("");
  const server = useLive((s) => s.servers.get(serverId));

  useEffect(() => {
    void api.apps().then(setCatalog);
  }, []);

  // Follow the install/update/uninstall run until it finishes.
  useEffect(() => {
    const id = result?.runId ?? runState?.id;
    if (!id || (runState && runState.status !== "running")) return;
    const t = setTimeout(async () => {
      const r = (await api.playbookRuns()).find((x) => x.id === id) ?? null;
      setRunState(r);
      if (r && r.status !== "running") void run("web.status");
    }, 2500);
    return () => clearTimeout(t);
  }, [result, runState, run]);

  if (!online) return <Offline />;
  if (data && !data.nginx) {
    return (
      <Card className="p-8 text-center text-sm text-muted-foreground">
        Install the web stack (nginx + PHP) on the Sites tab first. Apps also need MariaDB (Databases tab).
      </Card>
    );
  }

  const installed = (data?.sites ?? []).filter((s) => s.app);
  const lowMem = server?.latest && server.latest.mem_total < 900 * 1024 * 1024;
  const op = canOperate(role);

  async function follow(p: Promise<PlaybookRun[]>) {
    setError("");
    try {
      const runs = await p;
      setRunState(runs[0]);
    } catch (e) {
      setError((e as Error).message);
    }
  }

  return (
    <div className="grid gap-4">
      {result && <Credentials creds={result.creds} notes={result.notes} onClose={() => setResult(null)} />}
      {runState && <RunProgress run={runState} onClose={() => setRunState(null)} />}
      {error && <p className="text-sm text-destructive">{error}</p>}

      <Card>
        <CardHeader className="flex-row items-start justify-between gap-3">
          <div>
            <CardTitle>Installed apps</CardTitle>
            <CardDescription>One-click apps on this server's sites.</CardDescription>
          </div>
          <Button variant="ghost" size="icon" aria-label="Refresh" onClick={() => run("web.status")}>
            <RefreshCw />
          </Button>
        </CardHeader>
        <CardContent className="grid gap-2">
          {installed.length === 0 && <p className="text-sm text-muted-foreground">No apps installed yet. Pick one below.</p>}
          {installed.map((s) => {
            const tpl = catalog.find((c) => c.id === s.app!.id);
            const url = `${s.tls ? "https" : "http"}://${s.domain}`;
            return (
              <div key={s.domain} className="flex flex-wrap items-center gap-3 rounded-lg border px-4 py-3">
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-medium">{s.app!.name}</span>
                    <Badge variant="outline">{s.app!.version}</Badge>
                    <a href={url} target="_blank" rel="noreferrer" className="text-sm text-muted-foreground hover:text-brand">
                      {s.domain}
                    </a>
                  </div>
                  <div className="mt-0.5 text-xs text-muted-foreground">
                    installed {timeAgo(s.app!.installed_at)}
                    {s.app!.updated_at && ` · updated ${timeAgo(s.app!.updated_at)}`}
                    {s.app!.db && ` · database ${s.app!.db}`}
                  </div>
                </div>
                <div className="flex gap-1">
                  {tpl?.admin_path && (
                    <Button variant="ghost" size="sm" onClick={() => window.open(url + tpl.admin_path, "_blank", "noopener")}>
                      <ExternalLink /> Admin
                    </Button>
                  )}
                  {op && tpl?.updatable && (
                    <Button variant="ghost" size="sm" onClick={() => follow(api.updateApp(serverId, s.domain, s.app!.id))}>
                      <Download /> Update
                    </Button>
                  )}
                  {op && (
                    <Button
                      variant="ghost"
                      size="icon"
                      aria-label="Uninstall"
                      onClick={() => {
                        if (!confirm(`Uninstall ${s.app!.name} from ${s.domain}?\n\nThis deletes the site, all its files${s.app!.db ? ` and the database ${s.app!.db}` : ""}. Take a backup first if you need one.`)) return;
                        void follow(api.uninstallApp(serverId, s.domain, s.app!.db ?? ""));
                      }}
                    >
                      <Trash2 className="text-destructive" />
                    </Button>
                  )}
                </div>
              </div>
            );
          })}
        </CardContent>
      </Card>

      <div className="flex items-center gap-2 pt-2">
        <LayoutGrid className="size-4 text-brand" />
        <h2 className="font-semibold">App catalog</h2>
      </div>
      {lowMem && (
        <p className="flex items-center gap-2 rounded-lg border border-warning/30 bg-warning/10 px-3 py-2 text-sm text-warning">
          <TriangleAlert className="size-4 shrink-0" /> This server has under 1 GB of RAM; PHP apps with a database may run out of memory.
        </p>
      )}
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
        {catalog.map((c) => (
          <Card key={c.id} className="flex flex-col gap-3 p-5">
            <div className="flex items-start justify-between gap-2">
              <div>
                <div className="font-semibold">{c.name}</div>
                <div className="text-xs text-muted-foreground">
                  {c.category} · PHP {c.php_min}+{c.version !== "latest" && ` · ${c.version}`}
                </div>
              </div>
              {c.database && <Badge variant="outline">MariaDB</Badge>}
            </div>
            <p className="flex-1 text-sm text-muted-foreground">{c.description}</p>
            <div className="flex items-center justify-between gap-2">
              <a href={c.homepage} target="_blank" rel="noreferrer" className="text-xs text-muted-foreground hover:text-foreground">
                {c.homepage.replace(/^https?:\/\//, "")}
              </a>
              {op && (
                <Button variant="brand" size="sm" disabled={!data} onClick={() => setInstalling(c)}>
                  <Download /> Install
                </Button>
              )}
            </div>
          </Card>
        ))}
      </div>

      {installing && data && (
        <InstallDialog
          app={installing}
          phpVersions={data.php_versions.filter((v) => cmpVer(v, installing.php_min) >= 0 && (!installing.php_max || cmpVer(v, installing.php_max) <= 0))}
          onClose={() => setInstalling(null)}
          onInstall={async (body) => {
            const r = await api.installApp(serverId, body);
            setInstalling(null);
            setResult({ creds: r.credentials, notes: r.notes, runId: r.runs[0].id });
            setRunState(r.runs[0]);
          }}
        />
      )}
    </div>
  );
}

function RunProgress({ run, onClose }: { run: PlaybookRun; onClose: () => void }) {
  return (
    <Card className={run.status === "failed" ? "border-destructive/40" : run.status === "succeeded" ? "border-success/40" : ""}>
      <CardContent className="grid gap-2 pt-5">
        <div className="flex items-center justify-between gap-2 text-sm">
          <span className="flex items-center gap-2 font-medium">
            {run.status === "running" && <Loader2 className="size-4 animate-spin text-brand" />}
            {run.playbook_name}
            <Badge variant={run.status === "succeeded" ? "success" : run.status === "failed" ? "destructive" : "warning"}>{run.status}</Badge>
          </span>
          {run.status !== "running" && (
            <Button variant="ghost" size="sm" onClick={onClose}>
              Dismiss
            </Button>
          )}
        </div>
        <ol className="grid gap-1 text-sm">
          {run.steps.map((s, i) => (
            <li key={i} className="flex items-center gap-2">
              <span className={`size-2 shrink-0 rounded-full ${s.status === "succeeded" ? "bg-success" : s.status === "failed" ? "bg-destructive" : s.status === "running" ? "animate-pulse bg-warning" : "bg-muted-foreground/40"}`} />
              <span className="font-mono text-xs">
                {s.rollback && <span className="text-warning">undo </span>}
                {s.op}
              </span>
              <span className="text-xs text-muted-foreground">{s.status}</span>
              {s.error && <span className="truncate text-xs text-destructive">{s.error}</span>}
            </li>
          ))}
        </ol>
        {run.status === "failed" && <p className="text-xs text-muted-foreground">Anything the install created has been removed again. Details are in the server's Tasks tab.</p>}
      </CardContent>
    </Card>
  );
}

function Credentials({ creds, notes, onClose }: { creds: Record<string, string>; notes?: string; onClose: () => void }) {
  const [copied, setCopied] = useState("");
  const rows: [string, string][] = [
    ["Site", creds.url],
    ["Admin URL", creds.admin_url],
    ["Admin user", creds.admin_user],
    ["Admin password", creds.admin_password],
    ["Database", creds.db_name],
    ["DB user", creds.db_user],
    ["DB password", creds.db_password],
  ].filter(([, v]) => v) as [string, string][];
  return (
    <Card className="border-brand/40 bg-brand/5">
      <CardHeader>
        <CardTitle>Save these credentials now</CardTitle>
        <CardDescription>They are shown once and never stored by the panel. The install continues in the background below.</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3">
        <div className="grid gap-1.5 rounded-lg border bg-background/60 p-3 text-sm">
          {rows.map(([k, v]) => (
            <div key={k} className="flex items-center gap-2">
              <span className="w-32 shrink-0 text-muted-foreground">{k}</span>
              <code className="min-w-0 flex-1 truncate font-mono text-xs">{v}</code>
              <Button
                variant="ghost"
                size="icon"
                className="size-7"
                aria-label={`Copy ${k}`}
                onClick={async () => {
                  await navigator.clipboard.writeText(v).catch(() => {});
                  setCopied(k);
                }}
              >
                {copied === k ? <Check className="text-success" /> : <Copy />}
              </Button>
            </div>
          ))}
        </div>
        {notes && <p className="text-sm text-muted-foreground">{notes}</p>}
        <Button variant="secondary" className="w-fit" onClick={() => confirm("Have you saved the credentials? They cannot be shown again.") && onClose()}>
          I saved them
        </Button>
      </CardContent>
    </Card>
  );
}

function InstallDialog({
  app,
  phpVersions,
  onClose,
  onInstall,
}: {
  app: CatalogApp;
  phpVersions: string[];
  onClose: () => void;
  onInstall: (b: { app: string; domain: string; aliases: string; php_version: string; title: string; admin_user: string; admin_email: string; tls: string }) => Promise<void>;
}) {
  const [domain, setDomain] = useState("");
  const [aliases, setAliases] = useState("");
  const [php, setPhp] = useState(phpVersions[0] ?? "");
  const [title, setTitle] = useState("");
  const [adminUser, setAdminUser] = useState("admin");
  const [adminEmail, setAdminEmail] = useState("");
  const [tls, setTls] = useState("selfsigned");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90svh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>Install {app.name}</DialogTitle>
          <DialogDescription>Creates a new site{app.database && ", a database and a database user"}, installs {app.name} and generates the passwords.</DialogDescription>
        </DialogHeader>
        {phpVersions.length === 0 ? (
          <p className="text-sm text-destructive">
            {app.name} needs PHP {app.php_min}
            {app.php_max && `–${app.php_max}`}; install a suitable version on the Sites tab first.
          </p>
        ) : (
          <form
            className="grid gap-4"
            onSubmit={async (e) => {
              e.preventDefault();
              setBusy(true);
              setError("");
              try {
                await onInstall({ app: app.id, domain: domain.trim().toLowerCase(), aliases, php_version: php, title: title || domain, admin_user: adminUser, admin_email: adminEmail, tls });
              } catch (err) {
                setError((err as Error).message);
              } finally {
                setBusy(false);
              }
            }}
          >
            <div className="grid gap-2">
              <Label htmlFor="ai-domain">Domain</Label>
              <Input id="ai-domain" placeholder="blog.example.com" value={domain} onChange={(e) => setDomain(e.target.value)} required />
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div className="grid gap-2">
                <Label htmlFor="ai-aliases">Aliases (optional)</Label>
                <Input id="ai-aliases" placeholder="www.blog.example.com" value={aliases} onChange={(e) => setAliases(e.target.value)} />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="ai-php">PHP</Label>
                <Select id="ai-php" value={php} onChange={(e) => setPhp(e.target.value)}>
                  {phpVersions.map((v) => (
                    <option key={v} value={v}>
                      PHP {v}
                    </option>
                  ))}
                </Select>
              </div>
            </div>
            {app.admin && (
              <>
                <div className="grid gap-2">
                  <Label htmlFor="ai-title">Site title</Label>
                  <Input id="ai-title" placeholder={domain || "My site"} value={title} onChange={(e) => setTitle(e.target.value)} />
                </div>
                <div className="grid grid-cols-2 gap-3">
                  <div className="grid gap-2">
                    <Label htmlFor="ai-user">Admin user</Label>
                    <Input id="ai-user" value={adminUser} onChange={(e) => setAdminUser(e.target.value)} required />
                  </div>
                  <div className="grid gap-2">
                    <Label htmlFor="ai-email">Admin email</Label>
                    <Input id="ai-email" type="email" value={adminEmail} onChange={(e) => setAdminEmail(e.target.value)} required />
                  </div>
                </div>
              </>
            )}
            <div className="grid gap-2">
              <Label htmlFor="ai-tls">HTTPS</Label>
              <Select id="ai-tls" value={tls} onChange={(e) => setTls(e.target.value)}>
                <option value="letsencrypt">Let's Encrypt (domain must point here, port 80 open)</option>
                <option value="selfsigned">Self-signed (LAN / testing)</option>
                <option value="">None (HTTP only)</option>
              </Select>
            </div>
            {error && <p className="text-sm text-destructive">{error}</p>}
            <div className="flex justify-end gap-2">
              <Button type="button" variant="ghost" onClick={onClose}>
                Cancel
              </Button>
              <Button type="submit" variant="brand" disabled={busy || !domain}>
                <Download /> Install
              </Button>
            </div>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}
