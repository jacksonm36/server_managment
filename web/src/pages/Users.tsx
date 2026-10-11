import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { KeyRound, LogOut, Pencil, Plus, ShieldCheck, Trash2, UserCheck, UserX } from "lucide-react";
import { api, type Grant, type Permission, type RoleInfo, type ServerGroup, type User, type UserGroup } from "@/lib/api";
import { useLive } from "@/lib/live";
import { reloadServerGroups } from "@/lib/groups";
import { timeAgo } from "@/lib/utils";
import { GROUP_COLORS, GroupChip } from "@/components/GroupChip";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input, Label } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";

function genPassword() {
  const abc = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789";
  const buf = new Uint32Array(20);
  crypto.getRandomValues(buf);
  return Array.from(buf, (n) => abc[n % abc.length]).join("");
}

const TABS = [
  { id: "users", label: "Users" },
  { id: "roles", label: "Roles" },
  { id: "groups", label: "User groups" },
  { id: "server-groups", label: "Server groups" },
] as const;
type TabId = (typeof TABS)[number]["id"];

type Data = {
  users: User[];
  roles: RoleInfo[];
  groups: UserGroup[];
  serverGroups: ServerGroup[];
  grants: Grant[];
  perms: Permission[];
};

const empty: Data = { users: [], roles: [], groups: [], serverGroups: [], grants: [], perms: [] };

export function Users({ me }: { me: string }) {
  const [tab, setTab] = useState<TabId>(() => (TABS.find((t) => t.id === window.location.hash.slice(1))?.id ?? "users") as TabId);
  const [data, setData] = useState<Data>(empty);
  const [error, setError] = useState("");
  const reload = useCallback(async () => {
    try {
      const [users, roles, groups, serverGroups, grants, perms] = await Promise.all([
        api.users(),
        api.roles(),
        api.userGroups(),
        api.serverGroups(),
        api.grants(),
        api.permissions(),
      ]);
      setData({ users, roles, groups, serverGroups, grants, perms: perms.permissions });
    } catch (e) {
      setError((e as Error).message);
    }
  }, []);
  useEffect(() => {
    void reload();
  }, [reload]);

  // act runs a change, shows its error and reloads everything.
  const act = useCallback(
    async (f: () => Promise<unknown>) => {
      setError("");
      try {
        await f();
        return true;
      } catch (e) {
        setError((e as Error).message);
        return false;
      } finally {
        await reload();
      }
    },
    [reload],
  );

  return (
    <div className="grid gap-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Users &amp; access</h1>
        <p className="text-sm text-muted-foreground">
          Who can sign in, and what they may do. A role is a set of permissions; give it to a user or a user group on all servers or on one server
          group.
        </p>
      </div>
      <div role="tablist" className="-mx-1 flex gap-1 overflow-x-auto border-b px-1">
        {TABS.map((t) => (
          <button
            key={t.id}
            role="tab"
            aria-selected={tab === t.id}
            onClick={() => {
              history.replaceState(null, "", `#${t.id}`);
              setTab(t.id);
            }}
            className={`-mb-px cursor-pointer border-b-2 px-3 py-2 text-sm whitespace-nowrap ${
              tab === t.id ? "border-brand text-foreground" : "border-transparent text-muted-foreground hover:text-foreground"
            }`}
          >
            {t.label}
          </button>
        ))}
      </div>
      {error && <p className="text-sm text-destructive">{error}</p>}
      {tab === "users" && <UsersTab me={me} data={data} act={act} />}
      {tab === "roles" && <RolesTab data={data} act={act} />}
      {tab === "groups" && <UserGroupsTab data={data} act={act} />}
      {tab === "server-groups" && <ServerGroupsTab data={data} act={act} />}
    </div>
  );
}

type Act = (f: () => Promise<unknown>) => Promise<boolean>;

function Header({ text, action }: { text: ReactNode; action?: ReactNode }) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-3">
      <p className="max-w-3xl text-sm text-muted-foreground">{text}</p>
      {action}
    </div>
  );
}

// ---- access summary and editor ----

function grantLabel(g: Grant) {
  return `${g.role} on ${g.server_group_id ? g.server_group : "all servers"}`;
}

