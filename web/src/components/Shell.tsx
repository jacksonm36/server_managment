import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Link, useLocation } from "wouter";
import {
  Boxes,
  Database,
  Rocket,
  ChevronLeft,
  ChevronsRight,
  History,
  KeyRound,
  LayoutDashboard,
  LogOut,
  Menu,
  PanelLeft,
  Search,
  Server,
  Settings,
  ShieldCheck,
  Users as UsersIcon,
  Workflow,
  X,
} from "lucide-react";
import { api, type Me, type PVEGuest } from "@/lib/api";
import { useLive } from "@/lib/live";
import { pct } from "@/lib/utils";
import type { AccessData } from "@/lib/access";
import { ErrorBoundary } from "@/components/ErrorBoundary";

// The app frame: a sectioned sidebar on the left (collapsible to icons on
// desktop, a drawer on phones) and a top bar with search, fleet usage and
// the account. Until someone toggles it, the sidebar shrinks to icons on
// its own when the window is narrower than WIDE.

type NavItem = { href: string; label: string; icon: ReactNode; show: boolean; badge?: ReactNode };

function navSections(perms: Set<string>): { title?: string; items: NavItem[] }[] {
  const has = (p: string) => perms.has(p);
  return [
    { items: [{ href: "/", label: "Dashboard", icon: <LayoutDashboard />, show: true }] },
    {
      title: "Infrastructure",
      items: [
        { href: "/servers", label: "Servers", icon: <Server />, show: true },
        { href: "/proxmox", label: "Proxmox", icon: <Boxes />, show: has("proxmox.view") },
        { href: "/storage", label: "Storage", icon: <Database />, show: has("proxmox.manage") },
        { href: "/deploy", label: "Deploy", icon: <Rocket />, show: has("proxmox.guests") },
      ],
    },
    {
      title: "Operations",
      items: [
        {
          href: "/automation",
          label: "Automation",
          icon: <Workflow />,
          show: ["automation.view", "ansible.view", "tofu.view"].some(has),
          badge: <FiringDot />,
        },
        { href: "/security", label: "Security", icon: <ShieldCheck />, show: has("crowdsec.view") },
        { href: "/keys", label: "Keys", icon: <KeyRound />, show: has("keys.manage") },
        { href: "/activity", label: "Activity", icon: <History />, show: true },
      ],
    },
    {
      title: "Admin",
      items: [
        { href: "/users", label: "Users", icon: <UsersIcon />, show: has("users.manage") },
        { href: "/account", label: "Settings", icon: <Settings />, show: true },
      ],
    },
  ];
}

const COLLAPSE_KEY = "sm.sidebar.collapsed";
const WIDE = "(min-width: 1280px)";

// useMedia follows a CSS media query.
function useMedia(q: string) {
  const [m, setM] = useState(() => window.matchMedia(q).matches);
  useEffect(() => {
    const mq = window.matchMedia(q);
    const on = () => setM(mq.matches);
    mq.addEventListener("change", on);
    return () => mq.removeEventListener("change", on);
  }, [q]);
  return m;
}

