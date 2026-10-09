package ops

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

const f2bConfPath = "/etc/fail2ban/jail.d/servermgr.local"

type jail struct {
	Name            string   `json:"name"`
	CurrentlyFailed int      `json:"currently_failed"`
	TotalFailed     int      `json:"total_failed"`
	CurrentlyBanned int      `json:"currently_banned"`
	TotalBanned     int      `json:"total_banned"`
	BannedIPs       []string `json:"banned_ips"`
}

var jailName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

func f2bClient(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "fail2ban-client", args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("fail2ban-client %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// statusField reads "|- Currently failed:\t3" style lines.
func statusField(out, key string) string {
	for _, l := range strings.Split(out, "\n") {
		if i := strings.Index(l, key+":"); i >= 0 {
			return strings.TrimSpace(l[i+len(key)+1:])
		}
	}
	return ""
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func fail2banStatus(ctx context.Context) (any, error) {
	if _, err := exec.LookPath("fail2ban-client"); err != nil {
		return map[string]any{"installed": false}, nil
	}
	active := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "fail2ban").Run() == nil
	res := map[string]any{"installed": true, "active": active, "jails": []jail{}, "config": readF2BConfig()}
	if !active {
		return res, nil
	}
	out, err := f2bClient(ctx, "status")
	if err != nil {
		return nil, err
	}
	jails := []jail{}
	for _, name := range strings.Split(statusField(out, "Jail list"), ",") {
		name = strings.TrimSpace(name)
		if !jailName.MatchString(name) {
			continue
		}
		js, err := f2bClient(ctx, "status", name)
		if err != nil {
			continue
		}
		j := jail{
			Name:            name,
			CurrentlyFailed: atoi(statusField(js, "Currently failed")),
			TotalFailed:     atoi(statusField(js, "Total failed")),
			CurrentlyBanned: atoi(statusField(js, "Currently banned")),
			TotalBanned:     atoi(statusField(js, "Total banned")),
			BannedIPs:       strings.Fields(statusField(js, "Banned IP list")),
		}
		if j.BannedIPs == nil {
			j.BannedIPs = []string{}
		}
		jails = append(jails, j)
	}
	res["jails"] = jails
	return res, nil
}

type f2bConfig struct {
	BanTime  string `json:"bantime"`
	FindTime string `json:"findtime"`
	MaxRetry string `json:"maxretry"`
	IgnoreIP string `json:"ignoreip"`
}

func readF2BConfig() f2bConfig {
	c := f2bConfig{BanTime: "1h", FindTime: "10m", MaxRetry: "5"}
	b, err := os.ReadFile(f2bConfPath)
	if err != nil {
		return c
	}
	section := ""
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "[") {
			section = l
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok || section != "[DEFAULT]" {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "bantime":
			c.BanTime = v
		case "findtime":
			c.FindTime = v
		case "maxretry":
			c.MaxRetry = v
		case "ignoreip":
			c.IgnoreIP = v
		}
	}
	return c
}

var f2bDuration = regexp.MustCompile(`^-?\d{1,7}[smhdw]?$`)

func writeF2BConfig(c f2bConfig) error {
	for _, d := range []string{c.BanTime, c.FindTime} {
		if !f2bDuration.MatchString(d) {
			return fmt.Errorf("invalid duration %q (use e.g. 600, 10m, 1h, 1d)", d)
		}
	}
	if n, err := strconv.Atoi(c.MaxRetry); err != nil || n < 1 || n > 100 {
		return errors.New("maxretry must be 1-100")
	}
	ignore := []string{"127.0.0.1/8", "::1"}
	for _, ip := range strings.Fields(strings.ReplaceAll(c.IgnoreIP, ",", " ")) {
		if ip == "127.0.0.1/8" || ip == "::1" {
			continue
		}
		if net.ParseIP(ip) == nil {
			if _, _, err := net.ParseCIDR(ip); err != nil {
				return fmt.Errorf("invalid ignore address %q", ip)
			}
		}
		ignore = append(ignore, ip)
	}
	conf := fmt.Sprintf(`# Managed by servermgr. Changes here are overwritten from the panel.
[DEFAULT]
bantime = %s
findtime = %s
maxretry = %s
ignoreip = %s

[sshd]
enabled = true
backend = systemd
`, c.BanTime, c.FindTime, c.MaxRetry, strings.Join(ignore, " "))
	if err := os.MkdirAll("/etc/fail2ban/jail.d", 0o755); err != nil {
		return err
	}
	return os.WriteFile(f2bConfPath, []byte(conf), 0o644)
}

func fail2banInstall(ctx context.Context, logf func(string, ...any)) (any, error) {
	if err := aptInstall(ctx, logf, "fail2ban", "python3-systemd"); err != nil {
		return nil, err
	}
	if _, err := os.Stat(f2bConfPath); errors.Is(err, os.ErrNotExist) {
		if err := writeF2BConfig(readF2BConfig()); err != nil {
			return nil, err
		}
		logf("wrote %s", f2bConfPath)
	}
	logf("$ systemctl enable --now fail2ban")
	if out, err := exec.CommandContext(ctx, "systemctl", "enable", "--now", "fail2ban").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("enable fail2ban: %s", strings.TrimSpace(string(out)))
	}
	if out, err := exec.CommandContext(ctx, "systemctl", "restart", "fail2ban").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("restart fail2ban: %s", strings.TrimSpace(string(out)))
	}
	return fail2banStatus(ctx)
}

func fail2banConfigure(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	if _, err := exec.LookPath("fail2ban-client"); err != nil {
		return nil, errors.New("fail2ban is not installed")
	}
	c := f2bConfig{BanTime: p["bantime"], FindTime: p["findtime"], MaxRetry: p["maxretry"], IgnoreIP: p["ignoreip"]}
	if err := writeF2BConfig(c); err != nil {
		return nil, err
	}
	logf("wrote %s, reloading fail2ban", f2bConfPath)
	if _, err := f2bClient(ctx, "reload"); err != nil {
		return nil, err
	}
	return fail2banStatus(ctx)
}

func fail2banBan(ctx context.Context, p map[string]string, ban bool, logf func(string, ...any)) (any, error) {
	if !jailName.MatchString(p["jail"]) {
		return nil, errors.New("invalid jail name")
	}
	if net.ParseIP(p["ip"]) == nil {
		return nil, errors.New("invalid IP address")
	}
	verb := "unbanip"
	if ban {
		verb = "banip"
	}
	logf("$ fail2ban-client set %s %s %s", p["jail"], verb, p["ip"])
	if _, err := f2bClient(ctx, "set", p["jail"], verb, p["ip"]); err != nil {
		return nil, err
	}
	return fail2banStatus(ctx)
}
