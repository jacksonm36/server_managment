import { useEffect, useMemo, useState } from "react";
import qrcode from "qrcode-generator";
import { Check, Copy, KeyRound, ShieldCheck, ShieldOff } from "lucide-react";
import { api, type Account as AccountInfo } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label } from "@/components/ui/input";

export function Account() {
  const [info, setInfo] = useState<AccountInfo | null>(null);
  const reload = () => api.account().then(setInfo);
  useEffect(() => {
    void reload();
  }, []);

  if (!info) return <p className="text-sm text-muted-foreground">Loading…</p>;
  return (
    <div className="grid gap-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Account</h1>
        <p className="text-sm text-muted-foreground">Signed in as {info.username}</p>
      </div>
      <div className="grid gap-4 lg:grid-cols-2">
        <TwoFactor info={info} onChange={reload} />
        <PasswordCard />
      </div>
    </div>
  );
}

function Field(props: { id: string; label: string; type?: string; value: string; onChange: (v: string) => void; autoComplete?: string; inputMode?: "numeric" }) {
  return (
    <div className="grid gap-2">
      <Label htmlFor={props.id}>{props.label}</Label>
      <Input
        id={props.id}
        type={props.type ?? "text"}
        value={props.value}
        autoComplete={props.autoComplete}
        inputMode={props.inputMode}
        onChange={(e) => props.onChange(e.target.value)}
        required
      />
    </div>
  );
}

function RecoveryCodes({ codes, onDone }: { codes: string[]; onDone: () => void }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="grid gap-3">
      <p className="text-sm">
        Save these recovery codes somewhere safe. Each one signs you in once if you lose your authenticator. They are{" "}
        <strong>shown only now</strong>.
      </p>
      <ul className="grid grid-cols-2 gap-2 rounded-lg border bg-background/60 p-3 font-mono text-sm">
        {codes.map((c) => (
          <li key={c}>{c}</li>
        ))}
      </ul>
      <div className="flex gap-2">
        <Button
          variant="secondary"
          size="sm"
          onClick={async () => {
            await navigator.clipboard.writeText(codes.join("\n"));
            setCopied(true);
          }}
        >
          {copied ? <Check /> : <Copy />} {copied ? "Copied" : "Copy"}
        </Button>
        <Button variant="brand" size="sm" onClick={onDone}>
          I saved them
        </Button>
      </div>
    </div>
  );
}

