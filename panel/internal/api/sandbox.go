package api

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Tools that run user-supplied code (ansible-playbook with its lookups and
// local tasks, tofu with its providers and local-exec) are started inside
// bubblewrap. They see the system (/usr, /etc), the paths a run needs and
// nothing of the panel's own data: not the CA key, not .env, not other
// workspaces.

// sandboxCmd returns a command running name with args where ro paths are
// mounted read-only, rw paths writable, and dir is the working directory.
// The network is shared (servers, registries, the panel's state backend).
func sandboxCmd(ctx context.Context, ro, rw []string, dir, name string, args ...string) (*exec.Cmd, error) {
	bw, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, errors.New("bubblewrap is required to run this safely (apt install bubblewrap)")
	}
	a := []string{"--die-with-parent", "--new-session", "--unshare-all", "--share-net",
		"--ro-bind", "/usr", "/usr", "--ro-bind", "/etc", "/etc",
		"--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--tmpfs", "/run"}
	for _, d := range []string{"/bin", "/sbin", "/lib", "/lib32", "/lib64"} {
		if t, err := os.Readlink(d); err == nil {
			a = append(a, "--symlink", t, d)
		} else if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			a = append(a, "--ro-bind", d, d)
		}
	}
	// resolv.conf often points into /run (systemd-resolved).
	if p, err := filepath.EvalSymlinks("/etc/resolv.conf"); err == nil && strings.HasPrefix(p, "/run/") {
		a = append(a, "--ro-bind", p, p)
	}
	for _, p := range ro {
		a = append(a, "--ro-bind", p, p)
	}
	for _, p := range rw {
		a = append(a, "--bind", p, p)
	}
	a = append(a, "--chdir", dir, "--", name)
	return exec.CommandContext(ctx, bw, append(a, args...)...), nil
}
