import { useEffect, useMemo, useState } from "react";
import { Route, Switch } from "wouter";
import { LogOut } from "lucide-react";
import { api, type Me } from "@/lib/api";
import { startLive, stopLive } from "@/lib/live";
import { Shell } from "@/components/Shell";
import { Button } from "@/components/ui/button";
import { Login } from "@/pages/Login";
import { Servers } from "@/pages/Servers";
import { ServerDetail } from "@/pages/ServerDetail";
import { Activity } from "@/pages/Activity";
import { Account, PasswordCard } from "@/pages/Account";
import { Automation } from "@/pages/Automation";
import { Users } from "@/pages/Users";
import { Dashboard } from "@/pages/Dashboard";
import { CrowdSec } from "@/pages/CrowdSec";
import { Proxmox } from "@/pages/Proxmox";
import { ProxmoxGuest } from "@/pages/ProxmoxGuest";
import { ProxmoxStorage } from "@/pages/ProxmoxStorage";
import { Keys } from "@/pages/Keys";
import { VaultBanner } from "@/components/Vault";
import { JobsDock } from "@/components/JobsDock";
import { setVaultStatus } from "@/lib/vault";
import { AccessContext, accessFromMe, noAccess, readOnly } from "@/lib/access";
import { reloadServerGroups } from "@/lib/groups";

export function App() {
  const [me, setMe] = useState<Me | null | undefined>(undefined);
  const access = useMemo(() => (me ? accessFromMe(me) : noAccess), [me]);
  const loadMe = () =>
    api
      .me()
      .then((m) => {
        setMe(m);
        if (m.vault) setVaultStatus(m.vault);
      })
      .catch(() => setMe(null));
  const user = me?.username;
  const ready = !!me && !me.must_change_password;

  useEffect(() => {
    void loadMe();
  }, []);

  useEffect(() => {
    if (!ready) return;
    void reloadServerGroups();
    startLive();
    return stopLive;
  }, [ready]);

  async function logout() {
    await api.logout().catch(() => {});
    setMe(null);
  }

  if (me === undefined) return null;
  if (me === null) return <Login onLogin={() => void loadMe()} />;
  if (me.must_change_password)
    return (
      <div className="flex min-h-svh items-center justify-center p-4">
        <div className="grid w-full max-w-md gap-3">
          <PasswordCard forced onChanged={() => void loadMe()} />
          <Button variant="ghost" size="sm" className="w-fit" onClick={logout}>
            <LogOut /> Sign out
          </Button>
        </div>
      </div>
    );

  return (
    <AccessContext.Provider value={access}>
      <Shell me={me} access={access} onLogout={logout}>
        <VaultBanner />
        {readOnly(access) && (
          <p className="mb-6 rounded-lg border border-warning/30 bg-warning/10 px-4 py-2 text-sm text-warning">
            You have read-only access. Actions that change servers are disabled for your account.
          </p>
        )}
        <Switch>
          <Route path="/">
            <Dashboard />
          </Route>
          <Route path="/servers">
            <Servers />
          </Route>
          <Route path="/users">
            <Users me={user!} />
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
          <Route path="/storage">
            <ProxmoxStorage />
          </Route>
          <Route path="/proxmox/:cluster/:node/:type/:vmid">{(p) => <ProxmoxGuest {...p} />}</Route>
          <Route path="/security">
            <CrowdSec />
          </Route>
          <Route path="/keys">
            <Keys />
          </Route>
          <Route path="/activity">
            <Activity />
          </Route>
          <Route path="/servers/:id">{(p) => <ServerDetail id={p.id} />}</Route>
          <Route>
            <p className="text-muted-foreground">Page not found.</p>
          </Route>
        </Switch>
      </Shell>
      <JobsDock me={user!} />
    </AccessContext.Provider>
  );
}
