import { useSyncExternalStore } from "react";
import { api, type LiveEvent, type Metric, type Server } from "./api";

// A tiny external store fed by the panel's SSE stream. Components subscribe
// with useSyncExternalStore, so only the parts that read changed data
// re-render.

const MAX_POINTS = 180;

type State = {
  servers: Map<string, Server>;
  metrics: Map<string, Metric[]>;
  connected: boolean;
  jobTick: number; // bumps whenever a job event arrives
  lastJob: { serverId: string; jobId: string } | null;
};

let state: State = {
  servers: new Map(),
  metrics: new Map(),
  connected: false,
  jobTick: 0,
  lastJob: null,
};
const listeners = new Set<() => void>();
let es: EventSource | null = null;
let retry: ReturnType<typeof setTimeout> | null = null;

function set(next: Partial<State>) {
  state = { ...state, ...next };
  listeners.forEach((l) => l());
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => listeners.delete(l);
}

export async function refreshServers() {
  const list = await api.servers();
  const servers = new Map(list.map((s) => [s.id, s]));
  set({ servers });
}

export async function loadMetricsHistory(id: string) {
  const hist = (await api.metrics(id)) ?? []; // null for a server with no samples yet
  const metrics = new Map(state.metrics);
  // Merge with anything that arrived live while the request was in flight.
  const live = metrics.get(id) ?? [];
  const lastTs = hist.length ? hist[hist.length - 1].ts : 0;
  metrics.set(id, [...hist, ...live.filter((m) => m.ts > lastTs)].slice(-MAX_POINTS));
  set({ metrics });
}

function onEvent(e: LiveEvent) {
  const cur = state.servers.get(e.server_id);
  switch (e.type) {
    case "status": {
      if (!cur) {
        void refreshServers(); // new server enrolled
        return;
      }
      const servers = new Map(state.servers);
      servers.set(e.server_id, { ...cur, online: !!e.online, last_seen_at: new Date().toISOString() });
      set({ servers });
      if (e.online) void refreshServers(); // pick up fresh Hello info
      break;
    }
    case "metrics": {
      if (!e.metric) return;
      const metrics = new Map(state.metrics);
      const arr = metrics.get(e.server_id) ?? [];
      metrics.set(e.server_id, [...arr, e.metric].slice(-MAX_POINTS));
      const next: Partial<State> = { metrics };
      if (cur) {
        const servers = new Map(state.servers);
        servers.set(e.server_id, { ...cur, online: true, latest: e.metric });
        next.servers = servers;
      }
      set(next);
      break;
    }
    case "job":
      set({ jobTick: state.jobTick + 1, lastJob: { serverId: e.server_id, jobId: e.job_id ?? "" } });
      break;
  }
}

export function startLive() {
  if (es) return;
  const connect = () => {
    es = new EventSource("/api/v1/events");
    es.onopen = () => {
      set({ connected: true });
      void refreshServers();
    };
    es.onmessage = (m) => onEvent(JSON.parse(m.data));
    es.onerror = () => {
      set({ connected: false });
      es?.close();
      es = null;
      retry = setTimeout(connect, 3000);
    };
  };
  connect();
}

export function stopLive() {
  if (retry) clearTimeout(retry);
  es?.close();
  es = null;
  set({ connected: false, servers: new Map(), metrics: new Map() });
}

export function useLive<T>(select: (s: State) => T): T {
  return useSyncExternalStore(subscribe, () => select(state));
}