export function Shell({ me, access, onLogout, children }: { me: Me; access: AccessData; onLogout: () => void; children: ReactNode }) {
  // "1" collapsed, "0" open, null = follow the window width.
  const [pref, setPref] = useState<string | null>(() => {
    try {
      return localStorage.getItem(COLLAPSE_KEY);
    } catch {
      return null;
    }
  });
  const wide = useMedia(WIDE);
  const collapsed = pref === null ? !wide : pref === "1";
  const [drawer, setDrawer] = useState(false);
  const [loc] = useLocation();
  useEffect(() => setDrawer(false), [loc]);
  function toggle() {
    const next = !collapsed;
    // Toggling back to what the width would pick returns to automatic.
    const p = next === !wide ? null : next ? "1" : "0";
    setPref(p);
    try {
      if (p === null) localStorage.removeItem(COLLAPSE_KEY);
      else localStorage.setItem(COLLAPSE_KEY, p);
    } catch {
      /* private mode */
    }
  }
  const toggleRef = useRef(toggle);
  useEffect(() => {
    toggleRef.current = toggle;
  });
  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement;
      if (e.key !== "[" || e.ctrlKey || e.metaKey || e.altKey || t.closest("input,textarea,select,[contenteditable=true]")) return;
      toggleRef.current();
    };
    document.addEventListener("keydown", key);
    return () => document.removeEventListener("keydown", key);
  }, []);
  const sections = navSections(access.perms);

  return (
    <div className="min-h-svh">
      {drawer && <div className="fixed inset-0 z-40 bg-black/60 lg:hidden" onClick={() => setDrawer(false)} />}
      <aside
        className={`fixed inset-y-0 left-0 z-50 flex flex-col border-r bg-sidebar transition-[width,transform] duration-200 ${
          collapsed ? "lg:w-16" : "lg:w-60"
        } w-64 ${drawer ? "translate-x-0" : "-translate-x-full lg:translate-x-0"}`}
      >
        <div className="flex h-14 shrink-0 items-center justify-between gap-2 px-4">
          <Link href="/" className="flex min-w-0 items-center gap-2 font-bold tracking-wide uppercase">
            <ChevronsRight className="size-5 shrink-0 text-brand" strokeWidth={3} />
            <span className={collapsed ? "lg:hidden" : ""}>servermgr</span>
          </Link>
          <button
            onClick={toggle}
            className={`hidden size-6 shrink-0 cursor-pointer items-center justify-center rounded-full border text-muted-foreground hover:text-foreground lg:flex ${
              collapsed ? "lg:hidden" : ""
            }`}
            aria-label="Collapse sidebar"
          >
            <ChevronLeft className="size-3.5" />
          </button>
          <button onClick={() => setDrawer(false)} className="cursor-pointer text-muted-foreground lg:hidden" aria-label="Close menu">
            <X className="size-5" />
          </button>
        </div>
        {collapsed && (
          <button
            onClick={toggle}
            className="mx-auto mb-2 hidden size-6 cursor-pointer items-center justify-center rounded-full border text-muted-foreground hover:text-foreground lg:flex"
            aria-label="Expand sidebar"
          >
            <ChevronLeft className="size-3.5 rotate-180" />
          </button>
        )}
        <nav className="flex-1 overflow-y-auto px-3 pb-4 text-sm">
          {sections.map((s, i) => {
            const items = s.items.filter((x) => x.show);
            if (!items.length) return null;
            return (
              <div key={i} className={s.title ? "mt-5" : "mt-1"}>
                {s.title && (
                  <div className={`mb-1.5 flex items-center gap-2 px-2 text-xs text-muted-foreground ${collapsed ? "lg:hidden" : ""}`}>
                    {s.title}
                    <span className="h-px flex-1 bg-border" />
                  </div>
                )}
                {s.title && collapsed && <div className="mx-2 mb-1.5 hidden h-px bg-border lg:block" />}
                <div className="grid gap-0.5">
                  {items.map((it) => (
                    <SideLink key={it.href} item={it} collapsed={collapsed} />
                  ))}
                </div>
              </div>
            );
          })}
        </nav>
      </aside>

      <div className={`transition-[padding] duration-200 ${collapsed ? "lg:pl-16" : "lg:pl-60"}`}>
        <header className="sticky top-0 z-30 flex h-14 items-center gap-3 border-b bg-background/85 px-4 backdrop-blur sm:px-6">
          <button onClick={() => setDrawer(true)} className="cursor-pointer text-muted-foreground lg:hidden" aria-label="Open menu">
            <Menu className="size-5" />
          </button>
          <button
            onClick={toggle}
            className="hidden size-8 shrink-0 cursor-pointer items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground lg:flex"
            aria-label={collapsed ? "Expand sidebar" : "Collapse sidebar"}
            aria-pressed={!collapsed}
            title={`${collapsed ? "Expand" : "Collapse"} sidebar ([)${pref === null ? " · follows window width" : ""}`}
          >
            <PanelLeft className="size-4" />
          </button>
          <GlobalSearch canPVE={access.perms.has("proxmox.view")} />
          <div className="ml-auto flex items-center gap-4 text-sm">
            <FleetMeters />
            {me.tenant ? (
              <span className="hidden rounded border border-brand/40 bg-brand/10 px-1.5 py-0.5 text-[10px] font-semibold tracking-wider text-brand uppercase sm:inline" title="You work inside this organization">
                {me.tenant.name}
              </span>
            ) : (
              <span className="hidden rounded border border-brand/40 bg-brand/10 px-1.5 py-0.5 text-[10px] font-semibold tracking-wider text-brand uppercase sm:inline">
                Platform
              </span>
            )}
            <ConnectionDot />
            <Link
              href="/account"
              className="flex size-8 shrink-0 items-center justify-center rounded-full bg-brand text-sm font-semibold text-white uppercase"
              title={`${me.display_name || me.username} (${me.role})`}
            >
              {(me.display_name || me.username).slice(0, 1)}
            </Link>
            <button onClick={onLogout} className="cursor-pointer text-muted-foreground hover:text-foreground" aria-label="Sign out" title="Sign out">
              <LogOut className="size-4" />
            </button>
          </div>
        </header>
        <main className="mx-auto w-full max-w-[2400px] px-3 pt-5 pb-24 sm:px-6 sm:pt-8 xl:px-8 2xl:px-10">
          <ErrorBoundary key={loc}>{children}</ErrorBoundary>
        </main>
      </div>
    </div>
  );
}

