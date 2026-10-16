package proxmox

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNaturalLess(t *testing.T) {
	for _, c := range [][2]string{
		{"ubuntu-24.04.9-live-server-amd64.iso", "ubuntu-24.04.10-live-server-amd64.iso"},
		{"ubuntu-24.04-live-server-amd64.iso", "ubuntu-24.04.1-live-server-amd64.iso"},
		{"proxmox-ve_8.4-1.iso", "proxmox-ve_9.0-1.iso"},
		{"archlinux-2026.09.01-x86_64.iso", "archlinux-2026.10.01-x86_64.iso"},
	} {
		if !naturalLess(c[0], c[1]) || naturalLess(c[1], c[0]) {
			t.Errorf("%s should sort before %s", c[0], c[1])
		}
	}
}

func TestISOCatalog(t *testing.T) {
	if len(ISOs) < 10 || ISOByID("debian-netinst") == nil {
		t.Fatalf("catalog: %d entries", len(ISOs))
	}
}

func TestResolveISO(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/u/SHA256SUMS":
			w.Write([]byte(strings.Repeat("a", 64) + " *ubuntu-24.04.3-live-server-amd64.iso\n" +
				strings.Repeat("b", 64) + " *ubuntu-24.04.4-live-server-amd64.iso\n" +
				strings.Repeat("c", 64) + " *ubuntu-24.04.4-desktop-amd64.iso\n"))
		case "/r/CHECKSUM":
			w.Write([]byte("# Rocky-9.6-x86_64-minimal.iso: 1 bytes\nSHA256 (Rocky-9.6-x86_64-minimal.iso) = " + strings.Repeat("D", 64) + "\n"))
		case "/a/":
			w.Write([]byte(`<a href="alpine-virt-3.22.1-x86_64.iso">x</a> <a href="alpine-virt-3.22.2-x86_64.iso">x</a> <a href="alpine-virt-3.22.2-x86_64.iso.sha256">s</a> <a href="../">up</a>`))
		case "/a/alpine-virt-3.22.2-x86_64.iso.sha256":
			w.Write([]byte(strings.Repeat("e", 64) + "  alpine-virt-3.22.2-x86_64.iso\n"))
		case "/redirect/SHA256SUMS":
			http.Redirect(w, r, "http://example.invalid/x", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := isoHTTP.Transport
	isoHTTP.Transport = srv.Client().Transport
	defer func() { isoHTTP.Transport = old }()
	ctx := context.Background()
	mk := func(x ISO) *ISO {
		if x.Match != "" {
			x.match = mustRe(x.Match)
		}
		return &x
	}

	r, err := mk(ISO{Dir: srv.URL + "/u/", Match: `^ubuntu-24\.04(\.\d+)?-live-server-amd64\.iso$`, Checksums: "SHA256SUMS"}).Resolve(ctx)
	if err != nil || r.File != "ubuntu-24.04.4-live-server-amd64.iso" || r.Checksum != strings.Repeat("b", 64) || r.Algo != "sha256" || r.URL != srv.URL+"/u/ubuntu-24.04.4-live-server-amd64.iso" {
		t.Fatalf("ubuntu: %+v %v", r, err)
	}
	r, err = mk(ISO{Dir: srv.URL + "/r/", Match: `^Rocky-9\.\d+-x86_64-minimal\.iso$`, Checksums: "CHECKSUM"}).Resolve(ctx)
	if err != nil || r.File != "Rocky-9.6-x86_64-minimal.iso" || r.Checksum != strings.Repeat("d", 64) {
		t.Fatalf("rocky: %+v %v", r, err)
	}
	r, err = mk(ISO{Dir: srv.URL + "/a/", Match: `^alpine-virt-\d+\.\d+\.\d+-x86_64\.iso$`, List: "dir", ChecksumSuffix: ".sha256"}).Resolve(ctx)
	if err != nil || r.File != "alpine-virt-3.22.2-x86_64.iso" || r.Checksum != strings.Repeat("e", 64) {
		t.Fatalf("alpine: %+v %v", r, err)
	}
	if _, err = mk(ISO{Dir: srv.URL + "/u/", Match: `^nothing\.iso$`, Checksums: "SHA256SUMS"}).Resolve(ctx); err == nil {
		t.Fatal("no match should fail")
	}
	if _, err = mk(ISO{Dir: srv.URL + "/redirect/", Match: `x`, Checksums: "SHA256SUMS"}).Resolve(ctx); err == nil {
		t.Fatal("a redirect to http should fail")
	}
	r, err = mk(ISO{URL: srv.URL + "/fixed/tool.iso"}).Resolve(ctx)
	if err != nil || r.File != "tool.iso" || r.Checksum != "" {
		t.Fatalf("fixed: %+v %v", r, err)
	}
}
