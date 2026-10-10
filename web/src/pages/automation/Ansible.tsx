import { useCallback, useEffect, useRef, useState } from "react";
import { Copy, Download, GitBranch, KeyRound, Pencil, Play, Plus, RefreshCw, ShieldX, Square, Trash2, Wrench } from "lucide-react";
import { api, type AnsibleHost, type AnsibleOverview, type AnsiblePlaybook, type AnsibleRun, type Job } from "@/lib/api";
import { waitJob } from "@/lib/ops";
import { canOperate, isAdmin, useRole } from "@/lib/role";
import { timeAgo } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input, Label } from "@/components/ui/input";
import { Textarea } from "@/components/ui/select";

const runVariant = { running: "warning", succeeded: "success", failed: "destructive", cancelled: "outline" } as const;

export function Ansible() {
  const role = useRole();
  const [ov, setOv] = useState<AnsibleOverview | null>(null);
  const [pbs, setPbs] = useState<AnsiblePlaybook[]>([]);
  const [runs, setRuns] = useState<AnsibleRun[]>([]);
  const [err, setErr] = useState("");
  const [editing, setEditing] = useState<Partial<AnsiblePlaybook> | null>(null);
  const [running, setRunning] = useState<AnsiblePlaybook | null>(null);
  const [viewing, setViewing] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const [o, p, r] = await Promise.all([api.ansible(), api.ansiblePlaybooks(), api.ansibleRuns()]);
      setOv(o);
      setPbs(p);
      setRuns(r);
    } catch (e) {
      setErr((e as Error).message);
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);
  // Faster refresh while setup or a run is in progress.
  const busy = ov?.setup.running || runs.some((r) => r.status === "running");
  useEffect(() => {
    const t = setInterval(load, busy ? 2000 : 15000);
    return () => clearInterval(t);
  }, [load, busy]);

  if (!ov) return <p className="text-sm text-muted-foreground">{err || "Loading…"}</p>;

  if (!ov.installed) {
    return (
      <Card className="flex flex-col items-center gap-3 p-10 text-center">
        <Wrench className="size-8 text-muted-foreground" />
        <div className="font-medium">Ansible is not installed on the panel</div>
        <p className="max-w-md text-sm text-muted-foreground">
          Installs ansible-core into a private Python environment on the panel host and creates the panel's SSH key. Nothing is
          installed on your servers until you prepare them.
        </p>
        {isAdmin(role) ? (
          <Button
            variant="brand"
            disabled={ov.setup.running}
            onClick={() => api.ansibleSetup().then(load).catch((e) => setErr((e as Error).message))}
          >
            <Download /> {ov.setup.running ? "Installing…" : "Install Ansible"}
          </Button>
        ) : (
          <p className="text-sm text-muted-foreground">Ask an admin to install it.</p>
        )}
        {(ov.setup.log || ov.setup.error || err) && (
          <pre className="max-h-64 w-full max-w-2xl overflow-auto rounded-lg bg-muted p-3 text-left text-xs whitespace-pre-wrap">
            {ov.setup.log}
            {ov.setup.error && <span className="text-destructive">{"\n" + ov.setup.error}</span>}
            {err && <span className="text-destructive">{"\n" + err}</span>}
          </pre>
        )}
      </Card>
    );
  }

  const prepared = ov.hosts.filter((h) => h.prepared_at);
  return (
    <div className="grid gap-4">
      {err && <p className="rounded-lg border border-destructive/30 bg-destructive/10 px-4 py-2 text-sm text-destructive">{err}</p>}
      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_24rem]">
        <div className="grid min-w-0 content-start gap-4">
          <Card>
            <CardHeader className="flex-row flex-wrap items-start justify-between gap-3">
              <div>
                <CardTitle>Playbooks</CardTitle>
                <CardDescription>{ov.version}</CardDescription>
              </div>
              {isAdmin(role) && (
                <Button size="sm" variant="secondary" onClick={() => setEditing({ source: "inline", content: PLAYBOOK_TEMPLATE })}>
                  <Plus /> New playbook
                </Button>
              )}
            </CardHeader>
            <CardContent>
              <ul className="divide-y">
                {pbs.map((p) => (
                  <li key={p.id} className="flex flex-wrap items-center justify-between gap-3 py-3">
                    <div className="min-w-0">
                      <div className="flex items-center gap-2 font-medium">
                        {p.name}
                        {p.source === "git" && (
                          <Badge variant="outline" className="gap-1">
                            <GitBranch className="size-3" /> git
                          </Badge>
                        )}
                      </div>
                      <div className="truncate text-xs text-muted-foreground">
                        {p.source === "git" ? `${p.git_url} · ${p.git_path}${p.git_ref ? ` @ ${p.git_ref}` : ""}` : p.description}
                      </div>
                    </div>
                    <div className="flex gap-1">
                      {canOperate(role) && (
                        <Button size="sm" variant="brand" disabled={!prepared.length} onClick={() => setRunning(p)}>
                          <Play /> Run
                        </Button>
                      )}
                      {isAdmin(role) && (
                        <>
                          <Button size="icon" variant="ghost" className="size-8" aria-label={`Edit ${p.name}`} onClick={() => setEditing(p)}>
                            <Pencil className="size-4" />
                          </Button>
                          <Button
                            size="icon"
                            variant="ghost"
                            className="size-8 text-destructive"
                            aria-label={`Delete ${p.name}`}
                            onClick={() => {
                              if (confirm(`Delete playbook "${p.name}"? Its run history is kept.`))
                                api.deleteAnsiblePlaybook(p.id).then(load).catch((e) => setErr((e as Error).message));
                            }}
                          >
                            <Trash2 className="size-4" />
                          </Button>
                        </>
                      )}
                    </div>
                  </li>
                ))}
                {pbs.length === 0 && <li className="py-3 text-sm text-muted-foreground">No playbooks yet.</li>}
              </ul>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>Runs</CardTitle>
            </CardHeader>
            <CardContent>
              {runs.length === 0 ? (
                <p className="text-sm text-muted-foreground">Nothing has run yet.</p>
              ) : (
                <ul className="divide-y text-sm">
                  {runs.slice(0, 30).map((r) => (
                    <li key={r.id}>
                      <button
                        type="button"
                        className="grid w-full cursor-pointer grid-cols-[1fr_auto] items-center gap-x-3 gap-y-0.5 py-2 text-left hover:bg-accent/30 sm:grid-cols-[1fr_auto_auto_auto]"
                        onClick={() => setViewing(r.id)}
                      >
                        <span className="truncate font-medium">
                          {r.playbook_name}
                          {r.check && <span className="ml-2 text-xs font-normal text-muted-foreground">check mode</span>}
                        </span>
                        <Badge variant={runVariant[r.status]}>{r.status}</Badge>
                        <span className="text-xs text-muted-foreground">{recapSummary(r)}</span>
                        <span className="text-xs text-muted-foreground">
                          {r.created_by} · {timeAgo(r.created_at)}
                        </span>
                      </button>
                    </li>
                  ))}
                </ul>
              )}
            </CardContent>
          </Card>
        </div>

        <div className="grid content-start gap-4">
          <Hosts hosts={ov.hosts} admin={isAdmin(role)} onChange={load} onError={setErr} />
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <KeyRound className="size-4" /> Panel SSH key
              </CardTitle>
              <CardDescription>Ansible logs in as root with this key. Prepare adds it to a server, only for logins from the panel.</CardDescription>
            </CardHeader>
            <CardContent className="flex items-center gap-2">
              <code className="min-w-0 flex-1 truncate rounded bg-muted px-2 py-1 text-xs">{ov.public_key}</code>
              <Button size="icon" variant="ghost" className="size-8" aria-label="Copy public key" onClick={() => navigator.clipboard?.writeText(ov.public_key)}>
                <Copy className="size-4" />
              </Button>
            </CardContent>
          </Card>
        </div>
      </div>

      {editing && (
        <EditDialog
          pb={editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            void load();
          }}
        />
      )}
      {running && (
        <RunDialog
          pb={running}
          hosts={prepared}
          onClose={() => setRunning(null)}
          onStarted={(id) => {
            setRunning(null);
            setViewing(id);
            void load();
          }}
        />
      )}
      {viewing && <RunViewer id={viewing} canCancel={canOperate(role)} onClose={() => setViewing(null)} />}
    </div>
  );
}

