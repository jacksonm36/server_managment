package ops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Each cron job is one /etc/cron.d/servermgr-<id> file; the registry keeps
// the editable fields. Output goes to /var/log/servermgr-cron/<id>.log.
const (
	cronRegistry = "/etc/servermgr-agent/cron.json"
	cronDir      = "/etc/cron.d"
	cronLogDir   = "/var/log/servermgr-cron"
	cronLogMax   = 1 << 20
)

type CronJob struct {
	ID       string    `json:"id"`
	Schedule string    `json:"schedule"`
	User     string    `json:"user"`
	Command  string    `json:"command"`
	Comment  string    `json:"comment"`
	Enabled  bool      `json:"enabled"`
	Created  time.Time `json:"created_at"`
	LastRun  int64     `json:"last_run,omitempty"`
	LastLog  string    `json:"last_output,omitempty"`
}

var (
	cronMu      sync.Mutex
	cronField   = regexp.MustCompile(`^(\*|[0-9]{1,2}(-[0-9]{1,2})?)(/[0-9]{1,2})?(,(\*|[0-9]{1,2}(-[0-9]{1,2})?)(/[0-9]{1,2})?)*$`)
	cronNamed   = regexp.MustCompile(`^(?i:[a-z]{3}(-[a-z]{3})?(,[a-z]{3}(-[a-z]{3})?)*)$`)
	cronMacro   = regexp.MustCompile(`^@(reboot|yearly|annually|monthly|weekly|daily|midnight|hourly)$`)
	cronIDRe    = regexp.MustCompile(`^[a-f0-9]{12}$`)
	cronComment = regexp.MustCompile(`^[^\r\n]{0,120}$`)
)

func validSchedule(s string) bool {
	if cronMacro.MatchString(s) {
		return true
	}
	f := strings.Fields(s)
	if len(f) != 5 {
		return false
	}
	for i, x := range f {
		if cronField.MatchString(x) {
			continue
		}
		// Month (3) and weekday (4) also accept names like jan or mon-fri.
		if (i == 3 || i == 4) && cronNamed.MatchString(x) {
			continue
		}
		return false
	}
	return true
}

func loadCron() ([]*CronJob, error) {
	b, err := os.ReadFile(cronRegistry)
	if errors.Is(err, os.ErrNotExist) {
		return []*CronJob{}, nil
	}
	if err != nil {
		return nil, err
	}
	var jobs []*CronJob
	if err := json.Unmarshal(b, &jobs); err != nil {
		return nil, err
	}
	return jobs, nil
}

