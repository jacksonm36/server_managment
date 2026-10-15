import { useEffect, useRef, useState } from "react";
import { Terminal as XTerm } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { Maximize2, Minimize2, RotateCcw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Offline } from "@/components/OpStatus";

type State = "connecting" | "open" | "closed";

export function Terminal({ serverId, online }: { serverId: string; online: boolean }) {
  const host = useRef<HTMLDivElement>(null);
  const [state, setState] = useState<State>("connecting");
  const [note, setNote] = useState("");
  const [session, setSession] = useState(0); // bump to reconnect
  const [full, setFull] = useState(false);

  useEffect(() => {
    if (!online || !host.current) return;
    const term = new XTerm({
      cursorBlink: true,
      fontFamily: 'ui-monospace, "JetBrains Mono", Menlo, Consolas, monospace',
      fontSize: 13,
      scrollback: 5000,
      theme: { background: "#0a0a0a", foreground: "#e6e6e6", cursor: "#ff9d4d", selectionBackground: "#5a3a22" },
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(host.current);
    fit.fit();
    setState("connecting");
    setNote("");

    const proto = location.protocol === "https:" ? "wss" : "ws";
    const ws = new WebSocket(`${proto}://${location.host}/api/v1/servers/${serverId}/terminal?cols=${term.cols}&rows=${term.rows}`);
    ws.binaryType = "arraybuffer";
    const send = (m: object) => ws.readyState === WebSocket.OPEN && ws.send(JSON.stringify(m));

    ws.onopen = () => {
      setState("open");
      term.focus();
    };
    ws.onmessage = (ev) => {
      if (typeof ev.data === "string") {
        const m = JSON.parse(ev.data);
        if (m.type === "exit") setNote(m.error ? `Session ended: ${m.error}` : `Shell exited (code ${m.code}).`);
        return;
      }
      term.write(new Uint8Array(ev.data));
    };
    ws.onclose = () => {
      setState("closed");
      setNote((n) => n || "Disconnected.");
    };
    const dataSub = term.onData((data) => send({ type: "input", data }));
    const resizeSub = term.onResize(({ cols, rows }) => send({ type: "resize", cols, rows }));
    const ro = new ResizeObserver(() => {
      try {
        fit.fit();
      } catch {
        /* element hidden */
      }
    });
    ro.observe(host.current);

    return () => {
      ro.disconnect();
      dataSub.dispose();
      resizeSub.dispose();
      ws.close();
      term.dispose();
    };
  }, [serverId, online, session]);

  if (!online) return <Offline />;

  return (
    <Card className={`grid gap-2 overflow-hidden p-2 ${full ? "fixed inset-2 z-50 grid-rows-[auto_1fr]" : ""}`}>
      <div className="flex items-center justify-between gap-2 px-1">
        <span className="flex items-center gap-2 text-xs text-muted-foreground">
          <span className={`size-2 rounded-full ${state === "open" ? "bg-success" : state === "connecting" ? "animate-pulse bg-warning" : "bg-muted-foreground/50"}`} />
          {state === "open" ? "root shell · audit-logged" : state === "connecting" ? "connecting…" : note}
        </span>
        <div className="flex gap-1">
          {state === "closed" && (
            <Button variant="secondary" size="sm" onClick={() => setSession((n) => n + 1)}>
              <RotateCcw /> Reconnect
            </Button>
          )}
          <Button variant="ghost" size="icon" className="size-8" aria-label={full ? "Exit full screen" : "Full screen"} onClick={() => setFull(!full)}>
            {full ? <Minimize2 /> : <Maximize2 />}
          </Button>
        </div>
      </div>
      <div ref={host} className={`${full ? "h-full" : "h-[32rem]"} min-h-0 rounded-md bg-[#0a0a0a] p-2`} />
    </Card>
  );
}
