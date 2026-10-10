package ops

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var unitName = regexp.MustCompile(`^[A-Za-z0-9@._:\\-]{1,200}\.service$`)

// selfUnit is the agent's own service; stopping or disabling it would cut
// the panel off, so only restart is allowed.
const selfUnit = "servermgr-agent.service"

type service struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Load        string `json:"load"`
	Active      string `json:"active"`
	Sub         string `json:"sub"`
	Enabled     string `json:"enabled"`
}

func servicesList(ctx context.Context) (any, error) {
	out, err := exec.CommandContext(ctx, "systemctl", "list-units", "--type=service", "--all", "--no-pager", "--output=json").Output()
	if err != nil {
		return nil, fmt.Errorf("systemctl list-units: %w", err)
	}
	var units []struct {
		Unit, Load, Active, Sub, Description string
	}
	if err := json.Unmarshal(out, &units); err != nil {
		return nil, err
	}
	enabled := map[string]string{}
	if out, err := exec.CommandContext(ctx, "systemctl", "list-unit-files", "--type=service", "--no-pager", "--output=json").Output(); err == nil {
		var files []struct {
			UnitFile string `json:"unit_file"`
			State    string `json:"state"`
		}
		if json.Unmarshal(out, &files) == nil {
			for _, f := range files {
				enabled[f.UnitFile] = f.State
			}
		}
	}
	seen := map[string]bool{}
	list := []service{}
	for _, u := range units {
		if u.Load == "not-found" {
			continue
		}
		seen[u.Unit] = true
		list = append(list, service{Name: u.Unit, Description: u.Description, Load: u.Load, Active: u.Active, Sub: u.Sub, Enabled: enabled[u.Unit]})
	}
	// Installed but never loaded units (e.g. disabled ones) only show up as files.
	for name, state := range enabled {
		if !seen[name] && !strings.Contains(name, "@") {
			list = append(list, service{Name: name, Active: "inactive", Sub: "dead", Enabled: state})
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return map[string]any{"services": list}, nil
}

func serviceAction(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	name := p["name"]
	if !unitName.MatchString(name) {
		return nil, errors.New("invalid service name")
	}
	action := p["action"]
	switch action {
	case "start", "stop", "restart", "reload", "enable", "disable":
	default:
		return nil, errors.New("action must be start, stop, restart, reload, enable or disable")
	}
	if name == selfUnit && (action == "stop" || action == "disable") {
		return nil, errors.New("stopping or disabling the servermgr agent would disconnect this server")
	}
	args := []string{action, name}
	if action == "enable" || action == "disable" {
		args = []string{action, "--now", name}
	}
	if name == selfUnit && action == "restart" {
		// Restart after replying, or the job result would be lost.
		logf("restarting the agent in 2 seconds")
		go func() { _ = exec.Command("sh", "-c", "sleep 2; systemctl restart "+selfUnit).Run() }()
		return servicesList(ctx)
	}
	if err := run(ctx, logf, "systemctl", args...); err != nil {
		if out, _ := exec.CommandContext(ctx, "systemctl", "status", "--no-pager", "-n", "15", name).CombinedOutput(); len(out) > 0 {
			logf("%s", strings.TrimSpace(string(out)))
		}
		return nil, err
	}
	return servicesList(ctx)
}

// ---- logs ----

var (
	priorities  = map[string]bool{"emerg": true, "alert": true, "crit": true, "err": true, "warning": true, "notice": true, "info": true, "debug": true}
	sinceValues = map[string]string{"15m": "-15min", "1h": "-1h", "6h": "-6h", "24h": "-24h", "7d": "-7d", "today": "today", "boot": ""}
)

func clampLines(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return def
	}
	return min(n, 5000)
}

// journalLogs returns journal entries, optionally for one unit, filtered by
// priority, time and a plain-text search.
func journalLogs(ctx context.Context, p map[string]string) (any, error) {
	args := []string{"--no-pager", "-o", "short-iso", "-n", strconv.Itoa(clampLines(p["lines"], 300))}
	if u := p["unit"]; u != "" {
		if !strings.Contains(u, ".") {
			u += ".service"
		}
		if !unitName.MatchString(u) {
			return nil, errors.New("invalid unit")
		}
		args = append(args, "-u", u)
	}
	if pr := p["priority"]; pr != "" {
		if !priorities[pr] {
			return nil, errors.New("invalid priority")
		}
		args = append(args, "-p", pr)
	}
	if s := p["since"]; s != "" {
		v, ok := sinceValues[s]
		if !ok {
			return nil, errors.New("since must be one of 15m, 1h, 6h, 24h, 7d, today, boot")
		}
		if s == "boot" {
			args = append(args, "-b")
		} else {
			args = append(args, "--since", v)
		}
	}
	if g := p["grep"]; g != "" {
		if len(g) > 200 {
			return nil, errors.New("search text too long")
		}
		// --grep is a regex; quote it so the user's text is matched literally.
		args = append(args, "--case-sensitive=false", "--grep", regexp.QuoteMeta(g))
	}
	// journalctl exits non-zero when --grep matches nothing; inputs are
	// validated above, so its output is shown as-is either way.
	out, _ := exec.CommandContext(ctx, "journalctl", args...).CombinedOutput()
	return map[string]any{"source": "journal", "text": string(out)}, nil
}

// logRoots are directories whose *.log files the viewer may read.
var logRoots = []string{"/var/log", sitesRoot}

func logFiles() []map[string]any {
	out := []map[string]any{}
	add := func(path string, fi os.FileInfo) {
		out = append(out, map[string]any{"path": path, "size": fi.Size(), "mtime": fi.ModTime().Unix()})
	}
	_ = filepath.WalkDir("/var/log", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && strings.Count(path, "/") > 4 {
			return filepath.SkipDir
		}
		if fi, err := d.Info(); err == nil && fi.Mode().IsRegular() && isTextLog(path) {
			add(path, fi)
		}
		return nil
	})
	if sites, err := filepath.Glob(filepath.Join(sitesRoot, "*", "logs", "*.log")); err == nil {
		for _, f := range sites {
			if fi, err := os.Stat(f); err == nil {
				add(f, fi)
			}
		}
	}
	return out
}

