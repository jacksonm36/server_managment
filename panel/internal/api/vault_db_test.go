package api

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jacksonm36/servermgr/panel/internal/vault"
)

func TestVaultAndKeyLibraryEndToEnd(t *testing.T) {
	p := newTestPanel(t)
	ctx := context.Background()
	root, _ := p.login("root", rootPW)

	// A notification channel saved before the vault exists uses the panel key.
	code, _, _ := p.call(root, "POST", "/api/v1/channels", map[string]any{
		"name": "hook", "type": "webhook", "enabled": true, "config": map[string]string{"url": "https://example.com/hook-secret-token"},
	})
	p.expect("create channel", code, 200)
	var sealed string
	_ = p.conn.QueryRow(ctx, `SELECT config FROM notify_channels`).Scan(&sealed)
	if vault.IsSealed(sealed) {
		t.Fatal("channel sealed by a vault that doesn't exist")
	}

	// Keys need the vault.
	line := keyD + " laptop"
	code, _, _ = p.call(root, "POST", "/api/v1/ssh-keys", map[string]any{"keys": []map[string]string{{"line": line, "name": "laptop"}}})
	p.expect("import without vault", code, 423)

	code, setup, _ := p.call(root, "POST", "/api/v1/vault/setup", map[string]string{"passphrase": "short"})
	p.expect("weak passphrase", code, 400)
	code, setup, _ = p.call(root, "POST", "/api/v1/vault/setup", map[string]string{"passphrase": "vault passphrase 1"})
	p.expect("setup", code, 200)
	recovery, _ := setup["recovery_key"].(string)
	if recovery == "" || setup["secrets_moved"].(float64) != 1 {
		t.Fatalf("setup: %v", setup)
	}
	_ = p.conn.QueryRow(ctx, `SELECT config FROM notify_channels`).Scan(&sealed)
	if !vault.IsSealed(sealed) {
		t.Fatal("existing secret not moved into the vault")
	}
	p.expect("setup twice", first(p.call(root, "POST", "/api/v1/vault/setup", map[string]string{"passphrase": "vault passphrase 2"})), 409)

	// Recognise and import.
	code, _, cands := p.call(root, "POST", "/api/v1/ssh-keys/parse", map[string]any{
		"files": []map[string]string{{"name": "id_ed25519_relay.pub", "content": keyB}},
		"text":  line, "ssh_config": sampleConfig,
	})
	p.expect("parse", code, 200)
	if len(cands) != 2 || cands[0].(map[string]any)["name"] != "relay" || cands[1].(map[string]any)["name"] != "laptop" {
		t.Fatalf("parse: %v", cands)
	}
	code, _, res := p.call(root, "POST", "/api/v1/ssh-keys", map[string]any{"keys": []map[string]string{
		{"line": cands[0].(map[string]any)["line"].(string), "name": "relay", "source": "id_ed25519_relay.pub"},
		{"line": line, "name": "laptop"},
		{"line": line, "name": "laptop again"},
	}})
	p.expect("import", code, 200)
	if res[0].(map[string]any)["id"] == nil || res[1].(map[string]any)["id"] == nil || res[2].(map[string]any)["error"] == nil {
		t.Fatalf("import: %v", res)
	}
	var stored string
	_ = p.conn.QueryRow(ctx, `SELECT public_key FROM ssh_keys WHERE name = 'laptop'`).Scan(&stored)
	if !vault.IsSealed(stored) || strings.Contains(stored, strings.Fields(keyD)[1]) {
		t.Fatalf("key stored readable: %q", stored)
	}
	keyID := int64(res[1].(map[string]any)["id"].(float64))

	// Restart: the vault comes back locked.
	var hdr vault.Header
	if ok, err := p.api.Store.VaultHeader(ctx, &hdr); !ok || err != nil {
		t.Fatal(ok, err)
	}
	p.api.Vault = vault.New(&hdr)
	code, _, _ = p.call(root, "GET", fmt.Sprintf("/api/v1/ssh-keys/%d/public", keyID), nil)
	p.expect("read key while locked", code, 423)
	code, _, _ = p.call(root, "POST", fmt.Sprintf("/api/v1/ssh-keys/%d/deploy", keyID), map[string]any{"server_ids": []string{p.web}})
	p.expect("deploy while locked", code, 423)
	code, lst, _ := p.call(root, "GET", "/api/v1/ssh-keys", nil)
	p.expect("list while locked", code, 200)
	if len(lst["keys"].([]any)) != 2 || lst["vault"].(map[string]any)["unlocked"] != false {
		t.Fatalf("list: %v", lst)
	}

	p.expect("wrong passphrase", first(p.call(root, "POST", "/api/v1/vault/unlock", map[string]string{"passphrase": "nope nope nope"})), 403)
	p.expect("unlock with recovery key", first(p.call(root, "POST", "/api/v1/vault/unlock", map[string]string{"passphrase": strings.ToLower(recovery)})), 200)
	code, pub, _ := p.call(root, "GET", fmt.Sprintf("/api/v1/ssh-keys/%d/public", keyID), nil)
	if code != 200 || pub["line"] != line {
		t.Fatalf("public: %d %v", code, pub)
	}
	// Deploy reaches each server; they are offline here.
	code, _, out := p.call(root, "POST", fmt.Sprintf("/api/v1/ssh-keys/%d/deploy", keyID), map[string]any{"server_ids": []string{p.web, p.db}})
	p.expect("deploy", code, 202)
	if len(out) != 2 || !strings.Contains(out[0].(map[string]any)["error"].(string), "offline") {
		t.Fatalf("deploy: %v", out)
	}
	// Change passphrase, lock, unlock with the new one.
	p.expect("change passphrase", first(p.call(root, "POST", "/api/v1/vault/passphrase", map[string]string{"current": "vault passphrase 1", "new": "vault passphrase 3"})), 204)
	p.expect("lock", first(p.call(root, "POST", "/api/v1/vault/lock", nil)), 200)
	_, _ = p.api.Store.VaultHeader(ctx, &hdr)
	p.api.Vault = vault.New(&hdr)
	p.expect("old passphrase", first(p.call(root, "POST", "/api/v1/vault/unlock", map[string]string{"passphrase": "vault passphrase 1"})), 403)
	p.expect("new passphrase", first(p.call(root, "POST", "/api/v1/vault/unlock", map[string]string{"passphrase": "vault passphrase 3"})), 200)
	code, chans, _ := p.call(root, "GET", "/api/v1/channels", nil)
	_ = chans
	p.expect("channels readable after unlock", code, 200)
}
