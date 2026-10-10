// Operations that make sense to run across many servers, with the fields the
// UI asks for. Shared by bulk actions, playbooks and schedules.

export type Field = {
  key: string;
  label: string;
  placeholder?: string;
  kind?: "text" | "textarea" | "select" | "bool";
  options?: { value: string; label: string }[];
  optional?: boolean;
};

export type OpDef = { op: string; label: string; group: string; fields: Field[]; danger?: boolean };

export const CATALOG: OpDef[] = [
  { op: "packages.check_updates", label: "Check for updates", group: "Packages", fields: [] },
  {
    op: "packages.upgrade",
    label: "Upgrade packages",
    group: "Packages",
    fields: [
      { key: "dist", label: "Full upgrade", kind: "bool" },
      { key: "autoremove", label: "Autoremove", kind: "bool" },
      { key: "packages", label: "Only these packages", placeholder: "openssl nginx", optional: true },
    ],
  },
  { op: "system.reboot", label: "Reboot", group: "System", fields: [], danger: true },
  { op: "system.info", label: "System info", group: "System", fields: [] },
  {
    op: "ssh_keys.add",
    label: "Deploy SSH key",
    group: "Access",
    fields: [
      { key: "user", label: "User", placeholder: "root" },
      { key: "key", label: "Public key", kind: "textarea", placeholder: "ssh-ed25519 AAAA… you@laptop" },
      { key: "comment", label: "Comment", optional: true },
    ],
  },
  {
    op: "ssh_keys.remove",
    label: "Remove SSH key",
    group: "Access",
    danger: true,
    fields: [
      { key: "user", label: "User", placeholder: "root" },
      { key: "fingerprint", label: "Fingerprint", placeholder: "SHA256:…" },
    ],
  },
  { op: "firewall.install", label: "Install UFW", group: "Security", fields: [] },
  { op: "firewall.enable", label: "Enable firewall (keeps SSH open)", group: "Security", fields: [] },
  {
    op: "firewall.rule_add",
    label: "Add firewall rule",
    group: "Security",
    fields: [
      {
        key: "action",
        label: "Action",
        kind: "select",
        options: ["allow", "limit", "deny", "reject"].map((v) => ({ value: v, label: v })),
      },
      { key: "port", label: "Port", placeholder: "443" },
      { key: "proto", label: "Protocol", kind: "select", options: ["tcp", "udp", "any"].map((v) => ({ value: v, label: v })) },
      { key: "from", label: "Source", placeholder: "any", optional: true },
      { key: "comment", label: "Comment", optional: true },
    ],
  },
  { op: "fail2ban.install", label: "Install Fail2ban", group: "Security", fields: [] },
  { op: "crowdsec.status", label: "CrowdSec status", group: "Security", fields: [] },
  {
    op: "fail2ban.configure",
    label: "Fail2ban settings",
    group: "Security",
    fields: [
      { key: "bantime", label: "Ban time", placeholder: "1h" },
      { key: "findtime", label: "Find time", placeholder: "10m" },
      { key: "maxretry", label: "Max retry", placeholder: "5" },
      { key: "ignoreip", label: "Never ban", placeholder: "10.0.1.0/24", optional: true },
    ],
  },
  {
    op: "web.install",
    label: "Install nginx (+ PHP)",
    group: "Web",
    fields: [{ key: "php", label: "PHP version", placeholder: "8.4", optional: true }],
  },
  { op: "db.install", label: "Install MariaDB", group: "Web", fields: [] },
  { op: "docker.install", label: "Install Docker", group: "Docker", fields: [] },
  { op: "docker.prune", label: "Prune stopped containers & dangling images", group: "Docker", fields: [] },
];

export const opDef = (op: string) => CATALOG.find((d) => d.op === op);

/** Default values for an op's fields (booleans default to false, selects to the first option). */
export function defaults(def: OpDef): Record<string, string> {
  const p: Record<string, string> = {};
  for (const f of def.fields) {
    if (f.kind === "bool") p[f.key] = "false";
    else if (f.kind === "select") p[f.key] = f.options?.[0]?.value ?? "";
    else if (f.key === "user") p[f.key] = "root";
    else p[f.key] = "";
  }
  return p;
}

/** Drops empty optional fields so the agent sees only what was filled in. */
export function clean(def: OpDef, p: Record<string, string>): Record<string, string> {
  const out: Record<string, string> = {};
  for (const f of def.fields) {
    const v = (p[f.key] ?? "").trim();
    if (v === "" && f.optional) continue;
    out[f.key] = v;
  }
  return out;
}

export function missing(def: OpDef, p: Record<string, string>): string | null {
  for (const f of def.fields) {
    if (!f.optional && f.kind !== "bool" && !(p[f.key] ?? "").trim()) return f.label;
  }
  return null;
}
