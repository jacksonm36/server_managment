import { useEffect, useState } from "react";
import { Playbooks, Runs } from "./automation/Playbooks";
import { Schedules } from "./automation/Schedules";

const TABS = [
  { id: "playbooks", label: "Playbooks" },
  { id: "runs", label: "Runs" },
  { id: "schedules", label: "Schedules" },
] as const;
type TabId = (typeof TABS)[number]["id"];

function readTab(): TabId {
  const h = window.location.hash.slice(1);
  return (TABS.find((t) => t.id === h)?.id ?? "playbooks") as TabId;
}

export function Automation() {
  const [tab, setTab] = useState<TabId>(readTab);
  useEffect(() => {
    const on = () => setTab(readTab());
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);
  return (
    <div className="grid gap-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Automation</h1>
        <p className="text-sm text-muted-foreground">Reusable playbooks, run history, schedules and alerts.</p>
      </div>
      <div role="tablist" className="-mx-1 flex gap-1 overflow-x-auto border-b px-1">
        {TABS.map((t) => (
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
      {tab === "schedules" && <Schedules />}
    </div>
  );
}
