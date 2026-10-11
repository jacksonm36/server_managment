import { useCallback, useEffect, useMemo, useState } from "react";
import { AlertTriangle, Copy, Pencil, Radar, Rocket, Trash2, Undo2, Upload } from "lucide-react";
import { api, type BulkResult, type Job, type KeyCandidate, type ScannedServer, type SSHKey } from "@/lib/api";
import { useLive } from "@/lib/live";
import { setVaultStatus, useVault } from "@/lib/vault";
import { useServerGroups } from "@/lib/groups";
import { waitJob } from "@/lib/ops";
import { timeAgo } from "@/lib/utils";
import { VaultCard } from "@/components/Vault";
import { ServerPicker } from "@/components/ServerPicker";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label } from "@/components/ui/input";
import { Select, Textarea } from "@/components/ui/select";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";

type Where = { server_id: string; server: string; user: string };

const shortFP = (fp: string) => fp.replace("SHA256:", "").slice(0, 16) + "…";
const typeLabel = (k: { type: string; bits: number }) => `${k.type.replace(/^ssh-/, "")}${k.bits && !k.type.includes("ed25519") ? ` ${k.bits}` : ""}`;

export function Keys() {
  const [keys, setKeys] = useState<SSHKey[]>([]);
  const [error, setError] = useState("");
  const [scan, setScan] = useState<ScannedServer[] | null>(null);
  const [scanning, setScanning] = useState(false);
  const [dialog, setDialog] = useState<{ kind: "import" } | { kind: "deploy" | "remove"; key: SSHKey } | null>(null);
  const vault = useVault();

  const reload = useCallback(async () => {
    try {
      const r = await api.sshKeys();
      setKeys(r.keys);
      setVaultStatus(r.vault);
    } catch (e) {
      setError((e as Error).message);
    }
  }, []);
  useEffect(() => {
    void reload();
  }, [reload]);

  async function runScan() {
    setScanning(true);
    setError("");
    try {
      setScan(await api.scanSSHKeys());
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setScanning(false);
    }
  }

  // where maps a fingerprint to the servers and users that have it.
  const where = useMemo(() => {
    const m = new Map<string, Where[]>();
    for (const s of scan ?? [])
      for (const u of s.users)
        for (const k of u.keys) m.set(k.fingerprint, [...(m.get(k.fingerprint) ?? []), { server_id: s.server_id, server: s.name, user: u.user }]);
    return m;
  }, [scan]);
  const known = new Set(keys.map((k) => k.fingerprint));
  const unknown = [...where.entries()].filter(([fp]) => !known.has(fp));
  const comments = useMemo(() => {
    const m = new Map<string, string>();
    for (const s of scan ?? []) for (const u of s.users) for (const k of u.keys) if (k.comment) m.set(k.fingerprint, k.comment);
    return m;
  }, [scan]);

  async function act(f: () => Promise<unknown>) {
    setError("");
    try {
      await f();
    } catch (e) {
      setError((e as Error).message);
    }
    await reload();
  }

  return (
    <div className="grid gap-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">SSH keys</h1>
        <p className="text-sm text-muted-foreground">
          Your public keys in one place: drop in .pub files and they're recognised and named for you, then put them on servers in one click.
        </p>
      </div>
      <VaultCard />
      {error && <p className="text-sm text-destructive">{error}</p>}
      <Card>
        <CardHeader className="flex-row flex-wrap items-start justify-between gap-3">
          <div>
            <CardTitle>Key library</CardTitle>
            <CardDescription>
              {scan
                ? `Checked ${scan.length} online server(s).`
                : "Check servers to see where each key is installed and which unknown keys can log in."}
            </CardDescription>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button variant="outline" onClick={runScan} disabled={scanning}>
              <Radar /> {scanning ? "Checking…" : "Check servers"}
            </Button>
            <Button variant="brand" onClick={() => setDialog({ kind: "import" })} disabled={!vault.configured} title={vault.configured ? "" : "Set up the vault first"}>
              <Upload /> Add keys
            </Button>
          </div>
        </CardHeader>
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="text-left text-xs text-muted-foreground">
              <tr>
                <th className="px-4 py-2 font-medium">Name</th>
                <th className="px-4 py-2 font-medium">Key</th>
                <th className="px-4 py-2 font-medium">Installed on</th>
                <th className="px-4 py-2" />
              </tr>
            </thead>
            <tbody>
              {keys.length === 0 && (
                <tr className="border-t">
                  <td colSpan={4} className="px-4 py-6 text-center text-muted-foreground">
                    {vault.configured ? "No keys yet. Click “Add keys” and drop your .pub files." : "Set up the vault above, then add your keys."}
                  </td>
                </tr>
              )}
              {keys.map((k) => (
                <KeyRow
                  key={k.id}
                  k={k}
                  where={scan ? (where.get(k.fingerprint) ?? []) : null}
                  locked={!vault.unlocked}
                  onDeploy={() => setDialog({ kind: "deploy", key: k })}
                  onRemove={() => setDialog({ kind: "remove", key: k })}
                  act={act}
                />
              ))}
            </tbody>
          </table>
        </div>
      </Card>
      {unknown.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <AlertTriangle className="size-4 text-warning" /> Keys on servers that aren't in the library
            </CardTitle>
            <CardDescription>Whoever holds these keys can log in. Remove the ones you don't recognise from the server's SSH keys tab.</CardDescription>
          </CardHeader>
          <ul className="grid gap-1 px-6 pb-6 text-sm">
            {unknown.map(([fp, ws]) => (
              <li key={fp} className="flex flex-wrap items-center gap-x-3 gap-y-1">
                <span className="font-mono text-xs">{shortFP(fp)}</span>
                {comments.get(fp) && <span className="text-muted-foreground">{comments.get(fp)}</span>}
                <span className="text-xs text-muted-foreground">on {ws.map((w) => `${w.server} (${w.user})`).join(", ")}</span>
              </li>
            ))}
          </ul>
        </Card>
      )}
      {dialog?.kind === "import" && (
        <ImportDialog
          onClose={() => setDialog(null)}
          onDone={() => {
            setDialog(null);
            void reload();
          }}
        />
      )}
      {(dialog?.kind === "deploy" || dialog?.kind === "remove") && (
        <DeployDialog
          k={dialog.key}
          mode={dialog.kind}
          where={scan ? (where.get(dialog.key.fingerprint) ?? []) : null}
          users={[...new Set((scan ?? []).flatMap((s) => s.users.map((u) => u.user)))]}
          onClose={() => setDialog(null)}
          onFinished={() => scan && void runScan()}
        />
      )}
    </div>
  );
}

