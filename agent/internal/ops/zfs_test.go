package ops

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const zpoolStatusMirror = `  pool: tank
 state: ONLINE
status: Some supported and requested features are not enabled on the pool.
	The pool can still be used, but some features are unavailable.
action: Enable all features using 'zpool upgrade'. Once this is done,
	the pool may no longer be accessible by software that does not support
	the features. See zpool-features(7) for details.
  scan: scrub repaired 0B in 00:01:02 with 0 errors on Sun Oct 12 00:25:03 2026
config:

	NAME                                   STATE     READ WRITE CKSUM
	tank                                   ONLINE       0     0     0
	  mirror-0                             ONLINE       0     0     0
	    /dev/disk/by-id/ata-WDC_A-part1    ONLINE       0     0     0
	    /dev/disk/by-id/ata-WDC_B-part1    ONLINE       0     0     3
	  mirror-1                             ONLINE       0     0     0
	    /dev/sdc1                          ONLINE       0     0     0
	    /dev/sdd1                          ONLINE       0     0     0
	logs
	  /dev/nvme0n1p1                       ONLINE       0     0     0
	cache
	  /dev/nvme0n1p2                       ONLINE       0     0     0
	spares
	  /dev/sde1                            AVAIL

errors: No known data errors
`

const zpoolStatusDegraded = `  pool: data
 state: DEGRADED
status: One or more devices is currently being resilvered.  The pool will
	continue to function, possibly in a degraded state.
action: Wait for the resilver to complete.
  scan: resilver in progress since Wed Oct 22 18:00:01 2026
	1.20T scanned at 512M/s, 600G issued at 256M/s, 3.00T total
	150G resilvered, 19.53% done, 02:43:10 to go
config:

	NAME                  STATE     READ WRITE CKSUM
	data                  DEGRADED     0     0     0
	  raidz2-0            DEGRADED     0     0     0
	    /dev/sda          ONLINE       0     0     0
	    /dev/sdb          ONLINE       0     0     0
	    replacing-2       DEGRADED     0     0     0
	      1234567890123   UNAVAIL      0     0     0  was /dev/sdc1
	      /dev/sdf        ONLINE       0     0     0  (resilvering)
	    /dev/sdd          ONLINE       0     0     0

errors: No known data errors
`

func TestParseZpoolStatus(t *testing.T) {
	p := &zfsPool{Name: "tank"}
	parseZpoolStatus(zpoolStatusMirror, p)
	if !strings.HasPrefix(p.Scan, "scrub repaired 0B") || p.Errors != "No known data errors" ||
		!strings.Contains(p.Status, "can still be used") || !strings.Contains(p.Action, "zpool-features(7)") {
		t.Fatalf("fields: %+v", p)
	}
	if len(p.Vdevs) != 5 {
		t.Fatalf("top vdevs = %d", len(p.Vdevs))
	}
	m0 := p.Vdevs[0]
	if m0.Name != "mirror-0" || m0.Type != "mirror" || len(m0.Children) != 2 || m0.Children[1].Cksum != 3 ||
		m0.Children[0].Name != "/dev/disk/by-id/ata-WDC_A-part1" || m0.Children[0].Type != "disk" {
		t.Errorf("mirror-0 = %+v", m0)
	}
	if p.Vdevs[2].Type != "log" || len(p.Vdevs[2].Children) != 1 || p.Vdevs[3].Type != "cache" ||
		p.Vdevs[4].Type != "spare" || p.Vdevs[4].Children[0].State != "AVAIL" {
		t.Errorf("groups = %+v %+v %+v", p.Vdevs[2], p.Vdevs[3], p.Vdevs[4])
	}

	d := &zfsPool{Name: "data"}
	parseZpoolStatus(zpoolStatusDegraded, d)
	if !strings.Contains(d.Scan, "resilver in progress") || !strings.Contains(d.Scan, "19.53% done") {
		t.Errorf("scan = %q", d.Scan)
	}
	if len(d.Vdevs) != 1 || d.Vdevs[0].Type != "raidz2" || len(d.Vdevs[0].Children) != 4 {
		t.Fatalf("vdevs = %+v", d.Vdevs)
	}
	r := d.Vdevs[0].Children[2]
	if r.Type != "replacing" || len(r.Children) != 2 || r.Children[0].State != "UNAVAIL" || r.Children[0].Msg != "was /dev/sdc1" ||
		r.Children[1].Msg != "(resilvering)" {
		t.Errorf("replacing = %+v %+v", r, r.Children)
	}
	b, _ := json.Marshal(d)
	if !strings.Contains(string(b), `"children":[]`) {
		t.Error("leaf children should be [] in JSON")
	}
}

