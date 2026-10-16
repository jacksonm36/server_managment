import { useEffect, useMemo, useState } from "react";
import { Box, ChevronDown, ChevronRight, LayoutTemplate, Monitor, Network, Server } from "lucide-react";
import { api, type PVEGuest, type PVEGuestOS, type PVEResources } from "@/lib/api";
import { LogoMark, appIcon, osIcon } from "@/components/DistroIcon";
import { usePending } from "@/lib/pending";

// ComputeTree is the cluster as a tree: the cluster, its nodes, and each
// node's guests. Picking the cluster, a node or a guest changes what the
// page shows next to it. Tenants see no nodes, so their guests are grouped
// under plain node headings.

const dot = (s: string) => (s === "running" || s === "online" ? "bg-success" : s === "stopped" ? "bg-muted-foreground/50" : s === "paused" ? "bg-warning" : "bg-destructive");

export function ComputeTree({
  cluster,
  name,
  res,
  selected,
  onSelect,
  guest,
  onGuest,
}: {
  cluster: string;
  name: string;
  res: PVEResources;
  selected: string; // "" = the cluster, else a node name
  onSelect: (node: string) => void;
  guest: string; // the picked guest's id, or ""
  onGuest: (g: PVEGuest) => void;
}) {
  const [closed, setClosed] = useState<Record<string, boolean>>({});
  const [q, setQ] = useState("");
  // OS per guest ("node/vmid"); looked up in the background, cached by the panel.
  const [os, setOS] = useState<Record<string, PVEGuestOS>>({});
  useEffect(() => {
    const get = () =>
      api
        .pveGuestOS(cluster)
        .then(setOS)
        .catch(() => {});
    void get();
    const t = setInterval(get, 5 * 60_000);
    return () => clearInterval(t);
  }, [cluster]);
  const nodes = res.nodes ?? [];
  const byNode = useMemo(() => {
    const m = new Map<string, PVEGuest[]>();
    const t = q.trim().toLowerCase();
    for (const g of res.guests ?? []) {
      if (t && !`${g.vmid} ${g.name} ${g.tags ?? ""}`.toLowerCase().includes(t)) continue;
      m.set(g.node, [...(m.get(g.node) ?? []), g]);
    }
    for (const l of m.values()) l.sort((a, b) => (a.template ?? 0) - (b.template ?? 0) || a.vmid - b.vmid);
    return m;
  }, [res.guests, q]);
  // Nodes from the resources, plus node names only known from guests
  // (what a tenant sees).
  const names = [...new Set([...nodes.map((n) => n.node), ...byNode.keys()])].sort();
  const row = (active: boolean) =>
    `flex w-full min-w-0 cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm ${active ? "bg-brand/15 text-foreground" : "text-muted-foreground hover:bg-accent hover:text-foreground"}`;

  return (
    <nav aria-label="Compute" className="grid gap-1 text-sm">
      <input
        value={q}
        onChange={(e) => setQ(e.target.value)}
        placeholder="Filter guests…"
        aria-label="Filter guests"
        className="mb-1 h-8 w-full rounded-md border border-input bg-transparent px-2.5 text-sm outline-none placeholder:text-muted-foreground focus-visible:border-ring dark:bg-input/30"
      />
      <button className={row(selected === "" && !guest)} onClick={() => onSelect("")} aria-current={selected === "" && !guest ? "true" : undefined}>
        <Network className="size-4 shrink-0" />
        <span className="truncate font-medium">{name}</span>
        <span className="ml-auto text-xs tabular-nums">{(res.guests ?? []).filter((g) => !g.template).length}</span>
      </button>
      <ul className="grid gap-0.5 pl-2">
        {names.map((n) => {
          const node = nodes.find((x) => x.node === n);
          const guests = byNode.get(n) ?? [];
          const open = !closed[n] || q.trim() !== "";
          return (
            <li key={n}>
              <div className="flex items-center">
                <button
                  className="cursor-pointer rounded p-0.5 text-muted-foreground hover:text-foreground"
                  aria-label={open ? `Collapse ${n}` : `Expand ${n}`}
                  aria-expanded={open}
                  onClick={() => setClosed((c) => ({ ...c, [n]: open }))}
                >
                  {open ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
                </button>
                {node ? (
                  <button className={row(selected === n && !guest)} onClick={() => onSelect(n)} aria-current={selected === n && !guest ? "true" : undefined}>
                    <Server className="size-4 shrink-0" />
                    <span className="truncate">{n}</span>
                    <NodeDot node={node.node} status={node.status} />
                    {node.status === "online" && <span className="ml-auto text-xs tabular-nums">{Math.round(node.cpu * 100)}%</span>}
                  </button>
                ) : (
                  <span className="flex items-center gap-2 px-2 py-1.5 text-muted-foreground">
                    <Server className="size-4" /> {n}
                  </span>
                )}
              </div>
              {open && (
                <ul className="ml-3.5 grid gap-0.5 border-l pl-2">
                  {guests.map((g) => {
                    const Icon = g.template ? LayoutTemplate : g.type === "lxc" ? Box : Monitor;
                    const o = os[`${g.node}/${g.vmid}`];
                    const oi = o ? osIcon(o.os) : null;
                    const app = appIcon(o?.app, g.name ?? "", g.tags ?? "");
                    const osTitle = o ? `${o.pretty || o.os}${o.os_via ? ` (${o.os_via})` : ""}` : "";
                    const appTitle = app ? `${app.label}${o?.app && o.app_via ? ` (detected on ${o.app_via})` : o?.app ? "" : " (from the name or tags)"}` : "";
                    return (
                      <li key={g.id}>
                        <button
                          className={row(guest === g.id)}
                          onClick={() => onGuest(g)}
                          aria-current={guest === g.id ? "true" : undefined}
                          title={`${g.type === "lxc" ? "CT" : "VM"} ${g.vmid}${g.template ? " (template)" : ""}${osTitle ? ` · ${osTitle}` : ""}${appTitle ? ` · ${appTitle}` : ""}`}
                        >
                          {oi && (oi.icon || oi.windows) ? (
                            <LogoMark icon={oi.icon} windows={oi.windows} title={osTitle} />
                          ) : (
                            <Icon className="size-3.5 shrink-0" />
                          )}
                          <span className="truncate">{g.name || `${g.vmid}`}</span>
                          {app && <LogoMark icon={app.icon} Lucide={app.Lucide} title={appTitle} className="size-3" />}
                          {!g.template && <GuestDot node={g.node} vmid={g.vmid} status={g.status} />}
                          <span className="ml-auto font-mono text-[11px] text-muted-foreground/70">{g.vmid}</span>
                        </button>
                      </li>
                    );
                  })}
                  {guests.length === 0 && <li className="px-2 py-1 text-xs text-muted-foreground">{q ? "No match" : "No guests"}</li>}
                </ul>
              )}
            </li>
          );
        })}
      </ul>
    </nav>
  );
}

// A guest's or node's status dot; it pulses while a power action runs.
function GuestDot({ node, vmid, status }: { node: string; vmid: number; status: string }) {
  const p = usePending(`${node}/${vmid}`);
  return p ? (
    <span className="animate-ring size-1.5 shrink-0 rounded-full bg-warning text-warning" title={`${p}…`} />
  ) : (
    <span className={`size-1.5 shrink-0 rounded-full ${dot(status)}`} />
  );
}

function NodeDot({ node, status }: { node: string; status: string }) {
  const p = usePending(node);
  return p ? (
    <span className="animate-ring size-1.5 shrink-0 rounded-full bg-warning text-warning" title={`${p}…`} />
  ) : (
    <span className={`size-1.5 shrink-0 rounded-full ${dot(status)}`} title={status} />
  );
}
