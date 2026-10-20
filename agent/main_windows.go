package main

import (
	"context"
	"crypto/x509"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"

	"golang.org/x/sys/windows/svc"

	"github.com/jacksonm36/servermgr/agent/internal/winops"
)

// On Windows the agent runs as the servermgr-agent service (LocalSystem)
// and takes the jobs in package winops.

var defaultStateDir = filepath.Join(programData(), "servermgr-agent")

func programData() string {
	if d := os.Getenv("ProgramData"); d != "" {
		return d
	}
	return `C:\ProgramData`
}

// secureStateDir gives the state directory (the agent's private key) to
// SYSTEM and Administrators only: ProgramData lets every user read.
func secureStateDir(dir string) error { return winops.SecureDir(dir) }

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
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		log = slog.New(slog.NewTextHandler(f, nil))
	}
	winops.Lockdown(log, defaultStateDir)
	return log
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

// startBackground tells the jobs where the panel is; certificate renewal
// and site backups are Linux only.
func startBackground(_ context.Context, _ *slog.Logger, panelURL string, pool *x509.CertPool) {
	winops.PanelURL = panelURL
	winops.PanelCA = pool
}

// runJob carries out the jobs a Windows server supports.
var runJob = winops.Run
