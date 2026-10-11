import { useState } from "react";
import { Copy, Lock, LockOpen, ShieldCheck } from "lucide-react";
import { api } from "@/lib/api";
import { refreshVault, setVaultStatus, useVault } from "@/lib/vault";
import { useAccess } from "@/lib/access";
import { Button } from "@/components/ui/button";
import { Card, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label } from "@/components/ui/input";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";

const WHAT = "SSH keys, Proxmox API tokens, OpenTofu variables and state, and notification settings";

/** Shown on every page while the vault is locked, to those who can unlock it. */
export function VaultBanner() {
  const v = useVault();
  const { can } = useAccess();
  const [open, setOpen] = useState(false);
  if (!v.configured || v.unlocked || !can("vault.manage")) return null;
  return (
    <div className="mb-6 flex flex-wrap items-center justify-between gap-3 rounded-lg border border-warning/30 bg-warning/10 px-4 py-2 text-sm text-warning">
      <span className="flex items-center gap-2">
        <Lock className="size-4" /> The secrets vault is locked. Proxmox, OpenTofu, notifications and SSH key deploys wait until it's unlocked.
      </span>
      <Button size="sm" variant="outline" onClick={() => setOpen(true)}>
        <LockOpen /> Unlock
      </Button>
      {open && <UnlockDialog onClose={() => setOpen(false)} />}
    </div>
  );
}

