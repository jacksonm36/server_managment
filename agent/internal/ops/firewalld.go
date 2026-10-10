package ops

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
)

// firewalld support for RHEL-family hosts. Rules are presented in the same
// shape as UFW's (numbered list), built from the default zone's services,
// ports and rich rules; a rule is deleted by its number in that list.

type fwdRule struct {
	kind  string // service | port | rich
	value string
}

func fwCmd(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "firewall-cmd", args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("firewall-cmd %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func fwdActive(ctx context.Context) bool {
	return exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "firewalld").Run() == nil
}

func fwdRules(ctx context.Context) ([]fwdRule, error) {
	var rules []fwdRule
	for _, q := range []struct{ flag, kind string }{{"--list-services", "service"}, {"--list-ports", "port"}} {
		out, err := fwCmd(ctx, "--permanent", q.flag)
		if err != nil {
			return nil, err
		}
		for _, v := range strings.Fields(out) {
			rules = append(rules, fwdRule{q.kind, v})
		}
	}
	out, err := fwCmd(ctx, "--permanent", "--list-rich-rules")
	if err != nil {
		return nil, err
	}
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			rules = append(rules, fwdRule{"rich", l})
		}
	}
	return rules, nil
}

func firewalldStatus(ctx context.Context) (any, error) {
	if _, err := exec.LookPath("firewall-cmd"); err != nil {
		return map[string]any{"installed": false, "backend": "firewalld"}, nil
	}
	res := map[string]any{"installed": true, "backend": "firewalld", "active": fwdActive(ctx), "ssh_port": sshPort(), "rules": []fwRule{}}
	if res["active"] != true {
		return res, nil
	}
	zone, _ := fwCmd(ctx, "--get-default-zone")
	target, _ := fwCmd(ctx, "--permanent", "--zone="+zone, "--get-target")
	res["defaults"] = fmt.Sprintf("zone %s, incoming %s (unless allowed)", zone, strings.ToLower(strings.ReplaceAll(target, "default", "reject")))
	list, err := fwdRules(ctx)
	if err != nil {
		return nil, err
	}
	rules := []fwRule{}
	for i, r := range list {
		fr := fwRule{Num: i + 1, From: "Anywhere", Action: "ALLOW IN", Raw: r.value}
		switch r.kind {
		case "service":
			fr.To = r.value + " (service)"
		case "port":
			fr.To = r.value
		case "rich":
			fr.To, fr.From, fr.Action = describeRich(r.value)
		}
		rules = append(rules, fr)
	}
	res["rules"] = rules
	return res, nil
}

// describeRich turns a rich rule into To / From / Action columns.
func describeRich(rule string) (to, from, action string) {
	field := func(key string) string {
		i := strings.Index(rule, key+`="`)
		if i < 0 {
			return ""
		}
		rest := rule[i+len(key)+2:]
		return rest[:strings.Index(rest, `"`)]
	}
	to = field("port")
	if p := field("protocol"); p != "" && to != "" {
		to += "/" + p
	}
	if to == "" {
		to = "any"
	}
	from = field("address")
	if from == "" {
		from = "Anywhere"
	}
	switch {
	case strings.Contains(rule, "limit value"):
		action = "LIMIT IN"
	case strings.HasSuffix(rule, "accept"):
		action = "ALLOW IN"
	case strings.Contains(rule, " reject"):
		action = "REJECT IN"
	case strings.Contains(rule, " drop"):
		action = "DENY IN"
	default:
		action = "RULE"
	}
	return
}

func firewalldInstall(ctx context.Context, logf func(string, ...any)) (any, error) {
	if err := pkgInstall(ctx, logf, nil, []string{"firewalld"}); err != nil {
		return nil, err
	}
	return firewalldStatus(ctx)
}

func firewalldEnable(ctx context.Context, logf func(string, ...any)) (any, error) {
	if _, err := exec.LookPath("firewall-cmd"); err != nil {
		return nil, errors.New("firewalld is not installed")
	}
	if err := run(ctx, logf, "systemctl", "enable", "--now", "firewalld"); err != nil {
		return nil, err
	}
	// Keep SSH reachable before anything else, like the UFW path does.
	port := sshPort()
	if port == "22" {
		_, err := fwCmd(ctx, "--permanent", "--add-service=ssh")
		if err != nil {
			return nil, err
		}
	} else if _, err := fwCmd(ctx, "--permanent", "--add-port="+port+"/tcp"); err != nil {
		return nil, err
	}
	if _, err := fwCmd(ctx, "--reload"); err != nil {
		return nil, err
	}
	logf("firewalld enabled; SSH (%s/tcp) allowed", port)
	return firewalldStatus(ctx)
}

