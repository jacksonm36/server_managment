import { useEffect, useState } from "react";
import { Ban, Download, RefreshCw, Save, ShieldOff, Undo2 } from "lucide-react";
import { useAgentState } from "@/lib/ops";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { OpStatus, Offline } from "@/components/OpStatus";

type Jail = {
  name: string;
  currently_failed: number;
  total_failed: number;
  currently_banned: number;
  total_banned: number;
  banned_ips: string[];
};
type Config = { bantime: string; findtime: string; maxretry: string; ignoreip: string };
type F2B = { installed: boolean; active?: boolean; jails?: Jail[]; config?: Config };

export function Fail2ban({ serverId, online }: { serverId: string; online: boolean }) {
  const { data, busy, error, job, run } = useAgentState<F2B>(serverId, "fail2ban.status", online);
  const [cfg, setCfg] = useState<Config>({ bantime: "1h", findtime: "10m", maxretry: "5", ignoreip: "" });
  const [banJail, setBanJail] = useState("");
  const [banIP, setBanIP] = useState("");
  const disabled = !online || !!busy;

  useEffect(() => {
    if (data?.config) {
      const c = data.config;
      setCfg({ ...c, ignoreip: c.ignoreip.split(/\s+/).filter((ip) => ip !== "127.0.0.1/8" && ip !== "::1").join(" ") });
    }
    if (data?.jails?.length && !banJail) setBanJail(data.jails[0].name);
  }, [data, banJail]);

  if (data && !data.installed) {
    return (
      <Card className="flex flex-col items-center gap-3 p-10 text-center">
        <ShieldOff className="size-8 text-muted-foreground" />
        <div className="font-medium">Fail2ban is not installed</div>
        <p className="max-w-sm text-sm text-muted-foreground">
          Installs fail2ban with an sshd jail (systemd journal backend) and sensible defaults.
        </p>
        <Button variant="brand" disabled={disabled} onClick={() => run("fail2ban.install")}>
          <Download /> Install Fail2ban
        </Button>
        <div className="w-full max-w-xl text-left">
          <OpStatus busy={busy} error={error} job={job} />
        </div>
      </Card>
    );
  }

  const set = (k: keyof Config) => (e: React.ChangeEvent<HTMLInputElement>) => setCfg({ ...cfg, [k]: e.target.value });

  return (
    <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_22rem]">
      <div className="grid content-start gap-4">
        <Card>
          <CardHeader className="flex-row flex-wrap items-start justify-between gap-3">
            <div>
              <CardTitle className="flex items-center gap-2">
                Fail2ban
                {data && (data.active ? <Badge variant="success">running</Badge> : <Badge variant="destructive">stopped</Badge>)}
              </CardTitle>
              <CardDescription>{data?.jails ? `${data.jails.length} jail(s)` : "Loading…"}</CardDescription>
            </div>
            <Button variant="ghost" size="sm" disabled={disabled} onClick={() => run("fail2ban.status")} aria-label="Refresh">
              <RefreshCw />
            </Button>
          </CardHeader>
          <CardContent className="grid gap-3">
            {!online && <Offline />}
            {data && !data.active && (
              <Button variant="secondary" className="w-fit" disabled={disabled} onClick={() => run("fail2ban.install")}>
                Repair &amp; start
              </Button>
            )}
            {data?.jails?.map((j) => (
              <div key={j.name} className="rounded-lg border p-4">
                <div className="mb-3 flex items-center justify-between">
                  <div className="font-mono text-sm font-medium">{j.name}</div>
                  <Badge variant={j.currently_banned ? "warning" : "outline"}>{j.currently_banned} banned</Badge>
                </div>
                <div className="grid grid-cols-2 gap-2 text-xs text-muted-foreground sm:grid-cols-4">
                  <Num label="Failed now" v={j.currently_failed} />
                  <Num label="Failed total" v={j.total_failed} />
                  <Num label="Banned now" v={j.currently_banned} />
                  <Num label="Banned total" v={j.total_banned} />
                </div>
                {j.banned_ips.length > 0 && (
                  <ul className="mt-3 flex flex-wrap gap-2">
                    {j.banned_ips.map((ip) => (
                      <li key={ip} className="flex items-center gap-1 rounded-md border py-0.5 pr-0.5 pl-2 font-mono text-xs">
                        {ip}
                        <Button
                          variant="ghost"
                          size="icon"
                          className="size-6"
                          aria-label={`Unban ${ip}`}
                          title="Unban"
                          disabled={disabled}
                          onClick={() => run("fail2ban.unban", { jail: j.name, ip })}
                        >
                          <Undo2 className="size-3.5" />
                        </Button>
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            ))}
            <OpStatus busy={busy} error={error} job={job} />
          </CardContent>
        </Card>
      </div>

      <div className="grid content-start gap-4">
        <Card>
          <CardHeader>
            <CardTitle>Ban an IP</CardTitle>
          </CardHeader>
          <CardContent>
            <form
              className="grid gap-3"
              onSubmit={async (e) => {
                e.preventDefault();
                if (await run("fail2ban.ban", { jail: banJail, ip: banIP.trim() })) setBanIP("");
              }}
            >
              <Select value={banJail} onChange={(e) => setBanJail(e.target.value)} aria-label="Jail">
                {data?.jails?.map((j) => (
                  <option key={j.name}>{j.name}</option>
                ))}
              </Select>
              <Input placeholder="203.0.113.7" value={banIP} onChange={(e) => setBanIP(e.target.value)} aria-label="IP address" required />
              <Button type="submit" variant="destructive" disabled={disabled || !banJail || !banIP.trim()}>
                <Ban /> Ban
              </Button>
            </form>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Settings</CardTitle>
            <CardDescription>Applies to all jails.</CardDescription>
          </CardHeader>
          <CardContent>
            <form
              className="grid gap-3"
              onSubmit={(e) => {
                e.preventDefault();
                void run("fail2ban.configure", { ...cfg });
              }}
            >
              <div className="grid grid-cols-3 gap-2">
                <Field id="bt" label="Ban time" value={cfg.bantime} onChange={set("bantime")} />
                <Field id="ft" label="Find time" value={cfg.findtime} onChange={set("findtime")} />
                <Field id="mr" label="Max retry" value={cfg.maxretry} onChange={set("maxretry")} />
              </div>
              <Field id="ig" label="Never ban (IPs / CIDRs)" value={cfg.ignoreip} onChange={set("ignoreip")} placeholder="10.0.1.0/24" />
              <Button type="submit" variant="secondary" disabled={disabled}>
                <Save /> Save &amp; reload
              </Button>
            </form>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}

function Num({ label, v }: { label: string; v: number }) {
  return (
    <div>
      <div>{label}</div>
      <div className="tabular text-base font-semibold text-foreground">{v}</div>
    </div>
  );
}

function Field(props: { id: string; label: string; value: string; placeholder?: string; onChange: (e: React.ChangeEvent<HTMLInputElement>) => void }) {
  return (
    <div className="grid gap-1.5">
      <Label htmlFor={props.id} className="text-xs">
        {props.label}
      </Label>
      <Input id={props.id} value={props.value} placeholder={props.placeholder} onChange={props.onChange} />
    </div>
  );
}
