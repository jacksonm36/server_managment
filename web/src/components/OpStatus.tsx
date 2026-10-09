import { useState } from "react";
import { ChevronDown, ChevronRight, LoaderCircle } from "lucide-react";
import type { Job } from "@/lib/api";

/** Shows the running op, its error, and a collapsible log of the last job. */
export function OpStatus({ busy, error, job }: { busy: string | null; error: string; job: Job | null }) {
  const [open, setOpen] = useState(false);
  const showLog = !!job?.log;
  return (
    <div className="grid gap-2 empty:hidden">
      {busy && (
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <LoaderCircle className="size-4 animate-spin text-brand" />
          Running <span className="font-mono text-xs">{busy}</span>…
        </div>
      )}
      {error && <p className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">{error}</p>}
      {showLog && (
        <div>
          <button
            onClick={() => setOpen(!open)}
            className="flex cursor-pointer items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
          >
            {open || busy ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
            Output of <span className="font-mono">{job!.op}</span>
          </button>
          {(open || busy) && (
            <pre className="mt-2 max-h-72 overflow-auto rounded-lg border bg-background/60 p-3 font-mono text-xs leading-relaxed text-muted-foreground">
              {job!.log}
            </pre>
          )}
        </div>
      )}
    </div>
  );
}

export function Offline() {
  return <p className="text-sm text-muted-foreground">The agent is offline. Actions are available when it reconnects.</p>;
}
