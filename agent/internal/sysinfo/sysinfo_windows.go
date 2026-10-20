package sysinfo

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	kernel32            = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemTimes  = kernel32.NewProc("GetSystemTimes")
	procGlobalMemStatus = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetTickCount64  = kernel32.NewProc("GetTickCount64")
)

// OSRelease is the Linux os-release; Windows has none.
func OSRelease() map[string]string {
	id, ver, pretty, _ := osInfo()
	return map[string]string{"ID": id, "VERSION": ver, "PRETTY_NAME": pretty}
}

// Kernel is the Windows build, like 10.0.26100.4652.
func Kernel() string {
	_, _, _, k := osInfo()
	return k
}

func osInfo() (id, version, pretty, kernel string) {
	id = "windows"
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
	if err != nil {
		return id, "", "Windows", ""
	}
	defer k.Close()
	product, _, _ := k.GetStringValue("ProductName")
	display, _, _ := k.GetStringValue("DisplayVersion")
	build, _, _ := k.GetStringValue("CurrentBuild")
	ubr, _, _ := k.GetIntegerValue("UBR")
	maj, _, _ := k.GetIntegerValue("CurrentMajorVersionNumber")
	minor, _, _ := k.GetIntegerValue("CurrentMinorVersionNumber")
	// Windows 11 still calls itself Windows 10 in ProductName.
	if n, _ := strconv.Atoi(build); n >= 22000 && strings.HasPrefix(product, "Windows 10") {
		product = "Windows 11" + strings.TrimPrefix(product, "Windows 10")
	}
	pretty = strings.TrimSpace(product + " " + display)
	kernel = fmt.Sprintf("%d.%d.%s.%d", maj, minor, build, ubr)
	return id, display, pretty, kernel
}

func filetime(ft windows.Filetime) uint64 {
	return uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime)
}

// cpuTimes returns idle and total CPU time; kernel time includes idle.
func cpuTimes() (idle, total uint64, err error) {
	var i, k, u windows.Filetime
	if r, _, e := procGetSystemTimes.Call(uintptr(unsafe.Pointer(&i)), uintptr(unsafe.Pointer(&k)), uintptr(unsafe.Pointer(&u))); r == 0 {
		return 0, 0, e
	}
	return filetime(i), filetime(k) + filetime(u), nil
}

type memStatusEx struct {
	Length, MemoryLoad                     uint32
	TotalPhys, AvailPhys                   uint64
	TotalPageFile, AvailPageFile           uint64
	TotalVirtual, AvailVirtual, AvailExtVi uint64
}

func memInfo() (total, avail uint64, err error) {
	m := memStatusEx{}
	m.Length = uint32(unsafe.Sizeof(m))
	if r, _, e := procGlobalMemStatus.Call(uintptr(unsafe.Pointer(&m))); r == 0 {
		return 0, 0, e
	}
	return m.TotalPhys, m.AvailPhys, nil
}

func diskUsage() (total, used uint64) {
	root := os.Getenv("SystemDrive") + `\`
	if root == `\` {
		root = `C:\`
	}
	p, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return
	}
	var free, tot, totFree uint64
	if windows.GetDiskFreeSpaceEx(p, &free, &tot, &totFree) == nil {
		total, used = tot, tot-totFree
	}
	return
}

// Windows has no load average.
func loadAvg() (l1, l5, l15 float64) { return }

func uptime() uint64 {
	ms, _, _ := procGetTickCount64.Call()
	return uint64(ms) / 1000
}
