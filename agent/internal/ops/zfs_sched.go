package ops

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

// Scheduled ZFS snapshots and replication to another server over SSH.
//
// The policies live in <state>/zfs.json. Snapshots are named
// sm-<schedule>-YYYYMMDD-HHMM (UTC) and pruning only ever touches names of
// that shape. Replication sends sm-repl-* snapshots with zfs send into a
// forced command on the target (servermgr-agent zfs-recv <parent>), which
// can only list, receive into and prune datasets under that parent.

// zfsStateDir holds zfs.json and the replication key; a variable for tests.
var zfsStateDir = "/etc/servermgr-agent"

// zfsStateMu guards zfs.json and the replication key.
var zfsStateMu sync.Mutex

type zfsSnapPolicy struct {
	Dataset   string `json:"dataset"`
	Schedule  string `json:"schedule"`
	Keep      int    `json:"keep"`
	Recursive bool   `json:"recursive"`
	LastRun   int64  `json:"last_run"`
	LastError string `json:"last_error"`
}

type zfsReplPolicy struct {
	Dataset      string   `json:"dataset"`
	TargetHost   string   `json:"target_host"`
	TargetPort   int      `json:"target_port"`
	TargetParent string   `json:"target_parent"`
	TargetName   string   `json:"target_name"`
	Schedule     string   `json:"schedule"`
	Keep         int      `json:"keep"`
	LastRun      int64    `json:"last_run"`
	LastOK       bool     `json:"last_ok"`
	LastError    string   `json:"last_error"`
	LastBytes    int64    `json:"last_bytes"`
	HostKeys     []string `json:"host_keys,omitempty"`
}

type zfsState struct {
	Policies     []*zfsSnapPolicy `json:"policies"`
	Replications []*zfsReplPolicy `json:"replications"`
}

func zfsStatePath() string { return filepath.Join(zfsStateDir, "zfs.json") }

// loadZFSState reads zfs.json; a missing file is an empty state.
func loadZFSState() (*zfsState, error) {
	st := &zfsState{}
	b, err := os.ReadFile(zfsStatePath())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, st); err != nil {
			return nil, fmt.Errorf("zfs.json: %w", err)
		}
	}
	if st.Policies == nil {
		st.Policies = []*zfsSnapPolicy{}
	}
	if st.Replications == nil {
		st.Replications = []*zfsReplPolicy{}
	}
	return st, nil
}

