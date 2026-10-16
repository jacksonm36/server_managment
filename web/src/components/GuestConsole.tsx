import { useEffect, useRef, useState } from "react";
import { Terminal as XTerm } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import {
  AArrowDown,
  AArrowUp,
  Camera,
  ClipboardCopy,
  ClipboardPaste,
  Expand,
  ExternalLink,
  Maximize,
  Minimize,
  Monitor,
  RotateCcw,
  Scaling,
  Shrink,
  TerminalSquare,
  X,
} from "lucide-react";
import { api, type PVEGuestRef } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Select, Textarea } from "@/components/ui/select";

type Mode = "auto" | "vga" | "serial";
type State = "connecting" | "open" | "closed";

type Opened = { sid: string; mode: "vga" | "term" };

// Display quality: lower JPEG quality means smaller updates, so more of
// them per second on a slow link; less compression costs bandwidth but
// saves CPU, which helps on a LAN.
export type Quality = "smooth" | "balanced" | "sharp";
const qualities: Record<Quality, { label: string; quality: number; compression: number }> = {
  smooth: { label: "Smooth", quality: 3, compression: 1 },
  balanced: { label: "Balanced", quality: 6, compression: 2 },
  sharp: { label: "Sharp", quality: 9, compression: 0 },
};
const QUALITY_KEY = "sm.console.quality";
function savedQuality(): Quality {
  try {
    const q = localStorage.getItem(QUALITY_KEY);
    return q === "smooth" || q === "sharp" ? q : "balanced";
  } catch {
    return "balanced";
  }
}

// Console controls the full window's toolbar can call.
type Controls = {
  keys?: (combo: number[]) => void; // keysyms pressed in order, released in reverse
  type?: (text: string, cancelled: () => boolean, progress: (done: number) => void) => Promise<void>; // keystrokes into the display
  clip?: (text: string) => void; // onto the VM's clipboard (shared clipboard)
  paste?: (text: string) => void; // into a text console (bracketed paste when the shell asks for it)
  input?: (data: string) => void; // raw bytes into a text console
  selection?: () => string;
  quality?: (q: Quality) => void;
  scale?: (fit: boolean) => void;
  font?: (delta: number) => void;
  screenshot?: () => Promise<Blob | null>;
};

// X11 keysyms for the special keys menu.
const XK = {
  ctrl: 0xffe3,
  alt: 0xffe9,
  shift: 0xffe1,
  win: 0xffeb,
  del: 0xffff,
  backspace: 0xff08,
  tab: 0xff09,
  esc: 0xff1b,
  print: 0xff61,
  f: (n: number) => 0xffbe + n - 1,
};
const vgaKeys: { label: string; combo: number[] }[] = [
  { label: "Ctrl+Alt+Del", combo: [XK.ctrl, XK.alt, XK.del] },
  { label: "Ctrl+Alt+Backspace", combo: [XK.ctrl, XK.alt, XK.backspace] },
  ...[1, 2, 3, 4, 5, 6, 7].map((n) => ({ label: `Ctrl+Alt+F${n}`, combo: [XK.ctrl, XK.alt, XK.f(n)] })),
  { label: "Alt+Tab", combo: [XK.alt, XK.tab] },
  { label: "Alt+F4", combo: [XK.alt, XK.f(4)] },
  { label: "Windows key", combo: [XK.win] },
  { label: "Windows+R", combo: [XK.win, 0x72] },
  { label: "Windows+L (lock)", combo: [XK.win, 0x6c] },
  { label: "Escape", combo: [XK.esc] },
  { label: "Print Screen", combo: [XK.print] },
];
const termKeys: { label: string; data: string }[] = [
  { label: "Ctrl+C (interrupt)", data: "\x03" },
  { label: "Ctrl+D (end of input)", data: "\x04" },
  { label: "Ctrl+Z (suspend)", data: "\x1a" },
  { label: "Ctrl+\\ (quit)", data: "\x1c" },
  { label: "Ctrl+L (clear)", data: "\x0c" },
  { label: "Ctrl+R (history search)", data: "\x12" },
  { label: "Escape", data: "\x1b" },
  { label: "Tab", data: "\t" },
];

