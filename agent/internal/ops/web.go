package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"
)

// Web hosting layout (CloudPanel-like):
//
//	/srv/sites/<domain>/            site home, owner <siteuser>:www-data 0750
//	/srv/sites/<domain>/public/     document root (setgid www-data)
//	/srv/sites/<domain>/logs/       nginx access/error logs
//	/etc/nginx/sites-available/servermgr-<domain>.conf (+ symlink in sites-enabled)
//	/etc/php/<v>/fpm/pool.d/servermgr-<domain>.conf    per-site pool, own Linux user
//	/etc/servermgr-agent/sites/<domain>.json           site registry (source of truth)
const (
	acmeWebroot = "/var/lib/servermgr-agent/acme"
	certsDir    = "/etc/servermgr-agent/certs"
)

// Variables so tests can point them at a temp dir.
var (
	sitesRoot    = "/srv/sites"
	siteRegistry = "/etc/servermgr-agent/sites"
)

type Site struct {
	Domain      string        `json:"domain"`
	Aliases     []string      `json:"aliases"`
	Type        string        `json:"type"` // static | php | proxy
	PHPVersion  string        `json:"php_version,omitempty"`
	ProxyTarget string        `json:"proxy_target,omitempty"`
	User        string        `json:"user"`
	Root        string        `json:"root"`
	TLS         string        `json:"tls"` // "" | selfsigned | letsencrypt
	TLSEmail    string        `json:"tls_email,omitempty"`
	TLSStaging  bool          `json:"tls_staging,omitempty"`
	TLSExpires  time.Time     `json:"tls_expires,omitzero"`
	ForceHTTPS  bool          `json:"force_https"`
	CreatedAt   time.Time     `json:"created_at"`
	Backup      *BackupPolicy `json:"backup,omitempty"`
	App         *SiteApp      `json:"app,omitempty"`
	Access      *SiteAccess   `json:"access,omitempty"`
}

func (s *Site) home() string    { return filepath.Join(sitesRoot, s.Domain) }
func (s *Site) names() []string { return append([]string{s.Domain}, s.Aliases...) }
func (s *Site) certDir() string { return filepath.Join(certsDir, s.Domain) }

// nginxConf is the vhost file. Debian uses sites-available + a symlink in
// sites-enabled; RHEL nginx only reads conf.d, so nginxLink is empty there.
func (s *Site) nginxConf() string {
	if isRHEL() {
		return "/etc/nginx/conf.d/servermgr-" + s.Domain + ".conf"
	}
	return "/etc/nginx/sites-available/servermgr-" + s.Domain + ".conf"
}
func (s *Site) nginxLink() string {
	if isRHEL() {
		return ""
	}
	return "/etc/nginx/sites-enabled/servermgr-" + s.Domain + ".conf"
}
func (s *Site) fpmPool() string { return phpLayoutFor(s.PHPVersion).poolFile(s.Domain) }
func (s *Site) fpmSocket() string {
	return filepath.Join(phpLayoutFor(s.PHPVersion).sockDir, "servermgr-"+s.Domain+".sock")
}

// phpLayout is where one PHP version keeps its FPM pieces: Debian/sury
// packages (php8.4-fpm) or Remi's parallel installs on RHEL (php84-php-fpm).
type phpLayout struct {
	poolDir, bin, service, sockDir string
}

func (l phpLayout) poolFile(domain string) string {
	return filepath.Join(l.poolDir, "servermgr-"+domain+".conf")
}

func phpLayoutFor(v string) phpLayout {
	if isRHEL() {
		xy := strings.ReplaceAll(v, ".", "")
		return phpLayout{
			poolDir: "/etc/opt/remi/php" + xy + "/php-fpm.d",
			bin:     "/opt/remi/php" + xy + "/root/usr/sbin/php-fpm",
			service: "php" + xy + "-php-fpm",
			sockDir: "/var/opt/remi/php" + xy + "/run/php-fpm",
		}
	}
	return phpLayout{
		poolDir: "/etc/php/" + v + "/fpm/pool.d",
		bin:     "/usr/sbin/php-fpm" + v,
		service: "php" + v + "-fpm",
		sockDir: "/run/php",
	}
}