export function UnlockDialog({ onClose }: { onClose: () => void }) {
  const [pass, setPass] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Unlock the secrets vault</DialogTitle>
          <DialogDescription>Enter the vault passphrase, or the recovery key if you lost it. It stays unlocked until the panel restarts.</DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={async (e) => {
            e.preventDefault();
            setBusy(true);
            setError("");
            try {
              setVaultStatus(await api.vaultUnlock(pass));
              onClose();
            } catch (err) {
              setError((err as Error).message);
            } finally {
              setBusy(false);
            }
          }}
        >
          <Input type="password" autoFocus autoComplete="off" value={pass} onChange={(e) => setPass(e.target.value)} aria-label="Passphrase or recovery key" />
          {error && <p className="text-sm text-destructive">{error}</p>}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" variant="brand" disabled={busy || !pass}>
              {busy ? "Checking…" : "Unlock"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

export function VaultCard() {
  const v = useVault();
  const { can } = useAccess();
  const manage = can("vault.manage");
  const [dialog, setDialog] = useState<"setup" | "unlock" | "change" | null>(null);
  return (
    <Card>
      <CardHeader className="flex-row flex-wrap items-start justify-between gap-3">
        <div>
          <CardTitle className="flex items-center gap-2">
            <ShieldCheck className="size-4" /> Secrets vault
            {v.configured && (
              <span className={`text-xs font-normal ${v.unlocked ? "text-success" : "text-warning"}`}>{v.unlocked ? "unlocked" : "locked"}</span>
            )}
          </CardTitle>
          <CardDescription className="max-w-2xl">
            {v.configured
              ? `Encrypts ${WHAT} with a key that only exists in memory. Someone with a copy of the server's disk and database can't read them. After every restart, unlock it once with your passphrase.`
              : `Set a passphrase to encrypt ${WHAT} with a key that only exists in memory, so a copy of the server's disk and database isn't enough to read them. The SSH key library needs it.`}
          </CardDescription>
        </div>
        {manage && (
          <div className="flex flex-wrap gap-2">
            {!v.configured && (
              <Button variant="brand" onClick={() => setDialog("setup")}>
                Set up vault
              </Button>
            )}
            {v.configured && !v.unlocked && (
              <Button variant="brand" onClick={() => setDialog("unlock")}>
                <LockOpen /> Unlock
              </Button>
            )}
            {v.configured && v.unlocked && (
              <>
                <Button variant="outline" size="sm" onClick={() => setDialog("change")}>
                  Change passphrase
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={async () => confirm("Lock the vault now? Deploys, Proxmox, OpenTofu and notifications stop until someone unlocks it.") && setVaultStatus(await api.vaultLock())}
                >
                  <Lock /> Lock
                </Button>
              </>
            )}
          </div>
        )}
      </CardHeader>
      {dialog === "setup" && <SetupDialog onClose={() => setDialog(null)} />}
      {dialog === "unlock" && <UnlockDialog onClose={() => setDialog(null)} />}
      {dialog === "change" && <ChangeDialog onClose={() => setDialog(null)} />}
    </Card>
  );
}

function SetupDialog({ onClose }: { onClose: () => void }) {
  const [pass, setPass] = useState("");
  const [pass2, setPass2] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState<{ recovery_key: string; secrets_moved: number } | null>(null);
  const [saved, setSaved] = useState(false);
  const [copied, setCopied] = useState(false);
  const close = () => {
    if (done) void refreshVault();
    onClose();
  };
  return (
    <Dialog open onOpenChange={(o) => !o && (!done || saved) && close()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{done ? "Save your recovery key" : "Set up the secrets vault"}</DialogTitle>
          <DialogDescription>
            {done
              ? "It unlocks the vault if you forget the passphrase. It is shown only now; nobody, including the panel, can recover the vault without one of the two."
              : "Choose a long passphrase (at least 12 characters). It is never stored. Without it or the recovery key, the stored secrets are lost."}
          </DialogDescription>
        </DialogHeader>
        {done ? (
          <div className="grid gap-4">
            <div className="flex items-center gap-2">
              <code className="flex-1 rounded-md border bg-background/60 px-3 py-2 font-mono text-sm">{done.recovery_key}</code>
              <Button
                variant="outline"
                size="icon"
                title="Copy"
                aria-label="Copy recovery key"
                onClick={async () => {
                  await navigator.clipboard.writeText(done.recovery_key).catch(() => {});
                  setCopied(true);
                }}
              >
                <Copy />
              </Button>
            </div>
            {copied && <p className="text-xs text-muted-foreground">Copied. Keep it in a password manager or on paper, not on this server.</p>}
            {done.secrets_moved > 0 && <p className="text-sm text-muted-foreground">{done.secrets_moved} existing secret(s) were moved into the vault.</p>}
            <label className="flex cursor-pointer items-center gap-2 text-sm">
              <input type="checkbox" className="accent-[var(--color-brand)]" checked={saved} onChange={(e) => setSaved(e.target.checked)} />
              I saved the recovery key somewhere safe
            </label>
            <div className="flex justify-end">
              <Button variant="brand" disabled={!saved} onClick={close}>
                Done
              </Button>
            </div>
          </div>
        ) : (
          <form
            className="grid gap-4"
            onSubmit={async (e) => {
              e.preventDefault();
              if (pass !== pass2) return setError("The passphrases don't match.");
              setBusy(true);
              setError("");
              try {
                setDone(await api.vaultSetup(pass));
              } catch (err) {
                setError((err as Error).message);
              } finally {
                setBusy(false);
              }
            }}
          >
            <div className="grid gap-2">
              <Label htmlFor="v-pass">Passphrase</Label>
              <Input id="v-pass" type="password" autoComplete="new-password" value={pass} onChange={(e) => setPass(e.target.value)} />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="v-pass2">Repeat passphrase</Label>
              <Input id="v-pass2" type="password" autoComplete="new-password" value={pass2} onChange={(e) => setPass2(e.target.value)} />
            </div>
            {error && <p className="text-sm text-destructive">{error}</p>}
            <div className="flex justify-end gap-2">
              <Button type="button" variant="ghost" onClick={onClose}>
                Cancel
              </Button>
              <Button type="submit" variant="brand" disabled={busy || pass.length < 12}>
                {busy ? "Encrypting…" : "Create vault"}
              </Button>
            </div>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}

function ChangeDialog({ onClose }: { onClose: () => void }) {
  const [cur, setCur] = useState("");
  const [next, setNext] = useState("");
  const [next2, setNext2] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Change vault passphrase</DialogTitle>
          <DialogDescription>Your recovery key keeps working.</DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={async (e) => {
            e.preventDefault();
            if (next !== next2) return setError("The new passphrases don't match.");
            setBusy(true);
            setError("");
            try {
              await api.vaultPassphrase(cur, next);
              onClose();
            } catch (err) {
              setError((err as Error).message);
            } finally {
              setBusy(false);
            }
          }}
        >
          <div className="grid gap-2">
            <Label htmlFor="vc-cur">Current passphrase or recovery key</Label>
            <Input id="vc-cur" type="password" autoComplete="off" value={cur} onChange={(e) => setCur(e.target.value)} />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="vc-new">New passphrase</Label>
            <Input id="vc-new" type="password" autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="vc-new2">Repeat new passphrase</Label>
            <Input id="vc-new2" type="password" autoComplete="new-password" value={next2} onChange={(e) => setNext2(e.target.value)} />
          </div>
          {error && <p className="text-sm text-destructive">{error}</p>}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" variant="brand" disabled={busy || next.length < 12 || !cur}>
              Change
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
