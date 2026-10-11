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
  groups?: number[];
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
  steps: (Step & { status: string; job_id?: string; error?: string; rollback?: boolean })[];
  created_by: string;
  created_at: string;
  finished_at: string | null;
};

export type Schedule = {
  id: string;
  name: string;
  cron: string;
  kind: "op" | "playbook" | "ansible";
  op: string;
  params: Record<string, string>;
  playbook_id: string | null;
  server_ids: string[];
  all_servers: boolean;
  server_group_id: number | null;
  enabled: boolean;
  created_by: string;
  last_run: string | null;
  last_result: string;
  next_run: string | null;
};

type ScheduleBody = Omit<Schedule, "id" | "created_by" | "last_run" | "last_result" | "next_run" | "playbook_id"> & { playbook_id: string };

export type Channel = {
  id: string;
  name: string;
  type: "email" | "telegram" | "discord" | "webhook";
  config: Record<string, string>;
  enabled: boolean;
  last_sent_at: string | null;
  last_error: string;
};

export type AlertRule = {
  id: string;
  name: string;
  metric: "cpu" | "mem" | "disk" | "load" | "offline";
  threshold: number;
  duration_sec: number;
  server_ids: string[];
  all_servers: boolean;
  channel_ids: string[];
  enabled: boolean;
};

export type AlertEvent = {
  id: number;
  rule_id: string;
  rule_name: string;
  server_id: string;
  server_name: string;
  metric: string;
  value: number;
  threshold: number;
  state: "firing" | "resolved";
  started_at: string;
  resolved_at: string | null;
};

type ChannelBody = Pick<Channel, "name" | "type" | "config" | "enabled">;
const ruleBody = (r: AlertRule) => {
  const { name, metric, threshold, duration_sec, server_ids, all_servers, channel_ids, enabled } = r;
  return { name, metric, threshold, duration_sec, server_ids, all_servers, channel_ids, enabled };
};

export type User = {
  id: number;
  username: string;
  display_name: string;
  email: string;
  role: string;
  disabled: boolean;
  must_change_password: boolean;
  totp_enabled: boolean;
  groups: number[];
  sessions: number;
  last_login: string | null;
  created_at: string;
};

export type UserBody = {
  username?: string;
  password?: string;
  display_name: string;
  email: string;
  role: string;
  disabled: boolean;
  must_change_password?: boolean;
  groups: number[];
};

export type VaultStatus = { configured: boolean; unlocked: boolean };

export type SSHKey = {
  id: number;
  name: string;
  type: string;
  bits: number;
  fingerprint: string;
  comment: string;
  source: string;
  created_by: string;
  created_at: string;
  warning?: string;
};

export type KeyCandidate = {
  line?: string;
  file?: string;
  type?: string;
  bits?: number;
  fingerprint?: string;
  comment?: string;
  name?: string;
  name_from?: string;
  hosts?: string[];
  existing?: string;
  warning?: string;
  error?: string;
};

export type ScannedServer = {
  server_id: string;
  name: string;
  users: { user: string; keys: { type: string; fingerprint: string; comment: string }[] }[];
  error?: string;
};

export type KeyTarget = { server_ids?: string[]; server_group_id?: number; user: string };

export type BulkResult = { server_id: string; job_id?: string; error?: string };

export type Me = {
  username: string;
  display_name: string;
  role: string;
  must_change_password: boolean;
  permissions: string[];
  server_permissions: Record<string, string[]>;
  vault: VaultStatus;
};

export type Permission = { key: string; label: string; group: string; server: boolean };

export type RoleInfo = {
  id: number;
  name: string;
  description: string;
  permissions: string[];
  builtin: boolean;
  users: number;
  grants: number;
};

export type UserGroup = { id: number; name: string; description: string; members: number[] };

export type ServerGroup = { id: number; name: string; description: string; color: string; server_ids: string[] };

