package api

import (
	"fmt"
	"testing"
)

// TestPVEManage covers guest hardware edits, node and datacenter routes:
// what a platform admin may do, and that a tenant's user stays on their
// own storages and bridges, cannot pass host devices through and never
// reaches nodes or datacenter settings.
func TestPVEManage(t *testing.T) {
	p := newTestPanel(t)
	p.api.Cfg.DataDir = t.TempDir()
	call, login, expect := p.call, p.login, p.expect
	pve := newFakePVE(t)
	root, _ := login("root", rootPW)

	code, body, _ := call(root, "POST", "/api/v1/proxmox", map[string]any{
		"name": "lab", "url": pve.srv.URL, "token_id": fakeRootToken, "token_secret": fakeRootSecret, "fingerprint": pve.FP,
	})
	expect("add cluster", code, 200)
	cid := body["cluster"].(map[string]any)["id"].(string)
	base := "/api/v1/proxmox/" + cid
	pve.addGuest(100, &fakeGuest{typ: "qemu", node: "pve1", name: "web", cores: 2, memMB: 2048, diskGB: 20})
	pve.addGuest(101, &fakeGuest{typ: "qemu", node: "pve1", name: "acme-app", pool: "tenant-acme", cores: 1, memMB: 1024, diskGB: 10})

	g := base + "/guests/pve1/qemu/100"
	set := func(c client, path string, kv map[string]string, del ...string) int {
		body := map[string]any{"set": kv}
		if len(del) > 0 {
			body["delete"] = del
		}
		return first(call(c, "PUT", path+"/config", body))
	}

	// Platform admin.
	expect("set cores and memory", set(root, g, map[string]string{"cores": "4", "memory": "4096"}), 200)
	pve.mu.Lock()
	if pve.guests[100].cores != 4 || pve.guests[100].memMB != 4096 {
		t.Errorf("guest after edit: cores %d mem %d", pve.guests[100].cores, pve.guests[100].memMB)
	}
	pve.mu.Unlock()
	expect("host device for platform", set(root, g, map[string]string{"hostpci0": "0000:01:00.0"}), 200)
	expect("unknown key", set(root, g, map[string]string{"args": "-device foo"}), 400)
	expect("newline in value", set(root, g, map[string]string{"name": "a\nb"}), 400)
	expect("set and delete", set(root, g, map[string]string{"cores": "2"}, "cores"), 400)
	expect("nothing to change", set(root, g, map[string]string{}), 400)
	expect("bad digest", first(call(root, "PUT", g+"/config", map[string]any{"set": map[string]string{"cores": "2"}, "digest": "zz"})), 400)
	expect("bad resize", first(call(root, "POST", g+"/resize", map[string]any{"disk": "scsi0", "size": "lots"})), 400)
	expect("resize unused disk", first(call(root, "POST", g+"/resize", map[string]any{"disk": "unused0", "size": "+1G"})), 400)
	expect("migrate to itself", first(call(root, "POST", g+"/migrate", map[string]any{"target": "pve1"})), 400)
	expect("clone bad name", first(call(root, "POST", g+"/clone", map[string]any{"name": "bad name!"})), 400)
	expect("create without template", first(call(root, "POST", base+"/nodes/pve1/guests", map[string]any{"type": "lxc", "params": map[string]string{"hostname": "ct1"}})), 400)
	expect("create bad key", first(call(root, "POST", base+"/nodes/pve1/guests", map[string]any{"type": "qemu", "params": map[string]string{"args": "x"}})), 400)

	expect("unknown node view", first(call(root, "GET", base+"/nodes/pve1/view/secrets", nil)), 404)
	expect("bad node", first(call(root, "GET", base+"/nodes/-x/view/status", nil)), 400)
	expect("wipe without confirm", first(call(root, "POST", base+"/nodes/pve1/disks/wipedisk", map[string]any{"params": map[string]any{"disk": "/dev/sdb"}})), 400)
	expect("wipe bad path", first(call(root, "POST", base+"/nodes/pve1/disks/wipedisk", map[string]any{"params": map[string]any{"disk": "/etc/passwd"}, "confirm": "/etc/passwd"})), 400)
	expect("power without confirm", first(call(root, "POST", base+"/nodes/pve1/power", map[string]any{"command": "reboot"})), 400)
	expect("bad service action", first(call(root, "POST", base+"/nodes/pve1/services/pveproxy/kill", nil)), 400)
	expect("upload bad name", first(call(root, "POST", base+"/nodes/pve1/storage/local/upload?content=iso&filename=evil.sh", nil)), 400)
	expect("delete volume of another storage", first(call(root, "DELETE", base+"/nodes/pve1/storage/local/content?volid="+"nas:iso/x.iso", nil)), 400)

	expect("unknown dc section", first(call(root, "GET", base+"/dc/secrets", nil)), 404)
	expect("dc users", first(call(root, "GET", base+"/dc/users", nil)), 200)
	expect("dc bad param", first(call(root, "POST", base+"/dc/pools", map[string]any{"params": map[string]any{"Bad Key": "x"}})), 400)
	expect("dc nested param", first(call(root, "POST", base+"/dc/pools", map[string]any{"params": map[string]any{"poolid": map[string]any{}}})), 400)
	expect("delete read-only", first(call(root, "DELETE", base+"/dc/tasks/x", nil)), 400)
	expect("items of flat section", first(call(root, "GET", base+"/dc/pools/x/items", nil)), 404)

	// Tenant: alice in Acme with local-lvm and vmbr1.
	code, acme, _ := call(root, "POST", "/api/v1/tenants", map[string]any{"name": "Acme", "slug": "acme"})
	expect("create tenant", code, 200)
	tid := int64(acme["id"].(float64))
	expect("set up acme", first(call(root, "PUT", fmt.Sprintf("/api/v1/tenants/%d/clusters/%s", tid, cid), map[string]any{"storages": []string{"local-lvm"}, "bridges": []string{"vmbr1"}})), 200)
	expect("create alice", first(call(root, "POST", "/api/v1/users", map[string]any{"username": "alice", "password": "alice-password-1", "role": "admin", "tenant_id": tid})), 201)
	al, _ := login("alice", "alice-password-1")

	ag := base + "/guests/pve1/qemu/101"
	expect("alice sets cores", set(al, ag, map[string]string{"cores": "2"}), 200)
	expect("alice passes a PCI device", set(al, ag, map[string]string{"hostpci0": "0000:01:00.0"}), 400)
	expect("alice removes a host key", set(al, ag, nil, "usb0"), 400)
	expect("alice adds a disk on her storage", set(al, ag, map[string]string{"scsi1": "local-lvm:10"}), 200)
	expect("alice adds a disk elsewhere", set(al, ag, map[string]string{"scsi1": "ceph:10"}), 400)
	expect("alice mounts a host path", set(al, ag, map[string]string{"scsi2": "/dev/sda"}), 400)
	expect("alice uses another bridge", set(al, ag, map[string]string{"net1": "virtio,bridge=vmbr0"}), 400)
	expect("alice edits a guest outside her pool", set(al, g, map[string]string{"cores": "1"}), 404)
	expect("alice moves a disk elsewhere", first(call(al, "POST", ag+"/move-disk", map[string]any{"disk": "scsi0", "storage": "ceph"})), 400)
	expect("alice clones to another storage", first(call(al, "POST", ag+"/clone", map[string]any{"full": true, "storage": "ceph"})), 400)
	expect("alice creates a privileged container", first(call(al, "POST", base+"/nodes/pve1/guests", map[string]any{"type": "lxc", "params": map[string]string{"ostemplate": "local-lvm:vztmpl/x.tar.zst", "unprivileged": "0"}})), 400)
	expect("alice creates on another storage", first(call(al, "POST", base+"/nodes/pve1/guests", map[string]any{"type": "qemu", "params": map[string]string{"scsi0": "ceph:32"}})), 400)

	for _, path := range []string{"/nodes/pve1/view/status", "/nodes/pve1/syslog", "/nodes/pve1/tasks", "/dc/users", "/dc/acl", "/nodes/pve1/storage/local/content"} {
		expect("alice GET "+path, first(call(al, "GET", base+path, nil)), 403)
	}
	expect("alice opens a node shell", first(call(al, "POST", base+"/nodes/pve1/shell", map[string]any{})), 403)
	expect("alice changes ACLs", first(call(al, "POST", base+"/dc/acl", map[string]any{"params": map[string]any{"path": "/", "roles": "Administrator", "users": "sm-acme@pve"}})), 403)

	_, ch, _ := call(al, "GET", base+"/nodes/pve1/choices", nil)
	sts, _ := ch["storages"].([]any)
	for _, s := range sts {
		if n := s.(map[string]any)["storage"]; n != "local-lvm" {
			t.Errorf("alice is offered storage %v", n)
		}
	}
	for _, b := range ch["bridges"].([]any) {
		if b != "vmbr1" {
			t.Errorf("alice is offered bridge %v", b)
		}
	}
}
