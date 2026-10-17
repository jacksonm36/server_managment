package proxmox

import (
	"bufio"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ISO is an installer or tool ISO from isos.yaml.
type ISO struct {
	ID             string `yaml:"id" json:"id"`
	Name           string `yaml:"name" json:"name"`
	Vendor         string `yaml:"vendor" json:"vendor"`
	Category       string `yaml:"category" json:"category"`
	Icon           string `yaml:"icon" json:"icon"`
	URL            string `yaml:"url" json:"-"`
	Dir            string `yaml:"dir" json:"-"`
	Match          string `yaml:"match" json:"-"`
	Checksums      string `yaml:"checksums" json:"-"`
	List           string `yaml:"list" json:"-"`
	ChecksumSuffix string `yaml:"checksum_suffix" json:"-"`
	// Subdir picks the newest folder in dir whose name matches it (for
	// vendors with one folder per release); ChecksumsMatch picks the
	// checksum file from that folder's listing when its name changes.
	Subdir         string `yaml:"subdir" json:"-"`
	ChecksumsMatch string `yaml:"checksums_match" json:"-"`
	// Compression: the vendor ships the ISO compressed (gz, zst, bz2…);
	// Proxmox unpacks it and stores it without that extension.
	Compression string `yaml:"compression" json:"-"`
	// Follow resolves url through its redirects (Microsoft's fwlink
	// addresses) so Proxmox gets the file's real https address; File names
	// the download when the real name is unhelpful.
	Follow   bool   `yaml:"follow" json:"-"`
	FileName string `yaml:"file" json:"-"`
	// Windows is the Windows template edition (win11, ws2025…) this ISO
	// installs, so the template builder can offer to download it.
	Windows string `yaml:"windows" json:"windows,omitempty"`
	match   *regexp.Regexp
}

// ResolvedISO is an ISO's current download: the newest file and its hash.
type ResolvedISO struct {
	URL         string `json:"url"`
	File        string `json:"file"` // the name it is stored under
	Compression string `json:"compression,omitempty"`
	Algo        string `json:"checksum_algorithm,omitempty"`
	Checksum    string `json:"checksum,omitempty"`
}

//go:embed isos.yaml
var isosYAML []byte

// ISOs is the built-in ISO list.
var ISOs = func() []ISO {
	var list []ISO
	if err := yaml.Unmarshal(isosYAML, &list); err != nil {
		panic("proxmox/isos.yaml: " + err.Error())
	}
	seen := map[string]bool{}
	for i := range list {
		x := &list[i]
		if seen[x.ID] || (x.URL == "") == (x.Dir == "") || (x.Compression != "" && x.URL != "") || (x.Follow && x.URL == "") ||
			(x.FileName != "" && !isoFileName.MatchString(x.FileName)) {
			panic("proxmox/isos.yaml: bad or duplicate entry " + x.ID)
		}
		seen[x.ID] = true
		for _, u := range []string{x.URL, x.Dir} {
			if u != "" && !strings.HasPrefix(u, "https://") {
				panic("proxmox/isos.yaml: " + x.ID + " must use https")
			}
		}
		if x.Dir != "" {
			x.match = mustRe(x.Match)
			if !strings.HasSuffix(x.Dir, "/") {
				panic("proxmox/isos.yaml: " + x.ID + ": dir must end in /")
			}
		}
		for _, re := range []string{x.Subdir, x.ChecksumsMatch} {
			if re != "" {
				mustRe(re)
			}
		}
	}
	return list
}()

var isoFileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,120}\.iso$`)

func ISOByID(id string) *ISO {
	for i := range ISOs {
		if ISOs[i].ID == id {
			return &ISOs[i]
		}
	}
	return nil
}

// isoHTTP fetches checksum files and listings: HTTPS only, redirects too.
var isoHTTP = &http.Client{
	Timeout: 30 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || len(via) > 5 {
			return errors.New("refusing a redirect away from https")
		}
		return nil
	},
}

func isoGet(ctx context.Context, u string, max int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	res, err := isoHTTP.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("could not reach %s: %v", req.URL.Host, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s for %s", req.URL.Host, res.Status, req.URL.Path)
	}
	return io.ReadAll(io.LimitReader(res.Body, max))
}

type sumLine struct{ file, algo, sum string }

// parseSums reads every entry of a GNU or BSD style checksum file.
func parseSums(r io.Reader) []sumLine {
	var out []sumLine
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		var name, hash string
		if m := bsdSum.FindStringSubmatch(line); m != nil {
			name, hash = m[2], m[3]
		} else if m := gnuSum.FindStringSubmatch(line); m != nil {
			name, hash = m[2], m[1]
		} else {
			continue
		}
		algo := map[int]string{64: "sha256", 128: "sha512"}[len(hash)]
		if algo != "" {
			out = append(out, sumLine{path.Base(name), algo, strings.ToLower(hash)})
		}
	}
	return out
}

var (
	hrefRe    = regexp.MustCompile(`href="([^"?#/]+)"`)
	subdirRe  = regexp.MustCompile(`href="([^"?#/]+)/"`)
	listedRes = func(re *regexp.Regexp, page []byte) []string {
		var out []string
		for _, m := range re.FindAllSubmatch(page, -1) {
			if n, err := url.PathUnescape(string(m[1])); err == nil {
				out = append(out, n)
			}
		}
		return out
	}
)