function KeyRow({
  k,
  where,
  locked,
  onDeploy,
  onRemove,
  act,
}: {
  k: SSHKey;
  where: Where[] | null;
  locked: boolean;
  onDeploy: () => void;
  onRemove: () => void;
  act: (f: () => Promise<unknown>) => Promise<void>;
}) {
  const [copied, setCopied] = useState(false);
  return (
    <tr className="border-t align-top">
      <td className="px-4 py-2">
        <div className="font-medium">{k.name}</div>
        <div className="text-xs text-muted-foreground">
          {k.comment && <span>{k.comment} · </span>}added {timeAgo(k.created_at)}
          {k.created_by && ` by ${k.created_by}`}
        </div>
      </td>
      <td className="px-4 py-2">
        <div className="flex flex-wrap items-center gap-1.5">
          <Badge variant="outline">{typeLabel(k)}</Badge>
          <span className="font-mono text-xs text-muted-foreground" title={k.fingerprint}>
            {shortFP(k.fingerprint)}
          </span>
        </div>
        {k.warning && <div className="mt-1 text-xs text-warning">{k.warning}</div>}
      </td>
      <td className="px-4 py-2 text-xs text-muted-foreground">
        {where === null ? "not checked" : where.length === 0 ? "nowhere" : where.map((w) => `${w.server} (${w.user})`).join(", ")}
      </td>
      <td className="px-2 py-1 text-right whitespace-nowrap">
        <Button variant="secondary" size="sm" onClick={onDeploy} disabled={locked} title={locked ? "Unlock the vault first" : "Put this key on servers"}>
          <Rocket /> Deploy
        </Button>
        <Button variant="ghost" size="icon" title="Remove from servers" aria-label="Remove from servers" onClick={onRemove}>
          <Undo2 />
        </Button>
        <Button
          variant="ghost"
          size="icon"
          title={copied ? "Copied" : "Copy public key"}
          aria-label="Copy public key"
          disabled={locked}
          onClick={() =>
            act(async () => {
              const { line } = await api.sshKeyPublic(k.id);
              await navigator.clipboard.writeText(line);
              setCopied(true);
              setTimeout(() => setCopied(false), 1500);
            })
          }
        >
          <Copy />
        </Button>
        <Button
          variant="ghost"
          size="icon"
          title="Rename"
          aria-label="Rename"
          onClick={() => {
            const name = prompt("New name", k.name)?.trim();
            if (name && name !== k.name) void act(() => api.renameSSHKey(k.id, name));
          }}
        >
          <Pencil />
        </Button>
        <Button
          variant="ghost"
          size="icon"
          title="Delete from library"
          aria-label="Delete from library"
          onClick={() => confirm(`Delete ${k.name} from the library? Servers keep it until you remove it there.`) && act(() => api.deleteSSHKey(k.id))}
        >
          <Trash2 className="text-destructive" />
        </Button>
      </td>
    </tr>
  );
}

