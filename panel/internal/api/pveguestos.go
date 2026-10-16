package api

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jacksonm36/servermgr/panel/internal/proxmox"
)

// Guest operating systems for the compute tree's icons. A container's
// config names its distribution; a VM's config only says Linux or
// Windows, so for running VMs with the guest agent we also ask the agent.
// Answers are cached per cluster and scope for a few minutes because this
// costs one or two Proxmox calls per guest.

type guestOS struct {
	OS     string `json:"os"`               // distribution id: debian, ubuntu, windows, linux…
	Pretty string `json:"pretty,omitempty"` // e.g. "Debian GNU/Linux 13 (trixie)"
	OSVia  string `json:"os_via,omitempty"` // how the OS was found
	App    string `json:"app,omitempty"`    // detected software (a key of appPatterns)
	AppVia string `json:"app_via,omitempty"`
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
			info := lookupGuestOS(ctx, s.Cl, g, s.TC == nil)
			if info.OS == "" && info.App == "" {
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
func lookupGuestOS(ctx context.Context, cl *proxmox.Client, g proxmox.Resource, probe bool) guestOS {
	info := guestOSFromProxmox(ctx, cl, g)
	if !probe || g.Status != "running" || g.Template == 1 {
		return info.guestOS
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
	// The agent and the container config know the OS better than a banner.
	if pr.OS != "" && (info.OS == "" || info.OS == "linux") {
		info.OS, info.OSVia = pr.OS, pr.OSVia
	}
	return info.guestOS
}

type guestOSInfo struct {
	guestOS
	staticIPs []string // from cloud-init ipconfigN, for VMs without the agent
}

var ipconfigIP = regexp.MustCompile(`(?:^|,)ip=([0-9.]+)/`)

func guestOSFromProxmox(ctx context.Context, cl *proxmox.Client, g proxmox.Resource) guestOSInfo {
	path := proxmox.GuestPath(g.Node, g.Type, g.VMID)
	var cfg map[string]any
	if cl.Do(ctx, http.MethodGet, path+"/config", nil, &cfg) != nil {
		return guestOSInfo{}
	}
	ostype, _ := cfg["ostype"].(string)
	if g.Type == "lxc" {
		if ostype == "unmanaged" {
			ostype = "linux"
		}
		return guestOSInfo{guestOS: guestOS{OS: ostype, OSVia: "container config"}}
	}
	info := guestOSInfo{guestOS: guestOS{OS: qemuOSType(ostype), OSVia: "VM OS type"}}
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
	if cl.Do(actx, http.MethodGet, path+"/agent/get-osinfo", nil, &osinfo) != nil {
		return info
	}
	id := strings.ToLower(osinfo.Result.ID)
	if id == "mswindows" {
		id = "windows"
	}
	if id != "" {
		info.OS, info.OSVia = id, "guest agent"
	}
	info.Pretty = osinfo.Result.PrettyName
	if info.Pretty == "" {
		info.Pretty = osinfo.Result.Name
	}
	return info
}
