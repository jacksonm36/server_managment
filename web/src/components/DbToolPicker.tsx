import { useEffect, useState } from "react";
import { Download, ExternalLink, Lock, Wrench } from "lucide-react";
import { api, type CatalogApp } from "@/lib/api";
import { runOp } from "@/lib/ops";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";

const TOOLS = ["adminer", "phpmyadmin", "cloudbeaver"];
const BLURB: Record<string, string> = {
  adminer: "Recommended. One PHP file, many database types, modern theme.",
  phpmyadmin: "The classic MariaDB/MySQL web UI.",
  cloudbeaver: "DBeaver in the browser. Needs Docker and about 1 GB RAM.",
};

type Site = { domain: string; tls: string; access?: { auth_user?: string; allow_ips?: string[] }; app?: { id: string; version: string } };

/** "Database admin tool" card on the Databases tab: install status and links. */
export function DbToolPicker({ serverId, online, onInstall }: { serverId: string; online: boolean; onInstall: (app: string) => void }) {
  const [catalog, setCatalog] = useState<CatalogApp[]>([]);
  const [sites, setSites] = useState<Site[] | null>(null);

  useEffect(() => {
    void api.apps().then((a) => setCatalog(a.filter((x) => TOOLS.includes(x.id))));
  }, []);
  useEffect(() => {
    if (!online) return;
    void runOp(serverId, "web.status").then((j) => setSites(((j.result as { sites?: Site[] }) ?? {}).sites ?? []));
  }, [serverId, online]);

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Wrench className="size-4 text-brand" /> Database admin tool
        </CardTitle>
        <CardDescription>A web UI for your databases. Installed tools are password-protected by default.</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-2 sm:grid-cols-3">
        {TOOLS.map((id) => {
          const c = catalog.find((x) => x.id === id);
          if (!c) return null;
          const site = sites?.find((s) => s.app?.id === id);
          const protectedSite = !!(site?.access?.auth_user || site?.access?.allow_ips?.length);
          return (
            <div key={id} className={`flex flex-col gap-2 rounded-lg border p-3 ${id === "adminer" ? "border-brand/40" : ""}`}>
              <div className="flex items-center justify-between gap-2">
                <span className="font-medium">{c.name}</span>
                {site ? <Badge variant="success">installed</Badge> : id === "adminer" ? <Badge variant="outline">recommended</Badge> : null}
              </div>
              <p className="flex-1 text-xs text-muted-foreground">{BLURB[id]}</p>
              {site ? (
                <div className="flex flex-wrap items-center gap-2">
                  <Button variant="secondary" size="sm" onClick={() => window.open(`${site.tls ? "https" : "http"}://${site.domain}/`, "_blank", "noopener")}>
                    <ExternalLink /> Open
                  </Button>
                  <span className="truncate text-xs text-muted-foreground">{site.domain}</span>
                  {protectedSite && <Lock className="size-3.5 text-success" aria-label="protected" />}
                </div>
              ) : (
                <Button variant="outline" size="sm" className="w-fit" disabled={!online || sites === null} onClick={() => onInstall(id)}>
                  <Download /> Install
                </Button>
              )}
            </div>
          );
        })}
      </CardContent>
    </Card>
  );
}
