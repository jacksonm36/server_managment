import { useEffect, useState } from "react";
import { api, type DNSLookup } from "@/lib/api";
import { useAccess } from "@/lib/access";
import { Download, ExternalLink, Globe, Waypoints, Lock, LockOpen, Pencil, Plus, RefreshCw, Trash2 } from "lucide-react";
import { useAgentState } from "@/lib/ops";
import { timeAgo } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { OpStatus, Offline } from "@/components/OpStatus";

type Site = {
  domain: string;
  aliases: string[];
  type: "static" | "php" | "proxy";
  php_version?: string;
  proxy_target?: string;
  user: string;
  root: string;
  tls: "" | "selfsigned" | "letsencrypt";
  tls_email?: string;
  tls_staging?: boolean;
  tls_directory?: string;
  tls_expires?: string;
  force_https: boolean;
  created_at: string;
};
type Web = {
  nginx: boolean;
  nginx_version?: string;
  nginx_active?: boolean;
  php_versions: string[];
  sites: Site[];
};

const PHP_CHOICES = ["8.4", "8.3", "8.2", "8.1", "7.4"];
const TYPE_LABEL = { static: "Static", php: "PHP", proxy: "Reverse proxy" } as const;

export function Sites({ serverId, online, address }: { serverId: string; online: boolean; address?: string }) {
  const { can } = useAccess();
  const [dnsFor, setDnsFor] = useState<Site | null>(null);
  const { data, busy, error, job, run } = useAgentState<Web>(serverId, "web.status", online);
  const [editing, setEditing] = useState<Site | "new" | null>(null);
  const [tlsFor, setTlsFor] = useState<Site | null>(null);
  const [installPHP, setInstallPHP] = useState("8.4");
  const disabled = !online || !!busy;

  if (data && !data.nginx) {
    return (
      <Card className="flex flex-col items-center gap-3 p-10 text-center">
        <Globe className="size-8 text-muted-foreground" />
        <div className="font-medium">Web stack not installed</div>
        <p className="max-w-md text-sm text-muted-foreground">
          Installs nginx and PHP-FPM with common extensions. Each site then gets its own Linux user, PHP pool and nginx vhost.
        </p>
        <div className="flex items-center gap-2">
          <Select className="w-36" value={installPHP} onChange={(e) => setInstallPHP(e.target.value)} aria-label="PHP version">
            <option value="">No PHP</option>
            {PHP_CHOICES.map((v) => (
              <option key={v} value={v}>
                PHP {v}
              </option>
            ))}
          </Select>
          <Button variant="brand" disabled={disabled} onClick={() => run("web.install", installPHP ? { php: installPHP } : {})}>
            <Download /> Install nginx{installPHP && ` + PHP ${installPHP}`}
          </Button>
        </div>
        <div className="w-full max-w-xl text-left">
          <OpStatus busy={busy} error={error} job={job} />
        </div>
      </Card>
    );
  }

  const missingPHP = PHP_CHOICES.filter((v) => !data?.php_versions.includes(v));

  async function remove(s: Site) {
    if (!confirm(`Delete ${s.domain}? The nginx vhost and PHP pool are removed.`)) return;
    const files = confirm(`Also delete all files in /srv/sites/${s.domain} and the Linux user ${s.user}?\n\nOK = delete files, Cancel = keep them.`);
    await run("site.delete", { domain: s.domain, delete_files: String(files) });
  }

  return (
    <div className="grid gap-4">
      <Card>
        <CardHeader className="flex-row flex-wrap items-start justify-between gap-3">
          <div>
            <CardTitle className="flex items-center gap-2">
              Websites
              {data && (data.nginx_active ? <Badge variant="success">nginx running</Badge> : <Badge variant="destructive">nginx stopped</Badge>)}
            </CardTitle>
            <CardDescription className="flex flex-wrap items-center gap-1.5">
              {data ? (
                <>
                  {data.nginx_version}
                  {data.php_versions.map((v) => (
                    <Badge key={v} variant="outline">
                      PHP {v}
                    </Badge>
                  ))}
                </>
              ) : (
                "Loading…"
              )}
            </CardDescription>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {data && missingPHP.length > 0 && (
              <div className="flex items-center gap-1">
                <Select className="h-8 w-28" value={missingPHP.includes(installPHP) ? installPHP : missingPHP[0]} onChange={(e) => setInstallPHP(e.target.value)} aria-label="PHP version to install">
                  {missingPHP.map((v) => (
                    <option key={v} value={v}>
                      PHP {v}
                    </option>
                  ))}
                </Select>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={disabled}
                  onClick={() => run("web.php_install", { version: missingPHP.includes(installPHP) ? installPHP : missingPHP[0] })}
                >
                  <Download /> Install
                </Button>
              </div>
            )}
            <Button variant="ghost" size="sm" disabled={disabled} onClick={() => run("web.status")} aria-label="Refresh">
              <RefreshCw />
            </Button>
            <Button variant="brand" size="sm" disabled={disabled || !data} onClick={() => setEditing("new")}>
              <Plus /> Add site
            </Button>
          </div>
        </CardHeader>
        <CardContent className="grid gap-3">
          {!online && <Offline />}
          {data && data.sites.length === 0 && <p className="text-sm text-muted-foreground">No sites yet.</p>}
          {data?.sites.map((s) => {
            const scheme = s.tls ? "https" : "http";
            return (
              <div key={s.domain} className="flex flex-wrap items-center gap-3 rounded-lg border px-4 py-3">
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <a href={`${scheme}://${s.domain}`} target="_blank" rel="noreferrer" className="font-medium hover:text-brand">
                      {s.domain}
                    </a>
                    <Badge variant="outline">
                      {TYPE_LABEL[s.type]}
                      {s.type === "php" && ` ${s.php_version}`}
                    </Badge>
                    {s.tls === "letsencrypt" && <Badge variant="success"><Lock className="size-3" /> Let's Encrypt</Badge>}
                    {s.tls === "selfsigned" && <Badge variant="warning"><Lock className="size-3" /> self-signed</Badge>}
                    {!s.tls && <Badge variant="outline"><LockOpen className="size-3" /> HTTP only</Badge>}
                  </div>
                  <div className="mt-1 truncate text-xs text-muted-foreground">
                    {s.type === "proxy" ? `→ ${s.proxy_target}` : s.root}
                    {s.aliases.length > 0 && ` · also ${s.aliases.join(", ")}`}
                    {s.tls_expires && ` · cert expires ${new Date(s.tls_expires).toLocaleDateString()}`}
                    {` · created ${timeAgo(s.created_at)}`}
                  </div>
                </div>
                <div className="flex gap-1">
                  <Button variant="ghost" size="icon" title="Open site" aria-label="Open site" onClick={() => window.open(`${scheme}://${s.domain}`, "_blank", "noopener")}>
                    <ExternalLink />
                  </Button>
                  {can("dns.edit") && (
                    <Button variant="ghost" size="icon" title="Point DNS here" aria-label="Point DNS here" onClick={() => setDnsFor(s)}>
                      <Waypoints />
                    </Button>
                  )}
                  <Button variant="ghost" size="icon" title="SSL/TLS" aria-label="SSL/TLS" disabled={disabled} onClick={() => setTlsFor(s)}>
                    <Lock />
                  </Button>
                  <Button variant="ghost" size="icon" title="Edit" aria-label="Edit site" disabled={disabled} onClick={() => setEditing(s)}>
                    <Pencil />
                  </Button>
                  <Button variant="ghost" size="icon" title="Delete" aria-label="Delete site" disabled={disabled} onClick={() => remove(s)}>
                    <Trash2 className="text-destructive" />
                  </Button>
                </div>
              </div>
            );
          })}
          <OpStatus busy={busy} error={error} job={job} />
        </CardContent>
      </Card>

      {editing && data && (
        <SiteDialog
          site={editing === "new" ? null : editing}
          phpVersions={data.php_versions}
          onClose={() => setEditing(null)}
          onSubmit={async (params) => {
            const ok = await run(editing === "new" ? "site.create" : "site.update", params);
            if (ok) setEditing(null);
          }}
          busy={!!busy}
        />
      )}
      {dnsFor && <PointDNSDialog site={dnsFor} address={address ?? ""} onClose={() => setDnsFor(null)} />}
      {tlsFor && (
        <TLSDialog
          site={tlsFor}
          onClose={() => setTlsFor(null)}
          onSubmit={async (params) => {
            setTlsFor(null);
            await run("site.tls", { domain: tlsFor.domain, ...params });
          }}
        />
      )}
    </div>
  );
}

function SiteDialog(props: {
  site: Site | null;
  phpVersions: string[];
  busy: boolean;
  onClose: () => void;
  onSubmit: (p: Record<string, string>) => void;
}) {
  const s = props.site;
  const [domain, setDomain] = useState(s?.domain ?? "");
  const [aliases, setAliases] = useState(s?.aliases.join(" ") ?? "");
  const [type, setType] = useState<Site["type"]>(s?.type ?? (props.phpVersions.length ? "php" : "static"));
  const [php, setPhp] = useState(s?.php_version ?? props.phpVersions[0] ?? "");
  const [target, setTarget] = useState(s?.proxy_target ?? "http://127.0.0.1:3000");
  const [forceHttps, setForceHttps] = useState(s?.force_https ?? true);

  function submit(e: React.FormEvent) {
    e.preventDefault();
    const p: Record<string, string> = { domain: domain.trim().toLowerCase(), aliases: aliases.trim(), type };
    if (type === "php") p.php_version = php;
    if (type === "proxy") p.proxy_target = target.trim();
    if (s) p.force_https = String(forceHttps);
    props.onSubmit(p);
  }

  return (
    <Dialog open onOpenChange={(o) => !o && props.onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{s ? `Edit ${s.domain}` : "Add site"}</DialogTitle>
          <DialogDescription>
            {s ? "Changes are applied to nginx with a config test first." : "Creates a Linux user, document root, PHP pool and nginx vhost."}
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4">
          <div className="grid gap-2">
            <Label htmlFor="domain">Domain</Label>
            <Input id="domain" placeholder="example.com" value={domain} onChange={(e) => setDomain(e.target.value)} disabled={!!s} required />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="aliases">Aliases (optional)</Label>
            <Input id="aliases" placeholder="www.example.com" value={aliases} onChange={(e) => setAliases(e.target.value)} />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="type">Type</Label>
            <Select id="type" value={type} onChange={(e) => setType(e.target.value as Site["type"])}>
              <option value="static">Static files</option>
              <option value="php" disabled={!props.phpVersions.length}>
                PHP {props.phpVersions.length ? "" : "(install PHP first)"}
              </option>
              <option value="proxy">Reverse proxy (Node.js, Python, …)</option>
            </Select>
          </div>
          {type === "php" && (
            <div className="grid gap-2">
              <Label htmlFor="php">PHP version</Label>
              <Select id="php" value={php} onChange={(e) => setPhp(e.target.value)}>
                {props.phpVersions.map((v) => (
                  <option key={v} value={v}>
                    PHP {v}
                  </option>
                ))}
              </Select>
            </div>
          )}
          {type === "proxy" && (
            <div className="grid gap-2">
              <Label htmlFor="target">Upstream URL</Label>
              <Input id="target" className="font-mono" value={target} onChange={(e) => setTarget(e.target.value)} required />
            </div>
          )}
          {s && s.tls && (
            <label className="flex cursor-pointer items-center gap-2 text-sm">
              <input type="checkbox" className="accent-[var(--color-brand)]" checked={forceHttps} onChange={(e) => setForceHttps(e.target.checked)} />
              Redirect HTTP to HTTPS
            </label>
          )}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={props.onClose}>
              Cancel
            </Button>
            <Button type="submit" variant="brand" disabled={props.busy || !domain.trim()}>
              {s ? "Save" : "Create site"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function TLSDialog({ site, onClose, onSubmit }: { site: Site; onClose: () => void; onSubmit: (p: Record<string, string>) => void }) {
  const [mode, setMode] = useState<string>(site.tls || "letsencrypt");
  const [email, setEmail] = useState(site.tls_email ?? "");
  const [staging, setStaging] = useState(!!site.tls_staging);
  const [acmeDir, setAcmeDir] = useState(site.tls_directory ?? "");

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>SSL/TLS for {site.domain}</DialogTitle>
          <DialogDescription>Certificates are renewed automatically 30 days before they expire.</DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            const p: Record<string, string> = { mode };
            if (mode === "letsencrypt") {
              if (email.trim()) p.email = email.trim();
              p.staging = String(staging);
              if (acmeDir.trim()) p.acme_directory = acmeDir.trim();
            }
            onSubmit(p);
          }}
        >
          <div className="grid gap-2">
            {[
              { v: "letsencrypt", t: "Let's Encrypt", d: "Free trusted certificate. The domain must point to this server and port 80 must be reachable from the internet." },
              { v: "selfsigned", t: "Self-signed", d: "For LAN and testing. Browsers will show a warning." },
              { v: "off", t: "Off", d: "Serve over plain HTTP only." },
            ].map((o) => (
              <label key={o.v} className={`flex cursor-pointer gap-3 rounded-lg border p-3 ${mode === o.v ? "border-brand/60 bg-brand/5" : ""}`}>
                <input type="radio" name="mode" className="mt-1 accent-[var(--color-brand)]" checked={mode === o.v} onChange={() => setMode(o.v)} />
                <span>
                  <span className="block text-sm font-medium">{o.t}</span>
                  <span className="block text-xs text-muted-foreground">{o.d}</span>
                </span>
              </label>
            ))}
          </div>
          {mode === "letsencrypt" && (
            <>
              <div className="grid gap-2">
                <Label htmlFor="le-email">Email for expiry notices (optional)</Label>
                <Input id="le-email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} />
              </div>
              <label className="flex cursor-pointer items-center gap-2 text-sm">
                <input type="checkbox" className="accent-[var(--color-brand)]" checked={staging} onChange={(e) => setStaging(e.target.checked)} />
                Use the staging environment (testing, untrusted)
              </label>
              <details className="text-sm" open={!!acmeDir}>
                <summary className="cursor-pointer text-muted-foreground">Other ACME server</summary>
                <div className="mt-2 grid gap-1.5">
                  <Input
                    value={acmeDir}
                    onChange={(e) => setAcmeDir(e.target.value)}
                    placeholder="https://acme.zerossl.com/v2/DV90"
                    aria-label="ACME directory URL"
                  />
                  <span className="text-xs text-muted-foreground">Leave empty for Let's Encrypt. Any ACME CA without account binding works, e.g. Buypass or your own step-ca.</span>
                </div>
              </details>
            </>
          )}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" variant="brand">
              Apply
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/** Points a site's names at this server through a DNS provider in the panel. */
function PointDNSDialog({ site, address, onClose }: { site: Site; address: string; onClose: () => void }) {
  const names = [site.domain, ...site.aliases];
  const [found, setFound] = useState<Record<string, DNSLookup>>({});
  const [chosen, setChosen] = useState<Set<string>>(new Set(names));
  const [addrs, setAddrs] = useState(address);
  const [proxied, setProxied] = useState(false);
  const [busy, setBusy] = useState(false);
  const [log, setLog] = useState<{ name: string; ok: boolean; msg: string }[]>([]);

  useEffect(() => {
    for (const n of names)
      api
        .dnsLookup(n)
        .then((r) => setFound((f) => ({ ...f, [n]: r })))
        .catch((e) => setFound((f) => ({ ...f, [n]: { found: false, error: (e as Error).message } })));
  }, [site.domain]); // eslint-disable-line react-hooks/exhaustive-deps

  const cloudflare = Object.values(found).some((f) => f.provider?.type === "cloudflare");
  const list = addrs.split(/[\s,]+/).filter(Boolean);

  async function go() {
    setBusy(true);
    setLog([]);
    for (const n of names) {
      if (!chosen.has(n) || !found[n]?.found) continue;
      try {
        const r = await api.dnsPoint({ name: n, addresses: list, proxied: found[n].provider?.type === "cloudflare" ? proxied : undefined });
        setLog((l) => [...l, { name: n, ok: true, msg: `updated at ${r.provider}` }]);
      } catch (e) {
        setLog((l) => [...l, { name: n, ok: false, msg: (e as Error).message }]);
      }
    }
    setBusy(false);
    for (const n of names) api.dnsLookup(n).then((r) => setFound((f) => ({ ...f, [n]: r })));
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Point DNS at this server</DialogTitle>
          <DialogDescription>
            Sets A/AAAA records for the site's names and removes old ones (and a CNAME, which can't sit beside them). Names must be in a zone on the DNS page.
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="grid gap-2">
            {names.map((n) => {
              const f = found[n];
              return (
                <label key={n} className="flex items-start gap-2 rounded-md border px-3 py-2 text-sm">
                  <input
                    type="checkbox"
                    className="mt-1"
                    disabled={!f?.found}
                    checked={chosen.has(n) && !!f?.found}
                    onChange={(e) => {
                      const c = new Set(chosen);
                      if (e.target.checked) c.add(n);
                      else c.delete(n);
                      setChosen(c);
                    }}
                  />
                  <span className="min-w-0 flex-1">
                    <span className="font-medium">{n}</span>
                    <span className="block text-xs text-muted-foreground">
                      {!f
                        ? "Looking up…"
                        : !f.found
                          ? f.error
                          : `${f.provider?.name} · ${f.zone?.name} · now: ${f.records?.length ? f.records.map((r) => `${r.type} ${r.content}`).join(", ") : "no records"}`}
                    </span>
                  </span>
                </label>
              );
            })}
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="dns-addrs">Server address(es)</Label>
            <Input id="dns-addrs" value={addrs} placeholder="203.0.113.10, 2001:db8::10" onChange={(e) => setAddrs(e.target.value)} />
            <p className="text-xs text-muted-foreground">
              Pre-filled with the address the agent connects from{address ? "" : " (unknown while offline)"}. Behind NAT or a private network, use the public IP instead.
            </p>
          </div>
          {cloudflare && (
            <label className="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={proxied} onChange={(e) => setProxied(e.target.checked)} /> Proxy through Cloudflare
            </label>
          )}
          {log.map((l) => (
            <p key={l.name} className={`text-sm ${l.ok ? "text-success" : "text-destructive"}`}>
              {l.name}: {l.msg}
            </p>
          ))}
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={onClose}>
              Close
            </Button>
            <Button variant="brand" disabled={busy || list.length === 0 || !names.some((n) => chosen.has(n) && found[n]?.found)} onClick={() => void go()}>
              <Waypoints /> {busy ? "Updating…" : "Point DNS here"}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
