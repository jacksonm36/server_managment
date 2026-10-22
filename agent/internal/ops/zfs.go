package ops

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// ZFS pools, devices, datasets and snapshots, through zpool/zfs with
// parsable output. Every name and device is validated before it reaches a
// command line, and the pool and datasets that hold the running system
// cannot be exported, destroyed or renamed away.

var (
	zfsPoolName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.:-]{0,63}$`)
	zfsDSName   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.:/-]{0,250}$`)
	zfsSnapPart = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,100}$`)
	zfsDevPath  = regexp.MustCompile(`^/dev/[A-Za-z0-9/_.:-]{1,200}$`)
	zfsGUID     = regexp.MustCompile(`^\d{1,20}$`)
	zfsPropVal  = regexp.MustCompile(`^[A-Za-z0-9_.:/@+-]{1,200}$`)
	zfsSize     = regexp.MustCompile(`^\d+(\.\d+)?[KMGTP]?$`)
	zfsCompress = regexp.MustCompile(`^(on|off|lz4|lzjb|zle|gzip(-[1-9])?|zstd(-([1-9]|1[0-9]))?)$`)
)

// zfsProps are the dataset properties the panel may set.
var zfsProps = []string{"compression", "atime", "relatime", "recordsize", "volblocksize", "quota", "refquota", "reservation",
	"refreservation", "sync", "dedup", "xattr", "acltype", "mountpoint", "readonly", "canmount", "copies", "primarycache",
	"secondarycache", "logbias", "snapdir", "com.sun:auto-snapshot"}

// zfsVdevMin is the fewest devices each kind of vdev takes.
var zfsVdevMin = map[string]int{"": 1, "mirror": 2, "raidz": 3, "raidz2": 4, "raidz3": 5, "log": 1, "cache": 1, "spare": 1, "special": 1}

// zfsMu keeps changes one at a time.
var zfsMu sync.Mutex

// zfsCmd runs zpool/zfs and returns standard output; errors carry standard
// error. A variable so tests can fake the commands.
var zfsCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(string(out))
		}
		if msg == "" {
			msg = err.Error()
		}
		return out, fmt.Errorf("%s %s: %s", name, firstArg(args), msg)
	}
	return out, nil
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// zfsInstalled reports whether the ZFS tools are present; a variable for tests.
var zfsInstalled = func() bool {
	_, err := exec.LookPath("zpool")
	return err == nil
}

// zfsDo runs a change and logs it with its output.
func zfsDo(ctx context.Context, logf func(string, ...any), name string, args ...string) error {
	logf("$ %s %s", name, strings.Join(args, " "))
	out, err := zfsCmd(ctx, name, args...)
	if s := strings.TrimSpace(string(out)); s != "" {
		logf("%s", s)
	}
	return err
}

// Files read for status; variables for tests.
var (
	zfsMountsFile = "/proc/self/mounts"
	zfsArcFile    = "/proc/spl/kstat/zfs/arcstats"
)

// ---- status ----

type zfsVdev struct {
	Name     string     `json:"name"`
	Type     string     `json:"type"`
	State    string     `json:"state"`
	Read     uint64     `json:"read"`
	Write    uint64     `json:"write"`
	Cksum    uint64     `json:"cksum"`
	Msg      string     `json:"msg"`
	Children []*zfsVdev `json:"children"`
	indent   int
}

type zfsPool struct {
	Name     string     `json:"name"`
	Health   string     `json:"health"`
	Size     uint64     `json:"size"`
	Alloc    uint64     `json:"alloc"`
	Free     uint64     `json:"free"`
	Frag     *float64   `json:"frag"`
	Cap      float64    `json:"cap"`
	Dedup    float64    `json:"dedup"`
	ReadOnly bool       `json:"readonly"`
	Scan     string     `json:"scan"`
	Errors   string     `json:"errors"`
	Status   string     `json:"status"`
	Action   string     `json:"action"`
	Vdevs    []*zfsVdev `json:"vdevs"`
	Root     bool       `json:"root"`
}

type zfsDataset struct {
	Name          string  `json:"name"`
	Type          string  `json:"type"`
	Used          uint64  `json:"used"`
	Avail         uint64  `json:"avail"`
	Refer         uint64  `json:"refer"`
	Mountpoint    string  `json:"mountpoint"`
	Compression   string  `json:"compression"`
	CompressRatio float64 `json:"compressratio"`
	RecordSize    *uint64 `json:"recordsize"`
	VolSize       *uint64 `json:"volsize"`
	Quota         uint64  `json:"quota"`
	Reservation   uint64  `json:"reservation"`
	Atime         string  `json:"atime"`
	Sync          string  `json:"sync"`
	Dedup         string  `json:"dedup"`
	Encryption    string  `json:"encryption"`
	KeyStatus     string  `json:"keystatus"`
	ReadOnly      string  `json:"readonly"`
	Mounted       bool    `json:"mounted"`
}

type zfsImportable struct {
	Name  string `json:"name"`
	ID    string `json:"id"`
	State string `json:"state"`
}

type zfsArc struct {
	Size   uint64 `json:"size"`
	Max    uint64 `json:"max"`
	Hits   uint64 `json:"hits"`
	Misses uint64 `json:"misses"`
}

func zfsStatus(ctx context.Context) (any, error) {
	if !zfsInstalled() {
		return map[string]any{"installed": false}, nil
	}
	out, err := zfsCmd(ctx, "zpool", "list", "-H", "-p", "-o", "name,size,alloc,free,frag,cap,dedup,health,readonly")
	if err != nil {
		return nil, err
	}
	pools := parseZpoolList(string(out))
	rootDS := zfsRootDataset()
	for _, p := range pools {
		p.Root = rootDS != "" && zfsPoolOf(rootDS) == p.Name
		st, err := zfsCmd(ctx, "zpool", "status", "-P", "-p", p.Name)
		if err != nil {
			p.Errors = err.Error()
			continue
		}
		parseZpoolStatus(string(st), p)
	}
	datasets, err := zfsDatasets(ctx)
	if err != nil {
		return nil, err
	}
	imp := []zfsImportable{}
	if out, err := zfsCmd(ctx, "zpool", "import"); err == nil {
		imp = parseZpoolImport(string(out))
	}
	res := map[string]any{"installed": true, "pools": pools, "datasets": datasets, "importable": imp, "arc": nil}
	res["policies"], res["replications"] = zfsPolicyState()
	if b, err := os.ReadFile(zfsArcFile); err == nil {
		res["arc"] = parseArcStats(string(b))
	}
	return res, nil
}

func parseUint(s string) uint64 {
	n, _ := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	return n
}

func parseRatio(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(s), "x"), 64)
	return f
}

// optUint is nil for "-" (a property that does not apply).
func optUint(s string) *uint64 {
	if s == "-" || s == "" {
		return nil
	}
	n := parseUint(s)
	return &n
}

func parseZpoolList(out string) []*zfsPool {
	pools := []*zfsPool{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 9 {
			continue
		}
		p := &zfsPool{Name: f[0], Size: parseUint(f[1]), Alloc: parseUint(f[2]), Free: parseUint(f[3]),
			Cap: parseRatio(strings.TrimSuffix(f[5], "%")), Dedup: parseRatio(f[6]), Health: f[7], ReadOnly: f[8] == "on", Vdevs: []*zfsVdev{}}
		if v, err := strconv.ParseFloat(strings.TrimSuffix(f[4], "%"), 64); err == nil {
			p.Frag = &v
		}
		pools = append(pools, p)
	}
	return pools
}

var zpoolStatusKey = regexp.MustCompile(`^\s*(pool|state|status|action|scan|see|config|errors|remove|checkpoint):\s?(.*)$`)

// zfsGroups are the config lines that group devices by role.
var zfsGroups = map[string]string{"logs": "log", "cache": "cache", "spares": "spare", "special": "special", "dedup": "dedup"}

// parseZpoolStatus fills scan, errors, status, action and the vdev tree of
// p from `zpool status -P -p`.
func parseZpoolStatus(out string, p *zfsPool) {
	fields := map[string][]string{}
	var key string
	var config []string
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "\t") {
			if m := zpoolStatusKey.FindStringSubmatch(line); m != nil {
				key = m[1]
				if v := strings.TrimSpace(m[2]); v != "" {
					fields[key] = append(fields[key], v)
				}
				continue
			}
		}
		if key == "config" {
			if strings.HasPrefix(line, "\t") {
				config = append(config, strings.TrimPrefix(line, "\t"))
			}
			continue
		}
		if v := strings.TrimSpace(line); v != "" && key != "" {
			fields[key] = append(fields[key], v)
		}
	}
	join := func(k string) string { return strings.Join(fields[k], " ") }
	p.Scan, p.Errors, p.Status, p.Action = join("scan"), join("errors"), join("status"), join("action")
	p.Vdevs = parseZpoolConfig(config)
}

func zfsVdevType(name string) string {
	switch {
	case strings.HasPrefix(name, "mirror"):
		return "mirror"
	case strings.HasPrefix(name, "raidz1"), name == "raidz" || strings.HasPrefix(name, "raidz-"):
		return "raidz1"
	case strings.HasPrefix(name, "raidz2"):
		return "raidz2"
	case strings.HasPrefix(name, "raidz3"):
		return "raidz3"
	case strings.HasPrefix(name, "draid"):
		return "draid"
	case strings.HasPrefix(name, "replacing"):
		return "replacing"
	case strings.HasPrefix(name, "spare-"):
		return "spare-group"
	}
	return "disk"
}

// parseZpoolConfig builds the vdev tree from the config lines (the leading
// tab removed): two spaces of indent per level.
func parseZpoolConfig(lines []string) []*zfsVdev {
	var pool *zfsVdev
	groups := []*zfsVdev{}
	var stack []*zfsVdev
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " ")
		if trimmed == "" {
			continue
		}
		f := strings.Fields(trimmed)
		if f[0] == "NAME" && len(f) > 1 && f[1] == "STATE" {
			continue
		}
		indent := (len(line) - len(trimmed)) / 2
		v := &zfsVdev{Name: f[0], indent: indent, Children: []*zfsVdev{}}
		if indent == 0 {
			if g, ok := zfsGroups[f[0]]; ok && len(f) == 1 {
				v.Type = g
			} else {
				// The pool itself: its children are the data vdevs.
				v.Type = "pool"
			}
		} else {
			v.Type = zfsVdevType(f[0])
		}
		if len(f) > 1 {
			v.State = f[1]
		}
		if len(f) >= 5 {
			v.Read, v.Write, v.Cksum = parseUint(f[2]), parseUint(f[3]), parseUint(f[4])
			v.Msg = strings.Join(f[5:], " ")
		} else if len(f) > 2 {
			v.Msg = strings.Join(f[2:], " ")
		}
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		switch {
		case len(stack) > 0:
			parent := stack[len(stack)-1]
			parent.Children = append(parent.Children, v)
		case v.Type == "pool":
			pool = v
		default:
			groups = append(groups, v)
		}
		stack = append(stack, v)
	}
	// The pool's own line holds the data vdevs; groups (logs, cache…) follow.
	if pool == nil {
		return groups
	}
	return append(pool.Children, groups...)
}

func parseZpoolImport(out string) []zfsImportable {
	res := []zfsImportable{}
	var cur *zfsImportable
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "pool":
			res = append(res, zfsImportable{Name: v})
			cur = &res[len(res)-1]
		case "id":
			if cur != nil {
				cur.ID = v
			}
		case "state":
			if cur != nil {
				cur.State = v
			}
		}
	}
	return res
}

func parseArcStats(s string) *zfsArc {
	a := &zfsArc{}
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		switch f[0] {
		case "size":
			a.Size = parseUint(f[2])
		case "c_max":
			a.Max = parseUint(f[2])
		case "hits":
			a.Hits = parseUint(f[2])
		case "misses":
			a.Misses = parseUint(f[2])
		}
	}
	return a
}

const zfsDSFields = "name,type,used,avail,refer,mountpoint,compression,compressratio,recordsize,volsize,quota,reservation,atime,sync,dedup,encryption,keystatus,readonly,mounted"

func zfsDatasets(ctx context.Context) ([]zfsDataset, error) {
	out, err := zfsCmd(ctx, "zfs", "list", "-H", "-p", "-t", "filesystem,volume", "-o", zfsDSFields)
	if err != nil && strings.Contains(err.Error(), "bad property") {
		// ZFS without native encryption (before 0.8).
		fields := strings.Replace(zfsDSFields, "encryption,keystatus,", "", 1)
		out, err = zfsCmd(ctx, "zfs", "list", "-H", "-p", "-t", "filesystem,volume", "-o", fields)
		if err != nil {
			return nil, err
		}
		return parseZfsList(string(out), false), nil
	}
	if err != nil {
		return nil, err
	}
	return parseZfsList(string(out), true), nil
}

func parseZfsList(out string, enc bool) []zfsDataset {
	list := []zfsDataset{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if !enc && len(f) == 17 {
			f = append(f[:15], append([]string{"-", "-"}, f[15:]...)...)
		}
		if len(f) < 19 {
			continue
		}
		list = append(list, zfsDataset{Name: f[0], Type: f[1], Used: parseUint(f[2]), Avail: parseUint(f[3]), Refer: parseUint(f[4]),
			Mountpoint: f[5], Compression: f[6], CompressRatio: parseRatio(f[7]), RecordSize: optUint(f[8]), VolSize: optUint(f[9]),
			Quota: parseUint(f[10]), Reservation: parseUint(f[11]), Atime: f[12], Sync: f[13], Dedup: f[14], Encryption: f[15],
			KeyStatus: f[16], ReadOnly: f[17], Mounted: f[18] == "yes"})
	}
	return list
}

// zfsRootDataset is the dataset mounted at /, or "".
func zfsRootDataset() string {
	b, err := os.ReadFile(zfsMountsFile)
	if err != nil {
		return ""
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 3 && f[1] == "/" && f[2] == "zfs" {
			return f[0]
		}
	}
	return ""
}

// zfsMountedAt lists the zfs datasets mounted at or under prefix.
func zfsMountedAt(prefix string) []string {
	b, err := os.ReadFile(zfsMountsFile)
	if err != nil {
		return nil
	}
	var ds []string
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && f[2] == "zfs" && (f[1] == prefix || strings.HasPrefix(f[1], prefix+"/")) {
			ds = append(ds, f[0])
		}
	}
	return ds
}

func zfsPoolOf(ds string) string {
	p, _, _ := strings.Cut(ds, "/")
	p, _, _ = strings.Cut(p, "@")
	return p
}

// ---- validation ----

func validDataset(name string) error {
	if !zfsDSName.MatchString(name) || strings.Contains(name, "//") || strings.Contains(name, "..") || strings.HasSuffix(name, "/") {
		return fmt.Errorf("invalid dataset name %q", name)
	}
	if !zfsPoolName.MatchString(zfsPoolOf(name)) {
		return fmt.Errorf("invalid pool name in %q", name)
	}
	return nil
}

func validSnapshot(name string) error {
	ds, snap, ok := strings.Cut(name, "@")
	if !ok || !zfsSnapPart.MatchString(snap) {
		return fmt.Errorf("invalid snapshot name %q (dataset@name)", name)
	}
	return validDataset(ds)
}

func validPool(name string) error {
	if !zfsPoolName.MatchString(name) {
		return fmt.Errorf("invalid pool name %q", name)
	}
	return nil
}

// isBlockDevice is a variable for tests.
var isBlockDevice = func(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode()&os.ModeDevice != 0
}

// zfsLsblk returns lsblk's JSON tree; a variable for tests.
var zfsLsblk = func(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "lsblk", "-J", "-o", "NAME,PATH,MOUNTPOINT,FSTYPE").Output()
}

type lsblkDev struct {
	Name       string     `json:"name"`
	Path       string     `json:"path"`
	Mountpoint *string    `json:"mountpoint"`
	FSType     *string    `json:"fstype"`
	Children   []lsblkDev `json:"children"`
}

// inUse reports why d or a partition of it cannot be given to a pool.
func (d lsblkDev) inUse() string {
	if d.Mountpoint != nil && *d.Mountpoint != "" {
		return "mounted at " + *d.Mountpoint
	}
	if d.FSType != nil && *d.FSType == "zfs_member" {
		return "already part of a ZFS pool"
	}
	for _, c := range d.Children {
		if why := c.inUse(); why != "" {
			return c.Path + " is " + why
		}
	}
	return ""
}

func findLsblk(devs []lsblkDev, path string) *lsblkDev {
	for i := range devs {
		if devs[i].Path == path {
			return &devs[i]
		}
		if d := findLsblk(devs[i].Children, path); d != nil {
			return d
		}
	}
	return nil
}

// validDevice checks a device path; fresh devices (new to a pool) must
// exist and be unused.
func validDevice(ctx context.Context, dev string, fresh bool) error {
	if !zfsDevPath.MatchString(dev) || strings.Contains(dev, "..") {
		return fmt.Errorf("invalid device %q", dev)
	}
	if !fresh {
		return nil
	}
	if !isBlockDevice(dev) {
		return fmt.Errorf("%s is not a block device", dev)
	}
	real, err := filepath.EvalSymlinks(dev)
	if err != nil {
		real = dev
	}
	out, err := zfsLsblk(ctx)
	if err != nil {
		return fmt.Errorf("lsblk: %w", err)
	}
	var tree struct {
		Blockdevices []lsblkDev `json:"blockdevices"`
	}
	if err := json.Unmarshal(out, &tree); err != nil {
		return fmt.Errorf("lsblk: %w", err)
	}
	if d := findLsblk(tree.Blockdevices, real); d != nil {
		if why := d.inUse(); why != "" {
			return fmt.Errorf("%s is in use: %s", dev, why)
		}
	}
	return nil
}

// validPoolDevice is a device already in a pool: a path, or the GUID zpool
// shows for a missing one.
func validPoolDevice(dev string) error {
	if zfsGUID.MatchString(dev) {
		return nil
	}
	return validDevice(context.Background(), dev, false)
}

// zfsPropPairs checks "key=value" lines (or comma-separated pairs) against
// the allowlist.
func zfsPropPairs(props string) ([]string, error) {
	var pairs []string
	for _, line := range strings.FieldsFunc(props, func(r rune) bool { return r == '\n' || r == ',' }) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || !slices.Contains(zfsProps, k) {
			return nil, fmt.Errorf("property %q cannot be set here", k)
		}
		if !zfsPropVal.MatchString(v) {
			return nil, fmt.Errorf("invalid value for %s", k)
		}
		pairs = append(pairs, k+"="+v)
	}
	return pairs, nil
}

// ---- changes ----

func zfsPoolAction(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	action, pool := p["action"], p["pool"]
	if action == "import" {
		if !zfsGUID.MatchString(pool) && validPool(pool) != nil {
			return nil, errors.New("invalid pool name or id")
		}
	} else if err := validPool(pool); err != nil {
		return nil, err
	}
	var args []string
	switch action {
	case "scrub":
		args = []string{"scrub", pool}
	case "scrub_pause":
		args = []string{"scrub", "-p", pool}
	case "scrub_stop":
		args = []string{"scrub", "-s", pool}
	case "trim":
		args = []string{"trim", pool}
	case "clear":
		args = []string{"clear", pool}
	case "upgrade":
		args = []string{"upgrade", pool}
	case "export":
		if zfsPoolOf(zfsRootDataset()) == pool {
			return nil, errors.New(pool + " holds the running system; it cannot be exported")
		}
		args = []string{"export", pool}
	case "import":
		args = []string{"import", pool}
	default:
		return nil, errors.New("action must be scrub, scrub_pause, scrub_stop, trim, clear, upgrade, export or import")
	}
	zfsMu.Lock()
	err := zfsDo(ctx, logf, "zpool", args...)
	zfsMu.Unlock()
	if err != nil {
		return nil, err
	}
	return zfsStatus(ctx)
}

func zfsDeviceAction(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	pool := p["pool"]
	if err := validPool(pool); err != nil {
		return nil, err
	}
	var args []string
	switch action := p["action"]; action {
	case "add":
		vdev := p["vdev"]
		min, ok := zfsVdevMin[vdev]
		if !ok {
			return nil, errors.New("vdev must be empty (stripe), mirror, raidz, raidz2, raidz3, log, cache, spare or special")
		}
		devs := strings.Fields(p["devices"])
		if len(devs) < min {
			return nil, fmt.Errorf("%s needs at least %d devices", cmpStr(vdev, "a stripe"), min)
		}
		for _, d := range devs {
			if err := validDevice(ctx, d, true); err != nil {
				return nil, err
			}
		}
		args = []string{"add", pool}
		if vdev != "" {
			args = append(args, vdev)
		}
		args = append(args, devs...)
	case "attach", "replace":
		dev, nd := p["device"], p["new_device"]
		if err := validPoolDevice(dev); err != nil {
			return nil, err
		}
		if nd == "" && action == "attach" {
			return nil, errors.New("attach needs new_device")
		}
		args = []string{action, pool, dev}
		if nd != "" {
			if err := validDevice(ctx, nd, true); err != nil {
				return nil, err
			}
			args = append(args, nd)
		}
	case "detach", "offline", "online", "remove":
		dev := p["device"]
		if err := validPoolDevice(dev); err != nil {
			return nil, err
		}
		args = []string{action, pool, dev}
	default:
		return nil, errors.New("action must be add, attach, detach, replace, offline, online or remove")
	}
	zfsMu.Lock()
	err := zfsDo(ctx, logf, "zpool", args...)
	zfsMu.Unlock()
	if err != nil {
		return nil, err
	}
	return zfsStatus(ctx)
}

func cmpStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// zfsProtected reports why a dataset must not be destroyed or renamed.
func zfsProtected(ctx context.Context, name string) string {
	if !strings.Contains(name, "/") {
		return "it is the pool's root dataset (destroy the pool instead)"
	}
	return zfsHoldsSystem(ctx, name)
}

// zfsHoldsSystem reports why a dataset carries the running system (/ or
// /boot, itself or below it), or "".
func zfsHoldsSystem(ctx context.Context, name string) string {
	if root := zfsRootDataset(); root != "" && (root == name || strings.HasPrefix(root, name+"/")) {
		return "it holds the running system"
	}
	for _, ds := range zfsMountedAt("/boot") {
		if ds == name || strings.HasPrefix(ds, name+"/") {
			return "it holds /boot"
		}
	}
	if out, err := zfsCmd(ctx, "zfs", "get", "-H", "-o", "value", "mountpoint", name); err == nil {
		if mp := strings.TrimSpace(string(out)); mp == "/" || mp == "/boot" || strings.HasPrefix(mp, "/boot/") {
			return "it is mounted at " + mp
		}
	}
	return ""
}

// zfsSystemDirs may not be a dataset's mountpoint, nor anything below the
// ones that are the system's own (a dataset there would hide it).
var zfsSystemDirs = []string{"/", "/bin", "/boot", "/dev", "/etc", "/lib", "/lib32", "/lib64", "/libx32", "/proc", "/root",
	"/run", "/sbin", "/sys", "/usr", "/var", "/var/lib", "/var/log", "/var/lib/dpkg", "/var/lib/apt"}

func validMountpoint(mp string) error {
	if mp == "none" || mp == "legacy" {
		return nil
	}
	if !strings.HasPrefix(mp, "/") || strings.Contains(mp, "..") || strings.Contains(mp, "//") {
		return fmt.Errorf("mountpoint must be none, legacy or an absolute path")
	}
	mp = strings.TrimSuffix(mp, "/")
	if mp == "" {
		mp = "/"
	}
	for _, d := range zfsSystemDirs {
		if mp == d {
			return fmt.Errorf("%s is a system directory; a dataset mounted there would hide it", mp)
		}
	}
	for _, d := range []string{"/bin", "/boot", "/dev", "/etc", "/lib", "/lib32", "/lib64", "/libx32", "/proc", "/run", "/sbin", "/sys", "/usr"} {
		if strings.HasPrefix(mp, d+"/") {
			return fmt.Errorf("%s is inside the system directory %s", mp, d)
		}
	}
	return nil
}

// zfsCheckProps refuses property changes that would break the running
// system: a mountpoint over a system directory, and making the datasets
// that hold / or /boot read-only, unmountable or mounted elsewhere.
func zfsCheckProps(ctx context.Context, name string, keys, pairs []string) error {
	for _, kv := range pairs {
		if k, v, _ := strings.Cut(kv, "="); k == "mountpoint" {
			if err := validMountpoint(v); err != nil {
				return err
			}
		}
	}
	for _, k := range keys {
		if k == "mountpoint" || k == "readonly" || k == "canmount" {
			if why := zfsHoldsSystem(ctx, name); why != "" {
				return fmt.Errorf("%s cannot be changed on %s: %s", k, name, why)
			}
		}
	}
	return nil
}

func propKeys(pairs []string) []string {
	keys := make([]string, 0, len(pairs))
	for _, kv := range pairs {
		k, _, _ := strings.Cut(kv, "=")
		keys = append(keys, k)
	}
	return keys
}

func zfsDatasetAction(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	name := p["name"]
	if err := validDataset(name); err != nil {
		return nil, err
	}
	var args []string
	switch p["action"] {
	case "create":
		pairs, err := zfsPropPairs(p["props"])
		if err != nil {
			return nil, err
		}
		if !strings.Contains(name, "/") {
			return nil, errors.New("a dataset goes inside a pool: pool/name")
		}
		if err := zfsCheckProps(ctx, name, nil, pairs); err != nil {
			return nil, err
		}
		args = []string{"create"}
		switch p["type"] {
		case "", "filesystem":
		case "volume":
			if !zfsSize.MatchString(p["size"]) {
				return nil, errors.New("a volume needs a size such as 10G")
			}
			args = append(args, "-V", p["size"])
		default:
			return nil, errors.New("type must be filesystem or volume")
		}
		for _, kv := range pairs {
			args = append(args, "-o", kv)
		}
		args = append(args, name)
	case "destroy":
		if why := zfsProtected(ctx, name); why != "" {
			return nil, fmt.Errorf("%s cannot be destroyed: %s", name, why)
		}
		args = []string{"destroy"}
		if p["recursive"] == "1" {
			args = append(args, "-r")
		}
		args = append(args, name)
	case "set":
		pairs, err := zfsPropPairs(p["props"])
		if err != nil {
			return nil, err
		}
		if len(pairs) == 0 {
			return nil, errors.New("no properties to set")
		}
		if err := zfsCheckProps(ctx, name, propKeys(pairs), pairs); err != nil {
			return nil, err
		}
		args = append([]string{"set"}, append(pairs, name)...)
	case "inherit":
		keys := strings.FieldsFunc(p["props"], func(r rune) bool { return r == '\n' || r == ',' || r == ' ' })
		if len(keys) == 0 {
			return nil, errors.New("no properties to inherit")
		}
		for _, k := range keys {
			if !slices.Contains(zfsProps, k) {
				return nil, fmt.Errorf("property %q cannot be changed here", k)
			}
		}
		if err := zfsCheckProps(ctx, name, keys, nil); err != nil {
			return nil, err
		}
		// zfs inherit takes one property per run.
		zfsMu.Lock()
		defer zfsMu.Unlock()
		for _, k := range keys {
			if err := zfsDo(ctx, logf, "zfs", "inherit", k, name); err != nil {
				return nil, err
			}
		}
		return zfsStatus(ctx)
	case "rename":
		nn := p["new_name"]
		if err := validDataset(nn); err != nil {
			return nil, err
		}
		if zfsPoolOf(nn) != zfsPoolOf(name) {
			return nil, errors.New("a dataset can only be renamed within its pool")
		}
		if why := zfsProtected(ctx, name); why != "" {
			return nil, fmt.Errorf("%s cannot be renamed: %s", name, why)
		}
		args = []string{"rename", name, nn}
	default:
		return nil, errors.New("action must be create, destroy, set, inherit or rename")
	}
	zfsMu.Lock()
	err := zfsDo(ctx, logf, "zfs", args...)
	zfsMu.Unlock()
	if err != nil {
		return nil, err
	}
	return zfsStatus(ctx)
}

type zfsSnapshot struct {
	Name     string `json:"name"`
	Dataset  string `json:"dataset"`
	Used     uint64 `json:"used"`
	Refer    uint64 `json:"refer"`
	Creation int64  `json:"creation"`
}

const zfsMaxSnapshots = 2000

func zfsSnapshots(ctx context.Context, p map[string]string) (any, error) {
	args := []string{"list", "-H", "-p", "-t", "snapshot", "-o", "name,used,refer,creation", "-s", "creation"}
	if ds := p["dataset"]; ds != "" {
		if err := validDataset(ds); err != nil {
			return nil, err
		}
		args = append(args, "-r", ds)
	}
	out, err := zfsCmd(ctx, "zfs", args...)
	if err != nil {
		return nil, err
	}
	return map[string]any{"snapshots": parseSnapshots(string(out))}, nil
}

func parseSnapshots(out string) []zfsSnapshot {
	list := []zfsSnapshot{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 4 {
			continue
		}
		ds, _, _ := strings.Cut(f[0], "@")
		c, _ := strconv.ParseInt(f[3], 10, 64)
		list = append(list, zfsSnapshot{Name: f[0], Dataset: ds, Used: parseUint(f[1]), Refer: parseUint(f[2]), Creation: c})
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Creation > list[j].Creation })
	if len(list) > zfsMaxSnapshots {
		list = list[:zfsMaxSnapshots]
	}
	return list
}

func zfsSnapshotAction(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	name := p["name"]
	if err := validSnapshot(name); err != nil {
		return nil, err
	}
	ds, _, _ := strings.Cut(name, "@")
	rec := p["recursive"] == "1"
	var args []string
	switch p["action"] {
	case "create":
		args = []string{"snapshot"}
		if rec {
			args = append(args, "-r")
		}
		args = append(args, name)
	case "destroy":
		args = []string{"destroy"}
		if rec {
			args = append(args, "-r")
		}
		args = append(args, name)
	case "rollback":
		// Without recursive, zfs refuses when newer snapshots exist and
		// says so; with it, those newer snapshots are destroyed.
		args = []string{"rollback"}
		if rec {
			args = append(args, "-r")
		}
		args = append(args, name)
	case "clone":
		t := p["target"]
		if err := validDataset(t); err != nil {
			return nil, err
		}
		if zfsPoolOf(t) != zfsPoolOf(name) {
			return nil, errors.New("a clone goes in the same pool")
		}
		args = []string{"clone", name, t}
	default:
		return nil, errors.New("action must be create, destroy, rollback or clone")
	}
	zfsMu.Lock()
	err := zfsDo(ctx, logf, "zfs", args...)
	zfsMu.Unlock()
	if err != nil {
		return nil, err
	}
	return zfsSnapshots(ctx, map[string]string{"dataset": ds})
}

func zfsPoolCreate(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	name := p["name"]
	if err := validPool(name); err != nil {
		return nil, err
	}
	vdev := p["vdev"]
	if !slices.Contains([]string{"", "mirror", "raidz", "raidz2", "raidz3"}, vdev) {
		return nil, errors.New("vdev must be empty (stripe), mirror, raidz, raidz2 or raidz3")
	}
	devs := strings.Fields(p["devices"])
	if len(devs) < zfsVdevMin[vdev] {
		return nil, fmt.Errorf("%s needs at least %d devices", cmpStr(vdev, "a stripe"), zfsVdevMin[vdev])
	}
	for _, d := range devs {
		if err := validDevice(ctx, d, true); err != nil {
			return nil, err
		}
	}
	ashift := 12
	if s := p["ashift"]; s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 9 || n > 16 {
			return nil, errors.New("ashift must be 9 to 16")
		}
		ashift = n
	}
	comp := cmpStr(p["compression"], "lz4")
	if !zfsCompress.MatchString(comp) {
		return nil, errors.New("invalid compression")
	}
	if !zfsInstalled() {
		return nil, errors.New("ZFS is not installed on this server")
	}
	if _, err := zfsCmd(ctx, "zpool", "list", "-H", name); err == nil {
		return nil, fmt.Errorf("pool %s already exists", name)
	}
	args := []string{"create", "-o", "ashift=" + strconv.Itoa(ashift), "-O", "compression=" + comp, name}
	if vdev != "" {
		args = append(args, vdev)
	}
	args = append(args, devs...)
	zfsMu.Lock()
	err := zfsDo(ctx, logf, "zpool", args...)
	zfsMu.Unlock()
	if err != nil {
		return nil, err
	}
	return zfsStatus(ctx)
}

func zfsPoolDestroy(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	name := p["name"]
	if err := validPool(name); err != nil {
		return nil, err
	}
	if p["confirm"] != name {
		return nil, errors.New("type the pool's name to confirm")
	}
	if zfsPoolOf(zfsRootDataset()) == name {
		return nil, errors.New(name + " holds the running system; it cannot be destroyed")
	}
	for _, ds := range zfsMountedAt("/boot") {
		if zfsPoolOf(ds) == name {
			return nil, errors.New(name + " holds /boot; it cannot be destroyed")
		}
	}
	zfsMu.Lock()
	err := zfsDo(ctx, logf, "zpool", "destroy", name)
	zfsMu.Unlock()
	if err != nil {
		return nil, err
	}
	return zfsStatus(ctx)
}
