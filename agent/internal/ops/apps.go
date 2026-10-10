package ops

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// One-click apps. The panel sends a template's typed steps plus variables;
// the agent runs them as the site's Linux user. There is no shell: every
// argument is substituted individually, so values cannot inject commands.

// SiteApp records which catalog app a site runs.
type SiteApp struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Version     string    `json:"version"`
	DB          string    `json:"db,omitempty"`
	DBUser      string    `json:"db_user,omitempty"`
	InstalledAt time.Time `json:"installed_at"`
	UpdatedAt   time.Time `json:"updated_at,omitzero"`
}

// appStep is one typed install/update action; exactly one field is set.
type appStep struct {
	Tool   string     `json:"tool,omitempty"`   // "wp-cli": ensure the pinned wp-cli phar
	WP     []string   `json:"wp,omitempty"`     // wp-cli arguments (run in the docroot)
	PHP    []string   `json:"php,omitempty"`    // php CLI arguments
	Dir    string     `json:"dir,omitempty"`    // working dir for php, relative to the site home
	Stdin  string     `json:"stdin,omitempty"`  // fed to wp/php on stdin (keeps secrets out of argv)
	Fetch  *fetchStep `json:"fetch,omitempty"`  // download, verify, extract
	Write  *writeStep `json:"write,omitempty"`  // write a file
	Remove string     `json:"remove,omitempty"` // delete a path inside the site
}

type fetchStep struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Strip  int    `json:"strip"`
	Into   string `json:"into"` // relative to the site home, usually "public"
}

type writeStep struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Mode    string `json:"mode"`
}

const (
	wpCLIURL    = "https://github.com/wp-cli/wp-cli/releases/download/v2.12.0/wp-cli-2.12.0.phar"
	wpCLISHA256 = "ce34ddd838f7351d6759068d09793f26755463b4a4610a5a5c0a97b68220d85c"
	wpCLIPath   = "/usr/local/lib/servermgr/wp-cli.phar"
)

var varRe = regexp.MustCompile(`\$\{([a-z_]+)\}`)

func subst(s string, vars map[string]string) (string, error) {
	var missing string
	out := varRe.ReplaceAllStringFunc(s, func(m string) string {
		k := m[2 : len(m)-1]
		v, ok := vars[k]
		if !ok {
			missing = k
		}
		return v
	})
	if missing != "" {
		return "", fmt.Errorf("template uses unknown variable %q", missing)
	}
	return out, nil
}

func substAll(args []string, vars map[string]string) ([]string, error) {
	out := make([]string, len(args))
	for i, a := range args {
		v, err := subst(a, vars)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// insideSite resolves a template path relative to the site home and makes
// sure it stays there.
func insideSite(s *Site, rel string) (string, error) {
	p := filepath.Join(s.home(), filepath.Clean("/"+rel))
	if p != s.home() && !strings.HasPrefix(p, s.home()+"/") {
		return "", fmt.Errorf("path %q escapes the site", rel)
	}
	return p, nil
}

func phpCLI(v string) string {
	if isRHEL() {
		return "/opt/remi/php" + strings.ReplaceAll(v, ".", "") + "/root/usr/bin/php"
	}
	return "/usr/bin/php" + v
}

// asSite runs a command as the site user with a private HOME, streaming
// output (secrets never appear in arguments that are logged).
func asSite(ctx context.Context, s *Site, dir, stdin string, logf func(string, ...any), shown string, name string, args ...string) (string, error) {
	logf("$ %s", shown)
	full := append([]string{"-u", s.User, "--", "env", "HOME=" + filepath.Join(s.home(), "tmp"), "WP_CLI_CACHE_DIR=" + filepath.Join(s.home(), "tmp", ".wp-cli"), name}, args...)
	cmd := exec.CommandContext(ctx, "runuser", full...)
	cmd.Dir = dir
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out strings.Builder
	err := streamCmd(cmd, func(f string, a ...any) {
		line := fmt.Sprintf(f, a...)
		out.WriteString(line + "\n")
		logf("%s", line)
	})
	return out.String(), err
}

// shownArgs hides values of arguments that carry secrets in the job log.
func shownArgs(args []string, vars map[string]string) string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = a
		for k, v := range vars {
			if v != "" && (strings.Contains(k, "password") || strings.Contains(k, "secret")) && strings.Contains(a, v) {
				out[i] = strings.ReplaceAll(a, v, "••••")
			}
		}
	}
	return strings.Join(out, " ")
}

