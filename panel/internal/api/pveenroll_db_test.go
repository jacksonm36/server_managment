package api

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jacksonm36/servermgr/panel/internal/pki"
)

// TestPVEGuestEnroll enrolls a running Linux VM through its guest agent:
// the installer runs inside the VM, the server is linked to the guest, and
// Windows, containers and VMs already enrolled are refused.
func TestPVEGuestEnroll(t *testing.T) {
	p := newTestPanel(t)
	p.api.Cfg.DataDir = t.TempDir()
	ca, err := pki.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p.api.CA = ca
	call, expect := p.call, p.expect
	pve := newFakePVE(t)
	old := enrollOnlineWait
	enrollOnlineWait = 0
	defer func() { enrollOnlineWait = old }()
	root, _ := p.login("root", rootPW)
	code, body, _ := call(root, "POST", "/api/v1/proxmox", map[string]any{
		"name": "lab", "url": pve.srv.URL, "token_id": fakeRootToken, "token_secret": fakeRootSecret, "fingerprint": pve.FP,
	})
	expect("add cluster", code, 200)
	cid := body["cluster"].(map[string]any)["id"].(string)
	pve.addGuest(400, &fakeGuest{typ: "qemu", node: "pve1", name: "web-1", status: "running", config: map[string]string{"ostype": "l26"}})
	pve.addGuest(401, &fakeGuest{typ: "qemu", node: "pve1", name: "win", status: "running", config: map[string]string{"ostype": "win11"}})
	pve.addGuest(402, &fakeGuest{typ: "lxc", node: "pve1", name: "ct", status: "running"})
	g := "/api/v1/proxmox/" + cid + "/guests/pve1/"

	expect("windows", first(call(root, "POST", g+"qemu/401/enroll", nil)), 400)
	expect("container", first(call(root, "POST", g+"lxc/402/enroll", nil)), 400)

	// The "agent": takes the token off the installer command, as install.sh would.
	done := make(chan struct{})
	go func() {
		defer close(done)
		tok := regexp.MustCompile(`--token (\S+)`)
		for i := 0; i < 200; i++ {
			pve.mu.Lock()
			cmd := pve.guests[400].config["_exec"]
			pve.mu.Unlock()
			if m := tok.FindStringSubmatch(cmd); m != nil {
				if _, _, err := p.api.Store.Enroll(t.Context(), hashToken(m[1]), "web-1"); err != nil {
					t.Error(err)
				}
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Error("the installer never ran")
	}()
	code, out, _ := call(root, "POST", g+"qemu/400/enroll", nil)
	<-done
	expect("enroll", code, 200)
	sid, _ := out["server_id"].(string)
	pve.mu.Lock()
	cmd := pve.guests[400].config["_exec"]
	pve.mu.Unlock()
	if sid == "" || !strings.HasPrefix(cmd, "sh\x00-c\x00") || !strings.Contains(cmd, "/install.sh") {
		t.Fatalf("enroll: %v, command %q", out, cmd)
	}
	_, detail, _ := call(root, "GET", g+"qemu/400", nil)
	if detail["server_id"] != sid || detail["server_name"] != "web-1" {
		t.Errorf("guest detail link: %v %v", detail["server_id"], detail["server_name"])
	}
	expect("enrolled twice", first(call(root, "POST", g+"qemu/400/enroll", nil)), 409)
	expect("enroll a new VM", first(call(root, "POST", "/api/v1/proxmox/"+cid+"/nodes/pve1/guests", map[string]any{"type": "qemu", "params": map[string]string{}, "enroll": true})), 400)
}