const FONT_KEY = "sm.console.font";
function savedFont(): number {
  try {
    const n = Number(localStorage.getItem(FONT_KEY));
    return n >= 9 && n <= 28 ? n : 14;
  } catch {
    return 14;
  }
}
function save(key: string, v: string) {
  try {
    localStorage.setItem(key, v);
  } catch {
    /* private window */
  }
}

// Characters that need Shift on a US keyboard, which is what the guest's
// keymap assumes for typed text.
const shifted = new Set('~!@#$%^&*()_+{}|:"<>?ABCDEFGHIJKLMNOPQRSTUVWXYZ');
// keysym is the X11 keysym for a character: Latin-1 maps to itself,
// anything else to the Unicode range.
function keysym(ch: string): number | null {
  if (ch === "\n") return 0xff0d;
  if (ch === "\t") return 0xff09;
  const c = ch.codePointAt(0) ?? 0;
  if (c >= 0x20 && c <= 0x7e) return c;
  if (c >= 0xa0 && c <= 0xff) return c;
  if (c > 0xff) return 0x01000000 | c;
  return null;
}
const wait = (ms: number) => new Promise((r) => setTimeout(r, ms));

// clean drops control characters (escape sequences, C1 controls) from text
// crossing between this computer and a guest, keeping tabs and line
// breaks. Without it, pasted or copied text could carry a sequence that
// ends a shell's bracketed paste early and runs what follows, or that
// drives the terminal itself.
export function clean(t: string): string {
  // eslint-disable-next-line no-control-regex
  return t.replace(/[\x00-\x08\x0b\x0c\x0e-\x1f\x7f-\x9f]/g, "");
}

type Hooks = {
  sharedClip?: boolean; // the VM's display shares its clipboard (vga ...,clipboard=vnc)
  onPasteKey?: () => void; // Ctrl+Shift+V on a display without a shared clipboard
  onCopied?: (msg: string) => void;
};

