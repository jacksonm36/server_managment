import { useState } from "react";
import { KeyRound, Plus, Trash2 } from "lucide-react";
import { useAgentState } from "@/lib/ops";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label } from "@/components/ui/input";
import { Select, Textarea } from "@/components/ui/select";
import { OpStatus, Offline } from "@/components/OpStatus";

type Key = { type: string; fingerprint: string; comment: string; options?: string };
type KeysState = { user: string; path: string; keys: Key[]; users: { name: string; uid: number; home: string }[] };

export function SSHKeys({ serverId, online }: { serverId: string; online: boolean }) {
  const [user, setUser] = useState("root");
  const { data, busy, error, job, run } = useAgentState<KeysState>(serverId, "ssh_keys.list", online, { user });
  const [key, setKey] = useState("");
  const [comment, setComment] = useState("");

  async function add(e: React.FormEvent) {
    e.preventDefault();
    const params: Record<string, string> = { user, key: key.trim() };
    if (comment.trim()) params.comment = comment.trim();
    if (await run("ssh_keys.add", params)) {
      setKey("");
      setComment("");
    }
  }

  async function remove(k: Key) {
    if (!confirm(`Remove ${k.comment || k.fingerprint} from ${user}? Make sure you keep another way in.`)) return;
    await run("ssh_keys.remove", { user, fingerprint: k.fingerprint });
  }

  const users = data?.users ?? [{ name: "root", uid: 0, home: "/root" }];

  return (
    <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_22rem]">
      <Card>
        <CardHeader className="flex-row flex-wrap items-start justify-between gap-3">
          <div>
            <CardTitle>Authorized keys</CardTitle>
            <CardDescription className="font-mono text-xs">{data?.path ?? "~/.ssh/authorized_keys"}</CardDescription>
          </div>
          <div className="w-44">
            <Select value={user} onChange={(e) => setUser(e.target.value)} aria-label="User" disabled={!!busy}>
              {users.map((u) => (
                <option key={u.name} value={u.name}>
                  {u.name}
                </option>
              ))}
            </Select>
          </div>
        </CardHeader>
        <CardContent className="grid gap-4">
          {!online && <Offline />}
          {data && data.keys.length === 0 && (
            <p className="text-sm text-muted-foreground">No keys authorized for {data.user}.</p>
          )}
          {data && data.keys.length > 0 && (
            <ul className="grid gap-2">
              {data.keys.map((k) => (
                <li key={k.fingerprint} className="flex items-center gap-3 rounded-lg border px-3 py-2.5">
                  <KeyRound className="size-4 shrink-0 text-brand" />
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-sm font-medium">{k.comment || <span className="text-muted-foreground">no comment</span>}</div>
                    <div className="truncate font-mono text-xs text-muted-foreground">
                      {k.type} · {k.fingerprint}
                      {k.options && ` · ${k.options}`}
                    </div>
                  </div>
                  <Button variant="ghost" size="icon" aria-label="Remove key" disabled={!online || !!busy} onClick={() => remove(k)}>
                    <Trash2 className="text-destructive" />
                  </Button>
                </li>
              ))}
            </ul>
          )}
          <OpStatus busy={busy} error={error} job={job} />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Deploy a key</CardTitle>
          <CardDescription>Adds an OpenSSH public key for {user}.</CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={add} className="grid gap-3">
            <div className="grid gap-2">
              <Label htmlFor="pubkey">Public key</Label>
              <Textarea
                id="pubkey"
                className="font-mono text-xs"
                rows={5}
                placeholder="ssh-ed25519 AAAAC3Nza… you@laptop"
                value={key}
                onChange={(e) => setKey(e.target.value)}
                required
              />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="keycomment">Comment (optional)</Label>
              <Input id="keycomment" placeholder="jackson laptop" value={comment} onChange={(e) => setComment(e.target.value)} />
            </div>
            <Button type="submit" variant="brand" disabled={!online || !!busy || !key.trim()}>
              <Plus /> Add key
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
