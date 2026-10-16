import { useState } from "react";
import { Check, Copy, Network } from "lucide-react";
import type { PVEGuestDetail, PVEGuestIface } from "@/lib/api";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";

// A guest's network cards (net0, net1…) from its config, each matched by MAC
// to the interface the guest itself reports, so every address shows under
// the bridge and VLAN it lives on. Interfaces with no card of their own
// (docker0, br-…, tunnels) are listed apart as "inside the guest".

export type GuestNet = {
  key: string; // net0
  model: string; // virtio, e1000…; empty for a container
  mac: string;
  bridge: string;
  tag: string;
  firewall: boolean;
  down: boolean;
  ifname: string; // container's name for it (eth0)
  static: string[]; // container's configured ip/ip6
  addrs: string[]; // reported by the guest, CIDR
};

function parseNet(key: string, raw: string, lxc: boolean): GuestNet {
  const n: GuestNet = { key, model: "", mac: "", bridge: "", tag: "", firewall: false, down: false, ifname: "", static: [], addrs: [] };
  for (const part of raw.split(",")) {
    const [k, v = ""] = part.split("=");
    switch (k) {
      case "bridge":
        n.bridge = v;
        break;
      case "tag":
        n.tag = v;
        break;
      case "firewall":
        n.firewall = v === "1";
        break;
      case "link_down":
        n.down = v === "1";
        break;
      case "name":
        n.ifname = v;
        break;
      case "hwaddr":
        n.mac = v.toUpperCase();
        break;
      case "ip":
      case "ip6":
        if (v && v !== "dhcp" && v !== "auto" && v !== "manual") n.static.push(v);
        break;
      default:
        if (!lxc && /^(virtio|e1000e?|rtl8139|vmxnet3|i82551|i82557b|i82559er|ne2k_pci|ne2k_isa|pcnet)$/.test(k)) {
          n.model = k;
          n.mac = v.toUpperCase();
        }
    }
  }
  return n;
}

// guestNets pairs config cards with reported interfaces.
export function guestNets(g: PVEGuestDetail): { nets: GuestNet[]; others: PVEGuestIface[]; reported: boolean } {
  const lxc = g.type === "lxc";
  const nets = Object.entries(g.config)
    .filter(([k]) => /^net\d+$/.test(k))
    .sort(([a], [b]) => a.localeCompare(b, undefined, { numeric: true }))
    .map(([k, v]) => parseNet(k, String(v), lxc));
  const ifs = g.interfaces ?? [];
  const used = new Set<PVEGuestIface>();
  for (const n of nets) {
    const m = ifs.find((i) => !used.has(i) && ((n.mac && i.mac?.toUpperCase() === n.mac) || (lxc && n.ifname && i.name === n.ifname)));
    if (m) {
      used.add(m);
      n.addrs = m.addrs;
    }
  }
  return { nets, others: ifs.filter((i) => !used.has(i) && i.addrs.length > 0), reported: ifs.length > 0 };
}

const bare = (a: string) => a.split("/")[0];

function IPChip({ addr, primary }: { addr: string; primary?: boolean }) {
  const [done, setDone] = useState(false);
  return (
    <button
      type="button"
      title={`${addr}: click to copy`}
      onClick={(e) => {
        e.stopPropagation();
        void navigator.clipboard?.writeText(bare(addr)).then(() => {
          setDone(true);
          setTimeout(() => setDone(false), 1200);
        });
      }}
      className={`group inline-flex cursor-pointer items-center gap-1 rounded-md border px-1.5 py-0.5 font-mono text-xs transition-colors ${
        primary ? "border-brand/40 bg-brand/10 text-foreground hover:bg-brand/20" : "bg-muted/40 text-foreground/90 hover:bg-accent"
      }`}
    >
      {bare(addr)}
      {done ? <Check className="size-3 text-success" /> : <Copy className="size-3 opacity-0 transition-opacity group-hover:opacity-60" />}
    </button>
  );
}

function NetLabel({ n }: { n: GuestNet }) {
  return (
    <span className="inline-flex items-center gap-1.5 text-xs whitespace-nowrap">
      <span className={`size-1.5 rounded-full ${n.down ? "bg-destructive" : n.addrs.length ? "bg-success" : "bg-muted-foreground/50"}`} />
      <span className="font-medium">{n.ifname || n.key}</span>
      <span className="text-muted-foreground">{n.bridge || "no bridge"}</span>
      {n.tag && <span className="rounded bg-sky-500/15 px-1 text-[10px] font-medium text-sky-400">VLAN {n.tag}</span>}
      {n.firewall && <span className="rounded bg-warning/15 px-1 text-[10px] font-medium text-warning">FW</span>}
      {n.down && <span className="text-[10px] text-destructive">unplugged</span>}
    </span>
  );
}