func firewalldDisable(ctx context.Context, logf func(string, ...any)) (any, error) {
	if err := run(ctx, logf, "systemctl", "disable", "--now", "firewalld"); err != nil {
		return nil, err
	}
	return firewalldStatus(ctx)
}

func firewalldRuleAdd(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
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
	port = strings.Replace(port, ":", "-", 1) // firewalld writes ranges as a-b
	protos := []string{p["proto"]}
	switch p["proto"] {
	case "", "any":
		protos = []string{"tcp", "udp"}
	case "tcp", "udp":
	default:
		return nil, errors.New("proto must be tcp, udp or any")
	}
	from := strings.TrimSpace(p["from"])
	family := ""
	if from != "" && from != "any" {
		ip := net.ParseIP(from)
		if ip == nil {
			var err error
			if ip, _, err = net.ParseCIDR(from); err != nil {
				return nil, errors.New("source must be an IP address or CIDR")
			}
		}
		family = "ipv4"
		if ip.To4() == nil {
			family = "ipv6"
		}
	} else {
		from = ""
	}
	for _, proto := range protos {
		var args []string
		if action == "allow" && from == "" {
			args = []string{"--permanent", "--add-port=" + port + "/" + proto}
		} else {
			verdict := map[string]string{"allow": "accept", "deny": "drop", "reject": "reject", "limit": `accept limit value="6/m"`}[action]
			rule := "rule"
			if family != "" {
				rule += ` family="` + family + `" source address="` + from + `"`
			}
			rule += ` port port="` + port + `" protocol="` + proto + `" ` + verdict
			args = []string{"--permanent", "--add-rich-rule=" + rule}
		}
		logf("$ firewall-cmd %s", strings.Join(args, " "))
		if _, err := fwCmd(ctx, args...); err != nil {
			return nil, err
		}
	}
	if _, err := fwCmd(ctx, "--reload"); err != nil {
		return nil, err
	}
	return firewalldStatus(ctx)
}

func firewalldRuleDelete(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	n, err := strconv.Atoi(p["num"])
	if err != nil || n < 1 {
		return nil, errors.New("invalid rule number")
	}
	list, err := fwdRules(ctx)
	if err != nil {
		return nil, err
	}
	if n > len(list) {
		return nil, errors.New("no such rule (refresh and try again)")
	}
	r := list[n-1]
	if (r.kind == "service" && r.value == "ssh") || (r.kind == "port" && r.value == sshPort()+"/tcp") {
		return nil, errors.New("this rule keeps SSH reachable; removing it could lock you out (use the terminal if you really need to)")
	}
	flag := map[string]string{"service": "--remove-service=", "port": "--remove-port=", "rich": "--remove-rich-rule="}[r.kind]
	logf("$ firewall-cmd --permanent %s%s", flag, r.value)
	if _, err := fwCmd(ctx, "--permanent", flag+r.value); err != nil {
		return nil, err
	}
	if _, err := fwCmd(ctx, "--reload"); err != nil {
		return nil, err
	}
	return firewalldStatus(ctx)
}

// ---- package helpers for the other features ----

func installFail2banPkgs(ctx context.Context, logf func(string, ...any)) error {
	if isRHEL() {
		if err := ensureEPEL(ctx, logf); err != nil {
			return err
		}
		return pkgInstall(ctx, logf, nil, []string{"fail2ban-server", "python3-systemd", "nftables"})
	}
	return aptInstall(ctx, logf, "fail2ban", "python3-systemd", "nftables")
}

func installDockerPkgs(ctx context.Context, logf func(string, ...any)) error {
	if !isRHEL() {
		return aptInstall(ctx, logf, "docker.io", "docker-cli", "docker-compose")
	}
	// Docker CE from Docker's own repository (RHEL does not ship Docker).
	if err := pkgInstall(ctx, logf, nil, []string{"dnf-plugins-core"}); err != nil {
		return err
	}
	if !fileExists("/etc/yum.repos.d/docker-ce.repo") {
		if err := run(ctx, logf, "dnf", "config-manager", "--add-repo", "https://download.docker.com/linux/rhel/docker-ce.repo"); err != nil {
			return err
		}
	}
	return pkgInstall(ctx, logf, nil, []string{"docker-ce", "docker-ce-cli", "containerd.io", "docker-compose-plugin"})
}
