package catalog

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Source describes a PHP app whose release the panel looks up and pins
// itself ("Refresh catalog"): it downloads the latest release, records its
// sha256 and finds the web root inside the archive. Agents then install
// exactly that file, verified against the recorded checksum.
type Source struct {
	ID            string `yaml:"id" json:"id"`
	Name          string `yaml:"name" json:"name"`
	Category      string `yaml:"category" json:"category"`
	Description   string `yaml:"description" json:"description"`
	Homepage      string `yaml:"homepage" json:"homepage"`
	PHPMin        string `yaml:"php_min" json:"php_min"`
	Database      bool   `yaml:"database" json:"database"`
	InstallerPath string `yaml:"installer_path" json:"installer_path"`
	Notes         string `yaml:"notes" json:"notes,omitempty"`
	MinRAMMB      int    `yaml:"min_ram_mb" json:"min_ram_mb,omitempty"`

	// One of: GitHub releases (Asset matches the release file; empty means
	// the tag's source tarball) or a fixed download URL.
	GitHub string `yaml:"github" json:"github,omitempty"`
	Asset  string `yaml:"asset" json:"asset,omitempty"`
	URL    string `yaml:"url" json:"url,omitempty"`
	// VersionRe extracts the version from the final download URL (URL sources).
	VersionRe string `yaml:"version_re" json:"version_re,omitempty"`
	// Scrape finds the latest version on download index pages: each step's
	// regex captures a value (the highest version wins) that later step URLs
	// and URL use as {0}, {1}, ...; the last capture is the version.
	Scrape []ScrapeStep `yaml:"scrape" json:"-"`
	// AllowNoVendor accepts a composer.json without vendor/ (the project
	// bundles its libraries elsewhere).
	AllowNoVendor bool `yaml:"allow_no_vendor" json:"-"`

	// Public is the app's own public folder (e.g. "p"): the release goes to
	// the site's app/ folder and only app/<public> is served.
	Public string `yaml:"public" json:"public,omitempty"`
	// Deny lists paths under the web root that nginx must not serve, for
	// apps that rely on .htaccess: "data/" (folder), "install.php" (file) or
	// "user/*.md,twig" (files by extension).
	Deny []string `yaml:"deny" json:"deny,omitempty"`
	// Protect turns on the site's password protection by default (apps
	// that start with a well-known default login).
	Protect bool `yaml:"protect" json:"protect,omitempty"`
}

type ScrapeStep struct {
	URL string `yaml:"url"`
	Re  string `yaml:"re"`
}

// Pinned is the result of refreshing one source.
type Pinned struct {
	Version   string    `json:"version,omitempty"`
	URL       string    `json:"url,omitempty"`
	SHA256    string    `json:"sha256,omitempty"`
	Strip     int       `json:"strip"`
	Into      string    `json:"into,omitempty"`
	WebRoot   string    `json:"web_root,omitempty"` // relative to the site home when not public/
	Size      int64     `json:"size,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
	Error     string    `json:"error,omitempty"`
}

//go:embed sources.yaml
var sourcesYAML []byte

var (
	sources   []*Source
	pinnedMu  sync.RWMutex
	pinnedMap = map[string]*Pinned{}
)

func loadSources() {
	if err := yaml.Unmarshal(sourcesYAML, &sources); err != nil {
		panic("catalog sources.yaml: " + err.Error())
	}
	seen := map[string]bool{}
	for _, s := range sources {
		switch {
		case !idRe.MatchString(s.ID):
			panic("catalog sources.yaml: bad id " + s.ID)
		case seen[s.ID] || apps[s.ID] != nil:
			panic("catalog sources.yaml: duplicate id " + s.ID)
		case (s.GitHub == "") == (s.URL == ""):
			panic("catalog sources.yaml: " + s.ID + " needs exactly one of github or url")
		case s.Name == "" || s.Category == "":
			panic("catalog sources.yaml: " + s.ID + " needs name and category")
		}
		if s.Asset != "" {
			regexp.MustCompile(s.Asset)
		}
		if s.VersionRe != "" {
			regexp.MustCompile(s.VersionRe)
		}
		for _, st := range s.Scrape {
			if regexp.MustCompile(st.Re).NumSubexp() != 1 {
				panic("catalog sources.yaml: " + s.ID + " scrape regex needs one capture group")
			}
		}
		if s.Public != "" && !denyPathRe.MatchString(s.Public) {
			panic("catalog sources.yaml: " + s.ID + " bad public folder")
		}
		for _, d := range s.Deny {
			if !validDeny(d) {
				panic("catalog sources.yaml: " + s.ID + " bad deny entry " + d)
			}
		}
		if s.InstallerPath == "" {
			s.InstallerPath = "/"
		}
		if s.PHPMin == "" {
			s.PHPMin = "8.1"
		}
		seen[s.ID] = true
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Name < sources[j].Name })
}

var (
	idRe       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)
	denyPathRe = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*(/[A-Za-z0-9_-][A-Za-z0-9._-]*)*$`)
	denyExtRe  = regexp.MustCompile(`^[A-Za-z0-9]+(,[A-Za-z0-9]+)*$`)
)