function SideLink({ item, collapsed }: { item: NavItem; collapsed: boolean }) {
  const [loc] = useLocation();
  const active = item.href === "/" ? loc === "/" : loc.startsWith(item.href);
  return (
    <Link
      href={item.href}
      title={item.label}
      aria-label={item.label}
      aria-current={active ? "page" : undefined}
      className={`flex items-center gap-3 rounded-lg px-3 py-2 font-medium transition-colors [&_svg]:size-4 [&_svg]:shrink-0 ${
        collapsed ? "lg:justify-center lg:px-0" : ""
      } ${active ? "bg-gradient-to-r from-brand to-brand/60 text-white shadow-sm" : "text-foreground/80 hover:bg-accent hover:text-foreground"}`}
    >
      {item.icon}
      <span className={collapsed ? "lg:hidden" : ""}>{item.label}</span>
      {item.badge && <span className={`ml-auto ${collapsed ? "lg:hidden" : ""}`}>{item.badge}</span>}
    </Link>
  );
}

// Average usage across the online servers, like a fleet health strip.
function FleetMeters() {
  const map = useLive((s) => s.servers);
  const avg = useMemo(() => {
    const live = [...map.values()].filter((s) => s.online && s.latest);
    if (!live.length) return null;
    const m = (f: (x: NonNullable<(typeof live)[0]["latest"]>) => number) => Math.round(live.reduce((a, s) => a + f(s.latest!), 0) / live.length);
    return { cpu: m((x) => x.cpu), ram: m((x) => pct(x.mem_used, x.mem_total)), disk: m((x) => pct(x.disk_used, x.disk_total)) };
  }, [map]);
  if (!avg) return null;
  return (
    <div className="hidden items-center gap-4 text-[11px] font-semibold tracking-wider text-muted-foreground xl:flex" title="Average across online servers">
      <Strip label="CPU" v={avg.cpu} />
      <Strip label="RAM" v={avg.ram} />
      <Strip label="Storage" v={avg.disk} />
    </div>
  );
}

function Strip({ label, v }: { label: string; v: number }) {
  const color = v > 90 ? "bg-destructive" : v > 75 ? "bg-warning" : "bg-brand";
  return (
    <span className="flex items-center gap-2 uppercase">
      {label}
      <span className="h-1 w-16 overflow-hidden rounded-full bg-muted">
        <span className={`block h-full rounded-full ${color}`} style={{ width: `${Math.min(100, v)}%` }} />
      </span>
      <span className="tabular w-8 text-foreground">{v}%</span>
    </span>
  );
}

type Hit = { href: string; title: string; sub: string };

