import type { ServerGroup } from "@/lib/api";

export const GROUP_COLORS = ["#3b82f6", "#22c55e", "#f59e0b", "#ef4444", "#a855f7", "#06b6d4", "#ec4899", "#64748b"];

export function GroupChip({ group, onClick, active }: { group: Pick<ServerGroup, "name" | "color">; onClick?: () => void; active?: boolean }) {
  const color = group.color || "#64748b";
  const cls = `inline-flex items-center gap-1.5 rounded-md border px-2 py-0.5 text-xs font-medium whitespace-nowrap ${
    active ? "bg-accent text-foreground" : "text-muted-foreground"
  } ${onClick ? "cursor-pointer hover:text-foreground" : ""}`;
  const body = (
    <>
      {color !== "transparent" && <span className="size-2 rounded-full" style={{ background: color }} />}
      {group.name}
    </>
  );
  return onClick ? (
    <button type="button" className={cls} onClick={onClick} aria-pressed={active}>
      {body}
    </button>
  ) : (
    <span className={cls}>{body}</span>
  );
}
