import { useEffect, useState } from "react";
import { KeyRound } from "lucide-react";
import { api, type PVECIOpts, type SSHKey } from "@/lib/api";
import { useAccess } from "@/lib/access";
import { Input, Label } from "@/components/ui/input";
import { Textarea } from "@/components/ui/select";

// CloudInitFields edits what a new VM gets from cloud-init on top of the
// panel's own SSH key: the login user and password, more SSH keys (typed,
// or picked from the key library), DNS, tags, start on boot and notes.

export type CIForm = { ciuser: string; cipassword: string; sshkeys: string; ssh_key_ids: number[]; nameserver: string; searchdomain: string; tags: string; onboot: boolean; description: string };

export const emptyCI: CIForm = { ciuser: "", cipassword: "", sshkeys: "", ssh_key_ids: [], nameserver: "", searchdomain: "", tags: "", onboot: false, description: "" };

// ciOpts turns the form into request fields, leaving out what is empty.
export function ciOpts(f: CIForm): PVECIOpts {
  const tags = f.tags
    .split(/[\s,;]+/)
    .map((t) => t.trim().toLowerCase())
    .filter(Boolean);
  return {
    ...(f.ciuser.trim() && { ciuser: f.ciuser.trim() }),
    ...(f.cipassword && { cipassword: f.cipassword }),
    ...(f.sshkeys.trim() && { sshkeys: f.sshkeys }),
    ...(f.ssh_key_ids.length && { ssh_key_ids: f.ssh_key_ids }),
    ...(f.nameserver.trim() && { nameserver: f.nameserver.trim() }),
    ...(f.searchdomain.trim() && { searchdomain: f.searchdomain.trim() }),
    ...(tags.length && { tags }),
    ...(f.onboot && { onboot: true }),
    ...(f.description.trim() && { description: f.description.trim() }),
  };
}

export function CloudInitFields({ value: f, onChange, defaultUser, idPrefix = "ci" }: { value: CIForm; onChange: (f: CIForm) => void; defaultUser?: string; idPrefix?: string }) {
  const ac = useAccess();
  const canKeys = ac.can("keys.manage");
  const [keys, setKeys] = useState<SSHKey[] | null>(null);
  const [locked, setLocked] = useState(false);
  useEffect(() => {
    if (!canKeys) return;
    api
      .sshKeys()
      .then((r) => {
        setKeys(r.keys);
        setLocked(!r.vault.unlocked);
      })
      .catch(() => setKeys([]));
  }, [canKeys]);
  const id = (k: string) => `${idPrefix}-${k}`;
  const set = (p: Partial<CIForm>) => onChange({ ...f, ...p });
  const toggleKey = (k: number) => set({ ssh_key_ids: f.ssh_key_ids.includes(k) ? f.ssh_key_ids.filter((x) => x !== k) : [...f.ssh_key_ids, k] });

  return (
    <div className="grid gap-3">
      <div className="grid gap-3 sm:grid-cols-2">
        <div className="grid gap-1.5">
          <Label htmlFor={id("user")}>Login user</Label>
          <Input id={id("user")} value={f.ciuser} onChange={(e) => set({ ciuser: e.target.value })} placeholder="root" autoComplete="off" />
          <span className="text-xs text-muted-foreground">
            Empty means root.
            {defaultUser && defaultUser !== "root" && (
              <>
                {" "}
                This image's own user is{" "}
                <button type="button" className="cursor-pointer text-brand hover:underline" onClick={() => set({ ciuser: defaultUser })}>
                  {defaultUser}
                </button>
                .
              </>
            )}{" "}
            Other users enroll with sudo.
          </span>
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor={id("pass")}>Password (optional)</Label>
          <Input id={id("pass")} type="password" value={f.cipassword} onChange={(e) => set({ cipassword: e.target.value })} minLength={8} autoComplete="new-password" />
          <span className="text-xs text-muted-foreground">For the console; SSH logins use keys.</span>
        </div>
      </div>

      <div className="grid gap-1.5">
        <Label>SSH keys</Label>
        <p className="text-xs text-muted-foreground">The panel's own key is always added, so servermgr can enroll and manage the VM.</p>
        {canKeys && keys && keys.length > 0 && (
          <div className="grid gap-1 rounded-md border p-2">
            {locked && <p className="text-xs text-warning">The vault is locked: unlock it (top bar) to use library keys.</p>}
            {keys.map((k) => (
              <label key={k.id} className="flex cursor-pointer items-center gap-2 rounded px-1 py-0.5 text-sm hover:bg-accent">
                <input type="checkbox" className="accent-brand" checked={f.ssh_key_ids.includes(k.id)} onChange={() => toggleKey(k.id)} disabled={locked} />
                <KeyRound className="size-3.5 text-muted-foreground" />
                <span className="truncate">{k.name}</span>
                <span className="ml-auto truncate font-mono text-[11px] text-muted-foreground">{k.fingerprint.replace(/^SHA256:/, "").slice(0, 16)}…</span>
              </label>
            ))}
          </div>
        )}
        <Textarea
          id={id("keys")}
          rows={2}
          value={f.sshkeys}
          onChange={(e) => set({ sshkeys: e.target.value })}
          placeholder="More public keys, one per line (ssh-ed25519 AAAA… you@laptop)"
          className="font-mono text-xs"
          aria-label="More SSH public keys"
        />
      </div>

      <div className="grid gap-3 sm:grid-cols-2">
        <div className="grid gap-1.5">
          <Label htmlFor={id("dns")}>DNS servers</Label>
          <Input id={id("dns")} value={f.nameserver} onChange={(e) => set({ nameserver: e.target.value })} placeholder="From DHCP or the node (e.g. 1.1.1.1 9.9.9.9)" />
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor={id("search")}>Search domain</Label>
          <Input id={id("search")} value={f.searchdomain} onChange={(e) => set({ searchdomain: e.target.value })} placeholder="lan.example.com" />
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor={id("tags")}>Tags</Label>
          <Input id={id("tags")} value={f.tags} onChange={(e) => set({ tags: e.target.value })} placeholder="web, prod" />
        </div>
        <label className="flex items-center gap-2 self-end pb-2 text-sm">
          <input type="checkbox" className="accent-brand" checked={f.onboot} onChange={(e) => set({ onboot: e.target.checked })} /> Start when the node boots
        </label>
      </div>
      <div className="grid gap-1.5">
        <Label htmlFor={id("notes")}>Notes</Label>
        <Textarea id={id("notes")} rows={2} value={f.description} onChange={(e) => set({ description: e.target.value })} placeholder="Shown on the VM's summary in Proxmox" />
      </div>
    </div>
  );
}
