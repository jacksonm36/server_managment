package ops

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func zfsStateTemp(t *testing.T) string {
	t.Helper()
	old := zfsStateDir
	t.Cleanup(func() { zfsStateDir = old })
	zfsStateDir = t.TempDir()
	return zfsStateDir
}

func TestZfsDue(t *testing.T) {
	at := func(s string) time.Time {
		tm, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	cases := []struct {
		sched, last, now string
		want             bool
	}{
		{"hourly", "2026-10-22T10:00:30Z", "2026-10-22T10:59:00Z", false},
		{"hourly", "2026-10-22T10:00:30Z", "2026-10-22T11:00:10Z", true},
		{"daily", "2026-10-22T00:00:30Z", "2026-10-22T23:59:00Z", false},
		{"daily", "2026-10-22T00:00:30Z", "2026-10-23T00:00:10Z", true},
		{"daily", "2026-10-20T00:00:30Z", "2026-10-22T15:00:00Z", true}, // missed days catch up
		// 2026-10-19 is a Monday.
		{"weekly", "2026-10-19T00:00:30Z", "2026-10-25T23:59:00Z", false},
		{"weekly", "2026-10-19T00:00:30Z", "2026-10-26T00:00:10Z", true},
		{"weekly", "2026-10-12T00:00:30Z", "2026-10-21T09:00:00Z", true}, // missed Monday
		{"monthly", "2026-10-01T00:00:30Z", "2026-10-31T23:00:00Z", false},
		{"monthly", "2026-10-01T00:00:30Z", "2026-11-01T00:00:10Z", true},
		{"manual", "2026-01-01T00:00:00Z", "2026-10-22T00:00:00Z", false},
	}
	for _, c := range cases {
		if got := zfsDue(c.sched, at(c.last).Unix(), at(c.now)); got != c.want {
			t.Errorf("%s last %s now %s: got %v", c.sched, c.last, c.now, got)
		}
	}
	if !zfsDue("hourly", 0, time.Now()) || zfsDue("manual", 0, time.Now()) {
		t.Error("never run: hourly is due, manual is not")
	}
}

func TestZfsPruneList(t *testing.T) {
	names := []string{"sm-daily-20261020-0000", "manual", "sm-daily-20261022-0000", "sm-hourly-20261022-0100",
		"sm-daily-20261021-0000", "sm-daily-x", "sm-daily-20261019-0000"}
	got := zfsPruneList(names, zfsSnapRe("daily"), 2)
	want := []string{"sm-daily-20261019-0000", "sm-daily-20261020-0000"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
	if got := zfsPruneList(names, zfsSnapRe("daily"), 10); got != nil {
		t.Errorf("keep 10: %v", got)
	}
	// keep below 1 still never destroys the newest
	repl := []string{"sm-repl-20261022-100000", "sm-repl-20261022-110000"}
	if got := zfsPruneList(repl, zfsReplRe, 0); !slices.Equal(got, []string{"sm-repl-20261022-100000"}) {
		t.Errorf("keep 0: %v", got)
	}
}

func TestZfsSnapPolicyRun(t *testing.T) {
	ran := fakeZfs(t)
	zfsStateTemp(t)
	ctx := context.Background()
	if _, err := zfsSnapPolicyOp(ctx, map[string]string{"dataset": "tank", "schedule": "yearly"}); err == nil {
		t.Error("bad schedule accepted")
	}
	if _, err := zfsSnapPolicyOp(ctx, map[string]string{"dataset": "tank", "schedule": "daily", "keep": "0"}); err == nil {
		t.Error("keep 0 accepted")
	}
	res, err := zfsSnapPolicyOp(ctx, map[string]string{"dataset": "tank/data", "schedule": "hourly", "recursive": "1"})
	if err != nil {
		t.Fatal(err)
	}
	pols := res.(map[string]any)["policies"].([]*zfsSnapPolicy)
	if len(pols) != 1 || pols[0].Keep != 24 || !pols[0].Recursive {
		t.Fatalf("policies %+v", pols[0])
	}
	if fi, err := os.Stat(zfsStatePath()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("zfs.json: %v %v", fi, err)
	}

	old := zfsCmd
	zfsCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if line := name + " " + strings.Join(args, " "); strings.HasPrefix(line, "zfs list -H -p -t snapshot -o name") {
			return []byte("tank/data@sm-hourly-20261022-0800\ntank/data@keepme\ntank/data@sm-hourly-20261022-0900\ntank/data@sm-hourly-20261022-1000\n"), nil
		}
		return old(ctx, name, args...)
	}
	pols[0].Keep = 2
	now := time.Date(2026, 10, 22, 10, 0, 5, 0, time.UTC)
	if err := runSnapPolicy(ctx, pols[0], now, t.Logf); err != nil {
		t.Fatal(err)
	}
	want := []string{"zfs snapshot -r tank/data@sm-hourly-20261022-1000", "zfs destroy -r tank/data@sm-hourly-20261022-0800"}
	if !slices.Equal(*ran, want) {
		t.Errorf("ran %q", *ran)
	}

	res, _ = zfsSnapPolicyOp(ctx, map[string]string{"dataset": "tank/data", "schedule": "hourly", "delete": "1"})
	if n := len(res.(map[string]any)["policies"].([]*zfsSnapPolicy)); n != 0 {
		t.Errorf("after delete: %d policies", n)
	}
	st, _ := zfsStatus(ctx)
	if _, ok := st.(map[string]any)["replications"]; !ok {
		t.Error("zfs.status has no replications")
	}
}

func testPubKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestZfsReplAuthorize(t *testing.T) {
	fakeZfs(t)
	dir := t.TempDir()
	oldAK, oldGlob, oldPort, oldExe := authorizedKeysPath, sshHostKeyGlob, zfsSSHPort, agentExe
	t.Cleanup(func() { authorizedKeysPath, sshHostKeyGlob, zfsSSHPort, agentExe = oldAK, oldGlob, oldPort, oldExe })
	authorizedKeysPath = filepath.Join(dir, "ssh", "authorized_keys")
	sshHostKeyGlob = filepath.Join(dir, "ssh_host_*_key.pub")
	zfsSSHPort = func() string { return "2222" }
	agentExe = func() (string, error) { return "/usr/local/bin/servermgr-agent", nil }
	_ = os.WriteFile(filepath.Join(dir, "ssh_host_ed25519_key.pub"), []byte("ssh-ed25519 AAAAHOST root@x\n"), 0o644)

	other := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(testPubKey(t)))) + " admin@laptop"
	_ = os.MkdirAll(filepath.Dir(authorizedKeysPath), 0o755)
	_ = os.WriteFile(authorizedKeysPath, []byte("# keys\n"+other+"\n"), 0o644)

	k := testPubKey(t)
	pub := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))) + " servermgr-zfs-repl@src"
	ctx := context.Background()
	p := map[string]string{"public_key": pub, "parent": "tank/backup", "from": "10.0.0.5"}
	var res any
	var err error
	for range 2 {
		if res, err = zfsReplAuthorize(ctx, p, t.Logf); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(authorizedKeysPath)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	key := strings.Fields(pub)[1]
	wantLine := `restrict,from="10.0.0.5",command="/usr/local/bin/servermgr-agent zfs-recv tank/backup" ssh-ed25519 ` + key + " servermgr-zfs-repl"
	if len(lines) != 3 || lines[0] != "# keys" || lines[1] != other || lines[2] != wantLine {
		t.Fatalf("authorized_keys:\n%s", b)
	}
	if fi, _ := os.Stat(authorizedKeysPath); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
	if fi, _ := os.Stat(filepath.Dir(authorizedKeysPath)); fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", fi.Mode())
	}
	m := res.(map[string]any)
	if hk := m["host_keys"].([]string); len(hk) != 1 || hk[0] != "ssh-ed25519 AAAAHOST" || m["ssh_port"] != "2222" {
		t.Errorf("result %v", m)
	}

	// Without from, the option is left out.
	if _, err := zfsReplAuthorize(ctx, map[string]string{"public_key": pub, "parent": "tank/backup"}, t.Logf); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(authorizedKeysPath)
	if !strings.Contains(string(b), `restrict,command="/usr/local/bin/servermgr-agent zfs-recv tank/backup" ssh-ed25519 `) {
		t.Errorf("without from:\n%s", b)
	}

	if _, err := zfsReplAuthorize(ctx, map[string]string{"public_key": pub, "revoke": "1"}, t.Logf); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(authorizedKeysPath)
	if string(b) != "# keys\n"+other+"\n" {
		t.Errorf("after revoke:\n%s", b)
	}

	bad := []map[string]string{
		{"public_key": "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAgQC", "parent": "tank"},
		{"public_key": `command="sh" ` + pub, "parent": "tank"},
		{"public_key": pub, "parent": "../etc"},
		{"public_key": pub, "parent": "tank", "from": "10.0.0.5\" ,x"},
	}
	for _, p := range bad {
		if _, err := zfsReplAuthorize(ctx, p, t.Logf); err == nil {
			t.Errorf("accepted %v", p)
		}
	}
}

