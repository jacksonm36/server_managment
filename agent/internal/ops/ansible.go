package ops

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Ansible on a managed server: the panel's own SSH key is allowed for root,
// from the panel's address only, sshd and python3 are installed, and the
// host keys are returned so the panel pins them (no trust on first use).

const (
	ansibleTag      = "servermgr-ansible"
	rootAuthorized  = "/root/.ssh/authorized_keys"
	sshHostKeysGlob = "/etc/ssh/ssh_host_*_key.pub"
)

var ed25519Pub = regexp.MustCompile(`^ssh-ed25519 [A-Za-z0-9+/=]{60,80}$`)

func ansiblePrepare(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	key := strings.TrimSpace(p["pubkey"])
	if !ed25519Pub.MatchString(key) {
		return nil, errors.New("pubkey must be an ssh-ed25519 public key")
	}
	var from []string
	for _, f := range strings.FieldsFunc(p["from"], func(r rune) bool { return r == ',' || r == ' ' }) {
		if net.ParseIP(f) == nil {
			if _, _, err := net.ParseCIDR(f); err != nil {
				return nil, fmt.Errorf("invalid from address %q", f)
			}
		}
		from = append(from, f)
	}
	if len(from) == 0 {
		return nil, errors.New("from (the panel's address) is required")
	}
	if err := pkgInstall(ctx, logf, []string{"openssh-server", "python3", "python3-apt"}, []string{"openssh-server", "python3"}); err != nil {
		return nil, err
	}
	if err := run(ctx, logf, "systemctl", "enable", "--now", sshUnit()); err != nil {
		return nil, err
	}
	logf("allowing the panel's Ansible key for root from %s", strings.Join(from, ", "))
	line := fmt.Sprintf(`from="%s",no-agent-forwarding,no-X11-forwarding,no-port-forwarding %s %s`, strings.Join(from, ","), key, ansibleTag)
	if err := setAnsibleKey(ctx, line); err != nil {
		return nil, err
	}
	return ansibleHostInfo(ctx, from[0])
}

func ansibleRevoke(ctx context.Context, logf func(string, ...any)) (any, error) {
	logf("removing the panel's Ansible key from %s", rootAuthorized)
	if err := setAnsibleKey(ctx, ""); err != nil {
		return nil, err
	}
	return map[string]any{"authorized": false}, nil
}

func sshUnit() string {
	if isRHEL() {
		return "sshd"
	}
	return "ssh"
}

// setAnsibleKey replaces the panel's line in root's authorized_keys (or
// removes it when line is empty), keeping every other key.
func setAnsibleKey(ctx context.Context, line string) error {
	if err := os.MkdirAll(filepath.Dir(rootAuthorized), 0o700); err != nil {
		return err
	}
	b, err := os.ReadFile(rootAuthorized)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var keep []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == "" || strings.HasSuffix(strings.TrimSpace(l), " "+ansibleTag) {
			continue
		}
		keep = append(keep, l)
	}
	if line != "" {
		keep = append(keep, line)
	}
	out := strings.Join(keep, "\n")
	if out != "" {
		out += "\n"
	}
	if err := writeFileAtomic(rootAuthorized, []byte(out), 0o600); err != nil {
		return err
	}
	if _, err := exec.LookPath("restorecon"); err == nil {
		_ = exec.CommandContext(ctx, "restorecon", "-R", filepath.Dir(rootAuthorized)).Run()
	}
	return nil
}

// ansibleHostInfo reports what the panel needs to connect: sshd port(s),
// host keys to pin, and whether sshd lets root in with a key from the panel.
func ansibleHostInfo(ctx context.Context, panelAddr string) (any, error) {
	res := map[string]any{"authorized": true}
	var keys []string
	files, _ := filepath.Glob(sshHostKeysGlob)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if fl := strings.Fields(string(b)); len(fl) >= 2 {
			keys = append(keys, fl[0]+" "+fl[1])
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("sshd has no host keys")
	}
	res["host_keys"] = keys
	ip := strings.Split(panelAddr, "/")[0]
	out, err := exec.CommandContext(ctx, "sshd", "-T", "-C", "user=root,host=servermgr,addr="+ip).Output()
	if err != nil {
		return nil, fmt.Errorf("sshd -T: %v", err)
	}
	var ports []string
	for _, l := range strings.Split(string(out), "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(l), " ")
		switch k {
		case "port":
			ports = append(ports, v)
		case "permitrootlogin":
			res["root_login"] = v
		}
	}
	res["ports"] = ports
	res["sshd_active"] = exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", sshUnit()).Run() == nil
	return res, nil
}
