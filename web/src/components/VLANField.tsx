import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";

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