var (
	domainRe     = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,62}$`)
	phpVersionRe = regexp.MustCompile(`^\d\.\d$`)
	emailRe      = regexp.MustCompile(`^[^@\s]{1,64}@[a-z0-9.-]{1,253}\.[a-z]{2,}$`)
)

// siteMu serialises site changes so concurrent jobs never interleave nginx
// writes and reloads.
var siteMu sync.Mutex

func withSiteLock(f func() (any, error)) (any, error) {
	siteMu.Lock()
	defer siteMu.Unlock()
	return f()
}

func validDomain(d string) bool { return len(d) <= 253 && domainRe.MatchString(d) }

// siteUser derives a Linux user name (max 32 chars) from the domain.
func siteUser(domain string) string {
	slug := strings.NewReplacer(".", "_", "-", "_").Replace(domain)
	if len(slug) > 24 {
		sum := sha256.Sum256([]byte(domain))
		slug = slug[:17] + "_" + hex.EncodeToString(sum[:])[:6]
	}
	return "s_" + slug
}

func loadSite(domain string) (*Site, error) {
	if !validDomain(domain) {
		return nil, fmt.Errorf("invalid domain %q", domain)
	}
	b, err := os.ReadFile(filepath.Join(siteRegistry, domain+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("site %s does not exist", domain)
	}
	if err != nil {
		return nil, err
	}
	s := &Site{}
	return s, json.Unmarshal(b, s)
}

func saveSite(s *Site) error {
	if err := os.MkdirAll(siteRegistry, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	return writeFileAtomic(filepath.Join(siteRegistry, s.Domain+".json"), b, 0o600)
}

func listSites() ([]*Site, error) {
	entries, err := os.ReadDir(siteRegistry)
	if errors.Is(err, os.ErrNotExist) {
		return []*Site{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []*Site{}
	for _, e := range entries {
		if d, ok := strings.CutSuffix(e.Name(), ".json"); ok {
			if s, err := loadSite(d); err == nil {
				out = append(out, s)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	return out, nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func run(ctx context.Context, logf func(string, ...any), name string, args ...string) error {
	logf("$ %s %s", name, strings.Join(args, " "))
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if s := strings.TrimSpace(string(out)); s != "" {
		logf("%s", s)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// ---- status & install ----

func phpVersions() []string {
	var cands []string
	if isRHEL() {
		dirs, _ := filepath.Glob("/etc/opt/remi/php[0-9][0-9]")
		for _, d := range dirs {
			xy := strings.TrimPrefix(filepath.Base(d), "php")
			cands = append(cands, xy[:1]+"."+xy[1:])
		}
	} else {
		dirs, _ := filepath.Glob("/etc/php/*/fpm")
		for _, d := range dirs {
			cands = append(cands, filepath.Base(filepath.Dir(d)))
		}
	}
	out := []string{}
	for _, v := range cands {
		if _, err := os.Stat(phpLayoutFor(v).bin); err == nil {
			out = append(out, v)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

// nginxModernHTTP2 reports whether nginx understands `http2 on;` (1.25.1+).
// Older builds (e.g. RHEL 9 AppStream) need `listen 443 ssl http2;`.
func nginxModernHTTP2() bool {
	out, err := exec.Command("nginx", "-v").CombinedOutput()
	if err != nil {
		return true
	}
	m := regexp.MustCompile(`nginx/(\d+)\.(\d+)\.(\d+)`).FindStringSubmatch(string(out))
	if m == nil {
		return true
	}
	maj, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	patch, _ := strconv.Atoi(m[3])
	return maj > 1 || min > 25 || (min == 25 && patch >= 1)
}

func webStatus(ctx context.Context) (any, error) {
	res := map[string]any{"nginx": false, "php_versions": phpVersions(), "sites": []*Site{}}
	if _, err := exec.LookPath("nginx"); err == nil {
		res["nginx"] = true
		out, _ := exec.CommandContext(ctx, "nginx", "-v").CombinedOutput()
		res["nginx_version"] = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "nginx version:"))
		res["nginx_active"] = exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "nginx").Run() == nil
	}
	sites, err := listSites()
	if err != nil {
		return nil, err
	}
	res["sites"] = sites
	return res, nil
}

const nginxHardening = `# Managed by servermgr.
map $http_upgrade $sm_connection_upgrade {
    default upgrade;
    ''      close;
}
`

func webInstall(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	if err := pkgInstall(ctx, logf, []string{"nginx", "ssl-cert"}, []string{"nginx"}); err != nil {
		return nil, err
	}
	const snippet = "/etc/nginx/conf.d/servermgr.conf"
	if err := os.WriteFile(snippet, []byte(nginxHardening), 0o644); err != nil {
		return nil, err
	}
	if err := run(ctx, logf, "nginx", "-t"); err != nil {
		_ = os.Remove(snippet)
		return nil, fmt.Errorf("nginx rejected servermgr.conf, removed it: %w", err)
	}
	// The stock default site answers for every unknown host; keep it but
	// make sure our sites win with explicit server_names.
	for _, d := range []string{acmeWebroot, sitesRoot, siteRegistry, certsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	if err := nginxReload(ctx, logf); err != nil {
		return nil, err
	}
	if v := p["php"]; v != "" {
		if err := installPHP(ctx, v, logf); err != nil {
			return nil, err
		}
	}
	return webStatus(ctx)
}

var phpExtensions = []string{"fpm", "cli", "mysql", "curl", "gd", "mbstring", "xml", "zip", "intl", "bcmath", "opcache"}

// installPHP installs php<v>-fpm and common extensions. Versions Debian does
// not ship come from packages.sury.org, which is added on first use.
func installPHP(ctx context.Context, v string, logf func(string, ...any)) error {
	if !phpVersionRe.MatchString(v) {
		return fmt.Errorf("invalid PHP version %q", v)
	}
	if isRHEL() {
		return installPHPRemi(ctx, v, logf)
	}
	pkgs := make([]string, 0, len(phpExtensions))
	for _, e := range phpExtensions {
		pkgs = append(pkgs, "php"+v+"-"+e)
	}
	aptMu.Lock()
	avail := exec.CommandContext(ctx, "apt-cache", "show", "php"+v+"-fpm").Run() == nil
	aptMu.Unlock()
	if !avail {
		if err := addSuryRepo(ctx, logf); err != nil {
			return err
		}
	}
	if err := aptInstall(ctx, logf, pkgs...); err != nil {
		return err
	}
	return run(ctx, logf, "systemctl", "enable", "--now", "php"+v+"-fpm")
}

func addSuryRepo(ctx context.Context, logf func(string, ...any)) error {
	const list = "/etc/apt/sources.list.d/php-sury.sources"
	if _, err := os.Stat(list); err == nil {
		return nil
	}
	codename := ""
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(l, "VERSION_CODENAME="); ok {
				codename = strings.Trim(v, `"`)
			}
		}
	}
	if codename == "" {
		return errors.New("cannot determine the OS codename for the PHP repository")
	}
	logf("adding packages.sury.org PHP repository for %s", codename)
	if err := aptInstall(ctx, logf, "ca-certificates", "curl"); err != nil {
		return err
	}
	keyDeb := "/tmp/debsuryorg-archive-keyring.deb"
	if err := run(ctx, logf, "curl", "-fsSLo", keyDeb, "https://packages.sury.org/debsuryorg-archive-keyring.deb"); err != nil {
		return err
	}
	defer os.Remove(keyDeb)
	if err := run(ctx, logf, "dpkg", "-i", keyDeb); err != nil {
		return err
	}
	src := fmt.Sprintf("Types: deb\nURIs: https://packages.sury.org/php/\nSuites: %s\nComponents: main\nSigned-By: /usr/share/keyrings/debsuryorg-archive-keyring.gpg\n", codename)
	return os.WriteFile(list, []byte(src), 0o644)
}

