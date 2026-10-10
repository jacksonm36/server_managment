import { useCallback, useEffect, useState } from "react";
import { BellRing, CheckCircle2, Pause, Pencil, Play, Plus, Send, Trash2 } from "lucide-react";
import { api, type AlertEvent, type AlertRule, type Channel } from "@/lib/api";
import { useLive } from "@/lib/live";
import { timeAgo } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { ServerPicker } from "@/components/ServerPicker";

const METRICS = {
  cpu: { label: "CPU usage", unit: "%", def: 90 },
  mem: { label: "Memory usage", unit: "%", def: 90 },
  disk: { label: "Disk usage (/)", unit: "%", def: 85 },
  load: { label: "Load average (1 min)", unit: "", def: 4 },
  offline: { label: "Server offline", unit: "", def: 0 },
} as const;
type Metric = keyof typeof METRICS;

const fmt = (m: string, v: number) => (m === "offline" ? `${Math.round(v)} min` : m === "load" ? v.toFixed(2) : `${v.toFixed(1)}%`);
const dur = (s: number) => (s % 60 === 0 ? `${s / 60} min` : `${s} s`);

export function Alerts() {
  const [rules, setRules] = useState<AlertRule[]>([]);
  const [channels, setChannels] = useState<Channel[]>([]);
  const [events, setEvents] = useState<AlertEvent[]>([]);
  const [editing, setEditing] = useState<AlertRule | null>(null);
  const servers = useLive((s) => s.servers);

  const reload = useCallback(() => {
    void api.alertRules().then(setRules);
    void api.alertEvents().then(setEvents);
  }, []);
  useEffect(() => {
    reload();
    void api.channels().then(setChannels);
    const t = setInterval(() => void api.alertEvents().then(setEvents), 10000);
    return () => clearInterval(t);
  }, [reload]);

  const firing = events.filter((e) => e.state === "firing");
  const blank: AlertRule = {
    id: "",
    name: "",
    metric: "cpu",
    threshold: 90,
    duration_sec: 300,
    server_ids: [],
    all_servers: true,
    channel_ids: channels.map((c) => c.id),
    enabled: true,
  };

  async function save(r: AlertRule) {
    const body = { ...r };
    await (r.id ? api.updateAlertRule(r.id, body) : api.createAlertRule(body));
    reload();
  }

  return (
    <div className="grid gap-4">
      <Card className={firing.length ? "border-destructive/40" : ""}>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            {firing.length ? <BellRing className="size-4 text-destructive" /> : <CheckCircle2 className="size-4 text-success" />}
            {firing.length ? `${firing.length} alert(s) firing` : "All clear"}
          </CardTitle>
        </CardHeader>
        {firing.length > 0 && (
          <CardContent className="grid gap-2">
            {firing.map((e) => (
              <div key={e.id} className="flex flex-wrap items-center gap-2 rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm">
                <span className="font-medium">{e.rule_name}</span>
                <span className="text-muted-foreground">on</span>
                <span className="font-medium">{e.server_name}</span>
                <Badge variant="destructive">{fmt(e.metric, e.value)}</Badge>
                <span className="ml-auto text-xs text-muted-foreground">since {timeAgo(e.started_at)}</span>
              </div>
            ))}
          </CardContent>
        )}
      </Card>

      <div className="flex items-center justify-between gap-3">
        <h2 className="font-semibold">Rules</h2>
        <Button variant="brand" size="sm" onClick={() => setEditing(blank)}>
          <Plus /> New rule
        </Button>
      </div>
      {rules.length === 0 && <p className="text-sm text-muted-foreground">No alert rules yet.</p>}
      {rules.map((r) => (
        <Card key={r.id} className="flex flex-wrap items-center gap-3 p-4">
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-medium">{r.name}</span>
              {!r.enabled && <Badge variant="warning">paused</Badge>}
            </div>
            <div className="mt-1 text-sm text-muted-foreground">
              {r.metric === "offline"
                ? `Offline for ${dur(r.duration_sec)}`
                : `${METRICS[r.metric as Metric].label} above ${fmt(r.metric, r.threshold)} for ${dur(r.duration_sec)}`}
              {" · "}
              {r.all_servers ? "all servers" : r.server_ids.map((id) => servers.get(id)?.name ?? "removed").join(", ")}
              {" · "}
              {r.channel_ids.length ? `notify ${r.channel_ids.map((id) => channels.find((c) => c.id === id)?.name ?? "?").join(", ")}` : "no notifications"}
            </div>
          </div>
          <div className="flex gap-1">
            <Button variant="ghost" size="icon" aria-label={r.enabled ? "Pause" : "Resume"} title={r.enabled ? "Pause" : "Resume"} onClick={() => save({ ...r, enabled: !r.enabled })}>
              {r.enabled ? <Pause /> : <Play className="text-success" />}
            </Button>
            <Button variant="ghost" size="icon" aria-label="Edit" title="Edit" onClick={() => setEditing(r)}>
              <Pencil />
            </Button>
            <Button
              variant="ghost"
              size="icon"
              aria-label="Delete"
              title="Delete"
              onClick={async () => {
                if (!confirm(`Delete alert rule "${r.name}"?`)) return;
                await api.deleteAlertRule(r.id);
                reload();
              }}
            >
              <Trash2 className="text-destructive" />
            </Button>
          </div>
        </Card>
      ))}

      <h2 className="mt-2 font-semibold">History</h2>
      {events.length === 0 ? (
        <p className="text-sm text-muted-foreground">No alerts have fired yet.</p>
      ) : (
        <Card className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="text-left text-xs text-muted-foreground">
              <tr>
                <th className="px-4 py-2 font-medium">Started</th>
                <th className="px-4 py-2 font-medium">Rule</th>
                <th className="px-4 py-2 font-medium">Server</th>
                <th className="px-4 py-2 font-medium">State</th>
                <th className="hidden px-4 py-2 font-medium sm:table-cell">Duration</th>
              </tr>
            </thead>
            <tbody>
              {events.map((e) => (
                <tr key={e.id} className="border-t">
                  <td className="px-4 py-2 whitespace-nowrap text-muted-foreground" title={new Date(e.started_at).toLocaleString()}>
                    {timeAgo(e.started_at)}
                  </td>
                  <td className="px-4 py-2">{e.rule_name}</td>
                  <td className="px-4 py-2">{e.server_name}</td>
                  <td className="px-4 py-2">
                    <Badge variant={e.state === "firing" ? "destructive" : "success"}>{e.state}</Badge>
                  </td>
                  <td className="hidden px-4 py-2 text-muted-foreground tabular sm:table-cell">
                    {e.resolved_at ? `${Math.max(1, Math.round((new Date(e.resolved_at).getTime() - new Date(e.started_at).getTime()) / 60000))} min` : "ongoing"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </Card>
      )}

      {editing && (
        <RuleDialog
          rule={editing}
          channels={channels}
          onClose={() => setEditing(null)}
          onSave={async (r) => {
            await save(r);
            setEditing(null);
          }}
        />
      )}
    </div>
  );
}

function RuleDialog({ rule, channels, onClose, onSave }: { rule: AlertRule; channels: Channel[]; onClose: () => void; onSave: (r: AlertRule) => Promise<void> }) {
  const [r, setR] = useState(rule);
  const [minutes, setMinutes] = useState(String(rule.duration_sec / 60));
  const [error, setError] = useState("");
  const set = (p: Partial<AlertRule>) => setR({ ...r, ...p });
  const isOffline = r.metric === "offline";

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90svh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{rule.id ? "Edit alert rule" : "New alert rule"}</DialogTitle>
          <DialogDescription>Fires when the condition holds for the whole duration, and resolves when it clears.</DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={async (e) => {
            e.preventDefault();
            setError("");
            try {
              await onSave({ ...r, duration_sec: Math.round(Number(minutes) * 60) });
            } catch (err) {
              setError((err as Error).message);
            }
          }}
        >
          <div className="grid gap-2">
            <Label htmlFor="ar-name">Name</Label>
            <Input id="ar-name" value={r.name} onChange={(e) => set({ name: e.target.value })} placeholder="High CPU" required maxLength={100} />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="ar-metric">Condition</Label>
            <Select
              id="ar-metric"
              value={r.metric}
              onChange={(e) => {
                const m = e.target.value as Metric;
                set({ metric: m, threshold: METRICS[m].def });
              }}
            >
              {Object.entries(METRICS).map(([k, v]) => (
                <option key={k} value={k}>
                  {v.label}
                </option>
              ))}
            </Select>
          </div>
          <div className="grid grid-cols-2 gap-3">
            {!isOffline && (
              <div className="grid gap-2">
                <Label htmlFor="ar-th">Above {METRICS[r.metric as Metric].unit}</Label>
                <Input id="ar-th" type="number" step="any" min={0} value={r.threshold} onChange={(e) => set({ threshold: Number(e.target.value) })} />
              </div>
            )}
            <div className="grid gap-2">
              <Label htmlFor="ar-dur">For (minutes)</Label>
              <Input id="ar-dur" type="number" step="0.5" min={0.5} max={isOffline ? 1440 : 14} value={minutes} onChange={(e) => setMinutes(e.target.value)} />
            </div>
          </div>
          <div className="grid gap-2">
            <Label>Servers</Label>
            <label className="flex cursor-pointer items-center gap-2 text-sm">
              <input type="checkbox" className="accent-[var(--color-brand)]" checked={r.all_servers} onChange={(e) => set({ all_servers: e.target.checked })} />
              All servers, including ones added later
            </label>
            {!r.all_servers && <ServerPicker value={r.server_ids} onChange={(ids) => set({ server_ids: ids })} />}
          </div>
          <div className="grid gap-2">
            <Label>Notify</Label>
            {channels.length === 0 ? (
              <p className="text-xs text-muted-foreground">No notification channels yet; add one on the Notifications tab. Alerts still show here.</p>
            ) : (
              <div className="flex flex-wrap gap-3">
                {channels.map((c) => (
                  <label key={c.id} className="flex cursor-pointer items-center gap-1.5 text-sm">
                    <input
                      type="checkbox"
                      className="accent-[var(--color-brand)]"
                      checked={r.channel_ids.includes(c.id)}
                      onChange={(e) => set({ channel_ids: e.target.checked ? [...r.channel_ids, c.id] : r.channel_ids.filter((x) => x !== c.id) })}
                    />
                    {c.name}
                  </label>
                ))}
              </div>
            )}
          </div>
          {error && <p className="text-sm text-destructive">{error}</p>}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" variant="brand">
              Save
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// ---- notification channels ----

const CHANNEL_FIELDS: Record<Channel["type"], { key: string; label: string; placeholder?: string; secret?: boolean; select?: string[] }[]> = {
  email: [
    { key: "host", label: "SMTP host", placeholder: "smtp.example.com" },
    { key: "port", label: "Port", placeholder: "587" },
    { key: "tls", label: "Encryption", select: ["starttls", "tls", "none"] },
    { key: "username", label: "Username (optional)" },
    { key: "password", label: "Password", secret: true },
    { key: "from", label: "From", placeholder: "servermgr <alerts@example.com>" },
    { key: "to", label: "To (comma-separated)", placeholder: "ops@example.com" },
  ],
  telegram: [
    { key: "bot_token", label: "Bot token", placeholder: "123456:ABC…", secret: true },
    { key: "chat_id", label: "Chat ID", placeholder: "-1001234567890" },
  ],
  discord: [{ key: "webhook_url", label: "Webhook URL", placeholder: "https://discord.com/api/webhooks/…", secret: true }],
  webhook: [
    { key: "url", label: "URL", placeholder: "https://hooks.example.com/servermgr" },
    { key: "secret", label: "Signing secret (optional)", secret: true },
  ],
};

export function Notifications() {
  const [list, setList] = useState<Channel[]>([]);
  const [editing, setEditing] = useState<Channel | null>(null);
  const [result, setResult] = useState<Record<string, string>>({});
  const reload = useCallback(() => api.channels().then(setList), []);
  useEffect(() => {
    void reload();
  }, [reload]);

  return (
    <div className="grid gap-4">
      <div className="flex items-center justify-between gap-3">
        <p className="text-sm text-muted-foreground">Where alerts are sent. Secrets are encrypted at rest and never shown again.</p>
        <Button variant="brand" size="sm" onClick={() => setEditing({ id: "", name: "", type: "discord", config: {}, enabled: true, last_sent_at: null, last_error: "" })}>
          <Plus /> New channel
        </Button>
      </div>
      {list.length === 0 && <p className="text-sm text-muted-foreground">No notification channels yet.</p>}
      {list.map((c) => (
        <Card key={c.id} className="flex flex-wrap items-center gap-3 p-4">
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-medium">{c.name}</span>
              <Badge variant="outline">{c.type}</Badge>
              {!c.enabled && <Badge variant="warning">disabled</Badge>}
            </div>
            <div className="mt-1 text-xs text-muted-foreground">
              {result[c.id] ?? (c.last_error ? <span className="text-destructive">Last attempt failed: {c.last_error}</span> : c.last_sent_at ? `Last sent ${timeAgo(c.last_sent_at)}` : "Never used")}
            </div>
          </div>
          <div className="flex gap-1">
            <Button
              variant="ghost"
              size="sm"
              onClick={async () => {
                setResult({ ...result, [c.id]: "Sending test…" });
                try {
                  await api.testChannel(c.id);
                  setResult({ ...result, [c.id]: "Test sent ✓" });
                } catch (e) {
                  setResult({ ...result, [c.id]: `Test failed: ${(e as Error).message}` });
                }
              }}
            >
              <Send /> Test
            </Button>
            <Button variant="ghost" size="icon" aria-label="Edit" onClick={() => setEditing(c)}>
              <Pencil />
            </Button>
            <Button
              variant="ghost"
              size="icon"
              aria-label="Delete"
              onClick={async () => {
                if (!confirm(`Delete channel "${c.name}"?`)) return;
                await api.deleteChannel(c.id);
                void reload();
              }}
            >
              <Trash2 className="text-destructive" />
            </Button>
          </div>
        </Card>
      ))}
      {editing && (
        <ChannelDialog
          c={editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            void reload();
          }}
        />
      )}
    </div>
  );
}

function ChannelDialog({ c, onClose, onSaved }: { c: Channel; onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState(c.name);
  const [type, setType] = useState<Channel["type"]>(c.type);
  const [enabled, setEnabled] = useState(c.enabled);
  const [cfg, setCfg] = useState<Record<string, string>>(c.config.tls ? c.config : { tls: "starttls", port: "587", ...c.config });
  const [error, setError] = useState("");

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90svh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{c.id ? "Edit channel" : "New notification channel"}</DialogTitle>
          <DialogDescription>{c.id ? "Leave a secret as •••••••• to keep the stored value." : "Use Test after saving to check delivery."}</DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={async (e) => {
            e.preventDefault();
            setError("");
            const config: Record<string, string> = {};
            for (const f of CHANNEL_FIELDS[type]) if (cfg[f.key]) config[f.key] = cfg[f.key];
            try {
              await (c.id ? api.updateChannel(c.id, { name, type, config, enabled }) : api.createChannel({ name, type, config, enabled }));
              onSaved();
            } catch (err) {
              setError((err as Error).message);
            }
          }}
        >
          <div className="grid grid-cols-2 gap-3">
            <div className="grid gap-2">
              <Label htmlFor="ch-name">Name</Label>
              <Input id="ch-name" value={name} onChange={(e) => setName(e.target.value)} required maxLength={100} placeholder="Ops Discord" />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="ch-type">Type</Label>
              <Select id="ch-type" value={type} disabled={!!c.id} onChange={(e) => setType(e.target.value as Channel["type"])}>
                <option value="discord">Discord</option>
                <option value="telegram">Telegram</option>
                <option value="email">Email (SMTP)</option>
                <option value="webhook">Webhook</option>
              </Select>
            </div>
          </div>
          {CHANNEL_FIELDS[type].map((f) => (
            <div key={f.key} className="grid gap-2">
              <Label htmlFor={`ch-${f.key}`}>{f.label}</Label>
              {f.select ? (
                <Select id={`ch-${f.key}`} value={cfg[f.key] ?? f.select[0]} onChange={(e) => setCfg({ ...cfg, [f.key]: e.target.value })}>
                  {f.select.map((o) => (
                    <option key={o} value={o}>
                      {o === "starttls" ? "STARTTLS (587)" : o === "tls" ? "TLS (465)" : "None (local relay only)"}
                    </option>
                  ))}
                </Select>
              ) : (
                <Input
                  id={`ch-${f.key}`}
                  type={f.secret ? "password" : "text"}
                  autoComplete="off"
                  placeholder={f.placeholder}
                  value={cfg[f.key] ?? ""}
                  onChange={(e) => setCfg({ ...cfg, [f.key]: e.target.value })}
                />
              )}
            </div>
          ))}
          {type === "webhook" && (
            <p className="text-xs text-muted-foreground">
              POSTs JSON. With a secret, the body is signed in the <code className="font-mono">X-Servermgr-Signature: sha256=…</code> header (HMAC-SHA256).
            </p>
          )}
          <label className="flex cursor-pointer items-center gap-2 text-sm">
            <input type="checkbox" className="accent-[var(--color-brand)]" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
            Enabled
          </label>
          {error && <p className="text-sm text-destructive">{error}</p>}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" variant="brand">
              Save
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