export type Grant = {
  id: number;
  user_id: number | null;
  user_group_id: number | null;
  role_id: number;
  role: string;
  server_group_id: number | null;
  server_group: string;
};

export type CatalogApp = {
  id: string;
  name: string;
  category: string;
  description: string;
  version: string;
  php_min: string;
  php_max?: string;
  database: boolean;
  admin: boolean;
  admin_path: string;
  homepage: string;
  notes?: string;
  updatable: boolean;
  kind: "php" | "compose";
  needs_docker: boolean;
  min_ram_mb?: number;
  protect: boolean;
};

export type PVECluster = { id: string; name: string; url: string; token_id: string; fingerprint: string; created_at: string; updated_at: string };
export type PVEResource = {
  id: string;
  type: "node" | "qemu" | "lxc" | "storage";
  node: string;
  vmid: number;
  name: string;
  status: string;
  cpu: number;
  maxcpu: number;
  mem: number;
  maxmem: number;
  disk: number;
  maxdisk: number;
  uptime: number;
  template?: number;
  tags?: string;
};
export type PVEGuest = PVEResource & { server_id?: string; server_name?: string };
export type PVEResources = { cluster: PVECluster; version?: string; error?: string; nodes?: PVEResource[]; guests?: PVEGuest[]; storage?: PVEResource[] };
export type PVESnapshot = { name: string; description?: string; snaptime?: number; vmstate?: number; parent?: string };
export type PVEProvision = {
  id: string;
  cluster_id: string;
  name: string;
  vmid: number | null;
  status: "running" | "succeeded" | "failed";
  log: string;
  server_id: string | null;
  created_by: string;
  created_at: string;
  finished_at: string | null;
};

export type TofuVar = { key: string; value: string; kind: "tf" | "env"; sensitive: boolean };
export type TofuWorkspace = {
  id: string;
  name: string;
  description: string;
  source: "inline" | "git";
  files: Record<string, string>;
  git_url: string;
  git_ref: string;
  git_path: string;
  created_at: string;
  updated_at: string;
};
export type TofuSummary = {
  changes?: boolean;
  add?: number;
  change?: number;
  destroy?: number;
  added?: number;
  changed?: number;
  destroyed?: number;
  outputs?: Record<string, unknown>;
};
export type TofuRun = {
  id: string;
  workspace_id: string;
  workspace_name: string;
  kind: "plan" | "destroy-plan" | "apply";
  plan_run_id: string | null;
  status: "running" | "succeeded" | "failed" | "cancelled";
  summary: TofuSummary | null;
  applied: boolean;
  created_by: string;
  created_at: string;
  finished_at: string | null;
};
export type TofuWorkspaceView = {
  workspace: TofuWorkspace;
  vars: (TofuVar & { has_value: boolean })[];
  vars_error?: string;
  state?: { serial: number; updated_at: string; resources: string[] | null };
  locked?: boolean;
  latest_plan?: TofuRun;
  active_run: string;
};
export type TofuOverview = {
  installed: boolean;
  version: string;
  setup: { running: boolean; error: string };
  workspaces: TofuWorkspaceView[];
};

export type AnsibleHost = {
  server_id: string;
  name: string;
  online: boolean;
  address: string;
  port: number;
  root_login: string;
  prepared_at: string | null;
};
export type AnsibleOverview = {
  installed: boolean;
  version: string;
  setup: { running: boolean; log: string; error: string };
  public_key: string;
  hosts: AnsibleHost[];
};
export type AnsiblePlaybook = {
  id: string;
  name: string;
  description: string;
  source: "inline" | "git";
  content: string;
  git_url: string;
  git_ref: string;
  git_path: string;
  created_at: string;
  updated_at: string;
};
export type AnsibleRecap = { ok: number; changed: number; unreachable: number; failed: number; skipped: number };
export type AnsibleRun = {
  id: string;
  playbook_id: string | null;
  playbook_name: string;
  server_ids: string[];
  check: boolean;
  status: "running" | "succeeded" | "failed" | "cancelled";
  recap: Record<string, AnsibleRecap> | null;
  created_by: string;
  created_at: string;
  finished_at: string | null;
};