function AccessSummary({ role, grants, viaGroups }: { role?: string; grants: Grant[]; viaGroups: Grant[] }) {
  return (
    <div className="flex flex-wrap gap-1">
      {role && <Badge variant={role === "admin" ? "warning" : "default"}>{role}</Badge>}
      {grants.map((g) => (
        <Badge key={g.id} variant="outline">
          {grantLabel(g)}
        </Badge>
      ))}
      {viaGroups.map((g) => (
        <Badge key={g.id} variant="outline" title="Through a user group">
          {grantLabel(g)} (group)
        </Badge>
      ))}
    </div>
  );
}

/** Lists and edits the role grants of one user or user group. */
function GrantsEditor({ target, data, act }: { target: { user_id: number } | { user_group_id: number }; data: Data; act: Act }) {
  const mine = data.grants.filter((g) => ("user_id" in target ? g.user_id === target.user_id : g.user_group_id === target.user_group_id));
  const [roleId, setRoleId] = useState("");
  const [scope, setScope] = useState("");
  const role = data.roles.find((r) => String(r.id) === roleId);
  const perms = role ? (role.builtin ? null : role.permissions) : null;
  const panelWide = scope !== "" && perms ? perms.filter((p) => !p.startsWith("server.")) : [];
  return (
    <div className="grid gap-2">
      <Label>Extra access</Label>
      {mine.length === 0 ? (
        <p className="text-xs text-muted-foreground">None.</p>
      ) : (
        <ul className="grid gap-1">
          {mine.map((g) => (
            <li key={g.id} className="flex items-center justify-between gap-2 rounded-md border px-2 py-1 text-sm">
              <span>
                <span className="font-medium">{g.role}</span> on{" "}
                {g.server_group_id ? (
                  <GroupChip group={{ name: g.server_group, color: data.serverGroups.find((s) => s.id === g.server_group_id)?.color ?? "" }} />
                ) : (
                  "all servers"
                )}
              </span>
              <Button variant="ghost" size="icon" title="Remove" aria-label="Remove" onClick={() => act(() => api.deleteGrant(g.id))}>
                <Trash2 className="text-destructive" />
              </Button>
            </li>
          ))}
        </ul>
      )}
      <div className="flex flex-wrap gap-2">
        <Select className="h-8 w-40" value={roleId} onChange={(e) => setRoleId(e.target.value)} aria-label="Role to give">
          <option value="">Give role…</option>
          {data.roles
            .filter((r) => r.name !== "none")
            .map((r) => (
              <option key={r.id} value={r.id}>
                {r.name}
              </option>
            ))}
        </Select>
        <Select className="h-8 w-48" value={scope} onChange={(e) => setScope(e.target.value)} aria-label="Where">
          <option value="">on all servers</option>
          {data.serverGroups.map((s) => (
            <option key={s.id} value={s.id}>
              on {s.name}
            </option>
          ))}
        </Select>
        <Button
          size="sm"
          variant="secondary"
          disabled={!roleId}
          onClick={async () => {
            const ok = await act(() =>
              api.createGrant({ ...target, role_id: Number(roleId), server_group_id: scope ? Number(scope) : null }),
            );
            if (ok) setRoleId("");
          }}
        >
          <Plus /> Add
        </Button>
      </div>
      {scope !== "" && roleId && (
        <p className="text-xs text-muted-foreground">
          On a server group only the role's server permissions apply (seeing and working on those servers).
          {panelWide.length > 0 && ` Panel-wide ones like ${panelWide.slice(0, 2).join(", ")} are ignored there.`}
        </p>
      )}
    </div>
  );
}

// ---- users ----