// Finds servers and Proxmox guests by name, host, VMID or tag. Guests load
// on first focus.
function GlobalSearch({ canPVE }: { canPVE: boolean }) {
  const map = useLive((s) => s.servers);
  const [q, setQ] = useState("");
  const [open, setOpen] = useState(false);
  const [sel, setSel] = useState(0);
  const [guests, setGuests] = useState<(PVEGuest & { cluster: string; clusterName: string })[] | null>(null);
  const [, nav] = useLocation();
  const box = useRef<HTMLDivElement>(null);

  function loadGuests() {
    if (!canPVE || guests) return;
    setGuests([]);
    void api
      .pveClusters()
      .then((cs) =>
        Promise.all(
          cs.map((c) =>
            api
              .pveResources(c.id)
              .then((r) => (r.guests ?? []).map((g) => ({ ...g, cluster: c.id, clusterName: c.name })))
              .catch(() => []),
          ),
        ),
      )
      .then((all) => setGuests(all.flat()))
      .catch(() => {});
  }

  useEffect(() => {
    const close = (e: MouseEvent) => {
      if (!box.current?.contains(e.target as Node)) setOpen(false);
    };
    const key = (e: KeyboardEvent) => {
      if (e.key === "/" && !(e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement)) {
        e.preventDefault();
        box.current?.querySelector("input")?.focus();
      }
    };
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", key);
    return () => {
      document.removeEventListener("mousedown", close);
      document.removeEventListener("keydown", key);
    };
  }, []);

  const hits = useMemo<Hit[]>(() => {
    const t = q.trim().toLowerCase();
    if (!t) return [];
    const out: Hit[] = [];
    for (const s of map.values()) {
      if (`${s.name} ${s.hostname} ${s.os_pretty}`.toLowerCase().includes(t)) out.push({ href: `/servers/${s.id}`, title: s.name, sub: `server · ${s.hostname}` });
    }
    for (const g of guests ?? []) {
      if (`${g.vmid} ${g.name} ${g.tags ?? ""} ${g.node}`.toLowerCase().includes(t))
        out.push({
          href: `/proxmox/${g.cluster}/${encodeURIComponent(g.node)}/${g.type}/${g.vmid}`,
          title: `${g.name} · ${g.vmid}`,
          sub: `${g.template ? "template" : g.type === "qemu" ? "VM" : "container"} · ${g.node} · ${g.clusterName}`,
        });
    }
    return out.slice(0, 12);
  }, [q, map, guests]);

  function go(h: Hit) {
    setOpen(false);
    setQ("");
    nav(h.href);
  }

  return (
    <div ref={box} className="relative w-full max-w-xs lg:max-w-md">
      <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
      <input
        value={q}
        onChange={(e) => {
          setQ(e.target.value);
          setSel(0);
          setOpen(true);
        }}
        onFocus={() => {
          loadGuests();
          setOpen(true);
        }}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown") setSel((s) => Math.min(s + 1, hits.length - 1));
          else if (e.key === "ArrowUp") setSel((s) => Math.max(s - 1, 0));
          else if (e.key === "Enter" && hits[sel]) go(hits[sel]);
          else if (e.key === "Escape") setOpen(false);
        }}
        placeholder={canPVE ? "Find a server or guest by name, VMID or tag" : "Find a server"}
        aria-label="Search"
        className="h-9 w-full rounded-lg border bg-card pr-3 pl-9 text-sm outline-none placeholder:text-muted-foreground focus:border-brand/60"
      />
      {open && q.trim() && (
        <div className="absolute top-full right-0 left-0 z-50 mt-1 max-h-96 overflow-y-auto rounded-lg border bg-popover p-1 shadow-xl sm:min-w-96">
          {hits.length === 0 && <div className="px-3 py-2 text-sm text-muted-foreground">No match.</div>}
          {hits.map((h, i) => (
            <button
              key={h.href}
              onMouseDown={(e) => e.preventDefault()}
              onClick={() => go(h)}
              onMouseEnter={() => setSel(i)}
              className={`block w-full cursor-pointer rounded-md px-3 py-1.5 text-left ${i === sel ? "bg-accent" : ""}`}
            >
              <div className="text-sm">{h.title}</div>
              <div className="text-xs text-muted-foreground">{h.sub}</div>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

function FiringDot() {
  const [n, setN] = useState(0);
  useEffect(() => {
    const load = () => api.firingAlerts().then((a) => setN(a.length)).catch(() => {});
    void load();
    const t = setInterval(load, 30000);
    return () => clearInterval(t);
  }, []);
  if (!n) return null;
  return (
    <span className="rounded-full bg-destructive px-1.5 text-[10px] font-semibold leading-4 text-white" title={`${n} alert(s) firing`}>
      {n}
    </span>
  );
}

function ConnectionDot() {
  const connected = useLive((s) => s.connected);
  return (
    <span className="flex items-center gap-1.5 text-xs text-muted-foreground" title={connected ? "Live updates connected" : "Reconnecting…"}>
      <span className={`size-2 rounded-full ${connected ? "bg-success" : "animate-pulse bg-warning"}`} />
      <span className="hidden sm:inline">{connected ? "Live" : "Reconnecting"}</span>
    </span>
  );
}
