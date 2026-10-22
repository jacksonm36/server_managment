//go:build !windows

package sysinfo

import (
	"bufio"
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
)

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

func osInfo() (id, version, pretty, kernel string) {
	rel := OSRelease()
	return rel["ID"], rel["VERSION"], rel["PRETTY_NAME"], Kernel()
}

func diskUsage() (total, used uint64) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs("/", &fs); err == nil {
		bs := uint64(fs.Bsize)
		total, used = fs.Blocks*bs, (fs.Blocks-fs.Bfree)*bs
	}
	return
}

func loadAvg() (l1, l5, l15 float64) {
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		if f := strings.Fields(string(b)); len(f) >= 3 {
			l1, _ = strconv.ParseFloat(f[0], 64)
			l5, _ = strconv.ParseFloat(f[1], 64)
			l15, _ = strconv.ParseFloat(f[2], 64)
		}
	}
	return
}

func uptime() uint64 {
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			up, _ := strconv.ParseFloat(f[0], 64)
			return uint64(up)
		}
	}
	return 0
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
