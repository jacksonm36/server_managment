//go:build windows

package winops

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// Service names: no slashes, at most 256 characters.
var serviceName = regexp.MustCompile(`^[A-Za-z0-9 ._@#$+(){}\-]{1,256}$`)

// selfService is the agent's own service; stopping or disabling it would
// cut the panel off, so only restart is allowed.
const selfService = "servermgr-agent"

// service has the shape of the Linux one (package ops), so the panel's
// Services page works for both: active is active|inactive|failed|activating|
// deactivating, sub the Windows state, enabled enabled|manual|disabled|static.
type service struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Load        string `json:"load"`
	Active      string `json:"active"`
	Sub         string `json:"sub"`
	Enabled     string `json:"enabled"`
}

// ERROR_SERVICE_NEVER_STARTED: a stopped service that simply never ran.
const errNeverStarted = 1077

func describe(name string, st svc.Status, cfg mgr.Config) service {
	s := service{Name: name, Description: cfg.DisplayName, Load: "loaded"}
	switch st.State {
	case svc.Running:
		s.Active, s.Sub = "active", "running"
	case svc.Paused:
		s.Active, s.Sub = "active", "paused"
	case svc.StartPending, svc.ContinuePending:
		s.Active, s.Sub = "activating", "start-pending"
	case svc.StopPending, svc.PausePending:
		s.Active, s.Sub = "deactivating", "stop-pending"
	default:
		s.Active, s.Sub = "inactive", "stopped"
		// An automatic service that stopped with an error has failed.
		if cfg.StartType == mgr.StartAutomatic && st.Win32ExitCode != 0 && st.Win32ExitCode != errNeverStarted {
			s.Active = "failed"
		}
	}
	switch cfg.StartType {
	case mgr.StartAutomatic:
		s.Enabled = "enabled"
	case mgr.StartManual:
		s.Enabled = "manual"
	case mgr.StartDisabled:
		s.Enabled = "disabled"
	default: // boot and system drivers
		s.Enabled = "static"
	}
	return s
}

func servicesList(context.Context) (any, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, fmt.Errorf("service manager: %w", err)
	}
	defer m.Disconnect()
	names, err := m.ListServices()
	if err != nil {
		return nil, err
	}
	list := []service{}
	for _, name := range names {
		s, err := m.OpenService(name)
		if err != nil {
			continue
		}
		st, err1 := s.Query()
		cfg, err2 := s.Config()
		s.Close()
		if err1 != nil || err2 != nil {
			continue
		}
		list = append(list, describe(name, st, cfg))
	}
	sort.Slice(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
	return map[string]any{"services": list}, nil
}

func serviceAction(ctx context.Context, p map[string]string, logf logFunc) (any, error) {
	// The Linux page names units "x.service"; Windows services have no suffix.
	name := strings.TrimSuffix(p["name"], ".service")
	if !serviceName.MatchString(name) {
		return nil, errors.New("invalid service name")
	}
	action := p["action"]
	switch action {
	case "start", "stop", "restart", "enable", "disable":
	case "reload":
		return nil, errors.New("Windows services cannot be reloaded; restart it instead")
	default:
		return nil, errors.New("action must be start, stop, restart, enable or disable")
	}
	self := strings.EqualFold(name, selfService)
	if self && (action == "stop" || action == "disable") {
		return nil, errors.New("stopping or disabling the servermgr agent would disconnect this server")
	}
	if self && action == "restart" {
		// Restart after replying, or the job result would be lost. The
		// helper is a separate, detached process: the service manager does
		// not end it when the agent stops, so it can start the agent again.
		logf("restarting the agent in 2 seconds")
		cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
			"Start-Sleep -Seconds 2; Restart-Service -Name "+selfService+" -Force")
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP}
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		_ = cmd.Process.Release()
		return servicesList(ctx)
	}

	m, err := mgr.Connect()
	if err != nil {
		return nil, fmt.Errorf("service manager: %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	defer s.Close()

	switch action {
	case "start":
		err = startService(ctx, s, name, logf)
	case "stop":
		err = stopService(ctx, s, name, logf)
	case "restart":
		if err = stopService(ctx, s, name, logf); err == nil {
			err = startService(ctx, s, name, logf)
		}
	case "enable": // like systemctl enable --now
		if err = setStartType(s, mgr.StartAutomatic); err == nil {
			logf("%s starts automatically at boot", name)
			err = startService(ctx, s, name, logf)
		}
	case "disable": // like systemctl disable --now
		if err = setStartType(s, mgr.StartDisabled); err == nil {
			logf("%s is disabled", name)
			err = stopService(ctx, s, name, logf)
		}
	}
	if err != nil {
		return nil, err
	}
	return servicesList(ctx)
}

// setStartType changes only the start type; everything else stays as is.
func setStartType(s *mgr.Service, start uint32) error {
	return windows.ChangeServiceConfig(s.Handle, windows.SERVICE_NO_CHANGE, start, windows.SERVICE_NO_CHANGE,
		nil, nil, nil, nil, nil, nil, nil)
}

func startService(ctx context.Context, s *mgr.Service, name string, logf logFunc) error {
	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State == svc.Running {
		logf("%s is already running", name)
		return nil
	}
	logf("starting %s", name)
	if err := s.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return fmt.Errorf("starting %s: %w", name, err)
	}
	return waitState(ctx, s, name, svc.Running)
}

func stopService(ctx context.Context, s *mgr.Service, name string, logf logFunc) error {
	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State == svc.Stopped {
		logf("%s is already stopped", name)
		return nil
	}
	logf("stopping %s", name)
	if _, err := s.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
		if errors.Is(err, windows.ERROR_DEPENDENT_SERVICES_RUNNING) {
			return fmt.Errorf("%s has running services that depend on it; stop those first", name)
		}
		return fmt.Errorf("stopping %s: %w", name, err)
	}
	return waitState(ctx, s, name, svc.Stopped)
}

// waitState waits up to a minute for the service to reach want.
func waitState(ctx context.Context, s *mgr.Service, name string, want svc.State) error {
	deadline := time.Now().Add(time.Minute)
	for {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == want {
			return nil
		}
		if want == svc.Running && st.State == svc.Stopped {
			return fmt.Errorf("%s stopped right after starting (exit code %d); see its event log", name, st.Win32ExitCode)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not finish changing state within a minute", name)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}
