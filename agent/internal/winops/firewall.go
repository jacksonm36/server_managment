//go:build windows

package winops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Windows Defender Firewall through the NetSecurity cmdlets. The panel
// lists and manages the inbound rules in the "servermgr" group (the ones it
// created); Windows' own rules are left alone. Every value reaching
// PowerShell is validated first and passed as a quoted string.

const fwGroup = "servermgr"

// keepOpen are the rules enabling the firewall adds so the server stays
// reachable; they cannot be deleted from the panel.
var keepOpen = map[string]bool{"servermgr-rdp": true, "servermgr-winrm": true, "servermgr-ssh": true}

type fwRule struct {
	Num    int    `json:"num"`
	To     string `json:"to"`
	Action string `json:"action"`
	From   string `json:"from"`
	V6     bool   `json:"v6"`
	Raw    string `json:"raw"`
}

type psRule struct {
	Name    string `json:"name"`
	Display string `json:"display"`
	Action  string `json:"action"`
	Enabled string `json:"enabled"`
	Proto   string `json:"proto"`
	Port    string `json:"port"`
	From    string `json:"from"`
	Desc    string `json:"desc"`
}

const fwStatusScript = `
$prof = @(Get-NetFirewallProfile | ForEach-Object {
  [pscustomobject]@{ name = [string]$_.Name; enabled = ([string]$_.Enabled -eq 'True'); inbound = [string]$_.DefaultInboundAction; outbound = [string]$_.DefaultOutboundAction; log = ([string]$_.LogBlocked -eq 'True') }
})
$rules = @(Get-NetFirewallRule -Group '` + fwGroup + `' -Direction Inbound -ErrorAction SilentlyContinue | ForEach-Object {
  $pf = $_ | Get-NetFirewallPortFilter
  $af = $_ | Get-NetFirewallAddressFilter
  [pscustomobject]@{ name = [string]$_.Name; display = [string]$_.DisplayName; action = [string]$_.Action; enabled = [string]$_.Enabled;
    proto = [string]$pf.Protocol; port = [string](@($pf.LocalPort) -join ','); from = [string](@($af.RemoteAddress) -join ','); desc = [string]$_.Description }
})
$other = @(Get-NetFirewallRule -Direction Inbound -Enabled True -Action Allow -ErrorAction SilentlyContinue | Where-Object { $_.Group -ne '` + fwGroup + `' }).Count
$ssh = [bool](Get-Service -Name sshd -ErrorAction SilentlyContinue)
ConvertTo-Json -Depth 4 -Compress @{ profiles = $prof; rules = $rules; other = $other; ssh = $ssh }
`

// firewallRules numbers rules (sorted by name) as the panel shows them.
func firewallRules(rules []psRule) []fwRule {
	out := []fwRule{}
	for i, r := range rules {
		to := r.Port
		if to == "" || strings.EqualFold(to, "Any") {
			to = "any"
		}
		to = strings.ReplaceAll(to, "-", ":")
		if p := strings.ToLower(r.Proto); p != "" && p != "any" {
			to += "/" + p
		}
		action := "ALLOW IN"
		if !strings.EqualFold(r.Action, "Allow") {
			action = "DENY IN"
		}
		if r.Enabled != "True" {
			action += " (off)"
		}
		from := r.From
		if from == "" || strings.EqualFold(from, "Any") {
			from = "Anywhere"
		}
		raw := r.Name
		if r.Desc != "" {
			raw += " # " + r.Desc
		}
		out = append(out, fwRule{Num: i + 1, To: to, Action: action, From: from, V6: strings.Contains(from, ":"), Raw: raw})
	}
	return out
}

// fwState is the status plus the rule names, in the numbering shown.
func fwState(ctx context.Context) (map[string]any, []psRule, error) {
	var st struct {
		Profiles []struct {
			Name     string `json:"name"`
			Enabled  bool   `json:"enabled"`
			Inbound  string `json:"inbound"`
			Outbound string `json:"outbound"`
			Log      bool   `json:"log"`
		} `json:"profiles"`
		Rules []psRule `json:"rules"`
		Other int      `json:"other"`
		SSH   bool     `json:"ssh"`
	}
	if err := psJSON(ctx, fwStatusScript, &st); err != nil {
		return nil, nil, err
	}
	active := len(st.Profiles) > 0
	defaults := map[string][]string{}
	logging := "off"
	for _, p := range st.Profiles {
		active = active && p.Enabled
		in, out := "deny", "allow" // what NotConfigured means
		if strings.EqualFold(p.Inbound, "Allow") {
			in = "allow"
		}
		if strings.EqualFold(p.Outbound, "Block") {
			out = "deny"
		}
		d := in + " (incoming), " + out + " (outgoing)"
		defaults[d] = append(defaults[d], p.Name)
		if p.Log {
			logging = "on (blocked)"
		}
	}
	var def []string
	for d, names := range defaults {
		if len(defaults) == 1 {
			def = append(def, d)
		} else {
			def = append(def, strings.Join(names, "/")+": "+d)
		}
	}
	sort.Strings(def)
	sort.Slice(st.Rules, func(i, j int) bool { return strings.ToLower(st.Rules[i].Name) < strings.ToLower(st.Rules[j].Name) })
	res := map[string]any{"installed": true, "backend": "windows", "active": active, "defaults": strings.Join(def, "; "),
		"logging": logging, "rules": firewallRules(st.Rules), "other_rules": st.Other}
	if st.SSH {
		res["ssh_port"] = "22"
	}
	return res, st.Rules, nil
}

func firewallStatus(ctx context.Context) (any, error) {
	res, _, err := fwState(ctx)
	return res, err
}