// validDeny mirrors the agent's check of deny entries.
func validDeny(d string) bool {
	if dir, exts, ok := strings.Cut(d, "/*."); ok {
		return denyPathRe.MatchString(dir) && denyExtRe.MatchString(exts)
	}
	return denyPathRe.MatchString(strings.TrimSuffix(d, "/"))
}

// Sources lists the refreshable apps.
func Sources() []*Source { return sources }

// SetPinned installs refresh results (loaded from the database at start-up).
func SetPinned(m map[string]*Pinned) {
	pinnedMu.Lock()
	defer pinnedMu.Unlock()
	pinnedMap = m
}

// PinnedState returns a copy of the refresh results.
func PinnedState() map[string]*Pinned {
	pinnedMu.RLock()
	defer pinnedMu.RUnlock()
	out := make(map[string]*Pinned, len(pinnedMap))
	for k, v := range pinnedMap {
		c := *v
		out[k] = &c
	}
	return out
}

// sourceApp turns a pinned source into an installable app: create the site
// (and database), fetch the verified release, then the user finishes the
// app's own installer in the browser.
func sourceApp(s *Source, p *Pinned) *App {
	notes := s.Notes
	if s.Database {
		notes = strings.TrimSpace("In the installer use database host localhost with the database name, user and password shown here. " + notes)
	}
	return &App{
		ID: s.ID, Name: s.Name, Category: s.Category, Description: s.Description, Version: p.Version,
		PHPMin: s.PHPMin, Database: s.Database, AdminPath: s.InstallerPath, Homepage: s.Homepage,
		Notes: notes, Kind: "php", MinRAMMB: s.MinRAMMB, Wizard: true, Protect: s.Protect,
		Install: sourceSteps(s, p),
	}
}

func sourceSteps(s *Source, p *Pinned) []map[string]any {
	var steps []map[string]any
	if p.WebRoot != "" || len(s.Deny) > 0 {
		// Agents that predate this step fail it, so the install rolls back
		// instead of serving the app's data folders.
		steps = append(steps, map[string]any{"nginx": map[string]any{"web_root": p.WebRoot, "deny": s.Deny}})
	}
	return append(steps, map[string]any{"fetch": map[string]any{"url": p.URL, "sha256": p.SHA256, "strip": p.Strip, "into": p.Into}})
}

// ---- refreshing ----

// Refresher downloads and inspects releases. The zero value uses GitHub's
// public API and a 10-minute timeout per download.
type Refresher struct {
	Client    *http.Client
	GitHubAPI string // default https://api.github.com
	GitHubWeb string // default https://github.com (fallback when the API rate limit is hit)
	MaxBytes  int64  // default 400 MiB
	Token     string // optional GitHub token (higher rate limit)
	AllowHTTP bool   // tests only: agents refuse non-https downloads
}

