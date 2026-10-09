import { useEffect, useState, type ReactNode } from "react";
import { Link, Route, Switch, useLocation } from "wouter";
import { ListChecks, LogOut, Server } from "lucide-react";
import { api } from "@/lib/api";
import { startLive, stopLive, useLive } from "@/lib/live";
import { Button } from "@/components/ui/button";
import { Login } from "@/pages/Login";
import { Servers } from "@/pages/Servers";
import { ServerDetail } from "@/pages/ServerDetail";
import { Progress } from "@/pages/Progress";

export function App() {
  const [user, setUser] = useState<string | null | undefined>(undefined);

  useEffect(() => {
    api
      .me()
      .then((r) => setUser(r.username))
      .catch(() => setUser(null));
  }, []);

  useEffect(() => {
    if (!user) return;
    startLive();
    return stopLive;
  }, [user]);

  if (user === undefined) return null;
  if (user === null) return <Login onLogin={setUser} />;

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
          <nav className="mr-auto flex items-center gap-1 text-sm">
            <NavLink href="/">Servers</NavLink>
            <NavLink href="/progress">
              <ListChecks className="size-4" /> Progress
            </NavLink>
          </nav>
          <div className="flex items-center gap-3 text-sm">
            <ConnectionDot />
            <span className="hidden text-muted-foreground sm:inline">{user}</span>
            <Button variant="ghost" size="sm" onClick={logout}>
              <LogOut /> Sign out
            </Button>
          </div>
        </div>
      </header>
      <main className="mx-auto max-w-7xl px-4 py-8 sm:px-6">
        <Switch>
          <Route path="/">
            <Servers />
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
