package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jacksonm36/servermgr/panel/internal/proxmox"
)

// TestTemplateFromImage builds a template from a catalog image against the
// fake Proxmox: checksum, download into import storage, VM, template.
func TestTemplateFromImage(t *testing.T) {
	p := newTestPanel(t)
	call, expect := p.call, p.expect
	pve := newFakePVE(t)
	root, _ := p.login("root", rootPW)
	code, body, _ := call(root, "POST", "/api/v1/proxmox", map[string]any{
		"name": "lab", "url": pve.srv.URL, "token_id": fakeRootToken, "token_secret": fakeRootSecret, "fingerprint": pve.FP,
	})
	expect("add cluster", code, 200)
	cid := body["cluster"].(map[string]any)["id"].(string)

	sum := strings.Repeat("ab", 64)
	sums := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  debian-13-genericcloud-amd64.qcow2\n", sum)
	}))
	defer sums.Close()
	img := proxmox.ImageByID("debian-13")
	old := img.Checksums
	img.Checksums = sums.URL
	defer func() { img.Checksums = old }()

	_, _, images := call(root, "GET", "/api/v1/proxmox-images", nil)
	if len(images) < 5 {
		t.Fatalf("images = %v", images)
	}
	_, tg, _ := call(root, "GET", "/api/v1/proxmox/"+cid+"/template-targets?node=pve1", nil)
	if fmt.Sprint(tg["nodes"], tg["image_storages"], tg["file_storages"], tg["disk_storages"]) != "[pve1 pve2] [local] [nas] [ceph local-lvm]" {
		t.Errorf("targets = %v", tg)
	}
	req := map[string]any{"image": "debian-13", "node": "pve1", "name": "debian-13-tpl", "image_storage": "local", "disk_storage": "local-lvm", "bridge": "vmbr0", "cores": 2, "memory_mb": 2048, "disk_gb": 20}
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for a, b := range req {
			m[a] = b
		}
		m[k] = v
		return m
	}
	expect("unknown image", first(call(root, "POST", "/api/v1/proxmox/"+cid+"/templates", with("image", "windows-xp"))), 400)
	expect("offline node", first(call(root, "POST", "/api/v1/proxmox/"+cid+"/templates", with("node", "pve3"))), 400)
	_, e, _ := call(root, "POST", "/api/v1/proxmox/"+cid+"/templates", with("image_storage", "nas"))
	if !strings.Contains(fmt.Sprint(e["error"]), "Import") {
		t.Errorf("storage without import content: %v", e)
	}
	expect("disk on an image-only storage", first(call(root, "POST", "/api/v1/proxmox/"+cid+"/templates", with("disk_storage", "local"))), 400)
	code, run, _ := call(root, "POST", "/api/v1/proxmox/"+cid+"/templates", req)
	expect("build template", code, 202)

	var log, status string
	for i := 0; i < 100; i++ {
		_, _, provs := call(root, "GET", "/api/v1/proxmox/provisions", nil)
		for _, x := range provs {
			if m := x.(map[string]any); m["id"] == run["id"] {
				log, status = fmt.Sprint(m["log"]), fmt.Sprint(m["status"])
			}
		}
		if status != "running" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if status != "succeeded" {
		t.Fatalf("build %s:\n%s", status, log)
	}
	pve.mu.Lock()
	defer pve.mu.Unlock()
	dl := pve.imports["local:import/servermgr-debian-13.qcow2"]
	if dl == nil || dl.Get("checksum") != sum || dl.Get("checksum-algorithm") != "sha512" || dl.Get("url") != img.URL {
		t.Errorf("download = %v", dl)
	}
	var tpl *fakeGuest
	for _, g := range pve.guests {
		if g.name == "debian-13-tpl" {
			tpl = g
		}
	}
	if tpl == nil || tpl.template != 1 || !strings.HasPrefix(tpl.config["scsi0"], "local-lvm:0,import-from=local:import/servermgr-debian-13.qcow2") ||
		tpl.config["ide2"] != "local-lvm:cloudinit" || tpl.config["ciuser"] != "debian" || tpl.config["vga"] != "serial0" {
		t.Errorf("template = %+v", tpl)
	}
}
