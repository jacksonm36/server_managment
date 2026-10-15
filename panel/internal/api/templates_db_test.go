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

// TestDeployFromCatalog checks flavors and the one-step deploy: it reuses
// a template built from the image on the node, or builds one first.
func TestDeployFromCatalog(t *testing.T) {
	p := newTestPanel(t)
	p.api.Cfg.DataDir = t.TempDir()
	call, expect := p.call, p.expect
	pve := newFakePVE(t)
	root, _ := p.login("root", rootPW)
	code, body, _ := call(root, "POST", "/api/v1/proxmox", map[string]any{
		"name": "lab", "url": pve.srv.URL, "token_id": fakeRootToken, "token_secret": fakeRootSecret, "fingerprint": pve.FP,
	})
	expect("add cluster", code, 200)
	cid := body["cluster"].(map[string]any)["id"].(string)
	sums := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  nothing-here.qcow2\n", strings.Repeat("ab", 64))
	}))
	defer sums.Close()
	for _, id := range []string{"debian-13", "ubuntu-24.04"} {
		img := proxmox.ImageByID(id)
		old := img.Checksums
		img.Checksums = sums.URL
		defer func() { img.Checksums = old }()
	}

	// Flavors: seeded, editable, unique names.
	_, _, flavors := call(root, "GET", "/api/v1/proxmox-flavors", nil)
	if len(flavors) != 4 {
		t.Fatalf("flavors = %v", flavors)
	}
	code, fl, _ := call(root, "POST", "/api/v1/proxmox-flavors", map[string]any{"name": "db", "cores": 6, "memory_mb": 12288, "disk_gb": 80})
	expect("add flavor", code, 200)
	fid := int64(fl["id"].(float64))
	expect("duplicate flavor", first(call(root, "POST", "/api/v1/proxmox-flavors", map[string]any{"name": "db", "cores": 1, "memory_mb": 512})), 409)
	expect("bad flavor", first(call(root, "POST", "/api/v1/proxmox-flavors", map[string]any{"name": "Huge", "cores": 999, "memory_mb": 512})), 400)
	expect("edit flavor", first(call(root, "PUT", fmt.Sprintf("/api/v1/proxmox-flavors/%d", fid), map[string]any{"name": "db", "cores": 6, "memory_mb": 16384, "disk_gb": 80})), 200)

	deploy := func(image, name string) string {
		t.Helper()
		code, run, _ := call(root, "POST", "/api/v1/proxmox/"+cid+"/deploy", map[string]any{
			"image": image, "node": "pve1", "name": name, "flavor_id": fid, "image_storage": "local", "disk_storage": "local-lvm", "bridge": "vmbr0",
		})
		expect("deploy "+name, code, 202)
		var log, status string
		for i := 0; i < 200; i++ {
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
			t.Fatalf("deploy %s %s:\n%s", name, status, log)
		}
		return log
	}
	expect("deploy unknown image", first(call(root, "POST", "/api/v1/proxmox/"+cid+"/deploy", map[string]any{"image": "nope", "node": "pve1", "name": "x", "flavor_id": fid})), 400)
	expect("deploy unknown flavor", first(call(root, "POST", "/api/v1/proxmox/"+cid+"/deploy", map[string]any{"image": "debian-13", "node": "pve1", "name": "x", "flavor_id": 9999})), 400)
	expect("deploy without image storage", first(call(root, "POST", "/api/v1/proxmox/"+cid+"/deploy", map[string]any{
		"image": "debian-13", "node": "pve1", "name": "x", "flavor_id": fid, "disk_storage": "local-lvm", "bridge": "vmbr0"})), 400)

	log := deploy("debian-13", "db-01")
	if !strings.Contains(log, "building one first") {
		t.Errorf("first deploy did not build a template:\n%s", log)
	}
	log = deploy("debian-13", "db-02")
	if !strings.Contains(log, "using template") || strings.Contains(log, "building") {
		t.Errorf("second deploy did not reuse the template:\n%s", log)
	}
	pve.mu.Lock()
	defer pve.mu.Unlock()
	templates, vms := 0, 0
	for _, g := range pve.guests {
		if g.template == 1 {
			templates++
		} else if strings.HasPrefix(g.name, "db-0") {
			vms++
			if g.config["cores"] != "6" || g.config["memory"] != "16384" || g.config["resized"] != "scsi0 80G" || g.config["net0"] != "virtio,bridge=vmbr0" {
				t.Errorf("%s config = %v", g.name, g.config)
			}
		}
	}
	if templates != 1 || vms != 2 {
		t.Errorf("templates = %d, vms = %d", templates, vms)
	}
}
