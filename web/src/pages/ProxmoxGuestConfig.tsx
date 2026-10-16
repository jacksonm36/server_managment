import { useEffect, useState } from "react";
import { Check, Loader2, Pencil, RotateCcw, Wrench, X } from "lucide-react";
import { api, type PVEGuestDetail, type PVEGuestRef, type PVEOption } from "@/lib/api";
import { useAccess } from "@/lib/access";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";

// ConfigTab shows a guest's whole config. Options the panel knows how to
// check (pveoptions.go) are edited in place: switches, choices or a value;
// the rest is read-only, and hardware links to its own tab.
export function ConfigTab({
  cluster,
  guest,
  detail: g,
  hardware,
  other,
  shown,
  onHardware,
  onChanged,
}: {
  cluster: string;
  guest: PVEGuestRef;
  detail: PVEGuestDetail;
  hardware: [string, string | number][];
  other: [string, string | number][];
  shown: (k: string, v: string | number) => string;
  onHardware: () => void;
  onChanged: () => void;
}) {
  const ac = useAccess();
  const canEdit = ac.can("proxmox.guests");
  const [opts, setOpts] = useState<PVEOption[] | null>(null);
  const [saving, setSaving] = useState("");
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const admin = ac.can("proxmox.manage");
  useEffect(() => {
    api
      .pveOptions(cluster, guest)
      .then((o) => setOpts(o.filter((x) => !x.admin || admin)))
      .catch(() => setOpts([]));
  }, [cluster, guest, admin]);

  async function change(key: string, value: string | null) {
    setSaving(key);
    setMsg(null);
    try {
      await api.pveSetOptions(cluster, guest, value === null ? { delete: [key] } : { set: { [key]: value } });
      setMsg({ ok: true, text: value === null ? `${key} reset to its default.` : `${key} saved.` });
      onChanged();
    } catch (e) {
      setMsg({ ok: false, text: (e as Error).message });
    } finally {
      setSaving("");
    }
  }

  const known = new Set((opts ?? []).map((o) => o.key));
  const rest = other.filter(([k]) => !known.has(k));
  const running = g.status.status === "running";

  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <Card className="lg:row-span-2">
        <CardHeader>
          <CardTitle>Options</CardTitle>
          <CardDescription>
            {canEdit ? "Click a value to change it." : "Read-only for your account."}
            {running && g.type === "qemu" && " Some changes (firmware, machine, display) apply after the VM is stopped and started."}
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-2">
          {msg && <p className={`text-sm ${msg.ok ? "text-success" : "text-destructive"}`}>{msg.text}</p>}
          {opts === null ? (
            <p className="text-sm text-muted-foreground">Loading…</p>
          ) : (
            <dl className="divide-y text-sm">
              {opts.map((o) => (
                <OptionRow
                  key={o.key}
                  o={o}
                  value={g.config[o.key] === undefined ? undefined : String(g.config[o.key])}
                  edit={canEdit}
                  saving={saving === o.key}
                  onSave={(v) => change(o.key, v)}
                />
              ))}
            </dl>
          )}
        </CardContent>
      </Card>
      <Card>
        <CardHeader className="flex-row items-center justify-between">
          <div>
            <CardTitle>Hardware</CardTitle>
            <CardDescription>CPU, memory, disks and network cards.</CardDescription>
          </div>
          <Button size="sm" variant="secondary" onClick={onHardware}>
            <Wrench /> Edit hardware
          </Button>
        </CardHeader>
        <CardContent>
          <KV rows={hardware} shown={shown} />
        </CardContent>
      </Card>
      {rest.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle>Other settings</CardTitle>
            <CardDescription>Set in Proxmox; not editable from the panel.</CardDescription>
          </CardHeader>
          <CardContent>
            <KV rows={rest} shown={shown} />
          </CardContent>
        </Card>
      )}
    </div>
  );
}