// Refresh pins every source (4 at a time) and returns the results by id.
// prev results are kept for sources that fail this time, with the error.
func (r *Refresher) Refresh(ctx context.Context, prev map[string]*Pinned, progress func(done, total int)) map[string]*Pinned {
	out := map[string]*Pinned{}
	var mu sync.Mutex
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	done := 0
	for _, s := range sources {
		wg.Add(1)
		sem <- struct{}{}
		go func(s *Source) {
			defer wg.Done()
			defer func() { <-sem }()
			p, err := r.Pin(ctx, s)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				p = &Pinned{CheckedAt: time.Now().UTC(), Error: err.Error()}
				if old := prev[s.ID]; old != nil && old.SHA256 != "" {
					// Keep the last good release installable.
					c := *old
					c.Error, c.CheckedAt = err.Error(), p.CheckedAt
					p = &c
				}
			}
			out[s.ID] = p
			done++
			if progress != nil {
				progress(done, len(sources))
			}
		}(s)
	}
	wg.Wait()
	return out
}

// Pin resolves, downloads and inspects one source.
func (r *Refresher) Pin(ctx context.Context, s *Source) (*Pinned, error) {
	version, url, err := r.resolve(ctx, s)
	if err != nil {
		return nil, err
	}
	f, sum, size, final, err := r.download(ctx, url)
	if err != nil {
		return nil, err
	}
	defer os.Remove(f)
	if s.URL != "" && len(s.Scrape) == 0 {
		// Pin the versioned URL a "latest" link redirected to.
		url = final
		if version == "" && s.VersionRe != "" {
			if m := regexp.MustCompile(s.VersionRe).FindStringSubmatch(final); len(m) > 1 {
				version = m[1]
			}
		}
		if version == "" {
			version = time.Now().UTC().Format("2006.01.02")
		}
	}
	if !r.AllowHTTP && !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("download %s is not https; agents only download over https", url)
	}
	if archiveKind(url) == "" {
		return nil, fmt.Errorf("download %s is not a .zip, .tar.gz, .tgz or .tar.bz2 file", url)
	}
	names, err := listArchive(f, url)
	if err != nil {
		return nil, fmt.Errorf("reading the archive: %w", err)
	}
	strip, into, webroot, err := appLayout(names, s)
	if err != nil {
		return nil, err
	}
	return &Pinned{Version: version, URL: url, SHA256: sum, Strip: strip, Into: into, WebRoot: webroot, Size: size, CheckedAt: time.Now().UTC()}, nil
}

func (r *Refresher) client() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return &http.Client{Timeout: 10 * time.Minute}
}

func (r *Refresher) resolve(ctx context.Context, s *Source) (version, url string, err error) {
	if len(s.Scrape) > 0 {
		return r.scrape(ctx, s)
	}
	if s.URL != "" {
		return "", s.URL, nil
	}
	api := r.GitHubAPI
	if api == "" {
		api = "https://api.github.com"
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, api+"/repos/"+s.GitHub+"/releases?per_page=15", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	if r.Token != "" {
		req.Header.Set("Authorization", "Bearer "+r.Token)
	}
	resp, err := r.client().Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		// Anonymous API calls are limited to 60 an hour; the release pages are not.
		return r.resolveWeb(ctx, s)
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("GitHub %s: %s", s.GitHub, resp.Status)
	}
	var rels []struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&rels); err != nil {
		return "", "", err
	}
	var re *regexp.Regexp
	if s.Asset != "" {
		re = regexp.MustCompile(s.Asset)
	}
	for _, rel := range rels {
		if rel.Draft || rel.Prerelease || preTag.MatchString(rel.Tag) {
			continue
		}
		v := strings.TrimPrefix(strings.TrimPrefix(rel.Tag, "v"), "V")
		if re == nil {
			return v, "https://github.com/" + s.GitHub + "/archive/refs/tags/" + rel.Tag + ".tar.gz", nil
		}
		for _, a := range rel.Assets {
			if re.MatchString(a.Name) {
				return v, a.URL, nil
			}
		}
	}
	if re != nil {
		return "", "", fmt.Errorf("no stable release of %s has a file matching %s", s.GitHub, s.Asset)
	}
	return "", "", fmt.Errorf("%s has no stable release", s.GitHub)
}

