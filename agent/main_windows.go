package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"

	"github.com/jacksonm36/servermgr/agent/internal/sysinfo"
	agentv1 "github.com/jacksonm36/servermgr/gen/agent/v1"
)

// On Windows the agent runs as the servermgr-agent service (LocalSystem).
// It reports the host and its metrics and takes the jobs below; the Linux
// operations (packages, firewall, sites…) do not apply.

var defaultStateDir = filepath.Join(programData(), "servermgr-agent")

func programData() string {
	if d := os.Getenv("ProgramData"); d != "" {
		return d
	}
	return `C:\ProgramData`
}

// secureStateDir gives the state directory (the agent's private key) to
// SYSTEM and Administrators only, with no inherited access: ProgramData
// lets every user read by default.
func secureStateDir(dir string) error {
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
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

// prepareRun logs to agent.log in the state directory when the agent runs
// as a service, which has no console.
func prepareRun(log *slog.Logger) *slog.Logger {
	if ok, _ := svc.IsWindowsService(); !ok {
		return log
	}
	path := filepath.Join(defaultStateDir, "agent.log")
	if fi, err := os.Stat(path); err == nil && fi.Size() > 10<<20 {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return log
	}
	return slog.New(slog.NewTextHandler(f, nil))
}

type service struct {
	cancel context.CancelFunc
	log    *slog.Logger
}

func (s *service) Execute(_ []string, r <-chan svc.ChangeRequest, st chan<- svc.Status) (bool, uint32) {
	st <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for c := range r {
		switch c.Cmd {
		case svc.Interrogate:
			st <- c.CurrentStatus
		case svc.Stop, svc.Shutdown:
			s.log.Info("stopping")
			st <- svc.Status{State: svc.StopPending}
			s.cancel()
			return false, 0
		}
	}
	return false, 0
}

// runContext ends when the service is stopped (or on Ctrl+C in a console).
func runContext(log *slog.Logger) (context.Context, context.CancelFunc) {
	if ok, _ := svc.IsWindowsService(); !ok {
		return signal.NotifyContext(context.Background(), os.Interrupt)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := svc.Run("servermgr-agent", &service{cancel: cancel, log: log}); err != nil {
			log.Error("service", "err", err)
		}
		cancel()
	}()
	return ctx, cancel
}

// startBackground: certificate renewal and site backups are Linux only.
func startBackground(context.Context, *slog.Logger, string, *x509.CertPool) {}

// runJob carries out the jobs a Windows server supports.
func runJob(_ context.Context, job *agentv1.Job, send func(*agentv1.JobUpdate)) {
	send(&agentv1.JobUpdate{JobId: job.Id, Status: agentv1.JobStatus_JOB_STATUS_RUNNING})
	var result any
	var err error
	switch job.Op {
	case agentv1.Op_OP_SYSTEM_INFO:
		hn, _ := os.Hostname()
		rel := sysinfo.OSRelease()
		result = map[string]any{"hostname": hn, "os": "windows", "os_version": rel["VERSION"], "os_pretty": rel["PRETTY_NAME"],
			"kernel": sysinfo.Kernel(), "arch": runtime.GOARCH, "cpu_count": runtime.NumCPU()}
	case agentv1.Op_OP_SYSTEM_REBOOT:
		send(&agentv1.JobUpdate{JobId: job.Id, Status: agentv1.JobStatus_JOB_STATUS_RUNNING, LogLines: []string{"rebooting in 5 seconds"}})
		go func() {
			time.Sleep(time.Second)
			_ = exec.Command("shutdown.exe", "/r", "/t", "5", "/c", "servermgr").Run()
		}()
		result = map[string]any{"scheduled": true}
	default:
		err = fmt.Errorf("%s is not available on Windows servers yet", job.Op)
	}
	if err != nil {
		send(&agentv1.JobUpdate{JobId: job.Id, Status: agentv1.JobStatus_JOB_STATUS_FAILED, Error: err.Error()})
		return
	}
	b, _ := json.Marshal(result)
	send(&agentv1.JobUpdate{JobId: job.Id, Status: agentv1.JobStatus_JOB_STATUS_SUCCEEDED, ResultJson: string(b)})
}