// ---- import with recognition and name filling ----

const looksLikeConfig = (name: string, text: string) =>
  name === "config" || name.startsWith("config.") || (/^\s*Host\s+\S/im.test(text) && !/^\s*(ssh-|ecdsa-|sk-)/m.test(text));

function ImportDialog({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [files, setFiles] = useState<{ name: string; content: string }[]>([]);
  const [config, setConfig] = useState<{ name: string; content: string } | null>(null);
  const [text, setText] = useState("");
  const [cands, setCands] = useState<(KeyCandidate & { pick: boolean })[] | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [drag, setDrag] = useState(false);

  async function addFiles(list: FileList | null) {
    if (!list) return;
    const read = await Promise.all([...list].filter((f) => f.size < 256 * 1024).map(async (f) => ({ name: f.name, content: await f.text() })));
    const cfg = read.find((f) => looksLikeConfig(f.name, f.content));
    if (cfg) setConfig(cfg);
    setFiles((cur) => [...cur.filter((c) => !read.some((r) => r.name === c.name)), ...read.filter((f) => f !== cfg)]);
    setCands(null);
  }

  async function recognise() {
    setBusy(true);
    setError("");
    try {
      const r = await api.parseSSHKeys({ files, text, ssh_config: config?.content ?? "" });
      setCands(r.map((c) => ({ ...c, pick: !c.error && !c.existing })));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function doImport() {
    if (!cands) return;
    setBusy(true);
    setError("");
    try {
      const picked = cands.filter((c) => c.pick && c.line);
      const res = await api.importSSHKeys(picked.map((c) => ({ line: c.line!, name: (c.name ?? "").trim(), source: c.file ?? "pasted" })));
      const failed = res.filter((r) => r.error);
      if (failed.length) setError(failed.map((f) => `${f.name}: ${f.error}`).join("; "));
      else onDone();
      setCands(cands.map((c) => (res.some((r) => r.name === c.name?.trim() && r.id) ? { ...c, pick: false, existing: c.name } : c)));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const set = (i: number, p: Partial<KeyCandidate & { pick: boolean }>) => setCands(cands!.map((c, j) => (j === i ? { ...c, ...p } : c)));

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90svh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Add SSH keys</DialogTitle>
          <DialogDescription>
            Drop your <code>.pub</code> files (public keys only). Add your <code>~/.ssh/config</code> too and each key is named after the hosts that use it.
          </DialogDescription>
        </DialogHeader>
        {!cands ? (
          <div className="grid gap-4">
            <label
              className={`flex cursor-pointer flex-col items-center gap-2 rounded-lg border-2 border-dashed p-6 text-center text-sm ${drag ? "border-brand bg-brand/5" : "text-muted-foreground"}`}
              onDragOver={(e) => {
                e.preventDefault();
                setDrag(true);
              }}
              onDragLeave={() => setDrag(false)}
              onDrop={(e) => {
                e.preventDefault();
                setDrag(false);
                void addFiles(e.dataTransfer.files);
              }}
            >
              <Upload className="size-6" />
              Drop .pub files and your ssh config here, or click to choose
              <input type="file" multiple className="hidden" onChange={(e) => void addFiles(e.target.files)} />
            </label>
            {(files.length > 0 || config) && (
              <div className="flex flex-wrap gap-1.5">
                {files.map((f) => (
                  <Badge key={f.name} variant="outline">
                    {f.name}
                  </Badge>
                ))}
                {config && <Badge variant="success">ssh config: {config.name}</Badge>}
              </div>
            )}
            <div className="grid gap-2">
              <Label htmlFor="k-paste">Or paste keys (one per line)</Label>
              <Textarea id="k-paste" rows={3} className="font-mono text-xs" value={text} onChange={(e) => setText(e.target.value)} placeholder="ssh-ed25519 AAAA… you@laptop" />
            </div>
            {error && <p className="text-sm text-destructive">{error}</p>}
            <div className="flex justify-end gap-2">
              <Button variant="ghost" onClick={onClose}>
                Cancel
              </Button>
              <Button variant="brand" onClick={recognise} disabled={busy || (!files.length && !text.trim())}>
                Recognise keys
              </Button>
            </div>
          </div>
        ) : (
          <div className="grid gap-4">
            <ul className="grid gap-2">
              {cands.map((c, i) => (
                <li key={i} className={`grid gap-1.5 rounded-lg border p-3 ${c.error ? "border-destructive/40" : ""}`}>
                  {c.error ? (
                    <p className="text-sm text-destructive">
                      {c.file && <span className="font-medium">{c.file}: </span>}
                      {c.error}
                    </p>
                  ) : (
                    <>
                      <div className="flex items-center gap-2">
                        <input
                          type="checkbox"
                          aria-label="Import this key"
                          className="accent-[var(--color-brand)]"
                          checked={c.pick}
                          disabled={!!c.existing}
                          onChange={(e) => set(i, { pick: e.target.checked })}
                        />
                        {c.existing ? (
                          <span className="text-sm">
                            Already in the library as <span className="font-medium">{c.existing}</span>
                          </span>
                        ) : (
                          <Input className="h-8" value={c.name ?? ""} onChange={(e) => set(i, { name: e.target.value })} aria-label="Name" />
                        )}
                      </div>
                      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 pl-6 text-xs text-muted-foreground">
                        <Badge variant="outline">{typeLabel({ type: c.type ?? "", bits: c.bits ?? 0 })}</Badge>
                        <span className="font-mono">{shortFP(c.fingerprint ?? "")}</span>
                        {c.file && <span>{c.file}</span>}
                        {c.comment && <span>“{c.comment}”</span>}
                        {c.name_from && !c.existing && <span>· name from {c.name_from}</span>}
                        {c.hosts && c.hosts.length > 0 && <span>· used for {c.hosts.join(", ")}</span>}
                      </div>
                      {c.warning && <p className="pl-6 text-xs text-warning">{c.warning}</p>}
                    </>
                  )}
                </li>
              ))}
            </ul>
            {error && <p className="text-sm text-destructive">{error}</p>}
            <div className="flex justify-end gap-2">
              <Button variant="ghost" onClick={() => setCands(null)}>
                Back
              </Button>
              <Button variant="brand" onClick={doImport} disabled={busy || !cands.some((c) => c.pick)}>
                Add {cands.filter((c) => c.pick).length} key(s)
              </Button>
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

// ---- one-click deploy / removal ----

type Progress = BulkResult & { status?: string };

function DeployDialog({
  k,
  mode,
  where,
  users,
  onClose,
  onFinished,
}: {
  k: SSHKey;
  mode: "deploy" | "remove";
  where: Where[] | null;
  users: string[];
  onClose: () => void;
  onFinished: () => void;
}) {
  const servers = useLive((s) => s.servers);
  const groups = useServerGroups();
  const [user, setUser] = useState("root");
  const [group, setGroup] = useState("");
  const [ids, setIds] = useState<string[]>(() =>
    mode === "remove" && where ? [...new Set(where.filter((w) => w.user === "root").map((w) => w.server_id))] : [],
  );
  const [results, setResults] = useState<Progress[] | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const deploy = mode === "deploy";

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!deploy && !confirm(`Remove ${k.name} from ${user} on the selected servers? Make sure you keep another way in.`)) return;
    setBusy(true);
    setError("");
    try {
      const t = { user: user.trim() || "root", ...(group ? { server_group_id: Number(group) } : { server_ids: ids }) };
      const res = await (deploy ? api.deploySSHKey(k.id, t) : api.undeploySSHKey(k.id, t));
      setResults(res.map((r) => ({ ...r, status: r.job_id ? "running" : "failed" })));
      await Promise.all(
        res.map(async (r) => {
          if (!r.job_id) return;
          let status = "failed";
          let err = "";
          try {
            const j = await waitJob({ id: r.job_id } as Job);
            status = j.status;
            err = j.error;
          } catch (x) {
            err = (x as Error).message;
          }
          setResults((cur) => cur!.map((c) => (c.server_id === r.server_id ? { ...c, status, error: err || c.error } : c)));
        }),
      );
      onFinished();
    } catch (x) {
      setError((x as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && !busy && onClose()}>
      <DialogContent className="max-h-[90svh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>
            {deploy ? "Deploy" : "Remove"} {k.name}
          </DialogTitle>
          <DialogDescription>
            {deploy
              ? "Adds the key to the user's authorized_keys on each server. Servers that already have it are left as they are."
              : "Removes the key from the user's authorized_keys on each server."}
          </DialogDescription>
        </DialogHeader>
        {results ? (
          <div className="grid gap-3">
            <ul className="grid gap-1 text-sm">
              {results.map((r) => (
                <li key={r.server_id} className="flex items-start justify-between gap-3">
                  <span>{servers.get(r.server_id)?.name ?? r.server_id}</span>
                  <span
                    className={`text-right text-xs ${r.status === "succeeded" ? "text-success" : r.status === "running" ? "text-muted-foreground" : "text-destructive"}`}
                  >
                    {r.status === "succeeded" ? (deploy ? "added" : "removed") : r.status === "running" ? "working…" : r.error || "failed"}
                  </span>
                </li>
              ))}
            </ul>
            <div className="flex justify-end">
              <Button variant="brand" onClick={onClose} disabled={busy}>
                Done
              </Button>
            </div>
          </div>
        ) : (
          <form className="grid gap-4" onSubmit={submit}>
            <div className="grid gap-2">
              <Label htmlFor="d-user">Login user</Label>
              <Input id="d-user" list="d-users" value={user} onChange={(e) => setUser(e.target.value)} className="w-48" />
              <datalist id="d-users">
                {users.map((u) => (
                  <option key={u} value={u} />
                ))}
              </datalist>
            </div>
            <div className="grid gap-2">
              <Label>Servers</Label>
              {groups.length > 0 && (
                <Select aria-label="Server group" value={group} onChange={(e) => setGroup(e.target.value)}>
                  <option value="">Pick servers below</option>
                  {groups.map((g) => (
                    <option key={g.id} value={g.id}>
                      Every server in {g.name} ({g.server_ids.length})
                    </option>
                  ))}
                </Select>
              )}
              {!group && <ServerPicker value={ids} onChange={setIds} />}
              {!deploy && where && <p className="text-xs text-muted-foreground">Pre-selected: the servers where the last check found it for root.</p>}
            </div>
            {error && <p className="text-sm text-destructive">{error}</p>}
            <div className="flex justify-end gap-2">
              <Button type="button" variant="ghost" onClick={onClose}>
                Cancel
              </Button>
              <Button type="submit" variant={deploy ? "brand" : "destructive"} disabled={busy || (!group && ids.length === 0)}>
                {deploy ? <Rocket /> : <Undo2 />} {deploy ? "Deploy" : "Remove"}
              </Button>
            </div>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}