// installPHPRemi installs a PHP version from Remi's repository, which ships
// every supported version side by side (php84-php-fpm, php83-php-fpm, …).
func installPHPRemi(ctx context.Context, v string, logf func(string, ...any)) error {
	if err := ensureEPEL(ctx, logf); err != nil {
		return err
	}
	if exec.CommandContext(ctx, "rpm", "-q", "remi-release").Run() != nil {
		osFamily()
		if famMajor < 8 {
			return errors.New("RHEL 8 or newer is required")
		}
		logf("adding the Remi PHP repository")
		aptMu.Lock()
		err := dnfRun(ctx, logf, "install", fmt.Sprintf("https://rpms.remirepo.net/enterprise/remi-release-%d.rpm", famMajor))
		aptMu.Unlock()
		if err != nil {
			return err
		}
	}
	xy := strings.ReplaceAll(v, ".", "")
	pkgs := []string{}
	for _, e := range []string{"fpm", "cli", "mysqlnd", "gd", "mbstring", "xml", "pecl-zip", "intl", "bcmath", "opcache"} {
		pkgs = append(pkgs, "php"+xy+"-php-"+e)
	}
	if err := pkgInstall(ctx, logf, nil, pkgs); err != nil {
		return err
	}
	return run(ctx, logf, "systemctl", "enable", "--now", phpLayoutFor(v).service)
}

