package api

import (
	"encoding/base64"
	"strings"
	"testing"
	"unicode/utf16"
)

// decodePS returns the script of a psCommand command line.
func decodePS(t *testing.T, cmd string) string {
	cmd = strings.ReplaceAll(cmd, "\x00", " ") // Proxmox gets the arguments NUL-separated
	i := strings.Index(cmd, "-EncodedCommand ")
	if i < 0 {
		t.Fatalf("no encoded command in %q", cmd)
	}
	b, err := base64.StdEncoding.DecodeString(strings.Fields(cmd[i:])[1])
	if err != nil {
		t.Fatal(err)
	}
	u := make([]uint16, len(b)/2)
	for j := range u {
		u[j] = uint16(b[2*j]) | uint16(b[2*j+1])<<8
	}
	return string(utf16.Decode(u))
}

func TestPSDataCannotEscape(t *testing.T) {
	evil := map[string]any{"name": "x'); Remove-Item C:\\ -Recurse; ('"}
	got := psData(evil)
	if strings.Contains(got, "Remove-Item") || strings.Count(got, "'") != 2 {
		t.Errorf("psData leaks text into code: %s", got)
	}
	if s := decodePS(t, strings.Join(psCommand("Write-Output 'é'"), " ")); !strings.HasSuffix(s, "\nWrite-Output 'é'") {
		t.Errorf("psCommand round trip: %q", s)
	}
	if !naturalAfter("vlc-3.0.10-win64.msi", "vlc-3.0.9-win64.msi") || naturalAfter("vlc-3.0.9-win64.msi", "vlc-3.0.10-win64.msi") {
		t.Error("naturalAfter")
	}
}

// TestPVEWindowsManage covers accounts and apps through the (fake) guest agent.
func TestPVEWindowsManage(t *testing.T) {
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
	pve.addGuest(100, &fakeGuest{typ: "qemu", node: "pve1", name: "win", status: "running", config: map[string]string{"ostype": "win11", "_applog": "12:00:00 Firefox: installed",
		"_exec_out": `[{"name":"Administrator","full_name":"","enabled":true,"admin":true,"locked":false,"builtin":true}]`}})
	pve.addGuest(101, &fakeGuest{typ: "qemu", node: "pve1", name: "linux", status: "running", config: map[string]string{"ostype": "l26"}})
	g := "/api/v1/proxmox/" + cid + "/guests/pve1/qemu/100/windows"

	_, _, users := call(root, "GET", g+"/users", nil)
	if len(users) != 1 || users[0].(map[string]any)["name"] != "Administrator" {
		t.Errorf("users = %v", users)
	}
	expect("linux VM", first(call(root, "GET", "/api/v1/proxmox/"+cid+"/guests/pve1/qemu/101/windows/users", nil)), 400)
	expect("bad name", first(call(root, "POST", g+"/users", map[string]any{"name": "a/b", "password": "Correct-Horse-9x"})), 400)
	expect("weak password", first(call(root, "POST", g+"/users", map[string]any{"name": "alice", "password": "short"})), 400)
	expect("create", first(call(root, "POST", g+"/users", map[string]any{"name": "alice", "full_name": "Alice O'Hara", "admin": true, "password": "Correct-Horse-9x"})), 204)
	pve.mu.Lock()
	script := decodePS(t, pve.guests[100].config["_exec"])
	pw := pve.guests[100].config["_password_of"]
	pve.mu.Unlock()
	if strings.Contains(script, "Correct-Horse") || strings.Contains(script, "O'Hara") || pw != "alice" {
		t.Errorf("password or raw input in the script, or password not set (%q)", pw)
	}
	expect("update", first(call(root, "PUT", g+"/users/alice", map[string]any{"enabled": false, "unlock": true})), 204)
	expect("delete", first(call(root, "DELETE", g+"/users/alice", nil)), 204)

	expect("unknown app", first(call(root, "POST", g+"/apps", map[string]any{"apps": []string{"evil"}})), 400)
	expect("no apps", first(call(root, "POST", g+"/apps", map[string]any{"apps": []string{}})), 400)
	_, st, _ := call(root, "GET", g+"/apps/status?pid=42", nil)
	if st["done"] != true || len(st["log"].([]any)) != 1 {
		t.Errorf("status = %v", st)
	}
}
