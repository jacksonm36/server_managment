package ops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// aptMu serialises everything that takes the dpkg lock, so two jobs started
// at once queue up instead of failing on "Could not get lock".
var aptMu sync.Mutex

var debPkgName = regexp.MustCompile(`^[a-z0-9][a-z0-9+.\-]*(:[a-z0-9]+)?$`)

func aptEnv() []string {
	return append(os.Environ(), "DEBIAN_FRONTEND=noninteractive", "LC_ALL=C", "NEEDRESTART_MODE=a")
}

// aptRun runs apt-get with the given arguments, streaming output.
func aptRun(ctx context.Context, logf func(string, ...any), args ...string) error {
	if _, err := exec.LookPath("apt-get"); err != nil {
		return errors.New("apt-get not found: only Debian/Ubuntu are supported for now")
	}
	full := append([]string{"-y", "-q", "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold"}, args...)
	logf("$ apt-get %s", strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, "apt-get", full...)
	cmd.Env = aptEnv()
	if err := streamCmd(cmd, logf); err != nil {
		return fmt.Errorf("apt-get %s: %w", args[0], err)
	}
	return nil
}

// aptUpdate refreshes package lists. apt-get update can exit 0 even when
// repositories were unreachable, so "Err:" lines are treated as failure;
// otherwise later steps would act on stale lists.
func aptUpdate(ctx context.Context, logf func(string, ...any)) error {
	logf("$ apt-get update")
	cmd := exec.CommandContext(ctx, "apt-get", "update", "-q")
	cmd.Env = aptEnv()
	fetchErrors := 0
	err := streamCmd(cmd, func(format string, a ...any) {
		line := fmt.Sprintf(format, a...)
		if strings.HasPrefix(line, "Err:") {
			fetchErrors++
		}
		logf("%s", line)
	})
	if err != nil {
		return fmt.Errorf("apt-get update: %w", err)
	}
	if fetchErrors > 0 {
		return fmt.Errorf("apt-get update could not fetch %d repository index(es); package lists may be stale (check network/DNS)", fetchErrors)
	}
	return nil
}

// aptInstall installs packages if they are missing.
func aptInstall(ctx context.Context, logf func(string, ...any), pkgs ...string) error {
	aptMu.Lock()
	defer aptMu.Unlock()
	if err := aptUpdate(ctx, logf); err != nil {
		return err
	}
	return aptRun(ctx, logf, append([]string{"install", "--no-install-recommends"}, pkgs...)...)
}

// upgradePackages upgrades everything (params: dist=true for full-upgrade) or
// only the space-separated list in params["packages"].
func upgradePackages(ctx context.Context, params map[string]string, logf func(string, ...any)) (any, error) {
	if isRHEL() {
		return dnfUpgrade(ctx, params, logf)
	}
	pkgs := strings.Fields(params["packages"])
	for _, p := range pkgs {
		if !debPkgName.MatchString(p) {
			return nil, fmt.Errorf("invalid package name %q", p)
		}
	}
	aptMu.Lock()
	defer aptMu.Unlock()
	if err := aptUpdate(ctx, logf); err != nil {
		return nil, err
	}
	var err error
	switch {
	case len(pkgs) > 0:
		err = aptRun(ctx, logf, append([]string{"install", "--only-upgrade"}, pkgs...)...)
	case params["dist"] == "true":
		err = aptRun(ctx, logf, "full-upgrade")
	default:
		err = aptRun(ctx, logf, "upgrade")
	}
	if err != nil {
		return nil, err
	}
	if params["autoremove"] == "true" {
		if err := aptRun(ctx, logf, "autoremove", "--purge"); err != nil {
			return nil, err
		}
	}
	_, rebootErr := os.Stat("/var/run/reboot-required")
	logf("upgrade finished")
	return map[string]any{"reboot_required": rebootErr == nil}, nil
}

// reboot schedules a reboot a few seconds out so the job result reaches the
// panel first.
func reboot(logf func(string, ...any)) (any, error) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return nil, errors.New("systemctl not found")
	}
	logf("rebooting in 3 seconds")
	go func() {
		time.Sleep(3 * time.Second)
		_ = exec.Command("systemctl", "reboot").Run()
	}()
	return map[string]any{"scheduled": true}, nil
}