func phpInstall(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	if err := installPHP(ctx, p["version"], logf); err != nil {
		return nil, err
	}
	return webStatus(ctx)
}

// ---- nginx & fpm config ----

var nginxTmpl = template.Must(template.New("site").Parse(`# Managed by servermgr. Edits are overwritten from the panel.
{{- $s := .Site}}
server {
    listen 80;
    listen [::]:80;
    server_name {{.Names}};

    location ^~ /.well-known/acme-challenge/ {
        root {{.ACME}};
        default_type text/plain;
        auth_basic off;
        allow all;
    }
{{- if and .HasCert $s.ForceHTTPS}}

    location / {
        return 301 https://$host$request_uri;
    }
}

server {
    listen 443 ssl{{if not .HTTP2On}} http2{{end}};
    listen [::]:443 ssl{{if not .HTTP2On}} http2{{end}};
{{- if .HTTP2On}}
    http2 on;
{{- end}}
    server_name {{.Names}};
{{- end}}
{{- if .HasCert}}
{{- if not $s.ForceHTTPS}}
    listen 443 ssl{{if not .HTTP2On}} http2{{end}};
    listen [::]:443 ssl{{if not .HTTP2On}} http2{{end}};
{{- if .HTTP2On}}
    http2 on;
{{- end}}
{{- end}}
    ssl_certificate     {{.CertDir}}/fullchain.pem;
    ssl_certificate_key {{.CertDir}}/privkey.pem;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_session_cache shared:SSL:10m;
    add_header Strict-Transport-Security "max-age=31536000" always;
{{- end}}

{{- with $s.Access}}
{{- range .AllowIPs}}
    allow {{.}};
{{- end}}
{{- if .AllowIPs}}
    deny all;
{{- end}}
{{- if .AuthUser}}
    auth_basic "Restricted";
    auth_basic_user_file {{$.Htpasswd}};
{{- end}}
{{- end}}

    root {{$s.Root}};
    index {{if eq $s.Type "php"}}index.php {{end}}index.html index.htm;
    access_log {{.Home}}/logs/access.log;
    error_log  {{.Home}}/logs/error.log;
    client_max_body_size 64m;

    add_header X-Content-Type-Options nosniff always;
    add_header Referrer-Policy strict-origin-when-cross-origin always;

    location ~ /\.(?!well-known) {
        deny all;
    }
{{- if eq $s.Type "static"}}

    location / {
        try_files $uri $uri/ =404;
    }
{{- else if eq $s.Type "php"}}

    location / {
        try_files $uri $uri/ /index.php?$args;
    }

    location ~ \.php$ {
        try_files $uri =404;
        include fastcgi_params;
        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
        fastcgi_param HTTPS $https if_not_empty;
        fastcgi_pass unix:{{.Socket}};
        fastcgi_read_timeout 300;
    }
{{- else if eq $s.Type "proxy"}}

    location / {
        proxy_pass {{$s.ProxyTarget}};
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $sm_connection_upgrade;
        proxy_read_timeout 300;
    }
{{- end}}
}
`))