func TestZFSRecv(t *testing.T) {
	ran := fakeZfs(t)
	old := zfsCmd
	zfsCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		line := name + " " + strings.Join(args, " ")
		switch {
		case line == "zfs list -H -o name tank/backup/missing":
			return nil, errors.New("does not exist")
		case strings.HasPrefix(line, "zfs list -H -p -t snapshot -o name,guid"):
			return []byte("tank/backup/data@sm-repl-20261022-100000\t111\ntank/backup/data@sm-repl-20261022-110000\t222\n"), nil
		case strings.HasPrefix(line, "zfs list -H -p -t snapshot -o name"):
			return []byte("tank/backup/data@sm-repl-20261022-100000\ntank/backup/data@other\ntank/backup/data@sm-repl-20261022-110000\ntank/backup/data@sm-repl-20261022-120000\n"), nil
		}
		return old(ctx, name, args...)
	}
	oldRecv := zfsRecvExec
	t.Cleanup(func() { zfsRecvExec = oldRecv })
	var recvArgs []string
	zfsRecvExec = func(_ context.Context, args []string, stdin io.Reader, _, _ io.Writer) error {
		recvArgs = args
		_, err := io.Copy(io.Discard, stdin)
		return err
	}
	call := func(cmd string) (int, string, string) {
		var out, errb bytes.Buffer
		code := ZFSRecv("tank/backup", cmd, strings.NewReader("stream"), &out, &errb)
		return code, out.String(), errb.String()
	}
	if code, out, _ := call("list data"); code != 0 || out != "sm-repl-20261022-100000 111\nsm-repl-20261022-110000 222\n" {
		t.Errorf("list: %d %q", code, out)
	}
	if code, out, _ := call("list missing"); code != 0 || out != "" {
		t.Errorf("list missing: %d %q", code, out)
	}
	if code, _, _ := call("recv data"); code != 0 || strings.Join(recvArgs, " ") != "recv -s -u -F tank/backup/data" {
		t.Errorf("recv: %d %v", code, recvArgs)
	}
	*ran = nil
	if code, _, e := call("prune data 2"); code != 0 || !slices.Equal(*ran, []string{"zfs destroy tank/backup/data@sm-repl-20261022-100000"}) {
		t.Errorf("prune: %d %q %s", code, *ran, e)
	}
	for _, cmd := range []string{"", "recv", "recv ../x", "recv a/b", "recv -x", "recv data extra", "destroy data",
		"list data;rm", "prune data 0", "prune data x", "prune data", "zfs recv data", "recv .hidden"} {
		if code, _, e := call(cmd); code != 1 || e == "" {
			t.Errorf("%q: exit %d", cmd, code)
		}
	}
	var out, errb bytes.Buffer
	if ZFSRecv("../etc", "list data", nil, &out, &errb) != 1 {
		t.Error("bad parent accepted")
	}
}

