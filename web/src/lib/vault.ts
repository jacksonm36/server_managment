import { useSyncExternalStore } from "react";
import { api, type VaultStatus } from "./api";

// Vault status shared by the banner and the Keys page.
let state: VaultStatus = { configured: false, unlocked: false };
const listeners = new Set<() => void>();

export function setVaultStatus(v: VaultStatus) {
  state = v;
  listeners.forEach((l) => l());
}

export async function refreshVault() {
  setVaultStatus(await api.vault());
}

export function useVault() {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => state,
  );
}
