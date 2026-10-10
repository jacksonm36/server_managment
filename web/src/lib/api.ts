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
  created_by: string;
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

export type AuditEntry = {
  id: number;
  ts: string;
  actor: string;
  ip: string;
  action: string;
  target: string;
  detail: unknown;
  success: boolean;
};

export type JobSummary = {
  id: string;
  server_id: string;
  server_name: string;
  op: string;
  status: Job["status"];
  error: string;
  created_by: string;
  created_at: string;
  finished_at: string | null;
};

export type Step = { op: string; params: Record<string, string>; continue_on_error?: boolean };

export type Playbook = {
  id: string;
  name: string;
  description: string;
  steps: Step[];
  builtin: boolean;
  created_at: string;
  updated_at: string;
};

export type PlaybookRun = {
  id: string;
  playbook_id: string | null;
  playbook_name: string;
  server_id: string;
  server_name: string;
  status: "running" | "succeeded" | "failed";
  steps: (Step & { status: string; job_id?: string; error?: string })[];
  created_by: string;
  created_at: string;
  finished_at: string | null;
};

export type Schedule = {
  id: string;
  name: string;
  cron: string;
  kind: "op" | "playbook";
  op: string;
  params: Record<string, string>;
  playbook_id: string | null;
  server_ids: string[];
  all_servers: boolean;
  enabled: boolean;
  created_by: string;
  last_run: string | null;
  last_result: string;
  next_run: string | null;
};

type ScheduleBody = Omit<Schedule, "id" | "created_by" | "last_run" | "last_result" | "next_run" | "playbook_id"> & { playbook_id: string };

export type FeatureStatus = "done" | "in-progress" | "todo";

export type Progress = {
  updated_at: string;
  phases: {
    name: string;
    description?: string;
    features: { name: string; status: FeatureStatus }[];
  }[];
};

export type Account = { username: string; totp_enabled: boolean; recovery_codes_left: number };

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
    public data: Record<string, unknown> = {},
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
  if (!res.ok) throw new ApiError(res.status, data.error ?? res.statusText, data);
  return data as T;
}

export const api = {
  login: (username: string, password: string, otp?: string) =>
    request<{ username: string }>("POST", "/api/v1/auth/login", otp ? { username, password, otp } : { username, password }),
  account: () => request<Account>("GET", "/api/v1/account"),
  changePassword: (current: string, next: string) => request<void>("POST", "/api/v1/account/password", { current, new: next }),
  totpSetup: (password: string) =>
    request<{ secret: string; otpauth_uri: string }>("POST", "/api/v1/account/totp/setup", { password }),
  totpEnable: (code: string) => request<{ recovery_codes: string[] }>("POST", "/api/v1/account/totp/enable", { code }),
  totpDisable: (password: string, code: string) => request<void>("POST", "/api/v1/account/totp/disable", { password, code }),
  recoveryCodes: (password: string) =>
    request<{ recovery_codes: string[] }>("POST", "/api/v1/account/recovery-codes", { password }),
  logout: () => request<void>("POST", "/api/v1/auth/logout"),
  me: () => request<{ username: string }>("GET", "/api/v1/me"),
  servers: () => request<Server[]>("GET", "/api/v1/servers"),
  server: (id: string) => request<Server>("GET", `/api/v1/servers/${id}`),
  deleteServer: (id: string) => request<void>("DELETE", `/api/v1/servers/${id}`),
  metrics: (id: string) => request<Metric[]>("GET", `/api/v1/servers/${id}/metrics`),
  jobs: (id: string) => request<Job[]>("GET", `/api/v1/servers/${id}/jobs`),
  job: (id: string) => request<Job>("GET", `/api/v1/jobs/${id}`),
  runJob: (id: string, op: string, params: Record<string, string> = {}) =>
    request<Job>("POST", `/api/v1/servers/${id}/jobs`, { op, params }),
  audit: (q: { action?: string; before?: number; limit?: number } = {}) => {
    const p = new URLSearchParams();
    if (q.action) p.set("action", q.action);
    if (q.before) p.set("before", String(q.before));
    if (q.limit) p.set("limit", String(q.limit));
    return request<AuditEntry[]>("GET", `/api/v1/audit?${p}`);
  },
  recentJobs: () => request<JobSummary[]>("GET", "/api/v1/jobs?limit=200"),
  bulk: (serverIds: string[], op: string, params: Record<string, string>) =>
    request<{ server_id: string; job_id?: string; error?: string }[]>("POST", "/api/v1/bulk", { server_ids: serverIds, op, params }),
  playbooks: () => request<Playbook[]>("GET", "/api/v1/playbooks"),
  createPlaybook: (p: Pick<Playbook, "name" | "description" | "steps">) => request<Playbook>("POST", "/api/v1/playbooks", p),
  updatePlaybook: (id: string, p: Pick<Playbook, "name" | "description" | "steps">) => request<Playbook>("PUT", `/api/v1/playbooks/${id}`, p),
  deletePlaybook: (id: string) => request<void>("DELETE", `/api/v1/playbooks/${id}`),
  runPlaybook: (id: string, serverIds: string[]) => request<PlaybookRun[]>("POST", `/api/v1/playbooks/${id}/run`, { server_ids: serverIds }),
  playbookRuns: () => request<PlaybookRun[]>("GET", "/api/v1/playbook-runs?limit=50"),
  schedules: () => request<Schedule[]>("GET", "/api/v1/schedules"),
  createSchedule: (s: ScheduleBody) => request<Schedule>("POST", "/api/v1/schedules", pickSchedule(s)),
  updateSchedule: (id: string, s: ScheduleBody) => request<Schedule>("PUT", `/api/v1/schedules/${id}`, pickSchedule(s)),
  deleteSchedule: (id: string) => request<void>("DELETE", `/api/v1/schedules/${id}`),
  runScheduleNow: (id: string) => request<{ result: string }>("POST", `/api/v1/schedules/${id}/run`),
  progress: () => request<Progress>("GET", "/api/v1/progress"),
  createJoinToken: (name: string) => request<JoinToken>("POST", "/api/v1/join-tokens", { name }),
};

// The API rejects unknown fields, so send exactly what it accepts.
function pickSchedule(s: ScheduleBody) {
  const { name, cron, kind, op, params, playbook_id, server_ids, all_servers, enabled } = s;
  return { name, cron, kind, op, params, playbook_id, server_ids, all_servers, enabled };
}
