import {
  siAlmalinux,
  siAlpinelinux,
  siApache,
  siArchlinux,
  siBookstack,
  siCentos,
  siDebian,
  siDjango,
  siDocker,
  siDrupal,
  siElasticsearch,
  siFedora,
  siGhost,
  siGitea,
  siGitlab,
  siGrafana,
  siJenkins,
  siJoomla,
  siLimesurvey,
  siLinux,
  siMariadb,
  siMastodon,
  siMatomo,
  siMattermost,
  siMoodle,
  siMongodb,
  siMysql,
  siNextcloud,
  siNginx,
  siNodedotjs,
  siOdoo,
  siOpensuse,
  siOpenvpn,
  siOwncloud,
  siPhp,
  siPhpbb,
  siPostgresql,
  siPrestashop,
  siRabbitmq,
  siRedis,
  siRedmine,
  siRockylinux,
  siRubyonrails,
  siUbuntu,
  siWireguard,
  siWordpress,
  siWoocommerce,
  siAdguard,
  siAnsible,
  siApacheguacamole,
  siAuthentik,
  siBitwarden,
  siCaddy,
  siCheckmk,
  siDeluge,
  siForgejo,
  siFreebsd,
  siGentoo,
  siGraylog,
  siHomeassistant,
  siIcinga,
  siImmich,
  siInfluxdb,
  siJellyfin,
  siJitsi,
  siK3s,
  siKalilinux,
  siKeycloak,
  siKibana,
  siKubernetes,
  siLinuxmint,
  siMatrix,
  siMikrotik,
  siMinio,
  siNetdata,
  siNixos,
  siNodered,
  siOllama,
  siOpenbao,
  siOpenmediavault,
  siOpensearch,
  siOpenwrt,
  siOpnsense,
  siPaperlessngx,
  siPfsense,
  siPihole,
  siPlex,
  siPortainer,
  siPrometheus,
  siProxmox,
  siQbittorrent,
  siRancher,
  siRustdesk,
  siSynology,
  siSyncthing,
  siTailscale,
  siTraefikproxy,
  siTransmission,
  siTruenas,
  siUbiquiti,
  siUnraid,
  siUptimekuma,
  siVaultwarden,
  type SimpleIcon,
} from "simple-icons";

// Logos for the deploy catalog: distributions and the apps inside TurnKey
// appliances (Simple Icons, CC0), and a drawn Windows mark. Anything else
// gets its initials.

const byKey: Record<string, SimpleIcon> = {
  debian: siDebian,
  ubuntu: siUbuntu,
  rocky: siRockylinux,
  alma: siAlmalinux,
  centos: siCentos,
  arch: siArchlinux,
  fedora: siFedora,
  opensuse: siOpensuse,
  alpine: siAlpinelinux,
  linux: siLinux,
  wordpress: siWordpress,
  nextcloud: siNextcloud,
  owncloud: siOwncloud,
  gitlab: siGitlab,
  gitea: siGitea,
  nginx: siNginx,
  node: siNodedotjs,
  odoo: siOdoo,
  moodle: siMoodle,
  mattermost: siMattermost,
  openvpn: siOpenvpn,
  wireguard: siWireguard,
  postgresql: siPostgresql,
  mysql: siMysql,
  mariadb: siMariadb,
  redmine: siRedmine,
  jenkins: siJenkins,
  drupal: siDrupal,
  joomla: siJoomla,
  phpbb: siPhpbb,
  rails: siRubyonrails,
  django: siDjango,
  lamp: siApache,
  apache: siApache,
  php: siPhp,
  mongodb: siMongodb,
  redis: siRedis,
  rabbitmq: siRabbitmq,
  docker: siDocker,
  grafana: siGrafana,
  elasticsearch: siElasticsearch,
  ghost: siGhost,
  mastodon: siMastodon,
  matomo: siMatomo,
  limesurvey: siLimesurvey,
  prestashop: siPrestashop,
  woocommerce: siWoocommerce,
  bookstack: siBookstack,
};

// Distributions and operating systems, by the id Proxmox or the guest agent
// reports (os-release ID, LXC ostype).
const osKeys: Record<string, SimpleIcon> = {
  debian: siDebian,
  ubuntu: siUbuntu,
  rocky: siRockylinux,
  almalinux: siAlmalinux,
  centos: siCentos,
  archlinux: siArchlinux,
  arch: siArchlinux,
  fedora: siFedora,
  opensuse: siOpensuse,
  "opensuse-leap": siOpensuse,
  "opensuse-tumbleweed": siOpensuse,
  alpine: siAlpinelinux,
  freebsd: siFreebsd,
  kali: siKalilinux,
  linuxmint: siLinuxmint,
  gentoo: siGentoo,
  nixos: siNixos,
  openwrt: siOpenwrt,
  linux: siLinux,
};

