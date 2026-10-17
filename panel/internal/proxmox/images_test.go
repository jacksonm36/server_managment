package proxmox

import (
	"net/url"
	"strings"
	"testing"
)

func TestImages(t *testing.T) {
	seen := map[string]bool{}
	for _, img := range Images {
		if img.ID == "" || img.Name == "" || img.User == "" || seen[img.ID] {
			t.Errorf("bad or duplicate image %+v", img)
		}
		seen[img.ID] = true
		for _, u := range []string{img.URL, img.Checksums} {
			if p, err := url.Parse(u); err != nil || p.Scheme != "https" {
				t.Errorf("%s: %q is not an https URL", img.ID, u)
			}
		}
	}
	if len(Images) < 5 || ImageByID("debian-13") == nil {
		t.Fatalf("catalog has %d images", len(Images))
	}
}

func TestFindChecksum(t *testing.T) {
	h256 := strings.Repeat("ab", 32)
	h512 := strings.Repeat("CD", 64)
	for _, c := range []struct{ file, want, algo string }{
		{h512 + "  debian-13-genericcloud-amd64.qcow2\n" + h256 + "  other.qcow2", strings.ToLower(h512), "sha512"},
		{h256 + " *noble-server-cloudimg-amd64.img", h256, "sha256"},
		{"# comment\nSHA256 (Rocky-9-GenericCloud-Base.latest.x86_64.qcow2) = " + h256, h256, "sha256"},
		{h256 + "  images/Arch-Linux-x86_64-cloudimg.qcow2", h256, "sha256"},
	} {
		file := map[string]string{"sha512": "debian-13-genericcloud-amd64.qcow2"}[c.algo]
		for _, f := range []string{"noble-server-cloudimg-amd64.img", "Rocky-9-GenericCloud-Base.latest.x86_64.qcow2", "Arch-Linux-x86_64-cloudimg.qcow2"} {
			if strings.Contains(c.file, f) {
				file = f
			}
		}
		algo, sum, ok := FindChecksum(strings.NewReader(c.file), file)
		if !ok || algo != c.algo || sum != c.want {
			t.Errorf("FindChecksum(%q) = %s %s %v", file, algo, sum, ok)
		}
	}
	if _, _, ok := FindChecksum(strings.NewReader(h256+"  a.img"), "b.img"); ok {
		t.Error("matched the wrong file")
	}
}
