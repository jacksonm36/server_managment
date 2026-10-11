package catalog

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func zipOf(t *testing.T, files ...string) []byte {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, f := range files {
		w, _ := z.Create(f)
		w.Write([]byte("x"))
	}
	z.Close()
	return b.Bytes()
}

func tgzOf(t *testing.T, files ...string) []byte {
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		tw.WriteHeader(&tar.Header{Name: f, Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
		tw.Write([]byte("x"))
	}
	tw.Close()
	gz.Close()
	return b.Bytes()
}

func TestWebRoot(t *testing.T) {
	cases := []struct {
		names []string
		strip int
		into  string
		err   string
	}{
		{[]string{"app-1.0/index.php", "app-1.0/lib/a.php", "app-1.0/vendor/x/index.php"}, 1, "public", ""},
		{[]string{"index.php", "core/a.php"}, 0, "public", ""},
		{[]string{"oc-4/upload/index.php", "oc-4/README.md"}, 2, "public", ""},
		{[]string{"inv/public/index.php", "inv/vendor/autoload.php", "inv/artisan", "inv/composer.json"}, 1, ".", ""},
		{[]string{"x/index.php", "x/composer.json"}, 0, "", "composer install"},
		{[]string{"x/readme.txt"}, 0, "", "no index.php"},
	}
	for _, c := range cases {
		strip, into, err := webRoot(c.names, false)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%v: want error %q, got %v", c.names, c.err, err)
			}
			continue
		}
		if err != nil || strip != c.strip || into != c.into {
			t.Errorf("%v: got strip=%d into=%q err=%v, want %d %q", c.names, strip, into, err, c.strip, c.into)
		}
	}
}

func TestPinGitHubAndURL(t *testing.T) {
	zipBody := zipOf(t, "blog-2.1/index.php", "blog-2.1/inc/a.php")
	tgzBody := tgzOf(t, "wiki/index.php", "wiki/vendor/autoload.php", "wiki/composer.json")
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/repos/acme/blog/releases", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{"tag_name": "v3.0.0-beta1", "prerelease": false, "assets": []map[string]string{{"name": "blog-3.0.0-beta1.zip", "browser_download_url": srv.URL + "/dl/beta.zip"}}},
			{"tag_name": "v2.1.0", "assets": []map[string]string{
				{"name": "blog-2.1.0-src.tar.gz", "browser_download_url": srv.URL + "/dl/src.tar.gz"},
				{"name": "blog-2.1.0.zip", "browser_download_url": srv.URL + "/dl/blog-2.1.0.zip"},
			}},
		})
	})
	mux.HandleFunc("/dl/blog-2.1.0.zip", func(w http.ResponseWriter, r *http.Request) { w.Write(zipBody) })
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/files/wiki-2026-04-01.tgz", http.StatusFound)
	})
	mux.HandleFunc("/files/wiki-2026-04-01.tgz", func(w http.ResponseWriter, r *http.Request) { w.Write(tgzBody) })
	srv = httptest.NewServer(mux)
	defer srv.Close()

	r := &Refresher{GitHubAPI: srv.URL, AllowHTTP: true}
	p, err := r.Pin(context.Background(), &Source{ID: "blog", GitHub: "acme/blog", Asset: `^blog-[0-9.]+\.zip$`})
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(zipBody)
	if p.Version != "2.1.0" || p.SHA256 != hex.EncodeToString(want[:]) || p.Strip != 1 || p.Into != "public" || !strings.HasSuffix(p.URL, "/dl/blog-2.1.0.zip") {
		t.Fatalf("github pin: %+v", p)
	}

	p, err = r.Pin(context.Background(), &Source{ID: "wiki", URL: srv.URL + "/latest.tgz", VersionRe: `wiki-([0-9-]+)\.tgz`})
	if err == nil {
		t.Fatal("expected 404 for /latest.tgz")
	}
	p, err = r.Pin(context.Background(), &Source{ID: "wiki", URL: srv.URL + "/latest", VersionRe: `wiki-([0-9-]+)\.tgz`})
	if err != nil {
		t.Fatal(err)
	}
	if p.Version != "2026-04-01" || !strings.HasSuffix(p.URL, "/files/wiki-2026-04-01.tgz") || p.Strip != 1 {
		t.Fatalf("url pin: %+v", p)
	}

	if _, err := r.Pin(context.Background(), &Source{ID: "nope", GitHub: "acme/blog", Asset: `\.rpm$`}); err == nil {
		t.Fatal("expected no matching asset")
	}
}

