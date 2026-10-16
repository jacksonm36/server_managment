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
