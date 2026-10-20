//go:build windows

package term

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// shell is PowerShell on a pseudo console (ConPTY). The agent is a
// LocalSystem service, so the shell runs as SYSTEM.
type shell struct {
	in     *os.File // our end of the console's input
	out    *os.File // our end of the console's output
	proc   windows.Handle
	exited chan struct{}

	mu     sync.Mutex
	hpc    windows.Handle
	closed bool // the pseudo console is closed
}

func startShell(cols, rows uint16) (*shell, error) {
	var ptyIn, inW, outR, ptyOut windows.Handle
	if err := windows.CreatePipe(&ptyIn, &inW, nil, 0); err != nil {
		return nil, fmt.Errorf("CreatePipe: %w", err)
	}
	if err := windows.CreatePipe(&outR, &ptyOut, nil, 0); err != nil {
		windows.CloseHandle(ptyIn)
		windows.CloseHandle(inW)
		return nil, fmt.Errorf("CreatePipe: %w", err)
	}
	var hpc windows.Handle
	err := windows.CreatePseudoConsole(windows.Coord{X: int16(cols), Y: int16(rows)}, ptyIn, ptyOut, 0, &hpc)
	// The pseudo console has its own copies of its pipe ends.
	windows.CloseHandle(ptyIn)
	windows.CloseHandle(ptyOut)
	if err != nil {
		windows.CloseHandle(inW)
		windows.CloseHandle(outR)
		return nil, fmt.Errorf("CreatePseudoConsole: %w", err)
	}
	s := &shell{in: os.NewFile(uintptr(inW), "conpty-in"), out: os.NewFile(uintptr(outR), "conpty-out"), hpc: hpc, exited: make(chan struct{})}
	if err := s.spawn(); err != nil {
		s.closeConsole()
		s.in.Close()
		s.out.Close()
		return nil, err
	}
	go func() {
		_, _ = windows.WaitForSingleObject(s.proc, windows.INFINITE)
		close(s.exited)
		// Closing the console ends its output pipe once the reader has
		// drained it, which ends the read loop.
		s.closeConsole()
	}()
	return s, nil
}

func (s *shell) spawn() error {
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return err
	}
	defer attrs.Delete()
	// The attribute's value is the console handle itself.
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, *(*unsafe.Pointer)(unsafe.Pointer(&s.hpc)), unsafe.Sizeof(s.hpc)); err != nil {
		return fmt.Errorf("attaching the pseudo console: %w", err)
	}
	si := windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	si.Cb = uint32(unsafe.Sizeof(si))

	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	ps := filepath.Join(root, `System32\WindowsPowerShell\v1.0\powershell.exe`)
	cmdline, err := windows.UTF16PtrFromString(windows.EscapeArg(ps) + " -NoLogo")
	if err != nil {
		return err
	}
	dir, _ := windows.UTF16PtrFromString(`C:\`)
	var pi windows.ProcessInformation
	if err := windows.CreateProcess(nil, cmdline, nil, nil, false, windows.EXTENDED_STARTUPINFO_PRESENT, nil, dir, &si.StartupInfo, &pi); err != nil {
		return fmt.Errorf("starting PowerShell: %w", err)
	}
	windows.CloseHandle(pi.Thread)
	s.proc = pi.Process
	return nil
}

func (s *shell) closeConsole() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		windows.ClosePseudoConsole(s.hpc)
	}
}

func (s *shell) Read(b []byte) (int, error) { return s.out.Read(b) }

func (s *shell) Write(b []byte) { _, _ = s.in.Write(b) }

func (s *shell) resize(cols, rows uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		_ = windows.ResizePseudoConsole(s.hpc, windows.Coord{X: int16(cols), Y: int16(rows)})
	}
}

// kill ends PowerShell; the waiter then closes the console, which ends the
// output. The process handle stays open until wait, so it cannot be reused.
func (s *shell) kill() {
	select {
	case <-s.exited:
	default:
		_ = windows.TerminateProcess(s.proc, 1)
	}
}

// wait reports PowerShell's exit code once the output has ended.
func (s *shell) wait() (int32, error) {
	<-s.exited
	var code uint32
	err := windows.GetExitCodeProcess(s.proc, &code)
	windows.CloseHandle(s.proc)
	s.in.Close()
	s.out.Close()
	return int32(code), err
}
