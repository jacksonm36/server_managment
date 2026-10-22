package ops

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/openpgp"
	"golang.org/x/crypto/openpgp/armor"
)

func TestSwExpand(t *testing.T) {
	v := map[string]string{"ID": "debian", "CODENAME": "bookworm", "MAJOR": "12"}
	got, err := swExpand("https://x/${ID} ${CODENAME} main", v)
	if err != nil || got != "https://x/debian bookworm main" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := swExpand("https://x/${CODENAME}", map[string]string{"ID": "fedora"}); err == nil {
		t.Error("a missing codename expanded")
	}
}

func TestSwCheck(t *testing.T) {
	ok := swApp{ID: "chrome", Apt: []string{"google-chrome-stable", "7zip|p7zip-full"}, Bin: []string{"hayabusa-*-lin=hayabusa"},
		AptRepo: &swRepo{Key: "https://k", Fingerprints: []string{"EB4C1BFD4F042F6DDDCCEC917721F63BD38B4796"}}}
	if err := swCheck(&ok); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []swApp{
		{ID: "../x"},
		{ID: "a", Apt: []string{"-oAPT::x"}},
		{ID: "a", Dnf: []string{"x;rm"}},
		{ID: "a", Bin: []string{"../../etc/passwd"}},
		{ID: "a", AptRepo: &swRepo{Key: "http://k", Fingerprints: []string{"EB4C1BFD4F042F6DDDCCEC917721F63BD38B4796"}}},
		{ID: "a", AptRepo: &swRepo{Key: "https://k"}},
		{ID: "a", Archive: &swFile{URL: "https://x/a.zip", SHA256: "nope"}},
	} {
		if swCheck(&bad) == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestSwProtected(t *testing.T) {
	for _, p := range []string{"openssh-server", "systemd", "linux-image-amd64", "kernel-core", "sudo", "qemu-guest-agent", "libc6", "python3", "nginx"} {
		if !swProtected.MatchString(p) {
			t.Errorf("%s is not protected", p)
		}
	}
	for _, p := range []string{"htop", "firefox", "nmap", "python3-pip", "google-chrome-stable"} {
		if swProtected.MatchString(p) {
			t.Errorf("%s is protected", p)
		}
	}
}

func testKey(t *testing.T) (*openpgp.Entity, []byte) {
	e, err := openpgp.NewEntity("test", "", "t@example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w, _ := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	if err := e.Serialize(w); err != nil {
		t.Fatal(err)
	}
	w.Close()
	return e, buf.Bytes()
}

func TestSwKeyPinned(t *testing.T) {
	e, pub := testKey(t)
	_, other := testKey(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/two" {
			w.Write(append(append([]byte{}, pub...), other...))
			return
		}
		w.Write(pub)
	}))
	defer srv.Close()
	old := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	defer func() { http.DefaultTransport = old }()
	fp := fmt.Sprintf("%X", e.PrimaryKey.Fingerprint)
	got, err := swKey(context.Background(), srv.URL+"/one", []string{fp})
	if err != nil || !bytes.Contains(got, []byte("BEGIN PGP PUBLIC KEY BLOCK")) {
		t.Fatalf("pinned key refused: %v", err)
	}
	if _, err := swKey(context.Background(), srv.URL+"/one", []string{strings.Repeat("A", 40)}); err == nil {
		t.Error("a key with another fingerprint was trusted")
	}
	if _, err := swKey(context.Background(), srv.URL+"/two", []string{fp}); err == nil {
		t.Error("a keyring with an extra, unpinned key was trusted")
	}
}

func TestSwUnpack(t *testing.T) {
	dir := t.TempDir()
	// A zip keeps the executable bit; an escaping entry is refused.
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	h := &zip.FileHeader{Name: "tool-1.0/bin/tool"}
	h.SetMode(0o755)
	f, _ := zw.CreateHeader(h)
	f.Write([]byte("#!/bin/sh\n"))
	f, _ = zw.Create("tool-1.0/README")
	f.Write([]byte("hi"))
	zw.Close()
	zp := filepath.Join(dir, "a.zip")
	os.WriteFile(zp, zb.Bytes(), 0o644)
	out := filepath.Join(dir, "out")
	if err := swUnpack(zp, "https://x/a.zip", out); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(out, "tool-1.0/bin/tool")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode: %v %v", fi, err)
	}
	if fi, _ := os.Stat(filepath.Join(out, "tool-1.0/README")); fi.Mode().Perm() != 0o644 {
		t.Fatalf("README mode %v", fi.Mode())
	}

	zb.Reset()
	zw = zip.NewWriter(&zb)
	f, _ = zw.Create("../evil")
	f.Write([]byte("x"))
	zw.Close()
	os.WriteFile(zp, zb.Bytes(), 0o644)
	if err := swUnpack(zp, "https://x/a.zip", filepath.Join(dir, "out2")); err == nil {
		t.Error("an entry outside the folder was written")
	}

	// A tar link out of the folder is skipped; one inside is kept.
	var tb bytes.Buffer
	gz := gzip.NewWriter(&tb)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "t/bin/real", Mode: 0o755, Size: 1, Typeflag: tar.TypeReg})
	tw.Write([]byte("x"))
	tw.WriteHeader(&tar.Header{Name: "t/bin/ok", Linkname: "real", Typeflag: tar.TypeSymlink})
	tw.WriteHeader(&tar.Header{Name: "t/bin/bad", Linkname: "/etc/shadow", Typeflag: tar.TypeSymlink})
	tw.WriteHeader(&tar.Header{Name: "t/bin/bad2", Linkname: "../../../../etc/shadow", Typeflag: tar.TypeSymlink})
	tw.Close()
	gz.Close()
	tp := filepath.Join(dir, "a.tar.gz")
	os.WriteFile(tp, tb.Bytes(), 0o644)
	out3 := filepath.Join(dir, "out3")
	if err := swUnpack(tp, "https://x/a.tar.gz", out3); err != nil {
		t.Fatal(err)
	}
	if l, err := os.Readlink(filepath.Join(out3, "t/bin/ok")); err != nil || l != "real" {
		t.Errorf("inside link: %q %v", l, err)
	}
	for _, n := range []string{"bad", "bad2"} {
		if _, err := os.Lstat(filepath.Join(out3, "t/bin", n)); err == nil {
			t.Errorf("link %s out of the folder was made", n)
		}
	}
}

func TestSwListTools(t *testing.T) {
	old := swToolsDir
	swToolsDir = t.TempDir()
	defer func() { swToolsDir = old }()
	os.MkdirAll(filepath.Join(swToolsDir, "capa"), 0o755)
	os.WriteFile(filepath.Join(swToolsDir, "capa", ".servermgr.json"), []byte(`{"name":"capa","source":"https://x/capa-v1-linux.zip"}`), 0o644)
	os.MkdirAll(filepath.Join(swToolsDir, "other"), 0o755) // not ours: no marker
	if _, err := uninstallSoftware(context.Background(), map[string]string{"id": "tool:other"}, t.Logf); err == nil {
		t.Error("removed a folder servermgr did not install")
	}
	if _, err := uninstallSoftware(context.Background(), map[string]string{"id": "pkg:openssh-server"}, t.Logf); err == nil {
		t.Error("openssh-server could be removed")
	}
	if _, err := uninstallSoftware(context.Background(), map[string]string{"id": "tool:../etc"}, t.Logf); err == nil {
		t.Error("a path was accepted as a tool")
	}
}
