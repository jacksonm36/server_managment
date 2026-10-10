import { useEffect, useState } from "react";
import { Link } from "wouter";
import { Play } from "lucide-react";
import { api, type Job, type Server } from "@/lib/api";
import { CATALOG, clean, defaults, missing, opDef } from "@/lib/catalog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { OpForm } from "@/components/OpForm";

type Result = { server_id: string; job_id?: string; error?: string };

export function BulkDialog({ servers, onClose }: { servers: Server[]; onClose: () => void }) {
  const [op, setOp] = useState(CATALOG[0].op);
  const [params, setParams] = useState<Record<string, string>>(defaults(CATALOG[0]));
  const [results, setResults] = useState<Result[] | null>(null);
  const [jobs, setJobs] = useState<Record<string, Job>>({});
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const def = opDef(op)!;

  // Poll the started jobs until they all finish.
  useEffect(() => {
    if (!results) return;
    const ids = results.filter((r) => r.job_id).map((r) => r.job_id!);
    let stop = false;
    const tick = async () => {
      const done = await Promise.all(ids.map((id) => api.job(id).catch(() => null)));
      if (stop) return;
      const map: Record<string, Job> = {};
      done.forEach((j) => j && (map[j.id] = j));
      setJobs(map);
      if (done.every((j) => !j || j.status === "succeeded" || j.status === "failed")) return;
      setTimeout(tick, 1500);
    };
    void tick();
    return () => {
      stop = true;
    };
  }, [results]);

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
      <DialogContent className="max-h-[90svh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>Run on {servers.length} server(s)</DialogTitle>
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
            <ul className="grid gap-2">
              {results.map((r) => {
                const j = r.job_id ? jobs[r.job_id] : undefined;
                const status = r.error ? "failed" : (j?.status ?? "queued");
                return (
                  <li key={r.server_id} className="flex items-center gap-3 rounded-lg border px-3 py-2 text-sm">
                    <Link href={`/servers/${r.server_id}#tasks`} className="min-w-0 flex-1 truncate font-medium hover:text-brand" onClick={onClose}>
                      {name(r.server_id)}
                    </Link>
                    <span className="max-w-[50%] truncate text-xs text-destructive">{r.error || j?.error}</span>
                    <Badge variant={status === "succeeded" ? "success" : status === "failed" ? "destructive" : "warning"}>{status}</Badge>
                  </li>
                );
              })}
            </ul>
            <div className="flex justify-end">
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
