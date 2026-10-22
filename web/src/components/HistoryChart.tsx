import { memo, useMemo, useState, type PointerEvent } from "react";
import { Card } from "@/components/ui/card";

export type Series = { label: string; color: string; values: (number | null)[] };

const W = 600;
const H = 140;

// SVG line chart for Proxmox RRD history: one or more series over shared
// timestamps (seconds), auto-scaled, with a hover readout. Like LiveChart,
// no chart library.
export const HistoryChart = memo(function HistoryChart({
  title,
  times,
  series,
  format,
  max,
}: {
  title: string;
  times: number[];
  series: Series[];
  format: (v: number) => string;
  max?: number; // fixed top of the scale, e.g. 100 for percent
}) {
  const [hover, setHover] = useState<number | null>(null);
  const t0 = times[0] ?? 0;
  const t1 = times[times.length - 1] ?? 1;
  const span = Math.max(1, t1 - t0);

  const peak = useMemo(() => {
    let m = 0;
    for (const s of series) for (const v of s.values) if (v != null && v > m) m = v;
    return m;
  }, [series]);
  const top = max ?? (peak > 0 ? peak * 1.1 : 1);

  const paths = useMemo(
    () =>
      series.map((s) => {
        let d = "";
        let pen = false;
        s.values.forEach((v, i) => {
          if (v == null || !Number.isFinite(v)) {
            pen = false;
            return;
          }
          const x = ((times[i] - t0) / span) * W;
          const y = H - (Math.min(v, top) / top) * H;
          d += `${pen ? "L" : "M"}${x.toFixed(1)},${y.toFixed(1)}`;
          pen = true;
        });
        return d;
      }),
    [series, times, t0, span, top],
  );

  function onMove(e: PointerEvent<SVGSVGElement>) {
    if (!times.length) return;
    const rect = e.currentTarget.getBoundingClientRect();
    const t = t0 + ((e.clientX - rect.left) / rect.width) * span;
    let best = 0;
    for (let i = 1; i < times.length; i++) if (Math.abs(times[i] - t) < Math.abs(times[best] - t)) best = i;
    setHover(best);
  }

  const idx = hover ?? lastIndex(series, times.length);
  const when = (t: number) =>
    span > 2 * 86400 ? new Date(t * 1000).toLocaleDateString(undefined, { month: "short", day: "numeric" }) : new Date(t * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });

  return (
    <Card className="p-4">
      <div className="mb-3 flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <div className="text-sm font-medium text-muted-foreground">{title}</div>
        <div className="flex flex-wrap items-baseline gap-x-3 text-right">
          {series.map((s) => {
            const v = idx >= 0 ? s.values[idx] : null;
            return (
              <span key={s.label} className="tabular text-sm">
                {series.length > 1 && (
                  <span className="mr-1 text-xs text-muted-foreground">
                    <span className="mr-1 inline-block size-2 rounded-full" style={{ background: s.color }} />
                    {s.label}
                  </span>
                )}
                <span className="font-semibold">{v != null ? format(v) : "—"}</span>
              </span>
            );
          })}
          {hover !== null && times[hover] && (
            <span className="tabular text-xs text-muted-foreground">{new Date(times[hover] * 1000).toLocaleString()}</span>
          )}
        </div>
      </div>
      <svg
        viewBox={`0 0 ${W} ${H}`}
        preserveAspectRatio="none"
        className="h-32 w-full touch-none overflow-visible"
        onPointerMove={onMove}
        onPointerLeave={() => setHover(null)}
        role="img"
        aria-label={title}
      >
        {[0.25, 0.5, 0.75].map((g) => (
          <line key={g} x1="0" x2={W} y1={H * g} y2={H * g} stroke="currentColor" className="text-border" strokeDasharray="3 4" vectorEffect="non-scaling-stroke" />
        ))}
        {paths.map((d, i) => (
          <path key={series[i].label} d={d} fill="none" stroke={series[i].color} strokeWidth="2" vectorEffect="non-scaling-stroke" strokeLinejoin="round" />
        ))}
        {hover !== null && times[hover] !== undefined && (
          <line
            x1={((times[hover] - t0) / span) * W}
            x2={((times[hover] - t0) / span) * W}
            y1="0"
            y2={H}
            stroke="currentColor"
            className="text-muted-foreground"
            vectorEffect="non-scaling-stroke"
          />
        )}
      </svg>
      <div className="mt-2 flex justify-between text-xs text-muted-foreground">
        <span>{times.length ? when(t0) : ""}</span>
        <span>peak {format(peak)}</span>
        <span>{times.length ? when(t1) : ""}</span>
      </div>
    </Card>
  );
});

// lastIndex is the newest sample that has a value in any series; Proxmox
// often leaves the current, unfinished interval empty.
function lastIndex(series: Series[], n: number) {
  for (let i = n - 1; i >= 0; i--) if (series.some((s) => s.values[i] != null)) return i;
  return -1;
}
