package api

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"

	"github.com/jacksonm36/servermgr/panel/internal/store"
)

// Ansible runs on the panel host from its own venv
// (<data>/ansible/venv, installed from the UI). It connects to servers over
// SSH as root with the panel's key, which the agent authorizes (from the
// panel's address only) with the ansible.prepare op; the host keys the
// agent reports are pinned, so there is no trust on first use.

const (
	ansibleCoreSpec   = "ansible-core>=2.18,<2.20"
	ansibleRunTimeout = 2 * time.Hour
	ansibleMaxOutput  = 2 << 20
	ansibleMaxContent = 256 << 10
)

var ansible = struct {
	mu       sync.Mutex
	setupRun bool
	setupLog string
	setupErr string
	version  string
	cancels  map[string]context.CancelFunc
}{cancels: map[string]context.CancelFunc{}}

func (a *API) ansibleDir() string {
	d, _ := filepath.Abs(filepath.Join(a.Cfg.DataDir, "ansible"))
	return d
}

func (a *API) ansibleBin(name string) string {
	return filepath.Join(a.ansibleDir(), "venv", "bin", name)
}

func (a *API) ansibleVersion() string {
	ansible.mu.Lock()
	v := ansible.version
	ansible.mu.Unlock()
	if v != "" {
		return v
	}
	out, err := exec.Command(a.ansibleBin("ansible-playbook"), "--version").Output()
	if err != nil {
		return ""
	}
	v = strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	ansible.mu.Lock()
	ansible.version = v
	ansible.mu.Unlock()
	return v
}

// ansibleKey returns the private key path and the public key, creating
// the key pair on first use.
func (a *API) ansibleKey() (string, string, error) {
	path := filepath.Join(a.ansibleDir(), "id_ed25519")
	if b, err := os.ReadFile(path); err == nil {
		k, err := ssh.ParsePrivateKey(b)
		if err != nil {
			return "", "", err
		}
		return path, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k.PublicKey()))), nil
	}
	if err := os.MkdirAll(a.ansibleDir(), 0o700); err != nil {
		return "", "", err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	blk, err := ssh.MarshalPrivateKey(priv, "servermgr-ansible")
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(blk), 0o600); err != nil {
		return "", "", err
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", err
	}
	return path, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp))), nil
}

