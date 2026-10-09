export type Metric = {
  ts: number;
  cpu: number;
  mem_total: number;
  mem_used: number;
  disk_total: number;
  disk_used: number;
  load1: number;
  load5: number;
  load15: number;
  uptime: number;
};

export type Server = {
  id: string;
  name: string;
  hostname: string;
  os: string;
  os_version: string;
  os_pretty: string;
  kernel: string;
  arch: string;
  cpu_count: number;
  agent_version: string;
  created_at: string;
  last_seen_at: string | null;
  online: boolean;
  latest: Metric | null;
};

export type Job = {
  id: string;
  server_id: string;
  op: string;
  params: Record<string, string>;
  status: "queued" | "running" | "succeeded" | "failed";
  log: string;
  result: unknown;
  error: string;
  created_at: string;
  finished_at: string | null;
};

export type JoinToken = {
  token: string;
  expires_at: string;
  ca_sha256: string;
  install_command: string;
};

export type LiveEvent = {
  type: "status" | "metrics" | "job";
  server_id: string;
  online?: boolean;
  metric?: Metric;
  job_id?: string;
};

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    credentials: "same-origin",
    headers: {
      "X-Requested-With": "servermgr",
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (res.status === 204) return undefined as T;
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new ApiError(res.status, data.error ?? res.statusText);
  return data as T;
}

export const api = {
  login: (username: string, password: string) =>
    request<{ username: string }>("POST", "/api/v1/auth/login", { username, password }),
  logout: () => request<void>("POST", "/api/v1/auth/logout"),
  me: () => request<{ username: string }>("GET", "/api/v1/me"),
  servers: () => request<Server[]>("GET", "/api/v1/servers"),
  server: (id: string) => request<Server>("GET", `/api/v1/servers/${id}`),
  deleteServer: (id: string) => request<void>("DELETE", `/api/v1/servers/${id}`),
  metrics: (id: string) => request<Metric[]>("GET", `/api/v1/servers/${id}/metrics`),
  jobs: (id: string) => request<Job[]>("GET", `/api/v1/servers/${id}/jobs`),
  job: (id: string) => request<Job>("GET", `/api/v1/jobs/${id}`),
  runJob: (id: string, op: string) => request<Job>("POST", `/api/v1/servers/${id}/jobs`, { op }),
  createJoinToken: (name: string) => request<JoinToken>("POST", "/api/v1/join-tokens", { name }),
};
