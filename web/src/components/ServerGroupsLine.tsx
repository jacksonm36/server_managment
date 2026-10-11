import { useState } from "react";
import { Check, Pencil } from "lucide-react";
import { api } from "@/lib/api";
import { reloadServerGroups, useServerGroups } from "@/lib/groups";
import { useAccess } from "@/lib/access";
import { GroupChip } from "@/components/GroupChip";
import { Button } from "@/components/ui/button";

/** The server's groups; users who manage access can change them in place. */
export function ServerGroupsLine({ serverId }: { serverId: string }) {
  const groups = useServerGroups();
  const { can } = useAccess();
  const [editing, setEditing] = useState(false);
  const [error, setError] = useState("");
  const mine = groups.filter((g) => g.server_ids.includes(serverId));
  const editable = can("users.manage") && groups.length > 0;
  if (!mine.length && !editable) return null;

  async function toggle(id: number) {
    const ids = mine.map((g) => g.id);
    setError("");
    try {
      await api.setServerGroups(serverId, ids.includes(id) ? ids.filter((x) => x !== id) : [...ids, id]);
      await reloadServerGroups();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  return (
    <div className="mt-2 flex flex-wrap items-center gap-1.5">
      {(editing ? groups : mine).map((g) => (
        <GroupChip key={g.id} group={g} active={editing && mine.includes(g)} onClick={editing ? () => toggle(g.id) : undefined} />
      ))}
      {editable && (
        <Button variant="ghost" size="sm" className="h-6 px-2 text-xs" onClick={() => setEditing(!editing)}>
          {editing ? <Check /> : <Pencil />} {editing ? "Done" : mine.length ? "Groups" : "Add to group"}
        </Button>
      )}
      {error && <span className="text-xs text-destructive">{error}</span>}
    </div>
  );
}
