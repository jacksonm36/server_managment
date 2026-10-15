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
  tenant_id: number | null;
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
  tenant_id?: number | null;
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
  tenant: { id: number; name: string } | null;
};

export type Tenant = {
  id: number;
  name: string;
  slug: string;
  description: string;
  server_group_id: number | null;
  max_guests: number;
  max_cores: number;
  max_memory_mb: number;
  max_disk_gb: number;
  users: number;
  created_at: string;
  clusters: TenantCluster[];
};
export type TenantBody = Pick<Tenant, "name" | "description" | "server_group_id" | "max_guests" | "max_cores" | "max_memory_mb" | "max_disk_gb"> & { slug?: string };
export type TenantCluster = {
  tenant_id: number;
  cluster_id: string;
  pool: string;
  storages: string[];
  bridges: string[];
  templates: number[];
  pve_user: string;
  token_id: string;
  set_up_at: string | null;
};
export type TenantClusterBody = { pool: string; storages: string[]; bridges: string[]; templates: number[]; rotate?: boolean };
export type TenantOptions = { storages: string[]; bridges: string[]; templates: { vmid: number; name: string; node: string }[]; pools: string[] };
export type TenantUsage = { guests: number; cores: number; memory_mb: number; disk_gb: number };

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
  login?: "email" | "password";
  wizard?: boolean;
  admin_path: string;
  homepage: string;
  notes?: string;
  updatable: boolean;
  kind: "php" | "compose";
  needs_docker: boolean;
  min_ram_mb?: number;
  protect: boolean;
};

export type CatalogSource = {
  id: string;
  name: string;
  category: string;
  homepage: string;
  github?: string;
  url?: string;
  pinned?: { version?: string; url?: string; sha256?: string; checked_at: string; error?: string };
};
export type CatalogStatus = { running: boolean; done: number; total: number; last_run: string | null; sources: CatalogSource[] };

// A tenant's user gets only id, name and pool.
export type PVECluster = { id: string; name: string; url?: string; token_id?: string; fingerprint?: string; pool?: string; created_at?: string; updated_at?: string };
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
  pool?: string;
  storage?: string;
};
export type PVEGuest = PVEResource & { server_id?: string; server_name?: string; tenant?: string };
export type PVETenantInfo = { name: string; pool: string; storages: string[]; bridges: string[]; usage: TenantUsage; quota: TenantUsage };
export type PVEResources = {
  cluster: PVECluster;
  version?: string;
  error?: string;
  nodes?: PVEResource[];
  guests?: PVEGuest[];
  storage?: PVEResource[];
  tenant?: PVETenantInfo;
};
export type PVEGuestDetail = {
  node: string;
  type: "qemu" | "lxc";
  vmid: number;
  status: Record<string, unknown> & { status: string; name?: string; cpu?: number; cpus?: number; mem?: number; maxmem?: number; disk?: number; maxdisk?: number; uptime?: number; netin?: number; netout?: number; qmpstatus?: string; lock?: string };
  config: Record<string, string | number>;
  ips: string[] | null;
};
export type PVEHardwareOptions = { storages: string[]; bridges: string[]; isos: string[] };
export type PVEHardwareReq = Partial<{
  cores: number;
  sockets: number;
  cpu: string;
  cpulimit: number;
  memory: number;
  balloon: number;
  swap: number;
  name: string;
  description: string;
  tags: string[];
  onboot: boolean;
  protection: boolean;
  agent: boolean;
  boot: string[];
}>;
export type PVENICReq = { model?: string; bridge: string; vlan?: number; firewall?: boolean; rate?: number; mac?: string; name?: string; ip?: string; gw?: string; link_down?: boolean };
export type PVETimeframe = "hour" | "day" | "week" | "month" | "year";
export type PVERRDPoint = { time: number; cpu?: number; maxcpu?: number; mem?: number; maxmem?: number; netin?: number; netout?: number; diskread?: number; diskwrite?: number };
export type PVETask = { upid: string; type: string; user: string; status?: string; starttime: number; endtime?: number };
export type PVEBackup = { volid: string; storage: string; size: number; ctime: number; format?: string; notes?: string; protected?: number };
export type PVEFirewallRule = {
  pos: number;
  type: "in" | "out";
  action: "ACCEPT" | "DROP" | "REJECT";
  enable: number;
  proto?: string;
  source?: string;
  dest?: string;
  dport?: string;
  sport?: string;
  iface?: string;
  comment?: string;
};
export type PVEFirewall = { enable: number; policy_in: string; policy_out: string; rules: PVEFirewallRule[] };
export type PVECloudInit = { ciuser: string; cipassword: string; sshkeys: string; nameserver: string; searchdomain: string; ipconfig: Record<string, string> };
export type PVEFlavor = { id: number; name: string; cores: number; memory_mb: number; disk_gb: number; description: string };
export type PVEDeployReq = {
  image: string;
  node: string;
  name: string;
  flavor_id?: number;
  cores?: number;
  memory_mb?: number;
  disk_gb?: number;
  image_storage: string;
  disk_storage: string;
  bridge: string;
  ip: string;
  gateway: string;
  enroll: boolean;
};
export type PVEImage = { id: string; name: string; vendor: string; user: string; url: string; checksums?: string };
export type PVETemplateTargets = { nodes: string[]; image_storages: string[]; file_storages: string[]; disk_storages: string[]; bridges: string[] };
export type PVETemplateReq = {
  image: string;
  node: string;
  name: string;
  image_storage: string;
  disk_storage: string;
  bridge: string;
  cores: number;
  memory_mb: number;
  disk_gb: number;
};
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