function UsersTab({ me, data, act }: { me: string; data: Data; act: Act }) {
  const [dialog, setDialog] = useState<{ kind: "edit"; user?: User } | { kind: "password"; user: User } | null>(null);
  const groupName = (id: number) => data.groups.find((g) => g.id === id)?.name ?? "?";
  return (
    <div className="grid gap-4">
      <Header
        text="Each user has a main role on all servers. Give more through user groups or extra access on a server group."
        action={
          <Button variant="brand" onClick={() => setDialog({ kind: "edit" })}>
            <Plus /> Add user
          </Button>
        }
      />
      <Card className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead className="text-left text-xs text-muted-foreground">
            <tr>
              <th className="px-4 py-2 font-medium">User</th>
              <th className="px-4 py-2 font-medium">Access</th>
              <th className="hidden px-4 py-2 font-medium md:table-cell">Groups</th>
              <th className="hidden px-4 py-2 font-medium sm:table-cell">Last sign-in</th>
              <th className="px-4 py-2" />
            </tr>
          </thead>
          <tbody>
            {data.users.map((u) => {
              const self = u.username === me;
              const own = data.grants.filter((g) => g.user_id === u.id);
              const viaGroups = data.grants.filter((g) => g.user_group_id && u.groups.includes(g.user_group_id));
              return (
                <tr key={u.id} className="border-t align-top">
                  <td className="px-4 py-2">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-medium">{u.display_name || u.username}</span>
                      {u.display_name && <span className="text-xs text-muted-foreground">{u.username}</span>}
                      {self && <Badge variant="outline">you</Badge>}
                      {u.totp_enabled && (
                        <Badge variant="success" title="Two-factor enabled">
                          <ShieldCheck className="size-3" /> 2FA
                        </Badge>
                      )}
                      {u.disabled && <Badge variant="destructive">disabled</Badge>}
                      {u.must_change_password && <Badge variant="warning">must change password</Badge>}
                    </div>
                    {u.email && <div className="text-xs text-muted-foreground">{u.email}</div>}
                  </td>
                  <td className="px-4 py-2">
                    <AccessSummary role={u.role} grants={own} viaGroups={viaGroups} />
                  </td>
                  <td className="hidden px-4 py-2 md:table-cell">
                    <div className="flex flex-wrap gap-1">
                      {u.groups.map((g) => (
                        <Badge key={g} variant="outline">
                          {groupName(g)}
                        </Badge>
                      ))}
                    </div>
                  </td>
                  <td className="hidden px-4 py-2 text-muted-foreground sm:table-cell">
                    {u.last_login ? timeAgo(u.last_login) : "never"}
                    {u.sessions > 0 && <div className="text-xs">{u.sessions} active session(s)</div>}
                  </td>
                  <td className="px-2 py-1 text-right whitespace-nowrap">
                    <Button variant="ghost" size="icon" title="Edit" aria-label="Edit" onClick={() => setDialog({ kind: "edit", user: u })}>
                      <Pencil />
                    </Button>
                    <Button variant="ghost" size="icon" title="Reset password" aria-label="Reset password" onClick={() => setDialog({ kind: "password", user: u })}>
                      <KeyRound />
                    </Button>
                    {!self && (
                      <>
                        {u.sessions > 0 && (
                          <Button
                            variant="ghost"
                            size="icon"
                            title="Sign out everywhere"
                            aria-label="Sign out everywhere"
                            onClick={() => confirm(`Sign ${u.username} out of all sessions?`) && act(() => api.endUserSessions(u.id))}
                          >
                            <LogOut />
                          </Button>
                        )}
                        <Button
                          variant="ghost"
                          size="icon"
                          title={u.disabled ? "Enable" : "Disable"}
                          aria-label={u.disabled ? "Enable" : "Disable"}
                          onClick={() => act(() => api.updateUser(u.id, { ...userBody(u), disabled: !u.disabled }))}
                        >
                          {u.disabled ? <UserCheck className="text-success" /> : <UserX />}
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon"
                          title="Delete"
                          aria-label="Delete"
                          onClick={() => confirm(`Delete user ${u.username}?`) && act(() => api.deleteUser(u.id))}
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
      {dialog?.kind === "edit" && (
        <UserDialog
          user={dialog.user}
          self={dialog.user?.username === me}
          data={data}
          act={act}
          onClose={() => setDialog(null)}
        />
      )}
      {dialog?.kind === "password" && <PasswordDialog user={dialog.user} onClose={() => setDialog(null)} act={act} />}
    </div>
  );
}

const userBody = (u: User) => ({
  display_name: u.display_name,
  email: u.email,
  role: u.role,
  disabled: u.disabled,
  groups: u.groups,
});

function UserDialog({ user, self, data, act, onClose }: { user?: User; self: boolean; data: Data; act: Act; onClose: () => void }) {
  // After creating, the dialog stays open on the new user so access can be added.
  const [id, setId] = useState(user?.id);
  const [username, setUsername] = useState(user?.username ?? "");
  const [displayName, setDisplayName] = useState(user?.display_name ?? "");
  const [email, setEmail] = useState(user?.email ?? "");
  const [role, setRole] = useState(user?.role ?? "operator");
  const [groups, setGroups] = useState<number[]>(user?.groups ?? []);
  const [password, setPassword] = useState(genPassword);
  const [mustChange, setMustChange] = useState(true);
  const [created, setCreated] = useState("");
  const [error, setError] = useState("");
  const roleInfo = data.roles.find((r) => r.name === role);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    const body = { display_name: displayName.trim(), email: email.trim(), role, disabled: user?.disabled ?? false, groups };
    try {
      if (id) {
        await api.updateUser(id, body);
        onClose();
      } else {
        const r = await api.createUser({ ...body, username: username.trim(), password, must_change_password: mustChange });
        setId(r.id);
        setCreated(password);
      }
    } catch (err) {
      setError((err as Error).message);
    } finally {
      await act(async () => {});
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90svh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{user ? `Edit ${user.username}` : created ? `Created ${username}` : "Add user"}</DialogTitle>
          <DialogDescription>
            {created ? "Share the password securely; it is shown once." : "Profile, main role and user groups."}
          </DialogDescription>
        </DialogHeader>
        {created && (
          <div className="grid gap-2">
            <code className="rounded-md border bg-background/60 px-3 py-2 font-mono text-sm">{created}</code>
            {mustChange && <p className="text-xs text-muted-foreground">They will be asked to choose their own password when they first sign in.</p>}
          </div>
        )}
        {!created && (
          <form className="grid gap-4" onSubmit={submit}>
            {!user && (
              <div className="grid gap-2">
                <Label htmlFor="u-name">Username</Label>
                <Input id="u-name" value={username} onChange={(e) => setUsername(e.target.value)} required autoComplete="off" />
              </div>
            )}
            <div className="grid gap-4 sm:grid-cols-2">
              <div className="grid gap-2">
                <Label htmlFor="u-display">Display name</Label>
                <Input id="u-display" value={displayName} onChange={(e) => setDisplayName(e.target.value)} autoComplete="off" />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="u-email">Email</Label>
                <Input id="u-email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} autoComplete="off" />
              </div>
            </div>
            <div className="grid gap-2">
              <Label htmlFor="u-role">Main role (on all servers)</Label>
              <Select id="u-role" value={role} disabled={self} onChange={(e) => setRole(e.target.value)}>
                {data.roles.map((r) => (
                  <option key={r.id} value={r.name}>
                    {r.name}
                  </option>
                ))}
              </Select>
              {roleInfo?.description && <p className="text-xs text-muted-foreground">{roleInfo.description}</p>}
              {self && <p className="text-xs text-muted-foreground">You can't change your own main role.</p>}
            </div>
            {data.groups.length > 0 && (
              <div className="grid gap-2">
                <Label>User groups</Label>
                <div className="flex flex-wrap gap-x-4 gap-y-1">
                  {data.groups.map((g) => (
                    <label key={g.id} className="flex cursor-pointer items-center gap-2 text-sm">
                      <input
                        type="checkbox"
                        className="accent-[var(--color-brand)]"
                        checked={groups.includes(g.id)}
                        onChange={(e) => setGroups(e.target.checked ? [...groups, g.id] : groups.filter((x) => x !== g.id))}
                      />
                      {g.name}
                    </label>
                  ))}
                </div>
              </div>
            )}
            {!user && (
              <div className="grid gap-2">
                <Label htmlFor="u-pw">Initial password</Label>
                <div className="flex gap-2">
                  <Input id="u-pw" className="font-mono" value={password} onChange={(e) => setPassword(e.target.value)} minLength={12} required autoComplete="off" />
                  <Button type="button" variant="outline" onClick={() => setPassword(genPassword())}>
                    New
                  </Button>
                </div>
                <label className="flex cursor-pointer items-center gap-2 text-sm">
                  <input type="checkbox" className="accent-[var(--color-brand)]" checked={mustChange} onChange={(e) => setMustChange(e.target.checked)} />
                  Ask them to choose their own at first sign-in
                </label>
              </div>
            )}
            {error && <p className="text-sm text-destructive">{error}</p>}
            <div className="flex justify-end gap-2">
              <Button type="button" variant="ghost" onClick={onClose}>
                Cancel
              </Button>
              <Button type="submit" variant="brand">
                {user ? "Save" : "Create"}
              </Button>
            </div>
          </form>
        )}
        {id && <GrantsEditor target={{ user_id: id }} data={data} act={act} />}
        {created && (
          <div className="flex justify-end">
            <Button variant="brand" onClick={onClose}>
              Done
            </Button>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

function PasswordDialog({ user, onClose, act }: { user: User; onClose: () => void; act: Act }) {
  const [password, setPassword] = useState(genPassword);
  const [reset2fa, setReset2fa] = useState(false);
  const [mustChange, setMustChange] = useState(true);
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
                await api.resetUserPassword(user.id, password, reset2fa, mustChange);
                setDone(true);
                void act(async () => {});
              } catch (err) {
                setError((err as Error).message);
              }
            }}
          >
            <Input className="font-mono" value={password} onChange={(e) => setPassword(e.target.value)} minLength={12} required aria-label="New password" />
            <label className="flex cursor-pointer items-center gap-2 text-sm">
              <input type="checkbox" className="accent-[var(--color-brand)]" checked={mustChange} onChange={(e) => setMustChange(e.target.checked)} />
              Ask them to choose their own at next sign-in
            </label>
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

// ---- roles ----

function RolesTab({ data, act }: { data: Data; act: Act }) {
  const [editing, setEditing] = useState<RoleInfo | "new" | null>(null);
  const [builtin, setBuiltin] = useState<Record<string, string[]>>({});
  useEffect(() => {
    void api.permissions().then((p) => setBuiltin(p.builtin));
  }, []);
  const total = data.perms.length;
  return (
    <div className="grid gap-4">
      <Header
        text="Built-in roles can't be changed. Make your own from any set of permissions."
        action={
          <Button variant="brand" onClick={() => setEditing("new")}>
            <Plus /> New role
          </Button>
        }
      />
      <div className="grid gap-3 md:grid-cols-2">
        {data.roles.map((r) => {
          const n = (r.builtin ? builtin[r.name] : r.permissions)?.length ?? 0;
          return (
            <Card key={r.id} className="grid gap-2 p-4">
              <div className="flex items-start justify-between gap-2">
                <div>
                  <div className="flex items-center gap-2 font-medium">
                    {r.name}
                    {r.builtin && <Badge variant="outline">built-in</Badge>}
                  </div>
                  <p className="text-sm text-muted-foreground">{r.description || "No description."}</p>
                </div>
                <div className="flex shrink-0">
                  <Button variant="ghost" size="icon" title={r.builtin ? "View" : "Edit"} aria-label={r.builtin ? "View" : "Edit"} onClick={() => setEditing(r)}>
                    <Pencil />
                  </Button>
                  {!r.builtin && (
                    <Button
                      variant="ghost"
                      size="icon"
                      title="Delete"
                      aria-label="Delete"
                      onClick={() => confirm(`Delete role ${r.name}?`) && act(() => api.deleteRole(r.id))}
                    >
                      <Trash2 className="text-destructive" />
                    </Button>
                  )}
                </div>
              </div>
              <p className="text-xs text-muted-foreground">
                {n} of {total} permissions · main role of {r.users} user(s) · used in {r.grants} grant(s)
              </p>
            </Card>
          );
        })}
      </div>
      {editing && (
        <RoleDialog
          role={editing === "new" ? undefined : editing}
          builtin={builtin}
          data={data}
          onClose={() => setEditing(null)}
          onSave={async (body) => {
            const ok = await act(() => api.saveRole(body, editing === "new" ? undefined : editing.id));
            if (ok) setEditing(null);
          }}
        />
      )}
    </div>
  );
}

function RoleDialog({
  role,
  builtin,
  data,
  onClose,
  onSave,
}: {
  role?: RoleInfo;
  builtin: Record<string, string[]>;
  data: Data;
  onClose: () => void;
  onSave: (r: { name: string; description: string; permissions: string[] }) => Promise<void>;
}) {
  const readOnly = !!role?.builtin;
  const [name, setName] = useState(role?.name ?? "");
  const [description, setDescription] = useState(role?.description ?? "");
  const [perms, setPerms] = useState<string[]>(role ? (role.builtin ? (builtin[role.name] ?? []) : role.permissions) : ["server.view"]);
  const groups = useMemo(() => {
    const m = new Map<string, Permission[]>();
    for (const p of data.perms) m.set(p.group, [...(m.get(p.group) ?? []), p]);
    return [...m.entries()];
  }, [data.perms]);
  const toggle = (k: string, on: boolean) => setPerms(on ? [...perms, k] : perms.filter((p) => p !== k));
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90svh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{readOnly ? `Role ${role?.name}` : role ? `Edit ${role.name}` : "New role"}</DialogTitle>
          <DialogDescription>
            Permissions marked “per server” can also be given on just one server group.
          </DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            void onSave({ name: name.trim(), description: description.trim(), permissions: perms });
          }}
        >
          {!readOnly && (
            <div className="grid gap-4 sm:grid-cols-2">
              <div className="grid gap-2">
                <Label htmlFor="r-name">Name</Label>
                <Input id="r-name" value={name} onChange={(e) => setName(e.target.value)} required />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="r-copy">Start from</Label>
                <Select id="r-copy" value="" onChange={(e) => e.target.value && setPerms(builtin[e.target.value] ?? [])}>
                  <option value="">Copy a built-in role…</option>
                  {Object.keys(builtin).map((b) => (
                    <option key={b} value={b}>
                      {b}
                    </option>
                  ))}
                </Select>
              </div>
              <div className="grid gap-2 sm:col-span-2">
                <Label htmlFor="r-desc">Description</Label>
                <Input id="r-desc" value={description} onChange={(e) => setDescription(e.target.value)} />
              </div>
            </div>
          )}
          {groups.map(([group, list]) => (
            <fieldset key={group} className="grid gap-1">
              <legend className="mb-1 flex items-center gap-3 text-sm font-medium">
                {group}
                {!readOnly && (
                  <button
                    type="button"
                    className="cursor-pointer text-xs font-normal text-muted-foreground hover:text-foreground"
                    onClick={() => {
                      const keys = list.map((p) => p.key);
                      const all = keys.every((k) => perms.includes(k));
                      setPerms(all ? perms.filter((p) => !keys.includes(p)) : [...new Set([...perms, ...keys])]);
                    }}
                  >
                    all / none
                  </button>
                )}
              </legend>
              {list.map((p) => (
                <label key={p.key} className={`flex items-start gap-2 text-sm ${readOnly ? "" : "cursor-pointer"}`}>
                  <input
                    type="checkbox"
                    className="mt-0.5 accent-[var(--color-brand)]"
                    checked={perms.includes(p.key)}
                    disabled={readOnly}
                    onChange={(e) => toggle(p.key, e.target.checked)}
                  />
                  <span>
                    {p.label}
                    {p.server && <span className="ml-1.5 text-xs text-muted-foreground">per server</span>}
                    {(p.key === "users.manage" || p.key === "server.terminal") && <span className="ml-1.5 text-xs text-warning">powerful</span>}
                  </span>
                </label>
              ))}
            </fieldset>
          ))}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              {readOnly ? "Close" : "Cancel"}
            </Button>
            {!readOnly && (
              <Button type="submit" variant="brand">
                Save
              </Button>
            )}
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// ---- user groups ----

function UserGroupsTab({ data, act }: { data: Data; act: Act }) {
  const [editing, setEditing] = useState<UserGroup | "new" | null>(null);
  const userName = (id: number) => {
    const u = data.users.find((x) => x.id === id);
    return u ? u.display_name || u.username : "?";
  };
  return (
    <div className="grid gap-4">
      <Header
        text="Give access to a team at once: everyone in a group gets the group's roles."
        action={
          <Button variant="brand" onClick={() => setEditing("new")}>
            <Plus /> New group
          </Button>
        }
      />
      {data.groups.length === 0 && <p className="text-sm text-muted-foreground">No user groups yet.</p>}
      <div className="grid gap-3 md:grid-cols-2">
        {data.groups.map((g) => (
          <Card key={g.id} className="grid gap-2 p-4">
            <div className="flex items-start justify-between gap-2">
              <div>
                <div className="font-medium">{g.name}</div>
                {g.description && <p className="text-sm text-muted-foreground">{g.description}</p>}
              </div>
              <div className="flex shrink-0">
                <Button variant="ghost" size="icon" title="Edit" aria-label="Edit" onClick={() => setEditing(g)}>
                  <Pencil />
                </Button>
                <Button
                  variant="ghost"
                  size="icon"
                  title="Delete"
                  aria-label="Delete"
                  onClick={() => confirm(`Delete group ${g.name}? Its members lose the group's access.`) && act(() => api.deleteUserGroup(g.id))}
                >
                  <Trash2 className="text-destructive" />
                </Button>
              </div>
            </div>
            <AccessSummary grants={data.grants.filter((x) => x.user_group_id === g.id)} viaGroups={[]} />
            <p className="text-xs text-muted-foreground">
              {g.members.length ? g.members.map(userName).join(", ") : "No members."}
            </p>
          </Card>
        ))}
      </div>
      {editing && <UserGroupDialog group={editing === "new" ? undefined : editing} data={data} act={act} onClose={() => setEditing(null)} />}
    </div>
  );
}

function UserGroupDialog({ group, data, act, onClose }: { group?: UserGroup; data: Data; act: Act; onClose: () => void }) {
  const [id, setId] = useState(group?.id);
  const [name, setName] = useState(group?.name ?? "");
  const [description, setDescription] = useState(group?.description ?? "");
  const [members, setMembers] = useState<number[]>(group?.members ?? []);
  const [error, setError] = useState("");
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90svh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{id ? `Edit ${name}` : "New user group"}</DialogTitle>
          <DialogDescription>Members get every role the group has.</DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={async (e) => {
            e.preventDefault();
            setError("");
            try {
              const g = await api.saveUserGroup({ name: name.trim(), description: description.trim(), members }, id);
              if (id) onClose();
              else setId(g.id);
            } catch (err) {
              setError((err as Error).message);
            } finally {
              await act(async () => {});
            }
          }}
        >
          <div className="grid gap-2">
            <Label htmlFor="g-name">Name</Label>
            <Input id="g-name" value={name} onChange={(e) => setName(e.target.value)} required />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="g-desc">Description</Label>
            <Input id="g-desc" value={description} onChange={(e) => setDescription(e.target.value)} />
          </div>
          <div className="grid gap-2">
            <Label>Members</Label>
            <div className="grid max-h-48 gap-1 overflow-y-auto rounded-lg border p-2">
              {data.users.map((u) => (
                <label key={u.id} className="flex cursor-pointer items-center gap-2 rounded px-1.5 py-1 text-sm hover:bg-accent/40">
                  <input
                    type="checkbox"
                    className="accent-[var(--color-brand)]"
                    checked={members.includes(u.id)}
                    onChange={(e) => setMembers(e.target.checked ? [...members, u.id] : members.filter((x) => x !== u.id))}
                  />
                  {u.display_name || u.username}
                  {u.display_name && <span className="text-xs text-muted-foreground">{u.username}</span>}
                </label>
              ))}
            </div>
          </div>
          {error && <p className="text-sm text-destructive">{error}</p>}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              {id && !group ? "Done" : "Cancel"}
            </Button>
            <Button type="submit" variant="brand">
              {id ? "Save" : "Create"}
            </Button>
          </div>
        </form>
        {id ? (
          <GrantsEditor target={{ user_group_id: id }} data={data} act={act} />
        ) : (
          <p className="text-xs text-muted-foreground">Create the group, then give it roles here.</p>
        )}
      </DialogContent>
    </Dialog>
  );
}

// ---- server groups ----

function ServerGroupsTab({ data, act }: { data: Data; act: Act }) {
  const [editing, setEditing] = useState<ServerGroup | "new" | null>(null);
  const servers = useLive((s) => s.servers);
  const actAndRefresh: Act = async (f) => {
    const ok = await act(f);
    void reloadServerGroups();
    return ok;
  };
  return (
    <div className="grid gap-4">
      <Header
        text="Group servers (for example by customer or environment) to filter them, run actions on them and give people access to just those servers."
        action={
          <Button variant="brand" onClick={() => setEditing("new")}>
            <Plus /> New server group
          </Button>
        }
      />
      {data.serverGroups.length === 0 && <p className="text-sm text-muted-foreground">No server groups yet.</p>}
      <div className="grid gap-3 md:grid-cols-2">
        {data.serverGroups.map((g) => (
          <Card key={g.id} className="grid gap-2 p-4">
            <div className="flex items-start justify-between gap-2">
              <div>
                <GroupChip group={g} />
                {g.description && <p className="mt-1 text-sm text-muted-foreground">{g.description}</p>}
              </div>
              <div className="flex shrink-0">
                <Button variant="ghost" size="icon" title="Edit" aria-label="Edit" onClick={() => setEditing(g)}>
                  <Pencil />
                </Button>
                <Button
                  variant="ghost"
                  size="icon"
                  title="Delete"
                  aria-label="Delete"
                  onClick={() =>
                    confirm(`Delete server group ${g.name}? Access given on it is removed and schedules targeting it stop running.`) &&
                    actAndRefresh(() => api.deleteServerGroup(g.id))
                  }
                >
                  <Trash2 className="text-destructive" />
                </Button>
              </div>
            </div>
            <p className="text-xs text-muted-foreground">
              {g.server_ids.length ? g.server_ids.map((id) => servers.get(id)?.name ?? "?").join(", ") : "No servers."}
            </p>
          </Card>
        ))}
      </div>
      {editing && (
        <ServerGroupDialog
          group={editing === "new" ? undefined : editing}
          onClose={() => setEditing(null)}
          onSave={async (g) => {
            const ok = await actAndRefresh(() => api.saveServerGroup(g, editing === "new" ? undefined : editing.id));
            if (ok) setEditing(null);
          }}
        />
      )}
    </div>
  );
}

function ServerGroupDialog({
  group,
  onClose,
  onSave,
}: {
  group?: ServerGroup;
  onClose: () => void;
  onSave: (g: Omit<ServerGroup, "id">) => Promise<void>;
}) {
  const map = useLive((s) => s.servers);
  const servers = useMemo(() => [...map.values()].sort((a, b) => a.name.localeCompare(b.name)), [map]);
  const [name, setName] = useState(group?.name ?? "");
  const [description, setDescription] = useState(group?.description ?? "");
  const [color, setColor] = useState(group?.color || GROUP_COLORS[0]);
  const [ids, setIds] = useState<string[]>(group?.server_ids ?? []);
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90svh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{group ? `Edit ${group.name}` : "New server group"}</DialogTitle>
          <DialogDescription>A server can be in several groups.</DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            void onSave({ name: name.trim(), description: description.trim(), color, server_ids: ids });
          }}
        >
          <div className="grid gap-2">
            <Label htmlFor="sg-name">Name</Label>
            <Input id="sg-name" value={name} onChange={(e) => setName(e.target.value)} required />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="sg-desc">Description</Label>
            <Input id="sg-desc" value={description} onChange={(e) => setDescription(e.target.value)} />
          </div>
          <div className="grid gap-2">
            <Label>Color</Label>
            <div className="flex flex-wrap gap-2">
              {GROUP_COLORS.map((c) => (
                <button
                  key={c}
                  type="button"
                  aria-label={`Color ${c}`}
                  aria-pressed={color === c}
                  className={`size-7 cursor-pointer rounded-full border-2 ${color === c ? "border-foreground" : "border-transparent"}`}
                  style={{ background: c }}
                  onClick={() => setColor(c)}
                />
              ))}
            </div>
          </div>
          <div className="grid gap-2">
            <Label>Servers</Label>
            <div className="grid max-h-56 gap-1 overflow-y-auto rounded-lg border p-2">
              {servers.map((s) => (
                <label key={s.id} className="flex cursor-pointer items-center gap-2 rounded px-1.5 py-1 text-sm hover:bg-accent/40">
                  <input
                    type="checkbox"
                    className="accent-[var(--color-brand)]"
                    checked={ids.includes(s.id)}
                    onChange={(e) => setIds(e.target.checked ? [...ids, s.id] : ids.filter((x) => x !== s.id))}
                  />
                  <span className={`size-2 rounded-full ${s.online ? "bg-success" : "bg-muted-foreground/50"}`} />
                  {s.name}
                  <span className="truncate text-xs text-muted-foreground">{s.os_pretty}</span>
                </label>
              ))}
            </div>
          </div>
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
