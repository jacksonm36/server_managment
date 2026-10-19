package api

import "testing"

// TestPVEGuestPassword sets a password through the guest agent: VMs only,
// checked user names and passwords, and the password never in the audit log.
func TestPVEGuestPassword(t *testing.T) {
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
	pve.addGuest(100, &fakeGuest{typ: "qemu", node: "pve1", name: "win", status: "running"})
	pve.addGuest(101, &fakeGuest{typ: "lxc", node: "pve1", name: "ct", status: "running"})
	vm := "/api/v1/proxmox/" + cid + "/guests/pve1/qemu/100/password"

	expect("set", first(call(root, "POST", vm, map[string]any{"username": "Administrator", "password": "Correct-Horse-9"})), 204)
	pve.mu.Lock()
	if pve.guests[100].config["_password_of"] != "Administrator" {
		t.Error("agent/set-user-password was not called")
	}
	pve.mu.Unlock()
	expect("short", first(call(root, "POST", vm, map[string]any{"username": "Administrator", "password": "short"})), 400)
	expect("bad user", first(call(root, "POST", vm, map[string]any{"username": "a;rm -rf", "password": "Correct-Horse-9"})), 400)
	expect("container", first(call(root, "POST", "/api/v1/proxmox/"+cid+"/guests/pve1/lxc/101/password", map[string]any{"username": "root", "password": "Correct-Horse-9"})), 400)

	_, _, log := call(root, "GET", "/api/v1/audit?limit=20", nil)
	for _, e := range log {
		if d, _ := e.(map[string]any)["detail"].(map[string]any); d != nil && d["password"] != nil {
			t.Errorf("password in the audit log: %v", e)
		}
	}
}
