package api

import (
	"context"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/jacksonm36/servermgr/panel/internal/proxmox"
	"github.com/jacksonm36/servermgr/panel/internal/store"
)

// Guest operating systems for the compute tree's icons. A container's
// config names its distribution; a VM's config only says Linux or
// Windows, so for running VMs with the guest agent we also ask the agent.
// Answers are cached per cluster and scope for a few minutes because this
// costs one or two Proxmox calls per guest.
//
// The agent's answer is the guest's own claim and is treated as hostile:
// the id must match the os-release charset, text is stripped of control
// characters and capped. The newest answer per VM is stored, keyed by the
// VM's SMBIOS UUID so a new VM reusing the VMID does not inherit it, and
// shown while the VM is stopped. Without one the OS is guessed from the
// attached ISO, the notes, the name and tags, then from the VM's OS type.

type guestOS struct {
	OS     string `json:"os"`               // distribution id: debian, ubuntu, windows, linux…
	Pretty string `json:"pretty,omitempty"` // e.g. "Debian GNU/Linux 13 (trixie)"
	OSVia  string `json:"os_via,omitempty"` // how the OS was found
	App    string `json:"app,omitempty"`    // detected software (a key of appPatterns)
	AppVia string `json:"app_via,omitempty"`
	// Words from the notes, ISO names and hostname that may name the
	// software, for when nothing was detected.
	Hints string `json:"hints,omitempty"`
}

type guestOSCache struct {
	at   time.Time
	data map[string]guestOS
}

var (
	guestOSMu     sync.Mutex
	guestOSCached = map[string]guestOSCache{}
)

const guestOSTTL = 5 * time.Minute

// qemuOSType maps a VM's ostype to a family when the agent says nothing.
func qemuOSType(t string) string {
	switch {
	case strings.HasPrefix(t, "win"), t == "wxp", t == "wvista", strings.HasPrefix(t, "w2k"):
		return "windows"
	case t == "l26" || t == "l24":
		return "linux"
	case t == "solaris":
		return "solaris"
	}
	return ""
}

