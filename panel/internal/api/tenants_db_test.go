package api

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestTenantIsolation sets up two tenants on one (fake) Proxmox cluster
// and checks, route by route, that neither reaches the other: through the
// panel, and with the tenant's own Proxmox token, bypassing the panel.
func TestTenantIsolation(t *testing.T) {
	p := newTestPanel(t)
	p.api.Cfg.DataDir = t.TempDir()
	call, login, expect := p.call, p.login, p.expect
	id := func(m map[string]any) int64 { return int64(m["id"].(float64)) }
	pve := newFakePVE(t)
	root, _ := login("root", rootPW)

	code, body, _ := call(root, "POST", "/api/v1/proxmox", map[string]any{
		"name": "lab", "url": pve.srv.URL, "token_id": fakeRootToken, "token_secret": fakeRootSecret, "fingerprint": pve.FP,
	})
	expect("add cluster", code, 200)
	cid := body["cluster"].(map[string]any)["id"].(string)

	code, sg, _ := call(root, "POST", "/api/v1/server-groups", map[string]any{"name": "Acme servers", "server_ids": []string{p.web}})
	expect("server group", code, 200)
	code, acme, _ := call(root, "POST", "/api/v1/tenants", map[string]any{"name": "Acme", "slug": "acme", "server_group_id": id(sg), "max_guests": 2, "max_cores": 6})
	expect("create tenant acme", code, 200)
	code, globex, _ := call(root, "POST", "/api/v1/tenants", map[string]any{"name": "Globex", "slug": "globex"})
	expect("create tenant globex", code, 200)
	expect("bad slug", first(call(root, "POST", "/api/v1/tenants", map[string]any{"name": "X", "slug": "Bad Slug"})), 400)

	// Guests: one per tenant pool, one outside any pool, a shared template.
	pve.addGuest(100, &fakeGuest{typ: "qemu", node: "pve1", name: "acme-web", pool: "tenant-acme", cores: 2, memMB: 2048, diskGB: 20, status: "running"})
	pve.addGuest(101, &fakeGuest{typ: "qemu", node: "pve1", name: "globex-db", pool: "tenant-globex", cores: 2, memMB: 4096, diskGB: 50})
	pve.addGuest(102, &fakeGuest{typ: "lxc", node: "pve2", name: "infra-dns", cores: 1, memMB: 512, diskGB: 8})
	pve.addGuest(9000, &fakeGuest{typ: "qemu", node: "pve1", name: "debian-13", template: 1, cores: 2, memMB: 2048, diskGB: 10})
	pve.addGuest(9001, &fakeGuest{typ: "qemu", node: "pve1", name: "windows", template: 1, cores: 4, memMB: 8192, diskGB: 60})

	setup := func(tenant map[string]any, body map[string]any) (int, map[string]any) {
		c, b, _ := call(root, "PUT", fmt.Sprintf("/api/v1/tenants/%d/clusters/%s", id(tenant), cid), body)
		return c, b
	}
	code, out := setup(acme, map[string]any{"storages": []string{"local-lvm"}, "bridges": []string{"vmbr1"}, "templates": []int{9000}})
	expect("set up acme", code, 200)
	if code != 200 {
		t.Fatalf("setup: %v", out)
	}
	code, _ = setup(globex, map[string]any{"storages": []string{"ceph"}, "bridges": []string{"vmbr0"}, "templates": []int{9000, 9001}})
	expect("set up globex", code, 200)
	code, _ = setup(globex, map[string]any{"pool": "tenant-acme"})
	expect("pool already used by another tenant", code, 409)

	// Proxmox side: a user per tenant with exactly the expected ACLs.
	pve.mu.Lock()
	var acmeACL []string
	for _, a := range pve.acls {
		if a.user == "sm-acme@pve" {
			acmeACL = append(acmeACL, a.role+" "+a.path)
		}
	}
	pve.mu.Unlock()
	slices.Sort(acmeACL)
	want := []string{"PVEDatastoreUser /storage/local-lvm", "PVEPoolAdmin /pool/tenant-acme", "PVESDNUser /sdn/zones/localnetwork/vmbr1", "PVETemplateUser /vms/9000", "PVEVMAdmin /pool/tenant-acme"}
	if !slices.Equal(acmeACL, want) {
		t.Errorf("acme ACLs = %v, want %v", acmeACL, want)
	}
	// Re-running with fewer templates removes the old ACL.
	code, _ = setup(globex, map[string]any{"storages": []string{"ceph"}, "bridges": []string{"vmbr0"}, "templates": []int{9000}})
	expect("update globex", code, 200)
	pve.mu.Lock()
	for _, a := range pve.acls {
		if a.user == "sm-globex@pve" && a.path == "/vms/9001" {
			t.Errorf("stale ACL on /vms/9001 was not removed")
		}
	}
	pve.mu.Unlock()

	// Users: alice in Acme (with the admin role, which a tenant clamps),
	// bob in Globex.
	mk := func(name string, tenant map[string]any, role string) {
		c, b, _ := call(root, "POST", "/api/v1/users", map[string]any{"username": name, "password": name + "-password-1", "role": role, "tenant_id": id(tenant)})
		expect("create "+name, c, 201)
		if c != 201 {
			t.Fatalf("%v", b)
		}
	}
	mk("alice", acme, "admin")
	mk("bob", globex, "operator")
	al, _ := login("alice", "alice-password-1")
	bo, _ := login("bob", "bob-password-1")

	// alice's permissions shrink to the tenant set.
	_, me, _ := call(al, "GET", "/api/v1/me", nil)
	perms := []string{}
	for _, x := range me["permissions"].([]any) {
		perms = append(perms, x.(string))
	}
	for _, pm := range perms {
		if !tenantPerms[pm] {
			t.Errorf("tenant user has panel permission %s", pm)
		}
	}
	if me["tenant"].(map[string]any)["name"] != "Acme" {
		t.Errorf("me.tenant = %v", me["tenant"])
	}

	// Platform pages are closed.
	for _, path := range []string{"/api/v1/users", "/api/v1/tenants", "/api/v1/roles", "/api/v1/playbooks", "/api/v1/settings/baseline", "/api/v1/ssh-keys", "/api/v1/tenant-options/" + cid} {
		expect("alice GET "+path, first(call(al, "GET", path, nil)), 403)
	}
	expect("alice adds a cluster", first(call(al, "POST", "/api/v1/proxmox", map[string]any{"name": "x"})), 403)
	expect("alice deletes the cluster", first(call(al, "DELETE", "/api/v1/proxmox/"+cid, nil)), 403)

	// Servers: only the tenant's server group.
	_, _, servers := call(al, "GET", "/api/v1/servers", nil)
	if len(servers) != 1 || servers[0].(map[string]any)["id"] != p.web {
		t.Errorf("alice sees servers %v", servers)
	}
	_, _, servers = call(bo, "GET", "/api/v1/servers", nil)
	if len(servers) != 0 {
		t.Errorf("bob (no server group) sees servers %v", servers)
	}
	expect("alice views another server", first(call(al, "GET", "/api/v1/servers/"+p.other, nil)), 403)

	// Cluster list: no address, token or fingerprint.
	_, _, clusters := call(al, "GET", "/api/v1/proxmox", nil)
	if len(clusters) != 1 {
		t.Fatalf("alice clusters %v", clusters)
	}
	cv := clusters[0].(map[string]any)
	if cv["url"] != nil || cv["token_id"] != nil || cv["fingerprint"] != nil || cv["pool"] != "tenant-acme" {
		t.Errorf("tenant cluster view leaks: %v", cv)
	}

	// Inventory: own pool plus allowed templates only, no nodes.
	_, res, _ := call(al, "GET", "/api/v1/proxmox/"+cid+"/resources", nil)
	var vmids []int
	for _, g := range res["guests"].([]any) {
		vmids = append(vmids, int(g.(map[string]any)["vmid"].(float64)))
	}
	if !slices.Equal(vmids, []int{100, 9000}) {
		t.Errorf("alice inventory = %v, want [100 9000]", vmids)
	}
	if n := res["nodes"].([]any); len(n) != 0 {
		t.Errorf("alice sees nodes %v", n)
	}
	for _, s := range res["storage"].([]any) {
		if s.(map[string]any)["storage"] != "local-lvm" {
			t.Errorf("alice sees storage %v", s)
		}
	}
	if c, ok := res["cluster"].(map[string]any); !ok || c["url"] != nil {
		t.Errorf("resources cluster leaks: %v", res["cluster"])
	}

	// Every guest route on someone else's guest is "not found"; on her
	// own it works.
	g := func(node, typ string, vmid int) string {
		return fmt.Sprintf("/api/v1/proxmox/%s/guests/%s/%s/%d", cid, node, typ, vmid)
	}
	for _, other := range []string{g("pve1", "qemu", 101), g("pve2", "lxc", 102), g("pve1", "qemu", 9000), g("pve2", "qemu", 100)} {
		expect("power "+other, first(call(al, "POST", other+"/power", map[string]string{"action": "start"})), 404)
		expect("snapshots "+other, first(call(al, "GET", other+"/snapshots", nil)), 404)
		expect("snapshot "+other, first(call(al, "POST", other+"/snapshots", map[string]any{"name": "x1"})), 404)
		expect("delete snapshot "+other, first(call(al, "DELETE", other+"/snapshots/base", nil)), 404)
		expect("rollback "+other, first(call(al, "POST", other+"/snapshots/base/rollback", nil)), 404)
		expect("delete "+other, first(call(al, "DELETE", other, map[string]string{"confirm": "globex-db"})), 404)
	}
	expect("alice powers her VM", first(call(al, "POST", g("pve1", "qemu", 100)+"/power", map[string]string{"action": "shutdown"})), 204)
	expect("alice lists her snapshots", first(call(al, "GET", g("pve1", "qemu", 100)+"/snapshots", nil)), 200)
	expect("bob powers alice's VM", first(call(bo, "POST", g("pve1", "qemu", 100)+"/power", map[string]string{"action": "start"})), 404)

	// Provisioning: her template, storage and bridge only, into her pool,
	// within quota (2 guests, 6 vCPUs).
	prov := func(c client, body map[string]any) (int, map[string]any) {
		b := map[string]any{"template_vmid": 9000, "node": "pve1", "name": "acme-app", "cores": 2, "memory_mb": 1024, "ip": "dhcp", "enroll": false}
		for k, v := range body {
			b[k] = v
		}
		code, out, _ := call(c, "POST", "/api/v1/proxmox/"+cid+"/provision", b)
		return code, out
	}
	expect("template not hers", first2(prov(al, map[string]any{"template_vmid": 9001})), 400)
	expect("storage not hers", first2(prov(al, map[string]any{"storage": "ceph"})), 400)
	expect("bridge not hers", first2(prov(al, map[string]any{"bridge": "vmbr0"})), 400)
	expect("other node", first2(prov(al, map[string]any{"target": "pve2"})), 400)
	expect("over the vCPU quota", first2(prov(al, map[string]any{"cores": 5})), 400)
	code, pv := prov(al, nil)
	expect("provision in quota", code, 202)
	waitProvision(t, p, al, pv["id"].(string))
	pve.mu.Lock()
	var made *fakeGuest
	for vmid, gg := range pve.guests {
		if gg.name == "acme-app" {
			made = gg
			if !strings.Contains(gg.config["net0"], "bridge=vmbr1") {
				t.Errorf("new VM %d net0 = %q", vmid, gg.config["net0"])
			}
		}
	}
	pve.mu.Unlock()
	if made == nil || made.pool != "tenant-acme" {
		t.Fatalf("new VM not in the tenant pool: %+v", made)
	}
	expect("over the guest quota", first2(prov(al, map[string]any{"name": "acme-app2", "cores": 1})), 400)

	// Provisioning history and audit log are per tenant.
	_, _, provs := call(bo, "GET", "/api/v1/proxmox/provisions", nil)
	if len(provs) != 0 {
		t.Errorf("bob sees provisions %v", provs)
	}
	_, _, audit := call(al, "GET", "/api/v1/audit", nil)
	if len(audit) == 0 {
		t.Errorf("alice sees no audit entries")
	}
	for _, e := range audit {
		if e.(map[string]any)["actor"] != "alice" {
			t.Errorf("alice's audit log has %v", e)
		}
	}

	// Proxmox enforces it too: alice's tenant token, used directly,
	// cannot touch Globex's VM or the unpooled container.
	tok, sec := pve.tokenOf("sm-acme@pve")
	if tok == "" {
		t.Fatal("no acme token")
	}
	expect("token: start globex VM", pve.as(tok, sec, "POST", "/nodes/pve1/qemu/101/status/start", nil), 403)
	expect("token: delete infra CT", pve.as(tok, sec, "DELETE", "/nodes/pve2/lxc/102", nil), 403)
	expect("token: clone into globex pool", pve.as(tok, sec, "POST", "/nodes/pve1/qemu/9000/clone", url.Values{"newid": {"777"}, "pool": {"tenant-globex"}}), 403)
	expect("token: power the template", pve.as(tok, sec, "POST", "/nodes/pve1/qemu/9000/status/start", nil), 403)
	expect("token: read users", pve.as(tok, sec, "GET", "/access/users", nil), 403)
	expect("token: start own VM", pve.as(tok, sec, "POST", "/nodes/pve1/qemu/100/status/start", nil), 200)

	// A broader Proxmox ACL that leaks other guests to the tenant's user
	// (here through a group it was put in) fails the isolation check.
	pve.mu.Lock()
	pve.acls = append(pve.acls, fakeACL{"/", "PVEAuditor", "sm-acme@pve"}, fakeACL{"/", "PVEAuditor", "group:auditors"})
	pve.groups["sm-acme@pve"] = []string{"auditors"}
	pve.mu.Unlock()
	code, out = setup(acme, map[string]any{"storages": []string{"local-lvm"}, "bridges": []string{"vmbr1"}, "templates": []int{9000}})
	if code != 502 || !strings.Contains(fmt.Sprint(out["error"]), "isolation check failed") {
		t.Errorf("leak through a group: got %d %v", code, out)
	}
	pve.mu.Lock()
	for _, a := range pve.acls {
		if a.user == "sm-acme@pve" && a.path == "/" {
			t.Errorf("stray user ACL on / survived setup")
		}
	}
	delete(pve.groups, "sm-acme@pve")
	pve.mu.Unlock()

	// Rotating replaces the token; the old one stops working.
	code, _ = setup(acme, map[string]any{"storages": []string{"local-lvm"}, "bridges": []string{"vmbr1"}, "templates": []int{9000}, "rotate": true})
	expect("rotate", code, 200)
	expect("old token after rotate", pve.as(tok, sec, "GET", "/cluster/resources", nil), 401)
	expect("alice after rotate", first(call(al, "GET", "/api/v1/proxmox/"+cid+"/resources", nil)), 200)

	// Users can't escape their tenant, and the last platform admin rule
	// ignores tenant admins.
	var rootID, aliceID int64
	_ = p.conn.QueryRow(t.Context(), `SELECT id FROM admins WHERE username = 'root'`).Scan(&rootID)
	_ = p.conn.QueryRow(t.Context(), `SELECT id FROM admins WHERE username = 'alice'`).Scan(&aliceID)
	expect("root moves self into a tenant", first(call(root, "PUT", fmt.Sprintf("/api/v1/users/%d", rootID), map[string]any{"role": "admin", "tenant_id": id(acme)})), 400)
	expect("update alice without tenant_id keeps her in Acme", first(call(root, "PUT", fmt.Sprintf("/api/v1/users/%d", aliceID), map[string]any{"role": "admin"})), 204)
	_, me, _ = call(al, "GET", "/api/v1/me", nil)
	if me == nil || me["tenant"] == nil {
		t.Errorf("alice left her tenant: %v", me)
	}
	expect("delete tenant with users", first(call(root, "DELETE", fmt.Sprintf("/api/v1/tenants/%d", id(acme)), nil)), 409)

	// Removing the isolation deletes the Proxmox user; alice loses access.
	expect("remove isolation", first(call(root, "DELETE", fmt.Sprintf("/api/v1/tenants/%d/clusters/%s", id(acme), cid), nil)), 204)
	pve.mu.Lock()
	if pve.users["sm-acme@pve"] {
		t.Errorf("Proxmox user survived")
	}
	pve.mu.Unlock()
	_, _, clusters = call(al, "GET", "/api/v1/proxmox", nil)
	if len(clusters) != 0 {
		t.Errorf("alice still sees %v", clusters)
	}
	expect("alice resources after removal", first(call(al, "GET", "/api/v1/proxmox/"+cid+"/resources", nil)), 404)
}

func first2(code int, _ map[string]any) int { return code }

func waitProvision(t *testing.T, p *testPanel, c client, id string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		_, _, list := p.call(c, "GET", "/api/v1/proxmox/provisions", nil)
		for _, x := range list {
			m := x.(map[string]any)
			if m["id"] == id && m["status"] != "running" {
				if m["status"] != "succeeded" {
					t.Fatalf("provision %s: %v", m["status"], m["log"])
				}
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("provision did not finish")
}
