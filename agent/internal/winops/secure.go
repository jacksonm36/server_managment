//go:build windows

package winops

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// Local users get nothing of the agent: its folders, its service and its
// files are for SYSTEM and Administrators only, and it listens on no port
// (it dials out to the panel).

// adminsOnly is a protected DACL: full control for SYSTEM and
// Administrators, nothing inherited, nobody else.
const adminsOnly = "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"

// serviceSDDL lets SYSTEM run the service and Administrators manage it;
// users cannot query, start, stop or reconfigure it.
const serviceSDDL = "D:(A;;CCLCSWRPWPDTLOCRRC;;;SY)(A;;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;BA)"

// ServiceName is the agent's Windows service.
const ServiceName = "servermgr-agent"

// SecureDir creates dir and gives it to SYSTEM and Administrators only.
func SecureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString(adminsOnly)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

func system32(name string) string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", name)
}

// Lockdown (re)applies the lock on the agent's binary folder, its work
// folder and its service, and removes what a self-update left behind. It
// runs at every service start, so a change made meanwhile does not stay.
func Lockdown(log *slog.Logger, stateDir string) {
	try := func(what string, err error) {
		if err != nil {
			log.Warn("lockdown: "+what, "err", err)
		}
	}
	try("state folder", SecureDir(stateDir))
	try("work folder", SecureDir(workDir))
	if exe, err := os.Executable(); err == nil {
		try("program folder", SecureDir(filepath.Dir(exe)))
		_ = os.Remove(exe + ".old")
	}
	out, err := exec.Command(system32("sc.exe"), "sdset", ServiceName, serviceSDDL).CombinedOutput()
	if err != nil {
		err = fmt.Errorf("%v: %s", err, out)
	}
	try("service permissions", err)
}
