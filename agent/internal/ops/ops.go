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

	"github.com/jacksonm36/servermgr/agent/internal/sysinfo"
	agentv1 "github.com/jacksonm36/servermgr/gen/agent/v1"
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

	p := map[string]string{}
	for k, v := range job.Params {
		p[k] = v
	}
	if p["user"] == "" {
		p["user"] = "root"
	}

	var result any
	var err error
	switch job.Op {
	case agentv1.Op_OP_SYSTEM_INFO:
		result, err = systemInfo()
	case agentv1.Op_OP_PACKAGES_CHECK_UPDATES:
		result, err = checkUpdates(ctx, logf)
	case agentv1.Op_OP_PACKAGES_UPGRADE:
		result, err = upgradePackages(ctx, p, logf)
	case agentv1.Op_OP_SYSTEM_REBOOT:
		result, err = reboot(logf)
	case agentv1.Op_OP_SSH_KEYS_LIST:
		result, err = listSSHKeys(p)
	case agentv1.Op_OP_SSH_KEY_ADD:
		result, err = addSSHKey(p, logf)
	case agentv1.Op_OP_SSH_KEY_REMOVE:
		result, err = removeSSHKey(p, logf)
	case agentv1.Op_OP_FIREWALL_STATUS:
		result, err = firewallStatus(ctx)
	case agentv1.Op_OP_FIREWALL_INSTALL:
		result, err = firewallInstall(ctx, logf)
	case agentv1.Op_OP_FIREWALL_ENABLE:
		result, err = firewallEnable(ctx, logf)
	case agentv1.Op_OP_FIREWALL_DISABLE:
		result, err = firewallDisable(ctx, logf)
	case agentv1.Op_OP_FIREWALL_RULE_ADD:
		result, err = firewallRuleAdd(ctx, p, logf)
	case agentv1.Op_OP_FIREWALL_RULE_DELETE:
		result, err = firewallRuleDelete(ctx, p, logf)
	case agentv1.Op_OP_FAIL2BAN_STATUS:
		result, err = fail2banStatus(ctx)
	case agentv1.Op_OP_FAIL2BAN_INSTALL:
		result, err = fail2banInstall(ctx, logf)
	case agentv1.Op_OP_FAIL2BAN_CONFIGURE:
		result, err = fail2banConfigure(ctx, p, logf)
	case agentv1.Op_OP_FAIL2BAN_BAN:
		result, err = fail2banBan(ctx, p, true, logf)
	case agentv1.Op_OP_FAIL2BAN_UNBAN:
		result, err = fail2banBan(ctx, p, false, logf)
	case agentv1.Op_OP_WEB_STATUS:
		result, err = webStatus(ctx)
	case agentv1.Op_OP_WEB_INSTALL:
		result, err = webInstall(ctx, p, logf)
	case agentv1.Op_OP_PHP_INSTALL:
		result, err = phpInstall(ctx, p, logf)
	case agentv1.Op_OP_SITE_CREATE:
		result, err = withSiteLock(func() (any, error) { return siteCreate(ctx, p, logf) })
	case agentv1.Op_OP_SITE_UPDATE:
		result, err = withSiteLock(func() (any, error) { return siteUpdate(ctx, p, logf) })
	case agentv1.Op_OP_SITE_DELETE:
		result, err = withSiteLock(func() (any, error) { return siteDelete(ctx, p, logf) })
	case agentv1.Op_OP_DB_STATUS:
		result, err = dbStatus(ctx)
	case agentv1.Op_OP_DB_INSTALL:
		result, err = dbInstall(ctx, logf)
	case agentv1.Op_OP_DB_CREATE:
		result, err = dbCreate(ctx, p, logf)
	case agentv1.Op_OP_DB_DELETE:
		result, err = dbDelete(ctx, p, logf)
	case agentv1.Op_OP_DB_USER_SAVE:
		result, err = dbUserCreate(ctx, p, logf)
	case agentv1.Op_OP_DB_USER_DELETE:
		result, err = dbUserDelete(ctx, p, logf)
	case agentv1.Op_OP_FILES_LIST:
		result, err = filesList(p)
	case agentv1.Op_OP_FILES_READ:
		result, err = filesRead(p)
	case agentv1.Op_OP_FILES_WRITE:
		result, err = filesWrite(p, logf)
	case agentv1.Op_OP_FILES_MKDIR:
		result, err = filesMkdir(p, logf)
	case agentv1.Op_OP_FILES_DELETE:
		result, err = filesDelete(p, logf)
	case agentv1.Op_OP_FILES_RENAME:
		result, err = filesRename(p, logf)
	case agentv1.Op_OP_FILES_CHMOD:
		result, err = filesChmod(p, logf)
	case agentv1.Op_OP_CRON_LIST:
		result, err = cronStatus()
	case agentv1.Op_OP_CRON_SAVE:
		result, err = cronSave(p, logf)
	case agentv1.Op_OP_CRON_DELETE:
		result, err = cronDelete(p, logf)
	case agentv1.Op_OP_CRON_RUN:
		result, err = cronRun(ctx, p, logf)
	case agentv1.Op_OP_BACKUP_LIST:
		result, err = backupStatus()
	case agentv1.Op_OP_BACKUP_CREATE:
		result, err = backupCreate(ctx, p, logf)
	case agentv1.Op_OP_BACKUP_DELETE:
		result, err = backupDelete(p, logf)
	case agentv1.Op_OP_BACKUP_RESTORE:
		result, err = withSiteLock(func() (any, error) { return backupRestore(ctx, p, logf) })
	case agentv1.Op_OP_BACKUP_POLICY:
		result, err = backupPolicy(p, logf)
	case agentv1.Op_OP_SERVICES_LIST:
		result, err = servicesList(ctx)
	case agentv1.Op_OP_SERVICE_ACTION:
		result, err = serviceAction(ctx, p, logf)
	case agentv1.Op_OP_LOGS_READ:
		result, err = logsRead(ctx, p)
	case agentv1.Op_OP_DOCKER_STATUS:
		result, err = dockerStatus(ctx)
	case agentv1.Op_OP_DOCKER_INSTALL:
		result, err = dockerInstall(ctx, logf)
	case agentv1.Op_OP_DOCKER_CONTAINER:
		result, err = dockerContainerAction(ctx, p, logf)
	case agentv1.Op_OP_DOCKER_LOGS:
		result, err = dockerLogs(ctx, p)
	case agentv1.Op_OP_DOCKER_IMAGE_REMOVE:
		result, err = dockerImageRemove(ctx, p, logf)
	case agentv1.Op_OP_DOCKER_PRUNE:
		result, err = dockerPrune(ctx, logf)
	case agentv1.Op_OP_COMPOSE_READ:
		result, err = composeRead(p)
	case agentv1.Op_OP_COMPOSE_SAVE:
		result, err = composeSave(ctx, p, logf)
	case agentv1.Op_OP_COMPOSE_ACTION:
		result, err = composeAction(ctx, p, logf)
	case agentv1.Op_OP_SITE_ACCESS:
		result, err = withSiteLock(func() (any, error) { return siteAccess(ctx, p, logf) })
	case agentv1.Op_OP_APP_DEPLOY:
		result, err = appDeploy(ctx, p, logf)
	case agentv1.Op_OP_APP_UPDATE:
		result, err = appUpdate(ctx, p, logf)
	case agentv1.Op_OP_AGENT_UPDATE:
		result, err = selfUpdate(ctx, p, logf)
	case agentv1.Op_OP_SITE_TLS:
		result, err = withSiteLock(func() (any, error) { return siteTLS(ctx, p, logf) })
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
	if isRHEL() {
		return dnfCheckUpdates(ctx, logf)
	}
	if _, err := exec.LookPath("apt-get"); err != nil {
		return nil, errors.New("apt-get not found: only Debian/Ubuntu are supported for now")
	}
	aptMu.Lock()
	defer aptMu.Unlock()
	if err := aptUpdate(ctx, logf); err != nil {
		return nil, err
	}

	logf("$ apt list --upgradable")
	cmd := exec.CommandContext(ctx, "apt", "list", "--upgradable")
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
