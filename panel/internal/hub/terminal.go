package hub

// TermEvent is output from (or the end of) a terminal session.
type TermEvent struct {
	Data  []byte
	Exit  bool
	Code  int32
	Error string
}

type termSub struct {
	serverID string
	ch       chan TermEvent
}

// RegisterTerminal routes a session's agent output to the returned channel.
func (h *Hub) RegisterTerminal(serverID, sessionID string) (<-chan TermEvent, func()) {
	ch := make(chan TermEvent, 1024)
	h.mu.Lock()
	if h.terms == nil {
		h.terms = map[string]*termSub{}
	}
	h.terms[sessionID] = &termSub{serverID: serverID, ch: ch}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.terms, sessionID)
		h.mu.Unlock()
	}
}

// TerminalEvent delivers agent output. Events from a server that does not
// own the session are dropped, so one agent cannot write into another's
// terminal.
func (h *Hub) TerminalEvent(serverID, sessionID string, e TermEvent) {
	h.mu.RLock()
	sub := h.terms[sessionID]
	h.mu.RUnlock()
	if sub == nil || sub.serverID != serverID {
		return
	}
	select {
	case sub.ch <- e:
	default: // browser far behind; drop rather than stall the agent stream
	}
}
