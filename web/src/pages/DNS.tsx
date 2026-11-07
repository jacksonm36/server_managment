import { useCallback, useEffect, useMemo, useState } from "react";
import { Cloud, CloudOff, ExternalLink, Globe, Pencil, Plus, RefreshCw, Search, Trash2 } from "lucide-react";
import { api, type DNSProvider, type DNSRecord, type DNSType, type DNSZone } from "@/lib/api";
import { useAccess } from "@/lib/access";
import { useVault } from "@/lib/vault";
import { timeAgo } from "@/lib/utils";
import { VaultCard } from "@/components/Vault";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";

// The DNS manager: DNS providers (Cloudflare, Hetzner, DigitalOcean, deSEC,
// PowerDNS), their zones and records. Tokens stay in the panel's vault.

const TTLS: [number, string][] = [
  [1, "Auto"],
  [60, "1 min"],
  [300, "5 min"],
  [1800, "30 min"],
  [3600, "1 hour"],
  [21600, "6 hours"],
  [86400, "1 day"],
];

const short = (zone: string, name: string) => (name === zone ? "@" : name.endsWith("." + zone) ? name.slice(0, -zone.length - 1) : name);

export function DNS() {
  const { can } = useAccess();
  const vault = useVault();
  const [types, setTypes] = useState<DNSType[]>([]);
  const [recordTypes, setRecordTypes] = useState<string[]>([]);
  const [providers, setProviders] = useState<DNSProvider[] | null>(null);
  const [sel, setSel] = useState<{ provider: string; zone: string } | null>(null);
  const [editing, setEditing] = useState<DNSProvider | "new" | null>(null);
  const [error, setError] = useState("");

  const reload = useCallback(async () => {
    try {
      const list = await api.dnsProviders();
      setProviders(list);
      setSel((s) => {
        if (s && list.some((p) => p.id === s.provider)) return s;
        const p = list.find((p) => p.zones.length > 0);
        return p ? { provider: p.id, zone: p.zones[0] } : null;
      });
    } catch (e) {
      setError((e as Error).message);
    }
  }, []);
  useEffect(() => {
    void reload();
    api
      .dnsTypes()
      .then((t) => {
        setTypes(t.providers);
        setRecordTypes(t.record_types);
      })
      .catch((e) => setError((e as Error).message));
  }, [reload]);

  const typeOf = (id: string) => types.find((t) => t.id === id);
  const provider = providers?.find((p) => p.id === sel?.provider);

  async function remove(p: DNSProvider) {
    if (!confirm(`Remove ${p.name} from the panel? Nothing changes at the provider; the panel just forgets the token.`)) return;
    try {
      await api.deleteDNSProvider(p.id);
    } catch (e) {
      setError((e as Error).message);
    }
    await reload();
  }

  return (
    <div className="grid gap-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">DNS</h1>
        <p className="text-sm text-muted-foreground">
          Manage zones at Cloudflare, Hetzner, DigitalOcean, deSEC or your own PowerDNS from one place, and point site names at your servers.
        </p>
      </div>
      {!vault.unlocked && <VaultCard />}
      {error && <p className="text-sm text-destructive">{error}</p>}

      <Card>
        <CardHeader className="flex-row flex-wrap items-start justify-between gap-3">
          <div>
            <CardTitle>Providers</CardTitle>
            <CardDescription>Each provider is an API token. The panel lists its zones to check the token before saving it.</CardDescription>
          </div>
          {can("dns.manage") && (
            <Button variant="brand" size="sm" onClick={() => setEditing("new")} disabled={!vault.unlocked} title={vault.unlocked ? "" : "Unlock the vault first"}>
              <Plus /> Add provider
            </Button>
          )}
        </CardHeader>
        <CardContent className="grid gap-2">
          {providers?.length === 0 && <p className="text-sm text-muted-foreground">No DNS providers yet.</p>}
          {providers?.map((p) => (
            <div key={p.id} className="flex flex-wrap items-center gap-3 rounded-lg border px-4 py-3">
              <Globe className="size-4 text-muted-foreground" />
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-medium">{p.name}</span>
                  <Badge variant="outline">{typeOf(p.type)?.name ?? p.type}</Badge>
                  {p.last_error ? <Badge variant="destructive">error</Badge> : <Badge variant="success">{p.zones.length} zone(s)</Badge>}
                </div>
                <div className="mt-1 truncate text-xs text-muted-foreground">
                  {p.last_error || p.zones.join(", ") || "No zones"} · added {timeAgo(p.created_at)}
                </div>
              </div>
              {can("dns.manage") && (
                <div className="flex gap-1">
                  <Button variant="ghost" size="icon" title="Edit" aria-label="Edit provider" disabled={!vault.unlocked} onClick={() => setEditing(p)}>
                    <Pencil />
                  </Button>
                  <Button variant="ghost" size="icon" title="Remove" aria-label="Remove provider" onClick={() => remove(p)}>
                    <Trash2 className="text-destructive" />
                  </Button>
                </div>
              )}
            </div>
          ))}
        </CardContent>
      </Card>

      {providers && providers.length > 0 && (
        <Zone
          providers={providers}
          sel={sel}
          onSelect={setSel}
          proxy={!!(provider && typeOf(provider.type)?.proxy)}
          minTTL={(provider && typeOf(provider.type)?.min_ttl) || 0}
          recordTypes={recordTypes}
          canEdit={can("dns.edit") && vault.unlocked}
        />
      )}

      {editing && (
        <ProviderDialog
          provider={editing === "new" ? null : editing}
          types={types}
          onClose={() => setEditing(null)}
          onSaved={async (p) => {
            setEditing(null);
            await reload();
            if (p.zones.length) setSel({ provider: p.id, zone: p.zones[0] });
          }}
        />
      )}
    </div>
  );
}

