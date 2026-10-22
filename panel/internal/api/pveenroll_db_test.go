package api

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/jacksonm36/servermgr/panel/internal/pki"
)

// TestPVEGuestEnroll enrolls running Linux and Windows VMs through their
// guest agents: the installer runs inside the VM with the join token kept
// off its command line, the server is linked to the guest, and containers
// and VMs already enrolled are refused.
func TestPVEGuestEnroll(t *testing.T) {
	p := newTestPanel(t)
	p.api.Cfg.DataDir = t.TempDir()
	ca, err := pki.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p.api.CA = ca
	p.api.Cfg.PublicURL = "https://panel.test:8443"
	p.api.Cfg.AgentBinDir = t.TempDir()
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

	expect("container", first(call(root, "POST", g+"lxc/402/enroll", nil)), 400)

	// The "agent": takes the token from where the installer gets it (Linux:
	// its stdin; Windows: the token file), as the real one would.
	agent := func(vmid int, key, name string) chan struct{} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			for i := 0; i < 200; i++ {
				pve.mu.Lock()
				tok := strings.TrimSpace(pve.guests[vmid].config[key])
				pve.mu.Unlock()
				if tok != "" {
					if _, _, err := p.api.Store.Enroll(t.Context(), hashToken(tok), name); err != nil {
						t.Error(err)
					}
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Error("the installer never ran")
		}()
		return done
	}
	done := agent(400, "_input", "web-1")
	code, out, _ := call(root, "POST", g+"qemu/400/enroll", nil)
	<-done
	expect("enroll", code, 200)
	sid, _ := out["server_id"].(string)
	pve.mu.Lock()
	cmd, input := pve.guests[400].config["_exec"], strings.TrimSpace(pve.guests[400].config["_input"])
	pve.mu.Unlock()
	if sid == "" || !strings.HasPrefix(cmd, "sh\x00-c\x00IFS= read -r SM_JOIN_TOKEN") || !strings.Contains(cmd, "/install.sh") ||
		input == "" || strings.Contains(cmd, input) || strings.Contains(cmd, "--token") {
		t.Fatalf("enroll: %v, command %q", out, cmd)
	}

	// Windows: no agent build on the panel yet, then the real thing.
	expect("windows without a build", first(call(root, "POST", g+"qemu/401/enroll", nil)), 502)
	if err := os.WriteFile(filepath.Join(p.api.Cfg.AgentBinDir, "servermgr-agent-windows-amd64.exe"), []byte("MZ agent"), 0o644); err != nil {
		t.Fatal(err)
	}
	done = agent(401, "_wrote_content", "win")
	code, out, _ = call(root, "POST", g+"qemu/401/enroll", nil)
	<-done
	expect("enroll windows", code, 200)
	pve.mu.Lock()
	wcmd, wrote, tok := pve.guests[401].config["_exec"], pve.guests[401].config["_wrote"], pve.guests[401].config["_wrote_content"]
	pve.mu.Unlock()
	args := strings.Split(wcmd, "\x00")
	b, _ := base64.StdEncoding.DecodeString(args[len(args)-1])
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	script := string(utf16.Decode(u))
	if out["server_id"] == "" || wrote != winEnrollDir+`\join-token;` || tok == "" || strings.Contains(wcmd, tok) || strings.Contains(script, tok) ||
		!strings.Contains(script, "Get-FileHash") || !strings.Contains(script, "ServermgrPin") || strings.Contains(script, "{ $true }") {
		t.Fatalf("windows enroll: %v, wrote %q, script %q", out, wrote, script)
	}

	_, detail, _ := call(root, "GET", g+"qemu/400", nil)
	if detail["server_id"] != sid || detail["server_name"] != "web-1" {
		t.Errorf("guest detail link: %v %v", detail["server_id"], detail["server_name"])
	}
	expect("enrolled twice", first(call(root, "POST", g+"qemu/400/enroll", nil)), 409)
	expect("enroll a new VM", first(call(root, "POST", "/api/v1/proxmox/"+cid+"/nodes/pve1/guests", map[string]any{"type": "qemu", "params": map[string]string{}, "enroll": true})), 400)
}
