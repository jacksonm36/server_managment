import { useEffect, useState } from "react";
import { CircleCheck, CircleX, Loader2, Pause, Play, Power, RotateCw, Square } from "lucide-react";

// PowerBanner shows a power action while it runs: what is happening, for
// how long, and a sweeping bar; then a check (or the error) for a moment.

export type PowerState = { action: string; phase: "running" | "done" | "failed"; error?: string; started: number } | null;

const words: Record<string, { ing: string; done: string; tone: "go" | "slow" | "stop"; icon: typeof Play }> = {
  start: { ing: "Starting", done: "Running", tone: "go", icon: Play },
  resume: { ing: "Resuming", done: "Running", tone: "go", icon: Play },
  shutdown: { ing: "Shutting down", done: "Shut down", tone: "slow", icon: Power },
  reboot: { ing: "Rebooting", done: "Rebooted", tone: "slow", icon: RotateCw },
  suspend: { ing: "Suspending", done: "Paused", tone: "slow", icon: Pause },
  stop: { ing: "Stopping", done: "Stopped", tone: "stop", icon: Square },
  startall: { ing: "Starting all guests", done: "Guests started", tone: "go", icon: Play },
  stopall: { ing: "Shutting down all guests", done: "Guests shut down", tone: "slow", icon: Square },
  migrateall: { ing: "Migrating all guests", done: "Guests migrated", tone: "slow", icon: RotateCw },
};
export const actionWord = (a: string) => words[a]?.ing ?? a;

const tones = {
  go: { box: "border-success/30 bg-success/10", bar: "bg-success", text: "text-success" },
  slow: { box: "border-warning/30 bg-warning/10", bar: "bg-warning", text: "text-warning" },
  stop: { box: "border-destructive/30 bg-destructive/10", bar: "bg-destructive", text: "text-destructive" },
};

export function PowerBanner({ state, name, hint, onDismiss }: { state: PowerState; name: string; hint?: string; onDismiss: () => void }) {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    if (state?.phase !== "running") return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [state?.phase]);
  useEffect(() => {
    if (state?.phase !== "done") return;
    const t = setTimeout(onDismiss, 3500);
    return () => clearTimeout(t);
  }, [state?.phase, onDismiss]);
  if (!state) return null;
  const w = words[state.action] ?? { ing: state.action, done: "Done", tone: "slow" as const, icon: Power };
  const tone = state.phase === "failed" ? tones.stop : tones[w.tone];
  const secs = Math.max(0, Math.round((now - state.started) / 1000));
  const Icon = w.icon;
  return (
    <div role="status" aria-live="polite" className={`animate-fade-in relative overflow-hidden rounded-lg border px-3 py-2 text-sm ${tone.box}`}>
      <div className="flex items-center gap-2">
        {state.phase === "running" ? (
          <span className={`relative grid size-6 place-items-center ${tone.text}`}>
            <Loader2 className="absolute size-6 animate-spin opacity-60" />
            <Icon className="size-3" />
          </span>
        ) : state.phase === "done" ? (
          <CircleCheck className={`animate-pop size-5 ${tone.text}`} />
        ) : (
          <CircleX className="animate-pop size-5 text-destructive" />
        )}
        <span className="min-w-0 flex-1 truncate">
          {state.phase === "running" ? (
            <>
              <span className="font-medium">{w.ing}</span> {name}…{hint && <span className="text-muted-foreground"> {hint}</span>}
            </>
          ) : state.phase === "done" ? (
            <>
              <span className="font-medium">{name}</span>: {w.done.toLowerCase()} after {secs}s.
            </>
          ) : (
            <>
              <span className="font-medium">{w.ing} failed:</span> {state.error}
            </>
          )}
        </span>
        {state.phase === "running" ? (
          <span className="tabular text-xs text-muted-foreground">{secs}s</span>
        ) : (
          <button type="button" onClick={onDismiss} className="cursor-pointer text-xs text-muted-foreground hover:text-foreground">
            Dismiss
          </button>
        )}
      </div>
      <div className="absolute inset-x-0 bottom-0 h-0.5 overflow-hidden">
        {state.phase === "running" ? (
          <div className={`animate-sweep h-full w-2/5 rounded-full ${tone.bar}`} />
        ) : (
          <div className={`h-full w-full ${tone.bar} opacity-60`} />
        )}
      </div>
    </div>
  );
}
