import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { ArchiveRestore, Download, Network, Pencil, Play, Plus, Power, RefreshCw, RotateCcw, RotateCw, Settings2, Shield, Square, TerminalSquare, Trash2, UsersRound } from "lucide-react";
import { api, type PVECluster, type PVEGuest, type PVEGuestRef, type PVEResources } from "@/lib/api";
import { setPending } from "@/lib/pending";
import { PowerBanner, type PowerState } from "@/components/PowerBanner";
import { pve, ranMsg, type Params, type PVETaskRow, type Row } from "@/lib/pveapi";
import { formatBytes } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Select, Textarea } from "@/components/ui/select";
import { GuestConsole } from "@/components/GuestConsole";
import { NodeGuests, NodeHeader, NodeRings, NodeSummary, useNodeStatus } from "@/components/PVENodeView";
import { Meter } from "@/components/LiveChart";
import { Check, Field, FormDialog } from "@/components/PVEForms";

// Platform admin views of a Proxmox cluster: its nodes (summary, shell,
// services, updates, network, disks, DNS/time, syslog, tasks), storage
// contents (upload, download, delete) and datacenter settings.

const errText = (e: unknown) => (e as Error).message;
const num = (v: unknown) => (typeof v === "number" ? v : Number(v ?? 0));
const str = (v: unknown) => (v === undefined || v === null ? "" : typeof v === "object" ? JSON.stringify(v) : String(v));

function useLoad<T>(f: () => Promise<T>, deps: unknown[]) {
  const [data, setData] = useState<T | null>(null);
  const [err, setErr] = useState("");
  const load = useCallback(() => f().then((d) => (setData(d), setErr(""))).catch((e) => setErr(errText(e))), deps);
  useEffect(() => {
    void load();
  }, [load]);
  return { data, err, load };
}

function Note({ msg }: { msg: { ok: boolean; text: string } | null }) {
  if (!msg) return null;
  return <p className={`text-sm ${msg.ok ? "text-success" : "text-destructive"}`}>{msg.text}</p>;
}

function Tabs<T extends string>({ tabs, cur, set }: { tabs: [T, string][]; cur: T; set: (t: T) => void }) {
  return (
    <nav className="-mx-1 flex gap-1 overflow-x-auto border-b px-1 text-sm">
      {tabs.map(([id, label]) => (
        <button
          key={id}
          onClick={() => set(id)}
          aria-current={cur === id ? "page" : undefined}
          className={`-mb-px cursor-pointer border-b-2 px-3 py-2 whitespace-nowrap ${cur === id ? "border-brand text-foreground" : "border-transparent text-muted-foreground hover:text-foreground"}`}
        >
          {label}
        </button>
      ))}
    </nav>
  );
}

