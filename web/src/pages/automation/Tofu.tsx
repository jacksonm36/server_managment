import { useCallback, useEffect, useRef, useState } from "react";
import { Boxes, Download, FileCode2, GitBranch, Lock, Pencil, Play, Plus, Square, Trash2, Unlock, X, Zap } from "lucide-react";
import { api, type TofuOverview, type TofuRun, type TofuVar, type TofuWorkspaceView } from "@/lib/api";
import { canOperate, isAdmin, useRole } from "@/lib/role";
import { timeAgo } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input, Label } from "@/components/ui/input";
import { Select, Textarea } from "@/components/ui/select";

const runVariant = { running: "warning", succeeded: "success", failed: "destructive", cancelled: "outline" } as const;
const kindLabel = { plan: "Plan", "destroy-plan": "Destroy plan", apply: "Apply" } as const;

export function Tofu() {
  const role = useRole();
  const [ov, setOv] = useState<TofuOverview | null>(null);
  const [runs, setRuns] = useState<TofuRun[]>([]);
  const [err, setErr] = useState("");
  const [editing, setEditing] = useState<TofuWorkspaceView | "new" | null>(null);
  const [viewing, setViewing] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const [o, r] = await Promise.all([api.tofu(), api.tofuRuns()]);
      setOv(o);
      setRuns(r);
    } catch (e) {
      setErr((e as Error).message);
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);
  const busy = ov?.setup.running || runs.some((r) => r.status === "running");
  useEffect(() => {
    const t = setInterval(load, busy ? 2000 : 15000);
    return () => clearInterval(t);
  }, [load, busy]);

  async function act(f: () => Promise<unknown>) {
    setErr("");
    try {
      await f();
    } catch (e) {
      setErr((e as Error).message);
    }
    void load();
  }

  if (!ov) return <p className="text-sm text-muted-foreground">{err || "Loading…"}</p>;
  if (!ov.installed) {
    return (
      <Card className="flex flex-col items-center gap-3 p-10 text-center">
        <Boxes className="size-8 text-muted-foreground" />
        <div className="font-medium">OpenTofu is not installed on the panel</div>
        <p className="max-w-md text-sm text-muted-foreground">
          Downloads OpenTofu {ov.version} from its official GitHub release, checks its SHA-256 and installs it into the panel's
          data directory. State is stored in the panel database, encrypted.
        </p>
        {isAdmin(role) ? (
          <Button variant="brand" disabled={ov.setup.running} onClick={() => act(api.tofuSetup)}>
            <Download /> {ov.setup.running ? "Installing…" : `Install OpenTofu ${ov.version}`}
          </Button>
        ) : (
          <p className="text-sm text-muted-foreground">Ask an admin to install it.</p>
        )}
        {(ov.setup.error || err) && <p className="text-sm text-destructive">{ov.setup.error || err}</p>}
      </Card>
    );
  }

  return (
    <div className="grid gap-4">
      {err && <p className="rounded-lg border border-destructive/30 bg-destructive/10 px-4 py-2 text-sm text-destructive">{err}</p>}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-muted-foreground">
          OpenTofu {ov.version}. Every run gets <code className="rounded bg-muted px-1 text-xs">var.servermgr_servers</code> (name → id,
          address, OS) if the configuration declares it.
        </p>
        {isAdmin(role) && (
          <Button size="sm" variant="secondary" onClick={() => setEditing("new")}>
            <Plus /> New workspace
          </Button>
        )}
      </div>

      <div className="grid gap-4 xl:grid-cols-2">
        {ov.workspaces.map((v) => (
          <WorkspaceCard
            key={v.workspace.id}
            v={v}
            role={role}
            onPlan={(destroy) => act(async () => setViewing((await api.tofuPlan(v.workspace.id, destroy)).id))}
            onView={setViewing}
            onEdit={() => setEditing(v)}
            onDelete={() => {
              if (confirm(`Delete workspace "${v.workspace.name}" and its state history?`)) void act(() => api.deleteTofuWorkspace(v.workspace.id));
            }}
            onUnlock={() => {
              if (confirm("Force-unlock the state? Only do this if no run is using it.")) void act(() => api.tofuUnlock(v.workspace.id));
            }}
          />
        ))}
        {ov.workspaces.length === 0 && <p className="text-sm text-muted-foreground">No workspaces yet.</p>}
      </div>

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
                    <span className="truncate">
                      <span className="font-medium">{r.workspace_name}</span>
                      <span className="ml-2 text-xs text-muted-foreground">{kindLabel[r.kind]}</span>
                    </span>
                    <Badge variant={runVariant[r.status]}>{r.status}</Badge>
                    <span className="text-xs text-muted-foreground">{runSummary(r)}</span>
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

      {editing && (
        <EditDialog
          view={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            void load();
          }}
        />
      )}
      {viewing && <RunViewer id={viewing} role={role} onClose={() => setViewing(null)} onOpen={setViewing} onChange={load} />}
    </div>
  );
}

