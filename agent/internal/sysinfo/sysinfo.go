// Package sysinfo reads host facts and metrics from /proc and /etc on Linux.
// It has no external dependencies so the agent stays small.
package sysinfo

import (
	"bufio"
	"errors"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
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

// OSRelease parses /etc/os-release.
func OSRelease() map[string]string {
	out := map[string]string{}
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		out[k] = strings.Trim(v, `"'`)
	}
	return out
}

func Kernel() string {
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (c *Collector) Hello(agentVersion string) *agentv1.Hello {
	hn, _ := os.Hostname()
	rel := OSRelease()
	return &agentv1.Hello{
		Hostname:     hn,
		Os:           rel["ID"],
		OsVersion:    rel["VERSION"],
		OsPretty:     rel["PRETTY_NAME"],
		Kernel:       Kernel(),
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

	var fs syscall.Statfs_t
	if err := syscall.Statfs("/", &fs); err == nil {
		bs := uint64(fs.Bsize)
		m.DiskTotalBytes = fs.Blocks * bs
		m.DiskUsedBytes = (fs.Blocks - fs.Bfree) * bs
	}

	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		f := strings.Fields(string(b))
		if len(f) >= 3 {
			m.Load1, _ = strconv.ParseFloat(f[0], 64)
			m.Load5, _ = strconv.ParseFloat(f[1], 64)
			m.Load15, _ = strconv.ParseFloat(f[2], 64)
		}
	}
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			up, _ := strconv.ParseFloat(f[0], 64)
			m.UptimeSeconds = uint64(up)
		}
	}
	return m, nil
}

// cpuTimes returns aggregate idle (idle+iowait) and total jiffies.
func cpuTimes() (idle, total uint64, err error) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, err
	}
	line, _, _ := strings.Cut(string(b), "\n")
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return 0, 0, errors.New("unexpected /proc/stat format")
	}
	for i, s := range f[1:] {
		v, _ := strconv.ParseUint(s, 10, 64)
		// guest and guest_nice (fields 9, 10) are already included in user/nice.
		if i >= 8 {
			break
		}
		total += v
		if i == 3 || i == 4 {
			idle += v
		}
	}
	return idle, total, nil
}

func memInfo() (total, avail uint64, err error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(fields[1], 10, 64)
		switch fields[0] {
		case "MemTotal:":
			total = v * 1024
		case "MemAvailable:":
			avail = v * 1024
		}
	}
	if total == 0 {
		return 0, 0, errors.New("MemTotal missing from /proc/meminfo")
	}
	return total, avail, sc.Err()
}