func saveZFSState(st *zfsState) error {
	if err := os.MkdirAll(zfsStateDir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(zfsStatePath(), b, 0o600); err != nil {
		return err
	}
	return writeReplKnownHosts(st)
}

// updateZFSState loads, changes and saves the state under the lock.
func updateZFSState(fn func(*zfsState) error) (*zfsState, error) {
	zfsStateMu.Lock()
	defer zfsStateMu.Unlock()
	st, err := loadZFSState()
	if err != nil {
		return nil, err
	}
	if err := fn(st); err != nil {
		return nil, err
	}
	return st, saveZFSState(st)
}

// zfsPolicyState is the part of zfs.status that lists the policies.
func zfsPolicyState() ([]*zfsSnapPolicy, []*zfsReplPolicy) {
	zfsStateMu.Lock()
	defer zfsStateMu.Unlock()
	st, err := loadZFSState()
	if err != nil {
		return []*zfsSnapPolicy{}, []*zfsReplPolicy{}
	}
	return st.Policies, st.Replications
}

// zfsDatasetExists asks zfs whether a dataset (or volume) is there.
func zfsDatasetExists(ctx context.Context, ds string) error {
	if _, err := zfsCmd(ctx, "zfs", "list", "-H", "-o", "name", ds); err != nil {
		return fmt.Errorf("dataset %s not found", ds)
	}
	return nil
}

// ---- schedules ----

var zfsSnapKeep = map[string]int{"hourly": 24, "daily": 7, "weekly": 4, "monthly": 12}

// zfsBucket is the start (UTC) of the schedule period that holds t.
func zfsBucket(schedule string, t time.Time) time.Time {
	t = t.UTC()
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	switch schedule {
	case "hourly":
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, time.UTC)
	case "weekly":
		return day.AddDate(0, 0, -((int(t.Weekday()) + 6) % 7)) // back to Monday
	case "monthly":
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	return day
}

// zfsDue reports whether a policy last run at last (unix) is due at now:
// once per period, so a run is never more than a tick late and a missed
// one (agent down) catches up at the next tick. Manual never is.
func zfsDue(schedule string, last int64, now time.Time) bool {
	switch schedule {
	case "hourly", "daily", "weekly", "monthly":
	default:
		return false
	}
	return last == 0 || zfsBucket(schedule, now).After(time.Unix(last, 0))
}

// zfsPruneList picks the snapshots (short names, any order) that match re
// to destroy, keeping the newest keep. Names sort by their timestamp.
func zfsPruneList(names []string, re *regexp.Regexp, keep int) []string {
	var ours []string
	for _, n := range names {
		if re.MatchString(n) {
			ours = append(ours, n)
		}
	}
	slices.Sort(ours)
	if keep < 1 {
		keep = 1
	}
	if len(ours) <= keep {
		return nil
	}
	return ours[:len(ours)-keep]
}

func zfsSnapRe(schedule string) *regexp.Regexp {
	return regexp.MustCompile(`^sm-` + regexp.QuoteMeta(schedule) + `-\d{8}-\d{4}$`)
}

var zfsReplRe = regexp.MustCompile(`^sm-repl-\d{8}-\d{6}$`)

// zfsShortSnaps lists the snapshots directly on ds as short names, oldest
// first.
func zfsShortSnaps(ctx context.Context, ds string) ([]string, error) {
	out, err := zfsCmd(ctx, "zfs", "list", "-H", "-p", "-t", "snapshot", "-o", "name", "-s", "creation", "-d", "1", ds)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, l := range strings.Split(string(out), "\n") {
		if d, s, ok := strings.Cut(strings.TrimSpace(l), "@"); ok && d == ds {
			names = append(names, s)
		}
	}
	return names, nil
}

// runSnapPolicy takes this period's snapshot and prunes the old ones.
func runSnapPolicy(ctx context.Context, pol *zfsSnapPolicy, now time.Time, logf func(string, ...any)) error {
	zfsMu.Lock()
	defer zfsMu.Unlock()
	snap := pol.Dataset + "@sm-" + pol.Schedule + "-" + now.UTC().Format("20060102-1504")
	args := []string{"snapshot"}
	if pol.Recursive {
		args = append(args, "-r")
	}
	if err := zfsDo(ctx, logf, "zfs", append(args, snap)...); err != nil {
		return err
	}
	names, err := zfsShortSnaps(ctx, pol.Dataset)
	if err != nil {
		return err
	}
	for _, n := range zfsPruneList(names, zfsSnapRe(pol.Schedule), pol.Keep) {
		args := []string{"destroy"}
		if pol.Recursive {
			args = append(args, "-r")
		}
		if err := zfsDo(ctx, logf, "zfs", append(args, pol.Dataset+"@"+n)...); err != nil {
			return err
		}
	}
	return nil
}

// zfsSnapPolicyOp adds, changes or deletes a snapshot schedule.
func zfsSnapPolicyOp(ctx context.Context, p map[string]string) (any, error) {
	ds, sched := p["dataset"], p["schedule"]
	if err := validDataset(ds); err != nil {
		return nil, err
	}
	def, ok := zfsSnapKeep[sched]
	if !ok {
		return nil, fmt.Errorf("schedule must be hourly, daily, weekly or monthly")
	}
	del := p["delete"] == "1"
	keep := def
	if !del {
		if s := p["keep"]; s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 || n > 1000 {
				return nil, fmt.Errorf("keep must be between 1 and 1000")
			}
			keep = n
		}
		if err := zfsDatasetExists(ctx, ds); err != nil {
			return nil, err
		}
	}
	st, err := updateZFSState(func(st *zfsState) error {
		i := slices.IndexFunc(st.Policies, func(x *zfsSnapPolicy) bool { return x.Dataset == ds && x.Schedule == sched })
		switch {
		case del && i >= 0:
			st.Policies = slices.Delete(st.Policies, i, i+1)
		case del:
		case i >= 0:
			st.Policies[i].Keep = keep
			st.Policies[i].Recursive = p["recursive"] == "1"
		default:
			st.Policies = append(st.Policies, &zfsSnapPolicy{Dataset: ds, Schedule: sched, Keep: keep, Recursive: p["recursive"] == "1"})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"policies": st.Policies}, nil
}

// ---- replication key (source) ----

func replKeyPath() string        { return filepath.Join(zfsStateDir, "zfs-repl") }
func replKnownHostsPath() string { return filepath.Join(zfsStateDir, "zfs-repl-known_hosts") }

// replSigner loads the replication key, creating it on first use.
func replSigner() (ssh.Signer, error) {
	zfsStateMu.Lock()
	defer zfsStateMu.Unlock()
	if b, err := os.ReadFile(replKeyPath()); err == nil {
		return ssh.ParsePrivateKey(b)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	blk, err := ssh.MarshalPrivateKey(priv, "servermgr-zfs-repl")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(zfsStateDir, 0o700); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(replKeyPath(), pem.EncodeToMemory(blk), 0o600); err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(priv)
}

// zfsAddressHint is the first non-loopback, non-link-local IPv4 address;
// a variable for tests.
var zfsAddressHint = func() string {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if ip := n.IP.To4(); ip != nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
			return ip.String()
		}
	}
	return ""
}

func zfsReplKey() (any, error) {
	signer, err := replSigner()
	if err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	pub := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	return map[string]any{"public_key": pub + " servermgr-zfs-repl@" + host, "address_hint": zfsAddressHint()}, nil
}

// ---- authorizing a source (target) ----

// Paths and the agent's own path on the target; variables for tests.
var (
	authorizedKeysPath = "/root/.ssh/authorized_keys"
	sshHostKeyGlob     = "/etc/ssh/ssh_host_*_key.pub"
	zfsSSHPort         = sshPort
	agentExe           = func() (string, error) {
		exe, err := os.Executable()
		if err != nil {
			return "", err
		}
		return filepath.EvalSymlinks(exe)
	}
)

var safeExePath = regexp.MustCompile(`^/[A-Za-z0-9/_.+-]+$`)

// replAuthLine is the authorized_keys line that lets a source in.
func replAuthLine(from, exe, parent string, pub ssh.PublicKey) string {
	opts := "restrict"
	if from != "" {
		opts += `,from="` + from + `"`
	}
	key := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	return opts + `,command="` + exe + " zfs-recv " + parent + `" ` + key + " servermgr-zfs-repl"
}

// editAuthorizedKeys replaces every line for pub with line (removes them
// when line is empty) and keeps everything else as it was.
func editAuthorizedKeys(path string, pub ssh.PublicKey, line string) error {
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	want := pub.Marshal()
	var out []string
	placed := false
	for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(l)); err == nil && bytes.Equal(k.Marshal(), want) {
			if line != "" && !placed {
				out = append(out, line)
				placed = true
			}
			continue
		}
		if l != "" || len(out) > 0 {
			out = append(out, l)
		}
	}
	if line != "" && !placed {
		out = append(out, line)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	data := strings.Join(out, "\n")
	if data != "" {
		data += "\n"
	}
	return writeFileAtomic(path, []byte(data), 0o600)
}

