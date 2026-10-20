//go:build windows

// Package winops implements the jobs the panel can send to a Windows agent.
// Results have the same shape as the Linux ones (package ops), so the
// panel's pages work for both.
package winops

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/jacksonm36/servermgr/agent/internal/sysinfo"
	agentv1 "github.com/jacksonm36/servermgr/gen/agent/v1"
)

// PanelURL and PanelCA are set at start-up for downloads from the panel.
var (
	PanelURL string
	PanelCA  *x509.CertPool
)

type logFunc = func(string, ...any)

// Run executes a job and reports progress through send. It always ends with
// a SUCCEEDED or FAILED update.
func Run(ctx context.Context, job *agentv1.Job, send func(*agentv1.JobUpdate)) {
	send(&agentv1.JobUpdate{JobId: job.Id, Status: agentv1.JobStatus_JOB_STATUS_RUNNING})
	logf := func(format string, a ...any) {
		send(&agentv1.JobUpdate{JobId: job.Id, Status: agentv1.JobStatus_JOB_STATUS_RUNNING, LogLines: []string{fmt.Sprintf(format, a...)}})
	}
	// Windows Update and installers can take long.
	ctx, cancel := context.WithTimeout(ctx, 90*time.Minute)
	defer cancel()
	p := map[string]string{}
	for k, v := range job.Params {
		p[k] = v
	}

	var result any
	var err error
	switch job.Op {
	case agentv1.Op_OP_SYSTEM_INFO:
		result, err = systemInfo()
	case agentv1.Op_OP_SYSTEM_REBOOT:
		result, err = reboot(logf)
	case agentv1.Op_OP_PACKAGES_CHECK_UPDATES:
		result, err = checkUpdates(ctx, logf)
	case agentv1.Op_OP_PACKAGES_UPGRADE:
		result, err = upgradePackages(ctx, p, logf)
	case agentv1.Op_OP_SERVICES_LIST:
		result, err = servicesList(ctx)
	case agentv1.Op_OP_SERVICE_ACTION:
		result, err = serviceAction(ctx, p, logf)
	case agentv1.Op_OP_LOGS_READ:
		result, err = logsRead(ctx, p)
	case agentv1.Op_OP_FILES_LIST:
		result, err = filesList(p)
	case agentv1.Op_OP_FILES_READ:
		result, err = filesRead(p)
	case agentv1.Op_OP_FILES_WRITE:
		result, err = filesWrite(p, logf)
	case agentv1.Op_OP_FILES_MKDIR:
		result, err = filesMkdir(p, logf)
	case agentv1.Op_OP_FILES_DELETE:
		result, err = filesDelete(p, logf)
	case agentv1.Op_OP_FILES_RENAME:
		result, err = filesRename(p, logf)
	case agentv1.Op_OP_FIREWALL_STATUS:
		result, err = firewallStatus(ctx)
	case agentv1.Op_OP_FIREWALL_ENABLE:
		result, err = firewallEnable(ctx, logf)
	case agentv1.Op_OP_FIREWALL_DISABLE:
		result, err = firewallDisable(ctx, logf)
	case agentv1.Op_OP_FIREWALL_RULE_ADD:
		result, err = firewallRuleAdd(ctx, p, logf)
	case agentv1.Op_OP_FIREWALL_RULE_DELETE:
		result, err = firewallRuleDelete(ctx, p, logf)
	case agentv1.Op_OP_WIN_APPS_LIST:
		result, err = winAppsList(ctx)
	case agentv1.Op_OP_WIN_APPS_INSTALL:
		result, err = winAppsInstall(ctx, p, logf)
	case agentv1.Op_OP_WIN_APP_UNINSTALL:
		result, err = winAppUninstall(ctx, p, logf)
	case agentv1.Op_OP_AGENT_UPDATE:
		result, err = selfUpdate(ctx, p, logf)
	default:
		err = fmt.Errorf("%s is not available on Windows servers", strings.TrimPrefix(job.Op.String(), "OP_"))
	}
	if err != nil {
		send(&agentv1.JobUpdate{JobId: job.Id, Status: agentv1.JobStatus_JOB_STATUS_FAILED, Error: err.Error()})
		return
	}
	b, _ := json.Marshal(result)
	send(&agentv1.JobUpdate{JobId: job.Id, Status: agentv1.JobStatus_JOB_STATUS_SUCCEEDED, ResultJson: string(b)})
}

func systemInfo() (any, error) {
	hn, _ := os.Hostname()
	rel := sysinfo.OSRelease()
	return map[string]any{"hostname": hn, "os": "windows", "os_version": rel["VERSION"], "os_pretty": rel["PRETTY_NAME"],
		"kernel": sysinfo.Kernel(), "arch": runtime.GOARCH, "cpu_count": runtime.NumCPU()}, nil
}

func reboot(logf logFunc) (any, error) {
	logf("rebooting in 5 seconds")
	go func() {
		time.Sleep(time.Second)
		_ = exec.Command("shutdown.exe", "/r", "/t", "5", "/c", "servermgr").Run()
	}()
	return map[string]any{"scheduled": true}, nil
}

// powershell runs script (encoded, so nothing in it needs quoting) and
// returns its standard output. Errors carry the end of standard error.
func powershell(ctx context.Context, script string) (string, error) {
	script = "$ErrorActionPreference = 'Stop'\n$ProgressPreference = 'SilentlyContinue'\n" +
		"[Console]::OutputEncoding = [Text.Encoding]::UTF8\n" + script
	u := utf16.Encode([]rune(script))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", base64.StdEncoding.EncodeToString(b))
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if len(msg) > 600 {
			msg = "…" + msg[len(msg)-600:]
		}
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), fmt.Errorf("powershell: %s", msg)
	}
	return out.String(), nil
}

// psJSON runs script, whose output must be one ConvertTo-Json document, and
// decodes it into v.
func psJSON(ctx context.Context, script string, v any) error {
	out, err := powershell(ctx, script)
	if err != nil {
		return err
	}
	out = strings.TrimSpace(strings.TrimPrefix(out, "\ufeff"))
	if out == "" {
		out = "null"
	}
	if err := json.Unmarshal([]byte(out), v); err != nil {
		return fmt.Errorf("reading PowerShell output: %w", err)
	}
	return nil
}

// psString is s as a single-quoted PowerShell string.
func psString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
