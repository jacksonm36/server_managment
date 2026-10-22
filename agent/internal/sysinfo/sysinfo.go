// Package sysinfo reads host facts and metrics: from /proc and /etc on
// Linux, from the registry and kernel32 on Windows.
package sysinfo

import (
	"os"
	"runtime"
	"sync"
	"time"

	agentv1 "github.com/jacksonm36/servermgr/gen/agent/v1"
)

type Collector struct {
	mu        sync.Mutex
	prevIdle  uint64
	prevTotal uint64
}

func New() *Collector {
	c := &Collector{}
	c.prevIdle, c.prevTotal, _ = cpuTimes()
	return c
}

func (c *Collector) Hello(agentVersion string) *agentv1.Hello {
	hn, _ := os.Hostname()
	id, ver, pretty, kernel := osInfo()
	return &agentv1.Hello{
		Hostname:     hn,
		Os:           id,
		OsVersion:    ver,
		OsPretty:     pretty,
		Kernel:       kernel,
		Arch:         runtime.GOARCH,
		AgentVersion: agentVersion,
		CpuCount:     uint32(runtime.NumCPU()),
	}
}

func (c *Collector) Metrics() (*agentv1.Metrics, error) {
	m := &agentv1.Metrics{TsUnixMs: time.Now().UnixMilli()}

	idle, total, err := cpuTimes()
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if dt := total - c.prevTotal; total > c.prevTotal {
		m.CpuPercent = 100 * float64(dt-(idle-c.prevIdle)) / float64(dt)
	}
	c.prevIdle, c.prevTotal = idle, total
	c.mu.Unlock()

	memTotal, memAvail, err := memInfo()
	if err != nil {
		return nil, err
	}
	m.MemTotalBytes, m.MemUsedBytes = memTotal, memTotal-memAvail
	m.DiskTotalBytes, m.DiskUsedBytes = diskUsage()
	m.Load1, m.Load5, m.Load15 = loadAvg()
	m.UptimeSeconds = uptime()
	return m, nil
}