func ensureWPCLI(ctx context.Context, logf func(string, ...any)) error {
	if b, err := os.ReadFile(wpCLIPath); err == nil {
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) == wpCLISHA256 {
			return nil
		}
	}
	logf("downloading wp-cli 2.12.0")
	tmp, err := download(ctx, wpCLIURL, wpCLISHA256)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := os.MkdirAll(filepath.Dir(wpCLIPath), 0o755); err != nil {
		return err
	}
	b, err := os.ReadFile(tmp)
	if err != nil {
		return err
	}
	return writeFileAtomic(wpCLIPath, b, 0o755)
}

// download fetches url to a temp file and verifies its SHA-256.
func download(ctx context.Context, url, want string) (string, error) {
	if !strings.HasPrefix(url, "https://") {
		return "", errors.New("downloads must use https")
	}
	if !sha256Hex.MatchString(strings.ToLower(want)) {
		return "", errors.New("template is missing a valid sha256 for " + url)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	f, err := os.CreateTemp("", "smapp-*")
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, 1<<30))
	f.Close()
	if err == nil && hex.EncodeToString(h.Sum(nil)) != strings.ToLower(want) {
		err = fmt.Errorf("checksum mismatch for %s", url)
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// extract unpacks a .zip / .tar.gz / .tgz / .tar.bz2 archive into dst,
// dropping strip leading path components and refusing escaping entries.
func extract(archive, url, dst string, strip int) error {
	clean := func(name string) (string, bool) {
		parts := strings.Split(strings.Trim(filepath.ToSlash(name), "/"), "/")
		if len(parts) <= strip {
			return "", false
		}
		rel := filepath.Join(parts[strip:]...)
		return rel, rel != "" && rel != "."
	}
	if strings.HasSuffix(url, ".zip") {
		zr, err := zip.OpenReader(archive)
		if err != nil {
			return err
		}
		defer zr.Close()
		for _, f := range zr.File {
			rel, ok := clean(f.Name)
			if !ok {
				continue
			}
			target, err := safeJoin(dst, rel)
			if err != nil {
				return err
			}
			if f.FileInfo().IsDir() {
				if err := os.MkdirAll(target, 0o755); err != nil {
					return err
				}
				continue
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			rc, err := f.Open()
			if err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
			if err == nil {
				_, err = io.Copy(out, rc)
				out.Close()
			}
			rc.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	var r io.Reader
	switch {
	case strings.HasSuffix(url, ".tar.gz") || strings.HasSuffix(url, ".tgz"):
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		r = gz
	case strings.HasSuffix(url, ".tar.bz2"):
		r = bzip2.NewReader(f)
	default:
		return errors.New("unsupported archive type")
	}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		rel, ok := clean(hdr.Name)
		if !ok {
			continue
		}
		target, err := safeJoin(dst, rel)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode).Perm()|0o600)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, tr)
			out.Close()
			if err != nil {
				return err
			}
		}
	}
}

