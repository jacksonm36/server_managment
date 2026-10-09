import { useCallback, useEffect, useState } from "react";
import { Link } from "wouter";
import { CheckCircle2, RefreshCw, XCircle } from "lucide-react";
import { api, type AuditEntry, type JobSummary } from "@/lib/api";
import { useLive } from "@/lib/live";
import { timeAgo } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Select } from "@/components/ui/select";

const FILTERS = [
  { v: "", label: "All actions" },
  { v: "auth.", label: "Sign-ins & security" },
  { v: "job.", label: "Server operations" },
  { v: "server.", label: "Servers" },
];

export function Activity() {
  const [view, setView] = useState<"audit" | "jobs">("audit");
  return (
    <div className="grid gap-6">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Activity</h1>
          <p className="text-sm text-muted-foreground">Who did what, and every task run on your servers.</p>
        </div>
        <div className="flex rounded-lg border p-0.5 text-sm">
          {(["audit", "jobs"] as const).map((v) => (
            <button
              key={v}
              onClick={() => setView(v)}
              className={`cursor-pointer rounded-md px-3 py-1.5 ${view === v ? "bg-accent text-foreground" : "text-muted-foreground hover:text-foreground"}`}
            >
              {v === "audit" ? "Audit log" : "Task history"}
            </button>
          ))}
        </div>
      </div>
      {view === "audit" ? <AuditLog /> : <JobHistory />}
    </div>
  );
}

function describe(e: AuditEntry): string {
  const d = (e.detail ?? {}) as Record<string, unknown>;
  switch (e.action) {
    case "auth.login":
      return e.success ? "Signed in" : `Sign-in failed (${d.error ?? "error"})`;
    case "auth.logout":
      return "Signed out";
    case "job.create":
      return `Ran ${d.op}${d.error ? ` (failed: ${d.error})` : ""}`;
    case "server.delete":
      return "Removed server";
    case "server.join_token":
      return "Created a join token";
    case "server.enroll":
      return e.success ? "Agent enrolled" : `Enrollment refused (${d.error ?? "error"})`;
    default:
      return e.action;
  }
}

function paramsOf(e: AuditEntry): string {
  const p = (e.detail as { params?: Record<string, string> })?.params;
  if (!p) return "";
  return Object.entries(p)
    .map(([k, v]) => `${k}=${v.length > 48 ? v.slice(0, 45) + "…" : v}`)
    .join(" ");
}