// Table renders rows of objects with chosen columns.
function Table({ rows, cols, actions, empty = "Nothing here." }: { rows: Row[]; cols: [string, string, ((r: Row) => ReactNode)?][]; actions?: (r: Row) => ReactNode; empty?: string }) {
  if (rows.length === 0) return <p className="text-sm text-muted-foreground">{empty}</p>;
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead className="text-left text-xs text-muted-foreground">
          <tr>
            {cols.map(([k, label]) => (
              <th key={k} className="py-2 pr-3 font-normal whitespace-nowrap">
                {label}
              </th>
            ))}
            {actions && <th />}
          </tr>
        </thead>
        <tbody className="divide-y">
          {rows.map((r, i) => (
            <tr key={i} className="align-top">
              {cols.map(([k, , f]) => (
                <td key={k} className="max-w-[28rem] py-1.5 pr-3 break-words">
                  {f ? f(r) : str(r[k])}
                </td>
              ))}
              {actions && <td className="py-1 text-right whitespace-nowrap">{actions(r)}</td>}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function TaskLog({ cluster, node, upid, onClose }: { cluster: string; node: string; upid: string; onClose: () => void }) {
  const [log, setLog] = useState<string | null>(null);
  const [status, setStatus] = useState("");
  const [err, setErr] = useState("");
  useEffect(() => {
    let stop = false;
    const get = () =>
      pve
        .nodeTaskLog(cluster, node, upid)
        .then((l) => {
          setLog(l.log);
          const st = l.status?.status === "running" ? "running" : (l.status?.exitstatus ?? "");
          setStatus(st);
          if (st === "running" && !stop) setTimeout(get, 2000);
        })
        .catch((e) => setErr(errText(e)));
    void get();
    return () => {
      stop = true;
    };
  }, [cluster, node, upid]);
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            Task log {status && <Badge variant={status === "OK" ? "success" : status === "running" ? "warning" : "destructive"}>{status}</Badge>}
          </DialogTitle>
        </DialogHeader>
        {err && <p className="text-sm text-destructive">{err}</p>}
        <pre className="max-h-[60vh] overflow-auto rounded-md bg-muted p-3 font-mono text-xs whitespace-pre-wrap">{log ?? "Loading…"}</pre>
      </DialogContent>
    </Dialog>
  );
}

// ================= nodes =================

type NodeTab = "summary" | "guests" | "services" | "updates" | "network" | "disks" | "system" | "syslog" | "tasks";

// NodeAdmin is a node's page for platform admins: the node's header with
// power and bulk actions, shell and package upgrade, its usage, and tabs
// for its guests, services, updates, network, disks, DNS/time/hosts, the
// system log and tasks.
export function NodeAdmin({
  cluster,
  node,
  nodes,
  guests,
  onGuest,
}: {
  cluster: string;
  node: string;
  nodes: string[];
  guests: PVEGuest[];
  onGuest: (g: PVEGuest) => void;
}) {
  const [tab, setTab] = useState<NodeTab>("summary");
  const { st, err, tasks } = useNodeStatus(cluster, node, true);
  return (
    <div className="grid gap-4">
      <NodeHeader node={node} st={st} online actions={<NodeActions cluster={cluster} node={node} nodes={nodes} />} />
      {err && <p className="text-sm text-destructive">{err}</p>}
      {st && <NodeRings st={st} />}
      <Tabs<NodeTab>
        tabs={[
          ["summary", "Summary"],
          ["guests", `Guests (${guests.filter((g) => !g.template).length})`],
          ["services", "Services"],
          ["updates", "Updates"],
          ["network", "Network"],
          ["disks", "Disks"],
          ["system", "DNS & time"],
          ["syslog", "System log"],
          ["tasks", "Tasks"],
        ]}
        cur={tab}
        set={setTab}
      />
      <div key={`${node}-${tab}`}>
        {tab === "summary" && <NodeSummary cluster={cluster} node={node} guests={guests} onGuest={onGuest} tasks={tasks} />}
        {tab === "guests" && <NodeGuests guests={guests} onGuest={onGuest} limit={0} />}
        {tab === "services" && <NodeServices cluster={cluster} node={node} />}
        {tab === "updates" && <NodeUpdates cluster={cluster} node={node} />}
        {tab === "network" && <NodeNetwork cluster={cluster} node={node} />}
        {tab === "disks" && <NodeDisks cluster={cluster} node={node} />}
        {tab === "system" && <NodeSystem cluster={cluster} node={node} />}
        {tab === "syslog" && <NodeSyslog cluster={cluster} node={node} />}
        {tab === "tasks" && <NodeTasks cluster={cluster} node={node} />}
      </div>
    </div>
  );
}

// NodeShell opens the node's shell (or package upgrade) in the console.
function NodeShell({ cluster, node, cmd, onClose }: { cluster: string; node: string; cmd: "" | "upgrade"; onClose: () => void }) {
  const ref = useMemo<PVEGuestRef>(() => ({ node, type: "lxc", vmid: 0 }), [node]);
  return <GuestConsole cluster={cluster} guest={ref} name={cmd ? `${node} · upgrade` : `${node} · shell`} open={() => pve.shell(cluster, node, cmd)} onClose={onClose} />;
}

function NodeActions({ cluster, node, nodes }: { cluster: string; node: string; nodes: string[] }) {
  const [shell, setShell] = useState<"" | "upgrade" | null>(null);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [power, setPower] = useState<"reboot" | "shutdown" | null>(null);
  const [migrateAll, setMigrateAll] = useState(false);
  const [pw, setPw] = useState<PowerState>(null);
  const clearPw = useCallback(() => setPw(null), []);
  const [hint, setHint] = useState("");
  const bulk = async (action: "startall" | "stopall") => {
    if (!confirm(action === "startall" ? `Start all guests on ${node} that are set to start on boot?` : `Shut down all guests on ${node}?`)) return;
    setMsg(null);
    setPending(node, action);
    setPw({ action, phase: "running", started: Date.now() });
    try {
      const r = await pve.bulk(cluster, node, action);
      setPw((p) => p && { ...p, phase: "done" });
      setMsg({ ok: true, text: ranMsg(r, "Done.") });
    } catch (e) {
      setPw((p) => p && { ...p, phase: "failed", error: errText(e) });
    } finally {
      setPending(node, null);
    }
  };
  // follow tracks a node reboot or shutdown: the node's API goes away and,
  // for a reboot, comes back with a fresh uptime.
  const follow = async (action: "reboot" | "shutdown") => {
    setMsg(null);
    setPending(node, action);
    setPw({ action, phase: "running", started: Date.now() });
    setHint("waiting for it to go down");
    const t0 = Date.now();
    let prev: number | undefined;
    let down = false;
    try {
      while (Date.now() - t0 < 15 * 60_000) {
        await new Promise((r) => setTimeout(r, 4000));
        const up = await api.pveNodeStatus(cluster, node).then(
          (s) => s.uptime ?? 0,
          () => undefined,
        );
        if (up === undefined) {
          if (action === "shutdown") break;
          down = true;
          setHint("down, waiting for it to come back");
          continue;
        }
        if (down || (prev !== undefined && up < prev)) break;
        prev = up;
      }
      setPw((p) => p && { ...p, phase: "done" });
    } finally {
      setHint("");
      setPending(node, null);
    }
  };

  return (
    <div className="grid justify-items-start gap-2 sm:justify-items-end">
      <div className="flex flex-wrap gap-2 sm:justify-end">
        <Button size="sm" variant="brand" onClick={() => setShell("")}>
          <TerminalSquare /> Shell
        </Button>
        <Button size="sm" variant="secondary" onClick={() => setShell("upgrade")}>
          <RefreshCw /> Upgrade packages
        </Button>
        <Button size="sm" variant="secondary" onClick={() => bulk("startall")}>
          <Play /> Start all
        </Button>
        <Button size="sm" variant="secondary" onClick={() => bulk("stopall")}>
          <Square /> Stop all
        </Button>
        {nodes.length > 1 && (
          <Button size="sm" variant="secondary" onClick={() => setMigrateAll(true)}>
            <RotateCcw /> Migrate all
          </Button>
        )}
        <Button size="sm" variant="ghost" onClick={() => setPower("reboot")}>
          <RotateCw /> Reboot
        </Button>
        <Button size="sm" variant="ghost" className="text-destructive" onClick={() => setPower("shutdown")}>
          <Power /> Shut down
        </Button>
      </div>
      <PowerBanner state={pw} name={node} hint={hint && `(${hint})`} onDismiss={clearPw} />
      <Note msg={msg} />
      {shell !== null && <NodeShell cluster={cluster} node={node} cmd={shell} onClose={() => setShell(null)} />}
      {power && (
        <TypedConfirm
          title={power === "reboot" ? `Reboot ${node}` : `Shut down ${node}`}
          text={`Guests on ${node} stop unless they are migrated first. Type the node name to confirm.`}
          expect={node}
          submit={power === "reboot" ? "Reboot" : "Shut down"}
          onClose={() => setPower(null)}
          run={async (c) => {
            await pve.nodePower(cluster, node, power, c);
            void follow(power);
          }}
        />
      )}
      {migrateAll && <MigrateAllDialog cluster={cluster} node={node} nodes={nodes} onClose={() => setMigrateAll(false)} onDone={(t) => setMsg({ ok: true, text: t })} />}
    </div>
  );
}

function MigrateAllDialog({ cluster, node, nodes, onClose, onDone }: { cluster: string; node: string; nodes: string[]; onClose: () => void; onDone: (t: string) => void }) {
  const others = nodes.filter((n) => n !== node);
  const [target, setTarget] = useState(others[0] ?? "");
  return (
    <FormDialog
      title={`Migrate all guests off ${node}`}
      submit="Migrate"
      onClose={onClose}
      onSubmit={async () => {
        const r = await pve.bulk(cluster, node, "migrateall", target);
        onDone(ranMsg(r, "Done.", `Migrating to ${target}; follow it under Tasks.`));
      }}
    >
      <Field label="Target node">
        <Select value={target} onChange={(e) => setTarget(e.target.value)}>
          {others.map((n) => (
            <option key={n}>{n}</option>
          ))}
        </Select>
      </Field>
    </FormDialog>
  );
}

function TypedConfirm({ title, text, expect, submit, onClose, run }: { title: string; text: string; expect: string; submit: string; onClose: () => void; run: (c: string) => Promise<void> }) {
  const [v, setV] = useState("");
  return (
    <FormDialog danger title={title} description={text} submit={submit} onClose={onClose} onSubmit={() => (v === expect ? run(v) : Promise.reject(new Error(`Type ${expect} to confirm.`)))}>
      <Input value={v} onChange={(e) => setV(e.target.value)} placeholder={expect} autoFocus />
    </FormDialog>
  );
}

function NodeServices({ cluster, node }: { cluster: string; node: string }) {
  const { data, err, load } = useLoad(() => pve.nodeView<Row[]>(cluster, node, "services"), [cluster, node]);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const act = async (svc: string, action: string) => {
    if (action === "stop" && !confirm(`Stop ${svc} on ${node}?`)) return;
    try {
      await pve.service(cluster, node, svc, action);
      setMsg({ ok: true, text: `${svc}: ${action} done.` });
    } catch (e) {
      setMsg({ ok: false, text: errText(e) });
    }
    void load();
  };
  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between">
        <CardTitle>Services</CardTitle>
        <Button size="icon" variant="ghost" aria-label="Refresh" onClick={() => void load()}>
          <RefreshCw />
        </Button>
      </CardHeader>
      <CardContent className="grid gap-2">
        <Note msg={msg} />
        {err && <p className="text-sm text-destructive">{err}</p>}
        <Table
          rows={data ?? []}
          cols={[
            ["name", "Service"],
            ["desc", "Description"],
            ["state", "State", (r) => <Badge variant={r.state === "running" ? "success" : "outline"}>{str(r.state)}</Badge>],
            ["unit-state", "Enabled"],
          ]}
          actions={(r) => (
            <div className="flex justify-end gap-1">
              {["start", "stop", "restart"].map((a) => (
                <Button key={a} size="sm" variant="ghost" onClick={() => act(str(r.service ?? r.name), a)}>
                  {a}
                </Button>
              ))}
            </div>
          )}
        />
      </CardContent>
    </Card>
  );
}

function NodeUpdates({ cluster, node }: { cluster: string; node: string }) {
  const { data, err, load } = useLoad(() => pve.nodeView<Row[]>(cluster, node, "updates"), [cluster, node]);
  const { data: repos } = useLoad(() => pve.nodeView<Row>(cluster, node, "repos"), [cluster, node]);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [shell, setShell] = useState(false);
  const [log, setLog] = useState("");
  const refresh = async () => {
    try {
      const r = await pve.aptRefresh(cluster, node);
      setMsg({ ok: true, text: "Refreshing the package lists…" });
      if (r.upid) setLog(r.upid);
    } catch (e) {
      setMsg({ ok: false, text: errText(e) });
    }
  };
  const infos = ((repos?.infos as Row[] | undefined) ?? []).filter((i) => i.kind === "warning" || i.kind === "error");
  return (
    <Card>
      <CardHeader className="flex-row flex-wrap items-center justify-between gap-2">
        <div>
          <CardTitle>Updates</CardTitle>
          <CardDescription>{data ? `${data.length} package${data.length === 1 ? "" : "s"} can be upgraded.` : "Loading…"}</CardDescription>
        </div>
        <div className="flex gap-2">
          <Button size="sm" variant="secondary" onClick={refresh}>
            <RefreshCw /> Refresh lists
          </Button>
          <Button size="sm" variant="brand" onClick={() => setShell(true)} disabled={!data?.length}>
            <Download /> Upgrade
          </Button>
          <Button size="icon" variant="ghost" aria-label="Reload" onClick={() => void load()}>
            <RotateCw />
          </Button>
        </div>
      </CardHeader>
      <CardContent className="grid gap-2">
        <Note msg={msg} />
        {err && <p className="text-sm text-destructive">{err}</p>}
        {infos.map((i, n) => (
          <p key={n} className="text-sm text-warning">
            {str(i.message)}
          </p>
        ))}
        <Table
          rows={data ?? []}
          empty="Everything is up to date."
          cols={[
            ["Package", "Package"],
            ["OldVersion", "Installed"],
            ["Version", "Available"],
            ["Origin", "Origin"],
            ["Description", "Description"],
          ]}
        />
      </CardContent>
      {shell && <NodeShell cluster={cluster} node={node} cmd="upgrade" onClose={() => (setShell(false), void load())} />}
      {log && <TaskLog cluster={cluster} node={node} upid={log} onClose={() => (setLog(""), void load())} />}
    </Card>
  );
}

const netTypes = ["bridge", "bond", "vlan", "OVSBridge", "OVSBond", "OVSPort", "OVSIntPort"];

function NodeNetwork({ cluster, node }: { cluster: string; node: string }) {
  const { data, err, load } = useLoad(() => pve.nodeView<Row[]>(cluster, node, "network"), [cluster, node]);
  const [edit, setEdit] = useState<Row | "new" | null>(null);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const rows = useMemo(() => [...(data ?? [])].sort((a, b) => str(a.iface).localeCompare(str(b.iface), undefined, { numeric: true })), [data]);
  const pending = rows.some((r) => Object.keys(r).some((k) => k.endsWith("_pending") || k === "pending")) || rows.some((r) => r.changes);
  const run = async (f: () => Promise<unknown>, ok: string) => {
    try {
      await f();
      setMsg({ ok: true, text: ok });
    } catch (e) {
      setMsg({ ok: false, text: errText(e) });
    }
    void load();
  };
  return (
    <Card>
      <CardHeader className="flex-row flex-wrap items-center justify-between gap-2">
        <div>
          <CardTitle>Network</CardTitle>
          <CardDescription>Changes are staged until you apply them (ifreload). A mistake can cut the node off; keep console access.</CardDescription>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button size="sm" variant="secondary" onClick={() => setEdit("new")}>
            <Plus /> Create
          </Button>
          <Button size="sm" variant="brand" onClick={() => confirm(`Apply the staged network configuration on ${node}?`) && run(() => pve.netApply(cluster, node), "Network configuration applied.")}>
            Apply
          </Button>
          <Button size="sm" variant="ghost" onClick={() => run(() => pve.netRevert(cluster, node), "Staged changes reverted.")}>
            Revert
          </Button>
        </div>
      </CardHeader>
      <CardContent className="grid gap-2">
        <Note msg={msg} />
        {pending && <p className="text-sm text-warning">There are staged changes that are not applied yet.</p>}
        {err && <p className="text-sm text-destructive">{err}</p>}
        <Table
          rows={rows}
          cols={[
            ["iface", "Name"],
            ["type", "Type"],
            ["active", "Active", (r) => (r.active ? "yes" : "no")],
            ["autostart", "Autostart", (r) => (r.autostart ? "yes" : "no")],
            ["cidr", "CIDR"],
            ["gateway", "Gateway"],
            ["bridge_ports", "Ports/slaves", (r) => str(r.bridge_ports ?? r.slaves ?? r["vlan-raw-device"])],
            ["comments", "Comment"],
          ]}
          actions={(r) =>
            netTypes.includes(str(r.type)) || r.type === "eth" ? (
              <div className="flex justify-end gap-1">
                <Button size="icon" variant="ghost" className="size-7" aria-label="Edit" onClick={() => setEdit(r)}>
                  <Pencil />
                </Button>
                {r.type !== "eth" && (
                  <Button
                    size="icon"
                    variant="ghost"
                    className="size-7 text-destructive"
                    aria-label="Delete"
                    onClick={() => confirm(`Delete ${str(r.iface)}? (staged)`) && run(() => pve.netDelete(cluster, node, str(r.iface)), `${str(r.iface)} deleted (staged).`)}
                  >
                    <Trash2 />
                  </Button>
                )}
              </div>
            ) : null
          }
        />
      </CardContent>
      {edit && <NetDialog cluster={cluster} node={node} row={edit === "new" ? null : edit} onClose={() => setEdit(null)} onDone={(t) => (setMsg({ ok: true, text: t }), void load())} />}
    </Card>
  );
}

function NetDialog({ cluster, node, row, onClose, onDone }: { cluster: string; node: string; row: Row | null; onClose: () => void; onDone: (t: string) => void }) {
  const [f, setF] = useState({
    iface: str(row?.iface),
    type: str(row?.type || "bridge"),
    cidr: str(row?.cidr),
    gateway: str(row?.gateway),
    cidr6: str(row?.cidr6),
    gateway6: str(row?.gateway6),
    ports: str(row?.bridge_ports ?? row?.slaves),
    vlanaware: !!row?.bridge_vlan_aware,
    bondmode: str(row?.bond_mode || "active-backup"),
    raw: str(row?.["vlan-raw-device"]),
    vlanid: str(row?.["vlan-id"]),
    mtu: str(row?.mtu),
    autostart: row ? !!row.autostart : true,
    comments: str(row?.comments),
  });
  const up = (k: keyof typeof f) => (v: string | boolean) => setF((x) => ({ ...x, [k]: v }));
  return (
    <FormDialog
      title={row ? `Edit ${f.iface}` : "Create interface"}
      submit="Save (staged)"
      onClose={onClose}
      onSubmit={async () => {
        const p: Params = { type: f.type, autostart: f.autostart };
        const opt = (k: string, v: string) => {
          if (v) p[k] = v;
        };
        opt("cidr", f.cidr);
        opt("gateway", f.gateway);
        opt("cidr6", f.cidr6);
        opt("gateway6", f.gateway6);
        opt("mtu", f.mtu);
        opt("comments", f.comments);
        if (f.type === "bridge") {
          opt("bridge_ports", f.ports);
          p.bridge_vlan_aware = f.vlanaware;
        } else if (f.type === "bond") {
          opt("slaves", f.ports);
          p.bond_mode = f.bondmode;
        } else if (f.type === "vlan") {
          opt("vlan-raw-device", f.raw);
          opt("vlan-id", f.vlanid);
        }
        if (row) await pve.netUpdate(cluster, node, f.iface, p);
        else await pve.netCreate(cluster, node, { ...p, iface: f.iface });
        onDone(`${f.iface} saved; apply to make it live.`);
      }}
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Name" hint={f.type === "bridge" ? "vmbrN" : f.type === "bond" ? "bondN" : f.type === "vlan" ? "e.g. vmbr0.20 or eno1.20" : undefined}>
          <Input value={f.iface} onChange={(e) => up("iface")(e.target.value)} disabled={!!row} required />
        </Field>
        <Field label="Type">
          <Select value={f.type} onChange={(e) => up("type")(e.target.value)} disabled={!!row}>
            {(row?.type === "eth" ? ["eth"] : netTypes).map((t) => (
              <option key={t}>{t}</option>
            ))}
          </Select>
        </Field>
        <Field label="IPv4/CIDR">
          <Input value={f.cidr} onChange={(e) => up("cidr")(e.target.value)} placeholder="10.0.0.2/24" />
        </Field>
        <Field label="Gateway (IPv4)">
          <Input value={f.gateway} onChange={(e) => up("gateway")(e.target.value)} />
        </Field>
        <Field label="IPv6/CIDR">
          <Input value={f.cidr6} onChange={(e) => up("cidr6")(e.target.value)} />
        </Field>
        <Field label="Gateway (IPv6)">
          <Input value={f.gateway6} onChange={(e) => up("gateway6")(e.target.value)} />
        </Field>
        {(f.type === "bridge" || f.type === "bond") && (
          <Field label={f.type === "bridge" ? "Bridge ports" : "Slaves"} hint="Space-separated, e.g. eno1 eno2">
            <Input value={f.ports} onChange={(e) => up("ports")(e.target.value)} />
          </Field>
        )}
        {f.type === "bond" && (
          <Field label="Mode">
            <Select value={f.bondmode} onChange={(e) => up("bondmode")(e.target.value)}>
              {["balance-rr", "active-backup", "balance-xor", "broadcast", "802.3ad", "balance-tlb", "balance-alb"].map((m) => (
                <option key={m}>{m}</option>
              ))}
            </Select>
          </Field>
        )}
        {f.type === "vlan" && (
          <>
            <Field label="VLAN raw device">
              <Input value={f.raw} onChange={(e) => up("raw")(e.target.value)} />
            </Field>
            <Field label="VLAN tag">
              <Input value={f.vlanid} onChange={(e) => up("vlanid")(e.target.value)} />
            </Field>
          </>
        )}
        <Field label="MTU">
          <Input type="number" value={f.mtu} onChange={(e) => up("mtu")(e.target.value)} placeholder="1500" />
        </Field>
        <Field label="Comment">
          <Input value={f.comments} onChange={(e) => up("comments")(e.target.value)} />
        </Field>
      </div>
      <div className="flex flex-wrap gap-4">
        <Check label="Autostart" v={f.autostart} set={up("autostart")} />
        {f.type === "bridge" && <Check label="VLAN aware" v={f.vlanaware} set={up("vlanaware")} />}
      </div>
    </FormDialog>
  );
}

function NodeDisks({ cluster, node }: { cluster: string; node: string }) {
  const { data, err, load } = useLoad(() => pve.nodeView<Row[]>(cluster, node, "disks"), [cluster, node]);
  const { data: zfs } = useLoad(() => pve.nodeView<Row[]>(cluster, node, "zfs").catch(() => []), [cluster, node]);
  const { data: lvm } = useLoad(() => pve.nodeView<Row>(cluster, node, "lvm").catch((): Row => ({})), [cluster, node]);
  const { data: thin } = useLoad(() => pve.nodeView<Row[]>(cluster, node, "lvmthin").catch(() => []), [cluster, node]);
  const { data: dirs } = useLoad(() => pve.nodeView<Row[]>(cluster, node, "directory").catch(() => []), [cluster, node]);
  const [create, setCreate] = useState<string | null>(null);
  const [danger, setDanger] = useState<{ action: "wipedisk" | "initgpt"; disk: string } | null>(null);
  const [smart, setSmart] = useState("");
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const unused = (data ?? []).filter((d) => d.used === undefined || d.used === "" || d.used === "unused").map((d) => str(d.devpath));
  const vgs = ((lvm?.children as Row[] | undefined) ?? []).map((v) => ({ name: v.name, size: v.size, free: v.free }));
  return (
    <div className="grid gap-4">
      <Card>
        <CardHeader className="flex-row flex-wrap items-center justify-between gap-2">
          <CardTitle>Disks</CardTitle>
          <div className="flex flex-wrap gap-2">
            {["zfs", "lvm", "lvmthin", "directory"].map((k) => (
              <Button key={k} size="sm" variant="secondary" onClick={() => setCreate(k)}>
                <Plus /> {k === "zfs" ? "ZFS pool" : k === "lvm" ? "LVM" : k === "lvmthin" ? "LVM-thin" : "Directory"}
              </Button>
            ))}
            <Button size="icon" variant="ghost" aria-label="Refresh" onClick={() => void load()}>
              <RefreshCw />
            </Button>
          </div>
        </CardHeader>
        <CardContent className="grid gap-2">
          <Note msg={msg} />
          {err && <p className="text-sm text-destructive">{err}</p>}
          <Table
            rows={data ?? []}
            cols={[
              ["devpath", "Device"],
              ["type", "Type"],
              ["usage", "Usage", (r) => str(r.used || "unused")],
              ["size", "Size", (r) => formatBytes(num(r.size))],
              ["model", "Model"],
              ["serial", "Serial"],
              ["health", "SMART", (r) => <Badge variant={r.health === "PASSED" || r.health === "OK" ? "success" : r.health ? "warning" : "outline"}>{str(r.health || "n/a")}</Badge>],
              ["wearout", "Wearout", (r) => (r.wearout === "N/A" || r.wearout === undefined ? "" : `${str(r.wearout)}%`)],
            ]}
            actions={(r) => (
              <div className="flex justify-end gap-1">
                <Button size="sm" variant="ghost" onClick={() => setSmart(str(r.devpath))}>
                  SMART
                </Button>
                <Button size="sm" variant="ghost" onClick={() => setDanger({ action: "initgpt", disk: str(r.devpath) })}>
                  Init GPT
                </Button>
                <Button size="sm" variant="ghost" className="text-destructive" onClick={() => setDanger({ action: "wipedisk", disk: str(r.devpath) })}>
                  Wipe
                </Button>
              </div>
            )}
          />
        </CardContent>
      </Card>
      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>ZFS pools</CardTitle>
          </CardHeader>
          <CardContent>
            <Table
              rows={zfs ?? []}
              cols={[
                ["name", "Pool"],
                ["health", "Health"],
                ["size", "Size", (r) => formatBytes(num(r.size))],
                ["free", "Free", (r) => formatBytes(num(r.free))],
                ["frag", "Frag", (r) => `${str(r.frag)}%`],
              ]}
            />
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle>LVM volume groups</CardTitle>
          </CardHeader>
          <CardContent>
            <Table
              rows={vgs}
              cols={[
                ["name", "Volume group"],
                ["size", "Size", (r) => formatBytes(num(r.size))],
                ["free", "Free", (r) => formatBytes(num(r.free))],
              ]}
            />
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle>LVM-thin pools</CardTitle>
          </CardHeader>
          <CardContent>
            <Table
              rows={thin ?? []}
              cols={[
                ["lv", "Pool"],
                ["vg", "VG"],
                ["lv_size", "Size", (r) => formatBytes(num(r.lv_size))],
                ["used", "Used", (r) => formatBytes(num(r.used))],
              ]}
            />
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle>Directories</CardTitle>
          </CardHeader>
          <CardContent>
            <Table
              rows={dirs ?? []}
              cols={[
                ["path", "Path"],
                ["device", "Device"],
                ["type", "FS"],
              ]}
            />
          </CardContent>
        </Card>
      </div>
      {create && <CreateStorageOnDisk cluster={cluster} node={node} kind={create} disks={unused} onClose={() => setCreate(null)} onDone={(t) => (setMsg({ ok: true, text: t }), void load())} />}
      {danger && (
        <TypedConfirm
          title={danger.action === "wipedisk" ? `Wipe ${danger.disk}` : `Initialize ${danger.disk} with GPT`}
          text="Everything on the disk is destroyed. Type the device path to confirm."
          expect={danger.disk}
          submit={danger.action === "wipedisk" ? "Wipe" : "Initialize"}
          onClose={() => setDanger(null)}
          run={async (c) => {
            const r = await pve.disk(cluster, node, danger.action, { disk: danger.disk }, c);
            setMsg({ ok: true, text: ranMsg(r, "Done.", "Started; see Tasks.") });
            void load();
          }}
        />
      )}
      {smart && <SmartDialog cluster={cluster} node={node} disk={smart} onClose={() => setSmart("")} />}
    </div>
  );
}

function SmartDialog({ cluster, node, disk, onClose }: { cluster: string; node: string; disk: string; onClose: () => void }) {
  const { data, err } = useLoad(() => pve.nodeView<Row>(cluster, node, "smart", `?disk=${encodeURIComponent(disk)}`), [cluster, node, disk]);
  const attrs = (data?.attributes as Row[] | undefined) ?? [];
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>
            SMART · {disk} {data?.health ? <Badge variant={data.health === "PASSED" ? "success" : "warning"}>{str(data.health)}</Badge> : null}
          </DialogTitle>
        </DialogHeader>
        {err && <p className="text-sm text-destructive">{err}</p>}
        <div className="max-h-[60vh] overflow-auto">
          {attrs.length ? (
            <Table
              rows={attrs}
              cols={[
                ["id", "ID"],
                ["name", "Attribute"],
                ["value", "Value"],
                ["worst", "Worst"],
                ["threshold", "Threshold"],
                ["raw", "Raw"],
              ]}
            />
          ) : (
            <pre className="font-mono text-xs whitespace-pre-wrap">{str(data?.text) || "Loading…"}</pre>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}

function CreateStorageOnDisk({ cluster, node, kind, disks, onClose, onDone }: { cluster: string; node: string; kind: string; disks: string[]; onClose: () => void; onDone: (t: string) => void }) {
  const [name, setName] = useState("");
  const [picked, setPicked] = useState<string[]>([]);
  const [raid, setRaid] = useState("single");
  const [fs, setFs] = useState("ext4");
  const [add, setAdd] = useState(true);
  const [confirmText, setConfirm] = useState("");
  const multi = kind === "zfs";
  const devs = picked.join(",");
  return (
    <FormDialog
      danger
      title={`Create ${kind === "zfs" ? "ZFS pool" : kind === "lvm" ? "LVM volume group" : kind === "lvmthin" ? "LVM-thin pool" : "directory storage"}`}
      description="The selected disks are formatted. Type the device list exactly as shown to confirm."
      submit="Create"
      onClose={onClose}
      onSubmit={async () => {
        const p: Params = { name, add_storage: add };
        if (multi) {
          p.devices = devs;
          p.raidlevel = raid;
          p.compression = "on";
          p.ashift = 12;
        } else {
          p.device = devs;
        }
        if (kind === "directory") p.filesystem = fs;
        const r = await pve.disk(cluster, node, kind, p, confirmText);
        onDone(ranMsg(r, "Created.", "Creating; see Tasks."));
      }}
    >
      <Field label="Name">
        <Input value={name} onChange={(e) => setName(e.target.value)} required pattern="[A-Za-z][A-Za-z0-9_\-]*" />
      </Field>
      <Field label={multi ? "Disks" : "Disk"} hint={disks.length ? undefined : "No unused disks on this node."}>
        <div className="grid gap-1">
          {disks.map((d) => (
            <label key={d} className="flex items-center gap-2 text-sm">
              <input
                type={multi ? "checkbox" : "radio"}
                name="disk"
                checked={picked.includes(d)}
                onChange={(e) => setPicked((p) => (multi ? (e.target.checked ? [...p, d] : p.filter((x) => x !== d)) : [d]))}
              />
              <span className="font-mono">{d}</span>
            </label>
          ))}
        </div>
      </Field>
      {multi && (
        <Field label="RAID level">
          <Select value={raid} onChange={(e) => setRaid(e.target.value)}>
            {["single", "mirror", "raid10", "raidz", "raidz2", "raidz3", "draid", "draid2", "draid3"].map((r) => (
              <option key={r}>{r}</option>
            ))}
          </Select>
        </Field>
      )}
      {kind === "directory" && (
        <Field label="Filesystem">
          <Select value={fs} onChange={(e) => setFs(e.target.value)}>
            <option>ext4</option>
            <option>xfs</option>
          </Select>
        </Field>
      )}
      <Check label="Add as storage" v={add} set={setAdd} />
      <Field label={`Type ${devs || "the disk"} to confirm`}>
        <Input value={confirmText} onChange={(e) => setConfirm(e.target.value)} placeholder={devs} required />
      </Field>
    </FormDialog>
  );
}

function NodeSystem({ cluster, node }: { cluster: string; node: string }) {
  const { data: dns, load: reloadDns } = useLoad(() => pve.nodeView<Row>(cluster, node, "dns"), [cluster, node]);
  const { data: time, load: reloadTime } = useLoad(() => pve.nodeView<Row>(cluster, node, "time"), [cluster, node]);
  const { data: hosts, load: reloadHosts } = useLoad(() => pve.nodeView<Row>(cluster, node, "hosts"), [cluster, node]);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [d, setD] = useState({ search: "", dns1: "", dns2: "", dns3: "" });
  const [tz, setTz] = useState("");
  const [h, setH] = useState("");
  useEffect(() => {
    if (dns) setD({ search: str(dns.search), dns1: str(dns.dns1), dns2: str(dns.dns2), dns3: str(dns.dns3) });
  }, [dns]);
  useEffect(() => {
    if (time) setTz(str(time.timezone));
  }, [time]);
  useEffect(() => {
    if (hosts) setH(str(hosts.data));
  }, [hosts]);
  const save = async (what: string, p: Params, reload: () => void) => {
    try {
      await pve.nodeSet(cluster, node, what, p);
      setMsg({ ok: true, text: `${what} saved.` });
    } catch (e) {
      setMsg({ ok: false, text: errText(e) });
    }
    reload();
  };
  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <Note msg={msg} />
      <Card>
        <CardHeader>
          <CardTitle>DNS</CardTitle>
        </CardHeader>
        <CardContent className="grid gap-3">
          {(["search", "dns1", "dns2", "dns3"] as const).map((k) => (
            <Field key={k} label={k === "search" ? "Search domain" : `DNS server ${k.slice(3)}`}>
              <Input value={d[k]} onChange={(e) => setD((x) => ({ ...x, [k]: e.target.value }))} />
            </Field>
          ))}
          <Button
            size="sm"
            variant="brand"
            className="w-fit"
            onClick={() => save("dns", Object.fromEntries(Object.entries(d).filter(([, v]) => v)) as Params, () => void reloadDns())}
          >
            Save DNS
          </Button>
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>Time</CardTitle>
          <CardDescription>{time ? `Local time ${new Date(num(time.localtime) * 1000).toISOString().replace("T", " ").slice(0, 19)}` : ""}</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-3">
          <Field label="Time zone" hint="For example Europe/Budapest or UTC.">
            <Input value={tz} onChange={(e) => setTz(e.target.value)} />
          </Field>
          <Button size="sm" variant="brand" className="w-fit" onClick={() => save("time", { timezone: tz }, () => void reloadTime())}>
            Save time zone
          </Button>
        </CardContent>
      </Card>
      <Card className="lg:col-span-2">
        <CardHeader>
          <CardTitle>/etc/hosts</CardTitle>
        </CardHeader>
        <CardContent className="grid gap-3">
          <Textarea rows={10} className="font-mono text-xs" value={h} onChange={(e) => setH(e.target.value)} />
          <Button
            size="sm"
            variant="brand"
            className="w-fit"
            onClick={() => save("hosts", { data: h, ...(hosts?.digest ? { digest: str(hosts.digest) } : {}) }, () => void reloadHosts())}
          >
            Save hosts
          </Button>
        </CardContent>
      </Card>
    </div>
  );
}

function NodeSyslog({ cluster, node }: { cluster: string; node: string }) {
  const [svc, setSvc] = useState("");
  const [limit, setLimit] = useState(500);
  const { data, err, load } = useLoad(() => pve.syslog(cluster, node, limit, svc), [cluster, node, limit, svc]);
  return (
    <Card>
      <CardHeader className="flex-row flex-wrap items-center justify-between gap-2">
        <CardTitle>System log</CardTitle>
        <div className="flex gap-2">
          <Input className="w-40" placeholder="service (e.g. pveproxy)" value={svc} onChange={(e) => setSvc(e.target.value)} />
          <Select className="w-28" value={String(limit)} onChange={(e) => setLimit(Number(e.target.value))}>
            {[200, 500, 2000, 5000].map((n) => (
              <option key={n} value={n}>
                {n} lines
              </option>
            ))}
          </Select>
          <Button size="icon" variant="ghost" aria-label="Refresh" onClick={() => void load()}>
            <RefreshCw />
          </Button>
        </div>
      </CardHeader>
      <CardContent>
        {err && <p className="text-sm text-destructive">{err}</p>}
        <pre className="max-h-[65vh] overflow-auto rounded-md bg-muted p-3 font-mono text-xs whitespace-pre-wrap">{(data ?? []).map((l) => l.t).join("\n") || "Loading…"}</pre>
      </CardContent>
    </Card>
  );
}

function NodeTasks({ cluster, node }: { cluster: string; node: string }) {
  const [errors, setErrors] = useState(false);
  const { data, err, load } = useLoad(() => pve.nodeTasks(cluster, node, errors), [cluster, node, errors]);
  const [log, setLog] = useState("");
  useEffect(() => {
    const t = setInterval(load, 10000);
    return () => clearInterval(t);
  }, [load]);
  return (
    <Card>
      <CardHeader className="flex-row flex-wrap items-center justify-between gap-2">
        <CardTitle>Tasks</CardTitle>
        <div className="flex items-center gap-3">
          <Check label="Only errors" v={errors} set={setErrors} />
          <Button size="icon" variant="ghost" aria-label="Refresh" onClick={() => void load()}>
            <RefreshCw />
          </Button>
        </div>
      </CardHeader>
      <CardContent>
        {err && <p className="text-sm text-destructive">{err}</p>}
        <TaskTable
          tasks={data ?? []}
          onOpen={(t) => setLog(t.upid)}
          onStop={async (t) => {
            if (confirm("Stop this task?")) await pve.nodeTaskStop(cluster, node, t.upid).catch(() => {});
            void load();
          }}
        />
      </CardContent>
      {log && <TaskLog cluster={cluster} node={node} upid={log} onClose={() => setLog("")} />}
    </Card>
  );
}

function TaskTable({ tasks, onOpen, onStop }: { tasks: PVETaskRow[]; onOpen: (t: PVETaskRow) => void; onStop?: (t: PVETaskRow) => void }) {
  return (
    <Table
      rows={tasks as unknown as Row[]}
      empty="No tasks."
      cols={[
        ["starttime", "Started", (r) => new Date(num(r.starttime) * 1000).toLocaleString()],
        ["type", "Task", (r) => `${str(r.type)}${r.id ? ` ${str(r.id)}` : ""}`],
        ["node", "Node"],
        ["user", "By"],
        [
          "status",
          "Result",
          (r) => {
            const s = str(r.status);
            return <Badge variant={!s ? "warning" : s === "OK" ? "success" : s.startsWith("WARNINGS") ? "warning" : "destructive"}>{s || "running"}</Badge>;
          },
        ],
      ]}
      actions={(r) => (
        <div className="flex justify-end gap-1">
          <Button size="sm" variant="ghost" onClick={() => onOpen(r as unknown as PVETaskRow)}>
            Log
          </Button>
          {onStop && !r.status && (
            <Button size="sm" variant="ghost" className="text-destructive" onClick={() => onStop(r as unknown as PVETaskRow)}>
              Stop
            </Button>
          )}
        </div>
      )}
    />
  );
}

// ================= storage =================

// UploadDialog streams an ISO, container template or disk image through
// the panel to a storage.
export function UploadDialog({ cluster, node, storage, onClose, onDone }: { cluster: string; node: string; storage: { storage: string; content: string }; onClose: () => void; onDone: (t: string) => void }) {
  const kinds: string[] = storage.content.split(",").filter((k) => k === "iso" || k === "vztmpl" || k === "import");
  const [content, setContent] = useState(kinds[0] ?? "iso");
  const [file, setFile] = useState<File | null>(null);
  const [pct, setPct] = useState(0);
  return (
    <FormDialog
      title={`Upload to ${storage.storage}`}
      description="The file streams through the panel straight to Proxmox. Keep this window open until it finishes."
      submit="Upload"
      onClose={onClose}
      onSubmit={async () => {
        if (!file) throw new Error("pick a file");
        await pve.upload(cluster, node, storage.storage, content, file, setPct);
        onDone(`${file.name} uploaded; Proxmox is moving it into place.`);
      }}
    >
      <Field label="Content">
        <Select value={content} onChange={(e) => setContent(e.target.value)}>
          {kinds.map((k) => (
            <option key={k} value={k}>
              {k === "iso" ? "ISO image" : k === "vztmpl" ? "Container template" : "Import (disk image)"}
            </option>
          ))}
        </Select>
      </Field>
      <Field label="File">
        <Input type="file" accept={content === "iso" ? ".iso,.img" : content === "vztmpl" ? ".tar.gz,.tar.xz,.tar.zst,.tgz" : ".img,.qcow2,.vmdk"} onChange={(e) => setFile(e.target.files?.[0] ?? null)} required />
      </Field>
      {pct > 0 && (
        <div className="grid gap-1">
          <Meter value={pct * 100} />
          <span className="text-xs text-muted-foreground">{Math.round(pct * 100)}%</span>
        </div>
      )}
    </FormDialog>
  );
}

// ================= datacenter =================

type FieldSpec = { k: string; label: string; hint?: string; type?: "text" | "number" | "bool" | "select" | "textarea" | "password"; options?: string[]; required?: boolean; create?: boolean };
type Section = {
  coll: string;
  title: string;
  about: string;
  id: string; // key field of an entry
  cols: [string, string][];
  fields?: FieldSpec[];
  fixed?: string[]; // fields only sent on create
  readOnly?: boolean;
  singleton?: boolean;
  items?: { id: string; cols: [string, string][]; fields: FieldSpec[] };
  keyOf?: (r: Row) => string;
};

const sections: Section[] = [
  { coll: "status", title: "Cluster status", about: "Nodes and quorum.", id: "id", readOnly: true, cols: [["name", "Name"], ["type", "Type"], ["online", "Online"], ["ip", "IP"], ["quorate", "Quorate"], ["nodes", "Nodes"], ["version", "Version"]] },
  { coll: "tasks", title: "Cluster tasks", about: "Recent tasks on all nodes.", id: "upid", readOnly: true, cols: [["starttime", "Started"], ["type", "Task"], ["id", "Object"], ["node", "Node"], ["user", "By"], ["status", "Result"]] },
  { coll: "log", title: "Cluster log", about: "", id: "uid", readOnly: true, cols: [["time", "Time"], ["node", "Node"], ["user", "User"], ["tag", "Tag"], ["msg", "Message"]] },
  {
    coll: "options",
    title: "Options",
    about: "Datacenter-wide options (datacenter.cfg).",
    id: "",
    singleton: true,
    cols: [],
    fields: [
      { k: "keyboard", label: "Keyboard layout" },
      { k: "language", label: "Language" },
      { k: "console", label: "Default console", type: "select", options: ["", "applet", "vv", "html5", "xtermjs"] },
      { k: "email_from", label: "Email from address" },
      { k: "http_proxy", label: "HTTP proxy" },
      { k: "mac_prefix", label: "MAC prefix" },
      { k: "max_workers", label: "Max workers (bulk actions)", type: "number" },
      { k: "migration", label: "Migration settings", hint: "type=secure,network=10.0.0.0/24" },
      { k: "ha", label: "HA settings", hint: "shutdown_policy=migrate" },
      { k: "tag-style", label: "Tag style" },
      { k: "next-id", label: "Next free VMID range", hint: "lower=100,upper=1000000" },
      { k: "description", label: "Notes", type: "textarea" },
    ],
  },
  {
    coll: "backup",
    title: "Backup jobs",
    about: "Scheduled vzdump jobs.",
    id: "id",
    cols: [["id", "ID"], ["enabled", "Enabled"], ["schedule", "Schedule"], ["storage", "Storage"], ["vmid", "Guests"], ["all", "All"], ["mode", "Mode"], ["comment", "Comment"]],
    fixed: ["id"],
    fields: [
      { k: "id", label: "Job ID", hint: "Optional; generated when empty.", create: true },
      { k: "schedule", label: "Schedule", hint: "systemd calendar, e.g. 02:00, sun 01:00, mon..fri 21:30", required: true },
      { k: "storage", label: "Storage", required: true },
      { k: "all", label: "All guests", type: "bool" },
      { k: "vmid", label: "Guests", hint: "Comma-separated VMIDs (when not all)" },
      { k: "exclude", label: "Exclude VMIDs" },
      { k: "pool", label: "Pool" },
      { k: "node", label: "Only on node" },
      { k: "mode", label: "Mode", type: "select", options: ["snapshot", "suspend", "stop"] },
      { k: "compress", label: "Compression", type: "select", options: ["zstd", "lzo", "gzip", "0"] },
      { k: "prune-backups", label: "Retention", hint: "keep-last=3,keep-daily=7,keep-weekly=4" },
      { k: "mailto", label: "Mail to" },
      { k: "notes-template", label: "Notes template", hint: "{{guestname}} {{vmid}}" },
      { k: "enabled", label: "Enabled", type: "bool" },
      { k: "comment", label: "Comment" },
    ],
  },
  {
    coll: "replication",
    title: "Replication",
    about: "Storage replication jobs (ZFS).",
    id: "id",
    cols: [["id", "ID"], ["target", "Target"], ["schedule", "Schedule"], ["rate", "Rate"], ["disable", "Disabled"], ["comment", "Comment"]],
    fixed: ["id", "type"],
    fields: [
      { k: "id", label: "Job ID", hint: "<vmid>-<n>, e.g. 100-0", required: true, create: true },
      { k: "type", label: "Type", type: "select", options: ["local"], create: true },
      { k: "target", label: "Target node", required: true },
      { k: "schedule", label: "Schedule", hint: "*/15" },
      { k: "rate", label: "Rate limit (MB/s)", type: "number" },
      { k: "disable", label: "Disabled", type: "bool" },
      { k: "comment", label: "Comment" },
    ],
  },
  {
    coll: "ha-resources",
    title: "HA resources",
    about: "Guests managed by the HA stack.",
    id: "sid",
    cols: [["sid", "Resource"], ["state", "State"], ["group", "Group"], ["max_restart", "Max restart"], ["max_relocate", "Max relocate"], ["comment", "Comment"]],
    fixed: ["sid", "type"],
    fields: [
      { k: "sid", label: "Resource", hint: "vm:100 or ct:101", required: true, create: true },
      { k: "state", label: "State", type: "select", options: ["started", "stopped", "ignored", "disabled"] },
      { k: "group", label: "Group" },
      { k: "max_restart", label: "Max restart", type: "number" },
      { k: "max_relocate", label: "Max relocate", type: "number" },
      { k: "comment", label: "Comment" },
    ],
  },
  {
    coll: "ha-groups",
    title: "HA groups",
    about: "Node groups for HA resources (Proxmox VE 8; version 9 uses HA rules).",
    id: "group",
    cols: [["group", "Group"], ["nodes", "Nodes"], ["restricted", "Restricted"], ["nofailback", "No failback"], ["comment", "Comment"]],
    fixed: ["group", "type"],
    fields: [
      { k: "group", label: "Group", required: true, create: true },
      { k: "nodes", label: "Nodes", hint: "node1:2,node2:1 (priority after the colon)", required: true },
      { k: "restricted", label: "Restricted", type: "bool" },
      { k: "nofailback", label: "No failback", type: "bool" },
      { k: "comment", label: "Comment" },
    ],
  },
  {
    coll: "ha-rules",
    title: "HA rules",
    about: "Node and resource affinity rules (Proxmox VE 9).",
    id: "rule",
    cols: [["rule", "Rule"], ["type", "Type"], ["resources", "Resources"], ["nodes", "Nodes"], ["strict", "Strict"], ["comment", "Comment"]],
    fixed: ["rule", "type"],
    fields: [
      { k: "rule", label: "Rule ID", required: true, create: true },
      { k: "type", label: "Type", type: "select", options: ["node-affinity", "resource-affinity"], create: true },
      { k: "resources", label: "Resources", hint: "vm:100,ct:101", required: true },
      { k: "nodes", label: "Nodes", hint: "node-affinity: node1:2,node2" },
      { k: "affinity", label: "Affinity", type: "select", options: ["", "positive", "negative"] },
      { k: "strict", label: "Strict", type: "bool" },
      { k: "comment", label: "Comment" },
    ],
  },
  {
    coll: "pools",
    title: "Pools",
    about: "Resource pools group guests and storages for permissions.",
    id: "poolid",
    cols: [["poolid", "Pool"], ["comment", "Comment"]],
    fixed: ["poolid"],
    fields: [
      { k: "poolid", label: "Pool", required: true, create: true },
      { k: "comment", label: "Comment" },
    ],
  },
  {
    coll: "users",
    title: "Users",
    about: "Proxmox users. Passwords apply to the pve realm only.",
    id: "userid",
    cols: [["userid", "User"], ["enable", "Enabled"], ["firstname", "First name"], ["lastname", "Last name"], ["email", "Email"], ["groups", "Groups"], ["expire", "Expires"]],
    fixed: ["userid", "password"],
    fields: [
      { k: "userid", label: "User ID", hint: "name@pve or name@pam", required: true, create: true },
      { k: "password", label: "Password", type: "password", create: true },
      { k: "groups", label: "Groups", hint: "Comma-separated" },
      { k: "firstname", label: "First name" },
      { k: "lastname", label: "Last name" },
      { k: "email", label: "Email" },
      { k: "expire", label: "Expires (epoch, 0 = never)", type: "number" },
      { k: "enable", label: "Enabled", type: "bool" },
      { k: "comment", label: "Comment" },
    ],
  },
  {
    coll: "groups",
    title: "Groups",
    about: "",
    id: "groupid",
    cols: [["groupid", "Group"], ["users", "Members"], ["comment", "Comment"]],
    fixed: ["groupid"],
    fields: [
      { k: "groupid", label: "Group", required: true, create: true },
      { k: "comment", label: "Comment" },
    ],
  },
  {
    coll: "roles",
    title: "Roles",
    about: "Built-in roles cannot be changed.",
    id: "roleid",
    cols: [["roleid", "Role"], ["special", "Built-in"], ["privs", "Privileges"]],
    fixed: ["roleid"],
    fields: [
      { k: "roleid", label: "Role", required: true, create: true },
      { k: "privs", label: "Privileges", hint: "VM.Audit,VM.PowerMgmt,Datastore.Audit", type: "textarea" },
    ],
  },
  {
    coll: "acl",
    title: "Permissions",
    about: "Who holds which role on which path.",
    id: "",
    cols: [["path", "Path"], ["type", "Type"], ["ugid", "User/group/token"], ["roleid", "Role"], ["propagate", "Propagate"]],
    keyOf: () => "",
    fields: [
      { k: "path", label: "Path", hint: "/, /vms/100, /pool/name, /storage/local", required: true },
      { k: "roles", label: "Role", required: true },
      { k: "users", label: "Users", hint: "user@realm, comma-separated" },
      { k: "groups", label: "Groups" },
      { k: "tokens", label: "API tokens", hint: "user@realm!token" },
      { k: "propagate", label: "Propagate", type: "bool" },
    ],
  },
  {
    coll: "realms",
    title: "Authentication realms",
    about: "",
    id: "realm",
    cols: [["realm", "Realm"], ["type", "Type"], ["comment", "Comment"], ["default", "Default"], ["tfa", "TFA"]],
    fixed: ["realm", "type"],
    fields: [
      { k: "realm", label: "Realm", required: true, create: true },
      { k: "type", label: "Type", type: "select", options: ["ldap", "ad", "openid"], create: true },
      { k: "server1", label: "Server" },
      { k: "base_dn", label: "Base DN" },
      { k: "user_attr", label: "User attribute" },
      { k: "domain", label: "AD domain" },
      { k: "issuer-url", label: "OpenID issuer URL" },
      { k: "client-id", label: "OpenID client ID" },
      { k: "client-key", label: "OpenID client key", type: "password" },
      { k: "default", label: "Default realm", type: "bool" },
      { k: "comment", label: "Comment" },
    ],
  },
  {
    coll: "fw-options",
    title: "Firewall options",
    about: "Cluster firewall. Enabling it without an allow rule for the web UI (8006) and SSH can lock you out.",
    id: "",
    singleton: true,
    cols: [],
    fields: [
      { k: "enable", label: "Firewall enabled", type: "bool" },
      { k: "policy_in", label: "Input policy", type: "select", options: ["ACCEPT", "DROP", "REJECT"] },
      { k: "policy_out", label: "Output policy", type: "select", options: ["ACCEPT", "DROP", "REJECT"] },
      { k: "ebtables", label: "ebtables", type: "bool" },
      { k: "log_ratelimit", label: "Log rate limit", hint: "enable=1,rate=1/second,burst=5" },
    ],
  },
  {
    coll: "fw-rules",
    title: "Firewall rules",
    about: "Cluster-wide rules, applied on every node.",
    id: "pos",
    cols: [["pos", "#"], ["type", "Dir"], ["action", "Action"], ["macro", "Macro"], ["proto", "Proto"], ["source", "Source"], ["dest", "Dest"], ["dport", "Port"], ["enable", "On"], ["comment", "Comment"]],
    fields: [
      { k: "type", label: "Direction", type: "select", options: ["in", "out", "group"], required: true },
      { k: "action", label: "Action", hint: "ACCEPT, DROP, REJECT or a security group name", required: true },
      { k: "macro", label: "Macro", hint: "SSH, HTTP, HTTPS, Ping…" },
      { k: "proto", label: "Protocol", hint: "tcp, udp, icmp" },
      { k: "source", label: "Source" },
      { k: "dest", label: "Destination" },
      { k: "dport", label: "Destination port" },
      { k: "sport", label: "Source port" },
      { k: "iface", label: "Interface" },
      { k: "log", label: "Log level", type: "select", options: ["", "nolog", "info", "warning", "err"] },
      { k: "enable", label: "Enabled", type: "bool" },
      { k: "comment", label: "Comment" },
    ],
  },
  {
    coll: "fw-groups",
    title: "Security groups",
    about: "Named rule sets you can use in rules (type group).",
    id: "group",
    cols: [["group", "Group"], ["comment", "Comment"]],
    fixed: ["group"],
    fields: [
      { k: "group", label: "Group", required: true, create: true },
      { k: "comment", label: "Comment" },
    ],
    items: {
      id: "pos",
      cols: [["pos", "#"], ["type", "Dir"], ["action", "Action"], ["macro", "Macro"], ["proto", "Proto"], ["source", "Source"], ["dest", "Dest"], ["dport", "Port"], ["enable", "On"]],
      fields: [
        { k: "type", label: "Direction", type: "select", options: ["in", "out"], required: true },
        { k: "action", label: "Action", type: "select", options: ["ACCEPT", "DROP", "REJECT"], required: true },
        { k: "macro", label: "Macro" },
        { k: "proto", label: "Protocol" },
        { k: "source", label: "Source" },
        { k: "dest", label: "Destination" },
        { k: "dport", label: "Destination port" },
        { k: "enable", label: "Enabled", type: "bool" },
        { k: "comment", label: "Comment" },
      ],
    },
  },
  {
    coll: "fw-aliases",
    title: "Firewall aliases",
    about: "Named addresses and networks.",
    id: "name",
    cols: [["name", "Name"], ["cidr", "CIDR"], ["comment", "Comment"]],
    fixed: ["name"],
    fields: [
      { k: "name", label: "Name", required: true, create: true },
      { k: "cidr", label: "Address or network", required: true },
      { k: "comment", label: "Comment" },
    ],
  },
  {
    coll: "fw-ipsets",
    title: "IP sets",
    about: "Lists of addresses for rules (+name).",
    id: "name",
    cols: [["name", "Name"], ["comment", "Comment"]],
    fixed: ["name"],
    fields: [
      { k: "name", label: "Name", required: true, create: true },
      { k: "comment", label: "Comment" },
    ],
    items: {
      id: "cidr",
      cols: [["cidr", "CIDR"], ["nomatch", "No match"], ["comment", "Comment"]],
      fields: [
        { k: "cidr", label: "Address or network", required: true },
        { k: "nomatch", label: "No match", type: "bool" },
        { k: "comment", label: "Comment" },
      ],
    },
  },
  {
    coll: "sdn-zones",
    title: "SDN zones",
    about: "Apply SDN changes with the button above the list.",
    id: "zone",
    cols: [["zone", "Zone"], ["type", "Type"], ["nodes", "Nodes"], ["ipam", "IPAM"], ["state", "State"]],
    fixed: ["zone", "type"],
    fields: [
      { k: "zone", label: "Zone", required: true, create: true },
      { k: "type", label: "Type", type: "select", options: ["simple", "vlan", "qinq", "vxlan", "evpn"], create: true },
      { k: "bridge", label: "Bridge (vlan/qinq)" },
      { k: "peers", label: "Peers (vxlan)" },
      { k: "nodes", label: "Nodes" },
      { k: "ipam", label: "IPAM", hint: "pve" },
      { k: "mtu", label: "MTU", type: "number" },
    ],
  },
  {
    coll: "sdn-vnets",
    title: "SDN VNets",
    about: "Virtual networks in a zone; open one for its subnets.",
    id: "vnet",
    cols: [["vnet", "VNet"], ["zone", "Zone"], ["tag", "Tag"], ["alias", "Alias"], ["state", "State"]],
    fixed: ["vnet"],
    fields: [
      { k: "vnet", label: "VNet", required: true, create: true },
      { k: "zone", label: "Zone", required: true },
      { k: "tag", label: "Tag", type: "number" },
      { k: "alias", label: "Alias" },
      { k: "vlanaware", label: "VLAN aware", type: "bool" },
    ],
    items: {
      id: "subnet",
      cols: [["subnet", "Subnet"], ["cidr", "CIDR"], ["gateway", "Gateway"], ["snat", "SNAT"]],
      fields: [
        { k: "subnet", label: "Subnet (CIDR)", required: true },
        { k: "type", label: "Type", type: "select", options: ["subnet"], required: true },
        { k: "gateway", label: "Gateway" },
        { k: "snat", label: "SNAT", type: "bool" },
      ],
    },
  },
  {
    coll: "storage",
    title: "Storage",
    about: "Storage definitions shared by all nodes.",
    id: "storage",
    cols: [["storage", "ID"], ["type", "Type"], ["content", "Content"], ["path", "Path/target"], ["shared", "Shared"], ["disable", "Disabled"], ["nodes", "Nodes"]],
    fixed: ["storage", "type"],
    fields: [
      { k: "storage", label: "ID", required: true, create: true },
      { k: "type", label: "Type", type: "select", options: ["dir", "nfs", "cifs", "lvm", "lvmthin", "zfspool", "rbd", "cephfs", "iscsi", "pbs", "btrfs"], create: true },
      { k: "content", label: "Content", hint: "images,rootdir,iso,vztmpl,backup,snippets,import" },
      { k: "path", label: "Path (dir/btrfs)" },
      { k: "server", label: "Server (nfs/cifs/pbs)" },
      { k: "export", label: "Export (nfs)" },
      { k: "share", label: "Share (cifs)" },
      { k: "username", label: "Username (cifs/pbs)" },
      { k: "password", label: "Password (cifs/pbs)", type: "password" },
      { k: "datastore", label: "Datastore (pbs)" },
      { k: "fingerprint", label: "Fingerprint (pbs)" },
      { k: "vgname", label: "Volume group (lvm)" },
      { k: "thinpool", label: "Thin pool (lvmthin)" },
      { k: "pool", label: "Pool (zfspool/rbd)" },
      { k: "nodes", label: "Restrict to nodes" },
      { k: "prune-backups", label: "Retention", hint: "keep-last=3" },
      { k: "shared", label: "Shared", type: "bool" },
      { k: "disable", label: "Disabled", type: "bool" },
    ],
  },
  {
    coll: "metrics",
    title: "Metric servers",
    about: "Send node and guest metrics to InfluxDB or Graphite.",
    id: "id",
    cols: [["id", "ID"], ["type", "Type"], ["server", "Server"], ["port", "Port"], ["disable", "Disabled"]],
    fixed: ["id", "type"],
    fields: [
      { k: "id", label: "ID", required: true, create: true },
      { k: "type", label: "Type", type: "select", options: ["influxdb", "graphite", "opentelemetry"], create: true },
      { k: "server", label: "Server", required: true },
      { k: "port", label: "Port", type: "number", required: true },
      { k: "influxdbproto", label: "InfluxDB protocol", type: "select", options: ["", "udp", "http", "https"] },
      { k: "organization", label: "Organization" },
      { k: "bucket", label: "Bucket" },
      { k: "token", label: "Token", type: "password" },
      { k: "disable", label: "Disabled", type: "bool" },
    ],
  },
];

// The Datacenter sections, grouped the way the menu shows them.
const groups: { title: string; icon: ReactNode; colls: string[] }[] = [
  { title: "Cluster", icon: <Settings2 />, colls: ["status", "tasks", "log", "options", "storage", "metrics"] },
  { title: "Backup & HA", icon: <ArchiveRestore />, colls: ["backup", "replication", "ha-resources", "ha-groups", "ha-rules"] },
  { title: "Access", icon: <UsersRound />, colls: ["users", "groups", "roles", "acl", "realms", "pools"] },
  { title: "Firewall", icon: <Shield />, colls: ["fw-options", "fw-rules", "fw-groups", "fw-aliases", "fw-ipsets"] },
  { title: "SDN", icon: <Network />, colls: ["sdn-zones", "sdn-vnets"] },
];

// DatacenterView is cluster-wide settings: a summary of the cluster and a
// grouped menu of its configuration sections.
export function DatacenterView({ cluster, res }: { cluster: PVECluster; res: PVEResources }) {
  const [cur, setCur] = useState(sections[0].coll);
  const sec = sections.find((s) => s.coll === cur)!;
  const title = (c: string) => sections.find((s) => s.coll === c)?.title ?? c;
  return (
    <div className="grid gap-4">
      <DatacenterSummary res={res} />
      <div className="grid gap-4 lg:grid-cols-[14rem_minmax(0,1fr)] lg:items-start">
        <Select className="lg:hidden" value={cur} onChange={(e) => setCur(e.target.value)} aria-label="Datacenter section">
          {groups.map((g) => (
            <optgroup key={g.title} label={g.title}>
              {g.colls.map((c) => (
                <option key={c} value={c}>
                  {title(c)}
                </option>
              ))}
            </optgroup>
          ))}
        </Select>
        <Card className="hidden p-2 lg:sticky lg:top-20 lg:block">
          <nav className="grid gap-3" aria-label="Datacenter sections">
            {groups.map((g) => (
              <div key={g.title}>
                <div className="flex items-center gap-2 px-2 pt-1 pb-1 text-[11px] font-semibold tracking-wider text-muted-foreground uppercase [&_svg]:size-3.5">
                  {g.icon} {g.title}
                </div>
                <ul className="grid gap-0.5">
                  {g.colls.map((c) => (
                    <li key={c}>
                      <button
                        onClick={() => setCur(c)}
                        aria-current={cur === c ? "page" : undefined}
                        className={`w-full cursor-pointer rounded-md px-2.5 py-1.5 text-left text-sm ${cur === c ? "bg-brand/15 font-medium text-foreground" : "text-muted-foreground hover:bg-accent hover:text-foreground"}`}
                      >
                        {title(c)}
                      </button>
                    </li>
                  ))}
                </ul>
              </div>
            ))}
          </nav>
        </Card>
        <div className="min-w-0">{sec.singleton ? <SingletonSection key={sec.coll} cluster={cluster.id} sec={sec} /> : <CollectionSection key={sec.coll} cluster={cluster.id} sec={sec} />}</div>
      </div>
    </div>
  );
}

// DatacenterSummary is the cluster at a glance: nodes, guests, CPU, memory
// and storage across all online nodes.
function DatacenterSummary({ res }: { res: PVEResources }) {
  const nodes = res.nodes ?? [];
  const on = nodes.filter((n) => n.status === "online");
  const guests = (res.guests ?? []).filter((g) => !g.template);
  const running = guests.filter((g) => g.status === "running").length;
  const cores = on.reduce((a, n) => a + n.maxcpu, 0);
  const cpu = cores ? (100 * on.reduce((a, n) => a + n.cpu * n.maxcpu, 0)) / cores : 0;
  const mem = on.reduce((a, n) => a + n.mem, 0);
  const maxmem = on.reduce((a, n) => a + n.maxmem, 0);
  // Shared storage is listed once per node; count it once.
  const seen = new Set<string>();
  let disk = 0,
    maxdisk = 0;
  for (const st of res.storage ?? []) {
    const k = st.shared ? (st.storage ?? st.id) : st.id;
    if (seen.has(k) || st.status !== "available") continue;
    seen.add(k);
    disk += st.disk;
    maxdisk += st.maxdisk;
  }
  const tiles = [
    { label: "Nodes", value: `${on.length}/${nodes.length}`, sub: on.length === nodes.length ? "all online" : `${nodes.length - on.length} offline`, v: null },
    { label: "Guests", value: `${running}`, sub: `running of ${guests.length}`, v: null },
    { label: "CPU", value: `${Math.round(cpu)}%`, sub: `${cores} cores online`, v: cpu },
    { label: "Memory", value: `${Math.round(maxmem ? (100 * mem) / maxmem : 0)}%`, sub: `${formatBytes(mem)} of ${formatBytes(maxmem)}`, v: maxmem ? (100 * mem) / maxmem : 0 },
    { label: "Storage", value: `${Math.round(maxdisk ? (100 * disk) / maxdisk : 0)}%`, sub: `${formatBytes(disk)} of ${formatBytes(maxdisk)}`, v: maxdisk ? (100 * disk) / maxdisk : 0 },
  ];
  return (
    <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 xl:grid-cols-5">
      {tiles.map((t) => (
        <Card key={t.label} className="p-4">
          <div className="text-xs font-semibold tracking-wider text-muted-foreground uppercase">{t.label}</div>
          <div className="tabular mt-1 text-2xl font-bold">{t.value}</div>
          {t.v !== null && <Meter value={t.v} className="mt-2" />}
          <div className="mt-1 truncate text-xs text-muted-foreground">{t.sub}</div>
        </Card>
      ))}
    </div>
  );
}

function cellText(k: string, v: unknown) {
  if ((k === "starttime" || k === "time") && typeof v === "number") return new Date(v * 1000).toLocaleString();
  if (v === 1 && (k === "enable" || k === "enabled" || k === "shared" || k === "disable" || k === "online" || k === "quorate" || k === "propagate" || k === "special")) return "yes";
  if (v === 0 && (k === "enable" || k === "enabled" || k === "shared" || k === "disable" || k === "online" || k === "quorate" || k === "propagate" || k === "special")) return "no";
  return str(v);
}

function CollectionSection({ cluster, sec }: { cluster: string; sec: Section }) {
  const { data, err, load } = useLoad(() => pve.dcList<Row[]>(cluster, sec.coll), [cluster, sec.coll]);
  const [edit, setEdit] = useState<Row | "new" | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const rows = Array.isArray(data) ? data : [];
  const keyOf = sec.keyOf ?? ((r: Row) => str(r[sec.id]));
  const remove = async (r: Row) => {
    const key = keyOf(r);
    const label = sec.coll === "acl" ? `${str(r.roleid)} on ${str(r.path)} for ${str(r.ugid)}` : key;
    if (!confirm(`Delete ${label}?`)) return;
    try {
      if (sec.coll === "acl") {
        const p: Params = { path: str(r.path), roles: str(r.roleid), delete: true };
        p[r.type === "group" ? "groups" : r.type === "token" ? "tokens" : "users"] = str(r.ugid);
        await pve.dcCreate(cluster, "acl", p);
      } else {
        await pve.dcDelete(cluster, sec.coll, key);
      }
      setMsg({ ok: true, text: `${label} deleted.` });
    } catch (e) {
      setMsg({ ok: false, text: errText(e) });
    }
    void load();
  };
  return (
    <Card>
      <CardHeader className="flex-row flex-wrap items-start justify-between gap-2">
        <div>
          <CardTitle>{sec.title}</CardTitle>
          {sec.about && <CardDescription>{sec.about}</CardDescription>}
        </div>
        <div className="flex gap-2">
          {sec.coll.startsWith("sdn-") && (
            <Button
              size="sm"
              variant="brand"
              onClick={async () => {
                try {
                  await pve.sdnApply(cluster);
                  setMsg({ ok: true, text: "SDN apply started." });
                } catch (e) {
                  setMsg({ ok: false, text: errText(e) });
                }
              }}
            >
              Apply SDN
            </Button>
          )}
          {!sec.readOnly && (
            <Button size="sm" variant="secondary" onClick={() => setEdit("new")}>
              <Plus /> Add
            </Button>
          )}
          <Button size="icon" variant="ghost" aria-label="Refresh" onClick={() => void load()}>
            <RefreshCw />
          </Button>
        </div>
      </CardHeader>
      <CardContent className="grid gap-2">
        <Note msg={msg} />
        {err && <p className="text-sm text-destructive">{err}</p>}
        <Table
          rows={rows}
          cols={sec.cols.map(([k, l]) => [k, l, (r: Row) => cellText(k, r[k])])}
          actions={
            sec.readOnly
              ? undefined
              : (r) => (
                  <div className="flex justify-end gap-1">
                    {sec.items && (
                      <Button size="sm" variant="ghost" onClick={() => setOpen(keyOf(r))}>
                        Open
                      </Button>
                    )}
                    {sec.coll !== "acl" && !(sec.coll === "roles" && r.special) && (
                      <Button size="icon" variant="ghost" className="size-7" aria-label="Edit" onClick={() => setEdit(r)}>
                        <Pencil />
                      </Button>
                    )}
                    {!(sec.coll === "roles" && r.special) && (
                      <Button size="icon" variant="ghost" className="size-7 text-destructive" aria-label="Delete" onClick={() => remove(r)}>
                        <Trash2 />
                      </Button>
                    )}
                  </div>
                )
          }
        />
      </CardContent>
      {edit && (
        <EntryDialog
          title={edit === "new" ? `Add to ${sec.title}` : `Edit ${keyOf(edit)}`}
          fields={(sec.fields ?? []).filter((f) => edit === "new" || !f.create)}
          initial={edit === "new" ? {} : edit}
          onClose={() => setEdit(null)}
          run={async (p) => {
            if (edit === "new") await pve.dcCreate(cluster, sec.coll, p);
            else {
              for (const k of sec.fixed ?? []) delete p[k];
              await pve.dcUpdate(cluster, sec.coll, keyOf(edit), p);
            }
            setMsg({ ok: true, text: "Saved." });
            void load();
          }}
        />
      )}
      {open && sec.items && <ItemsDialog cluster={cluster} sec={sec} entry={open} onClose={() => setOpen(null)} />}
    </Card>
  );
}

function SingletonSection({ cluster, sec }: { cluster: string; sec: Section }) {
  const { data, err, load } = useLoad(() => pve.dcList<Row>(cluster, sec.coll), [cluster, sec.coll]);
  const [edit, setEdit] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const entries = Object.entries(data ?? {}).filter(([k]) => k !== "digest");
  return (
    <Card>
      <CardHeader className="flex-row flex-wrap items-start justify-between gap-2">
        <div>
          <CardTitle>{sec.title}</CardTitle>
          {sec.about && <CardDescription>{sec.about}</CardDescription>}
        </div>
        <Button size="sm" variant="secondary" onClick={() => setEdit(true)}>
          <Pencil /> Edit
        </Button>
      </CardHeader>
      <CardContent className="grid gap-2">
        <Note msg={msg} />
        {err && <p className="text-sm text-destructive">{err}</p>}
        {entries.length === 0 ? (
          <p className="text-sm text-muted-foreground">Defaults.</p>
        ) : (
          <dl className="divide-y text-sm">
            {entries.map(([k, v]) => (
              <div key={k} className="grid grid-cols-[10rem_1fr] gap-3 py-1.5">
                <dt className="text-muted-foreground">{k}</dt>
                <dd className="font-mono text-xs break-all">{str(v)}</dd>
              </div>
            ))}
          </dl>
        )}
      </CardContent>
      {edit && (
        <EntryDialog
          title={`Edit ${sec.title.toLowerCase()}`}
          fields={sec.fields ?? []}
          initial={data ?? {}}
          onClose={() => setEdit(false)}
          run={async (p) => {
            if (data?.digest) p.digest = str(data.digest);
            await pve.dcUpdate(cluster, sec.coll, null, p);
            setMsg({ ok: true, text: "Saved." });
            void load();
          }}
        />
      )}
    </Card>
  );
}

function ItemsDialog({ cluster, sec, entry, onClose }: { cluster: string; sec: Section; entry: string; onClose: () => void }) {
  const items = sec.items!;
  const { data, err, load } = useLoad(() => pve.dcItems(cluster, sec.coll, entry), [cluster, sec.coll, entry]);
  const [add, setAdd] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>
            {sec.title} · {entry}
          </DialogTitle>
        </DialogHeader>
        <div className="flex justify-end">
          <Button size="sm" variant="secondary" onClick={() => setAdd(true)}>
            <Plus /> Add
          </Button>
        </div>
        <Note msg={msg} />
        {err && <p className="text-sm text-destructive">{err}</p>}
        <div className="max-h-[55vh] overflow-auto">
          <Table
            rows={Array.isArray(data) ? data : []}
            cols={items.cols.map(([k, l]) => [k, l, (r: Row) => cellText(k, r[k])])}
            actions={(r) => (
              <Button
                size="icon"
                variant="ghost"
                className="size-7 text-destructive"
                aria-label="Delete"
                onClick={async () => {
                  if (!confirm("Delete this entry?")) return;
                  try {
                    await pve.dcItemDelete(cluster, sec.coll, entry, str(r[items.id]));
                  } catch (e) {
                    setMsg({ ok: false, text: errText(e) });
                  }
                  void load();
                }}
              >
                <Trash2 />
              </Button>
            )}
          />
        </div>
        {add && (
          <EntryDialog
            title="Add"
            fields={items.fields}
            initial={{}}
            onClose={() => setAdd(false)}
            run={async (p) => {
              await pve.dcItemAdd(cluster, sec.coll, entry, p);
              void load();
            }}
          />
        )}
      </DialogContent>
    </Dialog>
  );
}

// EntryDialog edits the fields of a section plus free key=value lines for
// anything the form does not name.
function EntryDialog({ title, fields, initial, onClose, run }: { title: string; fields: FieldSpec[]; initial: Row; onClose: () => void; run: (p: Params) => Promise<void> }) {
  const [v, setV] = useState<Record<string, string | boolean>>(() => {
    const o: Record<string, string | boolean> = {};
    for (const f of fields) o[f.k] = f.type === "bool" ? initial[f.k] === 1 || initial[f.k] === true || initial[f.k] === "1" : f.type === "password" ? "" : str(initial[f.k]);
    return o;
  });
  const [extra, setExtra] = useState("");
  return (
    <FormDialog
      wide
      title={title}
      submit="Save"
      onClose={onClose}
      onSubmit={async () => {
        const p: Params = {};
        for (const f of fields) {
          const x = v[f.k];
          if (f.type === "bool") {
            if (x || initial[f.k] !== undefined) p[f.k] = !!x;
          } else if (x !== "") p[f.k] = f.type === "number" ? Number(x) : (x as string);
        }
        for (const line of extra.split("\n")) {
          const t = line.trim();
          if (!t) continue;
          const i = t.indexOf("=");
          if (i < 1) throw new Error(`"${t}" is not key=value`);
          p[t.slice(0, i).trim()] = t.slice(i + 1).trim();
        }
        await run(p);
      }}
    >
      <div className="grid gap-3 sm:grid-cols-2">
        {fields.map((f) => (
          <div key={f.k} className={f.type === "textarea" ? "sm:col-span-2" : undefined}>
            {f.type === "bool" ? (
              <div className="pt-6">
                <Check label={f.label} v={!!v[f.k]} set={(b) => setV((o) => ({ ...o, [f.k]: b }))} />
              </div>
            ) : (
              <Field label={f.label} hint={f.hint}>
                {f.type === "select" ? (
                  <Select value={String(v[f.k])} onChange={(e) => setV((o) => ({ ...o, [f.k]: e.target.value }))} required={f.required}>
                    {!f.options?.includes("") && !f.required && <option value="">—</option>}
                    {f.options?.map((o) => (
                      <option key={o} value={o}>
                        {o || "—"}
                      </option>
                    ))}
                  </Select>
                ) : f.type === "textarea" ? (
                  <Textarea rows={4} value={String(v[f.k])} onChange={(e) => setV((o) => ({ ...o, [f.k]: e.target.value }))} required={f.required} />
                ) : (
                  <Input
                    type={f.type === "number" ? "number" : f.type === "password" ? "password" : "text"}
                    autoComplete={f.type === "password" ? "new-password" : "off"}
                    value={String(v[f.k])}
                    onChange={(e) => setV((o) => ({ ...o, [f.k]: e.target.value }))}
                    required={f.required}
                  />
                )}
              </Field>
            )}
          </div>
        ))}
      </div>
      <Field label="More parameters" hint="One Proxmox parameter per line, key=value, for anything not in the form.">
        <Textarea rows={3} className="font-mono text-xs" value={extra} onChange={(e) => setExtra(e.target.value)} />
      </Field>
    </FormDialog>
  );
}