// fwEnableScript keeps RDP (and WinRM and SSH where they run) reachable,
// then turns the firewall on for every profile.
const fwEnableScript = `
$keep = @()
if (Get-Service -Name TermService -ErrorAction SilentlyContinue) { $keep += ,@('servermgr-rdp', '3389', 'RDP') }
if ((Get-Service -Name WinRM -ErrorAction SilentlyContinue).Status -eq 'Running') { $keep += ,@('servermgr-winrm', '5985', 'WinRM') }
if (Get-Service -Name sshd -ErrorAction SilentlyContinue) { $keep += ,@('servermgr-ssh', '22', 'SSH') }
foreach ($k in $keep) {
  if (-not (Get-NetFirewallRule -Name $k[0] -ErrorAction SilentlyContinue)) {
    New-NetFirewallRule -Name $k[0] -DisplayName ('servermgr: ' + $k[2]) -Group '` + fwGroup + `' -Direction Inbound -Action Allow -Protocol TCP -LocalPort $k[1] -Profile Any -Description ($k[2] + ', kept open by servermgr') | Out-Null
    'allowed ' + $k[1] + '/tcp (' + $k[2] + ')'
  }
}
Set-NetFirewallProfile -Profile Domain,Private,Public -Enabled True
'firewall enabled for the Domain, Private and Public profiles'
`

func firewallEnable(ctx context.Context, logf logFunc) (any, error) {
	out, err := powershell(ctx, fwEnableScript)
	logOut(logf, out)
	if err != nil {
		return nil, err
	}
	return firewallStatus(ctx)
}

func firewallDisable(ctx context.Context, logf logFunc) (any, error) {
	out, err := powershell(ctx, "Set-NetFirewallProfile -Profile Domain,Private,Public -Enabled False\n'firewall disabled'")
	logOut(logf, out)
	if err != nil {
		return nil, err
	}
	return firewallStatus(ctx)
}

func logOut(logf logFunc, out string) {
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			logf("%s", l)
		}
	}
}

var (
	fwPort      = regexp.MustCompile(`^\d{1,5}(:\d{1,5})?$`)
	safeComment = regexp.MustCompile(`^[A-Za-z0-9 _.,:/()-]{0,64}$`)
)

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

func firewallRuleAdd(ctx context.Context, p map[string]string, logf logFunc) (any, error) {
	var action string
	switch p["action"] {
	case "allow":
		action = "Allow"
	case "deny", "reject": // Windows Firewall only drops
		action = "Block"
	case "limit":
		return nil, errors.New("Windows Firewall has no rate limiting; use allow or deny")
	default:
		return nil, errors.New("action must be allow, deny or reject")
	}
	port := p["port"]
	if !validPort(port) {
		return nil, errors.New("port must be 1-65535 or a range like 6000:6010")
	}
	var protos []string
	switch p["proto"] {
	case "", "any":
		protos = []string{"TCP", "UDP"}
	case "tcp":
		protos = []string{"TCP"}
	case "udp":
		protos = []string{"UDP"}
	default:
		return nil, errors.New("proto must be tcp, udp or any")
	}
	from := strings.TrimSpace(p["from"])
	if from == "" || from == "any" {
		from = "Any"
	} else if net.ParseIP(from) == nil {
		if _, _, err := net.ParseCIDR(from); err != nil {
			return nil, errors.New("source must be an IP address or CIDR")
		}
	}
	comment := strings.TrimSpace(p["comment"])
	if !safeComment.MatchString(comment) {
		return nil, errors.New("comment may only contain letters, digits and basic punctuation")
	}
	winPort := strings.ReplaceAll(port, ":", "-")
	var script strings.Builder
	for _, proto := range protos {
		b := make([]byte, 6)
		_, _ = rand.Read(b)
		name := "servermgr-" + hex.EncodeToString(b)
		display := "servermgr: " + strings.ToLower(action) + " " + port + "/" + strings.ToLower(proto)
		if comment != "" {
			display += " (" + comment + ")"
		}
		fmt.Fprintf(&script, "New-NetFirewallRule -Name %s -DisplayName %s -Group %s -Direction Inbound -Action %s -Protocol %s -LocalPort %s -RemoteAddress %s -Description %s -Profile Any | Out-Null\n",
			psString(name), psString(display), psString(fwGroup), action, proto, psString(winPort), psString(from), psString(comment))
		fmt.Fprintf(&script, "'added %s'\n", strings.ReplaceAll(display, "'", ""))
	}
	out, err := powershell(ctx, script.String())
	logOut(logf, out)
	if err != nil {
		return nil, err
	}
	return firewallStatus(ctx)
}

func firewallRuleDelete(ctx context.Context, p map[string]string, logf logFunc) (any, error) {
	n, err := strconv.Atoi(p["num"])
	if err != nil || n < 1 {
		return nil, errors.New("invalid rule number")
	}
	_, rules, err := fwState(ctx)
	if err != nil {
		return nil, err
	}
	if n > len(rules) {
		return nil, errors.New("no such rule; refresh and try again")
	}
	r := rules[n-1]
	if keepOpen[r.Name] {
		return nil, errors.New("this rule keeps the server reachable (RDP, WinRM or SSH); removing it could lock you out (use the terminal if you really need to)")
	}
	out, err := powershell(ctx, "Remove-NetFirewallRule -Name "+psString(r.Name)+"\n'deleted "+strings.ReplaceAll(r.Name, "'", "")+"'")
	logOut(logf, out)
	if err != nil {
		return nil, err
	}
	return firewallStatus(ctx)
}