// sourceIPFor is the local address the panel uses to reach addr; servers
// only accept the panel key from there.
func sourceIPFor(addr string) (string, error) {
	c, err := net.Dial("udp", net.JoinHostPort(addr, "22"))
	if err != nil {
		return "", err
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP.String(), nil
}

// ansiblePrepareParams builds the agent params for ansible.prepare: the
// panel key and source address always come from the panel. The address to
// SSH to is the caller's override or the address the agent connects from.
func (a *API) ansiblePrepareParams(serverID string, in map[string]string) (map[string]string, string, error) {
	addr := strings.TrimSpace(in["address"])
	if addr == "" {
		addr = a.Hub.Addr(serverID)
	}
	if net.ParseIP(addr) == nil {
		return nil, "", errors.New("no usable address for this server; enter its IP")
	}
	from, err := sourceIPFor(addr)
	if err != nil {
		return nil, "", fmt.Errorf("cannot route to %s: %v", addr, err)
	}
	_, pub, err := a.ansibleKey()
	if err != nil {
		return nil, "", err
	}
	return map[string]string{"pubkey": pub, "from": from}, addr, nil
}

// recordAnsibleHost stores the pinned host keys once ansible.prepare finishes.
func (a *API) recordAnsibleHost(jobID, serverID, addr string) {
	ctx := a.baseCtx()
	job, err := a.waitJob(ctx, jobID, stepTimeout)
	if err != nil || job.Status != "succeeded" {
		return
	}
	var res struct {
		HostKeys  []string `json:"host_keys"`
		Ports     []string `json:"ports"`
		RootLogin string   `json:"root_login"`
	}
	if err := json.Unmarshal(job.Result, &res); err != nil || len(res.HostKeys) == 0 {
		a.Log.Error("ansible: bad prepare result", "server", serverID, "err", err)
		return
	}
	port := 22
	if len(res.Ports) > 0 {
		if p, err := strconv.Atoi(res.Ports[0]); err == nil {
			port = p
		}
	}
	h := &store.AnsibleHost{ServerID: serverID, Address: addr, Port: port, HostKeys: res.HostKeys, RootLogin: res.RootLogin}
	if err := a.Store.SaveAnsibleHost(ctx, h); err != nil {
		a.Log.Error("ansible: save host", "server", serverID, "err", err)
	}
}

// ---- handlers ----

func (a *API) handleAnsibleOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	servers, err := a.Store.ListServers(ctx)
	if err != nil {
		a.internal(w, err)
		return
	}
	hosts, err := a.Store.ListAnsibleHosts(ctx)
	if err != nil {
		a.internal(w, err)
		return
	}
	type hostRow struct {
		ServerID   string     `json:"server_id"`
		Name       string     `json:"name"`
		Online     bool       `json:"online"`
		Address    string     `json:"address"`
		Port       int        `json:"port"`
		RootLogin  string     `json:"root_login"`
		PreparedAt *time.Time `json:"prepared_at"`
	}
	rows := []hostRow{}
	for _, s := range servers {
		row := hostRow{ServerID: s.ID, Name: s.Name, Online: a.Hub.Online(s.ID), Address: a.Hub.Addr(s.ID)}
		if h := hosts[s.ID]; h != nil {
			row.Address, row.Port, row.RootLogin, row.PreparedAt = h.Address, h.Port, h.RootLogin, &h.PreparedAt
		}
		rows = append(rows, row)
	}
	pub := ""
	if fileExists(filepath.Join(a.ansibleDir(), "id_ed25519")) {
		_, pub, _ = a.ansibleKey()
	}
	ansible.mu.Lock()
	setup := map[string]any{"running": ansible.setupRun, "log": ansible.setupLog, "error": ansible.setupErr}
	ansible.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"installed": fileExists(a.ansibleBin("ansible-playbook")), "version": a.ansibleVersion(),
		"setup": setup, "public_key": pub, "hosts": rows,
	})
}

// handleAnsibleSetup creates the venv and installs ansible-core in the
// background; the overview reports progress.
func (a *API) handleAnsibleSetup(w http.ResponseWriter, r *http.Request) {
	ansible.mu.Lock()
	if ansible.setupRun {
		ansible.mu.Unlock()
		writeErr(w, http.StatusConflict, "setup is already running")
		return
	}
	ansible.setupRun, ansible.setupLog, ansible.setupErr, ansible.version = true, "", "", ""
	ansible.mu.Unlock()
	a.audit(r, "", "ansible.setup", ansibleCoreSpec, nil, true)
	go func() {
		ctx, cancel := context.WithTimeout(a.baseCtx(), 15*time.Minute)
		defer cancel()
		logf := func(s string) {
			ansible.mu.Lock()
			ansible.setupLog += s
			ansible.mu.Unlock()
		}
		err := func() error {
			if _, _, err := a.ansibleKey(); err != nil {
				return err
			}
			venv := filepath.Join(a.ansibleDir(), "venv")
			steps := [][]string{
				{"python3", "-m", "venv", venv},
				{filepath.Join(venv, "bin", "pip"), "install", "--disable-pip-version-check", "--upgrade", "pip"},
				{filepath.Join(venv, "bin", "pip"), "install", "--disable-pip-version-check", ansibleCoreSpec},
			}
			for _, s := range steps {
				logf("$ " + strings.Join(s, " ") + "\n")
				out, err := exec.CommandContext(ctx, s[0], s[1:]...).CombinedOutput()
				logf(lastLines(string(out), 15))
				if err != nil {
					return fmt.Errorf("%s: %v", filepath.Base(s[0]), err)
				}
			}
			return nil
		}()
		ansible.mu.Lock()
		ansible.setupRun = false
		if err != nil {
			ansible.setupErr = err.Error()
		}
		ansible.mu.Unlock()
		a.Log.Info("ansible setup finished", "err", err, "version", a.ansibleVersion())
	}()
	w.WriteHeader(http.StatusAccepted)
}