func TestReplTargetName(t *testing.T) {
	for in, want := range map[string]string{"tank/data/vm": "data-vm", "tank": "tank", "rpool/x": "x"} {
		if got := replTargetName(in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}

func TestReplBase(t *testing.T) {
	src := []replSnap{{"sm-repl-20261022-090000", "1"}, {"sm-hourly-20261022-1000", "2"},
		{"sm-repl-20261022-100000", "3"}, {"sm-repl-20261022-110000", "4"}, {"sm-repl-20261022-120000", "5"}}
	target := parseGUIDs("sm-repl-20261022-090000 1\nsm-hourly-20261022-1000 2\nsm-repl-20261022-100000 3\n")
	if got := replBase(src, target, "sm-repl-20261022-120000"); got != "sm-repl-20261022-100000" {
		t.Errorf("base %q", got)
	}
	if got := replBase(src, parseGUIDs("x 2\n"), ""); got != "" {
		t.Errorf("only a non-repl snapshot in common: %q", got)
	}
	if got := replBase(src, map[string]bool{}, ""); got != "" {
		t.Errorf("empty target: %q", got)
	}
}

func TestZfsReplPolicyAndRun(t *testing.T) {
	ran := fakeZfs(t)
	dir := zfsStateTemp(t)
	ctx := context.Background()
	hk := "ssh-ed25519 " + strings.Fields(string(ssh.MarshalAuthorizedKey(testPubKey(t))))[1]
	p := map[string]string{"dataset": "tank/data", "target_host": "backup.example.com", "target_port": "2222",
		"target_parent": "pool2/replicas", "host_keys": "[backup.example.com]:2222 " + hk + "\n", "schedule": "hourly"}
	for _, bad := range []map[string]string{
		{"target_host": "-oProxyCommand=x"}, {"target_port": "70000"}, {"schedule": "monthly"},
		{"host_keys": ""}, {"host_keys": "ssh-ed25519 notbase64"}, {"target_parent": "../x"},
	} {
		q := map[string]string{}
		for k, v := range p {
			q[k] = v
		}
		for k, v := range bad {
			q[k] = v
		}
		if _, err := zfsReplPolicyOp(ctx, q); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	res, err := zfsReplPolicyOp(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	reps := res.(map[string]any)["replications"].([]*zfsReplPolicy)
	if len(reps) != 1 || reps[0].TargetName != "data" || reps[0].Keep != 3 || reps[0].TargetPort != 2222 {
		t.Fatalf("replications %+v", reps)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "zfs-repl-known_hosts"))
	if string(b) != "[backup.example.com]:2222 "+hk+"\n" {
		t.Errorf("known_hosts %q", b)
	}
	p2 := map[string]string{"dataset": "tank", "target_host": "10.0.0.9", "target_parent": "pool2", "host_keys": hk, "schedule": "manual"}
	if _, err := zfsReplPolicyOp(ctx, p2); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(dir, "zfs-repl-known_hosts"))
	if !strings.Contains(string(b), "\n10.0.0.9 "+hk+"\n") {
		t.Errorf("port 22 known_hosts %q", b)
	}

	// A run: the target has sm-repl-…100000 (guid 3), so the send is incremental.
	old := zfsCmd
	zfsCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		line := name + " " + strings.Join(args, " ")
		switch {
		case strings.HasPrefix(line, "zfs list -H -p -t snapshot -o name,guid"):
			return []byte("tank/data@sm-repl-20261022-090000\t1\ntank/data@sm-repl-20261022-100000\t3\ntank/data@sm-repl-20261022-110000\t9\n"), nil
		case strings.HasPrefix(line, "zfs list -H -p -t snapshot -o name"):
			return []byte("tank/data@sm-repl-20261022-090000\ntank/data@sm-repl-20261022-100000\ntank/data@sm-repl-20261022-110000\ntank/data@sm-repl-20261022-120000\n"), nil
		}
		return old(ctx, name, args...)
	}
	oldSSH, oldSend := replSSH, zfsSendTo
	t.Cleanup(func() { replSSH, zfsSendTo = oldSSH, oldSend })
	var remotes []string
	var received int
	replSSH = func(_ context.Context, pol *zfsReplPolicy, remote string, stdin io.Reader, stdout io.Writer) error {
		remotes = append(remotes, remote)
		switch {
		case strings.HasPrefix(remote, "list "):
			_, _ = io.WriteString(stdout, "sm-repl-20261022-090000 1\nsm-repl-20261022-100000 3\n")
		case strings.HasPrefix(remote, "recv "):
			b, _ := io.ReadAll(stdin)
			received = len(b)
		}
		return nil
	}
	var sendArgs []string
	zfsSendTo = func(_ context.Context, args []string, w io.Writer) error {
		sendArgs = args
		_, err := w.Write(make([]byte, 4096))
		return err
	}
	*ran = nil
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	st, err := runReplication(ctx, "tank/data", "backup.example.com", false, now, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(sendArgs, " ") != "send -I @sm-repl-20261022-100000 tank/data@sm-repl-20261022-120000" {
		t.Errorf("send %v", sendArgs)
	}
	if !slices.Equal(remotes, []string{"list data", "recv data", "prune data 3"}) || received != 4096 {
		t.Errorf("remote %v received %d", remotes, received)
	}
	if !slices.Equal(*ran, []string{"zfs snapshot tank/data@sm-repl-20261022-120000", "zfs destroy tank/data@sm-repl-20261022-090000"}) {
		t.Errorf("ran %q", *ran)
	}
	r := st.Replications[0]
	if !r.LastOK || r.LastBytes != 4096 || r.LastRun != now.Unix() || r.LastError != "" {
		t.Errorf("recorded %+v", r)
	}

	// A failed receive is recorded and returned.
	replSSH = func(_ context.Context, _ *zfsReplPolicy, remote string, stdin io.Reader, _ io.Writer) error {
		if strings.HasPrefix(remote, "recv ") {
			return errors.New("ssh recv: permission denied")
		}
		return nil
	}
	zfsSendTo = func(ctx context.Context, args []string, w io.Writer) error {
		sendArgs = args
		_, err := w.Write(make([]byte, 10))
		return err
	}
	st, err = runReplication(ctx, "tank/data", "backup.example.com", false, now.Add(time.Hour), t.Logf)
	if err == nil || st.Replications[0].LastOK || !strings.Contains(st.Replications[0].LastError, "permission denied") {
		t.Errorf("failed run: %v %+v", err, st.Replications[0])
	}
	if sendArgs[1] != "tank/data@sm-repl-20261022-130000" {
		t.Errorf("full send expected: %v", sendArgs)
	}

	res, _ = zfsReplPolicyOp(ctx, map[string]string{"dataset": "tank/data", "target_host": "backup.example.com", "delete": "1"})
	if n := len(res.(map[string]any)["replications"].([]*zfsReplPolicy)); n != 1 {
		t.Errorf("after delete: %d", n)
	}
	b, _ = os.ReadFile(filepath.Join(dir, "zfs-repl-known_hosts"))
	if strings.Contains(string(b), "backup.example.com") {
		t.Errorf("known_hosts keeps a deleted target: %q", b)
	}
}

func TestReplSSHArgs(t *testing.T) {
	dir := zfsStateTemp(t)
	got := strings.Join(replSSHArgs(&zfsReplPolicy{TargetHost: "10.0.0.9", TargetPort: 22}, "list data"), " ")
	want := "-i " + dir + "/zfs-repl -p 22 -o BatchMode=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=" + dir +
		"/zfs-repl-known_hosts -o ConnectTimeout=15 root@10.0.0.9 list data"
	if got != want {
		t.Errorf("got %s", got)
	}
}

func TestZfsReplKey(t *testing.T) {
	dir := zfsStateTemp(t)
	res, err := zfsReplKey()
	if err != nil {
		t.Fatal(err)
	}
	pub := res.(map[string]any)["public_key"].(string)
	if !strings.HasPrefix(pub, "ssh-ed25519 AAAA") || !strings.Contains(pub, " servermgr-zfs-repl@") {
		t.Errorf("public key %q", pub)
	}
	fi, err := os.Stat(filepath.Join(dir, "zfs-repl"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file %v %v", fi, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "zfs-repl"))
	if !bytes.HasPrefix(b, []byte("-----BEGIN OPENSSH PRIVATE KEY-----")) {
		t.Error("key is not OpenSSH PEM")
	}
	res2, _ := zfsReplKey()
	if res2.(map[string]any)["public_key"] != pub {
		t.Error("the key changed on the second call")
	}
}