// sshHostKeys reads the server's public host keys as "type key" lines.
func sshHostKeys() []string {
	files, _ := filepath.Glob(sshHostKeyGlob)
	slices.Sort(files)
	keys := []string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if fl := strings.Fields(string(b)); len(fl) >= 2 {
			keys = append(keys, fl[0]+" "+fl[1])
		}
	}
	return keys
}

func zfsReplAuthorize(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	pub, _, opts, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(p["public_key"])))
	if err != nil {
		return nil, fmt.Errorf("invalid public key: %v", err)
	}
	if pub.Type() != ssh.KeyAlgoED25519 || len(opts) > 0 {
		return nil, fmt.Errorf("the replication key must be a plain ssh-ed25519 key")
	}
	line := ""
	if p["revoke"] != "1" {
		parent := p["parent"]
		if err := validDataset(parent); err != nil {
			return nil, err
		}
		if err := zfsDatasetExists(ctx, parent); err != nil {
			return nil, err
		}
		from := strings.TrimSpace(p["from"])
		if from != "" {
			if ip := net.ParseIP(from); ip != nil {
				from = ip.String()
			} else if _, n, err := net.ParseCIDR(from); err == nil {
				from = n.String()
			} else {
				return nil, fmt.Errorf("from must be an IP address or CIDR")
			}
		}
		exe, err := agentExe()
		if err != nil {
			return nil, err
		}
		if !safeExePath.MatchString(exe) {
			return nil, fmt.Errorf("the agent's path %q cannot go in a forced command", exe)
		}
		line = replAuthLine(from, exe, parent, pub)
	}
	if err := editAuthorizedKeys(authorizedKeysPath, pub, line); err != nil {
		return nil, err
	}
	if line == "" {
		logf("removed the replication key from %s", authorizedKeysPath)
	} else {
		logf("authorized the replication key in %s", authorizedKeysPath)
	}
	return map[string]any{"host_keys": sshHostKeys(), "ssh_port": zfsSSHPort()}, nil
}