func isTextLog(path string) bool {
	base := filepath.Base(path)
	if strings.HasSuffix(base, ".gz") || strings.HasSuffix(base, ".xz") || strings.HasSuffix(base, ".zst") || strings.HasSuffix(base, ".journal") {
		return false
	}
	for _, bin := range []string{"wtmp", "btmp", "lastlog", "faillog"} {
		if base == bin {
			return false
		}
	}
	return strings.HasSuffix(base, ".log") || !strings.Contains(base, ".") || regexp.MustCompile(`\.log\.\d+$`).MatchString(base)
}

func fileLogs(p map[string]string) (any, error) {
	path := filepath.Clean(p["path"])
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	ok := false
	for _, root := range logRoots {
		if strings.HasPrefix(real, root+"/") {
			ok = true
		}
	}
	if !ok || !isTextLog(real) {
		return nil, errors.New("only log files under /var/log and site log folders can be viewed")
	}
	lines := clampLines(p["lines"], 300)
	text := tailFile(real, int64(lines)*400)
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
	// Keep whole lines only, and at most the requested count.
	all := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return map[string]any{"source": "file", "path": real, "text": strings.Join(all, "\n")}, nil
}

func logsRead(ctx context.Context, p map[string]string) (any, error) {
	switch p["source"] {
	case "", "journal":
		return journalLogs(ctx, p)
	case "file":
		return fileLogs(p)
	case "files":
		return map[string]any{"files": logFiles()}, nil
	}
	return nil, errors.New("source must be journal, file or files")
}