// Software a guest runs, matched in its name and tags. Order matters: the
// first match wins.
const appKeys: [RegExp, SimpleIcon, string][] = [
  [/truenas/, siTruenas, "TrueNAS"],
  [/pfsense/, siPfsense, "pfSense"],
  [/opnsense/, siOpnsense, "OPNsense"],
  [/unifi|ubiquiti/, siUbiquiti, "UniFi"],
  [/mikrotik|routeros|chr\b/, siMikrotik, "MikroTik"],
  [/nextcloud/, siNextcloud, "Nextcloud"],
  [/owncloud/, siOwncloud, "ownCloud"],
  [/authentik/, siAuthentik, "authentik"],
  [/keycloak/, siKeycloak, "Keycloak"],
  [/openbao/, siOpenbao, "OpenBao"],
  [/vaultwarden/, siVaultwarden, "Vaultwarden"],
  [/bitwarden/, siBitwarden, "Bitwarden"],
  [/forgejo/, siForgejo, "Forgejo"],
  [/gitea/, siGitea, "Gitea"],
  [/gitlab/, siGitlab, "GitLab"],
  [/jenkins/, siJenkins, "Jenkins"],
  [/ansible|semaphore|awx/, siAnsible, "Ansible"],
  [/portainer/, siPortainer, "Portainer"],
  [/k3s/, siK3s, "K3s"],
  [/k8s|kube/, siKubernetes, "Kubernetes"],
  [/rancher/, siRancher, "Rancher"],
  [/docker/, siDocker, "Docker"],
  [/home-?assistant|\bhass\b|\bha-?os\b/, siHomeassistant, "Home Assistant"],
  [/node-?red/, siNodered, "Node-RED"],
  [/jellyfin/, siJellyfin, "Jellyfin"],
  [/plex/, siPlex, "Plex"],
  [/immich/, siImmich, "Immich"],
  [/paperless/, siPaperlessngx, "Paperless-ngx"],
  [/syncthing/, siSyncthing, "Syncthing"],
  [/deluge/, siDeluge, "Deluge"],
  [/qbittorrent/, siQbittorrent, "qBittorrent"],
  [/transmission/, siTransmission, "Transmission"],
  [/pi-?hole/, siPihole, "Pi-hole"],
  [/adguard/, siAdguard, "AdGuard"],
  [/traefik/, siTraefikproxy, "Traefik"],
  [/caddy/, siCaddy, "Caddy"],
  [/nginx|webhost/, siNginx, "nginx"],
  [/apache|lamp/, siApache, "Apache"],
  [/wordpress/, siWordpress, "WordPress"],
  [/tailscale|headscale/, siTailscale, "Tailscale"],
  [/wireguard|wg-?easy/, siWireguard, "WireGuard"],
  [/openvpn/, siOpenvpn, "OpenVPN"],
  [/guacamole/, siApacheguacamole, "Guacamole"],
  [/rustdesk/, siRustdesk, "RustDesk"],
  [/jitsi/, siJitsi, "Jitsi"],
  [/matrix|synapse/, siMatrix, "Matrix"],
  [/mattermost/, siMattermost, "Mattermost"],
  [/grafana/, siGrafana, "Grafana"],
  [/prometheus/, siPrometheus, "Prometheus"],
  [/influx/, siInfluxdb, "InfluxDB"],
  [/uptime-?kuma/, siUptimekuma, "Uptime Kuma"],
  [/netdata/, siNetdata, "Netdata"],
  [/checkmk/, siCheckmk, "Checkmk"],
  [/icinga/, siIcinga, "Icinga"],
  [/graylog/, siGraylog, "Graylog"],
  [/opensearch/, siOpensearch, "OpenSearch"],
  [/elastic/, siElasticsearch, "Elasticsearch"],
  [/kibana/, siKibana, "Kibana"],
  [/minio/, siMinio, "MinIO"],
  [/ollama/, siOllama, "Ollama"],
  [/openmediavault|\bomv\b/, siOpenmediavault, "openmediavault"],
  [/unraid/, siUnraid, "Unraid"],
  [/synology|xpenology/, siSynology, "Synology"],
  [/postgres/, siPostgresql, "PostgreSQL"],
  [/mariadb/, siMariadb, "MariaDB"],
  [/mysql/, siMysql, "MySQL"],
  [/mongo/, siMongodb, "MongoDB"],
  [/redis/, siRedis, "Redis"],
  [/rabbitmq/, siRabbitmq, "RabbitMQ"],
  [/nodejs|node-?js/, siNodedotjs, "Node.js"],
  [/proxmox|\bpbs\b|\bpve\b/, siProxmox, "Proxmox"],
];

