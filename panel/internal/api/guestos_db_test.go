package api

import (
	"strings"
	"testing"
)

// TestGuestOS covers where the compute tree's OS answers come from and
// that a guest's own claims are cleaned up before they reach anyone.
func TestGuestOS(t *testing.T) {
	p := newTestPanel(t)
	call, expect := p.call, p.expect
	pve := newFakePVE(t)
	root, _ := p.login("root", rootPW)
	code, body, _ := call(root, "POST", "/api/v1/proxmox", map[string]any{
		"name": "lab", "url": pve.srv.URL, "token_id": fakeRootToken, "token_secret": fakeRootSecret, "fingerprint": pve.FP,
	})
	expect("add cluster", code, 200)
	cid := body["cluster"].(map[string]any)["id"].(string)

	const uuidA = "aaaaaaaa-1111-2222-3333-444444444444"
	pve.addGuest(101, &fakeGuest{typ: "qemu", node: "pve1", name: "files", status: "running", config: map[string]string{
		"ostype": "l26", "agent": "1", "smbios1": "uuid=" + uuidA,
		"_osinfo": "ubuntu|Ubuntu 24.04 LTS\x1b[31m\u0000 <b>x</b>", "_hostname": "nextcloud-01",
	}})
	pve.addGuest(102, &fakeGuest{typ: "qemu", node: "pve1", name: "opnsense26.7", status: "running", config: map[string]string{
		"ostype": "other", "agent": "1", "_osinfo": "../../etc/passwd|evil",
	}})
	pve.addGuest(103, &fakeGuest{typ: "qemu", node: "pve1", name: "new-box", config: map[string]string{
		"ostype": "l26", "ide2": "local:iso/debian-13.1.0-amd64-netinst.iso,media=cdrom", "description": "Runs <Technitium> DNS",
	}})
	pve.addGuest(200, &fakeGuest{typ: "lxc", node: "pve1", name: "dns", config: map[string]string{"ostype": "alpine"}})

	get := func() map[string]any {
		code, body, _ := call(root, "GET", "/api/v1/proxmox/"+cid+"/guest-os?refresh=1", nil)
		expect("guest-os", code, 200)
		return body
	}
	field := func(b map[string]any, k, f string) string {
		g, _ := b[k].(map[string]any)
		s, _ := g[f].(string)
		return s
	}
	b := get()
	if field(b, "pve1/101", "os") != "ubuntu" || field(b, "pve1/101", "os_via") != "guest agent" {
		t.Fatalf("agent answer: %v", b["pve1/101"])
	}
	if pr := field(b, "pve1/101", "pretty"); strings.ContainsAny(pr, "\x1b\x00") || !strings.HasPrefix(pr, "Ubuntu 24.04 LTS") {
		t.Fatalf("pretty not cleaned: %q", pr)
	}
	if !strings.Contains(field(b, "pve1/101", "hints"), "nextcloud-01") {
		t.Fatalf("agent hostname missing from hints: %v", b["pve1/101"])
	}
	// A bogus id from the guest is dropped; the name says OPNsense.
	if field(b, "pve1/102", "os") != "freebsd" || field(b, "pve1/102", "os_via") != "ISO, notes or name" {
		t.Fatalf("bad agent id: %v", b["pve1/102"])
	}
	if field(b, "pve1/103", "os") != "debian" || !strings.Contains(field(b, "pve1/103", "hints"), "technitium") || strings.Contains(field(b, "pve1/103", "hints"), "<") {
		t.Fatalf("ISO/notes guess: %v", b["pve1/103"])
	}
	if field(b, "pve1/200", "os") != "alpine" || field(b, "pve1/200", "os_via") != "container config" {
		t.Fatalf("container: %v", b["pve1/200"])
	}

	// Stopped: the last agent answer is kept…
	pve.mu.Lock()
	pve.guests[101].status = "stopped"
	pve.mu.Unlock()
	b = get()
	if field(b, "pve1/101", "os") != "ubuntu" || !strings.HasPrefix(field(b, "pve1/101", "os_via"), "guest agent, last seen ") {
		t.Fatalf("stored answer: %v", b["pve1/101"])
	}
	// …but not for a different VM that reuses the VMID.
	pve.mu.Lock()
	pve.guests[101].config["smbios1"] = "uuid=bbbbbbbb-1111-2222-3333-444444444444"
	pve.mu.Unlock()
	b = get()
	if strings.HasPrefix(field(b, "pve1/101", "os_via"), "guest agent") {
		t.Fatalf("new VM inherited the old answer: %v", b["pve1/101"])
	}
}

func TestGuestOSHelpers(t *testing.T) {
	for in, want := range map[string]string{"Debian": "debian", "mswindows": "windows", "../x": "", "a b": "", strings.Repeat("a", 40): ""} {
		if got := cleanID(in); got != want {
			t.Errorf("cleanID(%q) = %q, want %q", in, got, want)
		}
	}
	if got := cleanText("  a\x1b[0m\tb‮ c ", 80); got != "a[0m b c" {
		t.Errorf("cleanText = %q", got)
	}
	if got := cleanText(strings.Repeat("é", 100), 5); got != "ééééé" {
		t.Errorf("cleanText cap = %q", got)
	}
	if got := hintWords("Home-Assistant <script>", "OPNsense_26.7.iso"); got != "home-assistant script opnsense 26.7.iso" {
		t.Errorf("hintWords = %q", got)
	}
	for in, want := range map[string]string{
		"truenas-scale-24.10.iso": "debian", "truenas core": "freebsd", "proxmox-ve_9.0-1.iso": "debian",
		"windows server 2022": "windows", "win11": "windows", "rocky-9.5": "rocky", "plain": "",
	} {
		if got := osFromHints(in); got != want {
			t.Errorf("osFromHints(%q) = %q, want %q", in, got, want)
		}
	}
}