// fixOwnership gives the site user (and the web group) the site tree,
// including app folders outside the docroot (data/, config/, private/).
func fixOwnership(ctx context.Context, s *Site) error {
	ents, err := os.ReadDir(s.home())
	if err != nil {
		return err
	}
	for _, e := range ents {
		if e.Name() == "logs" || e.Name() == "tmp" {
			continue
		}
		if err := exec.CommandContext(ctx, "chown", "-R", s.User+":"+webGroup(), filepath.Join(s.home(), e.Name())).Run(); err != nil {
			return err
		}
	}
	// Group can read, nobody else; directories keep setgid so new files stay readable by nginx.
	_ = exec.CommandContext(ctx, "chmod", "-R", "u+rwX,g+rX,g-w,o-rwx", s.Root).Run()
	return exec.CommandContext(ctx, "find", s.Root, "-type", "d", "-exec", "chmod", "g+s", "{}", "+").Run()
}

func runAppSteps(ctx context.Context, s *Site, steps []appStep, vars map[string]string, logf func(string, ...any)) (string, error) {
	// Tools may echo secrets back (wp-cli prints the full command for --prompt),
	// so every output line is masked before it reaches the panel.
	logf = maskSecrets(logf, vars)
	php := phpCLI(s.PHPVersion)
	var last string
	for i, st := range steps {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		switch {
		case st.Tool == "wp-cli":
			if err := ensureWPCLI(ctx, logf); err != nil {
				return "", err
			}
		case st.WP != nil:
			args, err := substAll(st.WP, vars)
			if err != nil {
				return "", err
			}
			stdin, err := subst(st.Stdin, vars)
			if err != nil {
				return "", err
			}
			out, err := asSite(ctx, s, s.Root, stdin, logf, "wp "+shownArgs(args, vars), php, append([]string{wpCLIPath, "--path=" + s.Root}, args...)...)
			if err != nil {
				return "", fmt.Errorf("step %d (wp %s): %w", i+1, args[0], err)
			}
			last = out
		case st.PHP != nil:
			args, err := substAll(st.PHP, vars)
			if err != nil {
				return "", err
			}
			stdin, err := subst(st.Stdin, vars)
			if err != nil {
				return "", err
			}
			dir := s.Root
			if st.Dir != "" {
				if dir, err = insideSite(s, st.Dir); err != nil {
					return "", err
				}
			}
			out, err := asSite(ctx, s, dir, stdin, logf, "php "+shownArgs(args, vars), php, args...)
			if err != nil {
				return "", fmt.Errorf("step %d (php %s): %w", i+1, args[0], err)
			}
			last = out
		case st.Fetch != nil:
			url, err := subst(st.Fetch.URL, vars)
			if err != nil {
				return "", err
			}
			into, err := insideSite(s, st.Fetch.Into)
			if err != nil {
				return "", err
			}
			logf("downloading %s", url)
			tmp, err := download(ctx, url, st.Fetch.SHA256)
			if err != nil {
				return "", err
			}
			logf("checksum ok; extracting into %s", into)
			err = extract(tmp, url, into, st.Fetch.Strip)
			os.Remove(tmp)
			if err != nil {
				return "", err
			}
			if err := fixOwnership(ctx, s); err != nil {
				return "", err
			}
		case st.Write != nil:
			p, err := insideSite(s, st.Write.Path)
			if err != nil {
				return "", err
			}
			content, err := subst(st.Write.Content, vars)
			if err != nil {
				return "", err
			}
			mode := os.FileMode(0o640)
			if m, err := strconv.ParseUint(st.Write.Mode, 8, 32); err == nil && m <= 0o770 {
				mode = os.FileMode(m)
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
				return "", err
			}
			if err := os.WriteFile(p, []byte(content), mode); err != nil {
				return "", err
			}
			_ = exec.CommandContext(ctx, "chown", s.User+":"+webGroup(), p).Run()
			logf("wrote %s", p)
		case st.Remove != "":
			p, err := insideSite(s, st.Remove)
			if err != nil {
				return "", err
			}
			if p == s.home() || p == s.Root {
				return "", errors.New("refusing to remove the site root")
			}
			_ = os.RemoveAll(p)
			logf("removed %s", p)
		default:
			return "", fmt.Errorf("step %d is empty", i+1)
		}
	}
	return last, nil
}

