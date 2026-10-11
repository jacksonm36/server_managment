import { useEffect, useMemo, useState } from "react";
import { useAccess } from "@/lib/access";
import { Playbooks, Runs } from "./automation/Playbooks";
import { Schedules } from "./automation/Schedules";
import { Alerts, Notifications } from "./automation/Alerts";
import { Ansible } from "./automation/Ansible";
import { Tofu } from "./automation/Tofu";

const TABS = [
  { id: "playbooks", label: "Playbooks", perm: "automation.view" },
  { id: "runs", label: "Runs", perm: "automation.view" },
  { id: "ansible", label: "Ansible", perm: "ansible.view" },
  { id: "tofu", label: "OpenTofu", perm: "tofu.view" },
  { id: "schedules", label: "Schedules", perm: "automation.view" },
  { id: "alerts", label: "Alerts", perm: "automation.view" },
  { id: "notifications", label: "Notifications", perm: "notifications.manage" },
] as const;
type TabId = (typeof TABS)[number]["id"];

function readTab(tabs: readonly (typeof TABS)[number][]): TabId {
  const h = window.location.hash.slice(1);
  return (tabs.find((t) => t.id === h)?.id ?? tabs[0]?.id ?? "playbooks") as TabId;
}

export function Automation() {
  const { can } = useAccess();
  const tabs = useMemo(() => TABS.filter((t) => can(t.perm)), [can]);
  const [tab, setTab] = useState<TabId>(() => readTab(tabs));
  useEffect(() => {
    const on = () => setTab(readTab(tabs));
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, [tabs]);
  return (
    <div className="grid gap-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Automation</h1>
        <p className="text-sm text-muted-foreground">Reusable playbooks, Ansible, OpenTofu, run history, schedules and alerts.</p>
      </div>
      <div role="tablist" className="-mx-1 flex gap-1 overflow-x-auto border-b px-1">
        {tabs.map((t) => (
          <button
            key={t.id}
            role="tab"
            aria-selected={tab === t.id}
            onClick={() => {
              history.replaceState(null, "", `#${t.id}`);
              setTab(t.id);
            }}
            className={`-mb-px cursor-pointer border-b-2 px-3 py-2 text-sm whitespace-nowrap ${
              tab === t.id ? "border-brand text-foreground" : "border-transparent text-muted-foreground hover:text-foreground"
            }`}
          >
            {t.label}
          </button>
        ))}
      </div>
      {tab === "playbooks" && <Playbooks />}
      {tab === "runs" && <Runs />}
      {tab === "ansible" && <Ansible />}
      {tab === "tofu" && <Tofu />}
      {tab === "schedules" && <Schedules />}
      {tab === "alerts" && <Alerts />}
      {tab === "notifications" && <Notifications />}
    </div>
  );
}
