package api

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// fakeAgent answers jobs for a server with answer's result, or fails them
// with its error. It records the params each op got.
type fakeAgent struct {
	mu   sync.Mutex
	seen map[string][]map[string]string
}

func (p *testPanel) fakeAgent(serverID string, answer func(op string, params map[string]string) (any, string)) *fakeAgent {
	f := &fakeAgent{seen: map[string][]map[string]string{}}
	send, detach := p.api.Hub.Attach(serverID)
	done := make(chan struct{})
	p.t.Cleanup(func() { close(done); detach() })
	go func() {
		for {
			select {
			case <-done:
				return
			case msg, ok := <-send:
				if !ok {
					return
				}
				j := msg.GetJob()
				if j == nil {
					continue
				}
				op := ""
				for name, spec := range opsByName {
					if spec.op == j.Op {
						op = name
					}
				}
				f.mu.Lock()
				f.seen[op] = append(f.seen[op], j.Params)
				f.mu.Unlock()
				res, errMsg := answer(op, j.Params)
				status, raw := "succeeded", []byte(nil)
				if errMsg != "" {
					status = "failed"
				} else {
					raw, _ = json.Marshal(res)
				}
				_ = p.api.Store.UpdateJob(context.Background(), j.Id, status, "", raw, errMsg, true)
			}
		}
	}()
	return f
}

func (f *fakeAgent) params(op string) []map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seen[op]
}

func TestZFSReplicationDB(t *testing.T) {
	p := newTestPanel(t)
	old := replJobWait
	replJobWait = 10 * time.Second
	t.Cleanup(func() { replJobWait = old })
	call, expect := p.call, p.expect
	root, _ := p.login("root", rootPW)

	var mu sync.Mutex
	failPolicy := false
	src := p.fakeAgent(p.web, func(op string, q map[string]string) (any, string) {
		mu.Lock()
		defer mu.Unlock()
		switch op {
		case "zfs.repl_key":
			return replKey{PublicKey: "ssh-ed25519 AAAAsource servermgr-zfs-repl", AddressHint: "10.0.0.5"}, ""
		case "zfs.repl_policy":
			if failPolicy {
				return nil, "zfs: dataset does not exist"
			}
			repls := []map[string]any{}
			if q["delete"] != "1" {
				repls = append(repls, map[string]any{"dataset": q["dataset"], "target_host": q["target_host"]})
			}
			return map[string]any{"replications": repls}, ""
		}
		return nil, "unexpected " + op
	})
	dst := p.fakeAgent(p.db, func(op string, q map[string]string) (any, string) {
		if op == "zfs.repl_authorize" {
			return map[string]any{"host_keys": []string{"ssh-ed25519 AAAAhost1", "ecdsa-sha2-nistp256 AAAAhost2"}, "ssh_port": 2222}, ""
		}
		return nil, "unexpected " + op
	})
	base := "/api/v1/servers/" + p.web + "/zfs/replication"

	code, k, _ := call(root, "GET", base+"/key", nil)
	expect("key", code, 200)
	if k["address_hint"] != "10.0.0.5" {
		t.Errorf("key: %v", k)
	}

	req := func(over map[string]any) map[string]any {
		b := map[string]any{"dataset": "tank/data", "target_server": p.db, "target_parent": "backup/web1", "target_host": "db1.lan", "from": "10.0.0.5"}
		for k, v := range over {
			b[k] = v
		}
		return b
	}
	expect("same server as target", first(call(root, "POST", base, req(map[string]any{"target_server": p.web}))), 400)
	expect("bad dataset", first(call(root, "POST", base, req(map[string]any{"dataset": "tank/../x"}))), 400)
	expect("no target address", first(call(root, "POST", base, req(map[string]any{"target_host": ""}))), 400)
	expect("bad schedule", first(call(root, "POST", base, req(map[string]any{"schedule": "yearly"}))), 400)
	expect("offline target", first(call(root, "POST", base, req(map[string]any{"target_server": p.other}))), 409)

	// Internal ops can't be sent as plain jobs.
	for _, op := range []string{"zfs.repl_key", "zfs.repl_authorize", "zfs.repl_policy"} {
		expect("job "+op, first(call(root, "POST", "/api/v1/servers/"+p.web+"/jobs", map[string]any{"op": op, "params": map[string]string{}})), 400)
	}

	code, out, _ := call(root, "POST", base, req(nil))
	expect("create", code, 200)
	if out["target_server"] != p.db || len(out["replications"].([]any)) != 1 {
		t.Errorf("create: %v", out)
	}
	if a := dst.params("zfs.repl_authorize"); len(a) != 1 || a[0]["public_key"] != "ssh-ed25519 AAAAsource servermgr-zfs-repl" || a[0]["parent"] != "backup/web1" || a[0]["from"] != "10.0.0.5" {
		t.Errorf("authorize params: %v", a)
	}
	pol := src.params("zfs.repl_policy")
	if len(pol) != 1 || pol[0]["host_keys"] != "ssh-ed25519 AAAAhost1\necdsa-sha2-nistp256 AAAAhost2" ||
		pol[0]["target_port"] != "2222" || pol[0]["keep"] != "3" || pol[0]["schedule"] != "daily" || pol[0]["target_host"] != "db1.lan" {
		t.Errorf("policy params: %v", pol)
	}

	// The last replication to the target goes: its key is revoked there.
	code, out, _ = call(root, "DELETE", base, map[string]any{"dataset": "tank/data", "target_host": "db1.lan", "target_server": p.db})
	expect("delete", code, 200)
	if out["warning"] != nil {
		t.Errorf("delete warning: %v", out["warning"])
	}
	if a := dst.params("zfs.repl_authorize"); len(a) != 2 || a[1]["revoke"] != "1" || a[1]["public_key"] == "" {
		t.Errorf("revoke params: %v", a)
	}

	// A failed step is a bad gateway carrying the agent's error.
	mu.Lock()
	failPolicy = true
	mu.Unlock()
	code, out, _ = call(root, "POST", base, req(nil))
	expect("failed policy", code, 502)
	if msg, _ := out["error"].(string); msg != "zfs.repl_policy: zfs: dataset does not exist" {
		t.Errorf("error: %v", out)
	}
}
