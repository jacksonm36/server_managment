import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Archive, Plus, RotateCcw, Save, Trash2 } from "lucide-react";
import { api, type PVEBackup, type PVEFirewall, type PVEGuestDetail, type PVEGuestRef } from "@/lib/api";
import { useAccess } from "@/lib/access";
import { formatBytes } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label } from "@/components/ui/input";
import { Select, Textarea } from "@/components/ui/select";

type TabProps = {
  cluster: string;
  guest: PVEGuestRef;
  detail: PVEGuestDetail;
  onChanged: () => void;
};

function useAction() {
  const [busy, setBusy] = useState("");
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const run = async (label: string, f: () => Promise<unknown>, done?: string) => {
    setBusy(label);
    setMsg(null);
    try {
      await f();
      if (done) setMsg({ ok: true, text: done });
      return true;
    } catch (e) {
      setMsg({ ok: false, text: (e as Error).message });
      return false;
    } finally {
      setBusy("");
    }
  };
  const note = msg && <p className={`text-sm ${msg.ok ? "text-success" : "text-destructive"}`}>{msg.text}</p>;
  return { busy, run, note };
}

// ---- backups ----

export function BackupsTab({ cluster, guest, detail, onChanged }: TabProps) {
  const ac = useAccess();
  const [data, setData] = useState<{
    storages: string[];
    backups: PVEBackup[];
  } | null>(null);
  const [err, setErr] = useState("");
  const [storage, setStorage] = useState("");
  const [mode, setMode] = useState("snapshot");
  const [notes, setNotes] = useState("");
  const [restore, setRestore] = useState<PVEBackup | null>(null);
  const [confirm, setConfirm] = useState("");
  const { busy, run, note } = useAction();
  const load = useCallback(
    () =>
      api
        .pveBackups(cluster, guest)
        .then((d) => {
          setData(d);
          setStorage((s) => (d.storages.includes(s) ? s : (d.storages[0] ?? "")));
        })
        .catch((e) => setErr((e as Error).message)),
    [cluster, guest],
  );
  useEffect(() => {
    void load();
  }, [load]);
  const stopped = detail.status.status === "stopped";

  return (
    <div className="grid gap-4">
      {ac.can("proxmox.operate") && (
        <Card>
          <CardHeader>
            <CardTitle>Back up now</CardTitle>
            <CardDescription>A full backup (vzdump, zstd). It runs in the background; watch the Tasks tab.</CardDescription>
          </CardHeader>
          <CardContent>
            {data && data.storages.length === 0 ? (
              <p className="text-sm text-muted-foreground">
                No backup storage is available
                {detail.node ? ` on ${detail.node}` : ""}.
              </p>
            ) : (
              <form
                className="grid gap-3 sm:grid-cols-[1fr_1fr_2fr_auto] sm:items-end"
                onSubmit={async (e) => {
                  e.preventDefault();
                  if (await run("backup", () => api.pveBackup(cluster, guest, { storage, mode, notes }), "Backup started.")) {
                    setNotes("");
                    onChanged();
                    setTimeout(load, 4000);
                  }
                }}
              >
                <div className="grid gap-1.5">
                  <Label htmlFor="bk-storage">Storage</Label>
                  <Select id="bk-storage" value={storage} onChange={(e) => setStorage(e.target.value)}>
                    {data?.storages.map((s) => (
                      <option key={s}>{s}</option>
                    ))}
                  </Select>
                </div>
                <div className="grid gap-1.5">
                  <Label htmlFor="bk-mode">Mode</Label>
                  <Select id="bk-mode" value={mode} onChange={(e) => setMode(e.target.value)}>
                    <option value="snapshot">Snapshot (no downtime)</option>
                    <option value="suspend">Suspend</option>
                    <option value="stop">Stop</option>
                  </Select>
                </div>
                <div className="grid gap-1.5">
                  <Label htmlFor="bk-notes">Notes</Label>
                  <Input id="bk-notes" value={notes} onChange={(e) => setNotes(e.target.value)} placeholder="before the upgrade" maxLength={200} />
                </div>
                <Button type="submit" variant="brand" disabled={!!busy || !storage}>
                  <Archive /> {busy === "backup" ? "Starting…" : "Back up"}
                </Button>
              </form>
            )}
          </CardContent>
        </Card>
      )}
      {note}
      <Card>
        <CardHeader className="flex-row items-center justify-between">
          <CardTitle>Backups</CardTitle>
          <Button size="sm" variant="ghost" onClick={() => void load()}>
            Refresh
          </Button>
        </CardHeader>
        <CardContent>
          {err && <p className="text-sm text-destructive">{err}</p>}
          {!data ? (
            <p className="text-sm text-muted-foreground">Loading…</p>
          ) : data.backups.length === 0 ? (
            <p className="text-sm text-muted-foreground">No backups of this guest.</p>
          ) : (
            <ul className="divide-y text-sm">
              {data.backups.map((b) => (
                <li key={b.volid} className="flex flex-wrap items-center justify-between gap-2 py-2">
                  <div className="min-w-0">
                    <div className="font-medium tabular">{new Date(b.ctime * 1000).toLocaleString()}</div>
                    <div className="truncate text-xs text-muted-foreground">
                      {b.storage} · {formatBytes(b.size)}
                      {b.notes ? ` · ${b.notes}` : ""}
                    </div>
                  </div>
                  {ac.can("proxmox.guests") && (
                    <span className="flex gap-1">
                      <Button
                        size="sm"
                        variant="secondary"
                        disabled={!!busy || !stopped}
                        title={stopped ? "Restore this backup over the guest" : "Stop the guest before restoring"}
                        onClick={() => {
                          setRestore(b);
                          setConfirm("");
                        }}
                      >
                        <RotateCcw /> Restore
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        className="text-destructive"
                        disabled={!!busy || !!b.protected}
                        title={b.protected ? "Protected in Proxmox" : "Delete this backup"}
                        onClick={async () => {
                          if (!window.confirm(`Delete the backup from ${new Date(b.ctime * 1000).toLocaleString()}?`)) return;
                          if (await run("delete", () => api.pveDeleteBackup(cluster, guest, b.volid), "Backup deleted.")) void load();
                        }}
                        aria-label="Delete backup"
                      >
                        <Trash2 />
                      </Button>
                    </span>
                  )}
                  {restore?.volid === b.volid && (
                    <form
                      className="flex w-full flex-wrap items-end gap-2 rounded-md border border-destructive/30 bg-destructive/5 p-3"
                      onSubmit={async (e) => {
                        e.preventDefault();
                        if (await run("restore", () => api.pveRestore(cluster, guest, b.volid, confirm), "Restore started. Watch the Tasks tab.")) {
                          setRestore(null);
                          onChanged();
                        }
                      }}
                    >
                      <div className="grid flex-1 gap-1.5">
                        <Label htmlFor="rs-confirm" className="text-destructive">
                          This replaces the guest's disks and config. Type {guest.vmid} to confirm.
                        </Label>
                        <Input id="rs-confirm" value={confirm} onChange={(e) => setConfirm(e.target.value)} autoFocus />
                      </div>
                      <Button type="submit" variant="destructive" disabled={confirm !== String(guest.vmid) || !!busy}>
                        Restore
                      </Button>
                      <Button type="button" variant="ghost" onClick={() => setRestore(null)}>
                        Cancel
                      </Button>
                    </form>
                  )}
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

// ---- cloud-init ----

export function CloudInitTab({ cluster, guest, detail, onChanged }: TabProps) {
  const ac = useAccess();
  const cfg = detail.config;
  const str = (k: string) => (cfg[k] === undefined ? "" : String(cfg[k]));
  const decode = (v: string) => {
    try {
      return decodeURIComponent(v);
    } catch {
      return v;
    }
  };
  const nets = Object.keys(cfg)
    .filter((k) => /^net\d+$/.test(k))
    .sort((a, b) => a.localeCompare(b, undefined, { numeric: true }));
  const [user, setUser] = useState(str("ciuser"));
  const [password, setPassword] = useState("");
  const [keys, setKeys] = useState(decode(str("sshkeys")));
  const [dns, setDns] = useState(str("nameserver"));
  const [search, setSearch] = useState(str("searchdomain"));
  const [ipc, setIpc] = useState<Record<string, string>>(() =>
    Object.fromEntries((nets.length ? nets : ["net0"]).map((n) => [`ipconfig${n.slice(3)}`, str(`ipconfig${n.slice(3)}`)])),
  );
  const { busy, run, note } = useAction();
  const can = ac.can("proxmox.guests");
  const hasDrive = Object.values(cfg).some((v) => String(v).includes("cloudinit"));

  async function save(e: FormEvent) {
    e.preventDefault();
    if (
      await run(
        "save",
        () =>
          api.pveCloudInit(cluster, guest, {
            ciuser: user,
            cipassword: password,
            sshkeys: keys,
            nameserver: dns,
            searchdomain: search,
            ipconfig: ipc,
          }),
        "Saved. The guest picks it up on its next boot.",
      )
    ) {
      setPassword("");
      onChanged();
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Cloud-init</CardTitle>
        <CardDescription>
          First-boot settings for the VM. Changes apply on the next boot.
          {!hasDrive && " This VM has no cloud-init drive, so they have no effect until one is added."}
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form className="grid gap-4" onSubmit={save}>
          <fieldset disabled={!can || !!busy} className="grid gap-4 sm:grid-cols-2">
            <div className="grid gap-1.5">
              <Label htmlFor="ci-user">User</Label>
              <Input id="ci-user" value={user} onChange={(e) => setUser(e.target.value)} placeholder="the image's default" />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="ci-pw">Password</Label>
              <Input
                id="ci-pw"
                type="password"
                autoComplete="new-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder={cfg.cipassword ? "set; leave empty to keep it" : "none"}
              />
            </div>
            <div className="grid gap-1.5 sm:col-span-2">
              <Label htmlFor="ci-keys">SSH public keys</Label>
              <Textarea
                id="ci-keys"
                rows={3}
                value={keys}
                onChange={(e) => setKeys(e.target.value)}
                placeholder="ssh-ed25519 AAAA… you@laptop"
                className="font-mono text-xs"
              />
            </div>
            {Object.keys(ipc).map((k) => (
              <div key={k} className="grid gap-1.5">
                <Label htmlFor={`ci-${k}`}>IP config, net{k.slice(8)}</Label>
                <Input
                  id={`ci-${k}`}
                  value={ipc[k]}
                  onChange={(e) => setIpc({ ...ipc, [k]: e.target.value })}
                  placeholder="ip=dhcp or ip=10.0.0.5/24,gw=10.0.0.1"
                  className="font-mono text-xs"
                />
              </div>
            ))}
            <div className="grid gap-1.5">
              <Label htmlFor="ci-dns">DNS servers</Label>
              <Input id="ci-dns" value={dns} onChange={(e) => setDns(e.target.value)} placeholder="from the host" />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="ci-search">Search domain</Label>
              <Input id="ci-search" value={search} onChange={(e) => setSearch(e.target.value)} placeholder="from the host" />
            </div>
          </fieldset>
          {can && (
            <div className="flex items-center gap-3">
              <Button type="submit" variant="brand" disabled={!!busy}>
                <Save /> {busy ? "Saving…" : "Save"}
              </Button>
              {note}
            </div>
          )}
        </form>
      </CardContent>
    </Card>
  );
}

// ---- firewall ----

const emptyRule: { type: "in" | "out"; action: "ACCEPT" | "DROP" | "REJECT"; proto: string; dport: string; source: string; comment: string } = {
  type: "in" as const,
  action: "ACCEPT" as const,
  proto: "tcp",
  dport: "",
  source: "",
  comment: "",
};

export function FirewallTab({ cluster, guest, detail }: TabProps) {
  const ac = useAccess();
  const can = ac.can("proxmox.guests");
  const [fw, setFw] = useState<PVEFirewall | null>(null);
  const [err, setErr] = useState("");
  const [rule, setRule] = useState(emptyRule);
  const { busy, run, note } = useAction();
  const load = useCallback(
    () =>
      api
        .pveFirewall(cluster, guest)
        .then(setFw)
        .catch((e) => setErr((e as Error).message)),
    [cluster, guest],
  );
  useEffect(() => {
    void load();
  }, [load]);
  const netsWithout = Object.entries(detail.config)
    .filter(([k, v]) => /^net\d+$/.test(k) && !String(v).includes("firewall=1"))
    .map(([k]) => k);
  const opts = (o: Partial<{ enable: boolean; policy_in: string; policy_out: string }>) =>
    run("options", () =>
      api.pveFirewallOptions(cluster, guest, {
        enable: !!fw?.enable,
        policy_in: fw!.policy_in,
        policy_out: fw!.policy_out,
        ...o,
      }),
    ).then((ok) => {
      if (ok) void load();
    });

  if (!fw) return err ? <p className="text-sm text-destructive">{err}</p> : <p className="text-sm text-muted-foreground">Loading…</p>;
  return (
    <div className="grid gap-4">
      <Card>
        <CardHeader>
          <CardTitle>Firewall</CardTitle>
          <CardDescription>
            Proxmox's firewall for this guest. It also needs the datacenter firewall on and firewall=1 on the network device.
            {netsWithout.length > 0 && ` Not set on ${netsWithout.join(", ")}.`}
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-wrap items-end gap-4">
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={!!fw.enable} disabled={!can || !!busy} onChange={(e) => void opts({ enable: e.target.checked })} />
            Enabled
          </label>
          {(["policy_in", "policy_out"] as const).map((k) => (
            <div key={k} className="grid gap-1.5">
              <Label htmlFor={k}>{k === "policy_in" ? "Incoming default" : "Outgoing default"}</Label>
              <Select id={k} value={fw[k]} disabled={!can || !!busy} onChange={(e) => void opts({ [k]: e.target.value })}>
                <option>ACCEPT</option>
                <option>DROP</option>
                <option>REJECT</option>
              </Select>
            </div>
          ))}
        </CardContent>
      </Card>
      {note}
      <Card>
        <CardHeader>
          <CardTitle>Rules</CardTitle>
          <CardDescription>Checked top to bottom; the first match wins.</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4">
          {fw.rules.length === 0 ? (
            <p className="text-sm text-muted-foreground">No rules.</p>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead className="text-left text-xs text-muted-foreground">
                  <tr>
                    <th className="py-2 pr-3 font-normal">#</th>
                    <th className="py-2 pr-3 font-normal">Direction</th>
                    <th className="py-2 pr-3 font-normal">Action</th>
                    <th className="py-2 pr-3 font-normal">Protocol / port</th>
                    <th className="py-2 pr-3 font-normal">Source</th>
                    <th className="hidden py-2 pr-3 font-normal md:table-cell">Comment</th>
                    <th />
                  </tr>
                </thead>
                <tbody className="divide-y">
                  {fw.rules.map((r) => (
                    <tr key={r.pos} className={r.enable ? "" : "opacity-50"}>
                      <td className="py-2 pr-3 tabular text-muted-foreground">{r.pos}</td>
                      <td className="py-2 pr-3">{r.type}</td>
                      <td className="py-2 pr-3">
                        <Badge variant={r.action === "ACCEPT" ? "success" : "destructive"}>{r.action}</Badge>
                      </td>
                      <td className="py-2 pr-3 font-mono text-xs">
                        {r.proto || "any"}
                        {r.dport ? ` ${r.dport}` : ""}
                      </td>
                      <td className="py-2 pr-3 font-mono text-xs">{r.source || "any"}</td>
                      <td className="hidden py-2 pr-3 text-xs text-muted-foreground md:table-cell">{r.comment}</td>
                      <td className="py-2 text-right">
                        {can && (
                          <Button
                            size="icon"
                            variant="ghost"
                            className="size-8 text-destructive"
                            aria-label="Delete rule"
                            disabled={!!busy}
                            onClick={async () => (await run("delete", () => api.pveFirewallDelete(cluster, guest, r.pos))) && load()}
                          >
                            <Trash2 />
                          </Button>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {can && (
            <form
              className="grid gap-3 rounded-md border p-3 sm:grid-cols-[auto_auto_auto_1fr_1.5fr_1.5fr_auto] sm:items-end"
              onSubmit={async (e) => {
                e.preventDefault();
                const r = {
                  ...rule,
                  enable: 1,
                  proto: rule.proto === "any" ? "" : rule.proto,
                };
                if (await run("add", () => api.pveFirewallAdd(cluster, guest, r))) {
                  setRule(emptyRule);
                  void load();
                }
              }}
            >
              <div className="grid gap-1.5">
                <Label htmlFor="fw-type">Direction</Label>
                <Select id="fw-type" value={rule.type} onChange={(e) => setRule({ ...rule, type: e.target.value as "in" | "out" })}>
                  <option value="in">in</option>
                  <option value="out">out</option>
                </Select>
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="fw-action">Action</Label>
                <Select id="fw-action" value={rule.action} onChange={(e) => setRule({ ...rule, action: e.target.value as "ACCEPT" })}>
                  <option>ACCEPT</option>
                  <option>DROP</option>
                  <option>REJECT</option>
                </Select>
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="fw-proto">Protocol</Label>
                <Select id="fw-proto" value={rule.proto} onChange={(e) => setRule({ ...rule, proto: e.target.value, dport: "" })}>
                  {["tcp", "udp", "icmp", "any"].map((p) => (
                    <option key={p}>{p}</option>
                  ))}
                </Select>
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="fw-port">Port</Label>
                <Input
                  id="fw-port"
                  value={rule.dport}
                  disabled={rule.proto !== "tcp" && rule.proto !== "udp"}
                  onChange={(e) => setRule({ ...rule, dport: e.target.value })}
                  placeholder="22, 8000:8080"
                />
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="fw-src">Source</Label>
                <Input id="fw-src" value={rule.source} onChange={(e) => setRule({ ...rule, source: e.target.value })} placeholder="any, or 10.0.0.0/8" />
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="fw-comment">Comment</Label>
                <Input id="fw-comment" value={rule.comment} onChange={(e) => setRule({ ...rule, comment: e.target.value })} maxLength={200} />
              </div>
              <Button type="submit" variant="secondary" disabled={!!busy}>
                <Plus /> Add
              </Button>
            </form>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
