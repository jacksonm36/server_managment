package api

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jacksonm36/servermgr/panel/internal/store"
)

// OpenTofu workspaces run on the panel host with a pinned, checksum-verified
// tofu binary (<data>/tofu/bin/tofu). State lives in the panel database via
// tofu's http backend, served below under /tofu/state/{id} and authorized
// with a token that exists only while a run of that workspace is active.
// Variables are sealed with the panel key; a plan is saved and the exact
// plan is applied.

const (
	tofuVersion   = "1.13.1"
	tofuSHA256    = "378ada19d4bc70c43732004e8159be771b23b9a5afdf059e5f8a2b3fa2c70a69" // tofu_1.13.1_linux_amd64.tar.gz
	tofuTimeout   = time.Hour
	tofuMaxOutput = 2 << 20
	tofuMaxFiles  = 512 << 10
	tofuStateUser = "servermgr"
)

var tofu = struct {
	mu       sync.Mutex
	setupRun bool
	setupErr string
	tokens   map[string]string // token -> workspace id, only during runs
	busy     map[string]string // workspace id -> run id
	cancels  map[string]context.CancelFunc
}{tokens: map[string]string{}, busy: map[string]string{}, cancels: map[string]context.CancelFunc{}}

func (a *API) tofuDir() string {
	d, _ := filepath.Abs(filepath.Join(a.Cfg.DataDir, "tofu"))
	return d
}

func (a *API) tofuBin() string { return filepath.Join(a.tofuDir(), "bin", "tofu") }

// ---- variables ----

// TofuVar is a workspace variable: kind "tf" becomes TF_VAR_<key>, kind
// "env" is set as an environment variable (provider credentials).
type TofuVar struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	Kind      string `json:"kind"`
	Sensitive bool   `json:"sensitive"`
}

var (
	tfVarKey  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	envVarKey = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)
)

// Environment names a workspace may not set: they would change how tofu,
// git or the dynamic linker behave on the panel host.
func reservedEnv(k string) bool {
	for _, p := range []string{"TF_", "LD_", "GIT_", "PYTHON", "SSL_", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY"} {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	switch k {
	case "PATH", "HOME", "USER", "SHELL", "TMPDIR", "IFS":
		return true
	}
	return false
}

func (a *API) tofuVars(w *store.TofuWorkspace) ([]TofuVar, error) {
	if w.SealedVars == "" {
		return []TofuVar{}, nil
	}
	b, err := a.open(w.SealedVars)
	if err != nil {
		return nil, fmt.Errorf("cannot decrypt variables: %v", err)
	}
	var vars []TofuVar
	return vars, json.Unmarshal(b, &vars)
}

// ---- setup ----

func (a *API) tofuInstalled() bool { return fileExists(a.tofuBin()) }

func (a *API) handleTofuSetup(w http.ResponseWriter, r *http.Request) {
	tofu.mu.Lock()
	if tofu.setupRun {
		tofu.mu.Unlock()
		writeErr(w, http.StatusConflict, "setup is already running")
		return
	}
	tofu.setupRun, tofu.setupErr = true, ""
	tofu.mu.Unlock()
	a.audit(r, "", "tofu.setup", tofuVersion, nil, true)
	go func() {
		err := a.installTofu()
		tofu.mu.Lock()
		tofu.setupRun = false
		if err != nil {
			tofu.setupErr = err.Error()
		}
		tofu.mu.Unlock()
		a.Log.Info("opentofu setup finished", "version", tofuVersion, "err", err)
	}()
	w.WriteHeader(http.StatusAccepted)
}

