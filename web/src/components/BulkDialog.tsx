import { useState } from "react";
import { Play } from "lucide-react";
import { api, type Server } from "@/lib/api";
import { CATALOG, clean, defaults, missing, opDef } from "@/lib/catalog";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { OpForm } from "@/components/OpForm";
import { JobResults, type StartedJob } from "@/components/JobResults";

export function BulkDialog({ servers, onClose }: { servers: Server[]; onClose: () => void }) {
  const [op, setOp] = useState(CATALOG[0].op);
  const [params, setParams] = useState<Record<string, string>>(defaults(CATALOG[0]));
  const [results, setResults] = useState<StartedJob[] | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const def = opDef(op)!;

  async function submit() {
    const miss = missing(def, params);
    if (miss) {
      setError(`${miss} is required`);
      return;
    }
    if (def.danger && !confirm(`${def.label} on ${servers.length} server(s)?`)) return;
    setBusy(true);
    setError("");
    try {
      setResults(await api.bulk(servers.map((s) => s.id), op, clean(def, params)));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const name = (id: string) => servers.find((s) => s.id === id)?.name ?? id;

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className={`max-h-[90svh] overflow-y-auto ${results ? "sm:max-w-3xl" : ""}`}>
        <DialogHeader>
          <DialogTitle>
            {results ? def.label : "Run"} on {servers.length} server(s)
          </DialogTitle>
          <DialogDescription>{servers.map((s) => s.name).join(", ")}</DialogDescription>
        </DialogHeader>
        {!results ? (
          <div className="grid gap-4">
            <OpForm
              op={op}
              params={params}
              onChange={(o, p) => {
                setOp(o);
                setParams(p);
              }}
              idPrefix="bulk"
            />
            {error && <p className="text-sm text-destructive">{error}</p>}
            <div className="flex justify-end gap-2">
              <Button variant="ghost" onClick={onClose}>
                Cancel
              </Button>
              <Button variant={def.danger ? "destructive" : "brand"} disabled={busy} onClick={submit}>
                <Play /> Run
              </Button>
            </div>
          </div>
        ) : (
          <div className="grid gap-3">
            <JobResults results={results} name={name} onNavigate={onClose} />
            <div className="flex items-center justify-between gap-3">
              <p className="text-xs text-muted-foreground">You can close this. Running jobs stay in the lower right corner.</p>
              <Button variant="secondary" onClick={onClose}>
                Close
              </Button>
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
