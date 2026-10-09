package ops

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDumpNginx writes rendered vhosts to $SM_NGINX_DUMP so they can be
// checked with a real `nginx -t` (see scripts/check-nginx.sh).
func TestDumpNginx(t *testing.T) {
	dir := os.Getenv("SM_NGINX_DUMP")
	if dir == "" {
		t.Skip("SM_NGINX_DUMP not set")
	}
	sites := []*Site{
		{Domain: "static.test", Type: "static", Root: "/srv/sites/static.test/public"},
		{Domain: "php.test", Aliases: []string{"www.php.test"}, Type: "php", PHPVersion: "8.4", Root: "/srv/sites/php.test/public"},
		{Domain: "app.test", Type: "proxy", ProxyTarget: "http://127.0.0.1:3000", Root: "/srv/sites/app.test/public"},
	}
	for _, tls := range []struct {
		name  string
		force bool
	}{{"plain", false}, {"tls", true}, {"tls-noforce", false}} {
		for _, s := range sites {
			c := *s
			c.ForceHTTPS = tls.force
			hasCert := tls.name != "plain"
			b, err := renderNginxWith(&c, hasCert)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, tls.name+"-"+c.Domain+".conf"), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}
