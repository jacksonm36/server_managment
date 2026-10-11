import { useMemo } from "react";
import { useLive } from "@/lib/live";
import { useServerGroups } from "@/lib/groups";
import { GroupChip } from "@/components/GroupChip";

/** Checkbox list of servers; value is the selected server IDs. */
export function ServerPicker({ value, onChange }: { value: string[]; onChange: (ids: string[]) => void }) {
  const map = useLive((s) => s.servers);
  const servers = useMemo(() => [...map.values()].sort((a, b) => a.name.localeCompare(b.name)), [map]);
  const all = servers.length > 0 && value.length === servers.length;
  const groups = useServerGroups().filter((g) => g.server_ids.length > 0);
  return (
    <div className="grid gap-2">
      {groups.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="text-xs text-muted-foreground">Add group:</span>
          {groups.map((g) => (
            <GroupChip
              key={g.id}
              group={g}
              active={g.server_ids.every((id) => value.includes(id))}
              onClick={() => onChange([...new Set([...value, ...g.server_ids.filter((id) => map.has(id))])])}
            />
          ))}
        </div>
      )}
      <label className="flex cursor-pointer items-center gap-2 text-sm font-medium">
        <input type="checkbox" className="accent-[var(--color-brand)]" checked={all} onChange={() => onChange(all ? [] : servers.map((s) => s.id))} />
        All servers ({servers.length})
      </label>
      <div className="grid max-h-56 gap-1 overflow-y-auto rounded-lg border p-2">
        {servers.map((s) => (
          <label key={s.id} className="flex cursor-pointer items-center gap-2 rounded px-1.5 py-1 text-sm hover:bg-accent/40">
            <input
              type="checkbox"
              className="accent-[var(--color-brand)]"
              checked={value.includes(s.id)}
              onChange={(e) => onChange(e.target.checked ? [...value, s.id] : value.filter((x) => x !== s.id))}
            />
            <span className={`size-2 rounded-full ${s.online ? "bg-success" : "bg-muted-foreground/50"}`} />
            {s.name}
            <span className="truncate text-xs text-muted-foreground">{s.os_pretty}</span>
          </label>
        ))}
      </div>
    </div>
  );
}
