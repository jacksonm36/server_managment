package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

// TestTofuProxmox checks Proxmox-bound workspaces: the stacks, the
// workspace link to a cluster, and the per-run proxy that swaps the run's
// token for the cluster's.
func TestTofuProxmox(t *testing.T) {
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
	pve.addGuest(9000, &fakeGuest{typ: "qemu", node: "pve1", name: "debian-13", template: 1, cores: 2, memMB: 2048, diskGB: 10})

	_, _, stacks := call(root, "GET", "/api/v1/proxmox/"+cid+"/tofu-stacks", nil)
	if len(stacks) != 4 {
		t.Fatalf("stacks = %v", stacks)
	}
	for _, x := range stacks {
		main := x.(map[string]any)["files"].(map[string]any)["main.tf"].(string)
		for _, want := range []string{`default = "pve1"`, `default = "ceph"`, `default = "vmbr0"`, "servermgr-ansible", `provider "proxmox" {}`} {
			if !strings.Contains(main, want) {
				t.Errorf("stack %v lacks %s", x.(map[string]any)["id"], want)
			}
		}
		if strings.Contains(main, "__") {
			t.Errorf("stack %v has an unfilled placeholder", x.(map[string]any)["id"])
		}
		if x.(map[string]any)["id"] == "clone-template" && !strings.Contains(main, "default = 9000") {
			t.Errorf("clone stack template default:\n%s", main)
		}
	}

	ws := map[string]any{"name": "pve-vms", "source": "inline", "files": map[string]string{"main.tf": "# x"}, "proxmox_cluster": cid}
	code, saved, _ := call(root, "POST", "/api/v1/tofu/workspaces", ws)
	expect("save workspace", code, 200)
	if saved["workspace"].(map[string]any)["proxmox_cluster"] != cid {
		t.Errorf("workspace = %v", saved["workspace"])
	}
	ws["proxmox_cluster"] = "00000000-0000-4000-8000-000000000000"
	expect("unknown cluster", first(call(root, "POST", "/api/v1/tofu/workspaces", ws)), 400)

	// The proxy: the run's token works, nothing else does, and the
	// cluster's real token never shows up in the environment.
	work := t.TempDir()
	env, token, stop, err := p.api.tofuProxmox(context.Background(), cid, work)
	if err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{}
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		vars[k] = v
	}
	if strings.Contains(strings.Join(env, "\n"), fakeRootSecret) || vars["PROXMOX_VE_API_TOKEN"] != tofuPVEUser+"="+token {
		t.Errorf("env = %v", env)
	}
	pemBytes, err := os.ReadFile(vars["SSL_CERT_FILE"])
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pemBytes)
	cl := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	get := func(path, auth string) (int, string) {
		req, _ := http.NewRequest("GET", strings.TrimSuffix(vars["PROXMOX_VE_ENDPOINT"], "/")+path, nil)
		if auth != "" {
			req.Header.Set("Authorization", "PVEAPIToken="+auth)
		}
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if c, b := get("/api2/json/version", vars["PROXMOX_VE_API_TOKEN"]); c != 200 || !strings.Contains(b, "8.4.1") {
		t.Errorf("proxied version = %d %s", c, b)
	}
	if c, _ := get("/api2/json/version", tofuPVEUser+"=wrong"); c != 401 {
		t.Errorf("wrong token = %d", c)
	}
	if c, _ := get("/api2/json/version", fakeRootToken+"="+fakeRootSecret); c != 401 {
		t.Errorf("the cluster's own token went through the proxy: %d", c)
	}
	if c, _ := get("/other", vars["PROXMOX_VE_API_TOKEN"]); c != 404 {
		t.Errorf("non-API path = %d", c)
	}
	stop()
	if _, err := os.Stat(vars["SSL_CERT_FILE"]); !os.IsNotExist(err) {
		t.Errorf("CA bundle left behind")
	}
}
