package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// CrowdSec on a managed server: the security engine runs agent-only and
// reports to the panel's central LAPI; the nftables firewall bouncer pulls
// the shared decisions, so a ban raised on any server blocks the IP on all.
// The panel creates the machine and bouncer credentials centrally and
// passes them in; nothing registers itself.

const (
	csLocalConf   = "/etc/crowdsec/config.yaml.local"
	csCreds       = "/etc/crowdsec/local_api_credentials.yaml"
	csAcquis      = "/etc/crowdsec/acquis.d/servermgr.yaml"
	csBouncerConf = "/etc/crowdsec/bouncers/crowdsec-firewall-bouncer.yaml"
	csCA          = "/etc/servermgr-agent/ca.crt" // panel CA, also signs the LAPI cert
)

var csItem = regexp.MustCompile(`^[a-z0-9_-]+/[a-z0-9_.-]+$`)

func csInstalled() bool { _, err := exec.LookPath("cscli"); return err == nil }

func cscliJSON(ctx context.Context, out any, args ...string) error {
	b, err := exec.CommandContext(ctx, "cscli", append(args, "-o", "json")...).Output()
	if err != nil {
		return fmt.Errorf("cscli %s: %w", strings.Join(args, " "), err)
	}
	return json.Unmarshal(b, out)
}

func crowdsecStatus(ctx context.Context) (any, error) {
	res := map[string]any{"installed": csInstalled()}
	if !csInstalled() {
		res["fail2ban_active"] = exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "fail2ban").Run() == nil
		return res, nil
	}
	active := func(u string) bool {
		return exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", u).Run() == nil
	}
	res["engine_active"] = active("crowdsec")
	res["bouncer_active"] = active("crowdsec-firewall-bouncer")
	res["fail2ban_active"] = active("fail2ban")
	if out, err := exec.CommandContext(ctx, "cscli", "version").CombinedOutput(); err == nil {
		for _, l := range strings.Split(string(out), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(l), "version:"); ok {
				res["version"] = strings.TrimSpace(v)
			}
		}
	}
	lapi := exec.CommandContext(ctx, "cscli", "lapi", "status")
	out, err := lapi.CombinedOutput()
	res["lapi_ok"] = err == nil
	if err != nil {
		res["lapi_error"] = lastLine(string(out))
	}
	var hub map[string][]struct {
		Name    string `json:"name"`
		Status  string `json:"status"`
		Version string `json:"local_version"`
	}
	if cscliJSON(ctx, &hub, "collections", "list") == nil {
		res["collections"] = hub["collections"]
	}
	return res, nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// addCrowdSecRepo adds CrowdSec's official package repository. Debian 13
// ships an old 1.4 release; the bookworm build is static and works on trixie.
func addCrowdSecRepo(ctx context.Context, logf func(string, ...any)) error {
	if isRHEL() {
		osFamily()
		repo := fmt.Sprintf(`[crowdsec_crowdsec]
name=crowdsec_crowdsec
baseurl=https://packagecloud.io/crowdsec/crowdsec/el/%d/$basearch
repo_gpgcheck=1
gpgcheck=0
enabled=1
gpgkey=https://packagecloud.io/crowdsec/crowdsec/gpgkey
sslverify=1
sslcacert=/etc/pki/tls/certs/ca-bundle.crt
`, max(famMajor, 8))
		// packagecloud signs the repository metadata (repo_gpgcheck), not each
		// rpm, the same as the official CrowdSec repo file.
		logf("adding the CrowdSec rpm repository")
		return os.WriteFile("/etc/yum.repos.d/crowdsec_crowdsec.repo", []byte(repo), 0o644)
	}
	const list = "/etc/apt/sources.list.d/crowdsec_crowdsec.list"
	if fileExists(list) {
		return nil
	}
	if err := aptInstall(ctx, logf, "ca-certificates", "curl", "gpg"); err != nil {
		return err
	}
	logf("adding the CrowdSec apt repository")
	key := "/etc/apt/keyrings/crowdsec_crowdsec-archive-keyring.gpg"
	if err := os.MkdirAll("/etc/apt/keyrings", 0o755); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", "curl -fsSL https://packagecloud.io/crowdsec/crowdsec/gpgkey | gpg --dearmor --yes -o "+key)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("crowdsec key: %s", strings.TrimSpace(string(out)))
	}
	src := "deb [signed-by=" + key + "] https://packagecloud.io/crowdsec/crowdsec/debian/ bookworm main\n"
	return os.WriteFile(list, []byte(src), 0o644)
}

// detectCollections picks hub collections for what runs on this server.
func detectCollections(ctx context.Context) []string {
	c := []string{"crowdsecurity/linux", "crowdsecurity/sshd"}
	if _, err := exec.LookPath("nginx"); err == nil {
		c = append(c, "crowdsecurity/nginx", "crowdsecurity/base-http-scenarios", "crowdsecurity/http-cve")
	}
	if _, err := exec.LookPath("mariadb"); err == nil {
		c = append(c, "crowdsecurity/mariadb")
	}
	if sites, err := listSites(); err == nil {
		for _, s := range sites {
			if s.App != nil && s.App.ID == "wordpress" {
				c = append(c, "crowdsecurity/wordpress")
				break
			}
			if s.App != nil && s.App.ID == "nextcloud" {
				c = append(c, "crowdsecurity/nextcloud")
			}
		}
	}
	return c
}

func acquisYAML() string {
	sshUnit := "ssh.service"
	if isRHEL() {
		sshUnit = "sshd.service"
	}
	// Debian 13 / RHEL log SSH to journald only (no auth.log).
	return fmt.Sprintf(`# Managed by servermgr.
source: journalctl
journalctl_filter:
  - "_SYSTEMD_UNIT=%s"
labels:
  type: syslog
---
filenames:
  - /var/log/nginx/*.log
  - %s/*/logs/*.log
labels:
  type: nginx
`, sshUnit, sitesRoot)
}