// ---- zfs-recv (the forced command on the target) ----

var replName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,200}$`)

// zfsRecvExec runs zfs recv with the SSH session's streams; a variable for
// tests.
var zfsRecvExec = func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, "zfs", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	return cmd.Run()
}

// ZFSRecv carries out one SSH_ORIGINAL_COMMAND from a replication source
// under parent: "list <name>", "recv <name>" or "prune <name> <keep>".
// It returns the exit code.
func ZFSRecv(parent, command string, stdin io.Reader, stdout, stderr io.Writer) int {
	ctx := context.Background()
	fail := func(f string, a ...any) int {
		fmt.Fprintf(stderr, "zfs-recv: "+f+"\n", a...)
		return 1
	}
	if err := validDataset(parent); err != nil {
		return fail("%v", err)
	}
	f := strings.Fields(command)
	if len(f) < 2 || !replName.MatchString(f[1]) {
		return fail("refused %q", command)
	}
	ds := parent + "/" + f[1]
	if validDataset(ds) != nil {
		return fail("refused %q", command)
	}
	switch {
	case f[0] == "list" && len(f) == 2:
		if zfsDatasetExists(ctx, ds) != nil {
			return 0
		}
		out, err := zfsCmd(ctx, "zfs", "list", "-H", "-p", "-t", "snapshot", "-o", "name,guid", "-s", "creation", "-d", "1", ds)
		if err != nil {
			return fail("%v", err)
		}
		for _, l := range strings.Split(string(out), "\n") {
			name, guid, ok := strings.Cut(strings.TrimSpace(l), "\t")
			if d, s, ok2 := strings.Cut(name, "@"); ok && ok2 && d == ds {
				fmt.Fprintf(stdout, "%s %s\n", s, strings.TrimSpace(guid))
			}
		}
		return 0
	case f[0] == "recv" && len(f) == 2:
		if err := zfsRecvExec(ctx, []string{"recv", "-s", "-u", "-F", ds}, stdin, stdout, stderr); err != nil {
			return fail("%v", err)
		}
		return 0
	case f[0] == "prune" && len(f) == 3:
		keep, err := strconv.Atoi(f[2])
		if err != nil || keep < 1 || keep > 1000 {
			return fail("refused %q", command)
		}
		names, err := zfsShortSnaps(ctx, ds)
		if err != nil {
			return fail("%v", err)
		}
		for _, n := range zfsPruneList(names, zfsReplRe, keep) {
			if _, err := zfsCmd(ctx, "zfs", "destroy", ds+"@"+n); err != nil {
				return fail("%v", err)
			}
		}
		return 0
	}
	return fail("refused %q", command)
}

// ---- replication policies (source) ----

var hostnameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,62})(\.[A-Za-z0-9]([A-Za-z0-9-]{0,62}))*$`)

// replTargetName is the dataset name under the target's parent: the source
// dataset without its pool, "/" as "-"; the pool itself keeps its name.
func replTargetName(ds string) string {
	_, rest, ok := strings.Cut(ds, "/")
	if !ok {
		return ds
	}
	return strings.ReplaceAll(rest, "/", "-")
}

// replHostPattern is how known_hosts names the target.
func replHostPattern(host string, port int) string {
	if port == 22 {
		return host
	}
	return "[" + host + "]:" + strconv.Itoa(port)
}

