package ops

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Per-site access control: HTTP basic auth and/or an IP allowlist, enforced
// by nginx. The ACME challenge path always stays open so certificates renew.

// Under /etc/nginx: the agent state dir is root-only (0700), which nginx
// workers cannot traverse.
const htpasswdDir = "/etc/nginx/servermgr-htpasswd"

type SiteAccess struct {
	AuthUser string   `json:"auth_user,omitempty"`
	AllowIPs []string `json:"allow_ips,omitempty"`
}

func (s *Site) htpasswdFile() string { return filepath.Join(htpasswdDir, s.Domain) }

// hashPassword produces a SHA-512 crypt hash with openssl, reading the
// password from stdin so it never shows up in the process list.
func hashPassword(ctx context.Context, pw string) (string, error) {
	cmd := exec.CommandContext(ctx, "openssl", "passwd", "-6", "-stdin")
	cmd.Stdin = strings.NewReader(pw + "\n")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("openssl passwd: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// siteAccess updates a site's protection. Params: domain, auth (on|off),
// auth_user, auth_password, allow_ips (space/comma separated; empty = any).
func siteAccess(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	s, err := loadSite(strings.ToLower(p["domain"]))
	if err != nil {
		return nil, err
	}
	acc := SiteAccess{}
	if s.Access != nil {
		acc = *s.Access
	}
	switch p["auth"] {
	case "on":
		user := strings.TrimSpace(p["auth_user"])
		if !unixUser.MatchString(strings.ToLower(user)) && !adminUserLike(user) {
			return nil, errors.New("auth user: 1-32 letters, digits, . _ -")
		}
		if len(p["auth_password"]) < 10 {
			return nil, errors.New("auth password must be at least 10 characters")
		}
		hash, err := hashPassword(ctx, p["auth_password"])
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(htpasswdDir, 0o750); err != nil {
			return nil, err
		}
		if err := writeFileAtomic(s.htpasswdFile(), []byte(user+":"+hash+"\n"), 0o640); err != nil {
			return nil, err
		}
		// nginx workers (web group) must read it; nobody else.
		_ = exec.CommandContext(ctx, "chgrp", webGroup(), s.htpasswdFile(), htpasswdDir).Run()
		acc.AuthUser = user
		logf("password protection on for %s (user %s)", s.Domain, user)
	case "off":
		acc.AuthUser = ""
		_ = os.Remove(s.htpasswdFile())
		logf("password protection off for %s", s.Domain)
	case "":
	default:
		return nil, errors.New("auth must be on or off")
	}
	if v, ok := p["allow_ips"]; ok {
		acc.AllowIPs = nil
		for _, ip := range strings.Fields(strings.ReplaceAll(v, ",", " ")) {
			if net.ParseIP(ip) == nil {
				if _, _, err := net.ParseCIDR(ip); err != nil {
					return nil, fmt.Errorf("invalid IP or CIDR %q", ip)
				}
			}
			acc.AllowIPs = append(acc.AllowIPs, ip)
		}
	}
	s.Access = &acc
	if acc.AuthUser == "" && len(acc.AllowIPs) == 0 {
		s.Access = nil
	}
	if err := applyNginx(ctx, s, logf); err != nil {
		return nil, err
	}
	if err := saveSite(s); err != nil {
		return nil, err
	}
	return webStatus(ctx)
}

func adminUserLike(u string) bool {
	if len(u) < 1 || len(u) > 32 {
		return false
	}
	for _, r := range u {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
