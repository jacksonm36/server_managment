import { useMemo } from "react";
import { Link } from "wouter";
import { Cpu, HardDrive, MemoryStick, ServerOff } from "lucide-react";
import { useLive } from "@/lib/live";
import type { Server } from "@/lib/api";
import { formatBytes, formatUptime, pct, timeAgo } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Meter } from "@/components/LiveChart";
import { AddServerDialog } from "@/components/AddServerDialog";

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

  return (
    <div className="grid gap-6">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Servers</h1>
          <p className="text-sm text-muted-foreground">
            {servers.length} total · {online} online
          </p>
        </div>
        <AddServerDialog />
      </div>

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
            <ServerCard key={s.id} s={s} />
          ))}
        </div>
      )}
    </div>
  );
}

function ServerCard({ s }: { s: Server }) {
  const m = s.online ? s.latest : null;
  return (
    <Link href={`/servers/${s.id}`} className="group block rounded-xl outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50">
      <Card className="h-full p-5 transition-colors group-hover:border-brand/40 group-hover:bg-accent/30">
        <div className="mb-4 flex items-start justify-between gap-3">
          <div className="min-w-0">
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