// parseHostKeys checks "type key" lines (a host field or comment around
// them is dropped).
func parseHostKeys(s string) ([]string, error) {
	var keys []string
	for _, l := range strings.Split(s, "\n") {
		f := strings.Fields(l)
		if len(f) == 0 {
			continue
		}
		if len(f) >= 3 && !strings.HasPrefix(f[0], "ssh-") && !strings.HasPrefix(f[0], "ecdsa-") && !strings.HasPrefix(f[0], "sk-") {
			f = f[1:] // a known_hosts line with its host
		}
		if len(f) < 2 {
			return nil, fmt.Errorf("invalid host key %q", l)
		}
		k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(f[0] + " " + f[1]))
		if err != nil || k.Type() != f[0] {
			return nil, fmt.Errorf("invalid host key %q", l)
		}
		keys = append(keys, f[0]+" "+f[1])
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("the target's host keys are needed")
	}
	return keys, nil
}

// writeReplKnownHosts writes the targets' host keys for ssh to check.
func writeReplKnownHosts(st *zfsState) error {
	var b strings.Builder
	for _, r := range st.Replications {
		for _, k := range r.HostKeys {
			b.WriteString(replHostPattern(r.TargetHost, r.TargetPort) + " " + k + "\n")
		}
	}
	if b.Len() == 0 {
		if err := os.Remove(replKnownHostsPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeFileAtomic(replKnownHostsPath(), []byte(b.String()), 0o600)
}

func zfsReplPolicyOp(ctx context.Context, p map[string]string) (any, error) {
	ds, host := p["dataset"], strings.ToLower(strings.TrimSpace(p["target_host"]))
	if err := validDataset(ds); err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	} else if len(host) > 253 || !hostnameRe.MatchString(host) {
		return nil, fmt.Errorf("invalid target host %q", p["target_host"])
	}
	pol := &zfsReplPolicy{Dataset: ds, TargetHost: host, TargetPort: 22, TargetParent: p["target_parent"],
		TargetName: replTargetName(ds), Schedule: p["schedule"], Keep: 3}
	del := p["delete"] == "1"
	if !del {
		if s := p["target_port"]; s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 || n > 65535 {
				return nil, fmt.Errorf("invalid target port %q", s)
			}
			pol.TargetPort = n
		}
		if err := validDataset(pol.TargetParent); err != nil {
			return nil, fmt.Errorf("target parent: %w", err)
		}
		switch pol.Schedule {
		case "hourly", "daily", "weekly", "manual":
		default:
			return nil, fmt.Errorf("schedule must be hourly, daily, weekly or manual")
		}
		if s := p["keep"]; s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 || n > 1000 {
				return nil, fmt.Errorf("keep must be between 1 and 1000")
			}
			pol.Keep = n
		}
		keys, err := parseHostKeys(p["host_keys"])
		if err != nil {
			return nil, err
		}
		pol.HostKeys = keys
		if err := zfsDatasetExists(ctx, ds); err != nil {
			return nil, err
		}
	}
	st, err := updateZFSState(func(st *zfsState) error {
		i := slices.IndexFunc(st.Replications, func(x *zfsReplPolicy) bool { return x.Dataset == ds && x.TargetHost == host })
		switch {
		case del && i >= 0:
			st.Replications = slices.Delete(st.Replications, i, i+1)
		case del:
		case i >= 0:
			old := st.Replications[i]
			pol.LastRun, pol.LastOK, pol.LastError, pol.LastBytes = old.LastRun, old.LastOK, old.LastError, old.LastBytes
			st.Replications[i] = pol
		default:
			st.Replications = append(st.Replications, pol)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"replications": st.Replications}, nil
}

// ---- running a replication ----

// replLocks keeps one replication per dataset at a time.
var (
	replLocksMu sync.Mutex
	replLocks   = map[string]*sync.Mutex{}
)

var errReplBusy = errors.New("a replication of this dataset is already running")

func replLock(ds string) *sync.Mutex {
	replLocksMu.Lock()
	defer replLocksMu.Unlock()
	if replLocks[ds] == nil {
		replLocks[ds] = &sync.Mutex{}
	}
	return replLocks[ds]
}

// replSSHArgs are ssh's arguments to run remote on the target.
func replSSHArgs(pol *zfsReplPolicy, remote string) []string {
	return []string{"-i", replKeyPath(), "-p", strconv.Itoa(pol.TargetPort), "-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + replKnownHostsPath(),
		"-o", "ConnectTimeout=15", "root@" + pol.TargetHost, remote}
}

// replSSH runs remote on the target with stdin and stdout; errors carry
// ssh's standard error. A variable for tests.
var replSSH = func(ctx context.Context, pol *zfsReplPolicy, remote string, stdin io.Reader, stdout io.Writer) error {
	cmd := exec.CommandContext(ctx, "ssh", replSSHArgs(pol, remote)...)
	var stderr bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("ssh %s: %s", strings.Fields(remote)[0], msg)
		}
		return fmt.Errorf("ssh %s: %w", strings.Fields(remote)[0], err)
	}
	return nil
}