// appIcon finds the software a guest runs from its name and tags.
export function appIcon(...hints: string[]): { icon: SimpleIcon; label: string } | null {
  const h = hints.join(" ").toLowerCase();
  for (const [re, icon, label] of appKeys) if (re.test(h)) return { icon, label };
  return null;
}

// LogoMark is a bare logo for tight rows (the compute tree): a Simple Icon,
// the Windows mark, or nothing.
export function LogoMark({ icon, windows, title, className = "size-3.5" }: { icon?: SimpleIcon | null; windows?: boolean; title?: string; className?: string }) {
  if (windows)
    return (
      <span title={title} className="inline-flex shrink-0">
        <WindowsMark className={className} />
      </span>
    );
  if (!icon) return null;
  const d = dark(icon.hex);
  return (
    <svg viewBox="0 0 24 24" className={`${className} shrink-0`} role="img" aria-label={title ?? icon.title}>
      <title>{title ?? icon.title}</title>
      <path d={icon.path} fill={d ? "currentColor" : `#${icon.hex}`} />
    </svg>
  );
}

// osIcon is the logo for an OS id ("debian", "windows", "linux"…).
export function osIcon(os: string): { icon: SimpleIcon | null; windows: boolean } {
  if (os === "windows") return { icon: null, windows: true };
  return { icon: osKeys[os] ?? null, windows: false };
}

// iconKey finds a logo for a vendor, image id or appliance package name.
export function iconKey(...hints: string[]): string {
  const h = hints.join(" ").toLowerCase();
  if (/windows|win(10|11|20\d\d)/.test(h)) return "windows";
  if (/node(js)?\b/.test(h)) return "node";
  if (/rails/.test(h)) return "rails";
  for (const k of Object.keys(byKey)) if (k !== "linux" && h.includes(k)) return k;
  return "";
}

// A dark brand colour (CentOS, AlmaLinux…) would vanish on the dark theme,
// so those logos are drawn in the text colour on a tint of the brand.
function dark(hex: string) {
  const n = parseInt(hex, 16);
  const [r, g, b] = [(n >> 16) & 255, (n >> 8) & 255, n & 255];
  return 0.2126 * r + 0.7152 * g + 0.0722 * b < 70;
}

function WindowsMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" className={className} aria-hidden>
      <path fill="#0078D4" d="M2 2h9.5v9.5H2zM12.5 2H22v9.5h-9.5zM2 12.5h9.5V22H2zM12.5 12.5H22V22h-9.5z" />
    </svg>
  );
}

export function DistroIcon({ k, label, size = "size-10" }: { k: string; label: string; size?: string }) {
  if (k === "windows")
    return (
      <div className={`grid ${size} shrink-0 place-items-center rounded-lg border border-sky-500/30 bg-sky-500/10`}>
        <WindowsMark className="size-1/2" />
      </div>
    );
  const ic = byKey[k];
  if (!ic) {
    const init = label
      .replace(/^TurnKey\s+/i, "")
      .split(/\s+/)
      .map((w) => w[0])
      .join("")
      .slice(0, 2)
      .toUpperCase();
    return <div className={`grid ${size} shrink-0 place-items-center rounded-lg border bg-muted text-sm font-bold text-muted-foreground`}>{init}</div>;
  }
  const d = dark(ic.hex);
  return (
    <div
      className={`grid ${size} shrink-0 place-items-center rounded-lg border`}
      style={{ background: `#${ic.hex}${d ? "40" : "1f"}`, borderColor: `#${ic.hex}${d ? "80" : "4d"}` }}
      title={ic.title}
    >
      <svg viewBox="0 0 24 24" className="size-1/2" aria-hidden>
        <path d={ic.path} fill={d ? "currentColor" : `#${ic.hex}`} />
      </svg>
    </div>
  );
}
