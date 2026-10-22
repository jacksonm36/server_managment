//go:build windows

package winops

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Logs: the Windows event logs stand in for the journal, and the viewer
// can read *.log files under a few known folders.

// eventLogs are the logs the viewer reads by name; any other "unit" is an
// event provider (a service or application) looked up in System and
// Application.
var eventLogs = map[string]string{"system": "System", "application": "Application", "security": "Security", "setup": "Setup"}

var providerName = regexp.MustCompile(`^[A-Za-z0-9 ._()/\-]{1,200}$`)

// Event levels at or above the Linux priority names (0 is how the Security
// log marks its audit events).
var eventLevels = map[string]string{"emerg": "1", "alert": "1", "crit": "1", "err": "1,2", "warning": "1,2,3",
	"notice": "0,1,2,3,4", "info": "0,1,2,3,4", "debug": "0,1,2,3,4,5"}

// sinceHours are the Linux "since" choices as hours ("boot" is handled apart).
var sinceHours = map[string]string{"15m": "0.25", "1h": "1", "6h": "6", "24h": "24", "7d": "168", "today": ""}

func clampLines(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return def
	}
	return min(n, 5000)
}

func eventLogsRead(ctx context.Context, p map[string]string) (any, error) {
	filter := []string{}
	unit := strings.TrimSuffix(strings.TrimSpace(p["unit"]), ".service")
	switch {
	case unit == "":
		filter = append(filter, "LogName = 'System'")
	case eventLogs[strings.ToLower(unit)] != "":
		filter = append(filter, "LogName = "+psString(eventLogs[strings.ToLower(unit)]))
	case providerName.MatchString(unit):
		filter = append(filter, "LogName = 'System', 'Application'", "ProviderName = "+psString(unit))
	default:
		return nil, errors.New("invalid log or provider name")
	}
	if pr := p["priority"]; pr != "" {
		lv, ok := eventLevels[pr]
		if !ok {
			return nil, errors.New("invalid priority")
		}
		filter = append(filter, "Level = "+lv)
	}
	switch s := p["since"]; s {
	case "":
	case "boot":
		filter = append(filter, "StartTime = (Get-CimInstance Win32_OperatingSystem).LastBootUpTime")
	case "today":
		filter = append(filter, "StartTime = (Get-Date).Date")
	default:
		h, ok := sinceHours[s]
		if !ok {
			return nil, errors.New("since must be one of 15m, 1h, 6h, 24h, 7d, today, boot")
		}
		filter = append(filter, "StartTime = (Get-Date).AddHours(-"+h+")")
	}
	where := "$true"
	if g := p["grep"]; g != "" {
		if len(g) > 200 {
			return nil, errors.New("search text too long")
		}
		// A literal, case-insensitive search.
		where = "([string]$_.Message).IndexOf(" + psString(g) + ", [StringComparison]::OrdinalIgnoreCase) -ge 0 -or ([string]$_.ProviderName).IndexOf(" + psString(g) + ", [StringComparison]::OrdinalIgnoreCase) -ge 0"
	}
	script := `$names = @{ 0 = 'INFO'; 1 = 'CRITICAL'; 2 = 'ERROR'; 3 = 'WARNING'; 4 = 'INFO'; 5 = 'VERBOSE' }
$f = @{ ` + strings.Join(filter, "; ") + ` }
try {
    $ev = @(Get-WinEvent -FilterHashtable $f | Where-Object { ` + where + ` } | Select-Object -First ` + strconv.Itoa(clampLines(p["lines"], 300)) + `)
} catch {
    if ($_.FullyQualifiedErrorId -notmatch 'NoMatchingEventsFound') { throw }
    $ev = @()
}
[array]::Reverse($ev)
foreach ($e in $ev) {
    $msg = [string]$e.Message
    if (-not $msg) { $msg = '(no message)' }
    $msg = ($msg -replace '\s*[\r\n]+\s*', ' ').Trim()
    '{0} {1}[{2}] {3}: {4}' -f $e.TimeCreated.ToString('yyyy-MM-ddTHH:mm:ss'), $e.ProviderName, $e.Id, $names[[int]$e.Level], $msg
}
`
	out, err := powershell(ctx, script)
	if err != nil {
		return nil, err
	}
	return map[string]any{"source": "journal", "text": strings.TrimRight(strings.ReplaceAll(out, "\r\n", "\n"), "\n")}, nil
}

