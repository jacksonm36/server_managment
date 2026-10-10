import { useState } from "react";
import { Box, Download, FileText, Layers, Pencil, Play, Plus, RefreshCw, RotateCw, Square, Trash2, Upload } from "lucide-react";
import { runOp, useAgentState } from "@/lib/ops";
import { formatBytes } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label } from "@/components/ui/input";
import { Textarea } from "@/components/ui/select";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { OpStatus, Offline } from "@/components/OpStatus";

type Container = { id: string; name: string; image: string; state: string; status: string; ports: string[]; project?: string };
type Image = { id: string; tags: string[]; size: number; created: number };
type App = { name: string; running: number; total: number };
type State = { installed: boolean; running: boolean; version?: string; error?: string; containers?: Container[]; images?: Image[]; apps?: App[] };

const SAMPLE = `services:
  web:
    image: nginx:1.27-alpine
    ports:
      - "8080:80"
    restart: unless-stopped
`;

export function Docker({ serverId, online }: { serverId: string; online: boolean }) {
  const { data, busy, error, job, run } = useAgentState<State>(serverId, "docker.status", online);
  const [logs, setLogs] = useState<{ name: string; text: string } | null>(null);
  const [editing, setEditing] = useState<{ name: string; yaml: string; isNew: boolean } | null>(null);
  const disabled = !online || !!busy;

  if (data && (!data.installed || !data.running)) {
    return (
      <Card className="flex flex-col items-center gap-3 p-10 text-center">
        <Box className="size-8 text-muted-foreground" />
        <div className="font-medium">{data.installed ? "Docker is installed but not running" : "Docker is not installed"}</div>
        {data.error && <p className="text-sm text-destructive">{data.error}</p>}
        <p className="max-w-sm text-sm text-muted-foreground">Installs Docker Engine and Docker Compose from the distribution's packages.</p>
        <Button variant="brand" disabled={disabled} onClick={() => run("docker.install")}>
          <Download /> {data.installed ? "Repair & start Docker" : "Install Docker"}
        </Button>
        <div className="w-full max-w-xl text-left">
          <OpStatus busy={busy} error={error} job={job} />
        </div>
      </Card>
    );
  }

  const act = (c: Container, action: string) => {
    if ((action === "remove" || action === "kill") && !confirm(`${action} container ${c.name}?`)) return;
    void run("docker.container", { id: c.name || c.id, action });
  };

  const showLogs = async (c: Container) => {
    setLogs({ name: c.name, text: "Loading…" });
    const j = await runOp(serverId, "docker.logs", { id: c.name || c.id, lines: "500" });
    setLogs({ name: c.name, text: j.status === "failed" ? j.error : (j.result as { text: string }).text || "(no output)" });
  };

  const openApp = async (name: string) => {
    const j = await runOp(serverId, "compose.read", { name });
    if (j.status === "failed") return alert(j.error);
    setEditing({ name, yaml: (j.result as { yaml: string }).yaml, isNew: false });
  };

  return (
    <div className="grid gap-4">
      <Card>
        <CardHeader className="flex-row flex-wrap items-start justify-between gap-3">
          <div>
            <CardTitle className="flex items-center gap-2">
              <Layers className="size-4 text-brand" /> Apps (Compose)
            </CardTitle>
            <CardDescription>Docker {data?.version} · compose files live in /srv/compose/&lt;app&gt;/compose.yaml</CardDescription>
          </div>
          <div className="flex gap-2">
            <Button variant="ghost" size="icon" disabled={disabled} onClick={() => run("docker.status")} aria-label="Refresh">
              <RefreshCw />
            </Button>
            <Button variant="brand" size="sm" disabled={disabled} onClick={() => setEditing({ name: "", yaml: SAMPLE, isNew: true })}>
              <Plus /> New app
            </Button>
          </div>
        </CardHeader>
        <CardContent className="grid gap-2">
          {!online && <Offline />}
          {data?.apps?.length === 0 && <p className="text-sm text-muted-foreground">No compose apps yet.</p>}
          {data?.apps?.map((a) => (
            <div key={a.name} className="flex flex-wrap items-center gap-3 rounded-lg border px-3 py-2">
              <span className="font-medium">{a.name}</span>
              <Badge variant={a.total === 0 ? "outline" : a.running === a.total ? "success" : "warning"}>
                {a.total === 0 ? "not deployed" : `${a.running}/${a.total} running`}
              </Badge>
              <div className="ml-auto flex gap-1">
                <Button variant="ghost" size="sm" disabled={disabled} onClick={() => run("compose.action", { name: a.name, action: "up" })}>
                  <Play /> Up
                </Button>
                <Button variant="ghost" size="sm" disabled={disabled} onClick={() => run("compose.action", { name: a.name, action: "pull" })} title="Pull new images and recreate">
                  <Upload /> Update
                </Button>
                <Button variant="ghost" size="sm" disabled={disabled} onClick={() => run("compose.action", { name: a.name, action: "down" })}>
                  <Square /> Down
                </Button>
                <Button variant="ghost" size="icon" aria-label="Edit" disabled={disabled} onClick={() => openApp(a.name)}>
                  <Pencil />
                </Button>
                <Button
                  variant="ghost"
                  size="icon"
                  aria-label="Delete app"
                  disabled={disabled}
                  onClick={() => confirm(`Stop and delete app ${a.name}? Named volumes are kept.`) && run("compose.action", { name: a.name, action: "delete" })}
                >
                  <Trash2 className="text-destructive" />
                </Button>
              </div>
            </div>
          ))}
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="flex-row items-center justify-between gap-3">
          <CardTitle>Containers</CardTitle>
          <Button variant="ghost" size="sm" disabled={disabled} onClick={() => confirm("Remove stopped containers and dangling images?") && run("docker.prune")}>
            <Trash2 /> Prune
          </Button>
        </CardHeader>
        <CardContent className="grid gap-3">
          {data?.containers?.length === 0 && <p className="text-sm text-muted-foreground">No containers.</p>}
          {!!data?.containers?.length && (
            <div className="overflow-x-auto rounded-lg border">
              <table className="w-full text-sm">
                <thead className="text-left text-xs text-muted-foreground">
                  <tr>
                    <th className="px-3 py-2 font-medium">Name</th>
                    <th className="px-3 py-2 font-medium">Image</th>
                    <th className="px-3 py-2 font-medium">State</th>
                    <th className="hidden px-3 py-2 font-medium md:table-cell">Ports</th>
                    <th className="px-3 py-2" />
                  </tr>
                </thead>
                <tbody>
                  {data.containers.map((c) => (
                    <tr key={c.id} className="border-t">
                      <td className="px-3 py-1.5">
                        <div className="font-mono text-xs">{c.name}</div>
                        {c.project && <div className="text-xs text-muted-foreground">app: {c.project}</div>}
                      </td>
                      <td className="max-w-48 truncate px-3 py-1.5 font-mono text-xs text-muted-foreground">{c.image}</td>
                      <td className="px-3 py-1.5">
                        <Badge variant={c.state === "running" ? "success" : c.state === "exited" ? "outline" : "warning"} title={c.status}>
                          {c.state}
                        </Badge>
                      </td>
                      <td className="hidden px-3 py-1.5 font-mono text-xs text-muted-foreground md:table-cell">{c.ports.join(", ") || "—"}</td>
                      <td className="px-1 py-1 text-right whitespace-nowrap">
                        {c.state === "running" ? (
                          <>
                            <Button variant="ghost" size="icon" className="size-8" title="Restart" aria-label="Restart" disabled={disabled} onClick={() => act(c, "restart")}>
                              <RotateCw />
                            </Button>
                            <Button variant="ghost" size="icon" className="size-8" title="Stop" aria-label="Stop" disabled={disabled} onClick={() => act(c, "stop")}>
                              <Square />
                            </Button>
                          </>
                        ) : (
                          <Button variant="ghost" size="icon" className="size-8" title="Start" aria-label="Start" disabled={disabled} onClick={() => act(c, "start")}>
                            <Play className="text-success" />
                          </Button>
                        )}
                        <Button variant="ghost" size="icon" className="size-8" title="Logs" aria-label="Logs" onClick={() => showLogs(c)}>
                          <FileText />
                        </Button>
                        <Button variant="ghost" size="icon" className="size-8" title="Remove" aria-label="Remove" disabled={disabled} onClick={() => act(c, "remove")}>
                          <Trash2 className="text-destructive" />
                        </Button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {logs && (
            <div className="grid gap-2">
              <div className="flex items-center justify-between text-sm">
                <span className="font-medium">Logs: {logs.name}</span>
                <Button variant="ghost" size="sm" onClick={() => setLogs(null)}>
                  Close
                </Button>
              </div>
              <pre className="max-h-96 overflow-auto rounded-lg border bg-[#0c0e12] p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap text-[#d4d4d4]">{logs.text}</pre>
            </div>
          )}
          <OpStatus busy={busy} error={error} job={job} />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Images</CardTitle>
        </CardHeader>
        <CardContent>
          {data?.images?.length === 0 ? (
            <p className="text-sm text-muted-foreground">No images.</p>
          ) : (
            <ul className="grid gap-1.5">
              {data?.images?.map((im) => (
                <li key={im.id} className="flex items-center gap-3 rounded-lg border px-3 py-1.5 text-sm">
                  <span className="min-w-0 flex-1 truncate font-mono text-xs">{im.tags.join(", ") || `<none> ${im.id}`}</span>
                  <span className="text-xs text-muted-foreground tabular">{formatBytes(im.size)}</span>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="size-8"
                    aria-label="Remove image"
                    disabled={disabled}
                    onClick={() => confirm(`Remove image ${im.tags[0] ?? im.id}?`) && run("docker.image_remove", { id: im.id })}
                  >
                    <Trash2 className="text-destructive" />
                  </Button>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>

      {editing && (
        <ComposeDialog
          initial={editing}
          busy={!!busy}
          onClose={() => setEditing(null)}
          onSave={async (name, yaml, deploy) => {
            const j = await run("compose.save", { name, yaml, deploy: String(deploy) });
            if (j) setEditing(null);
          }}
          error={error}
        />
      )}
    </div>
  );
}

function ComposeDialog(props: {
  initial: { name: string; yaml: string; isNew: boolean };
  busy: boolean;
  error: string;
  onClose: () => void;
  onSave: (name: string, yaml: string, deploy: boolean) => void;
}) {
  const [name, setName] = useState(props.initial.name);
  const [yaml, setYaml] = useState(props.initial.yaml);
  return (
    <Dialog open onOpenChange={(o) => !o && props.onClose()}>
      <DialogContent className="max-h-[90svh] max-w-3xl overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{props.initial.isNew ? "New compose app" : `Edit ${props.initial.name}`}</DialogTitle>
          <DialogDescription>The file is validated with docker compose before it is saved. Env values here are stored on the server only.</DialogDescription>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="grid gap-2">
            <Label htmlFor="app-name">App name</Label>
            <Input id="app-name" className="font-mono" value={name} disabled={!props.initial.isNew} onChange={(e) => setName(e.target.value.toLowerCase())} placeholder="blog" />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="app-yaml">compose.yaml</Label>
            <Textarea id="app-yaml" rows={18} spellCheck={false} className="font-mono text-xs leading-relaxed" value={yaml} onChange={(e) => setYaml(e.target.value)} />
          </div>
          {props.error && <p className="text-sm text-destructive">{props.error}</p>}
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={props.onClose}>
              Cancel
            </Button>
            <Button variant="secondary" disabled={props.busy || !name} onClick={() => props.onSave(name, yaml, false)}>
              Save
            </Button>
            <Button variant="brand" disabled={props.busy || !name} onClick={() => props.onSave(name, yaml, true)}>
              <Play /> Save & deploy
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