func lastLines(s string, n int) string {
	l := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	if len(l) == 1 && l[0] == "" {
		return ""
	}
	return strings.Join(l, "\n") + "\n"
}

func (a *API) handleAnsiblePrepare(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Address string `json:"address"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	p := map[string]string{}
	if req.Address != "" {
		p["address"] = req.Address
	}
	job, err := a.Dispatch(r.Context(), whoFrom(r), r.PathValue("id"), "ansible.prepare", p)
	if err != nil {
		a.writeDispatchErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (a *API) handleAnsibleRevoke(w http.ResponseWriter, r *http.Request) {
	job, err := a.Dispatch(r.Context(), whoFrom(r), r.PathValue("id"), "ansible.revoke", nil)
	if err != nil {
		a.writeDispatchErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (a *API) handleListAnsiblePlaybooks(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.ListAnsiblePlaybooks(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

var (
	gitURLRe  = regexp.MustCompile(`^https://[A-Za-z0-9.-]+(:\d+)?/[\w./~%-]+$`)
	gitRefRe  = regexp.MustCompile(`^[\w./-]{0,100}$`)
	gitPathRe = regexp.MustCompile(`^[\w./-]+\.ya?ml$`)
)

func validateAnsiblePlaybook(p *store.AnsiblePlaybook) error {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" || len(p.Name) > 80 {
		return errors.New("name is required (max 80 characters)")
	}
	if len(p.Description) > 500 {
		return errors.New("description is too long")
	}
	switch p.Source {
	case "inline", "":
		p.Source, p.GitURL, p.GitRef, p.GitPath = "inline", "", "", ""
		if len(p.Content) > ansibleMaxContent {
			return errors.New("playbook is too large")
		}
		var plays []map[string]any
		if err := yaml.Unmarshal([]byte(p.Content), &plays); err != nil {
			return fmt.Errorf("playbook YAML: %v", err)
		}
		if len(plays) == 0 {
			return errors.New("a playbook is a YAML list of plays")
		}
	case "git":
		p.Content = ""
		if !gitURLRe.MatchString(p.GitURL) {
			return errors.New("git URL must be an https:// repository URL")
		}
		if !gitRefRe.MatchString(p.GitRef) || strings.HasPrefix(p.GitRef, "-") {
			return errors.New("invalid branch or tag")
		}
		if !gitPathRe.MatchString(p.GitPath) || strings.Contains(p.GitPath, "..") || strings.HasPrefix(p.GitPath, "/") {
			return errors.New("playbook path must be a .yml file inside the repository")
		}
	default:
		return errors.New("source must be inline or git")
	}
	return nil
}

func (a *API) handleSaveAnsiblePlaybook(w http.ResponseWriter, r *http.Request) {
	var p store.AnsiblePlaybook
	if !readJSON(w, r, &p) {
		return
	}
	p.ID = r.PathValue("id")
	if err := validateAnsiblePlaybook(&p); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.Store.SaveAnsiblePlaybook(r.Context(), &p); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "playbook not found")
			return
		}
		if strings.Contains(err.Error(), "unique") {
			writeErr(w, http.StatusConflict, "a playbook with that name exists")
			return
		}
		a.internal(w, err)
		return
	}
	a.audit(r, "", "ansible.playbook_save", p.Name, map[string]string{"source": p.Source, "git_url": p.GitURL}, true)
	saved, _ := a.Store.GetAnsiblePlaybook(r.Context(), p.ID)
	writeJSON(w, http.StatusOK, saved)
}