func crowdsecInstall(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	lapi, err := url.Parse(p["lapi_url"])
	if err != nil || lapi.Scheme != "https" || lapi.Host == "" {
		return nil, errors.New("lapi_url must be https://host:port/")
	}
	if !unixUser.MatchString(strings.ToLower(p["machine"])) && !adminUserLike(p["machine"]) {
		return nil, errors.New("invalid machine name")
	}
	if len(p["password"]) < 16 || len(p["bouncer_key"]) < 16 {
		return nil, errors.New("machine password and bouncer key are required")
	}
	if err := addCrowdSecRepo(ctx, logf); err != nil {
		return nil, err
	}
	if err := pkgInstall(ctx, logf, []string{"crowdsec", "crowdsec-firewall-bouncer-nftables"}, []string{"crowdsec", "crowdsec-firewall-bouncer-nftables"}); err != nil {
		return nil, err
	}

	// Engine: agent only, credentials for the central LAPI.
	if err := os.WriteFile(csLocalConf, []byte("# Managed by servermgr: agent only, reports to the central LAPI.\napi:\n  server:\n    enable: false\n"), 0o600); err != nil {
		return nil, err
	}
	creds := fmt.Sprintf("url: %s\nlogin: %s\npassword: %s\nca_cert_path: %s\n", strings.TrimRight(lapi.String(), "/")+"/", p["machine"], p["password"], csCA)
	if err := writeFileAtomic(csCreds, []byte(creds), 0o600); err != nil {
		return nil, err
	}
	if err := os.MkdirAll("/etc/crowdsec/acquis.d", 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(csAcquis, []byte(acquisYAML()), 0o644); err != nil {
		return nil, err
	}

	logf("$ cscli hub update")
	_ = exec.CommandContext(ctx, "cscli", "hub", "update").Run()
	cols := detectCollections(ctx)
	logf("$ cscli collections install %s", strings.Join(cols, " "))
	if out, err := exec.CommandContext(ctx, "cscli", append([]string{"collections", "install"}, cols...)...).CombinedOutput(); err != nil {
		logf("%s", strings.TrimSpace(string(out)))
		return nil, errors.New("installing collections failed")
	}

	// Bouncer: pull decisions from the central LAPI, enforce with nftables.
	if err := configureBouncer(lapi.String(), p["bouncer_key"]); err != nil {
		return nil, err
	}
	for _, args := range [][]string{{"enable", "crowdsec"}, {"restart", "crowdsec"}, {"enable", "crowdsec-firewall-bouncer"}, {"restart", "crowdsec-firewall-bouncer"}} {
		if err := run(ctx, logf, "systemctl", args...); err != nil {
			return nil, err
		}
	}
	return crowdsecStatus(ctx)
}

// configureBouncer rewrites api_url / api_key / ca_cert_path in the
// bouncer's YAML, keeping the package defaults (nftables mode etc.).
func configureBouncer(lapi, key string) error {
	b, err := os.ReadFile(csBouncerConf)
	if err != nil {
		return err
	}
	set := map[string]string{"api_url": strings.TrimRight(lapi, "/") + "/", "api_key": key, "ca_cert_path": csCA}
	lines := strings.Split(string(b), "\n")
	for i, l := range lines {
		k, _, ok := strings.Cut(l, ":")
		if v, want := set[strings.TrimSpace(k)]; ok && want && !strings.HasPrefix(l, " ") {
			lines[i] = k + ": " + v
			delete(set, strings.TrimSpace(k))
		}
	}
	for k, v := range set {
		lines = append(lines, k+": "+v)
	}
	return writeFileAtomic(csBouncerConf, []byte(strings.Join(lines, "\n")), 0o600)
}

func crowdsecHub(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	if !csInstalled() {
		return nil, errors.New("CrowdSec is not installed")
	}
	action := p["action"]
	if action != "install" && action != "remove" {
		return nil, errors.New("action must be install or remove")
	}
	items := strings.Fields(p["items"])
	for _, it := range items {
		if !csItem.MatchString(it) {
			return nil, fmt.Errorf("invalid collection %q", it)
		}
	}
	if len(items) == 0 {
		return nil, errors.New("no collections given")
	}
	_ = exec.CommandContext(ctx, "cscli", "hub", "update").Run()
	args := append([]string{"collections", action}, items...)
	logf("$ cscli %s", strings.Join(args, " "))
	if out, err := exec.CommandContext(ctx, "cscli", args...).CombinedOutput(); err != nil {
		return nil, errors.New(lastLine(string(out)))
	}
	if err := run(ctx, logf, "systemctl", "reload-or-restart", "crowdsec"); err != nil {
		return nil, err
	}
	return crowdsecStatus(ctx)
}

func crowdsecRemove(ctx context.Context, logf func(string, ...any)) (any, error) {
	if !csInstalled() {
		return crowdsecStatus(ctx)
	}
	for _, u := range []string{"crowdsec-firewall-bouncer", "crowdsec"} {
		_ = run(ctx, logf, "systemctl", "disable", "--now", u)
	}
	aptMu.Lock()
	var err error
	if isRHEL() {
		err = dnfRun(ctx, logf, "remove", "crowdsec", "crowdsec-firewall-bouncer-nftables")
	} else {
		err = aptRun(ctx, logf, "purge", "crowdsec", "crowdsec-firewall-bouncer-nftables")
	}
	aptMu.Unlock()
	if err != nil {
		return nil, err
	}
	_ = os.Remove(csCreds)
	_ = os.Remove(csLocalConf)
	_ = os.Remove(csAcquis)
	return crowdsecStatus(ctx)
}