var fpmTmpl = template.Must(template.New("pool").Parse(`; Managed by servermgr. Edits are overwritten from the panel.
[servermgr-{{.Domain}}]
user = {{.User}}
group = {{.User}}
listen = {{.Socket}}
listen.owner = {{.WebGroup}}
listen.group = {{.WebGroup}}
listen.mode = 0660
pm = ondemand
pm.max_children = 10
pm.process_idle_timeout = 30s
pm.max_requests = 500
chdir = /
php_admin_value[open_basedir] = {{.Home}}/:/tmp/:/usr/share/php/
php_admin_value[upload_tmp_dir] = {{.Home}}/tmp
php_admin_value[session.save_path] = {{.Home}}/tmp
php_admin_value[error_log] = {{.Home}}/logs/php-error.log
php_admin_flag[log_errors] = on
php_admin_value[disable_functions] = exec,passthru,shell_exec,system,proc_open,popen
`))

func (s *Site) hasCert() bool {
	_, err := os.Stat(filepath.Join(s.certDir(), "fullchain.pem"))
	return s.TLS != "" && err == nil
}

func renderNginx(s *Site) ([]byte, error) { return renderNginxWith(s, s.hasCert()) }

func renderNginxWith(s *Site, hasCert bool) ([]byte, error) {
	var b bytes.Buffer
	err := nginxTmpl.Execute(&b, map[string]any{
		"Site": s, "Names": strings.Join(s.names(), " "), "ACME": acmeWebroot, "Home": s.home(),
		"HasCert": hasCert, "CertDir": s.certDir(), "Socket": s.fpmSocket(), "HTTP2On": nginxModernHTTP2(), "Htpasswd": s.htpasswdFile(),
	})
	return b.Bytes(), err
}

func nginxReload(ctx context.Context, logf func(string, ...any)) error {
	if err := run(ctx, logf, "nginx", "-t"); err != nil {
		return err
	}
	if exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "nginx").Run() != nil {
		return run(ctx, logf, "systemctl", "enable", "--now", "nginx")
	}
	return run(ctx, logf, "systemctl", "reload", "nginx")
}

// applyNginx writes the site's vhost and reloads nginx, restoring the
// previous config if the new one does not pass `nginx -t`.
func applyNginx(ctx context.Context, s *Site, logf func(string, ...any)) error {
	conf, err := renderNginx(s)
	if err != nil {
		return err
	}
	prev, prevErr := os.ReadFile(s.nginxConf())
	if err := writeFileAtomic(s.nginxConf(), conf, 0o644); err != nil {
		return err
	}
	if link := s.nginxLink(); link != "" {
		if _, err := os.Lstat(link); errors.Is(err, os.ErrNotExist) {
			if err := os.Symlink(s.nginxConf(), link); err != nil {
				return err
			}
		}
	}
	if err := nginxReload(ctx, logf); err != nil {
		if prevErr == nil {
			_ = writeFileAtomic(s.nginxConf(), prev, 0o644)
		} else {
			if link := s.nginxLink(); link != "" {
				_ = os.Remove(link)
			}
			_ = os.Remove(s.nginxConf())
		}
		_ = run(ctx, logf, "nginx", "-t")
		return fmt.Errorf("nginx rejected the config, previous config restored: %w", err)
	}
	return nil
}