// installTofu downloads the pinned release and verifies its SHA-256 before
// unpacking the tofu binary.
func (a *API) installTofu() error {
	ctx, cancel := context.WithTimeout(a.baseCtx(), 10*time.Minute)
	defer cancel()
	u := fmt.Sprintf("https://github.com/opentofu/opentofu/releases/download/v%[1]s/tofu_%[1]s_linux_amd64.tar.gz", tofuVersion)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 200<<20))
	if err != nil {
		return err
	}
	if sum := sha256.Sum256(body); hex.EncodeToString(sum[:]) != tofuSHA256 {
		return errors.New("checksum mismatch: refusing to install the download")
	}
	gz, err := gzip.NewReader(strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return errors.New("tofu binary not found in the archive")
		}
		if err != nil {
			return err
		}
		if h.Name != "tofu" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(a.tofuBin()), 0o700); err != nil {
			return err
		}
		tmp := a.tofuBin() + ".tmp"
		f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, io.LimitReader(tr, 300<<20)); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		return os.Rename(tmp, a.tofuBin())
	}
}

// ---- http state backend (called by tofu itself) ----

func (a *API) tofuStateAuth(w http.ResponseWriter, r *http.Request) (string, bool) {
	user, pass, ok := r.BasicAuth()
	wsID := r.PathValue("id")
	tofu.mu.Lock()
	want := tofu.tokens[pass]
	tofu.mu.Unlock()
	if !ok || user != tofuStateUser || want == "" || subtle.ConstantTimeCompare([]byte(want), []byte(wsID)) != 1 {
		w.Header().Set("WWW-Authenticate", `Basic realm="servermgr-tofu"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return "", false
	}
	return wsID, true
}

func (a *API) handleTofuState(w http.ResponseWriter, r *http.Request) {
	wsID, ok := a.tofuStateAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	switch r.Method {
	case http.MethodGet:
		st, err := a.Store.GetTofuState(ctx, wsID)
		if errors.Is(err, store.ErrNotFound) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err != nil {
			http.Error(w, "state unavailable", http.StatusInternalServerError)
			return
		}
		plain, err := a.open(st.Sealed)
		if err != nil {
			http.Error(w, "state unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(plain)
	case http.MethodPost:
		holder, err := a.Store.TofuLockHolder(ctx, wsID)
		if err != nil {
			http.Error(w, "lock check failed", http.StatusInternalServerError)
			return
		}
		if holder != "" && holder != r.URL.Query().Get("ID") {
			http.Error(w, "state is locked by another operation", http.StatusConflict)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}
		var head struct {
			Serial int64 `json:"serial"`
		}
		if json.Unmarshal(body, &head) != nil {
			http.Error(w, "state must be JSON", http.StatusBadRequest)
			return
		}
		sealed, err := a.seal(body)
		if err == nil {
			err = a.Store.PutTofuState(ctx, wsID, sealed, head.Serial)
		}
		if err != nil {
			http.Error(w, "save failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		if err := a.Store.DeleteTofuState(ctx, wsID); err != nil {
			http.Error(w, "delete failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	case "LOCK":
		body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		var info struct {
			ID string `json:"ID"`
		}
		if json.Unmarshal(body, &info) != nil || info.ID == "" {
			http.Error(w, "lock info required", http.StatusBadRequest)
			return
		}
		got, cur, err := a.Store.TofuLock(ctx, wsID, info.ID, body)
		if err != nil {
			http.Error(w, "lock failed", http.StatusInternalServerError)
			return
		}
		if !got {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusLocked)
			_, _ = w.Write(cur)
			return
		}
		w.WriteHeader(http.StatusOK)
	case "UNLOCK":
		body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		var info struct {
			ID string `json:"ID"`
		}
		_ = json.Unmarshal(body, &info)
		if _, err := a.Store.TofuUnlock(ctx, wsID, info.ID); err != nil {
			http.Error(w, "unlock failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// ---- workspaces API ----

type tofuVarView struct {
	Key       string `json:"key"`
	Value     string `json:"value"` // empty for sensitive values
	Kind      string `json:"kind"`
	Sensitive bool   `json:"sensitive"`
	HasValue  bool   `json:"has_value"`
}

func (a *API) tofuWorkspaceView(ctx context.Context, w *store.TofuWorkspace) map[string]any {
	vars, err := a.tofuVars(w)
	views := []tofuVarView{}
	for _, v := range vars {
		vv := tofuVarView{Key: v.Key, Kind: v.Kind, Sensitive: v.Sensitive, HasValue: v.Value != ""}
		if !v.Sensitive {
			vv.Value = v.Value
		}
		views = append(views, vv)
	}
	out := map[string]any{"workspace": w, "vars": views}
	if err != nil {
		out["vars_error"] = err.Error()
	}
	if st, err := a.Store.GetTofuState(ctx, w.ID); err == nil {
		out["state"] = map[string]any{"serial": st.Serial, "updated_at": st.UpdatedAt, "resources": a.tofuStateResources(st)}
	}
	if holder, _ := a.Store.TofuLockHolder(ctx, w.ID); holder != "" {
		out["locked"] = true
	}
	if p, err := a.Store.LatestTofuPlan(ctx, w.ID); err == nil {
		out["latest_plan"] = p
	}
	tofu.mu.Lock()
	out["active_run"] = tofu.busy[w.ID]
	tofu.mu.Unlock()
	return out
}

// tofuStateResources lists "type.name" of managed resources in the state.
func (a *API) tofuStateResources(st *store.TofuState) []string {
	plain, err := a.open(st.Sealed)
	if err != nil {
		return nil
	}
	var s struct {
		Resources []struct {
			Module string `json:"module"`
			Mode   string `json:"mode"`
			Type   string `json:"type"`
			Name   string `json:"name"`
		} `json:"resources"`
	}
	if json.Unmarshal(plain, &s) != nil {
		return nil
	}
	out := []string{}
	for _, r := range s.Resources {
		if r.Mode != "managed" {
			continue
		}
		n := r.Type + "." + r.Name
		if r.Module != "" {
			n = r.Module + "." + n
		}
		out = append(out, n)
	}
	return out
}

func (a *API) handleTofuOverview(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.ListTofuWorkspaces(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	views := []map[string]any{}
	for _, ws := range list {
		views = append(views, a.tofuWorkspaceView(r.Context(), ws))
	}
	tofu.mu.Lock()
	setup := map[string]any{"running": tofu.setupRun, "error": tofu.setupErr}
	tofu.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"installed": a.tofuInstalled(), "version": tofuVersion, "setup": setup, "workspaces": views})
}

var (
	tofuNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	tofuFileRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+\.(tf|tf\.json|tfvars|tftpl|tpl|json|yaml|yml|sh|txt)$`)
	backendRe  = regexp.MustCompile(`\bbackend\s+"`)
	gitSubdir  = regexp.MustCompile(`^[\w./-]{0,200}$`)
)

func (a *API) handleSaveTofuWorkspace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string            `json:"name"`
		Description string            `json:"description"`
		Source      string            `json:"source"`
		Files       map[string]string `json:"files"`
		GitURL      string            `json:"git_url"`
		GitRef      string            `json:"git_ref"`
		GitPath     string            `json:"git_path"`
		Vars        []struct {
			TofuVar
			Keep bool `json:"keep"` // sensitive value left blank: keep the stored one
		} `json:"vars"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	ws := &store.TofuWorkspace{ID: r.PathValue("id"), Name: strings.TrimSpace(req.Name), Description: strings.TrimSpace(req.Description),
		Source: req.Source, Files: map[string]string{}, GitURL: req.GitURL, GitRef: req.GitRef, GitPath: strings.Trim(req.GitPath, "/")}
	bad := func(msg string) { writeErr(w, http.StatusBadRequest, msg) }
	if !tofuNameRe.MatchString(ws.Name) {
		bad("name: lowercase letters, digits and dashes")
		return
	}
	if len(ws.Description) > 500 {
		bad("description is too long")
		return
	}
	switch ws.Source {
	case "inline":
		total := 0
		for name, content := range req.Files {
			if !tofuFileRe.MatchString(name) {
				bad(fmt.Sprintf("file name %q is not allowed", name))
				return
			}
			if backendRe.MatchString(content) {
				bad("remove the backend block: servermgr stores the state")
				return
			}
			total += len(content)
			ws.Files[name] = content
		}
		if len(ws.Files) == 0 || len(ws.Files) > 30 || total > tofuMaxFiles {
			bad("add between 1 and 30 files (512 KB total)")
			return
		}
		ws.GitURL, ws.GitRef, ws.GitPath = "", "", ""
	case "git":
		if !gitURLRe.MatchString(ws.GitURL) {
			bad("git URL must be an https:// repository URL")
			return
		}
		if !gitRefRe.MatchString(ws.GitRef) || strings.HasPrefix(ws.GitRef, "-") {
			bad("invalid branch or tag")
			return
		}
		if !gitSubdir.MatchString(ws.GitPath) || strings.Contains(ws.GitPath, "..") {
			bad("invalid directory")
			return
		}
	default:
		bad("source must be inline or git")
		return
	}

	var old []TofuVar
	if ws.ID != "" {
		cur, err := a.Store.GetTofuWorkspace(r.Context(), ws.ID)
		if err != nil {
			writeErr(w, http.StatusNotFound, "workspace not found")
			return
		}
		if old, err = a.tofuVars(cur); err != nil {
			a.internal(w, err)
			return
		}
	}
	vars := []TofuVar{}
	seen := map[string]bool{}
	for _, v := range req.Vars {
		v.Key = strings.TrimSpace(v.Key)
		switch v.Kind {
		case "tf":
			if !tfVarKey.MatchString(v.Key) || v.Key == "servermgr_servers" {
				bad(fmt.Sprintf("variable name %q is not allowed", v.Key))
				return
			}
		case "env":
			if !envVarKey.MatchString(v.Key) || reservedEnv(v.Key) {
				bad(fmt.Sprintf("environment variable %q is not allowed", v.Key))
				return
			}
		default:
			bad("variable kind must be tf or env")
			return
		}
		if seen[v.Kind+v.Key] {
			bad(fmt.Sprintf("variable %q is set twice", v.Key))
			return
		}
		seen[v.Kind+v.Key] = true
		if v.Keep && v.Value == "" {
			for _, o := range old {
				if o.Key == v.Key && o.Kind == v.Kind {
					v.Value = o.Value
				}
			}
		}
		if len(v.Value) > 32<<10 {
			bad(fmt.Sprintf("value of %q is too long", v.Key))
			return
		}
		vars = append(vars, v.TofuVar)
	}
	plain, _ := json.Marshal(vars)
	sealed, err := a.seal(plain)
	if err != nil {
		a.internal(w, err)
		return
	}
	ws.SealedVars = sealed
	if err := a.Store.SaveTofuWorkspace(r.Context(), ws); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "workspace not found")
			return
		}
		if strings.Contains(err.Error(), "unique") {
			writeErr(w, http.StatusConflict, "a workspace with that name exists")
			return
		}
		a.internal(w, err)
		return
	}
	keys := []string{}
	for _, v := range vars {
		keys = append(keys, v.Kind+":"+v.Key)
	}
	a.audit(r, "", "tofu.workspace_save", ws.Name, map[string]any{"source": ws.Source, "git_url": ws.GitURL, "vars": keys}, true)
	saved, _ := a.Store.GetTofuWorkspace(r.Context(), ws.ID)
	writeJSON(w, http.StatusOK, a.tofuWorkspaceView(r.Context(), saved))
}

func (a *API) handleDeleteTofuWorkspace(w http.ResponseWriter, r *http.Request) {
	ws, err := a.Store.GetTofuWorkspace(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "workspace not found")
		return
	}
	if st, err := a.Store.GetTofuState(r.Context(), ws.ID); err == nil && len(a.tofuStateResources(st)) > 0 {
		writeErr(w, http.StatusConflict, "the workspace still manages resources: run Destroy first")
		return
	}
	tofu.mu.Lock()
	busy := tofu.busy[ws.ID] != ""
	tofu.mu.Unlock()
	if busy {
		writeErr(w, http.StatusConflict, "a run is in progress")
		return
	}
	if err := a.Store.DeleteTofuWorkspace(r.Context(), ws.ID); err != nil {
		a.internal(w, err)
		return
	}
	_ = os.RemoveAll(filepath.Join(a.tofuDir(), "work", ws.ID))
	a.audit(r, "", "tofu.workspace_delete", ws.Name, nil, true)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleTofuUnlock(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tofu.mu.Lock()
	busy := tofu.busy[id] != ""
	tofu.mu.Unlock()
	if busy {
		writeErr(w, http.StatusConflict, "a run is in progress; cancel it instead")
		return
	}
	if _, err := a.Store.TofuUnlock(r.Context(), id, ""); err != nil {
		a.internal(w, err)
		return
	}
	a.audit(r, "", "tofu.force_unlock", id, nil, true)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleTofuPlan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Destroy bool `json:"destroy"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	kind := "plan"
	if req.Destroy {
		kind = "destroy-plan"
	}
	run, err := a.startTofuRun(r.Context(), whoFrom(r), r.PathValue("id"), kind, nil)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

// handleTofuApply applies a saved plan. Destroy plans require the
// workspace name typed as confirmation.
func (a *API) handleTofuApply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Confirm string `json:"confirm"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	plan, err := a.Store.GetTofuRun(r.Context(), r.PathValue("run"))
	if err != nil || plan.Kind == "apply" {
		writeErr(w, http.StatusNotFound, "plan not found")
		return
	}
	if plan.Kind == "destroy-plan" {
		if !accessFrom(r.Context()).Can("tofu.manage") {
			writeErr(w, http.StatusForbidden, "only admins can apply a destroy plan")
			return
		}
		if req.Confirm != plan.WorkspaceName {
			writeErr(w, http.StatusBadRequest, "type the workspace name to confirm destroying its resources")
			return
		}
	}
	if latest, err := a.Store.LatestTofuPlan(r.Context(), plan.WorkspaceID); err != nil || latest.ID != plan.ID {
		writeErr(w, http.StatusConflict, "a newer plan exists: apply the latest plan")
		return
	}
	var sum struct {
		Changes bool `json:"changes"`
	}
	_ = json.Unmarshal(plan.Summary, &sum)
	if plan.Status != "succeeded" || plan.Applied || !sum.Changes {
		writeErr(w, http.StatusConflict, "this plan cannot be applied (failed, already applied, or no changes)")
		return
	}
	id := plan.ID
	run, err := a.startTofuRun(r.Context(), whoFrom(r), plan.WorkspaceID, "apply", &id)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

func (a *API) handleListTofuRuns(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.ListTofuRuns(r.Context(), r.URL.Query().Get("workspace"), 100)
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *API) handleGetTofuRun(w http.ResponseWriter, r *http.Request) {
	run, err := a.Store.GetTofuRun(r.Context(), r.PathValue("id"))
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

func (a *API) handleCancelTofuRun(w http.ResponseWriter, r *http.Request) {
	tofu.mu.Lock()
	cancel := tofu.cancels[r.PathValue("id")]
	tofu.mu.Unlock()
	if cancel == nil {
		writeErr(w, http.StatusConflict, "run is not running")
		return
	}
	cancel()
	a.audit(r, "", "tofu.run_cancel", r.PathValue("id"), nil, true)
	w.WriteHeader(http.StatusNoContent)
}

// ---- runner ----

func randToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// servermgrServers is passed as TF_VAR_servermgr_servers: name => id,
// address and OS of every managed server.
func (a *API) servermgrServers(ctx context.Context) string {
	servers, err := a.Store.ListServers(ctx)
	if err != nil {
		return "{}"
	}
	hosts, _ := a.Store.ListAnsibleHosts(ctx)
	m := map[string]map[string]string{}
	for _, s := range servers {
		addr := a.Hub.Addr(s.ID)
		if h := hosts[s.ID]; h != nil {
			addr = h.Address
		}
		m[s.Name] = map[string]string{"id": s.ID, "address": addr, "os": s.OSPretty}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func (a *API) startTofuRun(ctx context.Context, who Who, wsID, kind string, planRunID *string) (*store.TofuRun, error) {
	if !a.tofuInstalled() {
		return nil, errors.New("OpenTofu is not installed on the panel yet")
	}
	ws, err := a.Store.GetTofuWorkspace(ctx, wsID)
	if err != nil {
		return nil, errors.New("workspace not found")
	}
	vars, err := a.tofuVars(ws)
	if err != nil {
		return nil, err
	}
	run := &store.TofuRun{ID: store.NewUUID(), WorkspaceID: ws.ID, WorkspaceName: ws.Name, Kind: kind, PlanRunID: planRunID, CreatedBy: who.Actor}
	tofu.mu.Lock()
	if tofu.busy[ws.ID] != "" {
		tofu.mu.Unlock()
		return nil, errors.New("another run of this workspace is in progress")
	}
	tofu.busy[ws.ID] = run.ID
	tofu.mu.Unlock()
	release := func() {
		tofu.mu.Lock()
		delete(tofu.busy, ws.ID)
		tofu.mu.Unlock()
	}
	if err := a.Store.CreateTofuRun(ctx, run); err != nil {
		release()
		return nil, err
	}
	runCtx, cancel := context.WithTimeout(a.baseCtx(), tofuTimeout)
	tofu.mu.Lock()
	tofu.cancels[run.ID] = cancel
	tofu.mu.Unlock()
	a.auditAs(ctx, who, "tofu."+kind, ws.Name, map[string]any{"run_id": run.ID}, true)
	go func() {
		defer func() {
			cancel()
			tofu.mu.Lock()
			delete(tofu.cancels, run.ID)
			tofu.mu.Unlock()
			release()
		}()
		a.executeTofuRun(runCtx, run, ws, vars)
	}()
	return run, nil
}

var (
	planSummaryRe  = regexp.MustCompile(`Plan: (\d+) to add, (\d+) to change, (\d+) to destroy`)
	applySummaryRe = regexp.MustCompile(`Apply complete! Resources: (\d+) added, (\d+) changed, (\d+) destroyed`)
	ansiRe         = regexp.MustCompile(`\x1b\[[0-9;]*m`)
)

func (a *API) executeTofuRun(ctx context.Context, run *store.TofuRun, ws *store.TofuWorkspace, vars []TofuVar) {
	var (
		mu  sync.Mutex
		out strings.Builder
	)
	write := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		if out.Len() < tofuMaxOutput {
			out.WriteString(ansiRe.ReplaceAllString(s, ""))
			if out.Len() >= tofuMaxOutput {
				out.WriteString("\n[servermgr] output truncated\n")
			}
		}
	}
	snapshot := func() string {
		mu.Lock()
		defer mu.Unlock()
		return out.String()
	}
	stopFlush := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				_ = a.Store.SetTofuRunOutput(a.baseCtx(), run.ID, snapshot())
			case <-stopFlush:
				return
			}
		}
	}()

	token := randToken()
	tofu.mu.Lock()
	tofu.tokens[token] = ws.ID
	tofu.mu.Unlock()
	defer func() {
		tofu.mu.Lock()
		delete(tofu.tokens, token)
		tofu.mu.Unlock()
	}()

	work := filepath.Join(a.tofuDir(), "work", ws.ID)
	planFile := func(id string) string { return filepath.Join(work, "plans", id+".tfplan") }
	summary := map[string]any{}
	status := "failed"

	err := func() error {
		for _, d := range []string{work, filepath.Join(work, "plans"), filepath.Join(work, "home")} {
			if err := os.MkdirAll(d, 0o700); err != nil {
				return err
			}
		}
		// Fresh copy of the configuration every run; providers stay in
		// TF_DATA_DIR and the shared plugin cache.
		src := filepath.Join(work, "src")
		if err := os.RemoveAll(src); err != nil {
			return err
		}
		cfgDir := src
		if ws.Source == "git" {
			args := []string{"clone", "--depth", "1", "--quiet"}
			if ws.GitRef != "" {
				args = append(args, "--branch", ws.GitRef)
			}
			args = append(args, "--", ws.GitURL, src)
			write(fmt.Sprintf("$ git clone %s %s\n", ws.GitURL, ws.GitRef))
			cmd := exec.CommandContext(ctx, "git", args...)
			cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
			if b, err := cmd.CombinedOutput(); err != nil {
				write(string(b))
				return fmt.Errorf("git clone failed: %v", err)
			}
			cfgDir = filepath.Join(src, filepath.FromSlash(ws.GitPath))
			if rel, err := filepath.Rel(src, cfgDir); err != nil || strings.HasPrefix(rel, "..") {
				return errors.New("directory escapes the repository")
			}
			matches, _ := filepath.Glob(filepath.Join(cfgDir, "*.tf"))
			for _, m := range matches {
				if b, err := os.ReadFile(m); err == nil && backendRe.Match(b) {
					return fmt.Errorf("%s declares a backend: remove it, servermgr stores the state", filepath.Base(m))
				}
			}
		} else {
			if err := os.MkdirAll(src, 0o700); err != nil {
				return err
			}
			for name, content := range ws.Files {
				if err := os.WriteFile(filepath.Join(src, name), []byte(content), 0o600); err != nil {
					return err
				}
			}
		}
		if err := os.WriteFile(filepath.Join(cfgDir, "zz_servermgr_backend.tf"), []byte("terraform {\n  backend \"http\" {}\n}\n"), 0o600); err != nil {
			return err
		}

		port := "8090"
		if u, err := url.Parse(a.Cfg.PublicURL); err == nil && u.Port() != "" {
			port = u.Port()
		}
		stateURL := "http://" + net.JoinHostPort("127.0.0.1", port) + "/tofu/state/" + ws.ID
		env := []string{
			"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + filepath.Join(work, "home"), "LANG=C.UTF-8",
			"TF_IN_AUTOMATION=1", "TF_INPUT=0",
			"TF_DATA_DIR=" + filepath.Join(work, "data"),
			"TF_HTTP_ADDRESS=" + stateURL, "TF_HTTP_LOCK_ADDRESS=" + stateURL, "TF_HTTP_UNLOCK_ADDRESS=" + stateURL,
			"TF_HTTP_LOCK_METHOD=LOCK", "TF_HTTP_UNLOCK_METHOD=UNLOCK",
			"TF_HTTP_USERNAME=" + tofuStateUser, "TF_HTTP_PASSWORD=" + token,
			"TF_VAR_servermgr_servers=" + a.servermgrServers(ctx),
		}
		for _, v := range vars {
			if v.Kind == "env" {
				env = append(env, v.Key+"="+v.Value)
			} else {
				env = append(env, "TF_VAR_"+v.Key+"="+v.Value)
			}
		}
		secrets := []string{token}
		for _, v := range vars {
			if v.Sensitive && len(v.Value) >= 4 {
				secrets = append(secrets, v.Value)
			}
		}
		masked := func(s string) {
			for _, sec := range secrets {
				s = strings.ReplaceAll(s, sec, "••••")
			}
			write(s)
		}
		tofuCmd := func(args ...string) (int, error) {
			masked("\n$ tofu " + strings.Join(args, " ") + "\n")
			// Sandboxed: providers, local-exec and external data sources
			// see only this workspace's directory.
			cmd, err := sandboxCmd(ctx, []string{a.tofuBin()}, []string{work}, cfgDir, a.tofuBin(), args...)
			if err != nil {
				return -1, err
			}
			cmd.Env = env
			return runStreamed(cmd, masked)
		}

		if code, err := tofuCmd("init", "-input=false", "-no-color", "-reconfigure"); err != nil || code != 0 {
			return fmt.Errorf("tofu init failed")
		}
		switch run.Kind {
		case "plan", "destroy-plan":
			args := []string{"plan", "-input=false", "-no-color", "-detailed-exitcode", "-out=" + planFile(run.ID)}
			if run.Kind == "destroy-plan" {
				args = append(args, "-destroy")
			}
			code, err := tofuCmd(args...)
			if err != nil {
				return err
			}
			switch code {
			case 0:
				summary["changes"] = false
				_ = os.Remove(planFile(run.ID))
			case 2:
				summary["changes"] = true
				if m := planSummaryRe.FindStringSubmatch(snapshot()); m != nil {
					summary["add"], summary["change"], summary["destroy"] = atoi(m[1]), atoi(m[2]), atoi(m[3])
				}
			default:
				return fmt.Errorf("tofu plan failed (exit %d)", code)
			}
			// Only the newest plan can be applied: drop older plan files.
			old, _ := filepath.Glob(filepath.Join(work, "plans", "*.tfplan"))
			for _, f := range old {
				if f != planFile(run.ID) {
					_ = os.Remove(f)
				}
			}
		case "apply":
			pf := planFile(*run.PlanRunID)
			if !fileExists(pf) {
				return errors.New("the saved plan is gone: plan again")
			}
			code, err := tofuCmd("apply", "-input=false", "-no-color", pf)
			_ = os.Remove(pf)
			_ = a.Store.MarkTofuPlanApplied(a.baseCtx(), *run.PlanRunID)
			if err != nil {
				return err
			}
			if code != 0 {
				return fmt.Errorf("tofu apply failed (exit %d)", code)
			}
			if m := applySummaryRe.FindStringSubmatch(snapshot()); m != nil {
				summary["added"], summary["changed"], summary["destroyed"] = atoi(m[1]), atoi(m[2]), atoi(m[3])
			}
			// Outputs, with sensitive values hidden.
			cmd, err := sandboxCmd(ctx, []string{a.tofuBin()}, []string{work}, cfgDir, a.tofuBin(), "output", "-json", "-no-color")
			if err != nil {
				return err
			}
			cmd.Env = env
			if b, err := cmd.Output(); err == nil {
				var outs map[string]struct {
					Sensitive bool `json:"sensitive"`
					Value     any  `json:"value"`
				}
				if json.Unmarshal(b, &outs) == nil {
					shown := map[string]any{}
					for k, o := range outs {
						if o.Sensitive {
							shown[k] = "(sensitive)"
						} else {
							shown[k] = o.Value
						}
					}
					summary["outputs"] = shown
				}
			}
		}
		return nil
	}()
	close(stopFlush)
	switch {
	case ctx.Err() == context.Canceled:
		status = "cancelled"
		write("\n[servermgr] cancelled\n")
	case err != nil:
		write("\n[servermgr] " + err.Error() + "\n")
	default:
		status = "succeeded"
	}
	if err := a.Store.FinishTofuRun(a.baseCtx(), run.ID, status, snapshot(), summary); err != nil {
		a.Log.Error("tofu: finish run", "run", run.ID, "err", err)
	}
	a.Log.Info("tofu run finished", "run", run.ID, "workspace", ws.Name, "kind", run.Kind, "status", status)
}

func atoi(s string) int { v, _ := strconv.Atoi(s); return v }

// runStreamed runs cmd in its own process group (so cancel stops tofu and
// its providers), streaming combined output line by line. It returns the
// exit code; err is set only when the command could not run or was stopped.
func runStreamed(cmd *exec.Cmd, write func(string)) (int, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 15 * time.Second
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return -1, err
	}
	done := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			write(sc.Text() + "\n")
		}
		_, _ = io.Copy(io.Discard, pr)
		close(done)
	}()
	err := cmd.Wait()
	pw.Close()
	<-done
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() >= 0 {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}