func TestSourcesLoad(t *testing.T) {
	ids := map[string]bool{}
	for _, s := range Sources() {
		if ids[s.ID] {
			t.Fatalf("duplicate %s", s.ID)
		}
		ids[s.ID] = true
	}
	// A pinned source shows up in the catalog as a wizard install.
	if len(Sources()) == 0 {
		t.Skip("no sources")
	}
	s := Sources()[0]
	SetPinned(map[string]*Pinned{s.ID: {Version: "1.0", URL: "https://example.org/a.zip", SHA256: strings.Repeat("a", 64), Strip: 1, Into: "public"}})
	defer SetPinned(map[string]*Pinned{})
	a, ok := Get(s.ID)
	if !ok || !a.Wizard || a.Kind != "php" || len(a.Install) != 1 || StepsJSON(a.Install) == "" {
		t.Fatalf("pinned source not installable: %+v", a)
	}
}

func TestResolveWebFallback(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/forum/releases", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusForbidden)
	})
	mux.HandleFunc("/acme/forum/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/acme/forum/releases/tag/v4.2.1", http.StatusFound)
	})
	mux.HandleFunc("/acme/forum/releases/tag/v4.2.1", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("/acme/forum/releases/expanded_assets/v4.2.1", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<a href="/acme/forum/releases/download/v4.2.1/forum-4.2.1.tar.gz">x</a><a href="/acme/forum/releases/download/v4.2.1/forum-4.2.1.zip">y</a>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	r := &Refresher{GitHubAPI: srv.URL, GitHubWeb: srv.URL}
	v, u, err := r.resolve(context.Background(), &Source{GitHub: "acme/forum", Asset: `\.zip$`})
	if err != nil || v != "4.2.1" || u != "https://github.com/acme/forum/releases/download/v4.2.1/forum-4.2.1.zip" {
		t.Fatalf("got %q %q %v", v, u, err)
	}
}

func TestScrape(t *testing.T) {
	zipBody := zipOf(t, "phpBB3/index.php")
	mux := http.NewServeMux()
	mux.HandleFunc("/pub/release/3.3/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pub/release/3.3/" {
			w.Write(zipBody)
			return
		}
		w.Write([]byte(`<a href="3.3.9/">3.3.9/</a> <a href="3.3.15/">3.3.15/</a> <a href="3.3.16-RC1/">x</a>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	r := &Refresher{AllowHTTP: true}
	p, err := r.Pin(context.Background(), &Source{ID: "phpbb",
		Scrape: []ScrapeStep{{URL: srv.URL + "/pub/release/3.3/", Re: `href="(3\.3\.\d+[^/"]*)/"`}},
		URL:    srv.URL + "/pub/release/3.3/{0}/phpBB-{0}.zip"})
	if err != nil || p.Version != "3.3.15" || !strings.HasSuffix(p.URL, "/3.3.15/phpBB-3.3.15.zip") || p.Strip != 1 {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestAppLayoutPublicFolder(t *testing.T) {
	s := &Source{ID: "rss", Public: "p", AllowNoVendor: true}
	strip, into, root, err := appLayout([]string{"FreshRSS-1.2/index.php", "FreshRSS-1.2/p/index.php", "FreshRSS-1.2/p/i/index.php", "FreshRSS-1.2/data/x"}, s)
	if err != nil || strip != 1 || into != "app" || root != "app/p" {
		t.Errorf("got strip=%d into=%q root=%q err=%v", strip, into, root, err)
	}
	if strip, _, _, err := appLayout([]string{"p/index.php", "lib/a.php"}, s); err != nil || strip != 0 {
		t.Errorf("flat archive: strip=%d err=%v", strip, err)
	}
	if _, _, _, err := appLayout([]string{"x/index.php"}, s); err == nil {
		t.Error("archive without p/index.php accepted")
	}
	// Without a public folder the regular detection applies.
	if _, into, root, err := appLayout([]string{"w/index.php"}, &Source{}); err != nil || into != "public" || root != "" {
		t.Errorf("plain: into=%q root=%q err=%v", into, root, err)
	}
}

func TestSourceSteps(t *testing.T) {
	p := &Pinned{URL: "https://x/a.zip", SHA256: "ab", Strip: 1, Into: "app", WebRoot: "app/p"}
	steps := sourceSteps(&Source{Deny: []string{"data/"}}, p)
	if len(steps) != 2 || steps[0]["nginx"] == nil || steps[1]["fetch"] == nil {
		t.Fatalf("steps = %v", steps)
	}
	if steps := sourceSteps(&Source{}, &Pinned{Into: "public"}); len(steps) != 1 {
		t.Errorf("plain app got an nginx step: %v", steps)
	}
	for d, ok := range map[string]bool{"data/": true, "install.php": true, "user/*.md,twig": true, "../x/": false, ".git/": false, "a b": false, "x/*.p|q": false} {
		if validDeny(d) != ok {
			t.Errorf("validDeny(%q) = %v", d, !ok)
		}
	}
}
