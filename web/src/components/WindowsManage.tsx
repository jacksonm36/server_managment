import { useCallback, useEffect, useState } from "react";
import { Check as CheckIcon, Download, KeyRound, Loader2, Lock, Plus, RefreshCw, ShieldCheck, Trash2, UserCog } from "lucide-react";
import { type PVEGuestRef } from "@/lib/api";
import { pve, type WinAppInfo, type WinUser } from "@/lib/pveapi";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input, Label } from "@/components/ui/input";

// The Windows tab of a running Windows VM: its local accounts and
// installing applications, both through the QEMU guest agent.

const errText = (e: unknown) => (e as Error).message;

export function WindowsTab({ cluster, guest, running }: { cluster: string; guest: PVEGuestRef; running: boolean }) {
  if (!running)
    return <p className="text-sm text-muted-foreground">Start the VM: accounts and applications are managed through its guest agent, inside the running Windows.</p>;
  return (
    <div className="grid gap-4 xl:grid-cols-2">
      <AccountsCard cluster={cluster} guest={guest} />
      <AppsCard cluster={cluster} guest={guest} />
    </div>
  );
}

function AccountsCard({ cluster, guest }: { cluster: string; guest: PVEGuestRef }) {
  const [users, setUsers] = useState<WinUser[] | null>(null);
  const [err, setErr] = useState("");
  const [msg, setMsg] = useState("");
  const [busy, setBusy] = useState("");
  const [add, setAdd] = useState(false);
  const [pw, setPw] = useState<WinUser | null>(null);
  const load = useCallback(() => {
    setErr("");
    pve
      .winUsers(cluster, guest)
      .then(setUsers)
      .catch((e) => setErr(errText(e)));
  }, [cluster, guest]);
  useEffect(load, [load]);
  const act = async (u: WinUser, label: string, body: Parameters<typeof pve.winUserUpdate>[3]) => {
    setBusy(u.name + label);
    setMsg("");
    try {
      await pve.winUserUpdate(cluster, guest, u.name, body);
      setMsg(`${u.name}: ${label}.`);
    } catch (e) {
      setErr(errText(e));
    } finally {
      setBusy("");
      load();
    }
  };
  const remove = async (u: WinUser) => {
    if (!confirm(`Delete the account ${u.name}? Its profile folder stays on the disk.`)) return;
    setBusy(u.name + "delete");
    try {
      await pve.winUserDelete(cluster, guest, u.name);
      setMsg(`${u.name} deleted.`);
    } catch (e) {
      setErr(errText(e));
    } finally {
      setBusy("");
      load();
    }
  };
  return (
    <Card>
      <CardHeader className="flex-row flex-wrap items-start justify-between gap-2">
        <div>
          <CardTitle className="flex items-center gap-2">
            <UserCog className="size-4" /> Accounts
          </CardTitle>
          <CardDescription>Local Windows accounts. Passwords go in through the guest agent, never typed into the console.</CardDescription>
        </div>
        <div className="flex gap-2">
          <Button size="sm" variant="secondary" onClick={() => setAdd(true)}>
            <Plus /> Add account
          </Button>
          <Button size="icon" variant="ghost" aria-label="Refresh" onClick={load}>
            <RefreshCw />
          </Button>
        </div>
      </CardHeader>
      <CardContent className="grid gap-2">
        {msg && <p className="text-sm text-success">{msg}</p>}
        {err && <p className="text-sm text-destructive">{err}</p>}
        {!users && !err && <p className="text-sm text-muted-foreground">Asking Windows…</p>}
        {users && (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="text-left text-xs text-muted-foreground">
                <tr>
                  <th className="py-2 pr-3 font-normal">Account</th>
                  <th className="py-2 pr-3 font-normal">Status</th>
                  <th className="hidden py-2 pr-3 font-normal sm:table-cell">Last sign-in</th>
                  <th />
                </tr>
              </thead>
              <tbody className="divide-y">
                {users.map((u) => (
                  <tr key={u.name} className="align-top">
                    <td className="py-2 pr-3">
                      <div className="font-medium">{u.name}</div>
                      {u.full_name && <div className="text-xs text-muted-foreground">{u.full_name}</div>}
                    </td>
                    <td className="py-2 pr-3">
                      <div className="flex flex-wrap gap-1">
                        <Badge variant={u.enabled ? "success" : "outline"}>{u.enabled ? "enabled" : "disabled"}</Badge>
                        {u.admin && <Badge variant="warning">admin</Badge>}
                        {u.locked && <Badge variant="destructive">locked</Badge>}
                        {u.builtin && <Badge variant="outline">built-in</Badge>}
                      </div>
                    </td>
                    <td className="hidden py-2 pr-3 text-xs text-muted-foreground sm:table-cell">{u.last_logon ? new Date(u.last_logon).toLocaleString() : "never"}</td>
                    <td className="py-1.5 text-right">
                      <div className="flex flex-wrap justify-end gap-1">
                        <Button size="sm" variant="ghost" onClick={() => setPw(u)} title="Set password">
                          <KeyRound />
                        </Button>
                        {u.locked && (
                          <Button size="sm" variant="ghost" disabled={!!busy} onClick={() => act(u, "unlocked", { unlock: true })}>
                            <Lock /> Unlock
                          </Button>
                        )}
                        <Button size="sm" variant="ghost" disabled={!!busy} onClick={() => act(u, u.enabled ? "disabled" : "enabled", { enabled: !u.enabled })}>
                          {busy === u.name + (u.enabled ? "disabled" : "enabled") ? <Loader2 className="animate-spin" /> : null}
                          {u.enabled ? "Disable" : "Enable"}
                        </Button>
                        <Button
                          size="sm"
                          variant="ghost"
                          disabled={!!busy}
                          onClick={() => act(u, u.admin ? "no longer an administrator" : "made an administrator", { admin: !u.admin })}
                          title={u.admin ? "Remove from Administrators" : "Add to Administrators"}
                        >
                          <ShieldCheck /> {u.admin ? "Revoke admin" : "Make admin"}
                        </Button>
                        {!u.builtin && (
                          <Button size="sm" variant="ghost" className="text-destructive" disabled={!!busy} onClick={() => remove(u)} aria-label={`Delete ${u.name}`}>
                            <Trash2 />
                          </Button>
                        )}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </CardContent>
      {add && <AccountDialog cluster={cluster} guest={guest} onClose={() => setAdd(false)} onDone={(t) => (setMsg(t), load())} />}
      {pw && <AccountDialog cluster={cluster} guest={guest} user={pw} onClose={() => setPw(null)} onDone={(t) => (setMsg(t), load())} />}
    </Card>
  );
}

// AccountDialog creates an account, or sets an existing one's password.
function AccountDialog({
  cluster,
  guest,
  user,
  onClose,
  onDone,
}: {
  cluster: string;
  guest: PVEGuestRef;
  user?: WinUser;
  onClose: () => void;
  onDone: (t: string) => void;
}) {
  const [name, setName] = useState("");
  const [full, setFull] = useState("");
  const [admin, setAdmin] = useState(false);
  const [p1, setP1] = useState("");
  const [p2, setP2] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{user ? `Password of ${user.name}` : "Add a Windows account"}</DialogTitle>
          <DialogDescription>At least 12 characters with three of: lower case, upper case, digits, symbols. Not stored or logged.</DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-3"
          onSubmit={async (e) => {
            e.preventDefault();
            if (p1 !== p2) return setErr("The passwords do not match.");
            setBusy(true);
            setErr("");
            try {
              if (user) {
                await pve.winUserUpdate(cluster, guest, user.name, { password: p1, unlock: user.locked });
                onDone(`Password of ${user.name} set${user.locked ? " and the account unlocked" : ""}.`);
              } else {
                await pve.winUserCreate(cluster, guest, { name: name.trim(), full_name: full.trim(), admin, password: p1 });
                onDone(`${name.trim()} created${admin ? " as an administrator" : ""}.`);
              }
              onClose();
            } catch (x) {
              setErr(errText(x));
            } finally {
              setBusy(false);
            }
          }}
        >
          {!user && (
            <>
              <div className="grid gap-1.5">
                <Label htmlFor="wa-name">Account name</Label>
                <Input id="wa-name" value={name} onChange={(e) => setName(e.target.value)} required maxLength={20} pattern="[A-Za-z0-9][A-Za-z0-9 ._\-]*" autoFocus />
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="wa-full">Full name (optional)</Label>
                <Input id="wa-full" value={full} onChange={(e) => setFull(e.target.value)} maxLength={64} />
              </div>
              <label className="flex cursor-pointer items-center gap-2 text-sm">
                <input type="checkbox" checked={admin} onChange={(e) => setAdmin(e.target.checked)} className="size-4 accent-[var(--color-brand)]" />
                Administrator
              </label>
            </>
          )}
          <div className="grid gap-1.5">
            <Label htmlFor="wa-p1">Password</Label>
            <Input id="wa-p1" type="password" autoComplete="new-password" minLength={12} value={p1} onChange={(e) => setP1(e.target.value)} required />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="wa-p2">Repeat it</Label>
            <Input id="wa-p2" type="password" autoComplete="new-password" value={p2} onChange={(e) => setP2(e.target.value)} required />
          </div>
          {err && <p className="text-sm text-destructive">{err}</p>}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" variant="brand" disabled={busy}>
              {busy && <Loader2 className="animate-spin" />} {user ? "Set password" : "Create"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// useWinApps loads the application catalog.
export function useWinApps() {
  const [apps, setApps] = useState<WinAppInfo[]>([]);
  useEffect(() => {
    pve
      .winApps()
      .then(setApps)
      .catch(() => {});
  }, []);
  return apps;
}

// AppPicker is a checklist of the catalog.
export function AppPicker({ apps, picked, onChange }: { apps: WinAppInfo[]; picked: string[]; onChange: (ids: string[]) => void }) {
  return (
    <div className="grid gap-1.5 sm:grid-cols-2">
      {apps.map((a) => {
        const on = picked.includes(a.id);
        return (
          <button
            key={a.id}
            type="button"
            onClick={() => onChange(on ? picked.filter((x) => x !== a.id) : [...picked, a.id])}
            className={`flex items-start gap-2 rounded-md border px-2.5 py-2 text-left text-sm ${on ? "border-brand bg-brand/5" : "hover:bg-accent/40"}`}
            title={a.note}
          >
            <span className={`mt-0.5 flex size-4 shrink-0 items-center justify-center rounded border ${on ? "border-brand bg-brand text-white" : ""}`}>
              {on && <CheckIcon className="size-3" />}
            </span>
            <span className="grid">
              <span className="font-medium">{a.name}</span>
              <span className="text-xs text-muted-foreground">{a.vendor}</span>
              {a.note && on && <span className="text-xs text-warning">{a.note}</span>}
            </span>
          </button>
        );
      })}
    </div>
  );
}

function AppsCard({ cluster, guest }: { cluster: string; guest: PVEGuestRef }) {
  const apps = useWinApps();
  const [picked, setPicked] = useState<string[]>([]);
  const [pid, setPid] = useState(0);
  const [log, setLog] = useState<string[]>([]);
  const [state, setState] = useState<"" | "running" | "ok" | "failed">("");
  const [err, setErr] = useState("");
  useEffect(() => {
    if (!pid) return;
    let stop = false;
    const poll = async () => {
      try {
        const s = await pve.winAppsStatus(cluster, guest, pid);
        setLog(s.log);
        if (s.done) return setState(s.exit_code === 0 ? "ok" : "failed");
      } catch (e) {
        setErr(errText(e));
      }
      if (!stop) setTimeout(poll, 4000);
    };
    void poll();
    return () => {
      stop = true;
    };
  }, [cluster, guest, pid]);
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Download className="size-4" /> Applications
        </CardTitle>
        <CardDescription>
          Installed silently by the guest agent, from each vendor&apos;s own download. Windows checks the publisher&apos;s signature (or the release&apos;s SHA-256)
          first and refuses anything else.
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3">
        <AppPicker apps={apps} picked={picked} onChange={setPicked} />
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            variant="brand"
            disabled={!picked.length || state === "running"}
            onClick={async () => {
              setErr("");
              setLog([]);
              try {
                const r = await pve.winAppsInstall(cluster, guest, picked);
                setState("running");
                setPid(r.pid);
              } catch (e) {
                setErr(errText(e));
              }
            }}
          >
            {state === "running" ? <Loader2 className="animate-spin" /> : <Download />} Install {picked.length || ""}
          </Button>
          {state === "ok" && <Badge variant="success">done</Badge>}
          {state === "failed" && <Badge variant="destructive">some failed</Badge>}
        </div>
        {err && <p className="text-sm text-destructive">{err}</p>}
        {log.length > 0 && <pre className="max-h-60 overflow-auto rounded-md bg-muted p-2 font-mono text-xs whitespace-pre-wrap">{log.join("\n")}</pre>}
      </CardContent>
    </Card>
  );
}