const pveNodeURL = (id: string, node: string) => `/api/v1/proxmox/${id}/nodes/${encodeURIComponent(node)}`;

// Storage page (platform admins).
export type PVEStorageConfig = {
  storage: string;
  type: string;
  content?: string;
  nodes?: string;
  disable?: number;
  shared?: number;
  path?: string;
  server?: string;
  export?: string;
  vgname?: string;
  thinpool?: string;
  pool?: string;
};
export type PVENodeStorage = { storage: string; type: string; content: string; active: number; enabled: number; shared?: number; total?: number; used?: number; avail?: number };
export type PVEStorageOverview = { config: PVEStorageConfig[]; nodes: Record<string, PVENodeStorage[]>; errors: string[] | null };
export type PVEStorageReq = {
  storage: string;
  type: "dir" | "nfs" | "lvmthin" | "zfspool";
  content: string[];
  nodes?: string[];
  disable?: boolean;
  path?: string;
  server?: string;
  export?: string;
  vgname?: string;
  thinpool?: string;
  pool?: string;
};
export type PVEVolume = { volid: string; content: string; format?: string; size?: number; ctime?: number; vmid?: number; notes?: string };
export type PVEDisk = {
  devpath: string;
  model?: string;
  serial?: string;
  size: number;
  type?: string;
  health?: string;
  wearout?: number | string;
  used?: string;
  mounted?: number | boolean;
  rpm?: number;
};
export type PVESMART = {
  health: string;
  type: string;
  text?: string;
  attributes?: { id: number | string; name: string; value: number | string; worst: number | string; threshold: number | string; raw: string; fail?: string }[];
};
export type PVEZPool = { name: string; health: string; size: number; alloc: number; free: number; frag?: number; dedup?: number };
export type PVEZNode = { name: string; state?: string; read?: number; write?: number; cksum?: number; msg?: string; children?: PVEZNode[] };
export type PVEZDetail = { name: string; state: string; status?: string; action?: string; scan?: string; errors?: string; children?: PVEZNode[] };

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
  activeJobs: () => request<JobSummary[]>("GET", "/api/v1/jobs?active=1"),
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
  catalogStatus: () => request<CatalogStatus>("GET", "/api/v1/apps/catalog"),
  refreshCatalog: () => request<{ status: string }>("POST", "/api/v1/apps/catalog/refresh"),
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
  pveImages: () => request<PVEImage[]>("GET", "/api/v1/proxmox-images"),
  pveTemplateTargets: (id: string, node: string) =>
    request<PVETemplateTargets>("GET", `/api/v1/proxmox/${id}/template-targets?node=${encodeURIComponent(node)}`),
  pveTemplateCreate: (id: string, b: PVETemplateReq) => request<PVEProvision>("POST", `/api/v1/proxmox/${id}/templates`, b),
  pveProvisions: () => request<PVEProvision[]>("GET", "/api/v1/proxmox/provisions"),
  pvePower: (id: string, g: PVEGuestRef, action: string) => request<void>("POST", `${pveGuestURL(id, g)}/power`, { action }),
  pveGuest: (id: string, g: PVEGuestRef) => request<PVEGuestDetail>("GET", pveGuestURL(id, g)),
  pveGuestRRD: (id: string, g: PVEGuestRef, tf: PVETimeframe) => request<PVERRDPoint[]>("GET", `${pveGuestURL(id, g)}/rrd?timeframe=${tf}`),
  pveGuestTasks: (id: string, g: PVEGuestRef) => request<PVETask[]>("GET", `${pveGuestURL(id, g)}/tasks`),
  pveConsole: (id: string, g: PVEGuestRef, mode: "auto" | "vga" | "serial") =>
    request<{ sid: string; mode: "vga" | "term"; password?: string; serial?: string }>("POST", `${pveGuestURL(id, g)}/console`, { mode }),
  pveBackups: (id: string, g: PVEGuestRef) => request<{ storages: string[]; backups: PVEBackup[] }>("GET", `${pveGuestURL(id, g)}/backups`),
  pveBackup: (id: string, g: PVEGuestRef, b: { storage: string; mode: string; notes: string }) =>
    request<{ upid: string }>("POST", `${pveGuestURL(id, g)}/backups`, b),
  pveDeleteBackup: (id: string, g: PVEGuestRef, volid: string) => request<void>("DELETE", `${pveGuestURL(id, g)}/backups?volid=${encodeURIComponent(volid)}`),
  pveRestore: (id: string, g: PVEGuestRef, volid: string, confirm: string) =>
    request<{ upid: string }>("POST", `${pveGuestURL(id, g)}/restore`, { volid, confirm }),
  pveCloudInit: (id: string, g: PVEGuestRef, c: PVECloudInit) => request<void>("PUT", `${pveGuestURL(id, g)}/cloudinit`, c),
  pveFirewall: (id: string, g: PVEGuestRef) => request<PVEFirewall>("GET", `${pveGuestURL(id, g)}/firewall`),
  pveFirewallOptions: (id: string, g: PVEGuestRef, o: { enable: boolean; policy_in: string; policy_out: string }) =>
    request<void>("PUT", `${pveGuestURL(id, g)}/firewall/options`, o),
  pveFirewallAdd: (id: string, g: PVEGuestRef, r: Omit<PVEFirewallRule, "pos">) => request<void>("POST", `${pveGuestURL(id, g)}/firewall/rules`, r),
  pveFirewallDelete: (id: string, g: PVEGuestRef, pos: number) => request<void>("DELETE", `${pveGuestURL(id, g)}/firewall/rules/${pos}`),
  pveTaskLog: (id: string, g: PVEGuestRef, upid: string) =>
    request<{ log: string }>("GET", `${pveGuestURL(id, g)}/tasks/log?upid=${encodeURIComponent(upid)}`),
  pveStorage: (id: string) => request<PVEStorageOverview>("GET", `/api/v1/proxmox/${id}/storage`),
  pveStorageCreate: (id: string, b: PVEStorageReq) => request<void>("POST", `/api/v1/proxmox/${id}/storage`, b),
  pveStorageUpdate: (id: string, storage: string, b: Partial<PVEStorageReq>) =>
    request<void>("PUT", `/api/v1/proxmox/${id}/storage/${encodeURIComponent(storage)}`, b),
  pveStorageDelete: (id: string, storage: string, confirm: string) =>
    request<void>("DELETE", `/api/v1/proxmox/${id}/storage/${encodeURIComponent(storage)}?confirm=${encodeURIComponent(confirm)}`),
  pveStorageContent: (id: string, node: string, storage: string) =>
    request<PVEVolume[]>("GET", `${pveNodeURL(id, node)}/storage/${encodeURIComponent(storage)}/content`),
  pveVolumeDelete: (id: string, node: string, storage: string, volume: string, confirm: string) =>
    request<void>(
      "DELETE",
      `${pveNodeURL(id, node)}/storage/${encodeURIComponent(storage)}/content?volume=${encodeURIComponent(volume)}&confirm=${encodeURIComponent(confirm)}`,
    ),
  pveDownloadURL: (id: string, node: string, storage: string, b: { url: string; content: "iso" | "vztmpl"; filename?: string; checksum?: string; checksum_algorithm?: string }) =>
    request<{ upid: string }>("POST", `${pveNodeURL(id, node)}/storage/${encodeURIComponent(storage)}/download`, b),
  pveNodeTask: (id: string, node: string, upid: string) =>
    request<{ status: string; exitstatus: string; log: string[] }>("GET", `${pveNodeURL(id, node)}/task?upid=${encodeURIComponent(upid)}`),
  pveDisks: (id: string, node: string) => request<PVEDisk[]>("GET", `${pveNodeURL(id, node)}/disks`),
  pveDiskSMART: (id: string, node: string, disk: string) => request<PVESMART>("GET", `${pveNodeURL(id, node)}/disks/smart?disk=${encodeURIComponent(disk)}`),
  pveDiskWipe: (id: string, node: string, disk: string, confirm: string) => request<void>("POST", `${pveNodeURL(id, node)}/disks/wipe`, { disk, confirm }),
  pveZFS: (id: string, node: string) => request<PVEZPool[]>("GET", `${pveNodeURL(id, node)}/zfs`),
  pveZFSDetail: (id: string, node: string, pool: string) => request<PVEZDetail>("GET", `${pveNodeURL(id, node)}/zfs/${encodeURIComponent(pool)}`),
  pveZFSCreate: (id: string, node: string, b: { name: string; devices: string[]; raidlevel: string; ashift: number; compression: string; add_storage: boolean }) =>
    request<{ upid: string }>("POST", `${pveNodeURL(id, node)}/zfs`, b),
  pveZFSDestroy: (id: string, node: string, pool: string, b: { confirm: string; cleanup_config: boolean; cleanup_disks: boolean }) =>
    request<{ upid: string }>("POST", `${pveNodeURL(id, node)}/zfs/${encodeURIComponent(pool)}/destroy`, b),
  pveHardwareOptions: (id: string, g: PVEGuestRef) => request<PVEHardwareOptions>("GET", `${pveGuestURL(id, g)}/hardware`),
  pveHardware: (id: string, g: PVEGuestRef, h: PVEHardwareReq) => request<void>("PUT", `${pveGuestURL(id, g)}/hardware`, h),
  pveDiskAdd: (id: string, g: PVEGuestRef, d: { bus?: string; storage: string; size_gb: number; path?: string }) =>
    request<void>("POST", `${pveGuestURL(id, g)}/disks`, d),
  pveDiskResize: (id: string, g: PVEGuestRef, disk: string, grow_gb: number) => request<void>("POST", `${pveGuestURL(id, g)}/disks/resize`, { disk, grow_gb }),
  pveDiskRemove: (id: string, g: PVEGuestRef, disk: string, destroy: boolean, confirm = "") =>
    request<void>("POST", `${pveGuestURL(id, g)}/disks/remove`, { disk, destroy, confirm }),
  pveNIC: (id: string, g: PVEGuestRef, key: string, n: PVENICReq) => request<void>("PUT", `${pveGuestURL(id, g)}/nics/${key}`, n),
  pveNICDelete: (id: string, g: PVEGuestRef, key: string) => request<void>("DELETE", `${pveGuestURL(id, g)}/nics/${key}`),
  pveCDROM: (id: string, g: PVEGuestRef, iso: string) => request<void>("PUT", `${pveGuestURL(id, g)}/cdrom`, { iso }),
  pveClone: (id: string, g: PVEGuestRef, c: { name: string; full: boolean; target?: string; storage?: string }) =>
    request<{ upid: string }>("POST", `${pveGuestURL(id, g)}/clone`, c),
  pveMigrate: (id: string, g: PVEGuestRef, target: string, online: boolean) => request<{ upid: string }>("POST", `${pveGuestURL(id, g)}/migrate`, { target, online }),
  pveMakeTemplate: (id: string, g: PVEGuestRef, confirm: string) => request<void>("POST", `${pveGuestURL(id, g)}/template`, { confirm }),
  pveSnapshots: (id: string, g: PVEGuestRef) => request<PVESnapshot[]>("GET", `${pveGuestURL(id, g)}/snapshots`),
  pveSnapshot: (id: string, g: PVEGuestRef, s: { name: string; description: string; vmstate: boolean }) =>
    request<void>("POST", `${pveGuestURL(id, g)}/snapshots`, s),
  pveDeleteSnapshot: (id: string, g: PVEGuestRef, name: string) => request<void>("DELETE", `${pveGuestURL(id, g)}/snapshots/${name}`),
  pveRollback: (id: string, g: PVEGuestRef, name: string) => request<void>("POST", `${pveGuestURL(id, g)}/snapshots/${name}/rollback`),
  pveDeleteGuest: (id: string, g: PVEGuestRef, confirm: string) => request<void>("DELETE", pveGuestURL(id, g), { confirm }),
  pveProvision: (
    id: string,
    b: {
      template_vmid: number;
      node: string;
      target: string;
      name: string;
      cores: number;
      memory_mb: number;
      disk_grow_gb?: number;
      disk_gb?: number;
      full_clone: boolean;
      ip: string;
      gateway: string;
      enroll: boolean;
      storage?: string;
      bridge?: string;
    },
  ) => request<PVEProvision>("POST", `/api/v1/proxmox/${id}/provision`, b),
  pveFlavors: () => request<PVEFlavor[]>("GET", "/api/v1/proxmox-flavors"),
  pveFlavorSave: (f: Omit<PVEFlavor, "id"> & { id?: number }) =>
    f.id ? request<PVEFlavor>("PUT", `/api/v1/proxmox-flavors/${f.id}`, f) : request<PVEFlavor>("POST", "/api/v1/proxmox-flavors", f),
  pveFlavorDelete: (id: number) => request<void>("DELETE", `/api/v1/proxmox-flavors/${id}`),
  pveDeploy: (id: string, b: PVEDeployReq) => request<PVEProvision>("POST", `/api/v1/proxmox/${id}/deploy`, b),
  tenants: () => request<Tenant[]>("GET", "/api/v1/tenants"),
  saveTenant: (id: number | null, t: TenantBody) =>
    id ? request<Tenant>("PUT", `/api/v1/tenants/${id}`, t) : request<Tenant>("POST", "/api/v1/tenants", t),
  deleteTenant: (id: number) => request<void>("DELETE", `/api/v1/tenants/${id}`),
  tenantOptions: (cluster: string) => request<TenantOptions>("GET", `/api/v1/tenant-options/${cluster}`),
  setupTenantCluster: (id: number, cluster: string, b: TenantClusterBody) =>
    request<{ cluster: TenantCluster; steps: string[] }>("PUT", `/api/v1/tenants/${id}/clusters/${cluster}`, b),
  removeTenantCluster: (id: number, cluster: string) => request<void>("DELETE", `/api/v1/tenants/${id}/clusters/${cluster}`),
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

export type PVEGuestRef = Pick<PVEGuest, "node" | "type" | "vmid">;

function pveGuestURL(id: string, g: PVEGuestRef) {
  return `/api/v1/proxmox/${id}/guests/${encodeURIComponent(g.node)}/${g.type}/${g.vmid}`;
}

// The API rejects unknown fields, so send exactly what it accepts.
function pickSchedule(s: ScheduleBody) {
  const { name, cron, kind, op, params, playbook_id, server_ids, all_servers, server_group_id, enabled } = s;
  return { name, cron, kind, op, params, playbook_id, server_ids, all_servers, server_group_id, enabled };
}
