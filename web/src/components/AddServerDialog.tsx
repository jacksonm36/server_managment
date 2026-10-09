import { useState } from "react";
import { Check, Copy, Plus, Terminal } from "lucide-react";
import { api, type JoinToken } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";

export function AddServerDialog() {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [token, setToken] = useState<JoinToken | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [copied, setCopied] = useState(false);

  function reset(o: boolean) {
    setOpen(o);
    if (!o) {
      setName("");
      setToken(null);
      setError("");
      setCopied(false);
    }
  }

  async function generate(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      setToken(await api.createJoinToken(name.trim()));
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function copy() {
    if (!token) return;
    try {
      await navigator.clipboard.writeText(token.install_command);
    } catch {
      // Clipboard API needs a secure context; fall back to selecting the text.
      const el = document.getElementById("install-cmd");
      if (el) window.getSelection()?.selectAllChildren(el);
      return;
    }
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  }

  return (
    <Dialog open={open} onOpenChange={reset}>
      <DialogTrigger asChild>
        <Button variant="brand">
          <Plus /> Add server
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Add a server</DialogTitle>
          <DialogDescription>
            Generate a one-time join token, then run the install command as root on the server. The agent dials out to
            this panel, so no inbound ports are needed on the server.
          </DialogDescription>
        </DialogHeader>

        {!token ? (
          <form onSubmit={generate} className="grid gap-4">
            <div className="grid gap-2">
              <Label htmlFor="srv-name">Display name (optional)</Label>
              <Input
                id="srv-name"
                placeholder="defaults to the server's hostname"
                value={name}
                maxLength={100}
                onChange={(e) => setName(e.target.value)}
                autoFocus
              />
            </div>
            {error && <p className="text-sm text-destructive">{error}</p>}
            <div className="flex justify-end">
              <Button type="submit" variant="brand" disabled={busy}>
                {busy ? "Generating…" : "Generate install command"}
              </Button>
            </div>
          </form>
        ) : (
          <div className="grid gap-3">
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <Terminal className="size-4" /> Run on the new server (Debian / Ubuntu, as root):
            </div>
            <div className="relative rounded-lg border bg-background/60">
              <pre
                id="install-cmd"
                className="max-h-40 overflow-auto p-4 pr-12 font-mono text-xs leading-relaxed break-all whitespace-pre-wrap"
              >
                {token.install_command}
              </pre>
              <Button size="icon" variant="ghost" className="absolute top-2 right-2" onClick={copy} aria-label="Copy">
                {copied ? <Check className="text-success" /> : <Copy />}
              </Button>
            </div>
            <p className="text-xs text-muted-foreground">
              The token works once and expires at {new Date(token.expires_at).toLocaleTimeString()}. The command pins
              the panel CA fingerprint, so the agent refuses to enroll against anything else. The server appears in the
              list as soon as the agent connects.
            </p>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
