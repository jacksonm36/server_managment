import { useEffect, useState } from "react";
import { api } from "./api";

type Build = { version: string; sha256: Record<string, string> };
let cached: Promise<Build> | null = null;

/** The agent version the panel can install; refreshed once per page load. */
export function useAgentBuild(): Build | null {
  const [b, setB] = useState<Build | null>(null);
  useEffect(() => {
    cached ??= api.agentBuild().catch(() => ({ version: "", sha256: {} }));
    void cached.then(setB);
  }, []);
  return b;
}

export const outdated = (b: Build | null, version: string) => !!b?.version && !!version && version !== b.version;
