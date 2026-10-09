import { useState } from "react";
import { Download, Plus, Power, PowerOff, RefreshCw, ShieldCheck, ShieldOff, Trash2 } from "lucide-react";
import { useAgentState } from "@/lib/ops";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { OpStatus, Offline } from "@/components/OpStatus";

type Rule = { num: number; to: string; action: string; from: string; v6: boolean; raw: string };
type FW = {
  installed: boolean;
  active?: boolean;
  defaults?: string;
  logging?: string;
  ssh_port?: string;
  rules?: Rule[];
};

const PRESETS = [
  { label: "SSH", port: "22", proto: "tcp" },
  { label: "HTTP", port: "80", proto: "tcp" },
  { label: "HTTPS", port: "443", proto: "tcp" },
  { label: "HTTP/3", port: "443", proto: "udp" },
];

export function Firewall({ serverId, online }: { serverId: string; online: boolean }) {
  const { data, busy, error, job, run } = useAgentState<FW>(serverId, "firewall.status", online);
  const [action, setAction] = useState("allow");
  const [port, setPort] = useState("");
  const [proto, setProto] = useState("tcp");
  const [from, setFrom] = useState("");
  const [comment, setComment] = useState("");
  const disabled = !online || !!busy;

  async function addRule(e: React.FormEvent) {
    e.preventDefault();
    const p: Record<string, string> = { action, port: port.trim(), proto };
    if (from.trim()) p.from = from.trim();
    if (comment.trim()) p.comment = comment.trim();
    if (await run("firewall.rule_add", p)) {
      setPort("");
      setFrom("");
      setComment("");
    }
  }

  async function del(r: Rule) {
    if (!confirm(`Delete rule ${r.num}: ${r.action} ${r.to} from ${r.from}?`)) return;
    await run("firewall.rule_delete", { num: String(r.num) });
  }

  async function toggle() {
    if (data?.active) {
      if (!confirm("Disable the firewall? All incoming traffic will be allowed.")) return;
      await run("firewall.disable");
    } else {
      if (!confirm(`Enable UFW? SSH (port ${data?.ssh_port ?? 22}/tcp) is allowed with rate limiting first, so you stay connected.`))
        return;
      await run("firewall.enable");
    }
  }

  if (data && !data.installed) {
    return (
      <Card className="flex flex-col items-center gap-3 p-10 text-center">
        <ShieldOff className="size-8 text-muted-foreground" />
        <div className="font-medium">UFW is not installed</div>
        <p className="max-w-sm text-sm text-muted-foreground">Install UFW to manage this server's firewall from the panel.</p>
        <Button variant="brand" disabled={disabled} onClick={() => run("firewall.install")}>
          <Download /> Install UFW
        </Button>
        <div className="w-full max-w-xl text-left">
          <OpStatus busy={busy} error={error} job={job} />
        </div>
      </Card>
    );
  }

  return (
    <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_22rem]">
      <Card>
        <CardHeader className="flex-row flex-wrap items-start justify-between gap-3">
          <div>
            <CardTitle className="flex items-center gap-2">
              Firewall (UFW)
              {data && (data.active ? <Badge variant="success"><ShieldCheck className="size-3" /> active</Badge> : <Badge variant="outline">inactive</Badge>)}
            </CardTitle>
            <CardDescription>{data?.defaults ? `Default: ${data.defaults}` : "Loading…"}</CardDescription>
          </div>
          <div className="flex gap-2">
            <Button variant="ghost" size="sm" disabled={disabled} onClick={() => run("firewall.status")} aria-label="Refresh">
              <RefreshCw />
            </Button>
            {data && (
              <Button variant={data.active ? "outline" : "brand"} size="sm" disabled={disabled} onClick={toggle}>
                {data.active ? <PowerOff /> : <Power />} {data.active ? "Disable" : "Enable"}
              </Button>
            )}
          </div>
        </CardHeader>
        <CardContent className="grid gap-4">
          {!online && <Offline />}
          {data?.active && (data.rules?.length ?? 0) === 0 && <p className="text-sm text-muted-foreground">No rules.</p>}
          {data && !data.active && (
            <p className="text-sm text-muted-foreground">The firewall is off. Rules you add are applied once it is enabled.</p>
          )}
          {!!data?.rules?.length && (
            <div className="overflow-auto rounded-lg border">
              <table className="w-full text-sm">
                <thead className="bg-card text-left text-xs text-muted-foreground">
                  <tr>
                    <th className="px-3 py-2 font-medium">#</th>
                    <th className="px-3 py-2 font-medium">To</th>
                    <th className="px-3 py-2 font-medium">Action</th>
                    <th className="px-3 py-2 font-medium">From</th>
                    <th className="px-3 py-2" />
                  </tr>
                </thead>
                <tbody>
                  {data.rules.map((r) => (
                    <tr key={r.num} className="border-t">
                      <td className="px-3 py-1.5 text-muted-foreground tabular">{r.num}</td>
                      <td className="px-3 py-1.5 font-mono text-xs">{r.to}</td>
                      <td className="px-3 py-1.5">
                        <Badge variant={r.action.startsWith("ALLOW") ? "success" : r.action.startsWith("LIMIT") ? "warning" : "destructive"}>
                          {r.action}
                        </Badge>
                      </td>
                      <td className="px-3 py-1.5 font-mono text-xs text-muted-foreground">{r.from}</td>
                      <td className="px-1 py-1 text-right">
                        <Button variant="ghost" size="icon" aria-label={`Delete rule ${r.num}`} disabled={disabled} onClick={() => del(r)}>
                          <Trash2 className="text-destructive" />
                        </Button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          <OpStatus busy={busy} error={error} job={job} />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Add rule</CardTitle>
          <CardDescription>Incoming traffic to a port.</CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={addRule} className="grid gap-3">
            <div className="flex flex-wrap gap-1.5">
              {PRESETS.map((p) => (
                <Button
                  key={p.label}
                  type="button"
                  variant="outline"
                  size="sm"
                  className="h-7 px-2 text-xs"
                  onClick={() => {
                    setPort(p.port);
                    setProto(p.proto);
                    setComment(p.label);
                  }}
                >
                  {p.label}
                </Button>
              ))}
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div className="grid gap-2">
                <Label htmlFor="fw-action">Action</Label>
                <Select id="fw-action" value={action} onChange={(e) => setAction(e.target.value)}>
                  <option value="allow">Allow</option>
                  <option value="limit">Limit (rate)</option>
                  <option value="deny">Deny</option>
                  <option value="reject">Reject</option>
                </Select>
              </div>
              <div className="grid gap-2">
                <Label htmlFor="fw-proto">Protocol</Label>
                <Select id="fw-proto" value={proto} onChange={(e) => setProto(e.target.value)}>
                  <option value="tcp">TCP</option>
                  <option value="udp">UDP</option>
                  <option value="any">Any</option>
                </Select>
              </div>
            </div>
            <div className="grid gap-2">
              <Label htmlFor="fw-port">Port or range</Label>
              <Input id="fw-port" placeholder="443 or 6000:6010" value={port} onChange={(e) => setPort(e.target.value)} required />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="fw-from">Source (optional)</Label>
              <Input id="fw-from" placeholder="any, 203.0.113.4 or 10.0.0.0/8" value={from} onChange={(e) => setFrom(e.target.value)} />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="fw-comment">Comment (optional)</Label>
              <Input id="fw-comment" value={comment} onChange={(e) => setComment(e.target.value)} maxLength={64} />
            </div>
            <Button type="submit" variant="brand" disabled={disabled || !port.trim()}>
              <Plus /> Add rule
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
