import { useCallback, useEffect, useRef, useState } from "react";
import { RefreshCw } from "lucide-react";
import { runOp } from "@/lib/ops";
import { formatBytes } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Offline } from "@/components/OpStatus";

type LogFile = { path: string; size: number; mtime: number };

export function Logs({ serverId, online, unit: initialUnit = "" }: { serverId: string; online: boolean; unit?: string }) {
  const [source, setSource] = useState<"journal" | "file">("journal");
  const [unit, setUnit] = useState(initialUnit);
  const [priority, setPriority] = useState("");
  const [since, setSince] = useState("1h");
  const [grep, setGrep] = useState("");
  const [lines, setLines] = useState("300");
  const [path, setPath] = useState("");
  const [files, setFiles] = useState<LogFile[]>([]);
  const [text, setText] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [follow, setFollow] = useState(false);
  const box = useRef<HTMLPreElement>(null);

  useEffect(() => setUnit(initialUnit), [initialUnit]);

  const load = useCallback(async () => {
    const p: Record<string, string> = { source, lines };
    if (grep.trim()) p.grep = grep.trim();
    if (source === "journal") {
      if (unit.trim()) p.unit = unit.trim().endsWith(".service") ? unit.trim() : `${unit.trim()}.service`;
      if (priority) p.priority = priority;
      if (since) p.since = since;
    } else {
      if (!path) return;
      p.path = path;
    }
    setBusy(true);
    setError("");
    try {
      const j = await runOp(serverId, "logs.read", p);
      if (j.status === "failed") throw new Error(j.error);
      setText((j.result as { text: string }).text || "(no entries)");
      requestAnimationFrame(() => box.current && (box.current.scrollTop = box.current.scrollHeight));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }, [serverId, source, unit, priority, since, grep, lines, path]);

  useEffect(() => {
    if (!online || source !== "file" || files.length) return;
    void runOp(serverId, "logs.read", { source: "files" }).then((j) => {
      const f = ((j.result as { files: LogFile[] }) ?? { files: [] }).files.sort((a, b) => b.mtime - a.mtime);
      setFiles(f);
      if (!path && f.length) setPath(f[0].path);
    });
  }, [online, source, serverId, files.length, path]);

  useEffect(() => {
    if (online) void load();
    // Reload when the source/target changes; text filters apply on Enter / Refresh.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [online, source, path, unit, priority, since]);

  useEffect(() => {
    if (!follow || !online) return;
    const t = setInterval(() => void load(), 5000);
    return () => clearInterval(t);
  }, [follow, online, load]);

  if (!online) return <Offline />;

  return (
    <Card>
      <CardHeader className="gap-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <CardTitle>Logs</CardTitle>
          <div className="flex items-center gap-2">
            <label className="flex cursor-pointer items-center gap-1.5 text-sm text-muted-foreground">
              <input type="checkbox" className="accent-[var(--color-brand)]" checked={follow} onChange={(e) => setFollow(e.target.checked)} />
              Follow (5 s)
            </label>
            <Button variant="ghost" size="icon" disabled={busy} onClick={() => load()} aria-label="Refresh">
              <RefreshCw className={busy ? "animate-spin" : ""} />
            </Button>
          </div>
        </div>
        <form
          className="flex flex-wrap gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            void load();
          }}
        >
          <Select className="w-32" value={source} onChange={(e) => setSource(e.target.value as "journal" | "file")} aria-label="Source">
            <option value="journal">Journal</option>
            <option value="file">Log file</option>
          </Select>
          {source === "journal" ? (
            <>
              <Input className="w-56 font-mono" placeholder="unit, e.g. nginx (all if empty)" value={unit} onChange={(e) => setUnit(e.target.value)} />
              <Select className="w-32" value={priority} onChange={(e) => setPriority(e.target.value)} aria-label="Priority">
                <option value="">Any level</option>
                <option value="err">Errors+</option>
                <option value="warning">Warnings+</option>
                <option value="info">Info+</option>
              </Select>
              <Select className="w-32" value={since} onChange={(e) => setSince(e.target.value)} aria-label="Since">
                <option value="15m">Last 15 min</option>
                <option value="1h">Last hour</option>
                <option value="6h">Last 6 h</option>
                <option value="24h">Last 24 h</option>
                <option value="7d">Last 7 days</option>
                <option value="boot">This boot</option>
                <option value="">All</option>
              </Select>
            </>
          ) : (
            <Select className="w-80 font-mono" value={path} onChange={(e) => setPath(e.target.value)} aria-label="File">
              {files.map((f) => (
                <option key={f.path} value={f.path}>
                  {f.path} ({formatBytes(f.size)})
                </option>
              ))}
            </Select>
          )}
          <Input className="w-48" placeholder="Search (Enter)" value={grep} onChange={(e) => setGrep(e.target.value)} />
          <Select className="w-28" value={lines} onChange={(e) => setLines(e.target.value)} aria-label="Lines">
            {["100", "300", "1000", "5000"].map((n) => (
              <option key={n} value={n}>
                {n} lines
              </option>
            ))}
          </Select>
        </form>
      </CardHeader>
      <CardContent>
        {error && <p className="mb-2 text-sm text-destructive">{error}</p>}
        <pre ref={box} className="h-[32rem] overflow-auto rounded-lg border bg-[#0c0e12] p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap text-[#d4d4d4]">
          {text}
        </pre>
      </CardContent>
    </Card>
  );
}