func parseAppParams(p map[string]string) (*Site, []appStep, map[string]string, error) {
	s, err := loadSite(strings.ToLower(p["domain"]))
	if err != nil {
		return nil, nil, nil, err
	}
	if s.Type != "php" {
		return nil, nil, nil, errors.New("apps need a PHP site")
	}
	var steps []appStep
	if err := json.Unmarshal([]byte(p["steps"]), &steps); err != nil {
		return nil, nil, nil, fmt.Errorf("bad steps: %w", err)
	}
	vars := map[string]string{}
	if p["vars"] != "" {
		if err := json.Unmarshal([]byte(p["vars"]), &vars); err != nil {
			return nil, nil, nil, fmt.Errorf("bad vars: %w", err)
		}
	}
	vars["home"] = s.home()
	vars["docroot"] = s.Root
	vars["domain"] = s.Domain
	return s, steps, vars, nil
}

// appDeploy installs a catalog app into an existing PHP site.
func appDeploy(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	s, steps, vars, err := parseAppParams(p)
	if err != nil {
		return nil, err
	}
	if s.App != nil {
		return nil, fmt.Errorf("%s already runs %s", s.Domain, s.App.Name)
	}
	// Start from an empty docroot (the welcome page is ours).
	_ = os.Remove(filepath.Join(s.Root, "index.html"))
	if _, err := runAppSteps(ctx, s, steps, vars, logf); err != nil {
		return nil, err
	}
	if err := fixOwnership(ctx, s); err != nil {
		return nil, err
	}
	version := p["version"]
	if vs := p["version_steps"]; vs != "" {
		var vsteps []appStep
		if json.Unmarshal([]byte(vs), &vsteps) == nil {
			if out, err := runAppSteps(ctx, s, vsteps, vars, func(string, ...any) {}); err == nil {
				version = strings.TrimSpace(out)
			}
		}
	}
	siteMu.Lock()
	s.App = &SiteApp{ID: p["app"], Name: p["name"], Version: version, DB: vars["db_name"], DBUser: vars["db_user"], InstalledAt: time.Now().UTC()}
	err = saveSite(s)
	siteMu.Unlock()
	if err != nil {
		return nil, err
	}
	logf("%s %s installed on %s", p["name"], version, s.Domain)
	return webStatus(ctx)
}

// appUpdate runs a template's update steps and refreshes the version.
func appUpdate(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	s, steps, vars, err := parseAppParams(p)
	if err != nil {
		return nil, err
	}
	if s.App == nil {
		return nil, errors.New("no app is installed on this site")
	}
	if _, err := runAppSteps(ctx, s, steps, vars, logf); err != nil {
		return nil, err
	}
	if err := fixOwnership(ctx, s); err != nil {
		return nil, err
	}
	if vs := p["version_steps"]; vs != "" {
		var vsteps []appStep
		if json.Unmarshal([]byte(vs), &vsteps) == nil {
			if out, err := runAppSteps(ctx, s, vsteps, vars, func(string, ...any) {}); err == nil {
				s.App.Version = strings.TrimSpace(out)
			}
		}
	}
	s.App.UpdatedAt = time.Now().UTC()
	siteMu.Lock()
	err = saveSite(s)
	siteMu.Unlock()
	if err != nil {
		return nil, err
	}
	logf("%s on %s is now %s", s.App.Name, s.Domain, s.App.Version)
	return webStatus(ctx)
}

// maskSecrets wraps logf so values of *password* variables never appear in
// job output.
func maskSecrets(logf func(string, ...any), vars map[string]string) func(string, ...any) {
	var secrets []string
	for k, v := range vars {
		if (strings.Contains(k, "password") || strings.Contains(k, "secret")) && len(v) >= 6 {
			secrets = append(secrets, v)
		}
	}
	return func(f string, a ...any) {
		line := fmt.Sprintf(f, a...)
		for _, s := range secrets {
			line = strings.ReplaceAll(line, s, "••••••••")
		}
		logf("%s", line)
	}
}
