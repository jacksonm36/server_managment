package api

import (
	"bytes"
	"encoding/xml"
	"io"
	"strings"
	"testing"
	"time"
)

// TestWindowsTemplates builds Standard and Hardened templates unattended,
// then deploys one the Windows way.
func TestWindowsTemplates(t *testing.T) {
	p := newTestPanel(t)
	p.api.Cfg.DataDir = t.TempDir()
	call, expect := p.call, p.expect
	pve := newFakePVE(t)
	old := winPoll
	winPoll = 10 * time.Millisecond
	defer func() { winPoll = old }()
	root, _ := p.login("root", rootPW)
	code, body, _ := call(root, "POST", "/api/v1/proxmox", map[string]any{
		"name": "lab", "url": pve.srv.URL, "token_id": fakeRootToken, "token_secret": fakeRootSecret, "fingerprint": pve.FP,
	})
	expect("add cluster", code, 200)
	cid := body["cluster"].(map[string]any)["id"].(string)

	req := map[string]any{"edition": "ws2022", "node": "pve1", "iso": "local:iso/win-server-2022.iso", "drivers_iso": "local:iso/virtio-win.iso",
		"disk_storage": "local-lvm", "bridge": "vmbr0", "profiles": []string{"standard", "hardened"}, "locale": "en-US", "keyboard": "hu-HU", "timezone": "Central Europe Standard Time"}
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for a, b := range req {
			m[a] = b
		}
		m[k] = v
		return m
	}
	build := "/api/v1/proxmox/" + cid + "/windows-templates"
	expect("unknown edition", first(call(root, "POST", build, with("edition", "win95"))), 400)
	expect("missing ISO", first(call(root, "POST", build, with("iso", "local:iso/nope.iso"))), 400)
	expect("same ISO twice", first(call(root, "POST", build, with("drivers_iso", "local:iso/win-server-2022.iso"))), 400)
	expect("bad profile", first(call(root, "POST", build, with("profiles", []string{"open"}))), 400)
	expect("bad key", first(call(root, "POST", build, with("product_key", "abc"))), 400)
	expect("bad time zone", first(call(root, "POST", build, with("timezone", "<x>"))), 400)
	code, _, runs := call(root, "POST", build, req)
	expect("build", code, 202)
	if len(runs) != 2 {
		t.Fatalf("runs = %v", runs)
	}
	for _, x := range runs {
		waitProvision(t, p, root, x.(map[string]any)["id"].(string))
	}

	pve.mu.Lock()
	tpls := map[string]int{}
	for vmid, g := range pve.guests {
		tpls[g.name] = vmid
		if g.template != 1 || g.config["bios"] != "ovmf" || g.config["ostype"] != "win11" || g.config["citype"] != "configdrive2" ||
			g.config["ide2"] != "local-lvm:cloudinit" || g.config["ide0"] != "" || g.config["ide3"] != "" ||
			!strings.Contains(g.config["tpmstate0"], "v2.0") || !strings.Contains(g.config["scsi0"], "discard=on") ||
			!strings.Contains(g.config["tags"], "windows") || !strings.Contains(g.config["tags"], "win-"+strings.TrimPrefix(g.name, "ws2022-")) {
			t.Errorf("template %d = %+v", vmid, g.config)
		}
	}
	if len(pve.uploads) != 0 {
		t.Errorf("answer CDs left behind: %v", len(pve.uploads))
	}
	if pve.sendkeys == 0 {
		t.Error("no key pressed at the CD prompt")
	}
	pve.mu.Unlock()
	if tpls["ws2022-standard"] == 0 || tpls["ws2022-hardened"] == 0 {
		t.Fatalf("templates = %v", tpls)
	}
	expect("build again with the same names", first(call(root, "POST", build, req)), 409)

	// The answer CD: the edition's drivers, the profile, no stray template markers.
	iso, err := answerISO(winEditionByID("ws2022"), pveWinBuildReq{Locale: "en-US", Keyboard: "hu-HU", TimeZone: "UTC", ImageIndex: 2}, "hardened", "Pw-123456789abc")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`E:\vioscsi\2k22\amd64`, "-Profile Hardened -Build", "<Value>2</Value>", "<InputLocale>hu-HU</InputLocale>", "Set-NetFirewallProfile"} {
		if !bytes.Contains(iso, []byte(want)) {
			t.Errorf("answer CD lacks %q", want)
		}
	}
	if bytes.Contains(iso, []byte("{{")) {
		t.Error("unrendered template in the answer CD")
	}

	// Deploy: Windows rules, Cloudbase-Init config, no SSH keys.
	prov := func(b map[string]any) (int, map[string]any) {
		m := map[string]any{"template_vmid": tpls["ws2022-hardened"], "node": "pve1", "name": "app-win", "ip": "10.0.0.60/24", "gateway": "10.0.0.1", "cipassword": "Correct-Horse-9"}
		for k, v := range b {
			m[k] = v
		}
		code, out, _ := call(root, "POST", "/api/v1/proxmox/"+cid+"/provision", m)
		return code, out
	}
	expect("long name", first2(prov(map[string]any{"name": "a-very-long-windows-name"})), 400)
	expect("no password", first2(prov(map[string]any{"cipassword": ""})), 400)
	expect("weak password", first2(prov(map[string]any{"cipassword": "alllowercase12"})), 400)
	expect("ssh keys", first2(prov(map[string]any{"sshkeys": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGa4u5Lq8gq7d3X3z9q9n6wq8ZVb7kzQwq9sZ8m3Jv1x a@b"})), 400)
	code, run := prov(nil)
	expect("deploy windows", code, 202)
	waitProvision(t, p, root, run["id"].(string))
	pve.mu.Lock()
	for _, g := range pve.guests {
		if g.name == "app-win" {
			if g.config["citype"] != "configdrive2" || g.config["ciuser"] != "Administrator" || g.config["cipassword"] != "Correct-Horse-9" ||
				g.config["sshkeys"] != "" || g.config["ipconfig0"] != "ip=10.0.0.60/24,gw=10.0.0.1" {
				t.Errorf("deployed config = %v", g.config)
			}
		}
	}
	pve.mu.Unlock()

	// An existing VM: script CD in, then template.
	pve.addGuest(300, &fakeGuest{typ: "qemu", node: "pve1", name: "my-win", config: map[string]string{"ostype": "win10", "scsi0": "ceph:vm-300-disk-0,size=60G", "ide2": "local:iso/win10.iso,media=cdrom"}})
	pve.addGuest(301, &fakeGuest{typ: "qemu", node: "pve1", name: "linux", config: map[string]string{"ostype": "l26"}})
	g300 := "/api/v1/proxmox/" + cid + "/guests/pve1/qemu/300"
	expect("prep a Linux VM", first(call(root, "POST", "/api/v1/proxmox/"+cid+"/guests/pve1/qemu/301/windows-prep", map[string]any{"storage": "local"})), 400)
	code, out, _ := call(root, "POST", g300+"/windows-prep", map[string]any{"storage": "local"})
	expect("prep", code, 200)
	if out["slot"] != "ide2" || out["pending"] != false {
		t.Errorf("prep = %v", out)
	}
	pve.mu.Lock()
	if pve.guests[300].config["ide2"] != "local:iso/servermgr-prepare-300.iso,media=cdrom" || pve.uploads["local:iso/servermgr-prepare-300.iso"] == nil {
		t.Errorf("prep CD = %v", pve.guests[300].config)
	}
	pve.guests[300].status = "running"
	pve.mu.Unlock()
	expect("template while running", first(call(root, "POST", g300+"/windows-template", map[string]any{"confirm": "300"})), 409)
	pve.mu.Lock()
	pve.guests[300].status = "stopped"
	pve.mu.Unlock()
	expect("unconfirmed", first(call(root, "POST", g300+"/windows-template", map[string]any{"confirm": "3"})), 400)
	expect("template", first(call(root, "POST", g300+"/windows-template", map[string]any{"confirm": "300", "profile": "standard"})), 200)
	pve.mu.Lock()
	c := pve.guests[300].config
	if pve.guests[300].template != 1 || c["ide2"] != "ceph:cloudinit" || c["citype"] != "configdrive2" || !strings.Contains(c["tags"], "win-standard") || len(pve.uploads) != 0 {
		t.Errorf("template 300 = %v (uploads %d)", c, len(pve.uploads))
	}
	pve.mu.Unlock()

	// Resetting Administrator on a deployed Windows VM, through the agent.
	pve.addGuest(302, &fakeGuest{typ: "qemu", node: "pve1", name: "win-vm", config: map[string]string{"ostype": "win10"}})
	g302 := "/api/v1/proxmox/" + cid + "/guests/pve1/qemu/302/windows-admin"
	expect("reset: weak password", first(call(root, "POST", g302, map[string]any{"password": "short"})), 400)
	expect("reset: Linux VM", first(call(root, "POST", "/api/v1/proxmox/"+cid+"/guests/pve1/qemu/301/windows-admin", map[string]any{"password": "Str0ng-Passw0rd!"})), 400)
	expect("reset: stopped VM", first(call(root, "POST", g302, map[string]any{"password": "Str0ng-Passw0rd!"})), 502)
	pve.mu.Lock()
	pve.guests[302].status = "running"
	pve.mu.Unlock()
	expect("reset", first(call(root, "POST", g302, map[string]any{"password": "Str0ng-Passw0rd!"})), 204)
	pve.mu.Lock()
	c = pve.guests[302].config
	if !strings.Contains(c["_exec"], "Enable-LocalUser") || strings.Contains(c["_exec"], "Str0ng") || c["_password"] != "Administrator:Str0ng-Passw0rd!" {
		t.Errorf("reset: exec %q, password %q", c["_exec"], c["_password"])
	}
	pve.mu.Unlock()

	code, _, _ = call(root, "GET", "/api/v1/windows/prepare.ps1", nil)
	expect("script", code, 200)
}

// TestWinSetupKey: Setup only ever gets the image's own generic key, so an
// edition mismatch cannot stop it; the admin's key goes on the CD.
func TestWinSetupKey(t *testing.T) {
	w10 := winEditionByID("win10")
	retail := "local:iso/Win10_22H2_Hungarian_x64v1.iso"
	eval := "local:iso/19045.2006.220908-0225.22h2_release_svc_refresh_CLIENTENTERPRISEEVAL_OEMRET_x64FRE_en-us.iso"
	for _, c := range []struct {
		iso, key   string
		index      int
		setup, own string
	}{
		{retail, "", 6, winGenericKeys[6], ""},
		{retail, winGenericKeys[6], 1, winGenericKeys[1], ""},
		{retail, "ABCDE-ABCDE-ABCDE-ABCDE-ABCDE", 1, winGenericKeys[1], "ABCDE-ABCDE-ABCDE-ABCDE-ABCDE"},
		{eval, "", 1, "", ""},
		{retail, "ABCDE-ABCDE-ABCDE-ABCDE-ABCDE", 20, "ABCDE-ABCDE-ABCDE-ABCDE-ABCDE", ""},
	} {
		s, o := winSetupKey(w10, pveWinBuildReq{ISO: c.iso, ProductKey: c.key, ImageIndex: c.index})
		if s != c.setup || o != c.own {
			t.Errorf("%+v: setup %q own %q", c, s, o)
		}
	}
	iso, err := answerISO(w10, pveWinBuildReq{ISO: retail, Locale: "hu-HU", Keyboard: "hu-HU", TimeZone: "UTC", ImageIndex: 6, ProductKey: "ABCDE-ABCDE-ABCDE-ABCDE-ABCDE"}, "standard", "Pw-123456789abc")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(iso, []byte("<Key>"+winGenericKeys[6]+"</Key>")) || !bytes.Contains(iso, []byte("SMKEY.TXT")) {
		t.Error("answer CD: generic key in the XML, own key in a file")
	}
	r := pveWinBuildReq{Node: "pve1", ISO: eval, DriversISO: "local:iso/virtio-win.iso", DiskStorage: "local-lvm", Bridge: "vmbr0", Profiles: []string{"standard"}, ProductKey: "ABCDE-ABCDE-ABCDE-ABCDE-ABCDE"}
	if msg := r.check(w10); !strings.Contains(msg, "evaluation") {
		t.Errorf("key with an evaluation ISO: %q", msg)
	}
}

func TestWinPassword(t *testing.T) {
	for pw, ok := range map[string]bool{"Correct-Horse-9": true, "short1A!": false, "alllowercase12": false, "ALLUPPER-1234": true, "Administrator-12": false} {
		if (winPasswordProblem(pw) == "") != ok {
			t.Errorf("%q: %q", pw, winPasswordProblem(pw))
		}
	}
	for i := 0; i < 50; i++ {
		if pw := winBuildPassword(); winPasswordProblem(pw) != "" {
			t.Fatalf("%q: %s", pw, winPasswordProblem(pw))
		}
	}
}

func TestWinUnattendXML(t *testing.T) {
	var x bytes.Buffer
	d := winUnattendData{Locale: "en-US", Keyboard: "hu-HU", TimeZone: "UTC", ProductKey: "AAAAA-BBBBB-CCCCC-DDDDD-EEEEE", Password: xmlText("a<b&c"), Profile: "Standard", Index: 2, DriverPaths: []string{`D:\vioscsi\w11\amd64`}}
	if err := winUnattend.Execute(&x, d); err != nil {
		t.Fatal(err)
	}
	dec := xml.NewDecoder(&x)
	for {
		if _, err := dec.Token(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("autounattend.xml: %v", err)
		}
	}
}
