import { useEffect, useRef, useState } from "react";
import {
  ArrowUp,
  ChevronRight,
  Download,
  File,
  FilePlus,
  Folder,
  FolderPlus,
  Link2,
  Pencil,
  RefreshCw,
  Save,
  Trash2,
  Upload,
  X,
} from "lucide-react";
import { runOp } from "@/lib/ops";
import { formatBytes, timeAgo } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Textarea } from "@/components/ui/select";
import { Offline } from "@/components/OpStatus";

type Entry = { name: string; type: "file" | "dir" | "link" | "other"; size: number; mode: string; mtime: number; owner: string; target?: string };
type Listing = { path: string; root: string; entries: Entry[] };
type Opened = { path: string; text?: string; base64?: string; size: number };

const MAX_UPLOAD = 2 << 20;

// Windows paths: "" is "This PC" (the drives), then C:\, C:\Users…
const winJoin = (dir: string, name: string) => (dir === "" ? `${name}\\` : `${dir.replace(/\\$/, "")}\\${name}`);
const winParent = (p: string) => (/^[A-Za-z]:\\?$/.test(p) ? "" : p.replace(/\\[^\\]+\\?$/, "").replace(/^([A-Za-z]:)$/, "$1\\"));

export function Files({ serverId, online, windows = false }: { serverId: string; online: boolean; windows?: boolean }) {
  const home = windows ? "" : "/srv/sites";
  const [list, setList] = useState<Listing | null>(null);
  const [path, setPath] = useState(home);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [open, setOpen] = useState<Opened | null>(null);
  const [draft, setDraft] = useState("");
  const upload = useRef<HTMLInputElement>(null);

  async function op<T>(name: string, params: Record<string, string>): Promise<T | null> {
    setBusy(true);
    setError("");
    try {
      const j = await runOp(serverId, name, params);
      if (j.status === "failed") throw new Error(j.error || "failed");
      return j.result as T;
    } catch (e) {
      setError((e as Error).message);
      return null;
    } finally {
      setBusy(false);
    }
  }

  async function load(p = path) {
    const r = await op<Listing>("files.list", { path: p });
    if (r) {
      setList(r);
      setPath(r.path);
    }
  }

  useEffect(() => {
    if (online) void load(home);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [serverId, online]);

  const join = (name: string) => (windows ? winJoin(path, name) : `${path.replace(/\/$/, "")}/${name}`);
  const disabled = !online || busy;
  const atRoot = list ? path === list.root : true;
  // On Windows, files can be created anywhere below a drive.
  const canCreate = !atRoot;

  async function openFile(e: Entry) {
    const r = await op<Opened>("files.read", { path: join(e.name) });
    if (r) {
      setOpen(r);
      setDraft(r.text ?? "");
    }
  }

  function download(o: Opened) {
    const bytes = o.base64 ? Uint8Array.from(atob(o.base64), (c) => c.charCodeAt(0)) : new TextEncoder().encode(o.text ?? "");
    const url = URL.createObjectURL(new Blob([bytes]));
    const a = document.createElement("a");
    a.href = url;
    a.download = o.path.split(/[\\/]/).pop() ?? "file";
    a.click();
    URL.revokeObjectURL(url);
  }

  async function onUpload(files: FileList | null) {
    if (!files) return;
    for (const f of Array.from(files)) {
      if (f.size > MAX_UPLOAD) {
        setError(`${f.name} is larger than 2 MiB; use SFTP for big files.`);
        continue;
      }
      const buf = new Uint8Array(await f.arrayBuffer());
      let bin = "";
      for (let i = 0; i < buf.length; i += 0x8000) bin += String.fromCharCode(...buf.subarray(i, i + 0x8000));
      const exists = list?.entries.some((e) => e.name === f.name);
      if (exists && !confirm(`${f.name} exists. Overwrite?`)) continue;
      await op("files.write", { path: join(f.name), content_b64: btoa(bin), overwrite: String(!!exists) });
    }
    if (upload.current) upload.current.value = "";
    await load();
  }

  const crumbs = windows ? path.split("\\").filter(Boolean) : path.split("/").filter(Boolean);
  const crumbPath = (i: number) => (windows ? (i === 0 ? `${crumbs[0]}\\` : crumbs.slice(0, i + 1).join("\\")) : "/" + crumbs.slice(0, i + 1).join("/"));

  return (
    <div className="grid gap-4">
      <Card>
        <CardHeader className="gap-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <CardTitle>Files</CardTitle>
            <div className="flex flex-wrap gap-1">
              <Button variant="ghost" size="sm" disabled={disabled || atRoot} onClick={() => load(windows ? winParent(path) : path.replace(/\/[^/]+$/, "") || "/")}>
                <ArrowUp /> Up
              </Button>
              <Button variant="ghost" size="sm" disabled={disabled} onClick={() => load()} aria-label="Refresh">
                <RefreshCw className={busy ? "animate-spin" : ""} />
              </Button>
              <Button
                variant="secondary"
                size="sm"
                disabled={disabled || !canCreate}
                onClick={async () => {
                  const n = prompt("New folder name");
                  if (n && (await op("files.mkdir", { path: join(n) })) !== null) await load();
                }}
              >
                <FolderPlus /> Folder
              </Button>
              <Button
                variant="secondary"
                size="sm"
                disabled={disabled || !canCreate}
                onClick={async () => {
                  const n = prompt("New file name");
                  if (n && (await op("files.write", { path: join(n), content: "" })) !== null) {
                    await load();
                    setOpen({ path: join(n), text: "", size: 0 });
                    setDraft("");
                  }
                }}
              >
                <FilePlus /> File
              </Button>
              <Button variant="brand" size="sm" disabled={disabled || !canCreate} onClick={() => upload.current?.click()}>
                <Upload /> Upload
              </Button>
              <input ref={upload} type="file" multiple hidden onChange={(e) => onUpload(e.target.files)} />
            </div>
          </div>
          <nav className="flex flex-wrap items-center gap-0.5 font-mono text-xs text-muted-foreground" aria-label="Path">
            {windows && (
              <button disabled={disabled || path === ""} onClick={() => load("")} className="cursor-pointer rounded px-1 hover:text-foreground disabled:cursor-default disabled:hover:text-muted-foreground">
                This PC
              </button>
            )}
            {crumbs.map((c, i) => {
              const p = crumbPath(i);
              const clickable = list && p.length >= list.root.length;
              return (
                <span key={p} className="flex items-center gap-0.5">
                  {(i > 0 || windows) && <ChevronRight className="size-3" />}
                  <button disabled={!clickable || disabled} onClick={() => load(p)} className="cursor-pointer rounded px-1 hover:text-foreground disabled:cursor-default disabled:hover:text-muted-foreground">
                    {c}
                  </button>
                </span>
              );
            })}
          </nav>
        </CardHeader>
        <CardContent className="grid gap-3">
          {!online && <Offline />}
          {error && <p className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">{error}</p>}
          {atRoot && list && list.entries.length === 0 && (
            <p className="text-sm text-muted-foreground">{windows ? "No drives found." : "No sites yet. Create one on the Sites tab."}</p>
          )}
          {list && list.entries.length > 0 && (
            <div className="overflow-x-auto rounded-lg border">
              <table className="w-full text-sm">
                <thead className="text-left text-xs text-muted-foreground">
                  <tr>
                    <th className="px-3 py-2 font-medium">Name</th>
                    <th className="hidden px-3 py-2 font-medium sm:table-cell">Size</th>
                    <th className="hidden px-3 py-2 font-medium md:table-cell">Modified</th>
                    <th className="hidden px-3 py-2 font-medium md:table-cell">Owner</th>
                    <th className="px-3 py-2 font-medium">{windows ? "Attributes" : "Mode"}</th>
                    <th className="px-3 py-2" />
                  </tr>
                </thead>
                <tbody>
                  {list.entries.map((e) => (
                    <tr key={e.name} className="group border-t hover:bg-accent/30">
                      <td className="px-3 py-1.5">
                        <button
                          className="flex cursor-pointer items-center gap-2 text-left hover:text-brand disabled:cursor-default"
                          disabled={disabled || e.type === "other"}
                          onClick={() => (e.type === "dir" ? load(join(e.name)) : e.type === "file" ? openFile(e) : load(join(e.name)))}
                        >
                          {e.type === "dir" ? (
                            <Folder className="size-4 shrink-0 text-brand" />
                          ) : e.type === "link" ? (
                            <Link2 className="size-4 shrink-0 text-muted-foreground" />
                          ) : (
                            <File className="size-4 shrink-0 text-muted-foreground" />
                          )}
                          <span className="truncate">{e.name}</span>
                          {e.target && <span className="truncate text-xs text-muted-foreground">→ {e.target}</span>}
                        </button>
                      </td>
                      <td className="hidden px-3 py-1.5 text-muted-foreground tabular sm:table-cell">{e.type === "file" || (atRoot && windows) ? formatBytes(e.size) : ""}</td>
                      <td className="hidden px-3 py-1.5 text-muted-foreground md:table-cell">{e.mtime ? timeAgo(new Date(e.mtime * 1000).toISOString()) : ""}</td>
                      <td className="hidden px-3 py-1.5 font-mono text-xs text-muted-foreground md:table-cell">{e.owner}</td>
                      <td className="px-3 py-1.5">
                        {windows ? (
                          <span className="font-mono text-xs text-muted-foreground" title="R read-only, H hidden, S system">
                            {e.mode}
                          </span>
                        ) : (
                        <button
                          className="cursor-pointer font-mono text-xs text-muted-foreground hover:text-foreground disabled:cursor-default"
                          disabled={disabled || atRoot}
                          title="Change permissions"
                          onClick={async () => {
                            const m = prompt(`Permissions for ${e.name} (octal)`, e.mode);
                            if (m && m !== e.mode && (await op("files.chmod", { path: join(e.name), mode: m })) !== null) await load();
                          }}
                        >
                          {e.mode}
                        </button>
                        )}
                      </td>
                      <td className="px-1 py-1 text-right whitespace-nowrap">
                        {!atRoot && (
                          <>
                            <Button
                              variant="ghost"
                              size="icon"
                              className="size-8"
                              aria-label={`Rename ${e.name}`}
                              disabled={disabled}
                              onClick={async () => {
                                const n = prompt("Rename to", e.name);
                                if (n && n !== e.name && (await op("files.rename", { from: join(e.name), to: join(n) })) !== null) await load();
                              }}
                            >
                              <Pencil />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon"
                              className="size-8"
                              aria-label={`Delete ${e.name}`}
                              disabled={disabled}
                              onClick={async () => {
                                if (!confirm(`Delete ${e.name}${e.type === "dir" ? " and everything in it" : ""}?`)) return;
                                if ((await op("files.delete", { path: join(e.name) })) !== null) await load();
                              }}
                            >
                              <Trash2 className="text-destructive" />
                            </Button>
                          </>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </CardContent>
      </Card>

      {open && (
        <Card>
          <CardHeader className="flex-row flex-wrap items-center justify-between gap-2">
            <CardTitle className="min-w-0 truncate font-mono text-sm">{open.path}</CardTitle>
            <div className="flex gap-1">
              <Button variant="ghost" size="sm" onClick={() => download(open)}>
                <Download /> Download
              </Button>
              {open.text !== undefined && (
                <Button
                  variant="brand"
                  size="sm"
                  disabled={disabled || draft === open.text}
                  onClick={async () => {
                    if ((await op("files.write", { path: open.path, content: draft, overwrite: "true" })) !== null) {
                      setOpen({ ...open, text: draft });
                      await load();
                    }
                  }}
                >
                  <Save /> Save
                </Button>
              )}
              <Button variant="ghost" size="icon" aria-label="Close" onClick={() => setOpen(null)}>
                <X />
              </Button>
            </div>
          </CardHeader>
          <CardContent>
            {open.text !== undefined ? (
              <Textarea
                className="min-h-96 font-mono text-xs leading-relaxed"
                spellCheck={false}
                value={draft}
                onChange={(e) => setDraft(e.target.value)}
                onKeyDown={(e) => {
                  if ((e.ctrlKey || e.metaKey) && e.key === "s") {
                    e.preventDefault();
                    if (draft !== open.text) void op("files.write", { path: open.path, content: draft, overwrite: "true" }).then((r) => r !== null && setOpen({ ...open, text: draft }));
                  }
                }}
              />
            ) : (
              <p className="text-sm text-muted-foreground">Binary file ({formatBytes(open.size)}). Use Download.</p>
            )}
          </CardContent>
        </Card>
      )}
    </div>
  );
}
