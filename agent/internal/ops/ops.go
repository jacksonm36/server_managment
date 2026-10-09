// Package ops implements the jobs the panel can send to an agent.
package ops

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	agentv1 "github.com/jacksonm36/servermgr/gen/agent/v1"
	"github.com/jacksonm36/servermgr/agent/internal/sysinfo"
)

type report func(*agentv1.JobUpdate)

// Run executes a job and reports progress through send. It always ends with
// a SUCCEEDED or FAILED update.
func Run(ctx context.Context, job *agentv1.Job, send report) {
	send(&agentv1.JobUpdate{JobId: job.Id, Status: agentv1.JobStatus_JOB_STATUS_RUNNING})
	logf := func(format string, a ...any) {
		send(&agentv1.JobUpdate{JobId: job.Id, Status: agentv1.JobStatus_JOB_STATUS_RUNNING, LogLines: []string{fmt.Sprintf(format, a...)}})
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	var result any
	var err error
	switch job.Op {
	case agentv1.Op_OP_SYSTEM_INFO:
		result, err = systemInfo()
	case agentv1.Op_OP_PACKAGES_CHECK_UPDATES:
		result, err = checkUpdates(ctx, logf)
	default:
		err = fmt.Errorf("unsupported op %v", job.Op)
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
	info := map[string]any{
		"hostname":   hn,
		"os":         rel["ID"],
		"os_version": rel["VERSION"],
		"os_pretty":  rel["PRETTY_NAME"],
		"kernel":     sysinfo.Kernel(),
		"arch":       runtime.GOARCH,
		"cpu_count":  runtime.NumCPU(),
	}
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "model name" {
				info["cpu_model"] = strings.TrimSpace(v)
				break
			}
		}
	}
	if out, err := exec.Command("systemd-detect-virt").Output(); err == nil {
		info["virtualization"] = strings.TrimSpace(string(out))
	}
	return info, nil
}

type pkgUpdate struct {
	Name       string `json:"name"`
	Current    string `json:"current"`
	Candidate  string `json:"candidate"`
	Repository string `json:"repository"`
	Security   bool   `json:"security"`
}

// e.g. "openssl/stable-security 3.5.1-1+deb13u1 amd64 [upgradable from: 3.5.1-1]"
var aptUpgradable = regexp.MustCompile(`^(\S+)/(\S+)\s+(\S+)\s+\S+\s+\[upgradable from:\s+([^\]]+)\]`)

func checkUpdates(ctx context.Context, logf func(string, ...any)) (any, error) {
	if _, err := exec.LookPath("apt-get"); err != nil {
		return nil, errors.New("apt-get not found: only Debian/Ubuntu are supported for now")
	}
	logf("$ apt-get update")
	cmd := exec.CommandContext(ctx, "apt-get", "update", "-q")
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive", "LC_ALL=C")
	// apt-get update can exit 0 even when repositories were unreachable, so
	// watch for "Err:" lines too; otherwise the result would be stale.
	fetchErrors := 0
	err := streamCmd(cmd, func(format string, a ...any) {
		line := fmt.Sprintf(format, a...)
		if strings.HasPrefix(line, "Err:") {
			fetchErrors++
		}
		logf("%s", line)
	})
	if err != nil {
		return nil, fmt.Errorf("apt-get update: %w", err)
	}
	if fetchErrors > 0 {
		return nil, fmt.Errorf("apt-get update could not fetch %d repository index(es); package lists may be stale (check network/DNS)", fetchErrors)
	}

	logf("$ apt list --upgradable")
	cmd = exec.CommandContext(ctx, "apt", "list", "--upgradable")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("apt list: %w", err)
	}
	updates := []pkgUpdate{}
	security := 0
	for _, line := range strings.Split(string(out), "\n") {
		m := aptUpgradable.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		u := pkgUpdate{Name: m[1], Repository: m[2], Candidate: m[3], Current: m[4]}
		u.Security = strings.Contains(u.Repository, "security")
		if u.Security {
			security++
		}
		updates = append(updates, u)
	}
	logf("%d package(s) can be upgraded (%d security)", len(updates), security)
	_, rebootErr := os.Stat("/var/run/reboot-required")
	return map[string]any{
		"updates":         updates,
		"count":           len(updates),
		"security_count":  security,
		"reboot_required": rebootErr == nil,
	}, nil
}

// streamCmd runs cmd and forwards combined output line by line.
func streamCmd(cmd *exec.Cmd, logf func(string, ...any)) error {
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		return err
	}
	pw.Close()
	sc := bufio.NewScanner(pr)
	for sc.Scan() {
		logf("%s", sc.Text())
	}
	pr.Close()
	return cmd.Wait()
}
