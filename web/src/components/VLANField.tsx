import { Input } from "@/components/ui/input";
import { useState } from "react";
import { Select } from "@/components/ui/select";
import type { PVENetBridge, PVENetVLAN } from "@/lib/pveapi";

// VLANInput is a NIC's VLAN tag: empty for untagged. When allowed is set
// (a tenant's VLANs), only those can be picked.
export function VLANInput({ id, value, onChange, allowed }: { id?: string; value: string; onChange: (v: string) => void; allowed?: number[] }) {
  if (allowed)
    return (
      <Select id={id} value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="">Untagged</option>
        {allowed.map((v) => (
          <option key={v} value={String(v)}>
            VLAN {v}
          </option>
        ))}
      </Select>
    );
  return <Input id={id} type="number" min={1} max={4094} value={value} onChange={(e) => onChange(e.target.value)} placeholder="untagged" />;
}

// vlanNum is the tag to send: 0 for untagged.
export const vlanNum = (v: string) => Math.max(0, Math.min(4094, Number(v) || 0));

// BridgeSelect picks one of the node's bridges, showing its comment and
// whether it carries VLANs. While the list loads (or if it failed) it
// falls back to typing the name.
export function BridgeSelect({ id, value, onChange, bridges, emptyLabel }: { id?: string; value: string; onChange: (v: string) => void; bridges: PVENetBridge[] | null; emptyLabel: string }) {
  if (!bridges || bridges.length === 0)
    return <Input id={id} value={value} onChange={(e) => onChange(e.target.value)} placeholder={bridges ? "no bridges found; type one (e.g. vmbr0)" : "loading bridges…"} />;
  return (
    <Select id={id} value={value} onChange={(e) => onChange(e.target.value)}>
      <option value="">{emptyLabel}</option>
      {bridges.map((b) => (
        <option key={b.iface} value={b.iface}>
          {b.iface}
          {b.comment ? ` · ${b.comment}` : ""}
          {b.vlan_aware ? " · VLAN-aware" : ""}
        </option>
      ))}
    </Select>
  );
}

const OTHER = "__other";

// VLANSelect offers Untagged, the tags the node's guests already use (with
// who is on them) and Other for any tag 1-4094.
export function VLANSelect({ id, value, onChange, vlans, bridge, disabled }: { id?: string; value: string; onChange: (v: string) => void; vlans: PVENetVLAN[] | null; bridge: string; disabled?: boolean }) {
  const known = (vlans ?? []).filter((v) => !bridge || v.bridges.length === 0 || v.bridges.includes(bridge));
  const isKnown = value === "" || known.some((v) => String(v.tag) === value);
  const [other, setOther] = useState(!isKnown);
  const who = (g: string[]) => (g.length === 0 ? "" : ` · ${g.length} guest${g.length === 1 ? "" : "s"} (${g.slice(0, 3).join(", ")}${g.length > 3 ? ", …" : ""})`);
  return (
    <div className="grid gap-1.5">
      <Select
        id={id}
        disabled={disabled}
        value={other || !isKnown ? OTHER : value}
        onChange={(e) => {
          if (e.target.value === OTHER) {
            setOther(true);
            onChange("");
          } else {
            setOther(false);
            onChange(e.target.value);
          }
        }}
      >
        <option value="">Untagged</option>
        {known.map((v) => (
          <option key={v.tag} value={String(v.tag)}>
            VLAN {v.tag}
            {who(v.guests)}
          </option>
        ))}
        <option value={OTHER}>Other tag…</option>
      </Select>
      {(other || !isKnown) && !disabled && (
        <Input type="number" min={1} max={4094} value={value} onChange={(e) => onChange(e.target.value)} placeholder="VLAN tag (1-4094)" autoFocus />
      )}
    </div>
  );
}
