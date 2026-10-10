import { useCallback, useEffect, useRef, useState } from "react";
import { api, type Job } from "./api";

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/** Runs an agent op and resolves with the finished job. onProgress gets each poll. */
export async function runOp(
  serverId: string,
  op: string,
  params: Record<string, string> = {},
  onProgress?: (j: Job) => void,
): Promise<Job> {
  const started = await api.runJob(serverId, op, params);
  onProgress?.(started);
  const deadline = Date.now() + 20 * 60 * 1000;
  let delay = 300;
  while (Date.now() < deadline) {
    await sleep(delay);
    delay = Math.min(delay * 1.4, 1500);
    const j = await api.job(started.id);
    onProgress?.(j);
    if (j.status === "succeeded" || j.status === "failed") return j;
  }
  throw new Error("timed out waiting for the agent");
}

/** Polls an already-started job until it finishes. */
export async function waitJob(started: Job, onProgress?: (j: Job) => void): Promise<Job> {
  onProgress?.(started);
  const deadline = Date.now() + 20 * 60 * 1000;
  let delay = 500;
  while (Date.now() < deadline) {
    await sleep(delay);
    delay = Math.min(delay * 1.4, 2000);
    const j = await api.job(started.id);
    onProgress?.(j);
    if (j.status === "succeeded" || j.status === "failed") return j;
  }
  throw new Error("timed out waiting for the agent");
}

/**
 * Agent-backed state for a tab: loads with `statusOp` when the server is
 * online, and `run` executes mutating ops whose result is the new state.
 */
export function useAgentState<T>(serverId: string, statusOp: string, online: boolean, params: Record<string, string> = {}) {
  const [data, setData] = useState<T | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [job, setJob] = useState<Job | null>(null);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  const run = useCallback(
    async (op: string, p: Record<string, string> = {}): Promise<Job | null> => {
      setBusy(op);
      setError("");
      try {
        const j = await runOp(serverId, op, p, (cur) => alive.current && setJob(cur));
        if (!alive.current) return j;
        if (j.status === "failed") throw new Error(j.error || "operation failed");
        if (j.result != null) setData(j.result as T);
        return j;
      } catch (e) {
        if (alive.current) setError((e as Error).message);
        return null;
      } finally {
        if (alive.current) setBusy(null);
      }
    },
    [serverId],
  );

  const key = JSON.stringify(params);
  useEffect(() => {
    if (online) void run(statusOp, JSON.parse(key));
  }, [run, statusOp, key, online]);

  return { data, setData, busy, error, job, run };
}