// finalURL follows u's redirects (https only, see isoHTTP) without
// downloading anything and returns where they end.
func finalURL(ctx context.Context, u string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err != nil {
		return "", err
	}
	res, err := isoHTTP.Do(req)
	if err != nil {
		return "", err
	}
	res.Body.Close()
	if res.StatusCode >= 300 {
		return "", fmt.Errorf("%s: %s", u, res.Status)
	}
	end := res.Request.URL
	if end.Scheme != "https" || !strings.HasSuffix(strings.ToLower(end.Path), ".iso") {
		return "", fmt.Errorf("%s does not lead to an https ISO", u)
	}
	return end.String(), nil
}

// newest is the naturally greatest name matching re.
func newest(names []string, re *regexp.Regexp) string {
	best := ""
	for _, n := range names {
		if re.MatchString(n) && (best == "" || naturalLess(best, n)) {
			best = n
		}
	}
	return best
}

// Resolve finds the ISO's newest file and its checksum on the vendor's
// server.
func (x *ISO) Resolve(ctx context.Context) (*ResolvedISO, error) {
	if x.URL != "" {
		u := x.URL
		if x.Follow {
			var err error
			if u, err = finalURL(ctx, u); err != nil {
				return nil, err
			}
		}
		r := &ResolvedISO{URL: u, File: path.Base(u)}
		if pu, err := url.Parse(u); err == nil {
			r.File = path.Base(pu.Path)
		}
		if x.FileName != "" {
			r.File = x.FileName
		}
		return r, x.addSum(ctx, r, nil)
	}
	dir := x.Dir
	if x.Subdir != "" {
		page, err := isoGet(ctx, dir, 2<<20)
		if err != nil {
			return nil, err
		}
		sub := newest(listedRes(subdirRe, page), mustRe(x.Subdir))
		if sub == "" {
			return nil, fmt.Errorf("no release folder matching %s at %s", x.Subdir, dir)
		}
		dir += url.PathEscape(sub) + "/"
	}
	var listing []string
	if x.List == "dir" || x.ChecksumsMatch != "" {
		page, err := isoGet(ctx, dir, 2<<20)
		if err != nil {
			return nil, err
		}
		listing = listedRes(hrefRe, page)
	}
	sumFile := x.Checksums
	if x.ChecksumsMatch != "" {
		if sumFile = newest(listing, mustRe(x.ChecksumsMatch)); sumFile == "" {
			return nil, fmt.Errorf("no checksum file matching %s at %s", x.ChecksumsMatch, dir)
		}
	}
	var sums []sumLine
	var names []string
	if sumFile != "" {
		b, err := isoGet(ctx, dir+url.PathEscape(sumFile), 4<<20)
		if err != nil {
			return nil, err
		}
		sums = parseSums(strings.NewReader(string(b)))
		if x.List != "dir" {
			for _, s := range sums {
				names = append(names, s.file)
			}
		}
	}
	if x.List == "dir" {
		names = listing
	}
	best := newest(names, x.match)
	if best == "" {
		return nil, fmt.Errorf("no file matching %s at %s", x.Match, dir)
	}
	r := &ResolvedISO{URL: dir + url.PathEscape(best), File: best}
	err := x.addSum(ctx, r, sums)
	if x.Compression != "" {
		r.Compression, r.File = x.Compression, strings.TrimSuffix(best, "."+x.Compression)
	}
	return r, err
}

func (x *ISO) addSum(ctx context.Context, r *ResolvedISO, sums []sumLine) error {
	if x.ChecksumSuffix != "" {
		b, err := isoGet(ctx, r.URL+x.ChecksumSuffix, 64<<10)
		if err != nil {
			return err
		}
		sums = parseSums(strings.NewReader(string(b)))
	}
	if x.Checksums == "" && x.ChecksumsMatch == "" && x.ChecksumSuffix == "" {
		return nil // the vendor publishes none
	}
	for _, s := range sums {
		if s.file == r.File {
			r.Algo, r.Checksum = s.algo, s.sum
			return nil
		}
	}
	return fmt.Errorf("the checksum file does not list %s", r.File)
}

// naturalLess compares names so that 24.04.10 sorts after 24.04.9.
func naturalLess(a, b string) bool {
	for a != "" && b != "" {
		da, db := digitRun(a), digitRun(b)
		if da > 0 && db > 0 {
			na, nb := strings.TrimLeft(a[:da], "0"), strings.TrimLeft(b[:db], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			a, b = a[da:], b[db:]
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

func digitRun(s string) int {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return i
}

func mustRe(s string) *regexp.Regexp { return regexp.MustCompile(s) }
