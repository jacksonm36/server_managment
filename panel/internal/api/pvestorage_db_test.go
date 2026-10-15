package api

import (
	"slices"
	"testing"
)

// TestPVEStorageAdmin drives the storage page's routes against the fake
// Proxmox: storage config, volumes, downloads, disk wipe and ZFS pools,
// with the confirmation and in-use checks.
func TestPVEStorageAdmin(t *testing.T) {
	p := newTestPanel(t)
	call, login, expect := p.call, p.login, p.expect
	pve := newFakePVE(t)
	root, _ := login("root", rootPW)
	code, body, _ := call(root, "POST", "/api/v1/proxmox", map[string]any{
		"name": "lab", "url": pve.srv.URL, "token_id": fakeRootToken, "token_secret": fakeRootSecret, "fingerprint": pve.FP,
	})
	expect("add cluster", code, 200)
	base := "/api/v1/proxmox/" + body["cluster"].(map[string]any)["id"].(string)

	code, st, _ := call(root, "GET", base+"/storage", nil)
	expect("list storage", code, 200)
	if n := st["nodes"].(map[string]any); len(n) != 2 || len(st["config"].([]any)) != len(pve.storage) {
		t.Errorf("storage = %v", st)
	}

	// Create: types and their required fields are checked.
	add := func(b map[string]any) int { return first(call(root, "POST", base+"/storage", b)) }
	expect("dir without path", add(map[string]any{"storage": "backup2", "type": "dir", "content": []string{"backup"}}), 400)
	expect("relative path", add(map[string]any{"storage": "backup2", "type": "dir", "path": "../etc", "content": []string{"backup"}}), 400)
	expect("iso on lvmthin", add(map[string]any{"storage": "thin2", "type": "lvmthin", "vgname": "pve", "thinpool": "data2", "content": []string{"iso"}}), 400)
	expect("unknown type", add(map[string]any{"storage": "x", "type": "cephfs", "content": []string{"iso"}}), 400)
	expect("unknown node", add(map[string]any{"storage": "x", "type": "dir", "path": "/x", "content": []string{"iso"}, "nodes": []string{"pve9"}}), 400)
	expect("create dir", add(map[string]any{"storage": "backup2", "type": "dir", "path": "/mnt/backup2", "content": []string{"backup", "iso"}, "nodes": []string{"pve1"}}), 201)
	expect("create nfs", add(map[string]any{"storage": "nas2", "type": "nfs", "server": "10.0.0.5", "export": "/srv/pve", "content": []string{"backup"}}), 201)
	pve.mu.Lock()
	got := pve.storageCfg["backup2"]
	pve.mu.Unlock()
	if got.Get("path") != "/mnt/backup2" || got.Get("content") != "backup,iso" || got.Get("nodes") != "pve1" {
		t.Errorf("created dir storage = %v", got)
	}

	expect("update", first(call(root, "PUT", base+"/storage/backup2", map[string]any{"disable": true, "nodes": []string{}})), 204)
	pve.mu.Lock()
	got = pve.storageCfg["backup2"]
	pve.mu.Unlock()
	if got.Get("disable") != "1" || got.Get("delete") != "nodes" {
		t.Errorf("updated storage = %v", got)
	}
	expect("update unknown", first(call(root, "PUT", base+"/storage/nope", map[string]any{"disable": true})), 404)
	expect("delete unconfirmed", first(call(root, "DELETE", base+"/storage/nas2", nil)), 400)
	expect("delete", first(call(root, "DELETE", base+"/storage/nas2?confirm=nas2", nil)), 204)

	// Downloads and volumes.
	dl := func(b map[string]any) int {
		return first(call(root, "POST", base+"/nodes/pve1/storage/local/download", b))
	}
	expect("download ftp", dl(map[string]any{"url": "ftp://x/a.iso", "content": "iso"}), 400)
	expect("download bad name", dl(map[string]any{"url": "https://x/a.iso", "content": "iso", "filename": "../a.iso"}), 400)
	expect("download unknown node", first(call(root, "POST", base+"/nodes/pve9/storage/local/download", map[string]any{"url": "https://x/a.iso", "content": "iso"})), 404)
	code, d, _ := call(root, "POST", base+"/nodes/pve1/storage/local/download", map[string]any{"url": "https://cdimage.example/debian-13.iso", "content": "iso"})
	expect("download iso", code, 202)
	upid, _ := d["upid"].(string)
	code, task, _ := call(root, "GET", base+"/nodes/pve1/task?upid="+upid, nil)
	expect("task status", code, 200)
	if task["exitstatus"] != "OK" {
		t.Errorf("task = %v", task)
	}
	expect("task of another node", first(call(root, "GET", base+"/nodes/pve2/task?upid="+upid, nil)), 400)
	expect("volume of another storage", first(call(root, "DELETE", base+"/nodes/pve1/storage/local/content?volume=nas:iso/debian-13.iso&confirm=nas:iso/debian-13.iso", nil)), 400)
	expect("volume unconfirmed", first(call(root, "DELETE", base+"/nodes/pve1/storage/local/content?volume=local:iso/debian-13.iso", nil)), 400)
	expect("delete volume", first(call(root, "DELETE", base+"/nodes/pve1/storage/local/content?volume=local:iso/debian-13.iso&confirm=local:iso/debian-13.iso", nil)), 204)

	// Disks: only unused ones are wiped.
	wipe := func(disk, confirm string) int {
		return first(call(root, "POST", base+"/nodes/pve1/disks/wipe", map[string]any{"disk": disk, "confirm": confirm}))
	}
	expect("wipe unconfirmed", wipe("/dev/sdb", "sdb"), 400)
	expect("wipe LVM disk", wipe("/dev/sda", "/dev/sda"), 409)
	expect("wipe mounted disk", wipe("/dev/sdd", "/dev/sdd"), 409)
	expect("wipe missing disk", wipe("/dev/sdz", "/dev/sdz"), 404)
	expect("wipe path", wipe("/etc/passwd", "/etc/passwd"), 400)
	expect("wipe partitioned disk", wipe("/dev/sdc", "/dev/sdc"), 204)
	expect("smart", first(call(root, "GET", base+"/nodes/pve1/disks/smart?disk=/dev/sda", nil)), 200)
	pve.mu.Lock()
	wiped := slices.Clone(pve.wiped)
	pve.mu.Unlock()
	if !slices.Equal(wiped, []string{"/dev/sdc"}) {
		t.Errorf("wiped = %v", wiped)
	}

	// ZFS: RAID minimums and free disks.
	mkpool := func(b map[string]any) int { return first(call(root, "POST", base+"/nodes/pve1/zfs", b)) }
	expect("mirror of one", mkpool(map[string]any{"name": "tank", "raidlevel": "mirror", "devices": []string{"/dev/sdb"}}), 400)
	expect("raidz of two", mkpool(map[string]any{"name": "tank", "raidlevel": "raidz", "devices": []string{"/dev/sdb", "/dev/sdc"}}), 400)
	expect("disk twice", mkpool(map[string]any{"name": "tank", "raidlevel": "mirror", "devices": []string{"/dev/sdb", "/dev/sdb"}}), 400)
	expect("used disk", mkpool(map[string]any{"name": "tank", "raidlevel": "mirror", "devices": []string{"/dev/sda", "/dev/sdb"}}), 409)
	expect("bad ashift", mkpool(map[string]any{"name": "tank", "raidlevel": "mirror", "ashift": 11, "devices": []string{"/dev/sdb", "/dev/sdc"}}), 400)
	expect("create pool", mkpool(map[string]any{"name": "tank", "raidlevel": "mirror", "devices": []string{"/dev/sdb", "/dev/sdc"}, "add_storage": true}), 202)
	pve.mu.Lock()
	z := pve.zfs["tank"]
	pve.mu.Unlock()
	if z.Get("devices") != "/dev/sdb,/dev/sdc" || z.Get("ashift") != "12" || z.Get("compression") != "lz4" || z.Get("add_storage") != "1" {
		t.Errorf("zfs create = %v", z)
	}
	_, _, pools := call(root, "GET", base+"/nodes/pve1/zfs", nil)
	if len(pools) != 1 {
		t.Errorf("pools = %v", pools)
	}
	expect("pool detail", first(call(root, "GET", base+"/nodes/pve1/zfs/tank", nil)), 200)
	expect("destroy unconfirmed", first(call(root, "POST", base+"/nodes/pve1/zfs/tank/destroy", map[string]any{"confirm": "tan"})), 400)
	expect("destroy", first(call(root, "POST", base+"/nodes/pve1/zfs/tank/destroy", map[string]any{"confirm": "tank", "cleanup_config": true})), 202)
}
