import { useEffect, useState } from "react";
import { Link } from "wouter";
import { Lock, Save } from "lucide-react";
import { api, type PanelCert, type Retention } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label } from "@/components/ui/input";
import { Textarea } from "@/components/ui/select";

// Panel-wide settings for admins: the web certificate and how long history
// is kept. Personal settings live on the Account page.
export function Settings() {
  return (
    <div className="grid gap-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Panel settings</h1>
        <p className="text-sm text-muted-foreground">
          Settings for the whole panel. Your password, 2FA and signed-in browsers are on your <Link href="/account" className="text-brand hover:underline">account page</Link>.
        </p>
      </div>
      <PanelCertCard />
      <RetentionCard />
    </div>
  );
}

function PanelCertCard() {
  const [data, setData] = useState<PanelCert | null>(null);
  const [mode, setMode] = useState<"internal" | "acme" | "custom">("internal");
  const [domain, setDomain] = useState("");
  const [email, setEmail] = useState("");
  const [staging, setStaging] = useState(false);
  const [cert, setCert] = useState("");
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const load = () =>
    api
      .panelCert()
      .then((d) => {
        setData(d);
        setMode(d.settings.mode || "internal");
        setDomain(d.settings.domain ?? "");
        setEmail(d.settings.email ?? "");
        setStaging(!!d.settings.staging);
        return d;
      })
      .catch((e) => setError((e as Error).message));
  useEffect(() => {
    void load();
  }, []);
  // While a certificate is being issued, check back every few seconds.
  useEffect(() => {
    if (!data?.settings.issuing) return;
    const t = setInterval(() => void load(), 4000);
    return () => clearInterval(t);
  }, [data?.settings.issuing]);

  async function save(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.setPanelCert(mode === "custom" ? { mode, cert_pem: cert, key_pem: key } : mode === "acme" ? { mode, domain, email, staging } : { mode });
      setCert("");
      setKey("");
      await load();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const s = data?.settings;
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Lock className="size-4" /> Web certificate
          {s?.mode === "acme" && s.issuing && <Badge variant="warning">issuing…</Badge>}
          {s && s.mode !== "internal" && s.expires && !s.issuing && <Badge variant="success">valid until {new Date(s.expires).toLocaleDateString()}</Badge>}
        </CardTitle>
        <CardDescription>
          Agents always reach the panel at {data?.public_host || "its public address"} with the panel's own certificate, so that name can't be used here. Give the
          browser address a separate name (for example panel.example.com) and the panel serves a trusted certificate for it.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={save} className="grid gap-4">
          <div className="flex flex-wrap gap-4 text-sm">
            {(
              [
                ["internal", "Panel's own certificate"],
                ["acme", "Let's Encrypt (DNS check)"],
                ["custom", "Upload a certificate"],
              ] as const
            ).map(([v, l]) => (
              <label key={v} className="flex items-center gap-2">
                <input type="radio" name="cert-mode" checked={mode === v} onChange={() => setMode(v)} /> {l}
              </label>
            ))}
          </div>
          {mode === "acme" && (
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="grid gap-1.5">
                <Label htmlFor="pc-domain">Host name</Label>
                <Input id="pc-domain" required value={domain} placeholder="panel.example.com" onChange={(e) => setDomain(e.target.value)} />
                <p className="text-xs text-muted-foreground">
                  Its zone must be on the <Link href="/dns" className="text-brand hover:underline">DNS page</Link>. The panel proves ownership with a TXT record, so it can stay
                  on a private network.
                </p>
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="pc-email">Email for expiry notices (optional)</Label>
                <Input id="pc-email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} />
                <label className="mt-1 flex items-center gap-2 text-sm">
                  <input type="checkbox" checked={staging} onChange={(e) => setStaging(e.target.checked)} /> Staging (test certificate, not trusted)
                </label>
              </div>
            </div>
          )}
          {mode === "custom" && (
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="grid gap-1.5">
                <Label htmlFor="pc-cert">Certificate chain (PEM)</Label>
                <Textarea id="pc-cert" required rows={6} className="font-mono text-xs" value={cert} onChange={(e) => setCert(e.target.value)} placeholder="-----BEGIN CERTIFICATE-----" />
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="pc-key">Private key (PEM)</Label>
                <Textarea id="pc-key" required rows={6} className="font-mono text-xs" value={key} onChange={(e) => setKey(e.target.value)} placeholder="-----BEGIN PRIVATE KEY-----" />
              </div>
            </div>
          )}
          {s?.domain && s.mode !== "internal" && (
            <p className="text-sm text-muted-foreground">
              Serving {s.domain}
              {s.issuer && ` · issued by ${s.issuer}`}
            </p>
          )}
          {s?.last_error && <p className="text-sm text-destructive">Last attempt failed: {s.last_error}</p>}
          {data && !data.https && <p className="text-sm text-warning">The panel isn't served over HTTPS (SM_PUBLIC_URL is http://), so this has no effect yet.</p>}
          {error && <p className="text-sm text-destructive">{error}</p>}
          <div>
            <Button type="submit" variant="brand" disabled={busy || !!s?.issuing}>
              <Save /> {mode === "acme" ? "Save and request certificate" : "Save"}
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}

const RET_FIELDS: [keyof Retention, string, string][] = [
  ["jobs_days", "Job history", "Finished jobs and their logs"],
  ["audit_days", "Activity log", "Who did what"],
  ["alerts_days", "Alert history", "Resolved alerts"],
  ["runs_days", "Automation runs", "Schedule and playbook runs"],
];

function RetentionCard() {
  const [r, setR] = useState<Retention | null>(null);
  const [msg, setMsg] = useState("");
  const [error, setError] = useState("");
  useEffect(() => {
    api
      .retention()
      .then(setR)
      .catch((e) => setError((e as Error).message));
  }, []);

  async function save(e: React.FormEvent) {
    e.preventDefault();
    if (!r) return;
    setError("");
    setMsg("");
    try {
      setR(await api.setRetention(r));
      setMsg("Saved. Older entries are removed within the hour.");
    } catch (err) {
      setError((err as Error).message);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>History retention</CardTitle>
        <CardDescription>How many days of history the database keeps. 0 keeps everything.</CardDescription>
      </CardHeader>
      <CardContent>
        {r && (
          <form onSubmit={save} className="grid gap-4">
            <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
              {RET_FIELDS.map(([k, label, help]) => (
                <div key={k} className="grid gap-1.5">
                  <Label htmlFor={`ret-${k}`}>{label}</Label>
                  <Input id={`ret-${k}`} type="number" min={0} max={3650} value={r[k]} onChange={(e) => setR({ ...r, [k]: Number(e.target.value) })} />
                  <p className="text-xs text-muted-foreground">{help}</p>
                </div>
              ))}
            </div>
            {error && <p className="text-sm text-destructive">{error}</p>}
            {msg && <p className="text-sm text-success">{msg}</p>}
            <div>
              <Button type="submit" variant="brand">
                <Save /> Save
              </Button>
            </div>
          </form>
        )}
        {!r && error && <p className="text-sm text-destructive">{error}</p>}
      </CardContent>
    </Card>
  );
}