function KV({ rows, shown }: { rows: [string, string | number][]; shown: (k: string, v: string | number) => string }) {
  if (!rows.length) return <p className="text-sm text-muted-foreground">Nothing set.</p>;
  return (
    <dl className="divide-y text-sm">
      {rows.map(([k, v]) => (
        <div key={k} className="grid grid-cols-[8rem_1fr] gap-3 py-1.5">
          <dt className="text-muted-foreground">{k}</dt>
          <dd className="min-w-0 font-mono text-xs break-all whitespace-pre-wrap">{shown(k, v)}</dd>
        </div>
      ))}
    </dl>
  );
}

function OptionRow({ o, value, edit, saving, onSave }: { o: PVEOption; value?: string; edit: boolean; saving: boolean; onSave: (v: string | null) => void }) {
  const [typing, setTyping] = useState<string | null>(null);
  const set = value !== undefined;
  let control: React.ReactNode;
  if (o.kind === "bool") {
    const on = (value ?? o.default) === "1";
    control = (
      <>
        {!set && <span className="text-[11px] text-muted-foreground italic">default</span>}
        <button
          type="button"
          role="switch"
          aria-checked={on}
          aria-label={o.key}
          disabled={!edit || saving}
          onClick={() => onSave(on ? "0" : "1")}
          className={`relative h-5 w-9 shrink-0 cursor-pointer rounded-full transition-colors disabled:cursor-default disabled:opacity-60 ${on ? "bg-brand" : "bg-muted"} ${set ? "" : "opacity-70"}`}
        >
          <span className={`absolute top-0.5 left-0.5 size-4 rounded-full bg-white shadow transition-transform ${on ? "translate-x-4" : ""}`} />
        </button>
      </>
    );
  } else if (o.kind === "enum") {
    control = (
      <Select
        aria-label={o.key}
        className="h-8 w-auto max-w-[12rem] py-0 font-mono text-xs"
        value={value ?? ""}
        disabled={!edit || saving}
        onChange={(e) => e.target.value && onSave(e.target.value)}
      >
        {!set && <option value="">default</option>}
        {o.values?.map((v) => (
          <option key={v} value={v}>
            {v}
          </option>
        ))}
      </Select>
    );
  } else if (typing !== null) {
    control = (
      <form
        className="flex items-center gap-1"
        onSubmit={(e) => {
          e.preventDefault();
          onSave(typing.trim());
          setTyping(null);
        }}
      >
        <Input autoFocus className="h-8 w-48 font-mono text-xs" value={typing} placeholder={o.hint} onChange={(e) => setTyping(e.target.value)} />
        <Button size="icon" variant="ghost" type="submit" aria-label="Save" disabled={!typing.trim()}>
          <Check />
        </Button>
        <Button size="icon" variant="ghost" type="button" aria-label="Cancel" onClick={() => setTyping(null)}>
          <X />
        </Button>
      </form>
    );
  } else {
    control = (
      <button
        type="button"
        disabled={!edit || saving}
        onClick={() => setTyping(value ?? "")}
        className="group inline-flex max-w-[16rem] cursor-pointer items-center gap-1.5 rounded px-1.5 py-0.5 text-left font-mono text-xs hover:bg-accent disabled:cursor-default disabled:hover:bg-transparent"
        title={o.hint ? `e.g. ${o.hint}` : undefined}
      >
        <span className={`truncate ${set ? "" : "text-muted-foreground italic"}`}>{set ? value : "default"}</span>
        {edit && <Pencil className="size-3 shrink-0 opacity-0 group-hover:opacity-60" />}
      </button>
    );
  }
  return (
    <div className="flex items-center justify-between gap-3 py-2">
      <div className="min-w-0">
        <dt className="font-mono text-xs">{o.key}</dt>
        <dd className="text-xs text-muted-foreground">{o.about}</dd>
      </div>
      <div className="flex shrink-0 items-center gap-1">
        {saving && <Loader2 className="size-3.5 animate-spin text-muted-foreground" />}
        {control}
        {edit && set && (
          <Button
            size="icon"
            variant="ghost"
            className="size-7"
            title="Reset to the Proxmox default"
            aria-label={`Reset ${o.key}`}
            disabled={saving}
            onClick={() => onSave(null)}
          >
            <RotateCcw className="size-3.5" />
          </Button>
        )}
      </div>
    </div>
  );
}
