package ops

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
)

var unixUser = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

type sshKey struct {
	Type        string `json:"type"`
	Fingerprint string `json:"fingerprint"`
	Comment     string `json:"comment"`
	Options     string `json:"options,omitempty"`
}

type loginUser struct {
	Name  string `json:"name"`
	UID   int    `json:"uid"`
	Home  string `json:"home"`
	Shell string `json:"shell"`
}

// loginUsers lists root and regular accounts that have a login shell.
func loginUsers() []loginUser {
	b, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return nil
	}
	var out []loginUser
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(line, ":")
		if len(f) < 7 {
			continue
		}
		uid, _ := strconv.Atoi(f[2])
		shell := f[6]
		if strings.HasSuffix(shell, "nologin") || strings.HasSuffix(shell, "/false") || shell == "" {
			continue
		}
		if uid != 0 && (uid < 1000 || uid >= 65534) {
			continue
		}
		out = append(out, loginUser{Name: f[0], UID: uid, Home: f[5], Shell: shell})
	}
	return out
}

type keyFile struct {
	path     string
	uid, gid int
	home     string
}

func authorizedKeysFor(name string) (*keyFile, error) {
	if name == "" {
		name = "root"
	}
	if !unixUser.MatchString(name) {
		return nil, fmt.Errorf("invalid user name %q", name)
	}
	u, err := user.Lookup(name)
	if err != nil {
		return nil, fmt.Errorf("user %q does not exist", name)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	if u.HomeDir == "" || u.HomeDir == "/" {
		return nil, fmt.Errorf("user %q has no home directory", name)
	}
	return &keyFile{path: filepath.Join(u.HomeDir, ".ssh", "authorized_keys"), uid: uid, gid: gid, home: u.HomeDir}, nil
}

func parseKeyLine(line string) (*sshKey, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return nil, false
	}
	pub, comment, options, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return nil, false
	}
	return &sshKey{
		Type: pub.Type(), Fingerprint: ssh.FingerprintSHA256(pub),
		Comment: comment, Options: strings.Join(options, ","),
	}, true
}

func listSSHKeys(params map[string]string) (any, error) {
	kf, err := authorizedKeysFor(params["user"])
	if err != nil {
		return nil, err
	}
	keys := []sshKey{}
	if b, err := os.ReadFile(kf.path); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if k, ok := parseKeyLine(line); ok {
				keys = append(keys, *k)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return map[string]any{"user": params["user"], "path": kf.path, "keys": keys, "users": loginUsers()}, nil
}

// writeKeys atomically replaces authorized_keys, keeping ownership and modes
// that sshd's StrictModes accepts.
func writeKeys(kf *keyFile, data []byte) error {
	dir := filepath.Dir(kf.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chown(dir, kf.uid, kf.gid); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".authorized_keys.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chown(kf.uid, kf.gid); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), kf.path)
}

func addSSHKey(params map[string]string, logf func(string, ...any)) (any, error) {
	kf, err := authorizedKeysFor(params["user"])
	if err != nil {
		return nil, err
	}
	raw := strings.TrimSpace(params["key"])
	if strings.ContainsAny(raw, "\r\n") {
		return nil, errors.New("paste exactly one public key")
	}
	pub, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(raw))
	if err != nil {
		return nil, errors.New("not a valid OpenSSH public key")
	}
	fp := ssh.FingerprintSHA256(pub)
	if c := strings.TrimSpace(params["comment"]); c != "" {
		comment = c
	}
	// Re-serialise the key ourselves so no options (command=, etc.) sneak in.
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	if comment != "" {
		line += " " + strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return -1
			}
			return r
		}, comment)
	}

	existing, err := os.ReadFile(kf.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, l := range strings.Split(string(existing), "\n") {
		if k, ok := parseKeyLine(l); ok && k.Fingerprint == fp {
			logf("key %s is already authorized for %s", fp, params["user"])
			return listSSHKeys(params)
		}
	}
	if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
		existing = append(existing, '\n')
	}
	if err := writeKeys(kf, append(existing, []byte(line+"\n")...)); err != nil {
		return nil, err
	}
	logf("added %s %s to %s", pub.Type(), fp, kf.path)
	return listSSHKeys(params)
}

func removeSSHKey(params map[string]string, logf func(string, ...any)) (any, error) {
	kf, err := authorizedKeysFor(params["user"])
	if err != nil {
		return nil, err
	}
	fp := params["fingerprint"]
	if !strings.HasPrefix(fp, "SHA256:") {
		return nil, errors.New("fingerprint must look like SHA256:…")
	}
	existing, err := os.ReadFile(kf.path)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	removed := 0
	sc := bufio.NewScanner(bytes.NewReader(existing))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		l := sc.Text()
		if k, ok := parseKeyLine(l); ok && k.Fingerprint == fp {
			removed++
			continue
		}
		out.WriteString(l + "\n")
	}
	if removed == 0 {
		return nil, fmt.Errorf("key %s not found", fp)
	}
	if err := writeKeys(kf, out.Bytes()); err != nil {
		return nil, err
	}
	logf("removed %s from %s", fp, kf.path)
	return listSSHKeys(params)
}
