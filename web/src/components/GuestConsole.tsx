import { useEffect, useRef, useState } from "react";
import { Terminal as XTerm } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { Keyboard, Monitor, RotateCcw, TerminalSquare, X } from "lucide-react";
import { api, type PVEGuestRef } from "@/lib/api";
import { Button } from "@/components/ui/button";

type Mode = "auto" | "vga" | "serial";
type State = "connecting" | "open" | "closed";

// GuestConsole is a full-window console for a Proxmox guest: noVNC for the
// display, xterm.js for containers and serial ports. Escape and other keys
// go to the guest, so it only closes with its own button.
export function GuestConsole({ cluster, guest, name, onClose }: { cluster: string; guest: PVEGuestRef; name: string; onClose: () => void }) {
  const host = useRef<HTMLDivElement>(null);
  const [want, setWant] = useState<Mode>("auto");
  const [mode, setMode] = useState<"vga" | "term" | null>(null);
  const [state, setState] = useState<State>("connecting");
  const [note, setNote] = useState("");
  const [session, setSession] = useState(0);
  const cad = useRef<(() => void) | null>(null);

  useEffect(() => {
    const el = host.current;
    if (!el) return;
    let cleanup = () => {};
    let gone = false;
    setState("connecting");
    setNote("");
    cad.current = null;
    const wsURL = (sid: string) => `${location.protocol === "https:" ? "wss" : "ws"}://${location.host}/api/v1/proxmox-console/${sid}`;

    (async () => {
      let c;
      try {
        c = await api.pveConsole(cluster, guest, want);
      } catch (e) {
        setState("closed");
        setNote((e as Error).message);
        return;
      }
      if (gone) return;
      setMode(c.mode);
      if (c.mode === "vga") {
        const { default: RFB } = await import("@novnc/novnc");
        if (gone) return;
        const rfb = new RFB(el, wsURL(c.sid), { credentials: { password: c.password }, wsProtocols: ["binary"] });
        rfb.scaleViewport = true;
        rfb.background = "#000";
        rfb.addEventListener("connect", () => {
          setState("open");
          rfb.focus();
        });
        rfb.addEventListener("disconnect", (e) => {
          setState("closed");
          setNote((e as CustomEvent<{ clean: boolean }>).detail?.clean ? "Disconnected." : "The console connection was lost.");
        });
        rfb.addEventListener("credentialsrequired", () => rfb.sendCredentials({ password: c.password }));
        cad.current = () => rfb.sendCtrlAltDel();
        cleanup = () => rfb.disconnect();
        return;
      }
      const term = new XTerm({
        cursorBlink: true,
        fontFamily: 'ui-monospace, "JetBrains Mono", Menlo, Consolas, monospace',
        fontSize: 14,
        scrollback: 5000,
        theme: { background: "#000000", foreground: "#e6e6e6", cursor: "#ff9d4d", selectionBackground: "#5a3a22" },
      });
      const fit = new FitAddon();
      term.loadAddon(fit);
      term.open(el);
      fit.fit();
      const ws = new WebSocket(wsURL(c.sid));
      ws.binaryType = "arraybuffer";
      const send = (m: object) => ws.readyState === WebSocket.OPEN && ws.send(JSON.stringify(m));
      ws.onopen = () => {
        setState("open");
        send({ type: "resize", cols: term.cols, rows: term.rows });
        send({ type: "input", data: "\r" }); // wake a login prompt
        term.focus();
      };
      ws.onmessage = (ev) => typeof ev.data !== "string" && term.write(new Uint8Array(ev.data));
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
          /* hidden */
        }
      });
      ro.observe(el);
      cleanup = () => {
        ro.disconnect();
        dataSub.dispose();
        resizeSub.dispose();
        ws.close();
        term.dispose();
      };
    })();
    return () => {
      gone = true;
      cleanup();
      el.replaceChildren();
    };
  }, [cluster, guest, want, session]);

  return (
    <div className="fixed inset-0 z-50 grid grid-rows-[auto_1fr] bg-background/95 backdrop-blur" role="dialog" aria-label={`Console of ${name}`}>
      <div className="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-2">
        <div className="flex min-w-0 items-center gap-2 text-sm">
          <span className={`size-2 shrink-0 rounded-full ${state === "open" ? "bg-success" : state === "connecting" ? "animate-pulse bg-warning" : "bg-muted-foreground/50"}`} />
          <span className="truncate font-medium">{name}</span>
          <span className="truncate text-xs text-muted-foreground">
            {state === "open" ? `${mode === "vga" ? "display" : "text console"} · audit-logged` : state === "connecting" ? "connecting…" : note}
          </span>
        </div>
        <div className="flex flex-wrap gap-1">
          {guest.type === "qemu" && (
            <>
              <Button size="sm" variant={mode === "vga" ? "secondary" : "ghost"} onClick={() => setWant("vga")} title="Graphical display (noVNC)">
                <Monitor /> Display
              </Button>
              <Button size="sm" variant={mode === "term" ? "secondary" : "ghost"} onClick={() => setWant("serial")} title="Serial console (needs a serial port on the VM)">
                <TerminalSquare /> Serial
              </Button>
            </>
          )}
          {mode === "vga" && state === "open" && (
            <Button size="sm" variant="ghost" onClick={() => cad.current?.()}>
              <Keyboard /> Ctrl+Alt+Del
            </Button>
          )}
          {state === "closed" && (
            <Button size="sm" variant="secondary" onClick={() => setSession((n) => n + 1)}>
              <RotateCcw /> Reconnect
            </Button>
          )}
          <Button size="sm" variant="ghost" onClick={onClose} aria-label="Close console">
            <X /> Close
          </Button>
        </div>
      </div>
      <div ref={host} className="min-h-0 overflow-hidden bg-black p-1" />
    </div>
  );
}
