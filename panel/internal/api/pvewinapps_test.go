package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestWinAppCatalog: every app has one source, a way to verify it and a
// known category.
func TestWinAppCatalog(t *testing.T) {
	seen := map[string]bool{}
	for _, x := range winApps {
		if seen[x.ID] || !regexp.MustCompile(`^[a-z0-9]+$`).MatchString(x.ID) {
			t.Errorf("%s: bad or repeated id", x.ID)
		}
		seen[x.ID] = true
		if x.Group == "" || !slices.Contains([]string{"msi", "exe", "zip"}, x.Kind) {
			t.Errorf("%s: category %q kind %q", x.ID, x.Group, x.Kind)
		}
		n := 0
		for _, src := range []string{x.URL, x.Repo, x.Dir, x.Page} {
			if src != "" {
				n++
			}
		}
		if n != 1 || (x.URL == "" && x.Match == "") {
			t.Errorf("%s: needs exactly one source", x.ID)
		}
		regexp.MustCompile(x.Match)
		// Only GitHub releases carry a SHA-256; everything else needs a signer.
		if x.Repo == "" && x.Signer == "" {
			t.Errorf("%s: nothing to verify the download with", x.ID)
		}
		if x.Kind == "zip" && x.Signer != "" && x.Exe == "" {
			t.Errorf("%s: a zip's signer is checked on its exe", x.ID)
		}
		for _, d := range x.Needs {
			if winAppByID(d) == nil {
				t.Errorf("%s needs unknown %s", x.ID, d)
			}
		}
	}
}

func TestWinAppResolve(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<a href="https://www.python.org/ftp/python/3.14.0/python-3.14.0-amd64.exe">x</a>
			<a href="https://www.python.org/ftp/python/3.13.9/python-3.13.9-amd64.exe">y</a>`))
	}))
	defer srv.Close()
	saved := appHTTP
	defer func() { appHTTP = saved }()
	appHTTP = srv.Client()
	py := *winAppByID("python")
	py.Page = srv.URL + "/downloads/windows/"
	j, err := py.resolve(context.Background())
	if err != nil || j.URL != "https://www.python.org/ftp/python/3.14.0/python-3.14.0-amd64.exe" || j.Signer == "" {
		t.Fatalf("page: %+v %v", j, err)
	}

	// Dependencies come first, once; resolved downloads are reused.
	t.Cleanup(func() { appCache.Lock(); appCache.m = map[string]appCached{}; appCache.Unlock() })
	appCache.Lock()
	for _, id := range []string{"temurin", "ghidra", "7zip"} {
		appCache.m[id] = appCached{&winAppJob{ID: id, URL: "https://example.com/" + id}, time.Now()}
	}
	appCache.Unlock()
	jobs, err := resolveWinApps(context.Background(), []string{"7zip", "ghidra", "temurin"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, j := range jobs {
		ids = append(ids, j.ID)
	}
	if !slices.Equal(ids, []string{"7zip", "temurin", "ghidra"}) {
		t.Errorf("order = %v", ids)
	}
	if _, err := resolveWinApps(context.Background(), []string{"ghidra", "ghidra"}); err == nil {
		t.Error("repeated app accepted")
	}
}

// A latest release without the file (source only, or no SHA-256): the
// newest recent release that has it.
func TestWinAppGitHubFallback(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const a = `"https://github.com/VirusTotal/yara/releases/download/`
		switch {
		case strings.HasSuffix(r.URL.Path, "/latest"):
			w.Write([]byte(`{"assets":[]}`))
		default:
			w.Write([]byte(`[{"prerelease":true,"assets":[{"name":"yara-v5.0.0-win64.zip","browser_download_url":` + a + `v5/yara-v5.0.0-win64.zip","digest":"sha256:` + strings.Repeat("a", 64) + `"}]},
				{"assets":[{"name":"yara-v4.5.5-win64.zip","browser_download_url":` + a + `v4.5.5/yara-v4.5.5-win64.zip"}]},
				{"assets":[{"name":"yara-v4.5.4-2326-win64.zip","browser_download_url":` + a + `v4.5.4/yara-v4.5.4-2326-win64.zip","digest":"sha256:` + strings.Repeat("b", 64) + `"}]}]`))
		}
	}))
	defer srv.Close()
	savedHTTP, savedAPI := appHTTP, ghAPI
	defer func() { appHTTP, ghAPI = savedHTTP, savedAPI }()
	appHTTP, ghAPI = srv.Client(), srv.URL
	j, err := winAppByID("yara").resolve(context.Background())
	if err != nil || !strings.HasSuffix(j.URL, "v4.5.4-2326-win64.zip") || j.SHA256 != strings.Repeat("b", 64) {
		t.Fatalf("got %+v %v", j, err)
	}
}

// TestWinAppsLive resolves every download for real; run it where the
// vendors are reachable: SM_LIVE_APPS=1 go test -run WinAppsLive ./panel/internal/api/
func TestWinAppsLive(t *testing.T) {
	if os.Getenv("SM_LIVE_APPS") == "" {
		t.Skip("set SM_LIVE_APPS=1")
	}
	for _, x := range winApps {
		j, err := x.resolve(context.Background())
		if err != nil {
			t.Errorf("%s: %v", x.ID, err)
			continue
		}
		t.Logf("%-12s %s sha256=%v", x.ID, j.URL, j.SHA256 != "")
	}
}

// The Windows agent installs programs with its own copy of apps.ps1.
func TestWinAppsScriptCopies(t *testing.T) {
	agent, err := os.ReadFile("../../../agent/internal/winops/apps.ps1")
	if err != nil {
		t.Fatal(err)
	}
	panel, err := windowsFiles.ReadFile("windows/apps.ps1")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(agent, panel) {
		t.Fatal("agent/internal/winops/apps.ps1 and panel/internal/api/windows/apps.ps1 differ; copy one over the other")
	}
}
