package ops

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/jacksonm36/servermgr/agent/internal/sysinfo"
)

// OS families the agent supports. Everything distro-specific (package
// manager, firewall, PHP layout, web server user) branches on this.
const (
	famDebian = "debian"
	famRHEL   = "rhel"
)

var (
	famOnce   sync.Once
	famCached string
	famMajor  int
)

// osFamily classifies the host from /etc/os-release (ID and ID_LIKE).
func osFamily() string {
	famOnce.Do(func() {
		rel := sysinfo.OSRelease()
		ids := strings.Fields(strings.ToLower(rel["ID"] + " " + rel["ID_LIKE"]))
		for _, id := range ids {
			switch id {
			case "debian", "ubuntu":
				famCached = famDebian
			case "rhel", "centos", "fedora", "almalinux", "rocky":
				famCached = famRHEL
			}
			if famCached != "" {
				break
			}
		}
		famMajor, _ = strconv.Atoi(strings.SplitN(rel["VERSION_ID"], ".", 2)[0])
	})
	return famCached
}

func isRHEL() bool { return osFamily() == famRHEL }

// webGroup is the group nginx runs as.
func webGroup() string {
	if isRHEL() {
		return "nginx"
	}
	return "www-data"
}

// ---- packages ----

func dnfRun(ctx context.Context, logf func(string, ...any), args ...string) error {
	full := append([]string{"-y", "--setopt=install_weak_deps=False"}, args...)
	logf("$ dnf %s", strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, "dnf", full...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	if err := streamCmd(cmd, logf); err != nil {
		return fmt.Errorf("dnf %s: %w", args[0], err)
	}
	return nil
}

// pkgInstall installs packages with the host's package manager; deb and
// rpm name the same software on each family.
func pkgInstall(ctx context.Context, logf func(string, ...any), deb, rpm []string) error {
	if fam := osFamily(); fam == famRHEL || fam == famDebian {
		want := deb
		if fam == famRHEL {
			want = rpm
		}
		if missing := missingPackages(ctx, fam, want); len(missing) == 0 {
			logf("already installed: %s", strings.Join(want, " "))
			return nil
		}
	}
	switch osFamily() {
	case famRHEL:
		aptMu.Lock()
		defer aptMu.Unlock()
		return dnfRun(ctx, logf, append([]string{"install"}, rpm...)...)
	case famDebian:
		return aptInstall(ctx, logf, deb...)
	}
	return errors.New("unsupported operating system (Debian/Ubuntu and RHEL/AlmaLinux/Rocky are supported)")
}

func missingPackages(ctx context.Context, fam string, pkgs []string) []string {
	var missing []string
	for _, p := range pkgs {
		var ok bool
		if fam == famRHEL {
			ok = exec.CommandContext(ctx, "rpm", "-q", "--whatprovides", p).Run() == nil
		} else {
			out, _ := exec.CommandContext(ctx, "dpkg-query", "-W", "-f=${Status}", p).Output()
			ok = strings.HasSuffix(strings.TrimSpace(string(out)), " installed")
		}
		if !ok {
			missing = append(missing, p)
		}
	}
	return missing
}

// ensureEPEL enables EPEL (fail2ban and friends live there on RHEL).
func ensureEPEL(ctx context.Context, logf func(string, ...any)) error {
	if exec.CommandContext(ctx, "rpm", "-q", "epel-release").Run() == nil {
		return nil
	}
	aptMu.Lock()
	defer aptMu.Unlock()
	return dnfRun(ctx, logf, "install", "epel-release")
}

var rpmNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9+._-]*$`)

// dnfCheckUpdates lists available updates, marking security ones.
func dnfCheckUpdates(ctx context.Context, logf func(string, ...any)) (any, error) {
	aptMu.Lock()
	defer aptMu.Unlock()
	logf("$ dnf makecache --refresh")
	cmd := exec.CommandContext(ctx, "dnf", "-q", "makecache", "--refresh")
	if err := streamCmd(cmd, logf); err != nil {
		return nil, fmt.Errorf("dnf makecache failed (check network/DNS): %w", err)
	}
	installed := map[string]string{}
	if out, err := exec.CommandContext(ctx, "rpm", "-qa", "--qf", "%{NAME}.%{ARCH}|%{EVR}\n").Output(); err == nil {
		for _, l := range strings.Split(string(out), "\n") {
			if k, v, ok := strings.Cut(l, "|"); ok {
				installed[k] = v
			}
		}
	}
	logf("$ dnf repoquery --upgrades")
	out, err := exec.CommandContext(ctx, "dnf", "-q", "repoquery", "--upgrades", "--latest-limit=1", "--qf", "%{name}|%{arch}|%{evr}|%{repoid}\n").Output()
	if err != nil {
		return nil, fmt.Errorf("dnf repoquery: %w", err)
	}
	security := map[string]bool{}
	if out, err := exec.CommandContext(ctx, "dnf", "-q", "updateinfo", "list", "--security", "--updates").Output(); err == nil {
		sc := bufio.NewScanner(strings.NewReader(string(out)))
		for sc.Scan() {
			f := strings.Fields(sc.Text())
			if len(f) >= 3 {
				security[f[len(f)-1]] = true // NEVRA of the fixed package
			}
		}
	}
	updates := []pkgUpdate{}
	nsec := 0
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(l, "|")
		if len(f) != 4 {
			continue
		}
		u := pkgUpdate{Name: f[0], Candidate: f[2], Repository: f[3], Current: installed[f[0]+"."+f[1]]}
		// updateinfo prints NEVRA with the same epoch convention as %{evr}.
		u.Security = security[fmt.Sprintf("%s-%s.%s", f[0], f[2], f[1])]
		if u.Security {
			nsec++
		}
		updates = append(updates, u)
	}
	sort.Slice(updates, func(i, j int) bool { return updates[i].Name < updates[j].Name })
	logf("%d package(s) can be upgraded (%d security)", len(updates), nsec)
	return map[string]any{"updates": updates, "count": len(updates), "security_count": nsec, "reboot_required": rhelRebootRequired(ctx)}, nil
}

// rhelRebootRequired uses needs-restarting (dnf-utils) when available.
func rhelRebootRequired(ctx context.Context) bool {
	if _, err := exec.LookPath("needs-restarting"); err != nil {
		return false
	}
	var ee *exec.ExitError
	err := exec.CommandContext(ctx, "needs-restarting", "-r").Run()
	return errors.As(err, &ee) && ee.ExitCode() == 1
}

func dnfUpgrade(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	pkgs := strings.Fields(p["packages"])
	for _, n := range pkgs {
		if !rpmNameRe.MatchString(n) {
			return nil, fmt.Errorf("invalid package name %q", n)
		}
	}
	aptMu.Lock()
	defer aptMu.Unlock()
	if err := dnfRun(ctx, logf, append([]string{"upgrade", "--refresh"}, pkgs...)...); err != nil {
		return nil, err
	}
	if p["autoremove"] == "true" {
		if err := dnfRun(ctx, logf, "autoremove"); err != nil {
			return nil, err
		}
	}
	logf("upgrade finished")
	return map[string]any{"reboot_required": rhelRebootRequired(ctx)}, nil
}

// ---- SELinux ----

func selinuxEnforcing() bool {
	out, err := exec.Command("getenforce").Output()
	return err == nil && strings.TrimSpace(string(out)) == "Enforcing"
}

// selinuxWeb labels the sites tree for nginx/php-fpm and lets nginx proxy
// to local apps. It is a no-op when SELinux is off.
func selinuxWeb(ctx context.Context, logf func(string, ...any), proxy bool) error {
	if !isRHEL() || !selinuxEnforcing() {
		return nil
	}
	if _, err := exec.LookPath("semanage"); err != nil {
		if err := pkgInstall(ctx, logf, nil, []string{"policycoreutils-python-utils"}); err != nil {
			return err
		}
	}
	spec := sitesRoot + "(/.*)?"
	if exec.CommandContext(ctx, "semanage", "fcontext", "-a", "-t", "httpd_sys_rw_content_t", spec).Run() != nil {
		_ = exec.CommandContext(ctx, "semanage", "fcontext", "-m", "-t", "httpd_sys_rw_content_t", spec).Run()
	}
	if err := run(ctx, logf, "restorecon", "-R", sitesRoot); err != nil {
		return err
	}
	if proxy {
		return run(ctx, logf, "setsebool", "-P", "httpd_can_network_connect", "1")
	}
	return nil
}
