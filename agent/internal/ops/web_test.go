package ops

import (
	"strings"
	"testing"
)

func TestSiteUser(t *testing.T) {
	if got := siteUser("example.com"); got != "s_example_com" {
		t.Fatalf("got %q", got)
	}
	long := siteUser("a-very-long-subdomain.of.some-example-domain.com")
	if len(long) > 32 || !strings.HasPrefix(long, "s_") || !unixUser.MatchString(long) {
		t.Fatalf("bad long user %q", long)
	}
}

func TestValidDomain(t *testing.T) {
	for _, d := range []string{"example.com", "a.b.example.co.uk", "shop.lan", "xn--bcher-kva.de"} {
		if !validDomain(d) {
			t.Errorf("%q should be valid", d)
		}
	}
	for _, d := range []string{"", "example", "-a.com", "a..com", "a.com;", "A.COM", "a.com/x", "a .com", "*.a.com"} {
		if validDomain(d) {
			t.Errorf("%q should be invalid", d)
		}
	}
}

func TestRenderNginx(t *testing.T) {
	for _, s := range []*Site{
		{Domain: "static.test", Type: "static", Root: "/srv/sites/static.test/public"},
		{Domain: "php.test", Aliases: []string{"www.php.test"}, Type: "php", PHPVersion: "8.4", Root: "/srv/sites/php.test/public"},
		{Domain: "app.test", Type: "proxy", ProxyTarget: "http://127.0.0.1:3000", Root: "/srv/sites/app.test/public"},
	} {
		b, err := renderNginx(s)
		if err != nil {
			t.Fatal(err)
		}
		conf := string(b)
		if strings.Count(conf, "{") != strings.Count(conf, "}") {
			t.Errorf("%s: unbalanced braces:\n%s", s.Domain, conf)
		}
		if !strings.Contains(conf, "server_name "+strings.Join(s.names(), " ")+";") {
			t.Errorf("%s: missing server_name", s.Domain)
		}
		if !strings.Contains(conf, "acme-challenge") {
			t.Errorf("%s: missing ACME location", s.Domain)
		}
		switch s.Type {
		case "php":
			if !strings.Contains(conf, "fastcgi_pass unix:/run/php/servermgr-php.test.sock;") {
				t.Errorf("php: missing fastcgi_pass:\n%s", conf)
			}
			if !strings.Contains(conf, "sqlite3?|db|sql") {
				t.Errorf("php: missing data-file deny:\n%s", conf)
			}
		case "proxy":
			if !strings.Contains(conf, "proxy_pass http://127.0.0.1:3000;") {
				t.Errorf("proxy: missing proxy_pass:\n%s", conf)
			}
		}
	}
}

func TestParseSiteParamsRejectsInjection(t *testing.T) {
	s := &Site{Domain: "a.test"}
	bad := []map[string]string{
		{"type": "proxy", "proxy_target": "http://127.0.0.1:3000; } server {"},
		{"type": "proxy", "proxy_target": "file:///etc/passwd"},
		{"type": "static", "aliases": "ok.test evil;.test"},
		{"type": "cgi"},
	}
	for _, p := range bad {
		if err := parseSiteParams(p, s); err == nil {
			t.Errorf("accepted %v", p)
		}
	}
}

func TestIPv6ListenRegex(t *testing.T) {
	in := "server {\n\tlisten 80 default_server;\n\tlisten [::]:80 default_server;\n\t# listen [::]:443 ssl default_server;\n}\n"
	out := string(ipv6Listen.ReplaceAll([]byte(in), []byte("${1}# ${2} # disabled by servermgr: this host has no IPv6")))
	if !strings.Contains(out, "\t# listen [::]:80 default_server; # disabled") || !strings.Contains(out, "\tlisten 80 default_server;") {
		t.Fatalf("unexpected:\n%s", out)
	}
	if strings.Count(out, "disabled by servermgr") != 1 {
		t.Fatalf("commented lines must stay untouched:\n%s", out)
	}
}
