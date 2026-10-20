//go:build !windows

package main

import (
	"context"
	"crypto/x509"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/jacksonm36/servermgr/agent/internal/ops"
)

const defaultStateDir = "/etc/servermgr-agent"

// runJob carries out a job from the panel.
var runJob = ops.Run

// secureStateDir: MkdirAll(0700) as root already keeps others out.
func secureStateDir(string) error { return nil }

func prepareRun(log *slog.Logger) *slog.Logger {
	ensureUnitDropIn(log)
	return log
}

func runContext(*slog.Logger) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// startBackground starts certificate renewal and scheduled backups.
func startBackground(ctx context.Context, log *slog.Logger, panelURL string, pool *x509.CertPool) {
	ops.PanelURL = panelURL
	ops.PanelCA = pool
	ops.StartRenewer(ctx, log)
	ops.StartBackups(ctx, log)
}

// ensureUnitDropIn makes systemd keep the agent running when a child it
// started (apt, dnf…) is OOM-killed; the default policy stops the unit.
func ensureUnitDropIn(log *slog.Logger) {
	const dir = "/etc/systemd/system/servermgr-agent.service.d"
	const path = dir + "/servermgr.conf"
	want := "[Service]\nOOMPolicy=continue\n"
	if b, err := os.ReadFile(path); err == nil && string(b) == want {
		return
	}
	if os.MkdirAll(dir, 0o755) != nil || os.WriteFile(path, []byte(want), 0o644) != nil {
		return
	}
	if err := exec.Command("systemctl", "daemon-reload").Run(); err != nil {
		log.Warn("systemctl daemon-reload", "err", err)
	}
}
