import { CATALOG, defaults, type OpDef } from "@/lib/catalog";
import { Input, Label } from "@/components/ui/input";
import { Select, Textarea } from "@/components/ui/select";

/** Picks an op from the catalog and edits its parameters. */
export function OpForm({
  op,
  params,
  onChange,
  idPrefix = "op",
}: {
  op: string;
  params: Record<string, string>;
  onChange: (op: string, params: Record<string, string>) => void;
  idPrefix?: string;
}) {
  const def = CATALOG.find((d) => d.op === op) as OpDef | undefined;
  const groups = [...new Set(CATALOG.map((d) => d.group))];
  return (
    <div className="grid gap-3">
      <div className="grid gap-2">
        <Label htmlFor={`${idPrefix}-op`}>Action</Label>
        <Select
          id={`${idPrefix}-op`}
          value={op}
          onChange={(e) => {
            const d = CATALOG.find((x) => x.op === e.target.value)!;
            onChange(d.op, defaults(d));
          }}
        >
          {groups.map((g) => (
            <optgroup key={g} label={g}>
              {CATALOG.filter((d) => d.group === g).map((d) => (
                <option key={d.op} value={d.op}>
                  {d.label}
                </option>
              ))}
            </optgroup>
          ))}
        </Select>
      </div>
      {def?.fields.map((f) => {
        const id = `${idPrefix}-${f.key}`;
        const set = (v: string) => onChange(op, { ...params, [f.key]: v });
        if (f.kind === "bool") {
          return (
            <label key={f.key} className="flex cursor-pointer items-center gap-2 text-sm">
              <input type="checkbox" className="accent-[var(--color-brand)]" checked={params[f.key] === "true"} onChange={(e) => set(String(e.target.checked))} />
              {f.label}
            </label>
          );
        }
        return (
          <div key={f.key} className="grid gap-2">
            <Label htmlFor={id}>
              {f.label} {f.optional && <span className="font-normal text-muted-foreground">(optional)</span>}
            </Label>
            {f.kind === "select" ? (
              <Select id={id} value={params[f.key] ?? ""} onChange={(e) => set(e.target.value)}>
                {f.options?.map((o) => (
                  <option key={o.value} value={o.value}>
                    {o.label}
                  </option>
                ))}
              </Select>
            ) : f.kind === "textarea" ? (
              <Textarea id={id} className="font-mono text-xs" rows={3} placeholder={f.placeholder} value={params[f.key] ?? ""} onChange={(e) => set(e.target.value)} />
            ) : (
              <Input id={id} placeholder={f.placeholder} value={params[f.key] ?? ""} onChange={(e) => set(e.target.value)} />
            )}
          </div>
        );
      })}
    </div>
  );
}
