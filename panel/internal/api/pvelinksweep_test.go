package api

import (
	"testing"

	"github.com/jacksonm36/servermgr/panel/internal/store"
)

func TestSMBIOSUUID(t *testing.T) {
	for in, want := range map[string]string{
		"uuid=491B2EF6-604a-4ddc-8daa-8283e862b64a":                   "491b2ef6-604a-4ddc-8daa-8283e862b64a",
		"manufacturer=QEMU,uuid=7676420c-5887-46c2-9e7a-bf5c37e23569": "7676420c-5887-46c2-9e7a-bf5c37e23569",
		"":                      "",
		"base64=1,product=eA==": "",
	} {
		if got := smbiosUUID(in); got != want {
			t.Errorf("smbiosUUID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLinkVerdict(t *testing.T) {
	vm := &linkGuest{node: "pve1", typ: "qemu"}
	ct := &linkGuest{node: "pve1", typ: "lxc"}
	known := store.ProxmoxLink{VMID: 147, ServerID: "s", VMUUID: "old"}
	legacy := store.ProxmoxLink{VMID: 147, ServerID: "s"}
	for _, c := range []struct {
		name   string
		l      store.ProxmoxLink
		g      *linkGuest
		uuid   string
		online bool
		want   string
	}{
		{"online, uuid unknown: learn it", legacy, vm, "new", true, linkLearn},
		{"online, uuid matches", known, vm, "old", true, linkKeep},
		{"online but guest missing from listing: keep", known, nil, "", true, linkKeep},
		{"offline, guest destroyed", known, nil, "", false, linkGone},
		{"offline, legacy link, guest destroyed", legacy, nil, "", false, linkGone},
		{"offline, VMID reused", known, vm, "new", false, linkReplaced},
		{"offline, same VM (just stopped)", known, vm, "old", false, linkKeep},
		{"offline, legacy link, VM exists: cannot tell", legacy, vm, "new", false, linkKeep},
		{"offline, uuid unreadable", known, vm, "", false, linkKeep},
		{"offline container", legacy, ct, "", false, linkKeep},
	} {
		if got := linkVerdict(c.l, c.g, c.uuid, c.online); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}
