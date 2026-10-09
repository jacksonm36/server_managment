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

type fwRule struct {
	Num    int    `json:"num"`
	To     string `json:"to"`
	Action string `json:"action"`
	From   string `json:"from"`
	V6     bool   `json:"v6"`
	Raw    string `json:"raw"`
}

// "[ 1] 22/tcp                     ALLOW IN    Anywhere                   # ssh"
var ufwRuleLine = regexp.MustCompile(`^\[\s*(\d+)\]\s+(.+?)\s{2,}((?:ALLOW|DENY|REJECT|LIMIT)(?: (?:IN|OUT|FWD))?)\s+(.+?)\s*$`)

func ufwCmd(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "ufw", args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("ufw %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func firewallStatus(ctx context.Context) (any, error) {
	if _, err := exec.LookPath("ufw"); err != nil {
		return map[string]any{"installed": false}, nil
	}
	verbose, err := ufwCmd(ctx, "status", "verbose")
	if err != nil {
		return nil, err
	}
	res := map[string]any{"installed": true, "active": strings.Contains(verbose, "Status: active")}
	for _, line := range strings.Split(verbose, "\n") {
		if v, ok := strings.CutPrefix(line, "Default: "); ok {
			res["defaults"] = strings.TrimSpace(v)
		}
		if v, ok := strings.CutPrefix(line, "Logging: "); ok {
			res["logging"] = strings.TrimSpace(v)
		}
	}
	rules := []fwRule{}
	if res["active"] == true {
		numbered, err := ufwCmd(ctx, "status", "numbered")
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(numbered, "\n") {
			m := ufwRuleLine.FindStringSubmatch(strings.TrimSpace(line))
			if m == nil {
				continue
			}
			n, _ := strconv.Atoi(m[1])
			r := fwRule{Num: n, To: m[2], Action: m[3], From: m[4], Raw: strings.TrimSpace(line)}
			r.V6 = strings.Contains(r.To, "(v6)") || strings.Contains(r.From, "(v6)")
			rules = append(rules, r)
		}
	}
	res["rules"] = rules
	res["ssh_port"] = sshPort()
	return res, nil
}

// sshPort returns sshd's listening port, so enabling the firewall never
// locks out SSH.
func sshPort() string {
	if out, err := exec.Command("sshd", "-T").Output(); err == nil {
		for _, l := range strings.Split(string(out), "\n") {
			if p, ok := strings.CutPrefix(l, "port "); ok {
				return strings.TrimSpace(p)
			}
		}
	}
	return "22"
}

var fwPort = regexp.MustCompile(`^\d{1,5}(:\d{1,5})?$`)

func validPort(p string) bool {
	if !fwPort.MatchString(p) {
		return false
	}
	for _, s := range strings.Split(p, ":") {
		n, _ := strconv.Atoi(s)
		if n < 1 || n > 65535 {
			return false
		}
	}
	return true
}

func firewallInstall(ctx context.Context, logf func(string, ...any)) (any, error) {
	if err := aptInstall(ctx, logf, "ufw"); err != nil {
		return nil, err
	}
	return firewallStatus(ctx)
}

func firewallEnable(ctx context.Context, logf func(string, ...any)) (any, error) {
	if _, err := exec.LookPath("ufw"); err != nil {
		return nil, errors.New("ufw is not installed")
	}
	port := sshPort()
	logf("$ ufw limit %s/tcp comment ssh", port)
	if _, err := ufwCmd(ctx, "limit", port+"/tcp", "comment", "ssh"); err != nil {
		return nil, err
	}
	logf("$ ufw --force enable")
	out, err := ufwCmd(ctx, "--force", "enable")
	if err != nil {
		return nil, err
	}
	logf("%s", strings.TrimSpace(out))
	return firewallStatus(ctx)
}

func firewallDisable(ctx context.Context, logf func(string, ...any)) (any, error) {
	logf("$ ufw disable")
	out, err := ufwCmd(ctx, "disable")
	if err != nil {
		return nil, err
	}
	logf("%s", strings.TrimSpace(out))
	return firewallStatus(ctx)
}

var safeComment = regexp.MustCompile(`^[A-Za-z0-9 _.,:/()-]{0,64}$`)

func firewallRuleAdd(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	action := p["action"]
	switch action {
	case "allow", "deny", "reject", "limit":
	default:
		return nil, errors.New("action must be allow, deny, reject or limit")
	}
	port := p["port"]
	if !validPort(port) {
		return nil, errors.New("port must be 1-65535 or a range like 6000:6010")
	}
	proto := p["proto"]
	switch proto {
	case "", "any":
		proto = ""
		if strings.Contains(port, ":") {
			return nil, errors.New("port ranges need tcp or udp")
		}
	case "tcp", "udp":
	default:
		return nil, errors.New("proto must be tcp, udp or any")
	}
	from := strings.TrimSpace(p["from"])
	if from == "" || from == "any" {
		from = "any"
	} else if net.ParseIP(from) == nil {
		if _, _, err := net.ParseCIDR(from); err != nil {
			return nil, errors.New("source must be an IP address or CIDR")
		}
	}
	comment := strings.TrimSpace(p["comment"])
	if !safeComment.MatchString(comment) {
		return nil, errors.New("comment may only contain letters, digits and basic punctuation")
	}
	args := []string{action, "from", from, "to", "any", "port", port}
	if proto != "" {
		args = append(args, "proto", proto)
	}
	if comment != "" {
		args = append(args, "comment", comment)
	}
	logf("$ ufw %s", strings.Join(args, " "))
	out, err := ufwCmd(ctx, args...)
	if err != nil {
		return nil, err
	}
	logf("%s", strings.TrimSpace(out))
	return firewallStatus(ctx)
}

func firewallRuleDelete(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	n, err := strconv.Atoi(p["num"])
	if err != nil || n < 1 {
		return nil, errors.New("invalid rule number")
	}
	logf("$ ufw --force delete %d", n)
	out, err := ufwCmd(ctx, "--force", "delete", strconv.Itoa(n))
	if err != nil {
		return nil, err
	}
	logf("%s", strings.TrimSpace(out))
	return firewallStatus(ctx)
}