// zfsSendTo runs zfs send with its stream into w; a variable for tests.
var zfsSendTo = func(ctx context.Context, args []string, w io.Writer) error {
	cmd := exec.CommandContext(ctx, "zfs", args...)
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = w, &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("zfs send: %s", msg)
		}
		return fmt.Errorf("zfs send: %w", err)
	}
	return nil
}

type replSnap struct{ Name, GUID string }

// replBase is the newest sm-repl snapshot of the source (oldest first in
// src) that the target also has, by guid; "" means a full send.
func replBase(src []replSnap, target map[string]bool, skip string) string {
	for i := len(src) - 1; i >= 0; i-- {
		s := src[i]
		if s.Name != skip && zfsReplRe.MatchString(s.Name) && target[s.GUID] {
			return s.Name
		}
	}
	return ""
}

// parseGUIDs reads "name guid" lines from the target's list.
func parseGUIDs(out string) map[string]bool {
	g := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		if f := strings.Fields(l); len(f) == 2 && zfsGUID.MatchString(f[1]) {
			g[f[1]] = true
		}
	}
	return g
}

type countWriter struct {
	w io.Writer
	n atomic.Int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(int64(n))
	return n, err
}

// replicate snapshots the dataset and sends it to the target, then prunes
// both sides. It returns the bytes sent.
func replicate(ctx context.Context, pol *zfsReplPolicy, now time.Time, logf func(string, ...any)) (int64, error) {
	ds, name := pol.Dataset, pol.TargetName
	snap := "sm-repl-" + now.UTC().Format("20060102-150405")
	if err := zfsDo(ctx, logf, "zfs", "snapshot", ds+"@"+snap); err != nil {
		return 0, err
	}
	out, err := zfsCmd(ctx, "zfs", "list", "-H", "-p", "-t", "snapshot", "-o", "name,guid", "-s", "creation", "-d", "1", ds)
	if err != nil {
		return 0, err
	}
	var src []replSnap
	for _, l := range strings.Split(string(out), "\n") {
		n, guid, ok := strings.Cut(strings.TrimSpace(l), "\t")
		if d, s, ok2 := strings.Cut(n, "@"); ok && ok2 && d == ds {
			src = append(src, replSnap{s, strings.TrimSpace(guid)})
		}
	}
	var listed bytes.Buffer
	if err := replSSH(ctx, pol, "list "+name, nil, &listed); err != nil {
		return 0, err
	}
	base := replBase(src, parseGUIDs(listed.String()), snap)
	args := []string{"send", ds + "@" + snap}
	if base != "" {
		args = []string{"send", "-I", "@" + base, ds + "@" + snap}
		logf("incremental send from @%s to %s:%s/%s", base, pol.TargetHost, pol.TargetParent, name)
	} else {
		logf("full send to %s:%s/%s", pol.TargetHost, pol.TargetParent, name)
	}

	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pr, pw := io.Pipe()
	cw := &countWriter{w: pw}
	sendErr := make(chan error, 1)
	go func() {
		err := zfsSendTo(sctx, args, cw)
		_ = pw.CloseWithError(err)
		sendErr <- err
	}()
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				logf("sent %d MiB", cw.n.Load()>>20)
			}
		}
	}()
	recvErr := replSSH(sctx, pol, "recv "+name, pr, io.Discard)
	close(done)
	if recvErr != nil {
		cancel()
	}
	_ = pr.CloseWithError(io.ErrClosedPipe) // a failed receive stops the send
	sErr := <-sendErr
	sent := cw.n.Load()
	if recvErr != nil {
		return sent, recvErr
	}
	if sErr != nil {
		return sent, sErr
	}
	logf("sent %d bytes", sent)

	if err := replSSH(ctx, pol, "prune "+name+" "+strconv.Itoa(pol.Keep), nil, io.Discard); err != nil {
		logf("pruning the target: %v", err)
	}
	names, err := zfsShortSnaps(ctx, ds)
	if err != nil {
		logf("pruning the source: %v", err)
		return sent, nil
	}
	for _, n := range zfsPruneList(names, zfsReplRe, pol.Keep) {
		if n == snap {
			continue
		}
		if err := zfsDo(ctx, logf, "zfs", "destroy", ds+"@"+n); err != nil {
			logf("pruning the source: %v", err)
		}
	}
	return sent, nil
}

