// Package term runs interactive shells on a PTY for the panel's web terminal.
package term

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"

	agentv1 "github.com/jacksonm36/servermgr/gen/agent/v1"
)

const (
	maxSessions = 5
	idleTimeout = 30 * time.Minute
	chunk       = 32 << 10
)

type session struct {
	cmd      *exec.Cmd
	pty      *os.File
	mu       sync.Mutex
	lastSeen time.Time
}

// Manager owns the terminal sessions of one panel connection. Close kills
// them all, so a dropped stream never leaves shells running.
type Manager struct {
	send     func(*agentv1.AgentMessage)
	mu       sync.Mutex
	sessions map[string]*session
	done     chan struct{}
}

func NewManager(send func(*agentv1.AgentMessage)) *Manager {
	m := &Manager{send: send, sessions: map[string]*session{}, done: make(chan struct{})}
	go m.reapIdle()
	return m
}

func (m *Manager) exit(id string, code int32, err error) {
	e := &agentv1.TerminalExit{SessionId: id, Code: code}
	if err != nil {
		e.Error = err.Error()
	}
	m.send(&agentv1.AgentMessage{Msg: &agentv1.AgentMessage_TerminalExit{TerminalExit: e}})
}

func clampSize(cols, rows uint32) *pty.Winsize {
	return &pty.Winsize{Cols: uint16(min(max(cols, 10), 500)), Rows: uint16(min(max(rows, 5), 200))}
}

// Open starts a login shell for root on a new PTY.
func (m *Manager) Open(o *agentv1.TerminalOpen) {
	m.mu.Lock()
	if _, dup := m.sessions[o.SessionId]; dup || len(m.sessions) >= maxSessions {
		m.mu.Unlock()
		m.exit(o.SessionId, -1, errors.New("too many open terminals on this server (max 5)"))
		return
	}
	shell := "/bin/bash"
	if _, err := os.Stat(shell); err != nil {
		shell = "/bin/sh"
	}
	cmd := exec.Command(shell, "-l")
	cmd.Dir = "/root"
	cmd.Env = []string{"TERM=xterm-256color", "HOME=/root", "USER=root", "LOGNAME=root", "SHELL=" + shell, "LANG=C.UTF-8",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}
	f, err := pty.StartWithSize(cmd, clampSize(o.Cols, o.Rows))
	if err != nil {
		m.mu.Unlock()
		m.exit(o.SessionId, -1, err)
		return
	}
	s := &session{cmd: cmd, pty: f, lastSeen: time.Now()}
	m.sessions[o.SessionId] = s
	m.mu.Unlock()

	go func() {
		buf := make([]byte, chunk)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				data := append([]byte(nil), buf[:n]...)
				m.send(&agentv1.AgentMessage{Msg: &agentv1.AgentMessage_TerminalOutput{TerminalOutput: &agentv1.TerminalOutput{SessionId: o.SessionId, Data: data}}})
			}
			if err != nil {
				break
			}
		}
		werr := cmd.Wait()
		code := int32(0)
		var ee *exec.ExitError
		if errors.As(werr, &ee) {
			code = int32(ee.ExitCode())
			werr = nil
		}
		m.mu.Lock()
		delete(m.sessions, o.SessionId)
		m.mu.Unlock()
		f.Close()
		m.exit(o.SessionId, code, werr)
	}()
}

func (m *Manager) get(id string) *session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

func (m *Manager) Input(in *agentv1.TerminalInput) {
	if s := m.get(in.SessionId); s != nil {
		s.mu.Lock()
		s.lastSeen = time.Now()
		s.mu.Unlock()
		_, _ = s.pty.Write(in.Data)
	}
}

func (m *Manager) Resize(r *agentv1.TerminalResize) {
	if s := m.get(r.SessionId); s != nil {
		_ = pty.Setsize(s.pty, clampSize(r.Cols, r.Rows))
	}
}

// Kill ends a session; the read loop then reports the exit.
func (m *Manager) Kill(id string) {
	if s := m.get(id); s != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_ = s.pty.Close()
	}
}

func (m *Manager) reapIdle() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-m.done:
			return
		case <-t.C:
		}
		m.mu.Lock()
		var idle []string
		for id, s := range m.sessions {
			s.mu.Lock()
			if time.Since(s.lastSeen) > idleTimeout {
				idle = append(idle, id)
			}
			s.mu.Unlock()
		}
		m.mu.Unlock()
		for _, id := range idle {
			m.Kill(id)
		}
	}
}

// Close kills every session (the panel stream went away).
func (m *Manager) Close() {
	close(m.done)
	m.mu.Lock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.Kill(id)
	}
}
