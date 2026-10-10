// Package hub is the in-memory registry of connected agents. It keeps recent
// metrics per server and fans events out to UI subscribers (SSE).
package hub

import (
	"errors"
	"sync"
	"time"

	agentv1 "github.com/jacksonm36/servermgr/gen/agent/v1"
)

// metricsWindow is how many samples are kept per server (5 s interval => 15 min).
const metricsWindow = 180

var ErrNotConnected = errors.New("agent not connected")

type Metric struct {
	TS         int64   `json:"ts"`
	CPU        float64 `json:"cpu"`
	MemTotal   uint64  `json:"mem_total"`
	MemUsed    uint64  `json:"mem_used"`
	DiskTotal  uint64  `json:"disk_total"`
	DiskUsed   uint64  `json:"disk_used"`
	Load1      float64 `json:"load1"`
	Load5      float64 `json:"load5"`
	Load15     float64 `json:"load15"`
	UptimeSecs uint64  `json:"uptime"`
}

func MetricFromProto(m *agentv1.Metrics) Metric {
	return Metric{
		TS: m.TsUnixMs, CPU: m.CpuPercent,
		MemTotal: m.MemTotalBytes, MemUsed: m.MemUsedBytes,
		DiskTotal: m.DiskTotalBytes, DiskUsed: m.DiskUsedBytes,
		Load1: m.Load1, Load5: m.Load5, Load15: m.Load15, UptimeSecs: m.UptimeSeconds,
	}
}

// Event is pushed to UI subscribers.
type Event struct {
	Type     string  `json:"type"` // "status" | "metrics" | "job"
	ServerID string  `json:"server_id"`
	Online   *bool   `json:"online,omitempty"`
	Metric   *Metric `json:"metric,omitempty"`
	JobID    string  `json:"job_id,omitempty"`
}

type conn struct {
	send      chan *agentv1.PanelMessage
	connected time.Time
}

type Hub struct {
	mu           sync.RWMutex
	conns        map[string]*conn
	metrics      map[string][]Metric
	subs         map[chan Event]struct{}
	offlineSince map[string]time.Time
}

func New() *Hub {
	return &Hub{
		conns:        map[string]*conn{},
		metrics:      map[string][]Metric{},
		subs:         map[chan Event]struct{}{},
		offlineSince: map[string]time.Time{},
	}
}

// Attach registers an agent stream. A newer connection for the same server
// replaces an older one; the returned detach func is a no-op in that case.
func (h *Hub) Attach(serverID string) (send <-chan *agentv1.PanelMessage, detach func()) {
	c := &conn{send: make(chan *agentv1.PanelMessage, 16), connected: time.Now()}
	h.mu.Lock()
	h.conns[serverID] = c
	delete(h.offlineSince, serverID)
	h.mu.Unlock()
	online := true
	h.publish(Event{Type: "status", ServerID: serverID, Online: &online})
	return c.send, func() {
		h.mu.Lock()
		cur := h.conns[serverID]
		if cur == c {
			delete(h.conns, serverID)
			h.offlineSince[serverID] = time.Now()
		}
		h.mu.Unlock()
		if cur == c {
			offline := false
			h.publish(Event{Type: "status", ServerID: serverID, Online: &offline})
		}
	}
}

func (h *Hub) Online(serverID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.conns[serverID]
	return ok
}

func (h *Hub) Send(serverID string, msg *agentv1.PanelMessage) error {
	h.mu.RLock()
	c, ok := h.conns[serverID]
	h.mu.RUnlock()
	if !ok {
		return ErrNotConnected
	}
	select {
	case c.send <- msg:
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("agent send queue full")
	}
}

func (h *Hub) RecordMetric(serverID string, m Metric) {
	h.mu.Lock()
	buf := append(h.metrics[serverID], m)
	if len(buf) > metricsWindow {
		buf = append(buf[:0:0], buf[len(buf)-metricsWindow:]...)
	}
	h.metrics[serverID] = buf
	h.mu.Unlock()
	h.publish(Event{Type: "metrics", ServerID: serverID, Metric: &m})
}

func (h *Hub) Metrics(serverID string) []Metric {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return append([]Metric(nil), h.metrics[serverID]...)
}

func (h *Hub) LatestMetric(serverID string) *Metric {
	h.mu.RLock()
	defer h.mu.RUnlock()
	buf := h.metrics[serverID]
	if len(buf) == 0 {
		return nil
	}
	m := buf[len(buf)-1]
	return &m
}

// OfflineSince reports when the server's agent disconnected, if it did so
// while this panel process was running.
func (h *Hub) OfflineSince(serverID string) (time.Time, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	t, ok := h.offlineSince[serverID]
	return t, ok
}

func (h *Hub) Forget(serverID string) {
	h.mu.Lock()
	delete(h.metrics, serverID)
	h.mu.Unlock()
}

func (h *Hub) JobChanged(serverID, jobID string) {
	h.publish(Event{Type: "job", ServerID: serverID, JobID: jobID})
}

func (h *Hub) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

// publish never blocks: slow subscribers drop events.
func (h *Hub) publish(e Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subs {
		select {
		case ch <- e:
		default:
		}
	}
}
