import { memo, useMemo, useState, type PointerEvent } from "react";
import { Card } from "@/components/ui/card";

type Point = { ts: number; v: number };

const W = 600;
const H = 140;
const WINDOW_MS = 15 * 60 * 1000;

// Lightweight SVG area chart for 0-100 % series. No chart library: a 180-point
// path is cheap to redraw every 5 s and keeps the bundle small.
export const LiveChart = memo(function LiveChart({
  title,
  points,
  detail,
  color = "var(--color-brand)",
}: {
  title: string;
  points: Point[];
  detail?: string;
  color?: string;
}) {
  const [hover, setHover] = useState<number | null>(null);
  const now = points.length ? points[points.length - 1].ts : Date.now();
  const start = now - WINDOW_MS;

  const { line, area, xy } = useMemo(() => {
    const xy = points
      .filter((p) => p.ts >= start)
      .map((p) => [((p.ts - start) / WINDOW_MS) * W, H - (Math.min(100, Math.max(0, p.v)) / 100) * H] as const);
    if (!xy.length) return { line: "", area: "", xy };
    const line = xy.map(([x, y], i) => `${i ? "L" : "M"}${x.toFixed(1)},${y.toFixed(1)}`).join("");
    const area = `${line}L${xy[xy.length - 1][0].toFixed(1)},${H}L${xy[0][0].toFixed(1)},${H}Z`;
    return { line, area, xy };
  }, [points, start]);

  const visible = points.filter((p) => p.ts >= start);
  const shown = hover !== null ? visible[hover] : visible[visible.length - 1];
  const gradId = `g-${title.replace(/\W/g, "")}`;

  function onMove(e: PointerEvent<SVGSVGElement>) {
    if (!xy.length) return;
    const rect = e.currentTarget.getBoundingClientRect();
    const x = ((e.clientX - rect.left) / rect.width) * W;
    let best = 0;
    for (let i = 1; i < xy.length; i++) if (Math.abs(xy[i][0] - x) < Math.abs(xy[best][0] - x)) best = i;
    setHover(best);
  }

  return (
    <Card className="p-4">
      <div className="mb-3 flex items-baseline justify-between gap-2">
        <div className="text-sm font-medium text-muted-foreground">{title}</div>
        <div className="text-right">
          <span className="tabular text-xl font-semibold">{shown ? `${shown.v.toFixed(1)}%` : "—"}</span>
          {hover !== null && shown && (
            <div className="tabular text-xs text-muted-foreground">{new Date(shown.ts).toLocaleTimeString()}</div>
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
        aria-label={`${title} over the last 15 minutes`}
      >
        <defs>
          <linearGradient id={gradId} x1="0" x2="0" y1="0" y2="1">
            <stop offset="0%" stopColor={color} stopOpacity="0.35" />
            <stop offset="100%" stopColor={color} stopOpacity="0" />
          </linearGradient>
        </defs>
        {[25, 50, 75].map((g) => (
          <line
            key={g}
            x1="0"
            x2={W}
            y1={H - (g / 100) * H}
            y2={H - (g / 100) * H}
            stroke="currentColor"
            className="text-border"
            strokeDasharray="3 4"
            vectorEffect="non-scaling-stroke"
          />
        ))}
        {area && <path d={area} fill={`url(#${gradId})`} />}
        {line && (
          <path d={line} fill="none" stroke={color} strokeWidth="2" vectorEffect="non-scaling-stroke" strokeLinejoin="round" />
        )}
        {hover !== null && xy[hover] && (
          <line
            x1={xy[hover][0]}
            x2={xy[hover][0]}
            y1="0"
            y2={H}
            stroke="currentColor"
            className="text-muted-foreground"
            vectorEffect="non-scaling-stroke"
          />
        )}
      </svg>
      <div className="mt-2 flex justify-between text-xs text-muted-foreground">
        <span>15 min ago</span>
        <span>{detail}</span>
        <span>now</span>
      </div>
    </Card>
  );
});

export function Meter({ value, className = "" }: { value: number; className?: string }) {
  const v = Math.min(100, Math.max(0, value));
  const color = v > 90 ? "bg-destructive" : v > 75 ? "bg-warning" : "bg-brand";
  return (
    <div className={`h-1.5 w-full overflow-hidden rounded-full bg-muted ${className}`}>
      <div className={`h-full rounded-full ${color} transition-[width] duration-500`} style={{ width: `${v}%` }} />
    </div>
  );
}