func TestParseZpoolListAndDatasets(t *testing.T) {
	pools := parseZpoolList("tank\t1000\t400\t600\t12\t40\t1.00\tONLINE\toff\nrpool\t500\t100\t400\t-\t20\t1.05x\tDEGRADED\ton\n")
	if len(pools) != 2 || pools[0].Size != 1000 || *pools[0].Frag != 12 || pools[0].Cap != 40 || pools[1].Frag != nil ||
		pools[1].Dedup != 1.05 || !pools[1].ReadOnly || pools[1].Health != "DEGRADED" {
		t.Fatalf("pools = %+v %+v", pools[0], pools[1])
	}
	ds := parseZfsList("tank\tfilesystem\t100\t900\t50\t/tank\tlz4\t1.50\t131072\t-\t0\t0\ton\tstandard\toff\toff\t-\toff\tyes\n"+
		"tank/vm-1\tvolume\t200\t900\t20\t-\toff\t1.00\t-\t10737418240\t0\t0\t-\tstandard\toff\taes-256-gcm\tavailable\toff\t-\n", true)
	if len(ds) != 2 || *ds[0].RecordSize != 131072 || ds[0].VolSize != nil || !ds[0].Mounted || ds[0].CompressRatio != 1.5 ||
		*ds[1].VolSize != 10737418240 || ds[1].RecordSize != nil || ds[1].KeyStatus != "available" || ds[1].Type != "volume" {
		t.Fatalf("datasets = %+v", ds)
	}
	old := parseZfsList("tank\tfilesystem\t100\t900\t50\t/tank\tlz4\t1.50\t131072\t-\t0\t0\ton\tstandard\toff\toff\tyes\n", false)
	if len(old) != 1 || old[0].Encryption != "-" || !old[0].Mounted {
		t.Fatalf("pre-encryption = %+v", old)
	}
}

func TestParseImportArcSnapshots(t *testing.T) {
	imp := parseZpoolImport(`   pool: backup
     id: 8812345678901234567
  state: ONLINE
 action: The pool can be imported using its name or numeric identifier.
 config:

	backup      ONLINE
	  /dev/sdg  ONLINE
`)
	if len(imp) != 1 || imp[0].Name != "backup" || imp[0].ID != "8812345678901234567" || imp[0].State != "ONLINE" {
		t.Fatalf("import = %+v", imp)
	}
	a := parseArcStats("13 1 0x01 123 4567 89\nname type data\nhits 4 1000\nmisses 4 10\nc_max 4 8589934592\nsize 4 4294967296\n")
	if a.Size != 4294967296 || a.Max != 8589934592 || a.Hits != 1000 || a.Misses != 10 {
		t.Fatalf("arc = %+v", a)
	}
	snaps := parseSnapshots("tank@a\t10\t20\t100\ntank/x@b\t1\t2\t300\n")
	if len(snaps) != 2 || snaps[0].Name != "tank/x@b" || snaps[0].Dataset != "tank/x" || snaps[1].Creation != 100 {
		t.Fatalf("snaps = %+v", snaps)
	}
}

