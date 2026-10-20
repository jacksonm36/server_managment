//go:build !windows

package term

import (
	"errors"
	"os"
	"os/exec"

	"github.com/creack/pty"
)

// shell is a root login shell on a PTY.
type shell struct {
	cmd *exec.Cmd
	pty *os.File
}

func startShell(cols, rows uint16) (*shell, error) {
	sh := "/bin/bash"
	if _, err := os.Stat(sh); err != nil {
		sh = "/bin/sh"
	}
	cmd := exec.Command(sh, "-l")
	cmd.Dir = "/root"
	cmd.Env = []string{"TERM=xterm-256color", "HOME=/root", "USER=root", "LOGNAME=root", "SHELL=" + sh, "LANG=C.UTF-8",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return nil, err
	}
	return &shell{cmd: cmd, pty: f}, nil
}

func (s *shell) Read(b []byte) (int, error) { return s.pty.Read(b) }

func (s *shell) Write(b []byte) { _, _ = s.pty.Write(b) }

func (s *shell) resize(cols, rows uint16) {
	_ = pty.Setsize(s.pty, &pty.Winsize{Cols: cols, Rows: rows})
}

func (s *shell) kill() {
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	_ = s.pty.Close()
}

// wait reaps the shell once its output has ended.
func (s *shell) wait() (int32, error) {
	werr := s.cmd.Wait()
	s.pty.Close()
	var ee *exec.ExitError
	if errors.As(werr, &ee) {
		return int32(ee.ExitCode()), nil
	}
	return 0, werr
}
