package api

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jacksonm36/servermgr/panel/internal/proxmox"
)

// Enrolling a Proxmox VM as a servermgr server through the QEMU guest
// agent: no SSH, no panel key and no reachable address needed, so it works
// for any running VM with the guest agent, Linux or Windows, including ones
// installed from an ISO. Deploys use it when SSH does not get through, and
// always for Windows.
//
// The one-time join token never appears on a command line (where any user
// of the guest could read it in the process list): Linux gets it on the
// installer's stdin, Windows in a file only SYSTEM and Administrators can
// read. The agent build and the panel's certificate are both checked
// against what the panel says before anything runs.

// enrollScript is the Linux installer run that reads the join token from
// its first line of input: the token reaches the agent in its environment.
func (a *API) enrollScript() string {
	return "IFS= read -r SM_JOIN_TOKEN && export SM_JOIN_TOKEN && " + a.installCommand("sh", "") + " 2>&1"
}

// enrollViaAgent runs the agent installer with a fresh join token inside
// the VM (as root, through the guest agent) and waits for the agent.
func (a *API) enrollViaAgent(ctx context.Context, cl *proxmox.Client, node string, vmid int, name string, logf func(string, ...any)) (string, error) {
	raw, hash := randomToken()
	if err := a.Store.CreateJoinToken(ctx, hash, name, time.Now().Add(joinTokenTTL), nil); err != nil {
		return "", err
	}
	out, err := agentRunInput(ctx, cl, proxmox.GuestPath(node, "qemu", vmid), []string{"sh", "-c", a.enrollScript()}, raw+"\n")
	logTail(logf, out, raw)
	if err != nil {
		return "", errors.New("agent installer failed: " + strings.ReplaceAll(err.Error(), raw, "••••"))
	}
	return a.waitEnrolled(ctx, hash)
}

// logTail logs the end of an installer's output, without the token.
func logTail(logf func(string, ...any), out, token string) {
	tail := strings.TrimSpace(strings.ReplaceAll(out, token, "••••"))
	if len(tail) > 300 {
		tail = "…" + tail[len(tail)-300:]
	}
	if tail != "" {
		logf("installer: %s", tail)
	}
}

//go:embed windows/agent-install.ps1
var winAgentInstall string

// winEnrollDir holds the join token and the agent download on Windows.
const winEnrollDir = `C:\Windows\servermgr\enroll`

// winEnrollPrep makes winEnrollDir readable by SYSTEM and Administrators
// only, before the token is written into it.
const winEnrollPrep = `$ErrorActionPreference = 'Stop'
$d = '` + winEnrollDir + `'
New-Item -ItemType Directory -Force -Path $d | Out-Null
Get-ChildItem -Path $d -Force | Remove-Item -Force -Recurse
& icacls.exe $d /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' | Out-Null
if ($LASTEXITCODE -ne 0) { throw "icacls failed ($LASTEXITCODE)" }
`

// agentSHA256 is the SHA-256 of an agent build the panel serves.
func (a *API) agentSHA256(file string) (string, error) {
	f, err := os.Open(filepath.Join(a.Cfg.AgentBinDir, file))
	if err != nil {
		return "", fmt.Errorf("the panel has no %s build (run make agent): %w", file, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// enrollWindowsViaAgent installs the Windows agent as a service through
// the guest agent (as SYSTEM) and waits for it to enroll.
func (a *API) enrollWindowsViaAgent(ctx context.Context, cl *proxmox.Client, node string, vmid int, name string, logf func(string, ...any)) (string, error) {
	if !strings.HasPrefix(a.Cfg.PublicURL, "https://") {
		return "", errors.New("Windows enrollment needs the panel on HTTPS")
	}
	sum, err := a.agentSHA256("servermgr-agent-windows-amd64.exe")
	if err != nil {
		return "", err
	}
	gp := proxmox.GuestPath(node, "qemu", vmid)
	if _, err := agentRun(ctx, cl, gp, psCommand(winEnrollPrep)); err != nil {
		return "", fmt.Errorf("preparing %s: %w", winEnrollDir, err)
	}
	raw, hash := randomToken()
	if err := a.Store.CreateJoinToken(ctx, hash, name, time.Now().Add(joinTokenTTL), nil); err != nil {
		return "", err
	}
	if err := cl.Do(ctx, http.MethodPost, gp+"/agent/file-write", url.Values{"file": {winEnrollDir + `\join-token`}, "content": {raw}}, nil); err != nil {
		return "", agentErr(err)
	}
	logf("installing the servermgr agent service (build %s…)", sum[:12])
	script := "$P = " + psData(map[string]string{"panel": a.Cfg.PublicURL, "sha256": sum, "ca": a.CA.Fingerprint()}) + "\n" + winAgentInstall
	out, err := agentRun(ctx, cl, gp, psCommand(script))
	logTail(logf, out, raw)
	if err != nil {
		return "", errors.New("agent installer failed: " + strings.ReplaceAll(err.Error(), raw, "••••"))
	}
	return a.waitEnrolled(ctx, hash)
}

// waitWindowsReady waits until a new Windows VM has its name and
// Cloudbase-Init is done (it reboots the VM while it works).
func waitWindowsReady(ctx context.Context, cl *proxmox.Client, node string, vmid int, name string, logf func(string, ...any)) error {
	want := strings.ToUpper(name)
	if len(want) > 15 {
		want = want[:15]
	}
	gp := proxmox.GuestPath(node, "qemu", vmid)
	logf("waiting for Windows to finish its first boot")
	for {
		out, err := agentRun(ctx, cl, gp, psCommand(`"$env:COMPUTERNAME|" + (Get-Service -Name cloudbase-init -ErrorAction SilentlyContinue).Status`))
		if err == nil {
			host, status, _ := strings.Cut(strings.TrimSpace(out), "|")
			if strings.EqualFold(host, want) && (status == "" || status == "Stopped") {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return errors.New("Windows did not finish its first boot in time")
		case <-time.After(winPoll):
		}
	}
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
// running Linux or Windows VM through its guest agent.
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
	enroll := a.enrollViaAgent
	if isWindowsConfig(cfg) {
		enroll = a.enrollWindowsViaAgent
	}
	name, _ := cfg["name"].(string)
	if name == "" {
		name = fmt.Sprintf("vm-%d", vmid)
	}
	target := fmt.Sprintf("%s/%s/%d", s.C.Name, node, vmid)
	var log []string
	logf := func(f string, v ...any) { log = append(log, fmt.Sprintf(f, v...)) }
	serverID, err := enroll(ctx, s.Cl, node, vmid, name, logf)
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

// shellQuote is s as one POSIX shell word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
