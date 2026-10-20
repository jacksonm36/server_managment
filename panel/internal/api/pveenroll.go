package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jacksonm36/servermgr/panel/internal/proxmox"
)

// Enrolling a Proxmox VM as a servermgr server through the QEMU guest
// agent: no SSH, no panel key and no reachable address needed, so it works
// for any running Linux VM with qemu-guest-agent, including ones installed
// from an ISO. Deploys use it when SSH does not get through.

// enrollViaAgent runs the agent installer with a fresh join token inside
// the VM (as root, through the guest agent) and waits for the agent.
func (a *API) enrollViaAgent(ctx context.Context, cl *proxmox.Client, node string, vmid int, name string, logf func(string, ...any)) (string, error) {
	raw, hash := randomToken()
	if err := a.Store.CreateJoinToken(ctx, hash, name, time.Now().Add(joinTokenTTL), nil); err != nil {
		return "", err
	}
	out, err := agentRun(ctx, cl, proxmox.GuestPath(node, "qemu", vmid), []string{"sh", "-c", a.installCommand("sh", raw) + " 2>&1"})
	tail := strings.TrimSpace(out)
	if len(tail) > 300 {
		tail = "…" + tail[len(tail)-300:]
	}
	if tail != "" {
		logf("installer: %s", strings.ReplaceAll(tail, raw, "••••"))
	}
	if err != nil {
		return "", errors.New("agent installer failed: " + strings.ReplaceAll(err.Error(), raw, "••••"))
	}
	return a.waitEnrolled(ctx, hash)
}

// enrollOnlineWait is how long an enrolled agent gets to connect.
var enrollOnlineWait = 30 * time.Second

// waitEnrolled waits for the agent to use the join token and come online.
func (a *API) waitEnrolled(ctx context.Context, hash []byte) (string, error) {
	for i := 0; i < 60; i++ {
		if sid, err := a.Store.JoinTokenServer(ctx, hash); err == nil && sid != "" {
			for t := time.Now(); time.Since(t) < enrollOnlineWait && !a.Hub.Online(sid); {
				time.Sleep(time.Second)
			}
			return sid, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return "", errors.New("the agent did not enroll within 2 minutes")
}

// linkEnrolled ties a freshly enrolled server to its VM: the link shown on
// the guest, the tenant's server group, and Prepare for Ansible.
func (a *API) linkEnrolled(ctx context.Context, who Who, s *pveScope, vmid int, serverID, addr string, logf func(string, ...any)) {
	_ = a.Store.LinkProxmoxGuest(ctx, s.C.ID, vmid, serverID)
	if t := who.Access.tenantGroup(); t != 0 {
		if err := a.Store.AddServerToGroup(ctx, t, serverID); err != nil {
			logf("warning: adding the server to your server group: %v", err)
		}
	}
	if addr == "" {
		return
	}
	logf("enrolled as server %s; restricting the panel key and pinning host keys (Prepare for Ansible)", serverID)
	if _, err := a.Dispatch(ctx, Who{Actor: who.Actor, IP: "panel"}, serverID, "ansible.prepare", map[string]string{"address": addr}); err != nil {
		logf("warning: ansible.prepare: %v", err)
	}
}

// POST /api/v1/proxmox/{id}/guests/{node}/qemu/{vmid}/enroll enrolls a
// running Linux VM through its guest agent.
func (a *API) handlePVEGuestEnroll(w http.ResponseWriter, r *http.Request) {
	s, node, typ, vmid, ok := a.pveGuest(w, r)
	if !ok {
		return
	}
	if typ != "qemu" {
		writeErr(w, http.StatusBadRequest, "containers have no guest agent: add one with the install command from Servers > Add server")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	if links, err := a.Store.ProxmoxLinks(ctx, s.C.ID); err == nil && links[vmid] != "" {
		if _, err := a.Store.GetServer(ctx, links[vmid]); err == nil {
			writeErr(w, http.StatusConflict, "this VM is already a servermgr server")
			return
		}
	}
	cfg, err := guestConfig(ctx, s, node, typ, vmid)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if isWindowsConfig(cfg) {
		writeErr(w, http.StatusBadRequest, errWinEnroll.Error())
		return
	}
	name, _ := cfg["name"].(string)
	if name == "" {
		name = fmt.Sprintf("vm-%d", vmid)
	}
	target := fmt.Sprintf("%s/%s/%d", s.C.Name, node, vmid)
	var log []string
	logf := func(f string, v ...any) { log = append(log, fmt.Sprintf(f, v...)) }
	serverID, err := a.enrollViaAgent(ctx, s.Cl, node, vmid, name, logf)
	if err != nil {
		a.audit(r, "", "proxmox.enroll", target, map[string]string{"error": err.Error()}, false)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	addr, _ := s.Cl.GuestIPv4(ctx, node, vmid)
	a.linkEnrolled(ctx, whoFrom(r), s, vmid, serverID, addr, logf)
	a.audit(r, "", "proxmox.enroll", target, map[string]string{"server_id": serverID}, true)
	writeJSON(w, http.StatusOK, map[string]any{"server_id": serverID, "name": name, "log": log})
}

// enrollContainer waits for a new container to be up with an address, then
// enrolls it over SSH as root with the panel key Proxmox put in. It runs in
// the background; the audit log and the Servers page show the outcome.
func (a *API) enrollContainer(who Who, s *pveScope, node string, vmid int, name, upid string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	target := fmt.Sprintf("%s/%s/%d", s.C.Name, node, vmid)
	logf := func(f string, v ...any) { a.Log.Info("container enrollment: "+fmt.Sprintf(f, v...), "guest", target) }
	fail := func(err error) {
		a.Log.Warn("container enrollment failed", "guest", target, "err", err)
		a.auditAs(context.Background(), who, "proxmox.enroll", target, map[string]string{"error": err.Error()}, false)
	}
	if strings.HasPrefix(upid, "UPID:") {
		if err := s.Cl.WaitTask(ctx, node, upid); err != nil {
			fail(err)
			return
		}
	}
	addr := ""
	for addr == "" {
		ifs, err := s.Cl.GuestInterfaces(ctx, node, "lxc", vmid)
		if err == nil {
			for _, ip := range proxmox.IfaceIPs(ifs) {
				if p := net.ParseIP(ip); p != nil && p.To4() != nil && !p.IsLoopback() && !p.IsLinkLocalUnicast() {
					addr = ip
					break
				}
			}
		}
		if addr != "" {
			break
		}
		select {
		case <-ctx.Done():
			fail(errors.New("the container got no IPv4 address"))
			return
		case <-time.After(5 * time.Second):
		}
	}
	serverID, err := a.enrollOverSSH(ctx, addr, "root", name, logf)
	if err != nil {
		fail(err)
		return
	}
	a.linkEnrolled(ctx, who, s, vmid, serverID, addr, logf)
	a.auditAs(ctx, who, "proxmox.enroll", target, map[string]string{"server_id": serverID}, true)
}
