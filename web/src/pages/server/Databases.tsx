import { useState } from "react";
import { Check, Copy, Database, Download, Plus, RefreshCw, Trash2, UserPlus } from "lucide-react";
import { useAgentState } from "@/lib/ops";
import { formatBytes } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label } from "@/components/ui/input";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { OpStatus, Offline } from "@/components/OpStatus";
import { DbToolPicker } from "@/components/DbToolPicker";

type DB = { name: string; size: number; users: string[] };
type DBUser = { user: string; host: string; databases: string[] };
type State = { installed: boolean; active?: boolean; version?: string; databases?: DB[]; users?: DBUser[] };

/** 24-char password from a URL-safe alphabet, generated in the browser. */
function genPassword() {
  const abc = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789-_";
  const buf = new Uint32Array(24);
  crypto.getRandomValues(buf);
  return Array.from(buf, (n) => abc[n % abc.length]).join("");
}

export function Databases({ serverId, online, onInstallApp }: { serverId: string; online: boolean; onInstallApp?: (app: string) => void }) {
  const { data, busy, error, job, run } = useAgentState<State>(serverId, "db.status", online);
  const [dialog, setDialog] = useState<"db" | "user" | null>(null);
  const [created, setCreated] = useState<{ user: string; host: string; password: string; db?: string } | null>(null);
  const disabled = !online || !!busy;

  if (data && !data.installed) {
    return (
      <Card className="flex flex-col items-center gap-3 p-10 text-center">
        <Database className="size-8 text-muted-foreground" />
        <div className="font-medium">MariaDB is not installed</div>
        <p className="max-w-sm text-sm text-muted-foreground">Installs MariaDB server and applies the usual hardening (no anonymous users, no remote root).</p>
        <Button variant="brand" disabled={disabled} onClick={() => run("db.install")}>
          <Download /> Install MariaDB
        </Button>
        <div className="w-full max-w-xl text-left">
          <OpStatus busy={busy} error={error} job={job} />
        </div>
      </Card>
    );
  }

  return (
    <div className="grid gap-4">
      {created && <Credentials c={created} onClose={() => setCreated(null)} />}
      {data?.active && onInstallApp && <DbToolPicker serverId={serverId} online={online} onInstall={onInstallApp} />}
      <Card>
        <CardHeader className="flex-row flex-wrap items-start justify-between gap-3">
          <div>
            <CardTitle className="flex items-center gap-2">
              Databases
              {data && (data.active ? <Badge variant="success">running</Badge> : <Badge variant="destructive">stopped</Badge>)}
            </CardTitle>
            <CardDescription>{data?.version ? `MariaDB ${data.version}` : "Loading…"}</CardDescription>
          </div>
          <div className="flex gap-2">
            <Button variant="ghost" size="sm" disabled={disabled} onClick={() => run("db.status")} aria-label="Refresh">
              <RefreshCw />
            </Button>
            <Button variant="secondary" size="sm" disabled={disabled || !data?.active} onClick={() => setDialog("user")}>
              <UserPlus /> Add user
            </Button>
            <Button variant="brand" size="sm" disabled={disabled || !data?.active} onClick={() => setDialog("db")}>
              <Plus /> Add database
            </Button>
          </div>
        </CardHeader>
        <CardContent className="grid gap-4">
          {!online && <Offline />}
          {data?.databases?.length === 0 && <p className="text-sm text-muted-foreground">No databases yet.</p>}
          {!!data?.databases?.length && (
            <div className="overflow-x-auto rounded-lg border">
              <table className="w-full text-sm">
                <thead className="text-left text-xs text-muted-foreground">
                  <tr>
                    <th className="px-3 py-2 font-medium">Database</th>
                    <th className="px-3 py-2 font-medium">Size</th>
                    <th className="px-3 py-2 font-medium">Users</th>
                    <th className="px-3 py-2" />
                  </tr>
                </thead>
                <tbody>
                  {data.databases.map((d) => (
                    <tr key={d.name} className="border-t">
                      <td className="px-3 py-2 font-mono text-xs">{d.name}</td>
                      <td className="px-3 py-2 text-muted-foreground tabular">{formatBytes(d.size)}</td>
                      <td className="px-3 py-2 font-mono text-xs text-muted-foreground">{d.users.join(", ") || "—"}</td>
                      <td className="px-1 py-1 text-right">
                        <Button
                          variant="ghost"
                          size="icon"
                          aria-label={`Drop ${d.name}`}
                          disabled={disabled}
                          onClick={() => {
                            if (!confirm(`Drop database ${d.name}? All its data is deleted permanently.`)) return;
                            const users = d.users.length > 0 && confirm("Also drop users that only had access to this database?");
                            void run("db.delete", { name: d.name, drop_users: String(users) });
                          }}
                        >
                          <Trash2 className="text-destructive" />
                        </Button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}

          {!!data?.users?.length && (
            <div className="grid gap-2">
              <div className="text-sm font-medium">Users</div>
              <ul className="grid gap-2">
                {data.users.map((u) => (
                  <li key={`${u.user}@${u.host}`} className="flex items-center gap-3 rounded-lg border px-3 py-2">
                    <span className="min-w-0 flex-1 truncate font-mono text-xs">
                      {u.user}@{u.host}
                      <span className="text-muted-foreground"> → {u.databases.join(", ") || "no databases"}</span>
                    </span>
                    <Button
                      variant="ghost"
                      size="sm"
                      disabled={disabled}
                      onClick={async () => {
                        if (!confirm(`Set a new random password for ${u.user}@${u.host}?`)) return;
                        const pw = genPassword();
                        if (await run("db.user_save", { user: u.user, host: u.host, password: pw })) setCreated({ user: u.user, host: u.host, password: pw });
                      }}
                    >
                      Reset password
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      aria-label={`Drop user ${u.user}`}
                      disabled={disabled}
                      onClick={() => confirm(`Drop user ${u.user}@${u.host}?`) && run("db.user_delete", { user: u.user, host: u.host })}
                    >
                      <Trash2 className="text-destructive" />
                    </Button>
                  </li>
                ))}
              </ul>
            </div>
          )}
          <OpStatus busy={busy} error={error} job={job} />
        </CardContent>
      </Card>

      {dialog && (
        <CreateDialog
          kind={dialog}
          databases={data?.databases?.map((d) => d.name) ?? []}
          busy={!!busy}
          onClose={() => setDialog(null)}
          onSubmit={async (p) => {
            const ok = await run(dialog === "db" ? "db.create" : "db.user_save", p);
            if (ok) {
              setDialog(null);
              if (p.user) setCreated({ user: p.user, host: p.host || "localhost", password: p.password, db: p.name });
            }
          }}
        />
      )}
    </div>
  );
}

function Credentials({ c, onClose }: { c: { user: string; host: string; password: string; db?: string }; onClose: () => void }) {
  const [copied, setCopied] = useState(false);
  return (
    <Card className="border-success/40 bg-success/5">
      <CardContent className="grid gap-3 pt-5">
        <div className="text-sm">
          Credentials for <span className="font-mono">{c.user}@{c.host}</span>. The password is <strong>not stored by the panel</strong>; copy it now.
        </div>
        <div className="grid gap-1 rounded-lg border bg-background/60 p-3 font-mono text-xs">
          {c.db && <div>database: {c.db}</div>}
          <div>user: {c.user}</div>
          <div>password: {c.password}</div>
          <div>host: {c.host === "localhost" ? "localhost (socket or 127.0.0.1)" : c.host}</div>
        </div>
        <div className="flex gap-2">
          <Button
            variant="secondary"
            size="sm"
            onClick={async () => {
              await navigator.clipboard.writeText(c.password);
              setCopied(true);
            }}
          >
            {copied ? <Check /> : <Copy />} Copy password
          </Button>
          <Button variant="ghost" size="sm" onClick={onClose}>
            Done
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

function CreateDialog(props: {
  kind: "db" | "user";
  databases: string[];
  busy: boolean;
  onClose: () => void;
  onSubmit: (p: Record<string, string>) => void;
}) {
  const [name, setName] = useState("");
  const [user, setUser] = useState("");
  const [host, setHost] = useState("localhost");
  const [password, setPassword] = useState(genPassword);
  const [dbs, setDbs] = useState<string[]>([]);
  const isDB = props.kind === "db";

  return (
    <Dialog open onOpenChange={(o) => !o && props.onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{isDB ? "Add database" : "Add database user"}</DialogTitle>
          <DialogDescription>{isDB ? "utf8mb4 with an optional user that gets full access to it." : "Grants full access to the selected databases."}</DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            const p: Record<string, string> = {};
            if (isDB) p.name = name.trim();
            if (user.trim()) Object.assign(p, { user: user.trim(), host: host.trim(), password });
            if (!isDB) p.databases = dbs.join(" ");
            props.onSubmit(p);
          }}
        >
          {isDB && (
            <div className="grid gap-2">
              <Label htmlFor="dbname">Database name</Label>
              <Input
                id="dbname"
                className="font-mono"
                placeholder="shop"
                value={name}
                onChange={(e) => {
                  setName(e.target.value);
                  if (!user || user === name.slice(0, 32)) setUser(e.target.value.slice(0, 32));
                }}
                pattern="[A-Za-z0-9_]{1,64}"
                required
              />
            </div>
          )}
          <div className="grid grid-cols-[1fr_9rem] gap-3">
            <div className="grid gap-2">
              <Label htmlFor="dbuser">User {isDB && "(optional)"}</Label>
              <Input id="dbuser" className="font-mono" value={user} onChange={(e) => setUser(e.target.value)} pattern="[A-Za-z0-9_]{1,32}" required={!isDB} />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="dbhost">Host</Label>
              <Input id="dbhost" className="font-mono" value={host} onChange={(e) => setHost(e.target.value)} />
            </div>
          </div>
          {user.trim() && (
            <div className="grid gap-2">
              <Label htmlFor="dbpw">Password</Label>
              <div className="flex gap-2">
                <Input id="dbpw" className="font-mono" value={password} onChange={(e) => setPassword(e.target.value)} minLength={12} required />
                <Button type="button" variant="outline" onClick={() => setPassword(genPassword())}>
                  New
                </Button>
              </div>
            </div>
          )}
          {!isDB && props.databases.length > 0 && (
            <div className="grid gap-2">
              <Label>Access to</Label>
              <div className="flex flex-wrap gap-3">
                {props.databases.map((d) => (
                  <label key={d} className="flex cursor-pointer items-center gap-1.5 font-mono text-xs">
                    <input
                      type="checkbox"
                      className="accent-[var(--color-brand)]"
                      checked={dbs.includes(d)}
                      onChange={(e) => setDbs(e.target.checked ? [...dbs, d] : dbs.filter((x) => x !== d))}
                    />
                    {d}
                  </label>
                ))}
              </div>
            </div>
          )}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={props.onClose}>
              Cancel
            </Button>
            <Button type="submit" variant="brand" disabled={props.busy}>
              Create
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
