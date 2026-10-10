import { useEffect, useState, type ReactNode } from "react";
import { Link, Route, Switch, useLocation } from "wouter";
import { History, ListChecks, LogOut, Server, Users as UsersIcon, Workflow } from "lucide-react";
import { api, type Role } from "@/lib/api";
import { startLive, stopLive, useLive } from "@/lib/live";
import { Button } from "@/components/ui/button";
import { Login } from "@/pages/Login";
import { Servers } from "@/pages/Servers";
import { ServerDetail } from "@/pages/ServerDetail";
import { Progress } from "@/pages/Progress";
import { Activity } from "@/pages/Activity";
import { Account } from "@/pages/Account";
import { Automation } from "@/pages/Automation";
import { Users } from "@/pages/Users";

export function App() {
  const [user, setUser] = useState<string | null | undefined>(undefined);
  const [role, setRole] = useState<Role>("viewer");

  useEffect(() => {
    api
      .me()
      .then((r) => {
        setUser(r.username);
        setRole(r.role);
      })
      .catch(() => setUser(null));
  }, []);

  useEffect(() => {
    if (!user) return;
    startLive();
    return stopLive;
  }, [user]);

  if (user === undefined) return null;
  if (user === null)
    return (
      <Login
        onLogin={(u) => {
          setUser(u);
          void api.me().then((r) => setRole(r.role));
        }}
      />
    );

  async function logout() {
    await api.logout().catch(() => {});
    setUser(null);
  }

  return (
    <div className="min-h-svh">
      <header className="sticky top-0 z-40 border-b bg-background/80 backdrop-blur">
        <div className="mx-auto flex h-14 max-w-7xl items-center justify-between gap-4 px-4 sm:px-6">
          <Link href="/" className="flex items-center gap-2 font-semibold">
            <span className="flex size-7 items-center justify-center rounded-lg bg-brand/15 text-brand">
              <Server className="size-4" />
            </span>
            servermgr
          </Link>
          <nav className="mr-auto flex min-w-0 items-center gap-1 overflow-x-auto text-sm">
            <NavLink href="/">Servers</NavLink>
            <NavLink href="/automation">
              <Workflow className="size-4" /> Automation
              <FiringDot />
            </NavLink>
            <NavLink href="/activity">
              <History className="size-4" /> Activity
            </NavLink>
            {role === "admin" && (
              <NavLink href="/users">
                <UsersIcon className="size-4" /> Users
              </NavLink>
            )}
            <NavLink href="/progress">
              <ListChecks className="size-4" /> Progress
            </NavLink>
          </nav>
          <div className="flex items-center gap-3 text-sm">
            <ConnectionDot />
            <Link href="/account" className="text-muted-foreground hover:text-foreground" title="Account & security">
              {user}
            </Link>
            <Button variant="ghost" size="sm" onClick={logout}>
              <LogOut /> Sign out
            </Button>
          </div>
        </div>
      </header>
      <main className="mx-auto max-w-7xl px-4 py-8 sm:px-6">
        {role === "viewer" && (
          <p className="mb-6 rounded-lg border border-warning/30 bg-warning/10 px-4 py-2 text-sm text-warning">
            You have read-only access. Actions that change servers are disabled for your account.
          </p>
        )}
        <Switch>
          <Route path="/">
            <Servers />
          </Route>
          <Route path="/users">
            <Users me={user} />
          </Route>
          <Route path="/automation">
            <Automation />
          </Route>
          <Route path="/account">
            <Account />
          </Route>
          <Route path="/activity">
            <Activity />
          </Route>
          <Route path="/progress">
            <Progress />
          </Route>
          <Route path="/servers/:id">{(p) => <ServerDetail id={p.id} />}</Route>
          <Route>
            <p className="text-muted-foreground">Page not found.</p>
          </Route>
        </Switch>
      </main>
    </div>
  );
}

function NavLink({ href, children }: { href: string; children: ReactNode }) {
  const [loc] = useLocation();
  const active = href === "/" ? loc === "/" || loc.startsWith("/servers") : loc.startsWith(href);
  return (
    <Link
      href={href}
      className={`flex items-center gap-1.5 rounded-md px-2.5 py-1.5 transition-colors ${
        active ? "bg-accent text-foreground" : "text-muted-foreground hover:text-foreground"
      }`}
    >
      {children}
    </Link>
  );
}

function FiringDot() {
  const [n, setN] = useState(0);
  useEffect(() => {
    const load = () => api.firingAlerts().then((a) => setN(a.length)).catch(() => {});
    void load();
    const t = setInterval(load, 30000);
    return () => clearInterval(t);
  }, []);
  if (!n) return null;
  return (
    <span className="rounded-full bg-destructive px-1.5 text-[10px] font-semibold leading-4 text-white" title={`${n} alert(s) firing`}>
      {n}
    </span>
  );
}

function ConnectionDot() {
  const connected = useLive((s) => s.connected);
  return (
    <span
      className="flex items-center gap-1.5 text-xs text-muted-foreground"
      title={connected ? "Live updates connected" : "Reconnecting…"}
    >
      <span className={`size-2 rounded-full ${connected ? "bg-success" : "bg-warning animate-pulse"}`} />
      <span className="hidden sm:inline">{connected ? "Live" : "Reconnecting"}</span>
    </span>
  );
}