function recapSummary(r: AnsibleRun) {
  const hosts = Object.values(r.recap ?? {});
  if (!hosts.length) return `${r.server_ids.length} server(s)`;
  const bad = hosts.filter((h) => h.failed || h.unreachable).length;
  const changed = hosts.reduce((n, h) => n + h.changed, 0);
  return `${hosts.length - bad}/${hosts.length} ok · ${changed} changed`;
}

function Hosts({ hosts, admin, onChange, onError }: { hosts: AnsibleHost[]; admin: boolean; onChange: () => void; onError: (e: string) => void }) {
  const [busy, setBusy] = useState<Record<string, string>>({});
  const [addr, setAddr] = useState<Record<string, string>>({});

  async function act(h: AnsibleHost, label: string, start: () => Promise<Job>) {
    setBusy((b) => ({ ...b, [h.server_id]: label }));
    onError("");
    try {
      const j = await waitJob(await start());
      if (j.status === "failed") throw new Error(`${h.name}: ${j.error}`);
    } catch (e) {
      onError((e as Error).message);
    } finally {
      setBusy((b) => ({ ...b, [h.server_id]: "" }));
      setTimeout(onChange, 1000);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Servers</CardTitle>
        <CardDescription>
          Prepare installs sshd and python3 if needed, authorizes the panel key for root (from the panel's address only) and pins
          the server's host keys.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <ul className="divide-y text-sm">
          {hosts.map((h) => (
            <li key={h.server_id} className="grid gap-2 py-2.5">
              <div className="flex items-center justify-between gap-2">
                <span className="flex items-center gap-2 font-medium">
                  <span className={`size-2 rounded-full ${h.online ? "bg-success" : "bg-muted-foreground/50"}`} />
                  {h.name}
                </span>
                {h.prepared_at ? (
                  <Badge variant={h.root_login === "no" ? "warning" : "success"} title={`Prepared ${timeAgo(h.prepared_at)}`}>
                    {h.root_login === "no" ? "root login disabled" : "ready"}
                  </Badge>
                ) : (
                  <Badge variant="outline">not prepared</Badge>
                )}
              </div>
              <div className="flex items-center gap-2">
                <Input
                  className="h-8 font-mono text-xs"
                  value={addr[h.server_id] ?? h.address}
                  onChange={(e) => setAddr({ ...addr, [h.server_id]: e.target.value })}
                  aria-label={`SSH address of ${h.name}`}
                  disabled={!admin}
                />
                {admin && (
                  <>
                    <Button
                      size="sm"
                      variant="secondary"
                      disabled={!h.online || !!busy[h.server_id]}
                      onClick={() => act(h, "preparing…", () => api.ansiblePrepare(h.server_id, (addr[h.server_id] ?? "").trim()))}
                    >
                      {busy[h.server_id] || (h.prepared_at ? <RefreshCw /> : "Prepare")}
                    </Button>
                    {h.prepared_at && !busy[h.server_id] && (
                      <Button
                        size="icon"
                        variant="ghost"
                        className="size-8 text-destructive"
                        title="Revoke the panel key"
                        aria-label={`Revoke Ansible access to ${h.name}`}
                        disabled={!h.online}
                        onClick={() => {
                          if (confirm(`Remove the panel's SSH key from ${h.name}?`)) void act(h, "revoking…", () => api.ansibleRevoke(h.server_id));
                        }}
                      >
                        <ShieldX className="size-4" />
                      </Button>
                    )}
                  </>
                )}
              </div>
              {h.root_login === "no" && (
                <p className="text-xs text-warning">sshd has PermitRootLogin no, so Ansible cannot log in. Allow key logins for root.</p>
              )}
            </li>
          ))}
        </ul>
      </CardContent>
    </Card>
  );
}

const PLAYBOOK_TEMPLATE = `- name: My playbook
  hosts: all
  tasks:
    - name: Make sure a package is installed
      ansible.builtin.package:
        name: htop
        state: present
`;

function EditDialog({ pb, onClose, onSaved }: { pb: Partial<AnsiblePlaybook>; onClose: () => void; onSaved: () => void }) {
  const [p, setP] = useState<Partial<AnsiblePlaybook>>(pb);
  const [err, setErr] = useState("");
  const [saving, setSaving] = useState(false);
  const set = (k: keyof AnsiblePlaybook) => (e: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) => setP({ ...p, [k]: e.target.value });
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[92svh] overflow-y-auto sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>{pb.id ? "Edit playbook" : "New playbook"}</DialogTitle>
          <DialogDescription>Runs against the servers you pick, as group "all" (and "servermgr").</DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-3"
          onSubmit={async (e) => {
            e.preventDefault();
            setSaving(true);
            setErr("");
            try {
              await api.saveAnsiblePlaybook(p);
              onSaved();
            } catch (e) {
              setErr((e as Error).message);
            } finally {
              setSaving(false);
            }
          }}
        >
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="grid gap-1.5">
              <Label htmlFor="ap-name">Name</Label>
              <Input id="ap-name" value={p.name ?? ""} onChange={set("name")} required maxLength={80} />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="ap-desc">Description</Label>
              <Input id="ap-desc" value={p.description ?? ""} onChange={set("description")} maxLength={500} />
            </div>
          </div>
          <div className="flex gap-1 rounded-lg border p-1 text-sm">
            {(["inline", "git"] as const).map((s) => (
              <button
                key={s}
                type="button"
                className={`flex-1 cursor-pointer rounded-md px-3 py-1.5 ${p.source === s ? "bg-accent font-medium" : "text-muted-foreground"}`}
                onClick={() => setP({ ...p, source: s })}
              >
                {s === "inline" ? "Write YAML here" : "From a git repository"}
              </button>
            ))}
          </div>
          {p.source === "git" ? (
            <div className="grid gap-3">
              <div className="grid gap-1.5">
                <Label htmlFor="ap-url">Repository (https)</Label>
                <Input id="ap-url" value={p.git_url ?? ""} onChange={set("git_url")} placeholder="https://github.com/acme/ansible.git" required />
              </div>
              <div className="grid gap-3 sm:grid-cols-2">
                <div className="grid gap-1.5">
                  <Label htmlFor="ap-path">Playbook file</Label>
                  <Input id="ap-path" value={p.git_path ?? ""} onChange={set("git_path")} placeholder="site.yml" required />
                </div>
                <div className="grid gap-1.5">
                  <Label htmlFor="ap-ref">Branch or tag (optional)</Label>
                  <Input id="ap-ref" value={p.git_ref ?? ""} onChange={set("git_ref")} placeholder="main" />
                </div>
              </div>
              <p className="text-xs text-muted-foreground">Public repositories only for now. The repository is cloned fresh for every run.</p>
            </div>
          ) : (
            <Textarea
              className="min-h-80 font-mono text-xs"
              value={p.content ?? ""}
              onChange={set("content")}
              spellCheck={false}
              aria-label="Playbook YAML"
              required
            />
          )}
          {err && <p className="text-sm text-destructive">{err}</p>}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" variant="brand" disabled={saving}>
              Save
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function RunDialog({ pb, hosts, onClose, onStarted }: { pb: AnsiblePlaybook; hosts: AnsibleHost[]; onClose: () => void; onStarted: (id: string) => void }) {
  const [ids, setIds] = useState<string[]>(hosts.map((h) => h.server_id));
  const [check, setCheck] = useState(true);
  const [vars, setVars] = useState("");
  const [err, setErr] = useState("");
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Run "{pb.name}"</DialogTitle>
          <DialogDescription>Only servers prepared for Ansible are listed.</DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={async (e) => {
            e.preventDefault();
            setErr("");
            try {
              const r = await api.runAnsiblePlaybook(pb.id, { server_ids: ids, check, extra_vars: vars });
              onStarted(r.id);
            } catch (e) {
              setErr((e as Error).message);
            }
          }}
        >
          <div className="grid max-h-56 gap-1 overflow-y-auto rounded-lg border p-2">
            {hosts.map((h) => (
              <label key={h.server_id} className="flex cursor-pointer items-center gap-2 rounded px-1.5 py-1 text-sm hover:bg-accent/40">
                <input
                  type="checkbox"
                  className="accent-[var(--color-brand)]"
                  checked={ids.includes(h.server_id)}
                  onChange={(e) => setIds(e.target.checked ? [...ids, h.server_id] : ids.filter((x) => x !== h.server_id))}
                />
                {h.name}
                <span className="font-mono text-xs text-muted-foreground">{h.address}</span>
              </label>
            ))}
          </div>
          <label className="flex items-start gap-2 text-sm">
            <input type="checkbox" className="mt-1" checked={check} onChange={(e) => setCheck(e.target.checked)} />
            <span>
              Check mode (dry run)
              <span className="block text-xs text-muted-foreground">Shows what would change, with diffs, without changing anything.</span>
            </span>
          </label>
          <div className="grid gap-1.5">
            <Label htmlFor="ar-vars">Extra variables (YAML or JSON, optional)</Label>
            <Textarea id="ar-vars" className="min-h-20 font-mono text-xs" value={vars} onChange={(e) => setVars(e.target.value)} placeholder="app_version: 1.4.2" spellCheck={false} />
            <p className="text-xs text-muted-foreground">Saved with the run's history. Don't put passwords here.</p>
          </div>
          {err && <p className="text-sm text-destructive">{err}</p>}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" variant={check ? "secondary" : "brand"} disabled={!ids.length}>
              <Play /> {check ? "Dry run" : "Run"} on {ids.length} server(s)
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function RunViewer({ id, canCancel, onClose }: { id: string; canCancel: boolean; onClose: () => void }) {
  const [run, setRun] = useState<AnsibleRun | null>(null);
  const [out, setOut] = useState("");
  const [err, setErr] = useState("");
  const pre = useRef<HTMLPreElement>(null);
  const stick = useRef(true);

  useEffect(() => {
    let alive = true;
    let offset = 0;
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      try {
        const g = await api.ansibleRun(id, offset);
        if (!alive) return;
        offset = g.total;
        if (g.output) setOut((o) => o + g.output);
        setRun(g.run);
        if (g.run.status === "running") timer = setTimeout(poll, 1000);
      } catch (e) {
        if (alive) setErr((e as Error).message);
      }
    };
    void poll();
    return () => {
      alive = false;
      clearTimeout(timer);
    };
  }, [id]);

  useEffect(() => {
    if (stick.current && pre.current) pre.current.scrollTop = pre.current.scrollHeight;
  }, [out]);

  const recap = Object.entries(run?.recap ?? {});
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[94svh] sm:max-w-4xl">
        <DialogHeader>
          <DialogTitle className="flex flex-wrap items-center gap-2 pr-6">
            {run?.playbook_name ?? "Run"}
            {run && <Badge variant={runVariant[run.status]}>{run.status}</Badge>}
            {run?.check && <Badge variant="outline">check mode</Badge>}
          </DialogTitle>
          <DialogDescription>{run ? `Started by ${run.created_by} ${timeAgo(run.created_at)}` : ""}</DialogDescription>
        </DialogHeader>
        {recap.length > 0 && (
          <div className="overflow-x-auto">
            <table className="w-full text-xs">
              <thead className="text-left text-muted-foreground">
                <tr>
                  {["Server", "ok", "changed", "failed", "unreachable", "skipped"].map((h) => (
                    <th key={h} className="pr-3 pb-1 font-normal">
                      {h}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody className="tabular">
                {recap.map(([host, r]) => (
                  <tr key={host}>
                    <td className="pr-3 font-medium">{host}</td>
                    <td className="pr-3 text-success">{r.ok}</td>
                    <td className={`pr-3 ${r.changed ? "text-warning" : ""}`}>{r.changed}</td>
                    <td className={`pr-3 ${r.failed ? "text-destructive" : ""}`}>{r.failed}</td>
                    <td className={`pr-3 ${r.unreachable ? "text-destructive" : ""}`}>{r.unreachable}</td>
                    <td className="pr-3">{r.skipped}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <pre
          ref={pre}
          onScroll={(e) => {
            const el = e.currentTarget;
            stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
          }}
          className="h-[55svh] overflow-auto rounded-lg bg-muted p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap"
        >
          {colorize(out)}
          {err && <span className="text-destructive">{"\n" + err}</span>}
        </pre>
        {run?.status === "running" && canCancel && (
          <div className="flex justify-end">
            <Button variant="outline" className="text-destructive" onClick={() => api.cancelAnsibleRun(id).catch((e) => setErr((e as Error).message))}>
              <Square /> Cancel run
            </Button>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

// Light highlighting of Ansible's plain output: task status prefixes.
function colorize(text: string) {
  return text.split("\n").map((line, i) => {
    let cls = "";
    if (/^(fatal|failed|ERROR)/.test(line) || /unreachable=[1-9]|failed=[1-9]/.test(line)) cls = "text-destructive";
    else if (/^changed/.test(line)) cls = "text-warning";
    else if (/^ok:/.test(line)) cls = "text-success";
    else if (/^(PLAY|TASK|RUNNING HANDLER)/.test(line)) cls = "font-semibold text-foreground";
    else if (/^skipping/.test(line)) cls = "text-muted-foreground";
    return (
      <span key={i} className={cls}>
        {line}
        {"\n"}
      </span>
    );
  });
}
