package api

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

func TestProbeable(t *testing.T) {
	got := probeable([]string{"8.8.8.8", "127.0.0.1", "169.254.1.1", "not-an-ip", "10.0.1.50", "192.168.5.5", "172.20.0.9"})
	if len(got) != 2 || got[0].String() != "10.0.1.50" || got[1].String() != "192.168.5.5" {
		t.Errorf("probeable = %v, want the first two private addresses", got)
	}
	if len(probeable([]string{"1.1.1.1", "2001:4860::1"})) != 0 {
		t.Error("public addresses must not be probed")
	}
}

func TestAppPatterns(t *testing.T) {
	cases := map[string]string{
		"server: nginx\n<title>pfSense - Login</title>": "pfsense",
		"server: nginx\n<title>Welcome</title>":         "nginx",
		"<title>TrueNAS - 10.0.1.5</title>":             "truenas",
		"<title>Technitium DNS Server</title>":          "technitium",
		"5.5.5-10.11.6-MariaDB-0+deb12u1":               "mariadb",
		"<title>Login</title>":                          "",
	}
	for in, want := range cases {
		if got := matchKey(appPatterns, in); got != want {
			t.Errorf("matchKey(%q) = %q, want %q", in, got, want)
		}
	}
	if got := matchKey(sshOS, "SSH-2.0-OpenSSH_9.2p1 Debian-2+deb12u3"); got != "debian" {
		t.Errorf("ssh debian = %q", got)
	}
	if got := matchKey(sshOS, "SSH-2.0-OpenSSH_for_Windows_9.5"); got != "windows" {
		t.Errorf("ssh windows = %q", got)
	}
}

func hostPort(t *testing.T, raw string) (string, int) {
	u, _ := url.Parse(raw)
	h, p, _ := net.SplitHostPort(u.Host)
	n, _ := strconv.Atoi(p)
	return h, n
}

func TestProbeHTTP(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<title>TrueNAS</title>"))
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			http.Redirect(w, r, "/ui/", http.StatusFound)
		case "/ui/":
			_, _ = w.Write([]byte("<html><title>Pi-hole Admin</title></html>"))
		case "/away":
			http.Redirect(w, r, other.URL+"/", http.StatusFound)
		}
	}))
	defer srv.Close()
	h, p := hostPort(t, srv.URL)
	if got := probeHTTP(context.Background(), "http", h, p); got != "pihole" {
		t.Errorf("same-host redirect: got %q, want pihole", got)
	}

	// A redirect to another host is not followed.
	away := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/", http.StatusFound)
	}))
	defer away.Close()
	h, p = hostPort(t, away.URL)
	if got := probeHTTP(context.Background(), "http", h, p); got == "truenas" {
		t.Error("followed a redirect to another host")
	}
}

func TestReadGreeting(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			_, _ = c.Write([]byte("SSH-2.0-OpenSSH_9.6p1 Ubuntu-3ubuntu13.5\r\nmore"))
			c.Close()
		}
	}()
	_, p, _ := net.SplitHostPort(ln.Addr().String())
	n, _ := strconv.Atoi(p)
	line := readGreeting(context.Background(), "127.0.0.1", n)
	if matchKey(sshOS, line) != "ubuntu" {
		t.Errorf("greeting %q", line)
	}
}