// logRoots are the folders whose *.log files the viewer may read.
func logRoots() []string {
	win := os.Getenv("SystemRoot")
	if win == "" {
		win = `C:\Windows`
	}
	pd := os.Getenv("ProgramData")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	drive := filepath.VolumeName(win)
	roots := []string{}
	for _, r := range []string{filepath.Join(win, "Logs"), filepath.Join(pd, "servermgr-agent"), drive + `\inetpub\logs`} {
		if real, err := filepath.EvalSymlinks(r); err == nil {
			roots = append(roots, real)
		}
	}
	return roots
}

func isTextLog(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".log")
}

func logFiles() []map[string]any {
	out := []map[string]any{}
	for _, root := range logRoots() {
		depth := strings.Count(root, `\`)
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && strings.Count(path, `\`)-depth >= 3 {
				return filepath.SkipDir
			}
			if fi, err := d.Info(); err == nil && fi.Mode().IsRegular() && isTextLog(path) {
				out = append(out, map[string]any{"path": path, "size": fi.Size(), "mtime": fi.ModTime().Unix()})
			}
			return nil
		})
	}
	return out
}

// underRoot reports whether path (resolved) lies inside one of roots.
func underRoot(path string, roots []string) bool {
	lp := strings.ToLower(path)
	for _, r := range roots {
		if strings.HasPrefix(lp, strings.ToLower(strings.TrimRight(r, `\`))+`\`) {
			return true
		}
	}
	return false
}

func fileLogs(p map[string]string) (any, error) {
	path := p["path"]
	if !filepath.IsAbs(path) {
		return nil, errors.New("path must be absolute")
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	if !underRoot(real, logRoots()) || !isTextLog(real) {
		return nil, errors.New(`only .log files under Windows\Logs, the agent's folder and inetpub\logs can be viewed`)
	}
	lines := clampLines(p["lines"], 300)
	text, err := tailFile(real, int64(lines)*400)
	if err != nil {
		return nil, err
	}
	text = strings.ReplaceAll(strings.TrimPrefix(text, "\ufeff"), "\r\n", "\n")
	if g := strings.ToLower(p["grep"]); g != "" {
		var b strings.Builder
		sc := bufio.NewScanner(strings.NewReader(text))
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			if strings.Contains(strings.ToLower(sc.Text()), g) {
				b.WriteString(sc.Text() + "\n")
			}
		}
		text = b.String()
	}
	all := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return map[string]any{"source": "file", "path": real, "text": strings.Join(all, "\n")}, nil
}

// tailFile returns about the last n bytes of path, from a line start.
func tailFile(path string, n int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	off := max(fi.Size()-n, 0) &^ 1 // even, for UTF-16 files
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return "", err
	}
	b, err := io.ReadAll(io.LimitReader(f, n))
	if err != nil {
		return "", err
	}
	s := string(b)
	// Some Windows logs are UTF-16: mostly NUL high bytes.
	if len(b) >= 4 && bytes.Count(b, []byte{0}) > len(b)/4 {
		u := make([]uint16, len(b)/2)
		for i := range u {
			u[i] = binary.LittleEndian.Uint16(b[2*i:])
		}
		s = string(utf16.Decode(u))
	}
	if off > 0 {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
	}
	return s, nil
}

func logsRead(ctx context.Context, p map[string]string) (any, error) {
	switch p["source"] {
	case "", "journal":
		return eventLogsRead(ctx, p)
	case "file":
		return fileLogs(p)
	case "files":
		return map[string]any{"files": logFiles()}, nil
	}
	return nil, errors.New("source must be journal, file or files")
}
