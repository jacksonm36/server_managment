package api

import (
	"cmp"
	"context"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jacksonm36/servermgr/panel/internal/proxmox"
)

// pveNetBridge is a bridge a NIC can be put on.
type pveNetBridge struct {
	Iface     string `json:"iface"`
	Comment   string `json:"comment,omitempty"`
	VLANAware bool   `json:"vlan_aware"`
}

// pveNetVLAN is a VLAN tag already in use on the node, with who uses it.
type pveNetVLAN struct {
	Tag     int      `json:"tag"`
	Bridges []string `json:"bridges"`
	Guests  []string `json:"guests"`
}

var nicTag = regexp.MustCompile(`(?:^|,)tag=(\d{1,4})(?:,|$)`)
var nicBridge = regexp.MustCompile(`(?:^|,)bridge=([^,]+)`)

// handlePVENetChoices lists a node's bridges (with their comments and
// whether they are VLAN-aware) and the VLAN tags its guests already use,
// for the network pickers of the deploy form.
func (a *API) handlePVENetChoices(w http.ResponseWriter, r *http.Request) {
	node := r.PathValue("node")
	if !pveNode.MatchString(node) {
		writeErr(w, http.StatusBadRequest, "bad node")
		return
	}
	s, err := a.pveScope(r)
	if err != nil {
		scopeErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	var nets []struct {
		Iface     string `json:"iface"`
		Comments  string `json:"comments"`
		VLANAware any    `json:"bridge_vlan_aware"`
	}
	bridges := []pveNetBridge{}
	if err := s.Cl.Do(ctx, http.MethodGet, "/nodes/"+url.PathEscape(node)+"/network", url.Values{"type": {"any_bridge"}}, &nets); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	for _, n := range nets {
		if s.TC != nil && !slices.Contains(s.TC.Bridges, n.Iface) {
			continue
		}
		aware := false
		switch v := n.VLANAware.(type) {
		case float64:
			aware = v == 1
		case string:
			aware = v == "1"
		}
		bridges = append(bridges, pveNetBridge{Iface: n.Iface, Comment: strings.TrimSpace(n.Comments), VLANAware: aware})
	}
	slices.SortFunc(bridges, func(x, y pveNetBridge) int { return strings.Compare(x.Iface, y.Iface) })

	vlans := []pveNetVLAN{}
	if s.TC != nil {
		for _, t := range s.TC.VLANs {
			vlans = append(vlans, pveNetVLAN{Tag: t, Bridges: []string{}, Guests: []string{}})
		}
	} else if res, err := s.Cl.Resources(ctx); err == nil {
		byTag := map[int]*pveNetVLAN{}
		var mu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, 8)
		for _, g := range res {
			if (g.Type != "qemu" && g.Type != "lxc") || g.Node != node {
				continue
			}
			wg.Add(1)
			go func(g proxmox.Resource) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				var cfg map[string]any
				if s.Cl.Do(ctx, http.MethodGet, proxmox.GuestPath(node, g.Type, g.VMID)+"/config", nil, &cfg) != nil {
					return
				}
				name := cmp.Or(g.Name, strconv.Itoa(g.VMID))
				mu.Lock()
				defer mu.Unlock()
				for k, v := range cfg {
					sv, ok := v.(string)
					if !ok || !pveNetKey.MatchString(k) {
						continue
					}
					m := nicTag.FindStringSubmatch(sv)
					if m == nil {
						continue
					}
					tag, _ := strconv.Atoi(m[1])
					if tag < 1 || tag > 4094 {
						continue
					}
					e := byTag[tag]
					if e == nil {
						e = &pveNetVLAN{Tag: tag, Bridges: []string{}, Guests: []string{}}
						byTag[tag] = e
					}
					if b := nicBridge.FindStringSubmatch(sv); b != nil && !slices.Contains(e.Bridges, b[1]) {
						e.Bridges = append(e.Bridges, b[1])
					}
					if !slices.Contains(e.Guests, name) {
						e.Guests = append(e.Guests, name)
					}
				}
			}(g)
		}
		wg.Wait()
		for _, e := range byTag {
			slices.Sort(e.Bridges)
			slices.Sort(e.Guests)
			vlans = append(vlans, *e)
		}
	}
	slices.SortFunc(vlans, func(x, y pveNetVLAN) int { return x.Tag - y.Tag })
	writeJSON(w, http.StatusOK, map[string]any{"bridges": bridges, "vlans": vlans})
}