// useGuestConsole connects a console into host: noVNC for the display,
// xterm.js for containers and serial ports. A preview only watches: it
// takes no keyboard or mouse input and uses a smaller font.
function useGuestConsole(
  host: React.RefObject<HTMLDivElement | null>,
  {
    cluster,
    guest,
    want,
    session,
    open,
    preview,
    hooks,
  }: { cluster: string; guest: PVEGuestRef; want: Mode; session: number; open?: () => Promise<Opened>; preview?: boolean; hooks?: Hooks },
) {
  const [mode, setMode] = useState<"vga" | "term" | null>(null);
  const [state, setState] = useState<State>("connecting");
  const [note, setNote] = useState("");
  const [size, setSize] = useState<{ w: number; h: number } | null>(null);
  const ctl = useRef<Controls>({});
  const [guestClip, setGuestClip] = useState("");
  // Read at event time, so changing them doesn't reconnect.
  const hk = useRef<Hooks>({});
  hk.current = hooks ?? {};
  useEffect(() => {
    const el = host.current;
    if (!el) return;
    let cleanup = () => {};
    let gone = false;
    setState("connecting");
    setNote("");
    setSize(null);
    ctl.current = {};
    setGuestClip("");
    const wsURL = (sid: string) => `${location.protocol === "https:" ? "wss" : "ws"}://${location.host}/api/v1/proxmox-console/${sid}`;

    (async () => {
      let c;
      try {
        c = await (open ? open() : api.pveConsole(cluster, guest, want));
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
        // The panel logs in to the display itself; noVNC holds no password.
        const rfb = new RFB(el, wsURL(c.sid), { wsProtocols: ["binary"] });
        rfb.scaleViewport = true;
        rfb.viewOnly = !!preview;
        rfb.background = "#000";
        rfb.addEventListener("connect", () => {
          setState("open");
          if (!preview) rfb.focus();
        });
        rfb.addEventListener("disconnect", (e) => {
          setState("closed");
          setNote((e as CustomEvent<{ clean: boolean }>).detail?.clean ? "Disconnected." : "The console connection was lost.");
        });
        rfb.addEventListener("credentialsrequired", () => {
          setNote("The display asked for a password; the panel should have answered it.");
          rfb.disconnect();
        });
        const q = qualities[savedQuality()];
        rfb.qualityLevel = q.quality;
        rfb.compressionLevel = q.compression;
        // The guest's clipboard, when it shares one (spice-vdagent or the
        // Windows guest tools). It reaches this computer's clipboard only
        // right after the user pressed copy (Ctrl+C, Ctrl+X, Ctrl+Insert) in
        // the console; otherwise it waits behind "Copy from VM". A guest
        // can't slip text onto the clipboard on its own.
        let copyKeyAt = 0;
        rfb.addEventListener("clipboard", (e) => {
          const t = clean(String((e as CustomEvent<{ text: string }>).detail?.text ?? "").slice(0, 100000));
          setGuestClip(t);
          if (!preview && t && Date.now() - copyKeyAt < 2000 && document.hasFocus() && el.contains(document.activeElement))
            navigator.clipboard?.writeText(t).then(
              () => hk.current.onCopied?.("Copied from the VM."),
              () => {},
            );
        });
        // The display's real size, so a preview can match its shape.
        const canvas = () => el.querySelector("canvas");
        const sizer = setInterval(() => {
          const cv = canvas();
          if (cv && cv.width > 1 && cv.height > 1) setSize((s) => (s && s.w === cv.width && s.h === cv.height ? s : { w: cv.width, h: cv.height }));
        }, 1000);
        const press = (combo: number[]) => {
          combo.forEach((k) => rfb.sendKey(k, null, true));
          [...combo].reverse().forEach((k) => rfb.sendKey(k, null, false));
        };
        // Keys the console handles before the VM sees them. With a shared
        // clipboard, Ctrl+V (and Ctrl+Shift+V) first puts this computer's
        // clipboard on the VM's, then lets the keystroke through, so it
        // pastes like on a local machine. Without one, Ctrl+Shift+V opens
        // the type-it dialog.
        const onKey = (e: KeyboardEvent) => {
          if (preview || !e.ctrlKey || e.altKey || e.metaKey) return;
          if (e.code === "KeyC" || e.code === "KeyX" || e.code === "Insert") copyKeyAt = Date.now();
          if (e.code !== "KeyV") return;
          if (hk.current.sharedClip) {
            e.stopPropagation(); // the paste event below sends the key
            return;
          }
          if (e.shiftKey) {
            e.stopPropagation();
            e.preventDefault();
            hk.current.onPasteKey?.();
          }
        };
        const onPaste = (e: ClipboardEvent) => {
          if (preview || !hk.current.sharedClip) return;
          e.preventDefault();
          const t = clean(e.clipboardData?.getData("text/plain") ?? "").slice(0, MAX_PASTE);
          if (t) rfb.clipboardPasteFrom(t);
          // Ctrl (and Shift) are already down on the VM; give its clipboard a
          // moment to update, then press V.
          setTimeout(() => {
            rfb.sendKey(0x76, "KeyV", true);
            rfb.sendKey(0x76, "KeyV", false);
          }, 80);
        };
        el.addEventListener("keydown", onKey, true);
        el.addEventListener("paste", onPaste, true);
        ctl.current = {
          keys: press,
          clip: (t) => rfb.clipboardPasteFrom(clean(t)),
          quality: (k) => {
            rfb.qualityLevel = qualities[k].quality;
            rfb.compressionLevel = qualities[k].compression;
          },
          scale: (fit) => {
            rfb.scaleViewport = fit;
          },
          screenshot: () => new Promise((res) => (canvas() ? canvas()!.toBlob(res, "image/png") : res(null))),
          type: async (text, cancelled, progress) => {
            const chars = [...clean(text.replace(/\r\n?/g, "\n"))];
            for (let i = 0; i < chars.length; i++) {
              if (cancelled() || gone) return;
              const k = keysym(chars[i]);
              if (k === null) continue;
              press(shifted.has(chars[i]) ? [XK.shift, k] : [k]);
              progress(i + 1);
              // A short pause keeps slow guests from dropping keys.
              await wait(chars[i] === "\n" ? 60 : 12);
            }
          },
        };
        cleanup = () => {
          clearInterval(sizer);
          el.removeEventListener("keydown", onKey, true);
          el.removeEventListener("paste", onPaste, true);
          rfb.disconnect();
        };
        return;
      }
      const term = new XTerm({
        cursorBlink: true,
        fontFamily: 'ui-monospace, "JetBrains Mono", Menlo, Consolas, monospace',
        fontSize: preview ? 10 : savedFont(),
        disableStdin: !!preview,
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
        if (!preview) term.focus();
      };
      ws.onmessage = (ev) => typeof ev.data !== "string" && term.write(new Uint8Array(ev.data));
      ws.onclose = () => {
        setState("closed");
        setNote((n) => n || "Disconnected.");
      };
      const dataSub = term.onData((data) => send({ type: "input", data }));
      const copySel = () => {
        const t = clean(term.getSelection());
        term.clearSelection();
        void copyText(t).then((ok) => hk.current.onCopied?.(ok ? "Copied." : "The browser blocked the clipboard."));
      };
      // Windows-style keys: Ctrl+C copies when text is selected and is the
      // usual interrupt (^C) when nothing is; Ctrl+V pastes. Ctrl+Shift+C/V
      // (Linux terminals) do the same. Pastes go through the browser, and
      // xterm wraps them in bracketed-paste markers when the shell asks, so
      // a multi-line paste is not run line by line.
      term.attachCustomKeyEventHandler((e) => {
        if (e.type !== "keydown" || !e.ctrlKey || e.altKey || e.metaKey) return true;
        if (e.code === "KeyC" && (e.shiftKey || term.hasSelection())) {
          if (term.hasSelection()) copySel();
          e.preventDefault();
          return false;
        }
        if (e.code === "KeyV") return false; // let the browser fire its paste event
        if (e.code === "Insert" && term.hasSelection()) {
          copySel();
          return false;
        }
        return true;
      });
      // Right-click copies the selection, or pastes when there is none (like
      // PuTTY and Windows Terminal).
      const onMenu = (e: MouseEvent) => {
        if (preview) return;
        e.preventDefault();
        if (term.hasSelection()) return copySel();
        navigator.clipboard?.readText().then(
          (t) => t && term.paste(clean(t).slice(0, MAX_PASTE)),
          () => hk.current.onCopied?.("Paste with Ctrl+V: the browser didn't allow reading the clipboard."),
        );
      };
      el.addEventListener("contextmenu", onMenu);
      // Browser pastes (Ctrl+V, the menu) are cleaned before xterm sees them.
      const onPaste = (e: ClipboardEvent) => {
        e.preventDefault();
        e.stopPropagation();
        const t = clean(e.clipboardData?.getData("text/plain") ?? "").slice(0, MAX_PASTE);
        if (t && !preview) term.paste(t);
      };
      el.addEventListener("paste", onPaste, true);
      ctl.current = {
        paste: (t) => term.paste(clean(t)),
        input: (d) => send({ type: "input", data: d }),
        selection: () => term.getSelection(),
        font: (d) => {
          const n = Math.min(28, Math.max(9, (term.options.fontSize ?? 14) + d));
          term.options.fontSize = n;
          save(FONT_KEY, String(n));
          fit.fit();
        },
      };
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
        el.removeEventListener("contextmenu", onMenu);
        el.removeEventListener("paste", onPaste, true);
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
    // open is read once per session (callers pass a fresh arrow each render).
  }, [cluster, guest, want, session, preview]);
  return { mode, state, note, ctl, guestClip, size };
}

// GuestConsole is a full-window console for a Proxmox guest. Escape and
// other keys go to the guest, so it only closes with its own button.
export function GuestConsole({
  cluster,
  guest,
  name,
  open,
  sharedClip,
  popout,
  onClose,
}: {
  cluster: string;
  guest: PVEGuestRef;
  name: string;
  // open replaces the guest console request, e.g. for a node shell.
  open?: () => Promise<Opened>;
  // sharedClip: the VM's display has clipboard=vnc.
  sharedClip?: boolean;
  // popout: this console is its own browser window.
  popout?: boolean;
  onClose: () => void;
}) {
  const root = useRef<HTMLDivElement>(null);
  const host = useRef<HTMLDivElement>(null);
  const [want, setWant] = useState<Mode>("auto");
  const [session, setSession] = useState(0);
  const [pasting, setPasting] = useState(false);
  const [toast, setToast] = useState("");
  const flash = (t: string) => {
    setToast(t);
    setTimeout(() => setToast((x) => (x === t ? "" : x)), 2500);
  };
  const { mode, state, note, ctl, guestClip } = useGuestConsole(host, {
    cluster,
    guest,
    want,
    session,
    open,
    hooks: { sharedClip, onPasteKey: () => setPasting(true), onCopied: flash },
  });
  const [quality, setQuality] = useState<Quality>(savedQuality);
  const [fit, setFit] = useState(true);
  const [full, setFull] = useState(false);
  useEffect(() => {
    const on = () => setFull(document.fullscreenElement === root.current);
    document.addEventListener("fullscreenchange", on);
    return () => document.removeEventListener("fullscreenchange", on);
  }, []);
  const refocus = () => (host.current?.querySelector("canvas, textarea") as HTMLElement | null)?.focus();

  return (
    <div ref={root} className="fixed inset-0 z-50 grid grid-rows-[auto_1fr] bg-background/95 backdrop-blur" role="dialog" aria-label={`Console of ${name}`}>
      <div className="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-2">
        <div className="flex min-w-0 items-center gap-2 text-sm">
          <span
            className={`size-2 shrink-0 rounded-full ${state === "open" ? "bg-success" : state === "connecting" ? "animate-pulse bg-warning" : "bg-muted-foreground/50"}`}
          />
          <span className="truncate font-medium">{name}</span>
          {location.protocol !== "https:" && (
            <span
              className="shrink-0 rounded bg-warning/15 px-1.5 py-0.5 text-[10px] font-medium text-warning"
              title="This page is plain http: keystrokes and the screen cross the network unencrypted unless a VPN carries them. Use the panel's https address."
            >
              not encrypted
            </span>
          )}
          <span className="truncate text-xs text-muted-foreground">
            {state === "open"
              ? `${mode === "vga" ? (sharedClip ? "display · shared clipboard" : "display") : "text console"} · audit-logged`
              : state === "connecting"
                ? "connecting…"
                : note}
          </span>
        </div>
        <div className="flex flex-wrap items-center gap-1">
          {guest.type === "qemu" && !open && (
            <>
              <Button size="sm" variant={mode === "vga" ? "secondary" : "ghost"} onClick={() => setWant("vga")} title="Graphical display (noVNC)">
                <Monitor /> Display
              </Button>
              <Button
                size="sm"
                variant={mode === "term" ? "secondary" : "ghost"}
                onClick={() => setWant("serial")}
                title="Serial console (needs a serial port on the VM)"
              >
                <TerminalSquare /> Serial
              </Button>
            </>
          )}
          {state === "open" && (
            <>
              {mode === "term" && (
                <Button
                  size="sm"
                  variant="ghost"
                  title="Copy the selected text (Ctrl+C with a selection, or right-click)"
                  onClick={async () => {
                    const t = ctl.current.selection?.() ?? "";
                    if (!t) return flash("Select text in the console first.");
                    flash((await copyText(t)) ? "Copied." : "The browser blocked the clipboard.");
                  }}
                >
                  <ClipboardCopy /> Copy
                </Button>
              )}
              {mode === "vga" && guestClip && (
                <Button
                  size="sm"
                  variant="ghost"
                  title="Copy what the VM put on its clipboard"
                  onClick={async () => flash((await copyText(guestClip)) ? "Copied the VM's clipboard." : "The browser blocked the clipboard.")}
                >
                  <ClipboardCopy /> Copy from VM
                </Button>
              )}
              <Button
                size="sm"
                variant="ghost"
                onClick={() => setPasting(true)}
                title={mode === "vga" ? (sharedClip ? "Send text to the VM (or just press Ctrl+V)" : "Type text into the VM (Ctrl+Shift+V)") : "Paste text (Ctrl+V)"}
              >
                <ClipboardPaste /> Paste
              </Button>
              <Select
                aria-label="Send keys"
                className="h-8 w-auto py-0 text-xs"
                value=""
                onChange={(e) => {
                  const i = Number(e.target.value);
                  if (mode === "vga") ctl.current.keys?.(vgaKeys[i].combo);
                  else ctl.current.input?.(termKeys[i].data);
                  refocus();
                }}
              >
                <option value="" disabled>
                  Keys…
                </option>
                {(mode === "vga" ? vgaKeys : termKeys).map((k, i) => (
                  <option key={k.label} value={i}>
                    {k.label}
                  </option>
                ))}
              </Select>
              {mode === "vga" && (
                <>
                  <Select
                    aria-label="Display quality"
                    className="h-8 w-auto py-0 text-xs"
                    value={quality}
                    title="Smooth sends more frames on a slow link; Sharp looks best on a LAN"
                    onChange={(e) => {
                      const q = e.target.value as Quality;
                      setQuality(q);
                      ctl.current.quality?.(q);
                      save(QUALITY_KEY, q);
                    }}
                  >
                    {(Object.keys(qualities) as Quality[]).map((q) => (
                      <option key={q} value={q}>
                        {qualities[q].label}
                      </option>
                    ))}
                  </Select>
                  <Button
                    size="sm"
                    variant="ghost"
                    title={fit ? "Show at actual size (scroll to see the rest)" : "Fit the display to the window"}
                    onClick={() => {
                      ctl.current.scale?.(!fit);
                      setFit(!fit);
                    }}
                  >
                    {fit ? <Scaling /> : <Shrink />} {fit ? "1:1" : "Fit"}
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    title="Save a screenshot (PNG)"
                    aria-label="Screenshot"
                    onClick={async () => {
                      const b = await ctl.current.screenshot?.();
                      if (!b) return flash("Nothing to capture yet.");
                      const a = document.createElement("a");
                      a.href = URL.createObjectURL(b);
                      a.download = `${name.replace(/[^\w.-]+/g, "_")}-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, "-")}.png`;
                      a.click();
                      setTimeout(() => URL.revokeObjectURL(a.href), 1000);
                    }}
                  >
                    <Camera />
                  </Button>
                </>
              )}
              {mode === "term" && (
                <>
                  <Button size="sm" variant="ghost" title="Smaller text" aria-label="Smaller text" onClick={() => ctl.current.font?.(-1)}>
                    <AArrowDown />
                  </Button>
                  <Button size="sm" variant="ghost" title="Larger text" aria-label="Larger text" onClick={() => ctl.current.font?.(1)}>
                    <AArrowUp />
                  </Button>
                </>
              )}
            </>
          )}
          {state === "closed" && (
            <Button size="sm" variant="secondary" onClick={() => setSession((n) => n + 1)}>
              <RotateCcw /> Reconnect
            </Button>
          )}
          <Button
            size="sm"
            variant="ghost"
            title={full ? "Leave full screen" : "Full screen"}
            aria-label={full ? "Leave full screen" : "Full screen"}
            onClick={() => void (full ? document.exitFullscreen() : root.current?.requestFullscreen())?.catch(() => {})}
          >
            {full ? <Minimize /> : <Maximize />}
          </Button>
          {!popout && !open && (
            <Button
              size="sm"
              variant="ghost"
              title="Open in its own window"
              aria-label="Open in its own window"
              onClick={() => {
                window.open(
                  `/console/${encodeURIComponent(cluster)}/${encodeURIComponent(guest.node)}/${guest.type}/${guest.vmid}?name=${encodeURIComponent(name)}${sharedClip ? "&clip=1" : ""}`,
                  `console-${guest.vmid}`,
                  "popup,width=1100,height=760",
                );
                onClose();
              }}
            >
              <ExternalLink />
            </Button>
          )}
          <Button size="sm" variant="ghost" onClick={popout ? () => window.close() : onClose} aria-label="Close console">
            <X /> Close
          </Button>
        </div>
      </div>
      <div ref={host} className="min-h-0 overflow-hidden bg-black p-1" />
      {toast && <div className="pointer-events-none fixed bottom-4 left-1/2 -translate-x-1/2 rounded-md border bg-card px-3 py-1.5 text-sm shadow-lg">{toast}</div>}
      {pasting && (
        <PasteDialog
          typed={mode === "vga"}
          sharedClip={mode === "vga" && sharedClip}
          onClose={() => {
            setPasting(false);
            setTimeout(refocus, 0);
          }}
          toClip={(t) => {
            ctl.current.clip?.(t);
            flash("On the VM's clipboard: paste it there with Ctrl+V.");
          }}
          run={async (text, cancelled, progress) => {
            if (mode === "vga") await ctl.current.type?.(text, cancelled, progress);
            else ctl.current.paste?.(text);
          }}
        />
      )}
    </div>
  );
}

// copyText puts text on the local clipboard. The Clipboard API needs a
// secure page (https); on plain http it falls back to a hidden textarea.
async function copyText(t: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(t);
    return true;
  } catch {
    const ta = document.createElement("textarea");
    ta.value = t;
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand("copy");
    ta.remove();
    return ok;
  }
}

const MAX_PASTE = 20000;

// PasteDialog takes text the user pastes or types, and only sends it on
// their say-so: nothing is read from the clipboard without a click, and
// text with line breaks needs a second look, since each line break is
// Enter in the guest.
function PasteDialog({
  typed,
  sharedClip,
  onClose,
  toClip,
  run,
}: {
  typed: boolean;
  sharedClip?: boolean;
  onClose: () => void;
  toClip: (text: string) => void;
  run: (text: string, cancelled: () => boolean, progress: (n: number) => void) => Promise<void>;
}) {
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(0);
  const [err, setErr] = useState("");
  const stop = useRef(false);
  const lines = text.split("\n").length;
  const total = [...text].length;
  return (
    <div className="fixed inset-0 z-[60] grid place-items-center bg-black/60 p-4" role="dialog" aria-label="Paste into the console">
      <div className="grid w-full max-w-lg gap-3 rounded-xl border bg-card p-4 shadow-xl">
        <div>
          <h2 className="font-semibold">{typed ? "Type into the VM" : "Paste into the console"}</h2>
          <p className="text-xs text-muted-foreground">
            {!typed
              ? "Sent as one paste; shells that support bracketed paste won't run it line by line."
              : sharedClip
                ? "Put it on the VM's clipboard and paste it there, or have it typed as keystrokes."
                : "The display has no shared clipboard, so the text is typed as keystrokes (US keyboard layout)."}
          </p>
          {typed && !sharedClip && (
            <p className="mt-1 text-xs text-muted-foreground">
              For real copy and paste (Ctrl+C / Ctrl+V both ways), set Display to <span className="font-mono">…,clipboard=vnc</span> in the Configuration tab,
              install spice-vdagent (Linux) or the virtio-win guest tools (Windows) in the VM, then stop and start it.
            </p>
          )}
        </div>
        <Textarea
          autoFocus
          rows={6}
          value={text}
          maxLength={MAX_PASTE}
          onChange={(e) => setText(e.target.value)}
          placeholder="Paste here with Ctrl+V"
          className="font-mono text-xs"
          disabled={busy}
          onKeyDown={(e) => e.stopPropagation()}
        />
        <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
          <span>
            {total} characters{lines > 1 ? `, ${lines} lines: each line break presses Enter` : ""}
            {busy && typed ? ` · typed ${done}` : ""}
          </span>
          <button
            type="button"
            className="cursor-pointer text-brand hover:underline"
            disabled={busy}
            onClick={async () => {
              try {
                setText((await navigator.clipboard.readText()).slice(0, MAX_PASTE));
              } catch {
                setErr("The browser didn't allow reading the clipboard; paste with Ctrl+V instead.");
              }
            }}
          >
            Read my clipboard
          </button>
        </div>
        {err && <p className="text-xs text-destructive">{err}</p>}
        <div className="flex justify-end gap-2">
          <Button
            size="sm"
            variant="ghost"
            onClick={() => {
              stop.current = true;
              onClose();
            }}
          >
            {busy ? "Stop" : "Cancel"}
          </Button>
          {sharedClip && (
            <Button
              size="sm"
              variant="secondary"
              disabled={!text || busy}
              onClick={() => {
                toClip(text);
                onClose();
              }}
            >
              <ClipboardCopy /> Put on VM clipboard
            </Button>
          )}
          <Button
            size="sm"
            variant="brand"
            disabled={!text || busy}
            onClick={async () => {
              if (lines > 1 && !confirm(`Send ${lines} lines? Every line break is Enter, so commands run as they arrive.`)) return;
              setBusy(true);
              stop.current = false;
              await run(text, () => stop.current, setDone);
              setBusy(false);
              onClose();
            }}
          >
            <ClipboardPaste /> {typed ? "Type it" : "Paste"}
          </Button>
        </div>
      </div>
    </div>
  );
}

// ConsolePreview is a small live view of a running guest's console. It
// takes no input; clicking it (or Expand) opens the full console.
export function ConsolePreview({ cluster, guest, onExpand }: { cluster: string; guest: PVEGuestRef; onExpand: () => void }) {
  const host = useRef<HTMLDivElement>(null);
  const [session, setSession] = useState(0);
  const { mode, state, note, size } = useGuestConsole(host, { cluster, guest, want: "auto", session, preview: true });
  // The box takes the guest display's shape (4:3, 16:10, …), so there are no
  // black bars; text consoles and the first second use 16:9.
  const ratio = mode === "vga" && size ? `${size.w} / ${size.h}` : "16 / 9";
  return (
    <div className="group relative w-full max-w-md self-start overflow-hidden rounded-lg border bg-black xl:max-w-none">
      <div
        ref={host}
        style={{ aspectRatio: ratio }}
        className="pointer-events-none w-full overflow-hidden transition-[aspect-ratio] duration-300 [&_canvas]:!cursor-default"
      />
      <button
        type="button"
        onClick={onExpand}
        className="absolute inset-0 flex cursor-pointer items-end justify-between gap-2 bg-gradient-to-t from-black/70 via-transparent to-transparent p-2 text-left text-xs text-white/90 opacity-90 transition-opacity hover:opacity-100"
        aria-label="Open the full console"
      >
        <span className="flex min-w-0 items-center gap-1.5">
          <span
            className={`size-1.5 shrink-0 rounded-full ${state === "open" ? "bg-success" : state === "connecting" ? "animate-pulse bg-warning" : "bg-muted-foreground/60"}`}
          />
          <span className="truncate">
            {state === "open" ? (mode === "vga" ? "Live display" : "Live console") : state === "connecting" ? "Connecting…" : note || "Disconnected"}
          </span>
        </span>
        <span className="flex shrink-0 items-center gap-1 rounded bg-white/10 px-1.5 py-0.5">
          <Expand className="size-3" /> Open
        </span>
      </button>
      {state === "closed" && (
        <button
          type="button"
          onClick={() => setSession((n) => n + 1)}
          className="absolute top-2 right-2 cursor-pointer rounded bg-white/10 p-1 text-white hover:bg-white/20"
          aria-label="Reconnect"
        >
          <RotateCcw className="size-3.5" />
        </button>
      )}
    </div>
  );
}
