package api

import (
	"strings"
	"testing"

	"github.com/jacksonm36/servermgr/panel/internal/store"
)

// Synthetic keys (not anyone's real keys).
const (
	keyA = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGq6yZ3c6xT3wT3fW0bJ0cX1pZQ9b3Q3u2yH0b1kJj9e me@example.com"
	keyB = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB0n9pJ5c2ZJ0a8s3uXbq5mX8f9q3V7y0l2xP5k0aH1Q me@example.com"
	keyC = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIM1rQy2w3X4c5V6b7N8m9L0k1J2h3G4f5D6s7A8q9W0e root@box"
	keyD = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPq7wR8eT9yU0iO1pA2sD3fG4hJ5kL6zX7cV8bN9mQ1w"
)

const sampleConfig = `
Host web1 10.0.0.5
    User jackson
    IdentityFile C:/Users/me/.ssh/id_ed25519
Host web2
    User jackson
    IdentityFile ~/.ssh/id_ed25519
Host db-root
    User root
    IdentityFile ~/.ssh/id_ed25519
Host relay relay.example.com
    User root
    IdentityFile "C:\Users\me\.ssh\id_ed25519_relay"
Host *
    IdentityFile ~/.ssh/id_wildcard
`

func TestSuggestNames(t *testing.T) {
	var cands []keyCandidate
	cands = append(cands, parseKeyText("id_ed25519.pub", keyA)...)
	cands = append(cands, parseKeyText("id_ed25519_relay.pub", keyB)...)
	cands = append(cands, parseKeyText("box.pub", keyC)...)
	cands = append(cands, parseKeyText("", keyD)...)
	cands = append(cands, parseKeyText("id_rsa", "-----BEGIN OPENSSH PRIVATE KEY-----\nxyz\n")...)
	library := []*store.SSHKey{{Name: "box", Fingerprint: "SHA256:other"}}
	suggestNames(cands, parseSSHConfig(sampleConfig), library)

	want := []struct{ name, from string }{
		{"jackson main", "ssh config"}, // used by 3 hosts, mostly as jackson
		{"relay", "ssh config"},        // one host
		{"root@box", "comment"},        // unique comment
		{"ed25519 ", "type"},           // no file, no comment: type + fingerprint
	}
	for i, w := range want {
		if c := cands[i]; c.Error != "" || !strings.HasPrefix(c.Name, w.name) || c.NameFrom != w.from {
			t.Errorf("key %d: got %q from %q (err %q), want %q from %q", i, c.Name, c.NameFrom, c.Error, w.name, w.from)
		}
	}
	if len(cands[0].Hosts) != 3 {
		t.Errorf("hosts: %v", cands[0].Hosts)
	}
	if !strings.Contains(cands[4].Error, "private key") {
		t.Errorf("private key not refused: %+v", cands[4])
	}
	if cands[0].Bits != 256 || cands[0].Fingerprint == "" || cands[0].Warning != "" {
		t.Errorf("detection: %+v", cands[0])
	}
}

func TestNameClashAndDuplicates(t *testing.T) {
	var cands []keyCandidate
	cands = append(cands, parseKeyText("a.pub", keyA)...)
	cands = append(cands, parseKeyText("b.pub", keyB)...)
	cands = append(cands, parseKeyText("c.pub", keyA)...)
	fpC := parseKeyText("", keyC)[0].Fingerprint
	cands = append(cands, parseKeyText("", keyC)...)
	// Both share a comment, so they fall back to the file name; "a" is taken.
	library := []*store.SSHKey{{Name: "a (me@example.com)", Fingerprint: "SHA256:x"}, {Name: "old", Fingerprint: fpC}}
	suggestNames(cands, nil, library)
	if cands[0].Name != "a (me@example.com) 2" || cands[1].Name != "b (me@example.com)" {
		t.Errorf("names: %q %q", cands[0].Name, cands[1].Name)
	}
	if cands[2].Error == "" {
		t.Error("duplicate key in one batch not flagged")
	}
	if cands[3].Existing != "old" {
		t.Errorf("existing key not recognised: %+v", cands[3])
	}
}

func TestKeyWarnings(t *testing.T) {
	if keyWarning("ssh-dss", 1024) == "" || keyWarning("ssh-rsa", 1024) == "" || keyWarning("ssh-ed25519", 256) != "" {
		t.Error("warnings")
	}
	got := parseKeyText("", `command="rm -rf /",no-pty `+keyA)
	if len(got) != 1 || !strings.Contains(got[0].Warning, "options") || strings.Contains(got[0].Line, "command") {
		t.Errorf("options not stripped: %+v", got)
	}
	if fp := storedKeyParam(keyA); !strings.HasPrefix(fp, "SHA256:") {
		t.Errorf("stored param %q", fp)
	}
}