func TestZfsValidation(t *testing.T) {
	for _, ok := range []string{"tank", "tank/vm-100-disk-0", "rpool/ROOT/pve-1", "a.b:c_d"} {
		if err := validDataset(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "1tank", "tank/", "/tank", "tank//x", "tank/../x", "tank x", "tank;rm", "-o", "tank/x@s"} {
		if validDataset(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if validSnapshot("tank/x@daily-2026.10.22") != nil || validSnapshot("tank/x") == nil || validSnapshot("tank/x@a b") == nil {
		t.Error("snapshot validation")
	}
	if validPool("-f") == nil || validPool("tank") != nil {
		t.Error("pool validation")
	}
	if validPoolDevice("1234567890123") != nil || validPoolDevice("/dev/sda") != nil || validPoolDevice("/dev/../etc/x") == nil || validPoolDevice("sda") == nil {
		t.Error("pool device validation")
	}
	if _, err := zfsPropPairs("compression=zstd\natime=off"); err != nil {
		t.Error(err)
	}
	for _, bad := range []string{"keylocation=file:///x", "compression=a b", "mountpoint", "exec=off;x"} {
		if _, err := zfsPropPairs(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestZfsDeviceInUse(t *testing.T) {
	oldB, oldL := isBlockDevice, zfsLsblk
	defer func() { isBlockDevice, zfsLsblk = oldB, oldL }()
	isBlockDevice = func(p string) bool { return p != "/dev/nope" }
	zfsLsblk = func(context.Context) ([]byte, error) {
		return []byte(`{"blockdevices":[
			{"name":"sda","path":"/dev/sda","mountpoint":null,"fstype":null,"children":[{"name":"sda1","path":"/dev/sda1","mountpoint":"/","fstype":"ext4"}]},
			{"name":"sdb","path":"/dev/sdb","mountpoint":null,"fstype":null,"children":[{"name":"sdb1","path":"/dev/sdb1","mountpoint":null,"fstype":"zfs_member"}]},
			{"name":"sdc","path":"/dev/sdc","mountpoint":null,"fstype":null}]}`), nil
	}
	ctx := context.Background()
	if err := validDevice(ctx, "/dev/sda", true); err == nil || !strings.Contains(err.Error(), "mounted at /") {
		t.Errorf("sda: %v", err)
	}
	if err := validDevice(ctx, "/dev/sdb", true); err == nil || !strings.Contains(err.Error(), "ZFS") {
		t.Errorf("sdb: %v", err)
	}
	if err := validDevice(ctx, "/dev/sdc", true); err != nil {
		t.Errorf("sdc: %v", err)
	}
	if err := validDevice(ctx, "/dev/nope", true); err == nil {
		t.Error("missing device accepted")
	}
}

// fakeZfs records commands and answers status queries.
func fakeZfs(t *testing.T) *[]string {
	t.Helper()
	oldCmd, oldInst, oldMounts, oldArc := zfsCmd, zfsInstalled, zfsMountsFile, zfsArcFile
	t.Cleanup(func() { zfsCmd, zfsInstalled, zfsMountsFile, zfsArcFile = oldCmd, oldInst, oldMounts, oldArc })
	dir := t.TempDir()
	zfsMountsFile = filepath.Join(dir, "mounts")
	_ = os.WriteFile(zfsMountsFile, []byte("rpool/ROOT/pve-1 / zfs rw 0 0\ntank /tank zfs rw 0 0\n"), 0o644)
	zfsArcFile = filepath.Join(dir, "none")
	zfsInstalled = func() bool { return true }
	var ran []string
	zfsCmd = func(_ context.Context, name string, args ...string) ([]byte, error) {
		line := name + " " + strings.Join(args, " ")
		switch {
		case strings.HasPrefix(line, "zpool list -H -p"):
			return []byte("rpool\t100\t50\t50\t1\t50\t1.00\tONLINE\toff\ntank\t100\t50\t50\t1\t50\t1.00\tONLINE\toff\n"), nil
		case strings.HasPrefix(line, "zpool status"):
			return []byte(zpoolStatusMirror), nil
		case strings.HasPrefix(line, "zfs list") && strings.Contains(line, "snapshot"):
			return []byte("tank@s1\t1\t2\t3\n"), nil
		case strings.HasPrefix(line, "zfs list"), line == "zpool import":
			return nil, nil
		case strings.HasPrefix(line, "zfs get"):
			return []byte("/tank/x\n"), nil
		}
		ran = append(ran, line)
		return nil, nil
	}
	return &ran
}

func TestZfsActions(t *testing.T) {
	ran := fakeZfs(t)
	ctx := context.Background()
	logf := func(string, ...any) {}
	res, err := zfsStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pools := res.(map[string]any)["pools"].([]*zfsPool)
	if !pools[0].Root || pools[1].Root {
		t.Errorf("root flags: %v %v", pools[0].Root, pools[1].Root)
	}
	if _, err := zfsPoolAction(ctx, map[string]string{"action": "export", "pool": "rpool"}, logf); err == nil {
		t.Error("exporting the root pool allowed")
	}
	if _, err := zfsPoolAction(ctx, map[string]string{"action": "scrub", "pool": "tank"}, logf); err != nil {
		t.Error(err)
	}
	if _, err := zfsPoolDestroy(ctx, map[string]string{"name": "rpool", "confirm": "rpool"}, logf); err == nil {
		t.Error("destroying the root pool allowed")
	}
	if _, err := zfsPoolDestroy(ctx, map[string]string{"name": "tank", "confirm": "tan"}, logf); err == nil {
		t.Error("unconfirmed destroy allowed")
	}
	for _, name := range []string{"rpool/ROOT", "rpool", "tank"} {
		if _, err := zfsDatasetAction(ctx, map[string]string{"action": "destroy", "name": name}, logf); err == nil {
			t.Errorf("destroying %s allowed", name)
		}
	}
	if _, err := zfsDatasetAction(ctx, map[string]string{"action": "destroy", "name": "tank/x", "recursive": "1"}, logf); err != nil {
		t.Error(err)
	}
	if _, err := zfsDatasetAction(ctx, map[string]string{"action": "create", "name": "tank/vol", "type": "volume", "size": "10G", "props": "compression=zstd"}, logf); err != nil {
		t.Error(err)
	}
	if _, err := zfsDatasetAction(ctx, map[string]string{"action": "rename", "name": "tank/x", "new_name": "rpool/x"}, logf); err == nil {
		t.Error("cross-pool rename allowed")
	}
	if _, err := zfsSnapshotAction(ctx, map[string]string{"action": "rollback", "name": "tank@s1"}, logf); err != nil {
		t.Error(err)
	}
	if _, err := zfsDatasetAction(ctx, map[string]string{"action": "inherit", "name": "tank/x", "props": "compression atime"}, logf); err != nil {
		t.Error(err)
	}
	want := []string{
		"zpool scrub tank",
		"zfs destroy -r tank/x",
		"zfs create -V 10G -o compression=zstd tank/vol",
		"zfs rollback tank@s1",
		"zfs inherit compression tank/x",
		"zfs inherit atime tank/x",
	}
	if strings.Join(*ran, "\n") != strings.Join(want, "\n") {
		t.Errorf("ran:\n%s", strings.Join(*ran, "\n"))
	}
}

func TestZfsMountpointGuard(t *testing.T) {
	for _, mp := range []string{"none", "legacy", "/tank", "/srv/data", "/var/lib/vz2", "/mnt/x/"} {
		if err := validMountpoint(mp); err != nil {
			t.Errorf("%s: %v", mp, err)
		}
	}
	for _, mp := range []string{"/", "/etc", "/usr/", "/usr/local", "/boot/efi", "/var", "/proc/x", "relative", "/a/../etc", "//etc"} {
		if validMountpoint(mp) == nil {
			t.Errorf("%s: accepted", mp)
		}
	}
}