func (r *Refresher) scrape(ctx context.Context, s *Source) (version, url string, err error) {
	var caps []string
	fill := func(t string) string {
		for i, c := range caps {
			t = strings.ReplaceAll(t, "{"+strconv.Itoa(i)+"}", c)
		}
		return t
	}
	for _, st := range s.Scrape {
		u := fill(st.URL)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		req.Header.Set("User-Agent", "servermgr-catalog")
		resp, err := r.client().Do(req)
		if err != nil {
			return "", "", err
		}
		page, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", "", fmt.Errorf("%s: %s", u, resp.Status)
		}
		best := ""
		for _, m := range regexp.MustCompile(st.Re).FindAllStringSubmatch(string(page), -1) {
			if !preTag.MatchString(m[1]) && (best == "" || versionLess(best, m[1])) {
				best = m[1]
			}
		}
		if best == "" {
			return "", "", fmt.Errorf("%s: no version found", u)
		}
		caps = append(caps, best)
	}
	return caps[len(caps)-1], fill(s.URL), nil
}

// versionLess compares dotted versions numerically (1.10 > 1.9).
func versionLess(a, b string) bool {
	pa, pb := numRe.FindAllString(a, -1), numRe.FindAllString(b, -1)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x < y
		}
	}
	return len(pa) < len(pb)
}

var numRe = regexp.MustCompile(`\d+`)

// resolveWeb finds the latest release from github.com's release pages.
func (r *Refresher) resolveWeb(ctx context.Context, s *Source) (version, url string, err error) {
	web := r.GitHubWeb
	if web == "" {
		web = "https://github.com"
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, web+"/"+s.GitHub+"/releases/latest", nil)
	resp, err := r.client().Do(req)
	if err != nil {
		return "", "", err
	}
	resp.Body.Close()
	tag := ""
	if i := strings.LastIndex(resp.Request.URL.Path, "/releases/tag/"); i >= 0 {
		tag = resp.Request.URL.Path[i+len("/releases/tag/"):]
	}
	if resp.StatusCode != http.StatusOK || tag == "" {
		return "", "", fmt.Errorf("GitHub %s: no latest release (%s)", s.GitHub, resp.Status)
	}
	v := strings.TrimPrefix(strings.TrimPrefix(tag, "v"), "V")
	if s.Asset == "" {
		return v, "https://github.com/" + s.GitHub + "/archive/refs/tags/" + tag + ".tar.gz", nil
	}
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, web+"/"+s.GitHub+"/releases/expanded_assets/"+tag, nil)
	resp, err = r.client().Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	page, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	re := regexp.MustCompile(s.Asset)
	for _, m := range assetHref.FindAllStringSubmatch(string(page), -1) {
		if strings.HasPrefix(m[1], "/"+s.GitHub+"/releases/download/") && re.MatchString(path.Base(m[1])) {
			return v, "https://github.com" + m[1], nil
		}
	}
	return "", "", fmt.Errorf("release %s of %s has no file matching %s", tag, s.GitHub, s.Asset)
}

var assetHref = regexp.MustCompile(`href="(/[^"]+/releases/download/[^"]+)"`)

var preTag = regexp.MustCompile(`(?i)(alpha|beta|rc|dev|pre|nightly|preview)`)

func (r *Refresher) download(ctx context.Context, url string) (file, sum string, size int64, final string, err error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("User-Agent", "servermgr-catalog")
	resp, err := r.client().Do(req)
	if err != nil {
		return "", "", 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", 0, "", fmt.Errorf("download %s: %s", url, resp.Status)
	}
	max := r.MaxBytes
	if max == 0 {
		max = 400 << 20
	}
	f, err := os.CreateTemp("", "sm-catalog-*")
	if err != nil {
		return "", "", 0, "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, max+1))
	f.Close()
	if err == nil && n > max {
		err = fmt.Errorf("download is larger than %d MB", max>>20)
	}
	if err != nil {
		os.Remove(f.Name())
		return "", "", 0, "", err
	}
	return f.Name(), hex.EncodeToString(h.Sum(nil)), n, resp.Request.URL.String(), nil
}

func archiveKind(url string) string {
	u := strings.ToLower(strings.SplitN(url, "?", 2)[0])
	switch {
	case strings.HasSuffix(u, ".zip"):
		return "zip"
	case strings.HasSuffix(u, ".tar.gz"), strings.HasSuffix(u, ".tgz"):
		return "tgz"
	case strings.HasSuffix(u, ".tar.bz2"):
		return "tbz"
	}
	return ""
}

// listArchive returns the file paths in an archive (at most 200k).
func listArchive(file, url string) ([]string, error) {
	var names []string
	add := func(n string) error {
		if len(names) >= 200000 {
			return errors.New("archive has too many files")
		}
		names = append(names, strings.TrimPrefix(path.Clean("/"+n), "/"))
		return nil
	}
	switch archiveKind(url) {
	case "zip":
		z, err := zip.OpenReader(file)
		if err != nil {
			return nil, err
		}
		defer z.Close()
		for _, f := range z.File {
			if !f.FileInfo().IsDir() {
				if err := add(f.Name); err != nil {
					return nil, err
				}
			}
		}
		return names, nil
	default:
		f, err := os.Open(file)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		var rd io.Reader = f
		if archiveKind(url) == "tgz" {
			gz, err := gzip.NewReader(f)
			if err != nil {
				return nil, err
			}
			rd = gz
		} else {
			rd = bzip2.NewReader(f)
		}
		tr := tar.NewReader(rd)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				return names, nil
			}
			if err != nil {
				return nil, err
			}
			if h.Typeflag == tar.TypeReg {
				if err := add(h.Name); err != nil {
					return nil, err
				}
			}
		}
	}
}