function runSummary(r: TofuRun) {
  const s = r.summary;
  if (!s) return "";
  if (r.kind === "apply") return s.added !== undefined ? `+${s.added} ~${s.changed} -${s.destroyed}` : "";
  if (s.changes === false) return "no changes";
  return s.add !== undefined ? `+${s.add} ~${s.change} -${s.destroy}${r.applied ? " · applied" : ""}` : "";
}

function WorkspaceCard({
  v,
  role,
  onPlan,
  onView,
  onEdit,
  onDelete,
  onUnlock,
}: {
  v: TofuWorkspaceView;
  role: ReturnType<typeof useRole>;
  onPlan: (destroy: boolean) => void;
  onView: (id: string) => void;
  onEdit: () => void;
  onDelete: () => void;
  onUnlock: () => void;
}) {
  const w = v.workspace;
  const plan = v.latest_plan;
  const pending = plan && plan.status === "succeeded" && plan.summary?.changes && !plan.applied;
  const running = !!v.active_run;
  return (
    <Card>
      <CardHeader className="flex-row flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <CardTitle className="flex flex-wrap items-center gap-2">
            {w.name}
            {w.source === "git" ? (
              <Badge variant="outline" className="gap-1">
                <GitBranch className="size-3" /> git
              </Badge>
            ) : (
              <Badge variant="outline" className="gap-1">
                <FileCode2 className="size-3" /> {Object.keys(w.files).length} file(s)
              </Badge>
            )}
            {v.locked && (
              <Badge variant="warning" className="gap-1">
                <Lock className="size-3" /> locked
              </Badge>
            )}
            {running && <Badge variant="warning">running</Badge>}
          </CardTitle>
          <CardDescription className="truncate">{w.source === "git" ? `${w.git_url}${w.git_path ? ` /${w.git_path}` : ""}${w.git_ref ? ` @ ${w.git_ref}` : ""}` : w.description}</CardDescription>
        </div>
        {isAdmin(role) && (
          <div className="flex gap-1">
            {v.locked && !running && (
              <Button size="icon" variant="ghost" className="size-8" title="Force unlock" aria-label="Force unlock" onClick={onUnlock}>
                <Unlock className="size-4" />
              </Button>
            )}
            <Button size="icon" variant="ghost" className="size-8" aria-label={`Edit ${w.name}`} onClick={onEdit}>
              <Pencil className="size-4" />
            </Button>
            <Button size="icon" variant="ghost" className="size-8 text-destructive" aria-label={`Delete ${w.name}`} onClick={onDelete}>
              <Trash2 className="size-4" />
            </Button>
          </div>
        )}
      </CardHeader>
      <CardContent className="grid gap-3 text-sm">
        <div className="grid grid-cols-3 gap-2 text-xs text-muted-foreground">
          <div>
            <div>Resources</div>
            <div className="tabular text-base font-semibold text-foreground">{v.state?.resources?.length ?? 0}</div>
          </div>
          <div>
            <div>State serial</div>
            <div className="tabular text-base font-semibold text-foreground">{v.state ? v.state.serial : "—"}</div>
          </div>
          <div>
            <div>Variables</div>
            <div className="tabular text-base font-semibold text-foreground">{v.vars.length}</div>
          </div>
        </div>
        {v.state?.resources && v.state.resources.length > 0 && (
          <ul className="flex flex-wrap gap-1">
            {v.state.resources.slice(0, 12).map((r) => (
              <li key={r} className="rounded border px-1.5 py-0.5 font-mono text-[11px]">
                {r}
              </li>
            ))}
            {v.state.resources.length > 12 && <li className="text-xs text-muted-foreground">+{v.state.resources.length - 12} more</li>}
          </ul>
        )}
        {v.vars_error && <p className="text-xs text-destructive">{v.vars_error}</p>}
        {pending && plan && (
          <button
            type="button"
            onClick={() => onView(plan.id)}
            className={`cursor-pointer rounded-lg border px-3 py-2 text-left text-xs ${plan.kind === "destroy-plan" ? "border-destructive/30 bg-destructive/10 text-destructive" : "border-warning/30 bg-warning/10 text-warning"}`}
          >
            {plan.kind === "destroy-plan" ? "Destroy plan" : "Plan"} ready to apply: {runSummary(plan)} · {timeAgo(plan.created_at)}. Review and apply →
          </button>
        )}
        {canOperate(role) && (
          <div className="flex flex-wrap gap-2">
            <Button size="sm" variant="brand" disabled={running} onClick={() => onPlan(false)}>
              <Play /> Plan
            </Button>
            {isAdmin(role) && (
              <Button size="sm" variant="outline" className="text-destructive" disabled={running || !v.state?.resources?.length} onClick={() => onPlan(true)}>
                <Zap /> Plan destroy
              </Button>
            )}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

type EditVar = TofuVar & { has_value?: boolean; keep?: boolean };

const TEMPLATE = `terraform {
  required_providers {
    # e.g. bpg/proxmox, hashicorp/aws, hetznercloud/hcloud …
  }
}

variable "servermgr_servers" {
  type    = map(object({ id = string, address = string, os = string }))
  default = {}
}
`;

function EditDialog({ view, onClose, onSaved }: { view: TofuWorkspaceView | null; onClose: () => void; onSaved: () => void }) {
  const w = view?.workspace;
  const [name, setName] = useState(w?.name ?? "");
  const [desc, setDesc] = useState(w?.description ?? "");
  const [source, setSource] = useState<"inline" | "git">(w?.source ?? "inline");
  const [files, setFiles] = useState<Record<string, string>>(w?.files && Object.keys(w.files).length ? w.files : { "main.tf": TEMPLATE });
  const [cur, setCur] = useState(Object.keys(files).sort()[0] ?? "main.tf");
  const [git, setGit] = useState({ url: w?.git_url ?? "", ref: w?.git_ref ?? "", path: w?.git_path ?? "" });
  const [vars, setVars] = useState<EditVar[]>((view?.vars ?? []).map((v) => ({ ...v, keep: v.sensitive && v.has_value })));
  const [err, setErr] = useState("");
  const [saving, setSaving] = useState(false);

  const fileNames = Object.keys(files).sort();
  const setVar = (i: number, p: Partial<EditVar>) => setVars(vars.map((v, j) => (j === i ? { ...v, ...p } : v)));

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[94svh] overflow-y-auto sm:max-w-4xl">
        <DialogHeader>
          <DialogTitle>{w ? `Edit ${w.name}` : "New workspace"}</DialogTitle>
          <DialogDescription>Don't declare a backend: servermgr stores the state (encrypted) and handles locking.</DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={async (e) => {
            e.preventDefault();
            setSaving(true);
            setErr("");
            try {
              await api.saveTofuWorkspace(w?.id ?? "", {
                name,
                description: desc,
                source,
                files: source === "inline" ? files : {},
                git_url: git.url,
                git_ref: git.ref,
                git_path: git.path,
                vars: vars.map(({ key, value, kind, sensitive, keep }) => ({ key, value, kind, sensitive, keep: !!keep && !value })),
              });
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
              <Label htmlFor="tw-name">Name</Label>
              <Input id="tw-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="proxmox-lab" pattern="[a-z0-9][a-z0-9\-]*" required />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="tw-desc">Description</Label>
              <Input id="tw-desc" value={desc} onChange={(e) => setDesc(e.target.value)} maxLength={500} />
            </div>
          </div>

          <div className="flex gap-1 rounded-lg border p-1 text-sm">
            {(["inline", "git"] as const).map((s) => (
              <button
                key={s}
                type="button"
                className={`flex-1 cursor-pointer rounded-md px-3 py-1.5 ${source === s ? "bg-accent font-medium" : "text-muted-foreground"}`}
                onClick={() => setSource(s)}
              >
                {s === "inline" ? "Files in the panel" : "From a git repository"}
              </button>
            ))}
          </div>

          {source === "inline" ? (
            <div className="grid gap-2">
              <div className="flex flex-wrap items-center gap-1">
                {fileNames.map((f) => (
                  <span key={f} className={`flex items-center rounded-md border text-xs ${f === cur ? "bg-accent" : ""}`}>
                    <button type="button" className="cursor-pointer px-2 py-1 font-mono" onClick={() => setCur(f)}>
                      {f}
                    </button>
                    {fileNames.length > 1 && (
                      <button
                        type="button"
                        className="cursor-pointer px-1 text-muted-foreground hover:text-destructive"
                        aria-label={`Remove ${f}`}
                        onClick={() => {
                          const { [f]: _, ...rest } = files;
                          setFiles(rest);
                          setCur(Object.keys(rest).sort()[0]);
                        }}
                      >
                        <X className="size-3" />
                      </button>
                    )}
                  </span>
                ))}
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  onClick={() => {
                    const n = prompt("File name (e.g. variables.tf, outputs.tf, cloud-init.yaml)");
                    if (n && !files[n]) {
                      setFiles({ ...files, [n]: "" });
                      setCur(n);
                    }
                  }}
                >
                  <Plus /> File
                </Button>
              </div>
              <Textarea
                className="min-h-80 font-mono text-xs"
                value={files[cur] ?? ""}
                onChange={(e) => setFiles({ ...files, [cur]: e.target.value })}
                spellCheck={false}
                aria-label={`Contents of ${cur}`}
              />
            </div>
          ) : (
            <div className="grid gap-3 sm:grid-cols-3">
              <div className="grid gap-1.5 sm:col-span-3">
                <Label htmlFor="tw-url">Repository (https, public)</Label>
                <Input id="tw-url" value={git.url} onChange={(e) => setGit({ ...git, url: e.target.value })} placeholder="https://github.com/acme/infra.git" required />
              </div>
              <div className="grid gap-1.5 sm:col-span-2">
                <Label htmlFor="tw-path">Directory (optional)</Label>
                <Input id="tw-path" value={git.path} onChange={(e) => setGit({ ...git, path: e.target.value })} placeholder="envs/lab" />
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="tw-ref">Branch or tag</Label>
                <Input id="tw-ref" value={git.ref} onChange={(e) => setGit({ ...git, ref: e.target.value })} placeholder="main" />
              </div>
            </div>
          )}

          <div className="grid gap-2">
            <div className="flex items-center justify-between">
              <Label>Variables</Label>
              <Button type="button" size="sm" variant="ghost" onClick={() => setVars([...vars, { key: "", value: "", kind: "tf", sensitive: false }])}>
                <Plus /> Variable
              </Button>
            </div>
            <p className="text-xs text-muted-foreground">
              Terraform variables are passed as <code>TF_VAR_name</code>; environment variables are for provider credentials (e.g.
              PROXMOX_VE_API_TOKEN). All values are encrypted; sensitive ones are never shown again and are masked in run output.
            </p>
            {vars.map((v, i) => (
              <div key={i} className="grid grid-cols-[7rem_1fr_1fr_auto_auto] items-center gap-2">
                <Select value={v.kind} onChange={(e) => setVar(i, { kind: e.target.value as TofuVar["kind"] })} aria-label="Kind">
                  <option value="tf">variable</option>
                  <option value="env">env</option>
                </Select>
                <Input className="font-mono text-xs" value={v.key} onChange={(e) => setVar(i, { key: e.target.value })} placeholder={v.kind === "env" ? "API_TOKEN" : "region"} aria-label="Name" required />
                <Input
                  className="font-mono text-xs"
                  type={v.sensitive ? "password" : "text"}
                  autoComplete="off"
                  value={v.value}
                  onChange={(e) => setVar(i, { value: e.target.value })}
                  placeholder={v.sensitive && v.has_value ? "•••• unchanged" : "value"}
                  aria-label="Value"
                />
                <label className="flex items-center gap-1 text-xs" title="Sensitive: write-only and masked in output">
                  <input type="checkbox" checked={v.sensitive} onChange={(e) => setVar(i, { sensitive: e.target.checked })} /> secret
                </label>
                <Button type="button" size="icon" variant="ghost" className="size-8" aria-label="Remove variable" onClick={() => setVars(vars.filter((_, j) => j !== i))}>
                  <X className="size-4" />
                </Button>
              </div>
            ))}
          </div>

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

function RunViewer({
  id,
  role,
  onClose,
  onOpen,
  onChange,
}: {
  id: string;
  role: ReturnType<typeof useRole>;
  onClose: () => void;
  onOpen: (id: string) => void;
  onChange: () => void;
}) {
  const [run, setRun] = useState<TofuRun | null>(null);
  const [out, setOut] = useState("");
  const [err, setErr] = useState("");
  const [confirm, setConfirm] = useState("");
  const pre = useRef<HTMLPreElement>(null);
  const stick = useRef(true);

  useEffect(() => {
    let alive = true;
    let offset = 0;
    let timer: ReturnType<typeof setTimeout>;
    setOut("");
    setRun(null);
    const poll = async () => {
      try {
        const g = await api.tofuRun(id, offset);
        if (!alive) return;
        offset = g.total;
        if (g.output) setOut((o) => o + g.output);
        setRun(g.run);
        if (g.run.status === "running") timer = setTimeout(poll, 1000);
        else onChange();
      } catch (e) {
        if (alive) setErr((e as Error).message);
      }
    };
    void poll();
    return () => {
      alive = false;
      clearTimeout(timer);
    };
  }, [id, onChange]);

  useEffect(() => {
    if (stick.current && pre.current) pre.current.scrollTop = pre.current.scrollHeight;
  }, [out]);

  const applicable = run && run.kind !== "apply" && run.status === "succeeded" && run.summary?.changes && !run.applied && canOperate(role);
  const destroy = run?.kind === "destroy-plan";
  const outputs = run?.kind === "apply" ? run.summary?.outputs : undefined;
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[94svh] overflow-y-auto sm:max-w-4xl">
        <DialogHeader>
          <DialogTitle className="flex flex-wrap items-center gap-2 pr-6">
            {run ? `${run.workspace_name}: ${kindLabel[run.kind]}` : "Run"}
            {run && <Badge variant={runVariant[run.status]}>{run.status}</Badge>}
            {run && runSummary(run) && <Badge variant="outline">{runSummary(run)}</Badge>}
          </DialogTitle>
          <DialogDescription>{run ? `Started by ${run.created_by} ${timeAgo(run.created_at)}` : ""}</DialogDescription>
        </DialogHeader>
        <pre
          ref={pre}
          onScroll={(e) => {
            const el = e.currentTarget;
            stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
          }}
          className="h-[50svh] overflow-auto rounded-lg bg-muted p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap"
        >
          {colorize(out)}
          {err && <span className="text-destructive">{"\n" + err}</span>}
        </pre>
        {outputs && Object.keys(outputs).length > 0 && (
          <div className="grid gap-1">
            <Label>Outputs</Label>
            <pre className="max-h-40 overflow-auto rounded-lg border p-2 text-xs">{JSON.stringify(outputs, null, 2)}</pre>
          </div>
        )}
        {run?.status === "running" && canOperate(role) && (
          <div className="flex justify-end">
            <Button variant="outline" className="text-destructive" onClick={() => api.cancelTofuRun(id).catch((e) => setErr((e as Error).message))}>
              <Square /> Cancel run
            </Button>
          </div>
        )}
        {applicable && (destroy ? isAdmin(role) : true) && (
          <form
            className={`grid gap-2 rounded-lg border p-3 ${destroy ? "border-destructive/40" : ""}`}
            onSubmit={async (e) => {
              e.preventDefault();
              setErr("");
              try {
                const r = await api.tofuApply(run.id, confirm);
                onOpen(r.id);
              } catch (e) {
                setErr((e as Error).message);
              }
            }}
          >
            <p className="text-sm">
              {destroy ? (
                <>
                  This destroys <b>{run.summary?.destroy}</b> resource(s). Type <code className="rounded bg-muted px-1">{run.workspace_name}</code> to confirm.
                </>
              ) : (
                <>Apply exactly this plan ({runSummary(run)}).</>
              )}
            </p>
            <div className="flex gap-2">
              {destroy && <Input value={confirm} onChange={(e) => setConfirm(e.target.value)} placeholder={run.workspace_name} aria-label="Confirm workspace name" />}
              <Button type="submit" variant={destroy ? "destructive" : "brand"} disabled={destroy && confirm !== run.workspace_name}>
                <Play /> {destroy ? "Destroy" : "Apply"}
              </Button>
            </div>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}

function colorize(text: string) {
  return text.split("\n").map((line, i) => {
    let cls = "";
    const t = line.trimStart();
    if (/^(Error|│ Error)/.test(t) || t.startsWith("- ") || / will be destroyed| must be replaced/.test(t)) cls = "text-destructive";
    else if (t.startsWith("+ ") || / will be created|Apply complete|Creation complete/.test(t)) cls = "text-success";
    else if (t.startsWith("~ ") || / will be updated in-place/.test(t)) cls = "text-warning";
    else if (/^(Plan:|No changes\.|\$ tofu)/.test(t)) cls = "font-semibold text-foreground";
    return (
      <span key={i} className={cls}>
        {line}
        {"\n"}
      </span>
    );
  });
}