// runReplication runs the policy for dataset and host if no replication of
// the dataset is under way, and records the outcome. When onlyIfDue, it
// checks again, under the lock, that the policy is still due.
func runReplication(ctx context.Context, ds, host string, onlyIfDue bool, now time.Time, logf func(string, ...any)) (*zfsState, error) {
	mu := replLock(ds)
	if !mu.TryLock() {
		return nil, errReplBusy
	}
	defer mu.Unlock()
	zfsStateMu.Lock()
	st, err := loadZFSState()
	zfsStateMu.Unlock()
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(st.Replications, func(x *zfsReplPolicy) bool { return x.Dataset == ds && x.TargetHost == host })
	if i < 0 {
		return nil, fmt.Errorf("no replication of %s to %s", ds, host)
	}
	pol := st.Replications[i]
	if onlyIfDue && !zfsDue(pol.Schedule, pol.LastRun, now) {
		return st, nil
	}
	sent, rerr := replicate(ctx, pol, now, logf)
	st, err = updateZFSState(func(st *zfsState) error {
		for _, r := range st.Replications {
			if r.Dataset == ds && r.TargetHost == host {
				r.LastRun, r.LastOK, r.LastBytes, r.LastError = now.Unix(), rerr == nil, sent, ""
				if rerr != nil {
					r.LastError = rerr.Error()
				}
			}
		}
		return nil
	})
	if rerr != nil {
		return st, rerr
	}
	return st, err
}

func zfsReplRun(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	st, err := runReplication(ctx, p["dataset"], strings.ToLower(strings.TrimSpace(p["target_host"])), false, time.Now(), logf)
	if err != nil {
		return nil, err
	}
	return map[string]any{"replications": st.Replications}, nil
}

// ---- scheduler ----

// StartZFS runs the snapshot and replication schedules every minute.
func StartZFS(ctx context.Context, log *slog.Logger) {
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if zfsInstalled() {
				runDueZFS(ctx, log, time.Now())
			}
		}
	}()
}

func runDueZFS(ctx context.Context, log *slog.Logger, now time.Time) {
	zfsStateMu.Lock()
	st, err := loadZFSState()
	zfsStateMu.Unlock()
	if err != nil {
		log.Warn("zfs schedules", "err", err)
		return
	}
	for _, pol := range st.Policies {
		if !zfsDue(pol.Schedule, pol.LastRun, now) {
			continue
		}
		logf := func(f string, a ...any) { log.Debug(fmt.Sprintf(f, a...)) }
		rerr := runSnapPolicy(ctx, pol, now, logf)
		if rerr != nil {
			log.Warn("scheduled zfs snapshot", "dataset", pol.Dataset, "schedule", pol.Schedule, "err", rerr)
		}
		_, err := updateZFSState(func(st *zfsState) error {
			for _, x := range st.Policies {
				if x.Dataset == pol.Dataset && x.Schedule == pol.Schedule {
					x.LastRun, x.LastError = now.Unix(), ""
					if rerr != nil {
						x.LastError = rerr.Error()
					}
				}
			}
			return nil
		})
		if err != nil {
			log.Warn("zfs schedules", "err", err)
		}
	}
	for _, r := range st.Replications {
		if !zfsDue(r.Schedule, r.LastRun, now) {
			continue
		}
		go func(ds, host string) {
			logf := func(f string, a ...any) { log.Info(fmt.Sprintf(f, a...), "dataset", ds, "target", host) }
			if _, err := runReplication(ctx, ds, host, true, now, logf); err != nil && !errors.Is(err, errReplBusy) {
				log.Warn("zfs replication", "dataset", ds, "target", host, "err", err)
			}
		}(r.Dataset, r.TargetHost)
	}
}
