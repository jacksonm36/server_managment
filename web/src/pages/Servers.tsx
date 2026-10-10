import { useMemo, useState } from "react";
import { Link } from "wouter";
import { Cpu, HardDrive, MemoryStick, Play, ServerOff, X } from "lucide-react";
import { useLive } from "@/lib/live";
import type { Server } from "@/lib/api";
import { formatBytes, formatUptime, pct, timeAgo } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Meter } from "@/components/LiveChart";
import { AddServerDialog } from "@/components/AddServerDialog";
import { BulkDialog } from "@/components/BulkDialog";
import { Button } from "@/components/ui/button";

export function StatusBadge({ online }: { online: boolean }) {
  return online ? (
    <Badge variant="success">
      <span className="relative flex size-2">
        <span className="absolute inline-flex size-full animate-ping rounded-full bg-success opacity-60" />
        <span className="relative inline-flex size-2 rounded-full bg-success" />
      </span>
      Online
    </Badge>
  ) : (
    <Badge variant="outline">
      <span className="size-2 rounded-full bg-muted-foreground/60" />
      Offline
    </Badge>
  );
}

export function Servers() {
  const map = useLive((s) => s.servers);
  const servers = useMemo(() => [...map.values()].sort((a, b) => a.name.localeCompare(b.name)), [map]);
  const online = servers.filter((s) => s.online).length;
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [bulk, setBulk] = useState(false);
  const picked = servers.filter((s) => selected.has(s.id));
  const toggle = (id: string) =>
    setSelected((cur) => {
      const n = new Set(cur);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });

  return (
    <div className="grid gap-6">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Servers</h1>
          <p className="text-sm text-muted-foreground">
            {servers.length} total · {online} online
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {servers.length > 1 && (
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setSelected(selected.size === servers.length ? new Set() : new Set(servers.map((s) => s.id)))}
            >
              {selected.size === servers.length ? "Clear selection" : "Select all"}
            </Button>
          )}
          <AddServerDialog />
        </div>
      </div>

      {picked.length > 0 && (
        <div className="sticky top-16 z-30 flex flex-wrap items-center justify-between gap-3 rounded-xl border border-brand/40 bg-card/95 px-4 py-2.5 shadow-lg backdrop-blur">
          <span className="text-sm">
            <span className="font-semibold">{picked.length}</span> selected
            {picked.some((s) => !s.online) && <span className="text-muted-foreground"> · offline servers will be skipped</span>}
          </span>
          <div className="flex gap-2">
            <Button variant="ghost" size="sm" onClick={() => setSelected(new Set())}>
              <X /> Clear
            </Button>
            <Button variant="brand" size="sm" onClick={() => setBulk(true)}>
              <Play /> Run action
            </Button>
          </div>
        </div>
      )}
      {bulk && <BulkDialog servers={picked} onClose={() => setBulk(false)} />}

      {servers.length === 0 ? (
        <Card className="flex flex-col items-center gap-3 p-12 text-center">
          <ServerOff className="size-8 text-muted-foreground" />
          <div className="font-medium">No servers yet</div>
          <p className="max-w-sm text-sm text-muted-foreground">
            Click “Add server” to get a one-line install command for your first server.
          </p>
        </Card>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
          {servers.map((s) => (
            <ServerCard key={s.id} s={s} selected={selected.has(s.id)} onToggle={() => toggle(s.id)} />
          ))}
        </div>
      )}
    </div>
  );
}

function ServerCard({ s, selected, onToggle }: { s: Server; selected: boolean; onToggle: () => void }) {
  const m = s.online ? s.latest : null;
  return (
    <Link href={`/servers/${s.id}`} className="group block rounded-xl outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50">
      <Card
        className={`h-full p-5 transition-colors group-hover:border-brand/40 group-hover:bg-accent/30 ${selected ? "border-brand/70 bg-brand/5" : ""}`}
      >
        <div className="mb-4 flex items-start justify-between gap-3">
          <input
            type="checkbox"
            aria-label={`Select ${s.name}`}
            className="mt-1 size-4 shrink-0 cursor-pointer accent-[var(--color-brand)]"
            checked={selected}
            onClick={(e) => e.stopPropagation()}
            onChange={(e) => {
              e.stopPropagation();
              onToggle();
            }}
          />
          <div className="min-w-0 flex-1">
            <div className="truncate font-semibold">{s.name}</div>
            <div className="truncate text-xs text-muted-foreground">
              {s.os_pretty || "unknown OS"} · {s.hostname}
            </div>
          </div>
          <StatusBadge online={s.online} />
        </div>
        {m ? (
          <div className="grid gap-3 text-sm">
            <Row icon={<Cpu />} label="CPU" value={`${m.cpu.toFixed(1)}%`} pctv={m.cpu} />
            <Row
              icon={<MemoryStick />}
              label="RAM"
              value={`${formatBytes(m.mem_used)} / ${formatBytes(m.mem_total)}`}
              pctv={pct(m.mem_used, m.mem_total)}
            />
            <Row
              icon={<HardDrive />}
              label="Disk"
              value={`${formatBytes(m.disk_used)} / ${formatBytes(m.disk_total)}`}
              pctv={pct(m.disk_used, m.disk_total)}
            />
            <div className="tabular flex justify-between pt-1 text-xs text-muted-foreground">
              <span>load {m.load1.toFixed(2)}</span>
              <span>up {formatUptime(m.uptime)}</span>
            </div>
          </div>
        ) : (
          <div className="text-sm text-muted-foreground">
            {s.online ? "Waiting for metrics…" : `Last seen ${timeAgo(s.last_seen_at)}`}
          </div>
        )}
      </Card>
    </Link>
  );
}

function Row({ icon, label, value, pctv }: { icon: React.ReactNode; label: string; value: string; pctv: number }) {
  return (
    <div className="grid gap-1.5">
      <div className="flex items-center justify-between gap-2">
        <span className="flex items-center gap-1.5 text-muted-foreground [&_svg]:size-3.5">
          {icon}
          {label}
        </span>
        <span className="tabular text-xs">{value}</span>
      </div>
      <Meter value={pctv} />
    </div>
  );
}