// NetSummary is the guest header's network line: one row per card with its
// addresses as click-to-copy chips, and the guest's own extra interfaces
// folded behind a toggle.
export function NetSummary({ g, running }: { g: PVEGuestDetail; running: boolean }) {
  const { nets, others, reported } = guestNets(g);
  const [more, setMore] = useState(false);
  if (!nets.length) return null;
  let first = true;
  return (
    <div className="mt-2 grid gap-1">
      {nets.map((n) => {
        const addrs = n.addrs.length ? n.addrs : n.static;
        return (
          <div key={n.key} className="flex flex-wrap items-center gap-1.5">
            <NetLabel n={n} />
            {addrs.map((a) => {
              const p = first && !a.includes(":");
              if (p) first = false;
              return <IPChip key={a} addr={a} primary={p} />;
            })}
            {!addrs.length && (
              <span className="text-xs text-muted-foreground">
                {!running ? "stopped" : g.type === "qemu" && !reported ? "no address reported (guest agent off?)" : "no address"}
              </span>
            )}
          </div>
        );
      })}
      {others.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5">
          <button type="button" onClick={() => setMore(!more)} className="cursor-pointer text-xs text-muted-foreground hover:text-foreground">
            {more ? "▾" : "▸"} {others.length} more inside the guest ({others.map((o) => o.name).join(", ")})
          </button>
          {more &&
            others.map((o) => (
              <span key={o.name} className="inline-flex items-center gap-1">
                <span className="text-xs text-muted-foreground">{o.name}</span>
                {o.addrs.map((a) => (
                  <IPChip key={a} addr={a} />
                ))}
              </span>
            ))}
        </div>
      )}
    </div>
  );
}

// NetworkCard is the overview's table of network cards.
export function NetworkCard({ g, running }: { g: PVEGuestDetail; running: boolean }) {
  const { nets, others, reported } = guestNets(g);
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Network className="size-4 text-brand" /> Networks
        </CardTitle>
        <CardDescription>
          {!running
            ? "Addresses show while the guest runs."
            : reported
              ? "Addresses as the guest reports them."
              : g.type === "qemu"
                ? "Turn on the QEMU guest agent (Configuration → agent, and qemu-guest-agent in the VM) to see addresses."
                : "The container reports no addresses."}
        </CardDescription>
      </CardHeader>
      <CardContent className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead className="text-left text-xs text-muted-foreground">
            <tr>
              <th className="py-1 pr-3 font-medium">Interface</th>
              <th className="py-1 pr-3 font-medium">Bridge</th>
              <th className="py-1 pr-3 font-medium">VLAN</th>
              <th className="py-1 pr-3 font-medium">MAC</th>
              <th className="py-1 pr-3 font-medium">Addresses</th>
            </tr>
          </thead>
          <tbody>
            {nets.map((n) => (
              <tr key={n.key} className="border-t align-top">
                <td className="py-2 pr-3">
                  <span className="font-medium">{n.key}</span>
                  {(n.ifname || n.model) && <span className="ml-1 text-xs text-muted-foreground">{n.ifname || n.model}</span>}
                  <div className="mt-0.5 flex gap-1">
                    {n.firewall && <span className="rounded bg-warning/15 px-1 text-[10px] font-medium text-warning">firewall</span>}
                    {n.down && <span className="rounded bg-destructive/15 px-1 text-[10px] font-medium text-destructive">unplugged</span>}
                  </div>
                </td>
                <td className="py-2 pr-3 font-mono text-xs">{n.bridge || "—"}</td>
                <td className="py-2 pr-3 text-xs">{n.tag || <span className="text-muted-foreground">untagged</span>}</td>
                <td className="py-2 pr-3 font-mono text-xs text-muted-foreground">{n.mac || "—"}</td>
                <td className="py-2">
                  <div className="flex flex-wrap gap-1">
                    {(n.addrs.length ? n.addrs : n.static).map((a) => (
                      <IPChip key={a} addr={a} primary={!a.includes(":")} />
                    ))}
                    {!n.addrs.length && !n.static.length && <span className="text-xs text-muted-foreground">—</span>}
                  </div>
                </td>
              </tr>
            ))}
            {others.map((o) => (
              <tr key={o.name} className="border-t align-top text-muted-foreground">
                <td className="py-2 pr-3">
                  {o.name} <span className="text-xs">inside the guest</span>
                </td>
                <td className="py-2 pr-3">—</td>
                <td className="py-2 pr-3">—</td>
                <td className="py-2 pr-3 font-mono text-xs">{o.mac || "—"}</td>
                <td className="py-2">
                  <div className="flex flex-wrap gap-1">
                    {o.addrs.map((a) => (
                      <IPChip key={a} addr={a} />
                    ))}
                  </div>
                </td>
              </tr>
            ))}
            {nets.length === 0 && (
              <tr>
                <td colSpan={5} className="py-2 text-muted-foreground">
                  No network cards. Add one on the Hardware tab.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </CardContent>
    </Card>
  );
}
