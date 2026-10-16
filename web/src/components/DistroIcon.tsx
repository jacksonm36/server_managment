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
  siGit,
  siPterodactyl,
  siRoundcube,
  siDovecot,
  type SimpleIcon,
} from "simple-icons";
import { Archive, Container, Globe, HardDrive, Mail, Network, Router, ServerCog, Shield, Terminal, Users, type LucideIcon } from "lucide-react";

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
  raspbian: siDebian,
  gentoo: siGentoo,
  nixos: siNixos,
  openwrt: siOpenwrt,
  linux: siLinux,
};

// Software a guest runs. Each entry is a key (the panel's outside
// detection names these), a pattern for the guest's name and tags, and a
// logo: a Simple Icon, or a generic icon for software without one.
type AppDef = { key: string; re: RegExp; label: string; icon?: SimpleIcon; Lucide?: LucideIcon };
const apps: AppDef[] = [
  { key: "truenas", re: /truenas/, icon: siTruenas, label: "TrueNAS" },
  { key: "pfsense", re: /pfsense/, icon: siPfsense, label: "pfSense" },
  { key: "opnsense", re: /opnsense/, icon: siOpnsense, label: "OPNsense" },
  { key: "unifi", re: /unifi|ubiquiti/, icon: siUbiquiti, label: "UniFi" },
  { key: "mikrotik", re: /mikrotik|routeros|\bchr\b/, icon: siMikrotik, label: "MikroTik" },
  { key: "proxmox", re: /proxmox|\bpbs\d*\b|\bpve\d*\b|proxui/, icon: siProxmox, label: "Proxmox" },
  { key: "nextcloud", re: /nextcloud/, icon: siNextcloud, label: "Nextcloud" },
  { key: "owncloud", re: /owncloud/, icon: siOwncloud, label: "ownCloud" },
  { key: "authentik", re: /authentik/, icon: siAuthentik, label: "authentik" },
  { key: "keycloak", re: /keycloak/, icon: siKeycloak, label: "Keycloak" },
  { key: "openbao", re: /openbao/, icon: siOpenbao, label: "OpenBao" },
  { key: "vault", re: /\bvault\b/, Lucide: Shield, label: "Vault" },
  { key: "vaultwarden", re: /vaultwarden/, icon: siVaultwarden, label: "Vaultwarden" },
  { key: "bitwarden", re: /bitwarden/, icon: siBitwarden, label: "Bitwarden" },
  { key: "forgejo", re: /forgejo/, icon: siForgejo, label: "Forgejo" },
  { key: "gitea", re: /gitea/, icon: siGitea, label: "Gitea" },
  { key: "gitlab", re: /gitlab/, icon: siGitlab, label: "GitLab" },
  { key: "git", re: /^git$|\bgit\b/, icon: siGit, label: "Git" },
  { key: "jenkins", re: /jenkins/, icon: siJenkins, label: "Jenkins" },
  { key: "semaphore", re: /semaphore/, icon: siAnsible, label: "Semaphore" },
  { key: "ansible", re: /ansible|\bawx\b/, icon: siAnsible, label: "Ansible" },
  { key: "pterodactyl", re: /pterodactyl|jexactyl|pelican/, icon: siPterodactyl, label: "Pterodactyl" },
  { key: "portainer", re: /portainer/, icon: siPortainer, label: "Portainer" },
  { key: "k3s", re: /k3s/, icon: siK3s, label: "K3s" },
  { key: "kubernetes", re: /k8s|kube/, icon: siKubernetes, label: "Kubernetes" },
  { key: "rancher", re: /rancher/, icon: siRancher, label: "Rancher" },
  { key: "docker", re: /docker/, icon: siDocker, label: "Docker" },
  { key: "homeassistant", re: /home-?assistant|\bhass\b|\bha-?os\b/, icon: siHomeassistant, label: "Home Assistant" },
  { key: "nodered", re: /node-?red/, icon: siNodered, label: "Node-RED" },
  { key: "jellyfin", re: /jellyfin/, icon: siJellyfin, label: "Jellyfin" },
  { key: "plex", re: /\bplex/, icon: siPlex, label: "Plex" },
  { key: "immich", re: /immich/, icon: siImmich, label: "Immich" },
  { key: "paperless", re: /paperless/, icon: siPaperlessngx, label: "Paperless-ngx" },
  { key: "syncthing", re: /syncthing/, icon: siSyncthing, label: "Syncthing" },
  { key: "deluge", re: /deluge/, icon: siDeluge, label: "Deluge" },
  { key: "qbittorrent", re: /qbittorrent/, icon: siQbittorrent, label: "qBittorrent" },
  { key: "transmission", re: /transmission/, icon: siTransmission, label: "Transmission" },
  { key: "pihole", re: /pi-?hole/, icon: siPihole, label: "Pi-hole" },
  { key: "adguard", re: /adguard/, icon: siAdguard, label: "AdGuard" },
  { key: "technitium", re: /technitium|\bdns\d*\b|bind9|unbound/, Lucide: Globe, label: "DNS server" },
  { key: "pangolin", re: /pangolin/, Lucide: Network, label: "Pangolin" },
  { key: "netboot", re: /netboot|\bpxe\b/, Lucide: Network, label: "netboot.xyz" },
  { key: "iventoy", re: /iventoy|ventoy/, Lucide: HardDrive, label: "iVentoy" },
  { key: "openarchiver", re: /open-?archiver|mailarch/, Lucide: Archive, label: "Open Archiver" },
  { key: "termix", re: /termix|\bssh\b/, Lucide: Terminal, label: "SSH / Termix" },
  { key: "rustdesk", re: /rustdesk|betterdesk/, icon: siRustdesk, label: "RustDesk" },
  { key: "traefik", re: /traefik/, icon: siTraefikproxy, label: "Traefik" },
  { key: "nginxproxymanager", re: /nginx-?proxy|\bnpm\b/, icon: siNginx, label: "Nginx Proxy Manager" },
  { key: "caddy", re: /caddy/, icon: siCaddy, label: "Caddy" },
  { key: "tailscale", re: /tailscale|headscale/, icon: siTailscale, label: "Tailscale" },
  { key: "wireguard", re: /wireguard|wg-?easy/, icon: siWireguard, label: "WireGuard" },
  { key: "openvpn", re: /openvpn/, icon: siOpenvpn, label: "OpenVPN" },
  { key: "guacamole", re: /guacamole/, icon: siApacheguacamole, label: "Guacamole" },
  { key: "jitsi", re: /jitsi/, icon: siJitsi, label: "Jitsi" },
  { key: "matrix", re: /matrix|synapse/, icon: siMatrix, label: "Matrix" },
  { key: "mattermost", re: /mattermost/, icon: siMattermost, label: "Mattermost" },
  { key: "mailcow", re: /mailcow/, Lucide: Mail, label: "mailcow" },
  { key: "roundcube", re: /roundcube/, icon: siRoundcube, label: "Roundcube" },
  { key: "dovecot", re: /dovecot/, icon: siDovecot, label: "Dovecot" },
  { key: "mail", re: /mail|smtp|imap|postfix|exchange/, Lucide: Mail, label: "Mail server" },
  { key: "domaincontroller", re: /\bdc\d*\b|-ad\b|\bad-|samba|domain/, Lucide: Users, label: "Domain controller" },
  { key: "unifi-controller", re: /controller/, Lucide: Router, label: "Controller" },
  { key: "grafana", re: /grafana/, icon: siGrafana, label: "Grafana" },
  { key: "prometheus", re: /prometheus/, icon: siPrometheus, label: "Prometheus" },
  { key: "influxdb", re: /influx/, icon: siInfluxdb, label: "InfluxDB" },
  { key: "uptimekuma", re: /uptime-?kuma/, icon: siUptimekuma, label: "Uptime Kuma" },
  { key: "netdata", re: /netdata/, icon: siNetdata, label: "Netdata" },
  { key: "checkmk", re: /checkmk/, icon: siCheckmk, label: "Checkmk" },
  { key: "icinga", re: /icinga/, icon: siIcinga, label: "Icinga" },
  { key: "graylog", re: /graylog/, icon: siGraylog, label: "Graylog" },
  { key: "opensearch", re: /opensearch/, icon: siOpensearch, label: "OpenSearch" },
  { key: "elasticsearch", re: /elastic/, icon: siElasticsearch, label: "Elasticsearch" },
  { key: "kibana", re: /kibana/, icon: siKibana, label: "Kibana" },
  { key: "minio", re: /minio/, icon: siMinio, label: "MinIO" },
  { key: "ollama", re: /ollama/, icon: siOllama, label: "Ollama" },
  { key: "openmediavault", re: /openmediavault|\bomv\b/, icon: siOpenmediavault, label: "openmediavault" },
  { key: "unraid", re: /unraid/, icon: siUnraid, label: "Unraid" },
  { key: "synology", re: /synology|xpenology/, icon: siSynology, label: "Synology" },
  { key: "wordpress", re: /wordpress/, icon: siWordpress, label: "WordPress" },
  { key: "mariadb", re: /mariadb/, icon: siMariadb, label: "MariaDB" },
  { key: "mysql", re: /mysql/, icon: siMysql, label: "MySQL" },
  { key: "postgres", re: /postgres/, icon: siPostgresql, label: "PostgreSQL" },
  { key: "mongodb", re: /mongo/, icon: siMongodb, label: "MongoDB" },
  { key: "redis", re: /redis/, icon: siRedis, label: "Redis" },
  { key: "rabbitmq", re: /rabbitmq/, icon: siRabbitmq, label: "RabbitMQ" },
  { key: "nodejs", re: /nodejs|node-?js/, icon: siNodedotjs, label: "Node.js" },
  { key: "iis", re: /\biis\b/, Lucide: ServerCog, label: "IIS" },
  { key: "nginx", re: /nginx|webhost|\bweb\d*\b/, icon: siNginx, label: "Web server" },
  { key: "apache", re: /apache|lamp/, icon: siApache, label: "Apache" },
  { key: "containers", re: /\bcontainers?\b/, Lucide: Container, label: "Containers" },
];
const appByKey = new Map(apps.map((a) => [a.key, a]));

export type AppMatch = { icon?: SimpleIcon; Lucide?: LucideIcon; label: string };

// appIcon is the software a guest runs: what the panel detected from the
// outside (key), else a guess from its name and tags.
export function appIcon(detected: string | undefined, ...hints: string[]): AppMatch | null {
  const d = detected ? appByKey.get(detected) : undefined;
  if (d) return d;
  const h = hints.join(" ").toLowerCase();
  return apps.find((a) => a.re.test(h)) ?? null;
}

// LogoMark is a bare logo for tight rows (the compute tree): a Simple Icon,
// the Windows mark, or nothing.
export function LogoMark({
  icon,
  Lucide,
  windows,
  title,
  className = "size-3.5",
}: {
  icon?: SimpleIcon | null;
  Lucide?: LucideIcon;
  windows?: boolean;
  title?: string;
  className?: string;
}) {
  if (Lucide)
    return (
      <span title={title} className="inline-flex shrink-0 text-muted-foreground">
        <Lucide className={className} aria-label={title} />
      </span>
    );
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