func (a *API) handlePVEGuestOS(w http.ResponseWriter, r *http.Request) {
	s, err := a.pveScope(r)
	if err != nil {
		scopeErr(w, err)
		return
	}
	key := s.C.ID
	if s.TC != nil {
		key += fmt.Sprintf("/t%d", s.TC.TenantID)
	}
	guestOSMu.Lock()
	c, ok := guestOSCached[key]
	guestOSMu.Unlock()
	if ok && time.Since(c.at) < guestOSTTL && r.URL.Query().Get("refresh") == "" {
		writeJSON(w, http.StatusOK, c.data)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	res, err := s.Cl.Resources(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	var guests []proxmox.Resource
	for _, x := range res {
		if (x.Type == "qemu" || x.Type == "lxc") && (s.TC == nil || x.Pool == s.TC.Pool) {
			guests = append(guests, x)
		}
	}
	facts, err := a.Store.GuestFacts(ctx, s.C.ID)
	if err != nil {
		a.Log.Warn("guest facts", "err", err)
		facts = map[int]store.GuestFact{}
	}
	out := make(map[string]guestOS, len(guests))
	var mu sync.Mutex
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for _, g := range guests {
		wg.Add(1)
		sem <- struct{}{}
		go func(g proxmox.Resource) {
			defer wg.Done()
			defer func() { <-sem }()
			info, fact := lookupGuestOS(ctx, s.Cl, g, s.TC == nil, facts[g.VMID])
			if fact != nil {
				if err := a.Store.SaveGuestFact(ctx, s.C.ID, *fact); err != nil {
					a.Log.Warn("save guest fact", "vmid", g.VMID, "err", err)
				}
			}
			if info.OS == "" && info.App == "" && info.Hints == "" {
				return
			}
			mu.Lock()
			out[fmt.Sprintf("%s/%d", g.Node, g.VMID)] = info
			mu.Unlock()
		}(g)
	}
	wg.Wait()
	guestOSMu.Lock()
	guestOSCached[key] = guestOSCache{at: time.Now(), data: out}
	guestOSMu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

// lookupGuestOS finds a guest's OS from Proxmox (config, guest agent) and,
// for platform users, what it runs by looking at it from the outside
// (pvedetect.go).
// It also returns a fact to store when the guest agent answered.
func lookupGuestOS(ctx context.Context, cl *proxmox.Client, g proxmox.Resource, probe bool, prev store.GuestFact) (guestOS, *store.GuestFact) {
	info := guestOSFromProxmox(ctx, cl, g)
	if g.Type == "qemu" && info.fact == nil && info.OSVia != "" {
		if prev.OS != "" && prev.VMUUID == info.uuid {
			info.OS, info.Pretty = prev.OS, prev.Pretty
			info.OSVia = "guest agent, last seen " + prev.SeenAt.Format("2006-01-02")
			info.Hints = hintWords(info.Hints, prev.Hostname)
		} else if info.guess != "" {
			info.OS, info.OSVia = info.guess, "ISO, notes or name"
		}
	}
	if !probe || g.Status != "running" || g.Template == 1 {
		return info.guestOS, info.fact
	}
	ictx, cancel := context.WithTimeout(ctx, 3*time.Second)
	ifs, _ := cl.GuestInterfaces(ictx, g.Node, g.Type, g.VMID)
	cancel()
	ips := proxmox.IfaceIPs(ifs)
	if len(ips) == 0 {
		ips = info.staticIPs
	}
	pr := probeGuest(ctx, fmt.Sprintf("%s/%d", g.Node, g.VMID), probeable(ips))
	info.App, info.AppVia = pr.App, pr.AppVia
	// The agent and the container config know the OS better than a banner;
	// a banner beats a guess.
	if pr.OS != "" && (info.OS == "" || info.OS == "linux" || info.OSVia == "ISO, notes or name") {
		info.OS, info.OSVia = pr.OS, pr.OSVia
	}
	return info.guestOS, info.fact
}

type guestOSInfo struct {
	guestOS
	staticIPs []string         // from cloud-init ipconfigN, for VMs without the agent
	uuid      string           // the VM's SMBIOS UUID
	guess     string           // OS guessed from ISO, notes, name and tags
	fact      *store.GuestFact // set when the guest agent answered
}

var (
	osIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)
	hostRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,62}$`)
	wordRe = regexp.MustCompile(`[^a-z0-9.-]+`)
	isoRe  = regexp.MustCompile(`^(ide|sata|scsi)\d+$`)
	uuidRe = regexp.MustCompile(`(?:^|,)uuid=([0-9a-fA-F-]{36})`)
)

// cleanID accepts an OS id only in the os-release charset.
func cleanID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "mswindows" {
		return "windows"
	}
	if !osIDRe.MatchString(s) {
		return ""
	}
	return s
}

// cleanText drops control and formatting characters, collapses spaces and
// caps the length in runes.
func cleanText(s string, max int) string {
	var b strings.Builder
	n, space := 0, false
	for _, r := range s {
		if n >= max {
			break
		}
		if unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if !unicode.IsPrint(r) {
			continue
		}
		if space {
			b.WriteByte(' ')
			n++
			space = false
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// hintWords reduces free text to plain lowercase words.
func hintWords(parts ...string) string {
	h := strings.TrimSpace(wordRe.ReplaceAllString(strings.ToLower(strings.Join(parts, " ")), " "))
	if len(h) > 400 {
		h = h[:400]
	}
	return h
}

// osHints guesses an OS from words. The first match wins, so appliances
// come before the systems they run on.
var osHints = []struct {
	re *regexp.Regexp
	os string
}{
	{regexp.MustCompile(`\bwin(dows)?[ ._-]?(server|1[01]\b|20(1[69]|2[25]))|\bwindows\b`), "windows"},
	{regexp.MustCompile(`truenas[ ._-]?scale`), "debian"},
	{regexp.MustCompile(`truenas|freenas|opnsense|pfsense|freebsd`), "freebsd"},
	{regexp.MustCompile(`proxmox|\bpve\b|\bpbs\b|\bpmg\b`), "debian"},
	{regexp.MustCompile(`debian`), "debian"},
	{regexp.MustCompile(`ubuntu`), "ubuntu"},
	{regexp.MustCompile(`rocky`), "rocky"},
	{regexp.MustCompile(`alma`), "almalinux"},
	{regexp.MustCompile(`centos`), "centos"},
	{regexp.MustCompile(`fedora`), "fedora"},
	{regexp.MustCompile(`\brhel|redhat|red-hat`), "rhel"},
	{regexp.MustCompile(`archlinux|\barch\b`), "arch"},
	{regexp.MustCompile(`opensuse|\bsles\b|\bsuse\b`), "opensuse"},
	{regexp.MustCompile(`alpine`), "alpine"},
	{regexp.MustCompile(`nixos`), "nixos"},
	{regexp.MustCompile(`\bkali`), "kali"},
	{regexp.MustCompile(`linuxmint|\bmint\b`), "linuxmint"},
	{regexp.MustCompile(`gentoo`), "gentoo"},
	{regexp.MustCompile(`openwrt`), "openwrt"},
	{regexp.MustCompile(`talos`), "talos"},
}

func osFromHints(h string) string {
	for _, x := range osHints {
		if x.re.MatchString(h) {
			return x.os
		}
	}
	return ""
}

var ipconfigIP = regexp.MustCompile(`(?:^|,)ip=([0-9.]+)/`)

func guestOSFromProxmox(ctx context.Context, cl *proxmox.Client, g proxmox.Resource) guestOSInfo {
	gp := proxmox.GuestPath(g.Node, g.Type, g.VMID)
	var cfg map[string]any
	if cl.Do(ctx, http.MethodGet, gp+"/config", nil, &cfg) != nil {
		return guestOSInfo{}
	}
	str := func(k string) string { v, _ := cfg[k].(string); return v }
	var isos []string
	for k, v := range cfg {
		val, _ := v.(string)
		if !isoRe.MatchString(k) || !(strings.Contains(val, "media=cdrom") || strings.Contains(val, ":iso/")) {
			continue
		}
		vol, _, _ := strings.Cut(val, ",")
		if vol != "none" && vol != "cdrom" && !strings.Contains(vol, "cloudinit") {
			isos = append(isos, path.Base(vol))
		}
	}
	hints := hintWords(append(isos, str("description"), str("hostname"))...)
	ostype := cleanID(str("ostype"))
	if g.Type == "lxc" {
		if ostype == "unmanaged" || ostype == "" {
			ostype = "linux"
		}
		return guestOSInfo{guestOS: guestOS{OS: ostype, OSVia: "container config", Hints: hints}}
	}
	info := guestOSInfo{guestOS: guestOS{OS: qemuOSType(ostype), OSVia: "VM OS type", Hints: hints}}
	info.guess = osFromHints(hintWords(append(isos, g.Name, g.Tags, str("description"))...))
	if m := uuidRe.FindStringSubmatch(str("smbios1")); m != nil {
		info.uuid = strings.ToLower(m[1])
	}
	for i := 0; i < 32; i++ {
		if v, ok := cfg[fmt.Sprintf("ipconfig%d", i)].(string); ok {
			if m := ipconfigIP.FindStringSubmatch(v); m != nil {
				info.staticIPs = append(info.staticIPs, m[1])
			}
		}
	}
	agent, _ := cfg["agent"].(string)
	if g.Status != "running" || g.Template == 1 || !(strings.HasPrefix(agent, "1") || strings.Contains(agent, "enabled=1")) {
		return info
	}
	actx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var osinfo struct {
		Result struct {
			ID         string `json:"id"`
			PrettyName string `json:"pretty-name"`
			Name       string `json:"name"`
		} `json:"result"`
	}
	if cl.Do(actx, http.MethodGet, gp+"/agent/get-osinfo", nil, &osinfo) != nil {
		return info
	}
	id := cleanID(osinfo.Result.ID)
	if id == "" {
		return info
	}
	info.OS, info.OSVia = id, "guest agent"
	info.Pretty = cleanText(osinfo.Result.PrettyName, 80)
	if info.Pretty == "" {
		info.Pretty = cleanText(osinfo.Result.Name, 80)
	}
	var hn struct {
		Result struct {
			HostName string `json:"host-name"`
		} `json:"result"`
	}
	host := ""
	if cl.Do(actx, http.MethodGet, gp+"/agent/get-host-name", nil, &hn) == nil {
		if h := strings.ToLower(strings.TrimSpace(hn.Result.HostName)); hostRe.MatchString(h) {
			host = h
		}
	}
	info.Hints = hintWords(info.Hints, host)
	info.fact = &store.GuestFact{VMID: g.VMID, VMUUID: info.uuid, OS: id, Pretty: info.Pretty, Hostname: host}
	return info
}
