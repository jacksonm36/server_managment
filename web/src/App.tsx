import { useEffect, useState, type ReactNode } from "react";
import { Link, Route, Switch, useLocation } from "wouter";
import { Boxes, History, LayoutDashboard, ListChecks, LogOut, Server, ShieldCheck, Users as UsersIcon, Workflow } from "lucide-react";
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
import { Dashboard } from "@/pages/Dashboard";
import { CrowdSec } from "@/pages/CrowdSec";
import { Proxmox } from "@/pages/Proxmox";
import { RoleContext } from "@/lib/role";

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
        <div className="mx-auto flex max-w-7xl flex-wrap items-center justify-between gap-x-4 px-4 sm:px-6 md:h-14 md:flex-nowrap">
          <Link href="/" className="flex h-14 items-center gap-2 font-semibold">
            <span className="flex size-7 items-center justify-center rounded-lg bg-brand/15 text-brand">
              <Server className="size-4" />
            </span>
            servermgr
          </Link>
          <nav className="order-last -mx-1 flex w-full min-w-0 items-center gap-1 overflow-x-auto px-1 pb-2 text-sm md:order-none md:mr-auto md:w-auto md:pb-0">
            <NavLink href="/" label="Dashboard">
              <LayoutDashboard className="size-4" />
            </NavLink>
            <NavLink href="/servers" label="Servers">
              <Server className="size-4" />
            </NavLink>
            <NavLink href="/automation" label="Automation">
              <Workflow className="size-4" />
              <FiringDot />
            </NavLink>
            <NavLink href="/proxmox" label="Proxmox">
              <Boxes className="size-4" />
            </NavLink>
            {role !== "viewer" && (
              <NavLink href="/security" label="Security">
                <ShieldCheck className="size-4" />
              </NavLink>
            )}
            <NavLink href="/activity" label="Activity">
              <History className="size-4" />
            </NavLink>
            {role === "admin" && (
              <NavLink href="/users" label="Users">
                <UsersIcon className="size-4" />
              </NavLink>
            )}
            <NavLink href="/progress" label="Progress">
              <ListChecks className="size-4" />
            </NavLink>
          </nav>
          <div className="flex items-center gap-3 text-sm">
            <ConnectionDot />
            <Link href="/account" className="text-muted-foreground hover:text-foreground" title="Account & security">
              {user}
            </Link>
            <Button variant="ghost" size="sm" onClick={logout} aria-label="Sign out" title="Sign out">
              <LogOut /> <span className="hidden sm:inline">Sign out</span>
            </Button>
          </div>
        </div>
      </header>
      <RoleContext.Provider value={role}>
      <main className="mx-auto max-w-7xl px-4 py-8 sm:px-6">
        {role === "viewer" && (
          <p className="mb-6 rounded-lg border border-warning/30 bg-warning/10 px-4 py-2 text-sm text-warning">
            You have read-only access. Actions that change servers are disabled for your account.
          </p>
        )}
        <Switch>
          <Route path="/">
            <Dashboard role={role} />
          </Route>
          <Route path="/servers">
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
          <Route path="/proxmox">
            <Proxmox />
          </Route>
          <Route path="/security">
            <CrowdSec />
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
      </RoleContext.Provider>
    </div>
  );
}

function NavLink({ href, label, children }: { href: string; label: string; children: ReactNode }) {
  const [loc] = useLocation();
  const active = href === "/" ? loc === "/" : loc.startsWith(href);
  return (
    <Link
      href={href}
      title={label}
      aria-label={label}
      className={`flex items-center gap-1.5 rounded-md px-2.5 py-1.5 transition-colors ${
        active ? "bg-accent text-foreground" : "text-muted-foreground hover:text-foreground"
      }`}
    >
      {children}
      <span className="hidden md:inline">{label}</span>
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
