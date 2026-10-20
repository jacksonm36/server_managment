// Package term runs interactive shells for the panel's web terminal: on a
// PTY on Linux (term_unix.go), on a pseudo console on Windows
// (term_windows.go).
package term

import (
	"errors"
	"sync"
	"time"

	agentv1 "github.com/jacksonm36/servermgr/gen/agent/v1"
)

const (
	maxSessions = 5
	idleTimeout = 30 * time.Minute
	chunk       = 32 << 10
)

type session struct {
	sh       *shell
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

func clampSize(cols, rows uint32) (uint16, uint16) {
	return uint16(min(max(cols, 10), 500)), uint16(min(max(rows, 5), 200))
}

// Open starts a shell for the server's administrator (root, or SYSTEM on
// Windows).
func (m *Manager) Open(o *agentv1.TerminalOpen) {
	m.mu.Lock()
	if _, dup := m.sessions[o.SessionId]; dup || len(m.sessions) >= maxSessions {
		m.mu.Unlock()
		m.exit(o.SessionId, -1, errors.New("too many open terminals on this server (max 5)"))
		return
	}
	sh, err := startShell(clampSize(o.Cols, o.Rows))
	if err != nil {
		m.mu.Unlock()
		m.exit(o.SessionId, -1, err)
		return
	}
	m.sessions[o.SessionId] = &session{sh: sh, lastSeen: time.Now()}
	m.mu.Unlock()

	go func() {
		buf := make([]byte, chunk)
		for {
			n, err := sh.Read(buf)
			if n > 0 {
				data := append([]byte(nil), buf[:n]...)
				m.send(&agentv1.AgentMessage{Msg: &agentv1.AgentMessage_TerminalOutput{TerminalOutput: &agentv1.TerminalOutput{SessionId: o.SessionId, Data: data}}})
			}
			if err != nil {
				break
			}
		}
		code, werr := sh.wait()
		m.mu.Lock()
		delete(m.sessions, o.SessionId)
		m.mu.Unlock()
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
		s.sh.Write(in.Data)
	}
}

func (m *Manager) Resize(r *agentv1.TerminalResize) {
	if s := m.get(r.SessionId); s != nil {
		s.sh.resize(clampSize(r.Cols, r.Rows))
	}
}

// Kill ends a session; the read loop then reports the exit.
func (m *Manager) Kill(id string) {
	if s := m.get(id); s != nil {
		s.sh.kill()
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
