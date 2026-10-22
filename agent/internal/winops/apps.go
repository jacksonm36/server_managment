//go:build windows

package winops

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

// apps.ps1 is the panel's installer script (a panel test keeps the copies
// equal): it downloads each program over HTTPS and runs it only if its
// Authenticode publisher or SHA-256 is the expected one.
//
//go:embed apps.ps1
var appsPS1 []byte

// workDir holds the script, its list and log: only SYSTEM and
// Administrators can write there, so no user can plant a file for SYSTEM.
const workDir = `C:\Windows\servermgr`

// installMu keeps installs one at a time (Windows Installer allows one).
var installMu sync.Mutex

type catalogApp struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	URL    string   `json:"url"`
	Kind   string   `json:"kind"`
	Args   []string `json:"args"`
	Signer string   `json:"signer,omitempty"`
	SHA256 string   `json:"sha256,omitempty"`
	Exe    string   `json:"exe,omitempty"`
}

var appID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)

func winAppsInstall(ctx context.Context, p map[string]string, logf logFunc) (any, error) {
	var apps []catalogApp
	if err := json.Unmarshal([]byte(p["apps"]), &apps); err != nil {
		return nil, fmt.Errorf("apps: %w", err)
	}
	if len(apps) == 0 || len(apps) > 80 {
		return nil, errors.New("between 1 and 80 applications")
	}
	for _, a := range apps {
		switch {
		case !appID.MatchString(a.ID):
			return nil, fmt.Errorf("invalid application id %q", a.ID)
		case !strings.HasPrefix(a.URL, "https://"):
			return nil, fmt.Errorf("%s: not an https URL", a.ID)
		case a.Kind != "msi" && a.Kind != "exe" && a.Kind != "zip":
			return nil, fmt.Errorf("%s: unknown kind %q", a.ID, a.Kind)
		case a.Signer == "" && a.SHA256 == "":
			return nil, fmt.Errorf("%s: nothing to verify the download with", a.ID)
		}
	}
	installMu.Lock()
	defer installMu.Unlock()
	if err := SecureDir(workDir); err != nil {
		return nil, err
	}
	script, list := filepath.Join(workDir, "agent-apps.ps1"), filepath.Join(workDir, "agent-apps.json")
	// Windows PowerShell 5.1 reads a script without a BOM as ANSI.
	body := append([]byte("\ufeff"), bytes.ReplaceAll(bytes.ReplaceAll(appsPS1, []byte("\r\n"), []byte("\n")), []byte("\n"), []byte("\r\n"))...)
	if err := os.WriteFile(script, body, 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(list, []byte(p["apps"]), 0o600); err != nil {
		return nil, err
	}
	defer os.Remove(list)
	cmd := exec.CommandContext(ctx, system32(`WindowsPowerShell\v1.0\powershell.exe`), "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-File", script, "-Json", list, "-LogFile", filepath.Join(workDir, "apps.log"))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	summary := ""
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		logf("%s", line)
		if _, s, ok := strings.Cut(line, "SERVERMGR-APPS done: "); ok {
			summary = s
		}
	}
	if err := cmd.Wait(); err != nil {
		if summary != "" {
			return nil, errors.New("some applications failed: " + summary)
		}
		return nil, fmt.Errorf("installer: %v %s", err, strings.TrimSpace(stderr.String()))
	}
	return map[string]any{"summary": summary}, nil
}

type program struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Publisher   string `json:"publisher"`
	InstallDate string `json:"install_date,omitempty"`
	SizeKB      uint64 `json:"size_kb,omitempty"`
	Uninstall   bool   `json:"can_uninstall"`
}

var uninstallRoots = map[string]string{
	"64": `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`,
	"32": `SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`,
}

var msiGUID = regexp.MustCompile(`^\{[0-9A-Fa-f-]{36}\}$`)

// uninstallCommand is how to remove a program silently, or "".
func uninstallCommand(k registry.Key, name string) string {
	if v, _, _ := k.GetIntegerValue("WindowsInstaller"); v == 1 && msiGUID.MatchString(name) {
		return "msiexec.exe /x " + name + " /qn /norestart"
	}
	q, _, _ := k.GetStringValue("QuietUninstallString")
	return strings.TrimSpace(q)
}

func winAppsList(context.Context) (any, error) {
	list := []program{}
	for bits, root := range uninstallRoots {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, root, registry.ENUMERATE_SUB_KEYS|registry.WOW64_64KEY)
		if err != nil {
			continue
		}
		names, _ := k.ReadSubKeyNames(-1)
		k.Close()
		for _, n := range names {
			sk, err := registry.OpenKey(registry.LOCAL_MACHINE, root+`\`+n, registry.QUERY_VALUE|registry.WOW64_64KEY)
			if err != nil {
				continue
			}
			name, _, _ := sk.GetStringValue("DisplayName")
			sys, _, _ := sk.GetIntegerValue("SystemComponent")
			parent, _, _ := sk.GetStringValue("ParentKeyName")
			rel, _, _ := sk.GetStringValue("ReleaseType")
			if name == "" || sys == 1 || parent != "" || rel == "Update" || rel == "Hotfix" || rel == "Security Update" {
				sk.Close()
				continue
			}
			pr := program{ID: bits + ":" + n, Name: name}
			pr.Version, _, _ = sk.GetStringValue("DisplayVersion")
			pr.Publisher, _, _ = sk.GetStringValue("Publisher")
			pr.InstallDate, _, _ = sk.GetStringValue("InstallDate")
			pr.SizeKB, _, _ = sk.GetIntegerValue("EstimatedSize")
			pr.Uninstall = uninstallCommand(sk, n) != ""
			sk.Close()
			list = append(list, pr)
		}
	}
	sort.Slice(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
	return map[string]any{"programs": list}, nil
}

var programID = regexp.MustCompile(`^(64|32):([^\\/]{1,200})$`)

// splitCommand finds the program in an uninstall command line.
func splitCommand(line string) (string, error) {
	if strings.HasPrefix(line, `"`) {
		if end := strings.Index(line[1:], `"`); end > 0 {
			return line[1 : end+1], nil
		}
	} else if i := strings.Index(strings.ToLower(line), ".exe"); i > 0 {
		return line[:i+4], nil
	}
	return "", fmt.Errorf("cannot read the uninstall command %q", line)
}

func winAppUninstall(ctx context.Context, p map[string]string, logf logFunc) (any, error) {
	m := programID.FindStringSubmatch(p["id"])
	if m == nil {
		return nil, errors.New("invalid program id")
	}
	sk, err := registry.OpenKey(registry.LOCAL_MACHINE, uninstallRoots[m[1]]+`\`+m[2], registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return nil, errors.New("program not found")
	}
	name, _, _ := sk.GetStringValue("DisplayName")
	line := uninstallCommand(sk, m[2])
	sk.Close()
	if line == "" {
		return nil, fmt.Errorf("%s has no silent uninstaller; remove it in the terminal or over RDP", name)
	}
	exe, err := splitCommand(line)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(exe) {
		exe = system32(exe)
	}
	installMu.Lock()
	defer installMu.Unlock()
	logf("uninstalling %s", name)
	cmd := exec.CommandContext(ctx, exe)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: line}
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		switch ee.ExitCode() {
		case 3010, 1641:
			logf("a restart finishes the removal")
			err = nil
		case 1605:
			err = nil // already gone
		}
	}
	if err != nil {
		return nil, fmt.Errorf("uninstaller: %v %s", err, strings.TrimSpace(string(out)))
	}
	return winAppsList(ctx)
}
