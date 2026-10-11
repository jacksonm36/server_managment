import { createContext, useContext, useMemo } from "react";

// What the signed-in user may do, from /api/v1/me. Server permissions can
// also be granted per server (through server groups).
export type AccessData = { role: string; perms: Set<string>; server: Record<string, Set<string>> };

export const noAccess: AccessData = { role: "none", perms: new Set(), server: {} };

export const AccessContext = createContext<AccessData>(noAccess);

export function useAccess() {
  const a = useContext(AccessContext);
  // Stable while the access does not change, so it can be an effect dependency.
  return useMemo(
    () => ({
      role: a.role,
      can: (perm: string, serverId?: string) => a.perms.has(perm) || (!!serverId && !!a.server[serverId]?.has(perm)),
      // canAnywhere: on all servers or at least one.
      canAnywhere: (perm: string) => a.perms.has(perm) || Object.values(a.server).some((s) => s.has(perm)),
    }),
    [a],
  );
}

export type Access = ReturnType<typeof useAccess>;

export function accessFromMe(me: { role: string; permissions?: string[]; server_permissions?: Record<string, string[]> }): AccessData {
  const server: Record<string, Set<string>> = {};
  for (const [id, ps] of Object.entries(me.server_permissions ?? {})) server[id] = new Set(ps);
  return { role: me.role, perms: new Set(me.permissions ?? []), server };
}

// readOnly is true when the user can only look.
export function readOnly(a: AccessData) {
  const viewOnly = (p: string) => p.endsWith(".view");
  return [...a.perms].every(viewOnly) && Object.values(a.server).every((s) => [...s].every(viewOnly));
}