function TwoFactor({ info, onChange }: { info: AccountInfo; onChange: () => void }) {
  const [step, setStep] = useState<"idle" | "password" | "scan" | "codes" | "disable" | "regen">("idle");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [setup, setSetup] = useState<{ secret: string; otpauth_uri: string } | null>(null);
  const [codes, setCodes] = useState<string[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const qr = useMemo(() => {
    if (!setup) return "";
    const q = qrcode(0, "M");
    q.addData(setup.otpauth_uri);
    q.make();
    return "data:image/svg+xml;utf8," + encodeURIComponent(q.createSvgTag({ cellSize: 5, margin: 2 }));
  }, [setup]);

  function reset() {
    setStep("idle");
    setPassword("");
    setCode("");
    setSetup(null);
    setError("");
  }

  async function act(fn: () => Promise<void>) {
    setBusy(true);
    setError("");
    try {
      await fn();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          Two-factor authentication
          {info.totp_enabled ? (
            <Badge variant="success">
              <ShieldCheck className="size-3" /> on
            </Badge>
          ) : (
            <Badge variant="warning">
              <ShieldOff className="size-3" /> off
            </Badge>
          )}
        </CardTitle>
        <CardDescription>
          {info.totp_enabled
            ? `Authenticator app codes are required to sign in. ${info.recovery_codes_left} recovery code(s) left.`
            : "Require a code from an authenticator app (Aegis, 1Password, Google Authenticator…) when signing in."}
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4">
        {step === "idle" &&
          (info.totp_enabled ? (
            <div className="flex flex-wrap gap-2">
              <Button variant="secondary" size="sm" onClick={() => setStep("regen")}>
                <KeyRound /> New recovery codes
              </Button>
              <Button variant="outline" size="sm" onClick={() => setStep("disable")}>
                Turn off
              </Button>
            </div>
          ) : (
            <Button variant="brand" className="w-fit" onClick={() => setStep("password")}>
              <ShieldCheck /> Set up two-factor
            </Button>
          ))}

        {step === "password" && (
          <form
            className="grid gap-3"
            onSubmit={(e) => {
              e.preventDefault();
              void act(async () => {
                setSetup(await api.totpSetup(password));
                setPassword("");
                setStep("scan");
              });
            }}
          >
            <Field id="pw2fa" label="Confirm your password" type="password" autoComplete="current-password" value={password} onChange={setPassword} />
            <div className="flex gap-2">
              <Button type="submit" variant="brand" size="sm" disabled={busy}>
                Continue
              </Button>
              <Button type="button" variant="ghost" size="sm" onClick={reset}>
                Cancel
              </Button>
            </div>
          </form>
        )}

        {step === "scan" && setup && (
          <form
            className="grid gap-4"
            onSubmit={(e) => {
              e.preventDefault();
              void act(async () => {
                const r = await api.totpEnable(code.trim());
                setCodes(r.recovery_codes);
                setCode("");
                setStep("codes");
              });
            }}
          >
            <div className="flex flex-wrap items-center gap-4">
              <img src={qr} alt="QR code for your authenticator app" className="size-44 rounded-lg bg-white p-1" />
              <div className="grid min-w-0 flex-1 gap-1 text-sm">
                <p>Scan with your authenticator app, or enter this key:</p>
                <code className="rounded-md border bg-background/60 px-2 py-1 font-mono text-xs break-all">{setup.secret}</code>
              </div>
            </div>
            <Field id="code" label="6-digit code from the app" inputMode="numeric" autoComplete="one-time-code" value={code} onChange={setCode} />
            <div className="flex gap-2">
              <Button type="submit" variant="brand" size="sm" disabled={busy || code.trim().length !== 6}>
                Verify &amp; turn on
              </Button>
              <Button type="button" variant="ghost" size="sm" onClick={reset}>
                Cancel
              </Button>
            </div>
          </form>
        )}

        {step === "codes" && (
          <RecoveryCodes
            codes={codes}
            onDone={() => {
              setCodes([]);
              reset();
              onChange();
            }}
          />
        )}

        {step === "regen" && (
          <form
            className="grid gap-3"
            onSubmit={(e) => {
              e.preventDefault();
              void act(async () => {
                const r = await api.recoveryCodes(password);
                setPassword("");
                setCodes(r.recovery_codes);
                setStep("codes");
              });
            }}
          >
            <p className="text-sm text-muted-foreground">Your old recovery codes stop working.</p>
            <Field id="pwregen" label="Confirm your password" type="password" autoComplete="current-password" value={password} onChange={setPassword} />
            <div className="flex gap-2">
              <Button type="submit" variant="brand" size="sm" disabled={busy}>
                Generate
              </Button>
              <Button type="button" variant="ghost" size="sm" onClick={reset}>
                Cancel
              </Button>
            </div>
          </form>
        )}

        {step === "disable" && (
          <form
            className="grid gap-3"
            onSubmit={(e) => {
              e.preventDefault();
              void act(async () => {
                await api.totpDisable(password, code.trim());
                reset();
                onChange();
              });
            }}
          >
            <Field id="pwdis" label="Password" type="password" autoComplete="current-password" value={password} onChange={setPassword} />
            <Field id="codedis" label="Authenticator or recovery code" autoComplete="one-time-code" value={code} onChange={setCode} />
            <div className="flex gap-2">
              <Button type="submit" variant="destructive" size="sm" disabled={busy}>
                Turn off two-factor
              </Button>
              <Button type="button" variant="ghost" size="sm" onClick={reset}>
                Cancel
              </Button>
            </div>
          </form>
        )}

        {error && <p className="text-sm text-destructive">{error}</p>}
      </CardContent>
    </Card>
  );
}

export function PasswordCard({ forced, onChanged }: { forced?: boolean; onChanged?: () => void }) {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (next !== confirm) {
      setMsg({ ok: false, text: "The new passwords don't match." });
      return;
    }
    setBusy(true);
    setMsg(null);
    try {
      await api.changePassword(current, next);
      setCurrent("");
      setNext("");
      setConfirm("");
      setMsg({ ok: true, text: "Password changed. Other sessions were signed out." });
      onChanged?.();
    } catch (err) {
      setMsg({ ok: false, text: (err as Error).message });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{forced ? "Choose a new password" : "Password"}</CardTitle>
        <CardDescription>
          {forced
            ? "An admin set a temporary password for you. Pick your own (at least 12 characters) to continue."
            : "At least 12 characters. Changing it signs out your other sessions."}
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={submit} className="grid gap-3">
          <Field id="cur" label="Current password" type="password" autoComplete="current-password" value={current} onChange={setCurrent} />
          <Field id="new" label="New password" type="password" autoComplete="new-password" value={next} onChange={setNext} />
          <Field id="new2" label="Repeat new password" type="password" autoComplete="new-password" value={confirm} onChange={setConfirm} />
          {msg && <p className={`text-sm ${msg.ok ? "text-success" : "text-destructive"}`}>{msg.text}</p>}
          <Button type="submit" variant="secondary" className="w-fit" disabled={busy || next.length < 12}>
            Change password
          </Button>
        </form>
      </CardContent>
    </Card>
  );
}