function ProviderDialog({ provider, types, onClose, onSaved }: { provider: DNSProvider | null; types: DNSType[]; onClose: () => void; onSaved: (p: DNSProvider) => void }) {
  const [type, setType] = useState(provider?.type ?? types[0]?.id ?? "cloudflare");
  const [name, setName] = useState(provider?.name ?? "");
  const [config, setConfig] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const t = types.find((x) => x.id === type);
  const keep = provider && provider.type === type; // blank fields keep their stored values

  async function save(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      onSaved(await api.saveDNSProvider(provider?.id ?? null, { name, type, config }));
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{provider ? `Edit ${provider.name}` : "Add a DNS provider"}</DialogTitle>
          <DialogDescription>
            The token is stored encrypted in the vault.{" "}
            {t?.docs_url && (
              <a href={t.docs_url} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-brand hover:underline">
                How to create one <ExternalLink className="size-3" />
              </a>
            )}
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={save} className="grid gap-3">
          <div className="grid gap-1.5">
            <Label htmlFor="dns-type">Provider</Label>
            <Select id="dns-type" value={type} onChange={(e) => setType(e.target.value)}>
              {types.map((x) => (
                <option key={x.id} value={x.id}>
                  {x.name}
                </option>
              ))}
            </Select>
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="dns-name">Name in the panel</Label>
            <Input id="dns-name" value={name} placeholder={t?.name} onChange={(e) => setName(e.target.value)} />
          </div>
          {t?.fields.map((f) => (
            <div key={f.key} className="grid gap-1.5">
              <Label htmlFor={`dns-${f.key}`}>
                {f.label}
                {f.optional && <span className="text-muted-foreground"> (optional)</span>}
              </Label>
              <Input
                id={`dns-${f.key}`}
                type={f.secret ? "password" : "text"}
                autoComplete="off"
                value={config[f.key] ?? ""}
                placeholder={keep ? "unchanged" : f.placeholder}
                required={!f.optional && !keep}
                onChange={(e) => setConfig({ ...config, [f.key]: e.target.value })}
              />
              {f.help && <p className="text-xs text-muted-foreground">{f.help}</p>}
            </div>
          ))}
          {error && <p className="text-sm text-destructive">{error}</p>}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" variant="brand" disabled={busy}>
              {busy ? "Checking…" : "Save"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function Zone({
  providers,
  sel,
  onSelect,
  proxy,
  minTTL,
  recordTypes,
  canEdit,
}: {
  providers: DNSProvider[];
  sel: { provider: string; zone: string } | null;
  onSelect: (s: { provider: string; zone: string }) => void;
  proxy: boolean;
  minTTL: number;
  recordTypes: string[];
  canEdit: boolean;
}) {
  const [zones, setZones] = useState<DNSZone[]>([]);
  const [records, setRecords] = useState<DNSRecord[] | null>(null);
  const [filter, setFilter] = useState("");
  const [editing, setEditing] = useState<DNSRecord | "new" | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (!sel) return;
    api
      .dnsZones(sel.provider)
      .then(setZones)
      .catch((e) => setError((e as Error).message));
  }, [sel?.provider]); // eslint-disable-line react-hooks/exhaustive-deps

  const load = useCallback(async () => {
    if (!sel) return;
    setLoading(true);
    setError("");
    try {
      setRecords((await api.dnsRecords(sel.provider, sel.zone)).records);
    } catch (e) {
      setRecords(null);
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [sel]);
  useEffect(() => {
    void load();
  }, [load]);

  const shown = useMemo(() => {
    const f = filter.trim().toLowerCase();
    return (records ?? []).filter((r) => !f || r.name.includes(f) || r.content.toLowerCase().includes(f) || r.type.toLowerCase() === f);
  }, [records, filter]);

  async function change(method: "POST" | "PUT" | "DELETE", rec: DNSRecord, old?: DNSRecord) {
    if (!sel) return false;
    setError("");
    try {
      await api.dnsRecordChange(sel.provider, sel.zone, method, rec, old);
      await load();
      return true;
    } catch (e) {
      setError((e as Error).message);
      return false;
    }
  }

  const options = providers.flatMap((p) => p.zones.map((z) => ({ value: `${p.id}|${z}`, label: providers.length > 1 ? `${z} (${p.name})` : z })));

  return (
    <Card>
      <CardHeader className="flex-row flex-wrap items-start justify-between gap-3">
        <div className="grid gap-2">
          <CardTitle>Records</CardTitle>
          <Select
            className="w-72"
            aria-label="Zone"
            value={sel ? `${sel.provider}|${sel.zone}` : ""}
            onChange={(e) => {
              const [provider, zone] = e.target.value.split("|");
              onSelect({ provider, zone });
            }}
          >
            {!sel && <option value="">Choose a zone</option>}
            {options.map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </Select>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <div className="relative">
            <Search className="absolute top-2 left-2.5 size-4 text-muted-foreground" />
            <Input className="h-8 w-48 pl-8" placeholder="Filter" value={filter} onChange={(e) => setFilter(e.target.value)} />
          </div>
          <Button variant="ghost" size="sm" onClick={() => void load()} disabled={loading || !sel} aria-label="Refresh">
            <RefreshCw className={loading ? "animate-spin" : ""} />
          </Button>
          {canEdit && (
            <Button variant="brand" size="sm" disabled={!sel || !records} onClick={() => setEditing("new")}>
              <Plus /> Add record
            </Button>
          )}
        </div>
      </CardHeader>
      {error && <p className="px-6 pb-3 text-sm text-destructive">{error}</p>}
      {zones.length > 0 && sel && !zones.some((z) => z.name === sel.zone) && (
        <p className="px-6 pb-3 text-sm text-warning">This zone is no longer at the provider.</p>
      )}
      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead className="text-left text-xs text-muted-foreground">
            <tr>
              <th className="px-4 py-2 font-medium">Type</th>
              <th className="px-4 py-2 font-medium">Name</th>
              <th className="px-4 py-2 font-medium">Content</th>
              <th className="px-4 py-2 font-medium">TTL</th>
              {proxy && <th className="px-4 py-2 font-medium">Proxy</th>}
              <th className="px-4 py-2" />
            </tr>
          </thead>
          <tbody>
            {records && shown.length === 0 && (
              <tr className="border-t">
                <td colSpan={6} className="px-4 py-6 text-center text-muted-foreground">
                  {records.length ? "Nothing matches the filter." : "No records."}
                </td>
              </tr>
            )}
            {sel &&
              shown.map((r, i) => (
                <tr key={`${r.id ?? ""}${r.type}${r.name}${r.content}${i}`} className="border-t align-top">
                  <td className="px-4 py-2">
                    <Badge variant="outline">{r.type}</Badge>
                  </td>
                  <td className="px-4 py-2 font-mono text-xs">{short(sel.zone, r.name)}</td>
                  <td className="max-w-md px-4 py-2 font-mono text-xs break-all">
                    {r.priority !== undefined && r.priority !== null && <span className="text-muted-foreground">{r.priority} </span>}
                    {r.content}
                  </td>
                  <td className="px-4 py-2 text-xs text-muted-foreground">{r.ttl === 1 ? "Auto" : `${r.ttl}s`}</td>
                  {proxy && (
                    <td className="px-4 py-2">
                      {["A", "AAAA", "CNAME"].includes(r.type) &&
                        (r.proxied ? <Cloud className="size-4 text-brand" aria-label="Proxied" /> : <CloudOff className="size-4 text-muted-foreground" aria-label="DNS only" />)}
                    </td>
                  )}
                  <td className="px-4 py-1 text-right whitespace-nowrap">
                    {canEdit && (
                      <>
                        <Button variant="ghost" size="icon" title="Edit" aria-label="Edit record" onClick={() => setEditing(r)}>
                          <Pencil />
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon"
                          title="Delete"
                          aria-label="Delete record"
                          onClick={() => confirm(`Delete ${r.type} ${r.name} → ${r.content}?`) && void change("DELETE", r)}
                        >
                          <Trash2 className="text-destructive" />
                        </Button>
                      </>
                    )}
                  </td>
                </tr>
              ))}
          </tbody>
        </table>
      </div>
      {editing && sel && (
        <RecordDialog
          zone={sel.zone}
          record={editing === "new" ? null : editing}
          recordTypes={recordTypes}
          proxy={proxy}
          minTTL={minTTL}
          onClose={() => setEditing(null)}
          onSubmit={async (rec) => {
            const ok = editing === "new" ? await change("POST", rec) : await change("PUT", rec, editing);
            if (ok) setEditing(null);
            return ok;
          }}
        />
      )}
    </Card>
  );
}

const PLACEHOLDER: Record<string, string> = {
  A: "203.0.113.10",
  AAAA: "2001:db8::10",
  CNAME: "target.example.com",
  MX: "mail.example.com",
  TXT: "v=spf1 include:example.com ~all",
  SRV: "5 5060 sip.example.com (weight port target)",
  CAA: '0 issue "letsencrypt.org"',
  NS: "ns1.example.com",
};

function RecordDialog({
  zone,
  record,
  recordTypes,
  proxy,
  minTTL,
  onClose,
  onSubmit,
}: {
  zone: string;
  record: DNSRecord | null;
  recordTypes: string[];
  proxy: boolean;
  minTTL: number;
  onClose: () => void;
  onSubmit: (r: DNSRecord) => Promise<boolean>;
}) {
  const [type, setType] = useState(record?.type ?? "A");
  const [name, setName] = useState(record ? short(zone, record.name) : "");
  const [content, setContent] = useState(record?.content ?? "");
  const [ttl, setTTL] = useState(record?.ttl ?? (proxy ? 1 : Math.max(3600, minTTL)));
  const [priority, setPriority] = useState(String(record?.priority ?? 10));
  const [proxied, setProxied] = useState(record?.proxied ?? false);
  const [busy, setBusy] = useState(false);
  const canProxy = proxy && ["A", "AAAA", "CNAME"].includes(type);
  const ttls = TTLS.filter(([v]) => (v === 1 ? proxy : v >= minTTL));
  if (!ttls.some(([v]) => v === ttl)) ttls.push([ttl, `${ttl}s`]);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    const r: DNSRecord = { type, name: name.trim() || "@", content, ttl };
    if (type === "MX" || type === "SRV") r.priority = Number(priority) || 0;
    if (canProxy) r.proxied = proxied;
    await onSubmit(r);
    setBusy(false);
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{record ? "Edit record" : "Add record"}</DialogTitle>
          <DialogDescription>In {zone}. Use @ for the zone itself.</DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-3">
          <div className="grid grid-cols-[8rem_1fr] gap-3">
            <div className="grid gap-1.5">
              <Label htmlFor="rec-type">Type</Label>
              <Select id="rec-type" value={type} onChange={(e) => setType(e.target.value)}>
                {recordTypes.map((t) => (
                  <option key={t}>{t}</option>
                ))}
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="rec-name">Name</Label>
              <div className="flex items-center gap-1">
                <Input id="rec-name" value={name} placeholder="@ or www" onChange={(e) => setName(e.target.value)} />
                <span className="shrink-0 text-xs text-muted-foreground">.{zone}</span>
              </div>
            </div>
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="rec-content">Content</Label>
            <Input id="rec-content" required value={content} placeholder={PLACEHOLDER[type]} onChange={(e) => setContent(e.target.value)} className="font-mono" />
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div className="grid gap-1.5">
              <Label htmlFor="rec-ttl">TTL</Label>
              <Select id="rec-ttl" value={ttl} onChange={(e) => setTTL(Number(e.target.value))}>
                {ttls.map(([v, l]) => (
                  <option key={v} value={v}>
                    {l}
                  </option>
                ))}
              </Select>
            </div>
            {(type === "MX" || type === "SRV") && (
              <div className="grid gap-1.5">
                <Label htmlFor="rec-prio">Priority</Label>
                <Input id="rec-prio" type="number" min={0} max={65535} value={priority} onChange={(e) => setPriority(e.target.value)} />
              </div>
            )}
          </div>
          {canProxy && (
            <label className="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={proxied} onChange={(e) => setProxied(e.target.checked)} />
              Proxied through Cloudflare (hides the server's address; HTTP/HTTPS only)
            </label>
          )}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" variant="brand" disabled={busy}>
              {busy ? "Saving…" : "Save"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