// Shapes are cscli's JSON output, passed through by the panel.
export type CsDecision = { id: number; value: string; scope: string; type: string; duration: string; origin: string; scenario: string };
export type CsAlert = {
  id: number;
  scenario: string;
  machine_id: string;
  created_at: string;
  events_count: number;
  source: { ip?: string; range?: string; cn?: string; as_name?: string; scope: string; value: string };
  decisions?: CsDecision[];
};
export type CrowdSecOverview = {
  available: boolean;
  error?: string;
  lapi_url?: string;
  machines?: { machineId: string; isValidated: boolean; last_heartbeat?: string; version?: string; ipAddress?: string }[];
  bouncers?: { name: string; last_pull?: string; version?: string; ip_address?: string; revoked?: boolean }[];
  decisions?: CsAlert[];
  alerts?: CsAlert[];
  allowlist?: { items?: { value: string; created_at: string }[] } | null;
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
  me: () => request<Me>("GET", "/api/v1/me"),
  users: () => request<User[]>("GET", "/api/v1/users"),
  createUser: (u: UserBody) => request<{ id: number }>("POST", "/api/v1/users", u),
  updateUser: (id: number, u: UserBody) => request<void>("PUT", `/api/v1/users/${id}`, u),
  resetUserPassword: (id: number, password: string, reset2fa: boolean, mustChange: boolean) =>
    request<void>("POST", `/api/v1/users/${id}/password`, { password, reset_2fa: reset2fa, must_change: mustChange }),
  endUserSessions: (id: number) => request<{ ended: number }>("POST", `/api/v1/users/${id}/sign-out`),
  deleteUser: (id: number) => request<void>("DELETE", `/api/v1/users/${id}`),
  vault: () => request<VaultStatus>("GET", "/api/v1/vault"),
  vaultSetup: (passphrase: string) => request<{ recovery_key: string; secrets_moved: number }>("POST", "/api/v1/vault/setup", { passphrase }),
  vaultUnlock: (passphrase: string) => request<VaultStatus>("POST", "/api/v1/vault/unlock", { passphrase }),
  vaultLock: () => request<VaultStatus>("POST", "/api/v1/vault/lock"),
  vaultPassphrase: (current: string, next: string) => request<void>("POST", "/api/v1/vault/passphrase", { current, new: next }),
  sshKeys: () => request<{ keys: SSHKey[]; vault: VaultStatus }>("GET", "/api/v1/ssh-keys"),
  parseSSHKeys: (b: { files: { name: string; content: string }[]; text: string; ssh_config: string }) =>
    request<KeyCandidate[]>("POST", "/api/v1/ssh-keys/parse", b),
  importSSHKeys: (keys: { line: string; name: string; source: string }[]) =>
    request<{ name: string; id?: number; error?: string }[]>("POST", "/api/v1/ssh-keys", { keys }),
  renameSSHKey: (id: number, name: string) => request<void>("PUT", `/api/v1/ssh-keys/${id}`, { name }),
  deleteSSHKey: (id: number) => request<void>("DELETE", `/api/v1/ssh-keys/${id}`),
  sshKeyPublic: (id: number) => request<{ line: string }>("GET", `/api/v1/ssh-keys/${id}/public`),
  deploySSHKey: (id: number, t: KeyTarget) => request<BulkResult[]>("POST", `/api/v1/ssh-keys/${id}/deploy`, t),
  undeploySSHKey: (id: number, t: KeyTarget) => request<BulkResult[]>("POST", `/api/v1/ssh-keys/${id}/undeploy`, t),
  scanSSHKeys: () => request<ScannedServer[]>("POST", "/api/v1/ssh-keys/scan"),
  permissions: () => request<{ permissions: Permission[]; builtin: Record<string, string[]> }>("GET", "/api/v1/permissions"),
  roles: () => request<RoleInfo[]>("GET", "/api/v1/roles"),
  saveRole: (r: Pick<RoleInfo, "name" | "description" | "permissions">, id?: number) =>
    request<RoleInfo>(id ? "PUT" : "POST", id ? `/api/v1/roles/${id}` : "/api/v1/roles", r),
  deleteRole: (id: number) => request<void>("DELETE", `/api/v1/roles/${id}`),
  userGroups: () => request<UserGroup[]>("GET", "/api/v1/user-groups"),
  saveUserGroup: (g: Omit<UserGroup, "id">, id?: number) =>
    request<UserGroup>(id ? "PUT" : "POST", id ? `/api/v1/user-groups/${id}` : "/api/v1/user-groups", g),
  deleteUserGroup: (id: number) => request<void>("DELETE", `/api/v1/user-groups/${id}`),
  serverGroups: () => request<ServerGroup[]>("GET", "/api/v1/server-groups"),
  saveServerGroup: (g: Omit<ServerGroup, "id">, id?: number) =>
    request<ServerGroup>(id ? "PUT" : "POST", id ? `/api/v1/server-groups/${id}` : "/api/v1/server-groups", g),
  deleteServerGroup: (id: number) => request<void>("DELETE", `/api/v1/server-groups/${id}`),
  setServerGroups: (serverId: string, groups: number[]) => request<void>("PUT", `/api/v1/servers/${serverId}/groups`, { groups }),
  grants: () => request<Grant[]>("GET", "/api/v1/grants"),
  createGrant: (g: { user_id?: number; user_group_id?: number; role_id: number; server_group_id?: number | null }) =>
    request<Grant>("POST", "/api/v1/grants", g),
  deleteGrant: (id: number) => request<void>("DELETE", `/api/v1/grants/${id}`),
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
  channels: () => request<Channel[]>("GET", "/api/v1/channels"),
  createChannel: (c: ChannelBody) => request<Channel>("POST", "/api/v1/channels", c),
  updateChannel: (id: string, c: ChannelBody) => request<Channel>("PUT", `/api/v1/channels/${id}`, c),
  deleteChannel: (id: string) => request<void>("DELETE", `/api/v1/channels/${id}`),
  testChannel: (id: string) => request<{ result: string }>("POST", `/api/v1/channels/${id}/test`),
  alertRules: () => request<AlertRule[]>("GET", "/api/v1/alert-rules"),
  createAlertRule: (r: AlertRule) => request<AlertRule>("POST", "/api/v1/alert-rules", ruleBody(r)),
  updateAlertRule: (id: string, r: AlertRule) => request<AlertRule>("PUT", `/api/v1/alert-rules/${id}`, ruleBody(r)),
  deleteAlertRule: (id: string) => request<void>("DELETE", `/api/v1/alert-rules/${id}`),
  alertEvents: () => request<AlertEvent[]>("GET", "/api/v1/alerts?limit=100"),
  firingAlerts: () => request<AlertEvent[]>("GET", "/api/v1/alerts?state=firing"),
  agentBuild: () => request<{ version: string; sha256: Record<string, string> }>("GET", "/api/v1/agent-build"),
  updateAgent: (id: string) => request<Job>("POST", `/api/v1/servers/${id}/agent-update`),
  apps: () => request<CatalogApp[]>("GET", "/api/v1/apps"),
  installApp: (id: string, body: Record<string, string | boolean>) =>
    request<{ runs: PlaybookRun[]; credentials: Record<string, string>; notes?: string }>("POST", `/api/v1/servers/${id}/apps`, body),
  updateApp: (id: string, domain: string, app: string) => request<PlaybookRun[]>("POST", `/api/v1/servers/${id}/apps/${domain}/update`, { app }),
  uninstallApp: (id: string, domain: string, db: string, compose = "") =>
    request<PlaybookRun[]>("POST", `/api/v1/servers/${id}/apps/${domain}/uninstall`, { db, compose }),
  pveClusters: () => request<PVECluster[]>("GET", "/api/v1/proxmox"),
  pveProbe: (url: string) => request<{ fingerprint: string; subject: string }>("POST", "/api/v1/proxmox/probe", { url }),
  savePVE: (id: string, c: { name: string; url: string; token_id: string; token_secret: string; fingerprint: string }) =>
    id
      ? request<{ cluster: PVECluster; version: string }>("PUT", `/api/v1/proxmox/${id}`, c)
      : request<{ cluster: PVECluster; version: string }>("POST", "/api/v1/proxmox", c),
  deletePVE: (id: string) => request<void>("DELETE", `/api/v1/proxmox/${id}`),
  pveResources: (id: string) => request<PVEResources>("GET", `/api/v1/proxmox/${id}/resources`),
  pveProvisions: () => request<PVEProvision[]>("GET", "/api/v1/proxmox/provisions"),
  pvePower: (id: string, g: PVEGuest, action: string) => request<void>("POST", `${pveGuestURL(id, g)}/power`, { action }),
  pveSnapshots: (id: string, g: PVEGuest) => request<PVESnapshot[]>("GET", `${pveGuestURL(id, g)}/snapshots`),
  pveSnapshot: (id: string, g: PVEGuest, s: { name: string; description: string; vmstate: boolean }) =>
    request<void>("POST", `${pveGuestURL(id, g)}/snapshots`, s),
  pveDeleteSnapshot: (id: string, g: PVEGuest, name: string) => request<void>("DELETE", `${pveGuestURL(id, g)}/snapshots/${name}`),
  pveRollback: (id: string, g: PVEGuest, name: string) => request<void>("POST", `${pveGuestURL(id, g)}/snapshots/${name}/rollback`),
  pveDeleteGuest: (id: string, g: PVEGuest, confirm: string) => request<void>("DELETE", pveGuestURL(id, g), { confirm }),
  pveProvision: (
    id: string,
    b: {
      template_vmid: number;
      node: string;
      target: string;
      name: string;
      cores: number;
      memory_mb: number;
      disk_grow_gb: number;
      full_clone: boolean;
      ip: string;
      gateway: string;
      enroll: boolean;
    },
  ) => request<PVEProvision>("POST", `/api/v1/proxmox/${id}/provision`, b),
  tofu: () => request<TofuOverview>("GET", "/api/v1/tofu"),
  tofuSetup: () => request<void>("POST", "/api/v1/tofu/setup"),
  saveTofuWorkspace: (
    id: string,
    w: Omit<TofuWorkspace, "id" | "created_at" | "updated_at"> & { vars: (TofuVar & { keep: boolean })[] },
  ) => (id ? request<TofuWorkspaceView>("PUT", `/api/v1/tofu/workspaces/${id}`, w) : request<TofuWorkspaceView>("POST", "/api/v1/tofu/workspaces", w)),
  deleteTofuWorkspace: (id: string) => request<void>("DELETE", `/api/v1/tofu/workspaces/${id}`),
  tofuUnlock: (id: string) => request<void>("POST", `/api/v1/tofu/workspaces/${id}/unlock`),
  tofuPlan: (id: string, destroy: boolean) => request<TofuRun>("POST", `/api/v1/tofu/workspaces/${id}/plan`, { destroy }),
  tofuApply: (planRunId: string, confirm: string) => request<TofuRun>("POST", `/api/v1/tofu/runs/${planRunId}/apply`, { confirm }),
  tofuRuns: () => request<TofuRun[]>("GET", "/api/v1/tofu/runs"),
  tofuRun: (id: string, offset: number) =>
    request<{ run: TofuRun; output: string; total: number }>("GET", `/api/v1/tofu/runs/${id}?offset=${offset}`),
  cancelTofuRun: (id: string) => request<void>("POST", `/api/v1/tofu/runs/${id}/cancel`),
  ansible: () => request<AnsibleOverview>("GET", "/api/v1/ansible"),
  ansibleSetup: () => request<void>("POST", "/api/v1/ansible/setup"),
  ansiblePrepare: (id: string, address: string) =>
    request<Job>("POST", `/api/v1/servers/${id}/ansible/prepare`, address ? { address } : {}),
  ansibleRevoke: (id: string) => request<Job>("POST", `/api/v1/servers/${id}/ansible/revoke`),
  ansiblePlaybooks: () => request<AnsiblePlaybook[]>("GET", "/api/v1/ansible/playbooks"),
  saveAnsiblePlaybook: (p: Partial<AnsiblePlaybook>) => {
    const body = {
      name: p.name ?? "",
      description: p.description ?? "",
      source: p.source ?? "inline",
      content: p.content ?? "",
      git_url: p.git_url ?? "",
      git_ref: p.git_ref ?? "",
      git_path: p.git_path ?? "",
    };
    return p.id
      ? request<AnsiblePlaybook>("PUT", `/api/v1/ansible/playbooks/${p.id}`, body)
      : request<AnsiblePlaybook>("POST", "/api/v1/ansible/playbooks", body);
  },
  deleteAnsiblePlaybook: (id: string) => request<void>("DELETE", `/api/v1/ansible/playbooks/${id}`),
  runAnsiblePlaybook: (id: string, b: { server_ids: string[]; check: boolean; extra_vars: string }) =>
    request<AnsibleRun>("POST", `/api/v1/ansible/playbooks/${id}/run`, b),
  ansibleRuns: () => request<AnsibleRun[]>("GET", "/api/v1/ansible/runs"),
  ansibleRun: (id: string, offset: number) =>
    request<{ run: AnsibleRun; output: string; total: number }>("GET", `/api/v1/ansible/runs/${id}?offset=${offset}`),
  cancelAnsibleRun: (id: string) => request<void>("POST", `/api/v1/ansible/runs/${id}/cancel`),
  crowdsec: () => request<CrowdSecOverview>("GET", "/api/v1/crowdsec"),
  crowdsecBan: (b: { type: string; value: string; duration: string; reason: string }) =>
    request<void>("POST", "/api/v1/crowdsec/decisions", b),
  crowdsecUnban: (id: number) => request<void>("DELETE", `/api/v1/crowdsec/decisions/${id}`),
  crowdsecAllow: (action: "add" | "remove", value: string) => request<void>("POST", "/api/v1/crowdsec/allowlist", { action, value }),
  crowdsecEnroll: (key: string, name: string) => request<{ result: string }>("POST", "/api/v1/crowdsec/enroll", { key, name }),
  crowdsecInstall: (id: string, trustLan: boolean) =>
    request<Job>("POST", `/api/v1/servers/${id}/crowdsec/install${trustLan ? "" : "?trust_lan=off"}`),
  crowdsecRemove: (id: string) => request<Job>("POST", `/api/v1/servers/${id}/crowdsec/remove`),
  createJoinToken: (name: string, playbookId = "") =>
    request<JoinToken>("POST", "/api/v1/join-tokens", { name, playbook_id: playbookId }),
  baseline: () => request<{ playbook_id: string }>("GET", "/api/v1/settings/baseline"),
  setBaseline: (playbookId: string) => request<{ playbook_id: string }>("PUT", "/api/v1/settings/baseline", { playbook_id: playbookId }),
};

function pveGuestURL(id: string, g: PVEGuest) {
  return `/api/v1/proxmox/${id}/guests/${encodeURIComponent(g.node)}/${g.type}/${g.vmid}`;
}

// The API rejects unknown fields, so send exactly what it accepts.
function pickSchedule(s: ScheduleBody) {
  const { name, cron, kind, op, params, playbook_id, server_ids, all_servers, server_group_id, enabled } = s;
  return { name, cron, kind, op, params, playbook_id, server_ids, all_servers, server_group_id, enabled };
}