func applyFPM(ctx context.Context, s *Site, logf func(string, ...any)) error {
	var b bytes.Buffer
	if err := fpmTmpl.Execute(&b, map[string]any{"Domain": s.Domain, "User": s.User, "Socket": s.fpmSocket(), "Home": s.home(), "WebGroup": webGroup()}); err != nil {
		return err
	}
	if err := writeFileAtomic(s.fpmPool(), b.Bytes(), 0o644); err != nil {
		return err
	}
	l := phpLayoutFor(s.PHPVersion)
	if err := run(ctx, logf, l.bin, "-t"); err != nil {
		_ = os.Remove(s.fpmPool())
		return err
	}
	return run(ctx, logf, "systemctl", "reload-or-restart", l.service)
}

// removeFPMPools deletes this site's pool from every PHP version except keep.
func removeFPMPools(ctx context.Context, s *Site, keep string, logf func(string, ...any)) {
	for _, v := range phpVersions() {
		if v == keep {
			continue
		}
		l := phpLayoutFor(v)
		if pool := l.poolFile(s.Domain); fileExists(pool) {
			_ = os.Remove(pool)
			_ = run(ctx, logf, "systemctl", "reload-or-restart", l.service)
		}
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// ---- create / update / delete ----

func parseSiteParams(p map[string]string, s *Site) error {
	s.Type = p["type"]
	switch s.Type {
	case "static":
		s.PHPVersion, s.ProxyTarget = "", ""
	case "php":
		s.ProxyTarget = ""
		s.PHPVersion = p["php_version"]
		if !slices.Contains(phpVersions(), s.PHPVersion) {
			return fmt.Errorf("PHP %s is not installed on this server", s.PHPVersion)
		}
	case "proxy":
		s.PHPVersion = ""
		u, err := url.Parse(p["proxy_target"])
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.ContainsAny(p["proxy_target"], " ;{}'\"\\") {
			return errors.New("proxy target must be a URL like http://127.0.0.1:3000")
		}
		s.ProxyTarget = u.String()
	default:
		return errors.New("type must be static, php or proxy")
	}
	s.Aliases = []string{}
	for _, a := range strings.Fields(strings.ReplaceAll(p["aliases"], ",", " ")) {
		a = strings.ToLower(a)
		if !validDomain(a) {
			return fmt.Errorf("invalid alias %q", a)
		}
		if a != s.Domain && !slices.Contains(s.Aliases, a) {
			s.Aliases = append(s.Aliases, a)
		}
	}
	if v, ok := p["force_https"]; ok {
		s.ForceHTTPS = v == "true"
	}
	return nil
}

const welcomePage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>%[1]s</title><style>body{font-family:system-ui,sans-serif;background:#0f1115;color:#e6e6e6;display:grid;place-items:center;min-height:100vh;margin:0}
main{text-align:center}h1{font-weight:600}p{color:#9aa0aa}</style></head>
<body><main><h1>%[1]s</h1><p>This site is hosted with servermgr. Upload your files to replace this page.</p></main></body></html>
`

func siteCreate(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	if _, err := exec.LookPath("nginx"); err != nil {
		return nil, errors.New("nginx is not installed; install the web stack first")
	}
	domain := strings.ToLower(strings.TrimSpace(p["domain"]))
	if !validDomain(domain) {
		return nil, fmt.Errorf("invalid domain %q", domain)
	}
	if _, err := loadSite(domain); err == nil {
		return nil, fmt.Errorf("site %s already exists", domain)
	}
	s := &Site{Domain: domain, User: siteUser(domain), CreatedAt: time.Now().UTC(), ForceHTTPS: true}
	if err := parseSiteParams(p, s); err != nil {
		return nil, err
	}
	s.Root = filepath.Join(s.home(), "public")

	// Linux user owning the site; no login shell.
	if exec.CommandContext(ctx, "id", "-u", s.User).Run() != nil {
		if err := run(ctx, logf, "useradd", "--home-dir", s.home(), "--no-create-home", "--shell", "/usr/sbin/nologin", "--user-group", s.User); err != nil {
			return nil, err
		}
	}
	for _, d := range []struct {
		path string
		mode os.FileMode
	}{{s.home(), 0o750}, {s.Root, os.ModeSetgid | 0o750}, {filepath.Join(s.home(), "logs"), 0o750}, {filepath.Join(s.home(), "tmp"), 0o700}} {
		if err := os.MkdirAll(d.path, d.mode); err != nil {
			return nil, err
		}
		if err := os.Chmod(d.path, d.mode); err != nil {
			return nil, err
		}
		group := webGroup()
		if strings.HasSuffix(d.path, "/tmp") {
			group = s.User
		}
		if err := run(ctx, func(string, ...any) {}, "chown", s.User+":"+group, d.path); err != nil {
			return nil, err
		}
	}
	if s.Type != "proxy" {
		idx := filepath.Join(s.Root, "index.html")
		if _, err := os.Stat(idx); errors.Is(err, os.ErrNotExist) {
			_ = os.WriteFile(idx, []byte(fmt.Sprintf(welcomePage, domain)), 0o640)
			_ = run(ctx, func(string, ...any) {}, "chown", s.User+":"+webGroup(), idx)
		}
	}
	if err := selinuxWeb(ctx, logf, s.Type == "proxy"); err != nil {
		return nil, err
	}
	if s.Type == "php" {
		if err := applyFPM(ctx, s, logf); err != nil {
			return nil, err
		}
	}
	if err := applyNginx(ctx, s, logf); err != nil {
		return nil, err
	}
	if err := saveSite(s); err != nil {
		return nil, err
	}
	logf("site %s created (user %s, root %s)", domain, s.User, s.Root)
	return webStatus(ctx)
}

func siteUpdate(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	s, err := loadSite(strings.ToLower(p["domain"]))
	if err != nil {
		return nil, err
	}
	oldPHP := s.PHPVersion
	if err := parseSiteParams(p, s); err != nil {
		return nil, err
	}
	if err := selinuxWeb(ctx, logf, s.Type == "proxy"); err != nil {
		return nil, err
	}
	if s.Type == "php" {
		if err := applyFPM(ctx, s, logf); err != nil {
			return nil, err
		}
	}
	if err := applyNginx(ctx, s, logf); err != nil {
		return nil, err
	}
	if oldPHP != "" {
		removeFPMPools(ctx, s, s.PHPVersion, logf)
	}
	if err := saveSite(s); err != nil {
		return nil, err
	}
	logf("site %s updated", s.Domain)
	return webStatus(ctx)
}

func siteDelete(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	s, err := loadSite(strings.ToLower(p["domain"]))
	if err != nil {
		return nil, err
	}
	if link := s.nginxLink(); link != "" {
		_ = os.Remove(link)
	}
	_ = os.Remove(s.nginxConf())
	if err := nginxReload(ctx, logf); err != nil {
		return nil, err
	}
	removeFPMPools(ctx, s, "", logf)
	_ = os.RemoveAll(s.certDir())
	if p["delete_files"] == "true" {
		// Guard against ever removing anything outside /srv/sites.
		if filepath.Dir(s.home()) != sitesRoot || s.Domain == "" {
			return nil, errors.New("refusing to delete unexpected path")
		}
		logf("removing %s", s.home())
		if err := os.RemoveAll(s.home()); err != nil {
			return nil, err
		}
		_ = run(ctx, logf, "userdel", s.User)
	} else {
		logf("kept files in %s and user %s", s.home(), s.User)
	}
	if err := os.Remove(filepath.Join(siteRegistry, s.Domain+".json")); err != nil {
		return nil, err
	}
	logf("site %s deleted", s.Domain)
	return webStatus(ctx)
}
