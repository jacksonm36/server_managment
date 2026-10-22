package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Enrolling a Proxmox node itself: the panel opens the node's shell
// through Proxmox (termproxy, as for "Shell" on the node page), runs the
// agent installer there and waits for the agent. The join token goes in
// on the installer's input once the terminal's echo is off, so it is
// neither shown nor kept in the shell's history (off for the run).

var (
	nodeEnrollDone = regexp.MustCompile(`SMENROLL-(\d+)`)
	ansiCodes      = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07|\r`)
)

// nodeEnrollWait bounds the whole install (download, service start).
var nodeEnrollWait = 5 * time.Minute

// POST /api/v1/proxmox/{id}/nodes/{node}/enroll
func (a *API) handlePVENodeEnroll(w http.ResponseWriter, r *http.Request) {
	if !accessFrom(r.Context()).Can("proxmox.console") {
		forbid(w, "proxmox.console")
		return
	}
	s, node, ok := a.pveNodeScope(w, r)
	if !ok {
		return
	}
	target := nodeTarget(s, node)
	ctx, cancel := context.WithTimeout(r.Context(), nodeEnrollWait)
	defer cancel()
	serverID, out, err := a.enrollNode(ctx, s, node)
	if err != nil {
		a.audit(r, "", "proxmox.node_enroll", target, map[string]string{"error": err.Error()}, false)
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "output": out})
		return
	}
	a.audit(r, "", "proxmox.node_enroll", target, map[string]string{"server_id": serverID}, true)
	writeJSON(w, http.StatusOK, map[string]any{"server_id": serverID, "output": out})
}

type nodeEnrollResult struct {
	code string
	err  error
}

func (a *API) enrollNode(ctx context.Context, s *pveScope, node string) (string, string, error) {
	raw, hash := randomToken()
	if err := a.Store.CreateJoinToken(ctx, hash, node, time.Now().Add(joinTokenTTL), nil); err != nil {
		return "", "", err
	}
	p, err := s.Cl.OpenNodeShell(ctx, node, "")
	if err != nil {
		return "", "", fmt.Errorf("opening the node's shell: %w", err)
	}
	conn, err := s.Cl.DialConsole(ctx, node, "node", 0, p)
	if err != nil {
		return "", "", fmt.Errorf("connecting to the node's shell: %w", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)
	if err := termLogin(ctx, conn, p); err != nil {
		return "", "", err
	}
	send := func(s string) error {
		return conn.Write(ctx, websocket.MessageBinary, []byte(fmt.Sprintf("0:%d:%s", len(s), s)))
	}
	_ = conn.Write(ctx, websocket.MessageBinary, []byte("1:200:50:"))

	// Read the terminal in the background. "SMREADY-2" is printed once
	// echo is off ("$((1+1))" in the echoed command does not match), and
	// "SMENROLL-<exit code>" when the installer is done.
	var (
		buf strings.Builder
		mu  sync.Mutex
	)
	done := make(chan nodeEnrollResult, 1)
	ready := make(chan struct{})
	go func() {
		readied := false
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				done <- nodeEnrollResult{err: err}
				return
			}
			mu.Lock()
			buf.Write(data)
			t := buf.String()
			if len(t) > 256<<10 {
				t = t[len(t)-64<<10:]
				buf.Reset()
				buf.WriteString(t)
			}
			mu.Unlock()
			if !readied && strings.Contains(t, "SMREADY-2") {
				readied = true
				close(ready)
			}
			if !readied && strings.Contains(t, "login:") {
				done <- nodeEnrollResult{err: errors.New("the node's shell asks for a login: the panel's API token must belong to root@pam to install the agent this way")}
				return
			}
			if m := nodeEnrollDone.FindStringSubmatch(t); m != nil {
				done <- nodeEnrollResult{code: m[1]}
				return
			}
		}
	}()
	// Give the login shell a moment to start, then run the installer.
	select {
	case <-ctx.Done():
		return "", "", ctx.Err()
	case <-time.After(2 * time.Second):
	}
	cmd := " unset HISTFILE; stty -echo; echo SMREADY-$((1+1)); " + a.enrollScript() + "; echo SMENROLL-$?; stty echo; exit\n"
	if err := send(cmd); err != nil {
		return "", "", err
	}
	var res nodeEnrollResult
	select {
	case <-ready:
		if err := send(raw + "\n"); err != nil {
			return "", "", err
		}
		select {
		case res = <-done:
		case <-ctx.Done():
			res.err = errors.New("the installer did not finish in time")
		}
	case res = <-done:
		if res.err == nil {
			res.err = errors.New("the shell ended before the installer started")
		}
	case <-time.After(30 * time.Second):
		res.err = errors.New("the node's shell did not answer")
	}
	mu.Lock()
	out := ansiCodes.ReplaceAllString(strings.ReplaceAll(buf.String(), raw, "••••"), "")
	mu.Unlock()
	if len(out) > 4000 {
		out = "…" + out[len(out)-4000:]
	}
	if res.err != nil {
		return "", out, res.err
	}
	if res.code != "0" {
		return "", out, fmt.Errorf("the agent installer exited with code %s", res.code)
	}
	serverID, err := a.waitEnrolled(ctx, hash)
	return serverID, out, err
}