// webRoot finds where the app's index.php lives and how to extract the
// archive so it ends up as the site's public/ folder. An app that ships its
// own public/ next to vendor/ (Laravel style) is extracted into the site
// home instead. Releases that need "composer install" are refused.
// appLayout says where a release goes in the site: apps with their own
// public folder go to app/ and serve app/<public>; others go through webRoot.
func appLayout(names []string, s *Source) (strip int, into, webroot string, err error) {
	if s.Public == "" {
		strip, into, err = webRoot(names, s.AllowNoVendor)
		return strip, into, "", err
	}
	depth := -1
	top := ""
	for _, n := range names {
		if path.Base(n) != "index.php" || strings.Contains("/"+n, "/vendor/") {
			continue
		}
		d := path.Dir(n)
		if d == s.Public {
			d = "."
		} else if top, ok := strings.CutSuffix(d, "/"+s.Public); ok {
			d = top
		} else {
			continue
		}
		if c := strings.Count(n, "/"); depth == -1 || c < depth {
			depth, top = c, d
		}
	}
	if depth == -1 {
		return 0, "", "", fmt.Errorf("no %s/index.php in the release", s.Public)
	}
	if set := names2set(names); !s.AllowNoVendor && hasAny(set, top, "composer.json") && !hasAny(set, top, "vendor/autoload.php") {
		return 0, "", "", errors.New("the release has no vendor/ folder (it needs composer install), so it cannot be installed as is")
	}
	if top != "." {
		strip = strings.Count(top, "/") + 1
	}
	return strip, "app", "app/" + s.Public, nil
}

func names2set(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}

func webRoot(names []string, allowNoVendor bool) (strip int, into string, err error) {
	set := names2set(names)
	best := ""
	depth := -1
	for _, n := range names {
		if path.Base(n) != "index.php" || strings.Contains(n, "/vendor/") || strings.HasPrefix(n, "vendor/") {
			continue
		}
		d := strings.Count(n, "/")
		if depth == -1 || d < depth {
			best, depth = path.Dir(n), d
		}
	}
	if depth == -1 {
		return 0, "", errors.New("no index.php in the release; it is not a ready-to-run PHP app")
	}
	root, into := best, "public"
	if path.Base(best) == "public" {
		parent := path.Dir(best)
		if hasAny(set, parent, "vendor/autoload.php", "artisan", "composer.json") {
			root, into = parent, "."
		}
	}
	if !allowNoVendor && hasAny(set, root, "composer.json") && !hasAny(set, root, "vendor/autoload.php") {
		return 0, "", errors.New("the release has no vendor/ folder (it needs composer install), so it cannot be installed as is")
	}
	if root == "." {
		return 0, into, nil
	}
	return strings.Count(root, "/") + 1, into, nil
}

func hasAny(set map[string]bool, dir string, files ...string) bool {
	for _, f := range files {
		if set[path.Join(dir, f)] {
			return true
		}
	}
	return false
}
