import { useEffect, useState } from "react";
import { api, type ServerGroup } from "./api";

// Server groups change rarely; share one copy across pages.
let cache: Promise<ServerGroup[]> | null = null;
const listeners = new Set<(g: ServerGroup[]) => void>();

export function reloadServerGroups() {
  cache = api.serverGroups().catch(() => []);
  void cache.then((g) => listeners.forEach((l) => l(g)));
  return cache;
}

export function useServerGroups() {
  const [groups, setGroups] = useState<ServerGroup[]>([]);
  useEffect(() => {
    listeners.add(setGroups);
    void (cache ?? reloadServerGroups()).then(setGroups);
    return () => void listeners.delete(setGroups);
  }, []);
  return groups;
}
