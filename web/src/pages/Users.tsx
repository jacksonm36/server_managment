import { useCallback, useEffect, useState } from "react";
import { KeyRound, Plus, ShieldCheck, Trash2, UserX, UserCheck } from "lucide-react";
import { api, type User } from "@/lib/api";
import { timeAgo } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input, Label } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";

const ROLES = {
  admin: "Everything, including users, notification channels and adding/removing servers.",
  operator: "Run any action on servers (incl. terminal), manage playbooks, schedules and alerts.",
  viewer: "Read-only: dashboards, status, services and logs. Cannot change anything or read files.",
};

function genPassword() {
  const abc = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789";
  const buf = new Uint32Array(20);
  crypto.getRandomValues(buf);
  return Array.from(buf, (n) => abc[n % abc.length]).join("");
}

export function Users({ me }: { me: string }) {
  const [list, setList] = useState<User[]>([]);
  const [error, setError] = useState("");
  const [dialog, setDialog] = useState<{ kind: "new" } | { kind: "password"; user: User } | null>(null);
  const reload = useCallback(() => api.users().then(setList).catch((e) => setError(e.message)), []);
  useEffect(() => {
    void reload();
  }, [reload]);

  async function update(u: User, patch: Partial<User>) {
    setError("");
    try {
      await api.updateUser(u.id, { role: patch.role ?? u.role, disabled: patch.disabled ?? u.disabled });
      await reload();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  return (
    <div className="grid gap-6">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Users</h1>
          <p className="text-sm text-muted-foreground">Who can sign in to this panel, and what they may do.</p>
        </div>
        <Button variant="brand" onClick={() => setDialog({ kind: "new" })}>
          <Plus /> Add user
        </Button>
      </div>
      {error && <p className="text-sm text-destructive">{error}</p>}
      <Card className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead className="text-left text-xs text-muted-foreground">
            <tr>
              <th className="px-4 py-2 font-medium">User</th>
              <th className="px-4 py-2 font-medium">Role</th>
              <th className="hidden px-4 py-2 font-medium sm:table-cell">Last sign-in</th>
              <th className="px-4 py-2" />
            </tr>
          </thead>
          <tbody>
            {list.map((u) => {
              const self = u.username === me;
              return (
                <tr key={u.id} className="border-t">
                  <td className="px-4 py-2">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-medium">{u.username}</span>
                      {self && <Badge variant="outline">you</Badge>}
                      {u.totp_enabled && (
                        <Badge variant="success" title="Two-factor enabled">
                          <ShieldCheck className="size-3" /> 2FA
                        </Badge>
                      )}
                      {u.disabled && <Badge variant="destructive">disabled</Badge>}
                    </div>
                  </td>
                  <td className="px-4 py-2">
                    <Select className="h-8 w-32" value={u.role} disabled={self} onChange={(e) => update(u, { role: e.target.value as User["role"] })} aria-label="Role">
                      {Object.keys(ROLES).map((r) => (
                        <option key={r} value={r}>
                          {r}
                        </option>
                      ))}
                    </Select>
                  </td>
                  <td className="hidden px-4 py-2 text-muted-foreground sm:table-cell">{u.last_login ? timeAgo(u.last_login) : "never"}</td>
                  <td className="px-2 py-1 text-right whitespace-nowrap">
                    <Button variant="ghost" size="icon" title="Reset password" aria-label="Reset password" onClick={() => setDialog({ kind: "password", user: u })}>
                      <KeyRound />
                    </Button>
                    {!self && (
                      <>
                        <Button
                          variant="ghost"
                          size="icon"
                          title={u.disabled ? "Enable" : "Disable"}
                          aria-label={u.disabled ? "Enable" : "Disable"}
                          onClick={() => update(u, { disabled: !u.disabled })}
                        >
                          {u.disabled ? <UserCheck className="text-success" /> : <UserX />}
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon"
                          title="Delete"
                          aria-label="Delete"
                          onClick={async () => {
                            if (!confirm(`Delete user ${u.username}?`)) return;
                            await api.deleteUser(u.id).catch((e) => setError(e.message));
                            void reload();
                          }}
                        >
                          <Trash2 className="text-destructive" />
                        </Button>
                      </>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </Card>
      <div className="grid gap-2 text-sm text-muted-foreground">
        {Object.entries(ROLES).map(([r, d]) => (
          <p key={r}>
            <span className="font-medium text-foreground">{r}</span> — {d}
          </p>
        ))}
      </div>
      {dialog?.kind === "new" && (
        <UserDialog
          onClose={() => setDialog(null)}
          onDone={() => {
            setDialog(null);
            void reload();
          }}
        />
      )}
      {dialog?.kind === "password" && <PasswordDialog user={dialog.user} onClose={() => setDialog(null)} />}
    </div>
  );
}

function UserDialog({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [username, setUsername] = useState("");
  const [role, setRole] = useState<User["role"]>("operator");
  const [password, setPassword] = useState(genPassword);
  const [error, setError] = useState("");
  const [created, setCreated] = useState(false);
  return (
    <Dialog open onOpenChange={(o) => !o && (created ? onDone() : onClose())}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Add user</DialogTitle>
          <DialogDescription>Share the password securely; they can change it and set up 2FA under Account.</DialogDescription>
        </DialogHeader>
        {created ? (
          <div className="grid gap-3">
            <p className="text-sm">
              Created <span className="font-medium">{username}</span> ({role}). Initial password (shown once):
            </p>
            <code className="rounded-md border bg-background/60 px-3 py-2 font-mono text-sm">{password}</code>
            <div className="flex justify-end">
              <Button variant="brand" onClick={onDone}>
                Done
              </Button>
            </div>
          </div>
        ) : (
          <form
            className="grid gap-4"
            onSubmit={async (e) => {
              e.preventDefault();
              setError("");
              try {
                await api.createUser({ username: username.trim(), password, role });
                setCreated(true);
              } catch (err) {
                setError((err as Error).message);
              }
            }}
          >
            <div className="grid gap-2">
              <Label htmlFor="u-name">Username</Label>
              <Input id="u-name" value={username} onChange={(e) => setUsername(e.target.value)} required autoComplete="off" />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="u-role">Role</Label>
              <Select id="u-role" value={role} onChange={(e) => setRole(e.target.value as User["role"])}>
                {Object.keys(ROLES).map((r) => (
                  <option key={r} value={r}>
                    {r}
                  </option>
                ))}
              </Select>
              <p className="text-xs text-muted-foreground">{ROLES[role]}</p>
            </div>
            <div className="grid gap-2">
              <Label htmlFor="u-pw">Initial password</Label>
              <div className="flex gap-2">
                <Input id="u-pw" className="font-mono" value={password} onChange={(e) => setPassword(e.target.value)} minLength={12} required autoComplete="off" />
                <Button type="button" variant="outline" onClick={() => setPassword(genPassword())}>
                  New
                </Button>
              </div>
            </div>
            {error && <p className="text-sm text-destructive">{error}</p>}
            <div className="flex justify-end gap-2">
              <Button type="button" variant="ghost" onClick={onClose}>
                Cancel
              </Button>
              <Button type="submit" variant="brand">
                Create
              </Button>
            </div>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}

function PasswordDialog({ user, onClose }: { user: User; onClose: () => void }) {
  const [password, setPassword] = useState(genPassword);
  const [reset2fa, setReset2fa] = useState(false);
  const [done, setDone] = useState(false);
  const [error, setError] = useState("");
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Reset password for {user.username}</DialogTitle>
          <DialogDescription>All of their sessions are signed out.</DialogDescription>
        </DialogHeader>
        {done ? (
          <div className="grid gap-3">
            <p className="text-sm">New password (shown once):</p>
            <code className="rounded-md border bg-background/60 px-3 py-2 font-mono text-sm">{password}</code>
            <div className="flex justify-end">
              <Button variant="brand" onClick={onClose}>
                Done
              </Button>
            </div>
          </div>
        ) : (
          <form
            className="grid gap-4"
            onSubmit={async (e) => {
              e.preventDefault();
              try {
                await api.resetUserPassword(user.id, password, reset2fa);
                setDone(true);
              } catch (err) {
                setError((err as Error).message);
              }
            }}
          >
            <Input className="font-mono" value={password} onChange={(e) => setPassword(e.target.value)} minLength={12} required aria-label="New password" />
            {user.totp_enabled && (
              <label className="flex cursor-pointer items-center gap-2 text-sm">
                <input type="checkbox" className="accent-[var(--color-brand)]" checked={reset2fa} onChange={(e) => setReset2fa(e.target.checked)} />
                Also turn off their two-factor (lost authenticator)
              </label>
            )}
            {error && <p className="text-sm text-destructive">{error}</p>}
            <div className="flex justify-end gap-2">
              <Button type="button" variant="ghost" onClick={onClose}>
                Cancel
              </Button>
              <Button type="submit" variant="destructive">
                Reset password
              </Button>
            </div>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}