function AuditLog() {
  const [filter, setFilter] = useState("");
  const [rows, setRows] = useState<AuditEntry[]>([]);
  const [more, setMore] = useState(false);
  const [loading, setLoading] = useState(false);
  const PAGE = 100;

  const load = useCallback(
    async (before = 0) => {
      setLoading(true);
      try {
        const page = await api.audit({ action: filter, before, limit: PAGE });
        setRows((r) => (before ? [...r, ...page] : page));
        setMore(page.length === PAGE);
      } finally {
        setLoading(false);
      }
    },
    [filter],
  );

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <Card className="overflow-hidden">
      <div className="flex flex-wrap items-center justify-between gap-3 border-b p-3">
        <div className="w-56">
          <Select value={filter} onChange={(e) => setFilter(e.target.value)} aria-label="Filter">
            {FILTERS.map((f) => (
              <option key={f.v} value={f.v}>
                {f.label}
              </option>
            ))}
          </Select>
        </div>
        <Button variant="ghost" size="sm" onClick={() => load()} disabled={loading}>
          <RefreshCw className={loading ? "animate-spin" : ""} /> Refresh
        </Button>
      </div>
      {rows.length === 0 ? (
        <p className="p-6 text-sm text-muted-foreground">{loading ? "Loading…" : "Nothing recorded yet."}</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="text-left text-xs text-muted-foreground">
              <tr>
                <th className="px-4 py-2 font-medium">When</th>
                <th className="px-4 py-2 font-medium">Who</th>
                <th className="px-4 py-2 font-medium">What</th>
                <th className="px-4 py-2 font-medium">Target</th>
                <th className="hidden px-4 py-2 font-medium md:table-cell">From</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((e) => (
                <tr key={e.id} className="border-t align-top">
                  <td className="px-4 py-2 whitespace-nowrap text-muted-foreground" title={new Date(e.ts).toLocaleString()}>
                    {timeAgo(e.ts)}
                  </td>
                  <td className="px-4 py-2">{e.actor || "—"}</td>
                  <td className="px-4 py-2">
                    <div className="flex items-center gap-1.5">
                      {e.success ? (
                        <CheckCircle2 className="size-3.5 shrink-0 text-success" />
                      ) : (
                        <XCircle className="size-3.5 shrink-0 text-destructive" />
                      )}
                      {describe(e)}
                    </div>
                    {paramsOf(e) && <div className="mt-0.5 font-mono text-xs break-all text-muted-foreground">{paramsOf(e)}</div>}
                  </td>
                  <td className="px-4 py-2">{e.target || "—"}</td>
                  <td className="hidden px-4 py-2 font-mono text-xs text-muted-foreground md:table-cell">{e.ip}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {more && (
        <div className="border-t p-3 text-center">
          <Button variant="ghost" size="sm" disabled={loading} onClick={() => load(rows[rows.length - 1].id)}>
            Load older
          </Button>
        </div>
      )}
    </Card>
  );
}

// Read-only ops (status checks, file browsing) are hidden by default.
const READ_ONLY = /(\.status|\.list|\.read|^system\.info|^packages\.check_updates)$/;

function JobHistory() {
  const [all, setRows] = useState<JobSummary[]>([]);
  const [showReads, setShowReads] = useState(false);
  const rows = showReads ? all : all.filter((j) => !READ_ONLY.test(j.op));
  const tick = useLive((s) => s.jobTick);

  useEffect(() => {
    api.recentJobs().then(setRows);
  }, [tick]);

  return (
    <Card className="overflow-hidden">
      <label className="flex cursor-pointer items-center gap-2 border-b p-3 text-sm text-muted-foreground">
        <input type="checkbox" className="accent-[var(--color-brand)]" checked={showReads} onChange={(e) => setShowReads(e.target.checked)} />
        Show status checks and file browsing
      </label>
      {rows.length === 0 ? (
        <p className="p-6 text-sm text-muted-foreground">No tasks yet.</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="text-left text-xs text-muted-foreground">
              <tr>
                <th className="px-4 py-2 font-medium">Started</th>
                <th className="px-4 py-2 font-medium">Server</th>
                <th className="px-4 py-2 font-medium">Task</th>
                <th className="px-4 py-2 font-medium">Status</th>
                <th className="hidden px-4 py-2 font-medium sm:table-cell">By</th>
                <th className="hidden px-4 py-2 font-medium md:table-cell">Duration</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((j) => (
                <tr key={j.id} className="border-t align-top">
                  <td className="px-4 py-2 whitespace-nowrap text-muted-foreground" title={new Date(j.created_at).toLocaleString()}>
                    {timeAgo(j.created_at)}
                  </td>
                  <td className="px-4 py-2">
                    <Link href={`/servers/${j.server_id}#tasks`} className="hover:text-brand">
                      {j.server_name}
                    </Link>
                  </td>
                  <td className="px-4 py-2">
                    <span className="font-mono text-xs">{j.op}</span>
                    {j.error && <div className="mt-0.5 text-xs text-destructive">{j.error}</div>}
                  </td>
                  <td className="px-4 py-2">
                    <Badge variant={j.status === "succeeded" ? "success" : j.status === "failed" ? "destructive" : "warning"}>
                      {j.status}
                    </Badge>
                  </td>
                  <td className="hidden px-4 py-2 sm:table-cell">{j.created_by || "—"}</td>
                  <td className="hidden px-4 py-2 text-muted-foreground tabular md:table-cell">
                    {j.finished_at ? `${((new Date(j.finished_at).getTime() - new Date(j.created_at).getTime()) / 1000).toFixed(1)}s` : "—"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}
