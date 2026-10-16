import { useEffect, useMemo, useState } from "react";
import { Check, Download, RefreshCw, ShieldCheck, ShieldAlert } from "lucide-react";
import { api, type PVEISO } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { LogoMark, appIcon, osIcon } from "@/components/DistroIcon";

// ISOLibrary lists official installer and tool ISOs, each resolved to the
// vendor's newest file, for one-click download into a storage. Proxmox
// fetches the file itself and checks it against the vendor's checksum.

const cats: [string, string][] = [
  ["", "All"],
  ["linux", "Linux"],
  ["hypervisor", "Proxmox"],
  ["tools", "Tools"],
];

function Logo({ x }: { x: PVEISO }) {
  const o = osIcon(x.icon);
  const a = o.icon || o.windows ? null : appIcon(undefined, x.icon);
  return (
    <div className="grid size-10 shrink-0 place-items-center rounded-lg border bg-muted/40">
      {o.icon || o.windows ? (
        <LogoMark icon={o.icon} windows={o.windows} className="size-5" />
      ) : a ? (
        <LogoMark icon={a.icon} Lucide={a.Lucide} className="size-5" />
      ) : (
        <span className="text-sm font-bold text-muted-foreground">{x.vendor.slice(0, 2).toUpperCase()}</span>
      )}
    </div>
  );
}

export function ISOLibrary({
  storage,
  have,
  onPick,
  onClose,
}: {
  storage: string;
  have: Set<string>; // ISO file names already on the storage
  onPick: (x: PVEISO) => void;
  onClose: () => void;
}) {
  const [list, setList] = useState<PVEISO[] | null>(null);
  const [err, setErr] = useState("");
  const [q, setQ] = useState("");
  const [cat, setCat] = useState("");
  const [loading, setLoading] = useState(false);

  const load = (refresh = false) => {
    setLoading(true);
    api
      .pveISOs(refresh)
      .then(setList)
      .catch((e) => setErr((e as Error).message))
      .finally(() => setLoading(false));
  };
  useEffect(() => load(), []);

  const shown = useMemo(() => {
    const t = q.trim().toLowerCase();
    return (list ?? []).filter((x) => (!cat || x.category === cat) && (!t || `${x.name} ${x.vendor} ${x.file ?? ""}`.toLowerCase().includes(t)));
  }, [list, q, cat]);

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[92vh] overflow-y-auto sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>ISO library</DialogTitle>
          <DialogDescription>
            Official installers, always the newest release from the vendor's own server. Proxmox downloads them straight into {storage} and checks each one
            against the vendor's checksum.
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-wrap items-center gap-2">
          <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search…" aria-label="Search ISOs" className="h-8 w-full sm:w-56" />
          <div className="flex flex-wrap gap-1" role="tablist">
            {cats.map(([k, label]) => (
              <Button key={k} size="sm" variant={cat === k ? "secondary" : "ghost"} role="tab" aria-selected={cat === k} onClick={() => setCat(k)}>
                {label}
              </Button>
            ))}
          </div>
          <Button size="icon" variant="ghost" className="ml-auto size-8" aria-label="Check for new releases" title="Check for new releases" onClick={() => load(true)} disabled={loading}>
            <RefreshCw className={`size-4 ${loading ? "animate-spin" : ""}`} />
          </Button>
        </div>
        {err && <p className="text-sm text-destructive">{err}</p>}
        {!list && !err && <p className="text-sm text-muted-foreground">Looking up the newest releases…</p>}
        <ul className="grid gap-2 sm:grid-cols-2">
          {shown.map((x) => {
            const there = !!x.file && have.has(x.file);
            return (
              <li key={x.id} className="flex min-w-0 items-start gap-3 rounded-lg border p-3">
                <Logo x={x} />
                <div className="grid min-w-0 flex-1 gap-1">
                  <div className="font-medium">{x.name}</div>
                  {x.error ? (
                    <p className="text-xs break-words text-destructive">{x.error}</p>
                  ) : (
                    <p className="truncate font-mono text-xs text-muted-foreground" title={x.url}>
                      {x.file}
                    </p>
                  )}
                  <div className="flex flex-wrap items-center gap-1.5">
                    {!x.error &&
                      (x.checksum ? (
                        <Badge variant="success" className="gap-1" title={`${x.checksum_algorithm} ${x.checksum}`}>
                          <ShieldCheck className="size-3" /> {x.checksum_algorithm} checked
                        </Badge>
                      ) : (
                        <Badge variant="outline" className="gap-1 text-muted-foreground" title="The vendor publishes no checksum file; the download still uses HTTPS.">
                          <ShieldAlert className="size-3" /> no checksum
                        </Badge>
                      ))}
                    {there && (
                      <Badge variant="outline" className="gap-1">
                        <Check className="size-3" /> on {storage}
                      </Badge>
                    )}
                  </div>
                </div>
                <Button size="sm" variant={there ? "ghost" : "secondary"} disabled={!!x.error || !x.url} onClick={() => onPick(x)}>
                  <Download /> {there ? "Again" : "Get"}
                </Button>
              </li>
            );
          })}
        </ul>
        {list && shown.length === 0 && <p className="text-sm text-muted-foreground">No match.</p>}
        <p className="text-xs text-muted-foreground">
          Windows isn't listed: Microsoft only hands out download links that expire. Download it from Microsoft and use Upload, or paste the link into
          “Download from URL”.
        </p>
      </DialogContent>
    </Dialog>
  );
}
