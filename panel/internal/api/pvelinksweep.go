package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/jacksonm36/servermgr/panel/internal/proxmox"
	"github.com/jacksonm36/servermgr/panel/internal/store"
)

// Servers enrolled from Proxmox guests clean themselves up: deleting the
// guest in the panel removes its server, and a sweep removes the server of
// a guest that was destroyed outside the panel, or whose VMID now belongs
// to a different VM (told apart by the VM's SMBIOS UUID), so a stale
// offline card never lingers. A server whose agent is online is never
// removed by the sweep.

var linkSweepEvery = 2 * time.Minute

// RunGuestLinkSweep sweeps the Proxmox guest links until ctx ends.
func (a *API) RunGuestLinkSweep(ctx context.Context) {
	first := time.NewTimer(30 * time.Second)
	defer first.Stop()
	tick := time.NewTicker(linkSweepEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-first.C:
		case <-tick.C:
		}
		a.sweepGuestLinks(ctx)
	}
}

type linkGuest struct{ node, typ string }

const (
	linkKeep     = "keep"
	linkLearn    = "learn"    // remember the VM's UUID
	linkGone     = "gone"     // the guest no longer exists
	linkReplaced = "replaced" // the VMID belongs to a different VM now
)

// linkVerdict decides what to do with one link. g is the guest with the
// link's VMID (nil if there is none) and uuid its SMBIOS UUID ("" for
// containers or when it could not be read).
func linkVerdict(l store.ProxmoxLink, g *linkGuest, uuid string, online bool) string {
	switch {
	case online:
		// A live agent proves the guest is there: learn its identity.
		if g != nil && uuid != "" && l.VMUUID != uuid {
			return linkLearn
		}
		return linkKeep
	case g == nil:
		return linkGone
	case l.VMUUID != "" && uuid != "" && l.VMUUID != uuid:
		return linkReplaced
	}
	return linkKeep
}

func (a *API) sweepGuestLinks(ctx context.Context) {
	clusters, err := a.Store.ListProxmoxClusters(ctx)
	if err != nil {
		a.Log.Warn("guest link sweep", "err", err)
		return
	}
	for _, c := range clusters {
		links, err := a.Store.ListProxmoxLinks(ctx, c.ID)
		if err != nil || len(links) == 0 {
			continue
		}
		_, cl, err := a.pveClient(ctx, c.ID)
		if err != nil {
			continue
		}
		res, err := cl.Resources(ctx)
		if err != nil {
			continue // cluster unreachable: decide nothing
		}
		guests := map[int]*linkGuest{}
		for _, r := range res {
			if r.Type == "qemu" || r.Type == "lxc" {
				guests[r.VMID] = &linkGuest{node: r.Node, typ: r.Type}
			}
		}
		if len(guests) == 0 {
			continue // a token that sees no guests proves nothing
		}
		for _, l := range links {
			g := guests[l.VMID]
			uuid := ""
			if g != nil && g.typ == "qemu" {
				uuid = guestUUID(ctx, cl, g.node, l.VMID)
			}
			switch linkVerdict(l, g, uuid, a.Hub.Online(l.ServerID)) {
			case linkLearn:
				_ = a.Store.SetProxmoxLinkUUID(ctx, c.ID, l.VMID, l.ServerID, uuid)
			case linkGone:
				a.removeGuestServer(ctx, Who{Actor: "system", IP: "panel"}, c, l.VMID, l.ServerID, "the guest no longer exists")
			case linkReplaced:
				a.removeGuestServer(ctx, Who{Actor: "system", IP: "panel"}, c, l.VMID, l.ServerID, "the VMID now belongs to a different VM")
			}
		}
	}
}

// guestUUID returns a VM's SMBIOS UUID, "" if it cannot be read.
func guestUUID(ctx context.Context, cl *proxmox.Client, node string, vmid int) string {
	var cfg struct {
		SMBIOS1 string `json:"smbios1"`
	}
	if err := cl.Do(ctx, http.MethodGet, proxmox.GuestPath(node, "qemu", vmid)+"/config", nil, &cfg); err != nil {
		return ""
	}
	return smbiosUUID(cfg.SMBIOS1)
}

// smbiosUUID picks uuid=… out of a smbios1 option string.
func smbiosUUID(s string) string {
	for _, part := range strings.Split(s, ",") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(part), "uuid="); ok {
			return strings.ToLower(v)
		}
	}
	return ""
}

// removeGuestServer removes the server enrolled from a guest (its link goes
// with it) and records why.
func (a *API) removeGuestServer(ctx context.Context, who Who, c *store.ProxmoxCluster, vmid int, serverID, reason string) {
	name := serverID
	if s, err := a.Store.GetServer(ctx, serverID); err == nil {
		name = s.Name
	}
	err := a.Store.DeleteServer(ctx, serverID)
	if err == nil {
		a.Hub.Forget(serverID)
	}
	a.auditAs(ctx, who, "server.delete", name, map[string]any{
		"server_id": serverID, "cluster": c.Name, "vmid": vmid, "reason": reason,
	}, err == nil)
	if err != nil {
		a.Log.Warn("removing a guest's server", "server", serverID, "err", err)
		return
	}
	a.Log.Info("removed a guest's server", "server", name, "cluster", c.Name, "vmid", vmid, "reason", reason)
}