func (a *API) handleDeleteAnsiblePlaybook(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.DeleteAnsiblePlaybook(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "playbook not found")
			return
		}
		a.internal(w, err)
		return
	}
	a.audit(r, "", "ansible.playbook_delete", r.PathValue("id"), nil, true)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleRunAnsiblePlaybook(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServerIDs []string `json:"server_ids"`
		Check     bool     `json:"check"`
		ExtraVars string   `json:"extra_vars"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	pb, err := a.Store.GetAnsiblePlaybook(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "playbook not found")
		return
	}
	run, err := a.StartAnsibleRun(r.Context(), whoFrom(r), pb, req.ServerIDs, req.Check, req.ExtraVars)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

func (a *API) handleListAnsibleRuns(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.ListAnsibleRuns(r.Context(), 100)
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleGetAnsibleRun returns the run; ?offset=N returns only the output
// after byte N so the UI can follow a live run cheaply.
func (a *API) handleGetAnsibleRun(w http.ResponseWriter, r *http.Request) {
	run, err := a.Store.GetAnsibleRun(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "run not found")
		return
	}
	total := len(run.Output)
	if off, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && off > 0 && off <= total {
		run.Output = run.Output[off:]
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run, "output": run.Output, "total": total})
}

func (a *API) handleCancelAnsibleRun(w http.ResponseWriter, r *http.Request) {
	ansible.mu.Lock()
	cancel := ansible.cancels[r.PathValue("id")]
	ansible.mu.Unlock()
	if cancel == nil {
		writeErr(w, http.StatusConflict, "run is not running")
		return
	}
	cancel()
	a.audit(r, "", "ansible.run_cancel", r.PathValue("id"), nil, true)
	w.WriteHeader(http.StatusNoContent)
}

// runAnsibleSchedule runs an Ansible playbook from a schedule on the
// targets that are prepared for Ansible, and summarizes in one line.
func (a *API) runAnsibleSchedule(ctx context.Context, who Who, pbID string, ids []string, check bool) string {
	pb, err := a.Store.GetAnsiblePlaybook(ctx, pbID)
	if err != nil {
		return "error: Ansible playbook not found"
	}
	hosts, err := a.Store.ListAnsibleHosts(ctx)
	if err != nil {
		return "error: " + err.Error()
	}
	var ready []string
	for _, id := range ids {
		if hosts[id] != nil {
			ready = append(ready, id)
		}
	}
	if len(ready) == 0 {
		return "skipped: no target server is prepared for Ansible"
	}
	run, err := a.StartAnsibleRun(ctx, who, pb, ready, check, "")
	if err != nil {
		return "error: " + err.Error()
	}
	res := fmt.Sprintf("started Ansible %q on %d server(s) (run %s)", pb.Name, len(ready), run.ID[:8])
	if n := len(ids) - len(ready); n > 0 {
		res += fmt.Sprintf(", %d not prepared skipped", n)
	}
	return res
}

// ---- runner ----

var inventoryName = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

// StartAnsibleRun validates the targets and starts ansible-playbook in the
// background. Used by the UI and by schedules.
func (a *API) StartAnsibleRun(ctx context.Context, who Who, pb *store.AnsiblePlaybook, serverIDs []string, check bool, extraVars string) (*store.AnsibleRun, error) {
	if !fileExists(a.ansibleBin("ansible-playbook")) {
		return nil, errors.New("Ansible is not installed on the panel yet")
	}
	if len(serverIDs) == 0 {
		return nil, errors.New("pick at least one server")
	}
	var vars map[string]any
	if strings.TrimSpace(extraVars) != "" {
		if err := yaml.Unmarshal([]byte(extraVars), &vars); err != nil || vars == nil {
			return nil, errors.New("extra vars must be a YAML or JSON mapping")
		}
	}
	hosts, err := a.Store.ListAnsibleHosts(ctx)
	if err != nil {
		return nil, err
	}
	type target struct {
		name string
		host *store.AnsibleHost
	}
	var targets []target
	used := map[string]bool{}
	for _, id := range serverIDs {
		srv, err := a.Store.GetServer(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("server %s not found", id)
		}
		h := hosts[id]
		if h == nil {
			return nil, fmt.Errorf("%s is not set up for Ansible yet (Prepare it first)", srv.Name)
		}
		name := strings.Trim(inventoryName.ReplaceAllString(srv.Name, "-"), "-")
		if name == "" || used[name] {
			name += "-" + id[:6]
		}
		used[name] = true
		targets = append(targets, target{name, h})
	}
	keyPath, _, err := a.ansibleKey()
	if err != nil {
		return nil, err
	}

	run := &store.AnsibleRun{ID: store.NewUUID(), PlaybookName: pb.Name, ServerIDs: serverIDs, Check: check, Status: "running", CreatedBy: who.Actor}
	if pb.ID != "" {
		id := pb.ID
		run.PlaybookID = &id
	}
	if err := a.Store.CreateAnsibleRun(ctx, run); err != nil {
		return nil, err
	}

	// Work dir: inventory, pinned known_hosts, config, playbook.
	if err := os.MkdirAll(filepath.Join(a.ansibleDir(), "runs"), 0o700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(filepath.Join(a.ansibleDir(), "runs"), "r")
	if err != nil {
		return nil, err
	}
	var inv, kh strings.Builder
	inv.WriteString("[servermgr]\n")
	for _, t := range targets {
		fmt.Fprintf(&inv, "%s ansible_host=%s ansible_port=%d\n", t.name, t.host.Address, t.host.Port)
		hostKey := t.host.Address
		if t.host.Port != 22 {
			hostKey = fmt.Sprintf("[%s]:%d", t.host.Address, t.host.Port)
		}
		for _, k := range t.host.HostKeys {
			fmt.Fprintf(&kh, "%s %s\n", hostKey, k)
		}
	}
	cfg := fmt.Sprintf(`[defaults]
inventory = %[1]s/inventory.ini
remote_user = root
private_key_file = %[2]s
host_key_checking = True
forks = 10
timeout = 20
interpreter_python = auto_silent
retry_files_enabled = False
nocolor = True
local_tmp = %[1]s/tmp

[ssh_connection]
ssh_args = -o ControlMaster=auto -o ControlPersist=60s -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=%[1]s/known_hosts -o GlobalKnownHostsFile=/dev/null
control_path_dir = %[1]s/cp
pipelining = True
`, dir, keyPath)
	files := map[string]string{"inventory.ini": inv.String(), "known_hosts": kh.String(), "ansible.cfg": cfg}
	if pb.Source == "inline" {
		files["playbook.yml"] = pb.Content
	}
	if vars != nil {
		b, _ := json.Marshal(vars)
		files["vars.json"] = string(b)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			os.RemoveAll(dir)
			return nil, err
		}
	}

	runCtx, cancel := context.WithTimeout(a.baseCtx(), ansibleRunTimeout)
	ansible.mu.Lock()
	ansible.cancels[run.ID] = cancel
	ansible.mu.Unlock()
	a.auditAs(ctx, who, "ansible.run", pb.Name, map[string]any{"servers": len(targets), "check": check, "run_id": run.ID}, true)
	go a.executeAnsibleRun(runCtx, cancel, run, pb, dir, vars != nil)
	return run, nil
}

var recapRe = regexp.MustCompile(`^(\S+)\s+:\s+ok=(\d+)\s+changed=(\d+)\s+unreachable=(\d+)\s+failed=(\d+)\s+skipped=(\d+)`)

func (a *API) executeAnsibleRun(ctx context.Context, cancel context.CancelFunc, run *store.AnsibleRun, pb *store.AnsiblePlaybook, dir string, hasVars bool) {
	defer func() {
		cancel()
		ansible.mu.Lock()
		delete(ansible.cancels, run.ID)
		ansible.mu.Unlock()
		os.RemoveAll(dir)
	}()
	var (
		mu  sync.Mutex
		out strings.Builder
	)
	write := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		if out.Len() < ansibleMaxOutput {
			out.WriteString(s)
			if out.Len() >= ansibleMaxOutput {
				out.WriteString("\n[servermgr] output truncated\n")
			}
		}
	}
	snapshot := func() string {
		mu.Lock()
		defer mu.Unlock()
		return out.String()
	}

	status := "failed"
	err := func() error {
		playbook := filepath.Join(dir, "playbook.yml")
		if pb.Source == "git" {
			repo := filepath.Join(dir, "repo")
			args := []string{"clone", "--depth", "1", "--quiet"}
			if pb.GitRef != "" {
				args = append(args, "--branch", pb.GitRef)
			}
			args = append(args, "--", pb.GitURL, repo)
			write(fmt.Sprintf("$ git clone %s %s\n", pb.GitURL, pb.GitRef))
			cmd := exec.CommandContext(ctx, "git", args...)
			cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
			if b, err := cmd.CombinedOutput(); err != nil {
				write(string(b))
				return fmt.Errorf("git clone failed: %v", err)
			}
			playbook = filepath.Join(repo, filepath.FromSlash(pb.GitPath))
			if rel, err := filepath.Rel(repo, playbook); err != nil || strings.HasPrefix(rel, "..") {
				return errors.New("playbook path escapes the repository")
			}
			if !fileExists(playbook) {
				return fmt.Errorf("%s not found in the repository", pb.GitPath)
			}
		}
		args := []string{playbook}
		if run.Check {
			args = append(args, "--check", "--diff")
		}
		if hasVars {
			args = append(args, "-e", "@"+filepath.Join(dir, "vars.json"))
		}
		write(fmt.Sprintf("$ ansible-playbook %s%s\n", pb.Name, map[bool]string{true: " --check --diff", false: ""}[run.Check]))
		cmd := exec.CommandContext(ctx, a.ansibleBin("ansible-playbook"), args...)
		cmd.Dir = filepath.Dir(playbook)
		cmd.Env = append(os.Environ(), "ANSIBLE_CONFIG="+filepath.Join(dir, "ansible.cfg"), "PYTHONUNBUFFERED=1", "ANSIBLE_FORCE_COLOR=0")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
		cmd.WaitDelay = 10 * time.Second
		pr, pw := io.Pipe()
		cmd.Stdout, cmd.Stderr = pw, pw
		if err := cmd.Start(); err != nil {
			return err
		}
		done := make(chan struct{})
		go func() {
			sc := bufio.NewScanner(pr)
			sc.Buffer(make([]byte, 64<<10), 1<<20)
			for sc.Scan() {
				write(sc.Text() + "\n")
			}
			io.Copy(io.Discard, pr)
			close(done)
		}()
		// Flush output to the DB while it runs so the UI can follow it.
		tick := time.NewTicker(time.Second)
		waitErr := make(chan error, 1)
		go func() { waitErr <- cmd.Wait(); pw.Close() }()
		var werr error
	loop:
		for {
			select {
			case <-tick.C:
				_ = a.Store.SetAnsibleRunOutput(a.baseCtx(), run.ID, snapshot())
			case werr = <-waitErr:
				break loop
			}
		}
		tick.Stop()
		<-done
		if ctx.Err() == context.Canceled {
			status = "cancelled"
			return errors.New("cancelled")
		}
		if ctx.Err() == context.DeadlineExceeded {
			return errors.New("timed out")
		}
		if werr != nil {
			return werr
		}
		status = "succeeded"
		return nil
	}()
	if err != nil {
		write("\n[servermgr] " + err.Error() + "\n")
	}

	final := snapshot()
	recap := map[string]*store.AnsibleRecap{}
	if i := strings.LastIndex(final, "PLAY RECAP"); i >= 0 {
		for _, l := range strings.Split(final[i:], "\n") {
			m := recapRe.FindStringSubmatch(strings.TrimSpace(l))
			if m == nil {
				continue
			}
			n := func(s string) int { v, _ := strconv.Atoi(s); return v }
			recap[m[1]] = &store.AnsibleRecap{OK: n(m[2]), Changed: n(m[3]), Unreachable: n(m[4]), Failed: n(m[5]), Skipped: n(m[6])}
		}
	}
	if err := a.Store.FinishAnsibleRun(a.baseCtx(), run.ID, status, final, recap); err != nil {
		a.Log.Error("ansible: finish run", "run", run.ID, "err", err)
	}
	names := make([]string, 0, len(recap))
	for h := range recap {
		names = append(names, h)
	}
	sort.Strings(names)
	a.Log.Info("ansible run finished", "run", run.ID, "playbook", pb.Name, "status", status, "hosts", names)
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