func saveCron(jobs []*CronJob) error {
	if err := os.MkdirAll(filepath.Dir(cronRegistry), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(jobs, "", "  ")
	return writeFileAtomic(cronRegistry, b, 0o600)
}

func cronFile(id string) string { return filepath.Join(cronDir, "servermgr-"+id) }
func cronLog(id string) string  { return filepath.Join(cronLogDir, id+".log") }

func writeCronFile(j *CronJob) error {
	if !j.Enabled {
		err := os.Remove(cronFile(j.ID))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	// The directory must be traversable so jobs running as other users can
	// append to their own log, which is owned by the job's user.
	if err := os.MkdirAll(cronDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(cronLogDir, 0o755); err != nil {
		return err
	}
	log := cronLog(j.ID)
	if _, err := os.Stat(log); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(log, nil, 0o640); err != nil {
			return err
		}
	}
	if u, err := user.Lookup(j.User); err == nil {
		_ = run(context.Background(), func(string, ...any) {}, "chown", u.Username, log)
	}
	// cron.d files must not be group/world writable and must end in a newline.
	return writeFileAtomic(cronFile(j.ID), []byte(renderCronLine(j, log)), 0o644)
}

// renderCronLine builds the cron.d file. % is escaped because cron turns an
// unescaped % into a newline (commands containing % are rejected earlier).
func renderCronLine(j *CronJob, log string) string {
	return fmt.Sprintf("# Managed by servermgr: %s\nSHELL=/bin/sh\nPATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\n%s %s { date -u +'--- \\%%FT\\%%TZ'; %s; } >> %s 2>&1\n",
		strings.ReplaceAll(j.Comment, "\n", " "), j.Schedule, j.User, j.Command, log)
}

func cronStatus() (any, error) {
	jobs, err := loadCron()
	if err != nil {
		return nil, err
	}
	for _, j := range jobs {
		if fi, err := os.Stat(cronLog(j.ID)); err == nil && fi.Size() > 0 {
			j.LastRun = fi.ModTime().Unix()
			j.LastLog = tailFile(cronLog(j.ID), 4096)
		}
	}
	sort.Slice(jobs, func(a, b int) bool { return jobs[a].Created.Before(jobs[b].Created) })
	_, hasCron := exec.LookPath("cron")
	if hasCron != nil {
		_, hasCron = exec.LookPath("crond") // cronie on RHEL
	}
	return map[string]any{"jobs": jobs, "users": loginUsersAndSites(), "cron_installed": hasCron == nil}, nil
}

// loginUsersAndSites lists users a job may run as: login users plus site users.
func loginUsersAndSites() []string {
	out := []string{}
	for _, u := range loginUsers() {
		out = append(out, u.Name)
	}
	if sites, err := listSites(); err == nil {
		for _, s := range sites {
			out = append(out, s.User)
		}
	}
	return out
}

func tailFile(path string, n int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	off := max(fi.Size()-n, 0)
	buf := make([]byte, fi.Size()-off)
	_, _ = f.ReadAt(buf, off)
	return string(buf)
}

func newCronID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func cronSave(p map[string]string, logf func(string, ...any)) (any, error) {
	cronMu.Lock()
	defer cronMu.Unlock()
	sched := strings.Join(strings.Fields(p["schedule"]), " ")
	if !validSchedule(sched) {
		return nil, errors.New("schedule must be 5 cron fields (e.g. */15 * * * *) or a macro like @daily")
	}
	if !unixUser.MatchString(p["user"]) {
		return nil, errors.New("invalid user")
	}
	if _, err := user.Lookup(p["user"]); err != nil {
		return nil, fmt.Errorf("user %s does not exist", p["user"])
	}
	cmd := strings.TrimSpace(p["command"])
	if cmd == "" || len(cmd) > 2000 || strings.ContainsAny(cmd, "\r\n%") {
		return nil, errors.New("command must be one line, at most 2000 characters, without % (cron treats it as a newline)")
	}
	if !cronComment.MatchString(p["comment"]) {
		return nil, errors.New("comment must be one line")
	}
	jobs, err := loadCron()
	if err != nil {
		return nil, err
	}
	var j *CronJob
	if id := p["id"]; id != "" {
		for _, x := range jobs {
			if x.ID == id {
				j = x
			}
		}
		if j == nil {
			return nil, fmt.Errorf("cron job %s not found", id)
		}
	} else {
		j = &CronJob{ID: newCronID(), Created: time.Now().UTC()}
		jobs = append(jobs, j)
	}
	j.Schedule, j.User, j.Command, j.Comment = sched, p["user"], cmd, p["comment"]
	j.Enabled = p["enabled"] != "false"
	if err := writeCronFile(j); err != nil {
		return nil, err
	}
	if err := saveCron(jobs); err != nil {
		return nil, err
	}
	logf("saved cron job %s: %s %s", j.ID, j.Schedule, j.Command)
	return cronStatus()
}

func cronDelete(p map[string]string, logf func(string, ...any)) (any, error) {
	cronMu.Lock()
	defer cronMu.Unlock()
	if !cronIDRe.MatchString(p["id"]) {
		return nil, errors.New("invalid id")
	}
	jobs, err := loadCron()
	if err != nil {
		return nil, err
	}
	kept := jobs[:0]
	found := false
	for _, j := range jobs {
		if j.ID == p["id"] {
			found = true
			continue
		}
		kept = append(kept, j)
	}
	if !found {
		return nil, errors.New("cron job not found")
	}
	_ = os.Remove(cronFile(p["id"]))
	_ = os.Remove(cronLog(p["id"]))
	if err := saveCron(kept); err != nil {
		return nil, err
	}
	logf("deleted cron job %s", p["id"])
	return cronStatus()
}

// cronRun runs a job now as its user, streaming output into the job log.
func cronRun(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	if !cronIDRe.MatchString(p["id"]) {
		return nil, errors.New("invalid id")
	}
	jobs, err := loadCron()
	if err != nil {
		return nil, err
	}
	for _, j := range jobs {
		if j.ID != p["id"] {
			continue
		}
		logf("$ runuser -u %s -- /bin/sh -c %q", j.User, j.Command)
		cmd := exec.CommandContext(ctx, "runuser", "-u", j.User, "--", "/bin/sh", "-c", j.Command)
		cmd.Dir = "/"
		err := streamCmd(cmd, logf)
		code := 0
		if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			return nil, err
		}
		logf("exit code %d", code)
		return map[string]any{"exit_code": code}, nil
	}
	return nil, errors.New("cron job not found")
}

// trimCronLogs keeps each cron log under cronLogMax by dropping its oldest half.
func trimCronLogs() {
	files, _ := filepath.Glob(filepath.Join(cronLogDir, "*.log"))
	for _, f := range files {
		if fi, err := os.Stat(f); err == nil && fi.Size() > cronLogMax {
			keep := tailFile(f, cronLogMax/2)
			_ = os.WriteFile(f, []byte(keep), fi.Mode().Perm())
		}
	}
}
