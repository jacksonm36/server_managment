import { useSyncExternalStore } from "react";

// Power actions in flight (guest start, node reboot…), so every view of a
// guest or node (its page, the tree) can show it moving. Keys are
// "node/vmid" for guests and "node" for hosts.

const pending = new Map<string, string>();
const subs = new Set<() => void>();
let version = 0;

function emit() {
  version++;
  subs.forEach((f) => f());
}

export function setPending(key: string, action: string | null) {
  if (action) pending.set(key, action);
  else pending.delete(key);
  emit();
}

export function usePending(key: string): string | undefined {
  useSyncExternalStore(
    (f) => {
      subs.add(f);
      return () => subs.delete(f);
    },
    () => version,
  );
  return pending.get(key);
}
