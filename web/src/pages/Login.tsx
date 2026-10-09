import { useState } from "react";
import { Server } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label } from "@/components/ui/input";

export function Login({ onLogin }: { onLogin: (username: string) => void }) {
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [needOTP, setNeedOTP] = useState(false);
  const [otp, setOtp] = useState("");

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const r = await api.login(username, password, needOTP ? otp.trim() : undefined);
      onLogin(r.username);
    } catch (err) {
      if (err instanceof ApiError && err.data.totp_required) {
        // First step passed; show the code field (only report an error if a code was tried).
        if (needOTP) setError(err.message);
        setNeedOTP(true);
        setOtp("");
      } else {
        setError((err as Error).message);
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="relative flex min-h-svh items-center justify-center overflow-hidden p-4">
      <div className="pointer-events-none absolute inset-0 bg-[radial-gradient(ellipse_at_top,color-mix(in_oklab,var(--color-brand)_14%,transparent),transparent_60%)]" />
      <Card className="relative w-full max-w-sm">
        <CardHeader className="items-center text-center">
          <div className="mb-2 flex size-11 items-center justify-center rounded-xl bg-brand/15 text-brand">
            <Server className="size-5" />
          </div>
          <CardTitle className="text-xl">servermgr</CardTitle>
          <CardDescription>Sign in to manage your servers</CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={submit} className="grid gap-4">
            <div className="grid gap-2">
              <Label htmlFor="username">Username</Label>
              <Input
                id="username"
                autoComplete="username"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                required
              />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="password">Password</Label>
              <Input
                id="password"
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoFocus
                required
              />
            </div>
            {needOTP && (
              <div className="grid gap-2">
                <Label htmlFor="otp">Two-factor code</Label>
                <Input
                  id="otp"
                  inputMode="numeric"
                  autoComplete="one-time-code"
                  placeholder="123456 or a recovery code"
                  value={otp}
                  onChange={(e) => setOtp(e.target.value)}
                  autoFocus
                  required
                />
              </div>
            )}
            {error && <p className="text-sm text-destructive">{error}</p>}
            <Button type="submit" variant="brand" className="w-full" disabled={busy}>
              {busy ? "Signing in…" : "Sign in"}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
