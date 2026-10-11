package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/crypto/ssh"

	"github.com/jacksonm36/servermgr/panel/internal/store"
)

// ---- recognising keys and filling in names ----

type keyCandidate struct {
	Line        string   `json:"line,omitempty"` // normalised "type base64 comment"
	File        string   `json:"file,omitempty"`
	Type        string   `json:"type,omitempty"`
	Bits        int      `json:"bits,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	Comment     string   `json:"comment,omitempty"`
	Name        string   `json:"name,omitempty"`
	NameFrom    string   `json:"name_from,omitempty"` // ssh config | comment | file name | type
	Hosts       []string `json:"hosts,omitempty"`     // hosts using it in the ssh config
	Existing    string   `json:"existing,omitempty"`  // already in the library as this name
	Warning     string   `json:"warning,omitempty"`
	Error       string   `json:"error,omitempty"`
}

type sshHost struct {
	Alias string
	User  string
}

// parseSSHConfig maps an IdentityFile base name (without .pub) to the hosts
// that use it.
func parseSSHConfig(cfg string) map[string][]sshHost {
	out := map[string][]sshHost{}
	var aliases []string
	var user string
	var files []string
	flush := func() {
		if len(aliases) == 0 {
			return
		}
		for _, f := range files {
			out[f] = append(out[f], sshHost{Alias: aliases[0], User: user})
		}
	}
	for _, raw := range strings.Split(cfg, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, _ := strings.Cut(strings.ReplaceAll(line, "=", " "), " ")
		val = strings.Trim(strings.TrimSpace(val), `"`)
		switch strings.ToLower(key) {
		case "host", "match":
			flush()
			aliases, user, files = nil, "", nil
			if strings.EqualFold(key, "host") {
				for _, a := range strings.Fields(val) {
					if !strings.ContainsAny(a, "*?!") {
						aliases = append(aliases, a)
					}
				}
			}
		case "user":
			user = val
		case "identityfile":
			files = append(files, keyBase(val))
		}
	}
	flush()
	return out
}

// keyBase is a key file's name without directories and ".pub".
func keyBase(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	return strings.TrimSuffix(path.Base(p), ".pub")
}

func keyBits(pub ssh.PublicKey) int {
	cp, ok := pub.(ssh.CryptoPublicKey)
	if !ok {
		return 0
	}
	switch k := cp.CryptoPublicKey().(type) {
	case *rsa.PublicKey:
		return k.N.BitLen()
	case *ecdsa.PublicKey:
		return k.Curve.Params().BitSize
	}
	if strings.Contains(pub.Type(), "ed25519") {
		return 256
	}
	return 0
}

func keyWarning(typ string, bits int) string {
	switch {
	case typ == ssh.KeyAlgoDSA:
		return "DSA keys are insecure and refused by current OpenSSH; replace it with an ed25519 key"
	case typ == ssh.KeyAlgoRSA && bits < 2048:
		return fmt.Sprintf("a %d-bit RSA key is too short to be safe; replace it", bits)
	case typ == ssh.KeyAlgoRSA && bits < 3072:
		return "2048-bit RSA works, but ed25519 or RSA 3072+ is recommended"
	}
	return ""
}

func cleanComment(c string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, c))
}

// parseKeyText finds every public key in a file or pasted text.
func parseKeyText(file, text string) []keyCandidate {
	if strings.Contains(text, "PRIVATE KEY") {
		return []keyCandidate{{File: file, Error: "this is a private key. Never upload it; use the matching .pub file"}}
	}
	var out []keyCandidate
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		pub, comment, options, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			short := line
			if len(short) > 40 {
				short = short[:40] + "…"
			}
			out = append(out, keyCandidate{File: file, Error: "not an OpenSSH public key: " + short})
			continue
		}
		comment = cleanComment(comment)
		c := keyCandidate{
			File: file, Type: pub.Type(), Bits: keyBits(pub), Fingerprint: ssh.FingerprintSHA256(pub), Comment: comment,
			Line: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))),
		}
		if comment != "" {
			c.Line += " " + comment
		}
		c.Warning = keyWarning(c.Type, c.Bits)
		if len(options) > 0 {
			c.Warning = strings.TrimPrefix(c.Warning+"; options like command= were removed", "; ")
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		out = append(out, keyCandidate{File: file, Error: "no public key found"})
	}
	return out
}

func shortType(t string) string {
	t = strings.TrimPrefix(t, "ssh-")
	t = strings.TrimPrefix(t, "sk-ssh-")
	t, _, _ = strings.Cut(t, "@")
	return t
}

// suggestNames fills in Name for each usable candidate: the host alias from
// the ssh config, else a unique comment, else the file name, else the type
// and a short fingerprint. Names never clash with the library or each other.
func suggestNames(cands []keyCandidate, cfg map[string][]sshHost, library []*store.SSHKey) {
	byFP := map[string]string{}
	taken := map[string]bool{}
	comments := map[string]int{}
	for _, k := range library {
		byFP[k.Fingerprint] = k.Name
		taken[strings.ToLower(k.Name)] = true
		comments[strings.ToLower(k.Comment)]++
	}
	for _, c := range cands {
		if c.Error == "" && byFP[c.Fingerprint] == "" {
			comments[strings.ToLower(c.Comment)]++
		}
	}
	seen := map[string]bool{}
	for i := range cands {
		c := &cands[i]
		if c.Error != "" {
			continue
		}
		if name := byFP[c.Fingerprint]; name != "" {
			c.Existing = name
			continue
		}
		if seen[c.Fingerprint] {
			c.Error = "the same key appears twice"
			continue
		}
		seen[c.Fingerprint] = true
		base := ""
		if c.File != "" {
			base = keyBase(c.File)
		}
		hosts := cfg[base]
		for _, h := range hosts {
			if !slices.Contains(c.Hosts, h.Alias) {
				c.Hosts = append(c.Hosts, h.Alias)
			}
		}
		switch {
		case len(hosts) == 1:
			c.Name, c.NameFrom = hosts[0].Alias, "ssh config"
		case len(hosts) > 1:
			users := map[string]int{}
			best := ""
			for _, h := range hosts {
				users[h.User]++
				if best == "" || users[h.User] > users[best] {
					best = h.User
				}
			}
			if best == "" {
				best = "default"
			}
			c.Name, c.NameFrom = best+" main", "ssh config"
		case c.Comment != "" && comments[strings.ToLower(c.Comment)] == 1:
			c.Name, c.NameFrom = c.Comment, "comment"
		case base != "":
			c.Name, c.NameFrom = base, "file name"
			if c.Comment != "" {
				c.Name += " (" + c.Comment + ")"
			}
		case c.Comment != "":
			c.Name, c.NameFrom = c.Comment+" "+strings.TrimPrefix(c.Fingerprint, "SHA256:")[:6], "comment"
		default:
			c.Name, c.NameFrom = shortType(c.Type)+" "+strings.TrimPrefix(c.Fingerprint, "SHA256:")[:8], "type"
		}
		c.Name = uniqueName(truncName(c.Name), taken)
		taken[strings.ToLower(c.Name)] = true
	}
}

func truncName(s string) string {
	if r := []rune(s); len(r) > 60 {
		return string(r[:60])
	}
	return s
}

func uniqueName(name string, taken map[string]bool) string {
	if !taken[strings.ToLower(name)] {
		return name
	}
	for i := 2; ; i++ {
		n := fmt.Sprintf("%s %d", name, i)
		if !taken[strings.ToLower(n)] {
			return n
		}
	}
}

func validKeyName(s string) bool {
	if s == "" || len([]rune(s)) > 64 {
		return false
	}
	return !strings.ContainsFunc(s, unicode.IsControl)
}

// ---- handlers ----

type sshKeyView struct {
	*store.SSHKey
	Warning string `json:"warning,omitempty"`
}

func (a *API) handleListSSHKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := a.Store.ListSSHKeys(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	out := make([]sshKeyView, 0, len(keys))
	for _, k := range keys {
		out = append(out, sshKeyView{k, keyWarning(k.Type, k.Bits)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out, "vault": a.vaultStatus()})
}

// handleParseSSHKeys recognises keys and suggests names; it stores nothing.
func (a *API) handleParseSSHKeys(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Files []struct {
			Name    string `json:"name"`
			Content string `json:"content"`
		} `json:"files"`
		Text      string `json:"text"`
		SSHConfig string `json:"ssh_config"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	var cands []keyCandidate
	for _, f := range req.Files {
		cands = append(cands, parseKeyText(f.Name, f.Content)...)
	}
	if strings.TrimSpace(req.Text) != "" {
		cands = append(cands, parseKeyText("", req.Text)...)
	}
	if len(cands) > 200 {
		writeErr(w, http.StatusBadRequest, "at most 200 keys at once")
		return
	}
	library, err := a.Store.ListSSHKeys(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	suggestNames(cands, parseSSHConfig(req.SSHConfig), library)
	writeJSON(w, http.StatusOK, cands)
}

// handleImportSSHKeys stores keys in the library, sealed by the vault.
func (a *API) handleImportSSHKeys(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Keys []struct {
			Line   string `json:"line"`
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"keys"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Keys) == 0 || len(req.Keys) > 200 {
		writeErr(w, http.StatusBadRequest, "send between 1 and 200 keys")
		return
	}
	if !a.Vault.Configured() {
		writeErr(w, http.StatusLocked, "set up the secrets vault first, so keys are stored encrypted")
		return
	}
	type result struct {
		Name  string `json:"name"`
		ID    int64  `json:"id,omitempty"`
		Error string `json:"error,omitempty"`
	}
	var out []result
	who := whoFrom(r)
	for _, k := range req.Keys {
		name := strings.TrimSpace(k.Name)
		res := result{Name: name}
		cands := parseKeyText("", k.Line)
		switch {
		case !validKeyName(name):
			res.Error = "a name of 1–64 characters is required"
		case len(cands) != 1 || cands[0].Error != "":
			res.Error = "not exactly one valid public key"
		default:
			c := cands[0]
			sealed, err := a.Vault.Seal([]byte(c.Line))
			if err != nil {
				a.secretErr(w, err)
				return
			}
			sk := &store.SSHKey{Name: name, Type: c.Type, Bits: c.Bits, Fingerprint: c.Fingerprint, Comment: c.Comment,
				Sealed: sealed, Source: truncName(cleanComment(k.Source)), CreatedBy: who.Actor}
			err = a.Store.CreateSSHKey(r.Context(), sk)
			switch {
			case errors.Is(err, store.ErrExists):
				res.Error = "that name or key is already in the library"
			case err != nil:
				a.internal(w, err)
				return
			default:
				res.ID = sk.ID
				a.audit(r, "", "ssh_key.import", name, map[string]string{"fingerprint": c.Fingerprint, "type": c.Type}, true)
			}
		}
		out = append(out, res)
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleRenameSSHKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	var req struct {
		Name string `json:"name"`
	}
	if !ok || !readJSON(w, r, &req) {
		if !ok {
			writeErr(w, http.StatusBadRequest, "bad id")
		}
		return
	}
	name := strings.TrimSpace(req.Name)
	if !validKeyName(name) {
		writeErr(w, http.StatusBadRequest, "a name of 1–64 characters is required")
		return
	}
	if err := a.Store.RenameSSHKey(r.Context(), id, name); err != nil {
		a.storeErr(w, err, "key")
		return
	}
	a.audit(r, "", "ssh_key.rename", strconv.FormatInt(id, 10), map[string]string{"name": name}, true)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleDeleteSSHKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	k, err := a.Store.GetSSHKey(r.Context(), id)
	if err != nil {
		a.storeErr(w, err, "key")
		return
	}
	if err := a.Store.DeleteSSHKey(r.Context(), id); err != nil {
		a.internal(w, err)
		return
	}
	a.audit(r, "", "ssh_key.delete", k.Name, map[string]string{"fingerprint": k.Fingerprint}, true)
	w.WriteHeader(http.StatusNoContent)
}

// handleSSHKeyPublic returns the key line, e.g. to copy it.
func (a *API) handleSSHKeyPublic(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	k, err := a.Store.GetSSHKey(r.Context(), id)
	if err != nil {
		a.storeErr(w, err, "key")
		return
	}
	line, err := a.openSecret(k.Sealed)
	if err != nil {
		a.secretErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"line": string(line)})
}

type keyTargetReq struct {
	ServerIDs     []string `json:"server_ids"`
	ServerGroupID *int64   `json:"server_group_id"`
	User          string   `json:"user"`
}

// keyTargets resolves the servers a deploy or removal goes to.
func (a *API) keyTargets(ctx context.Context, req keyTargetReq) ([]string, error) {
	ids := req.ServerIDs
	if req.ServerGroupID != nil {
		g, err := a.Store.GetServerGroup(ctx, *req.ServerGroupID)
		if err != nil {
			return nil, errors.New("server group not found")
		}
		ids = g.ServerIDs
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) == 0 || len(ids) > bulkMax {
		return nil, errors.New("pick between 1 and 500 servers")
	}
	return ids, nil
}

func (a *API) keyJobs(w http.ResponseWriter, r *http.Request, deploy bool) {
	id, ok := pathID(r)
	var req keyTargetReq
	if !ok || !readJSON(w, r, &req) {
		if !ok {
			writeErr(w, http.StatusBadRequest, "bad id")
		}
		return
	}
	if req.User == "" {
		req.User = "root"
	}
	k, err := a.Store.GetSSHKey(r.Context(), id)
	if err != nil {
		a.storeErr(w, err, "key")
		return
	}
	ids, err := a.keyTargets(r.Context(), req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	op, params := "ssh_keys.remove", map[string]string{"user": req.User, "fingerprint": k.Fingerprint}
	if deploy {
		line, err := a.openSecret(k.Sealed)
		if err != nil {
			a.secretErr(w, err)
			return
		}
		op, params = "ssh_keys.add", map[string]string{"user": req.User, "key": string(line), "comment": k.Name}
	}
	who := whoFrom(r)
	out := make([]bulkResult, 0, len(ids))
	for _, sid := range ids {
		res := bulkResult{ServerID: sid}
		if job, err := a.Dispatch(r.Context(), who, sid, op, params); err != nil {
			res.Error = err.Error()
		} else {
			res.JobID = job.ID
		}
		out = append(out, res)
	}
	action := "ssh_key.undeploy"
	if deploy {
		action = "ssh_key.deploy"
	}
	a.audit(r, "", action, k.Name, map[string]any{"fingerprint": k.Fingerprint, "user": req.User, "servers": len(ids)}, true)
	writeJSON(w, http.StatusAccepted, out)
}

func (a *API) handleDeploySSHKey(w http.ResponseWriter, r *http.Request)   { a.keyJobs(w, r, true) }
func (a *API) handleUndeploySSHKey(w http.ResponseWriter, r *http.Request) { a.keyJobs(w, r, false) }

type scannedKey struct {
	Type        string `json:"type"`
	Fingerprint string `json:"fingerprint"`
	Comment     string `json:"comment"`
}

type scannedUser struct {
	User string       `json:"user"`
	Keys []scannedKey `json:"keys"`
}

type scannedServer struct {
	ServerID string        `json:"server_id"`
	Name     string        `json:"name"`
	Users    []scannedUser `json:"users"`
	Error    string        `json:"error,omitempty"`
}

// handleScanSSHKeys reads authorized_keys of every login user on every online
// server the caller can see, so the UI can show where each key is installed.
func (a *API) handleScanSSHKeys(w http.ResponseWriter, r *http.Request) {
	servers, err := a.Store.ListServers(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	who := whoFrom(r)
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	list := func(sid, user string) (*struct {
		Keys  []scannedKey `json:"keys"`
		Users []struct {
			Name string `json:"name"`
		} `json:"users"`
	}, error) {
		job, err := a.Dispatch(ctx, who, sid, "ssh_keys.list", map[string]string{"user": user})
		if err != nil {
			return nil, err
		}
		if job, err = a.waitJob(ctx, job.ID, 20*time.Second); err != nil {
			return nil, err
		}
		if job.Status != "succeeded" {
			return nil, errors.New(job.Error)
		}
		var res struct {
			Keys  []scannedKey `json:"keys"`
			Users []struct {
				Name string `json:"name"`
			} `json:"users"`
		}
		return &res, json.Unmarshal(job.Result, &res)
	}
	out := []*scannedServer{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, s := range servers {
		if !who.Access.CanOn("server.view", s.ID) || !a.Hub.Online(s.ID) {
			continue
		}
		ss := &scannedServer{ServerID: s.ID, Name: s.Name, Users: []scannedUser{}}
		out = append(out, ss)
		wg.Add(1)
		go func() {
			defer wg.Done()
			root, err := list(s.ID, "root")
			if err != nil {
				mu.Lock()
				ss.Error = err.Error()
				mu.Unlock()
				return
			}
			users := []scannedUser{{User: "root", Keys: root.Keys}}
			for _, u := range root.Users {
				if u.Name == "root" {
					continue
				}
				if res, err := list(s.ID, u.Name); err == nil {
					users = append(users, scannedUser{User: u.Name, Keys: res.Keys})
				}
			}
			mu.Lock()
			ss.Users = users
			mu.Unlock()
		}()
	}
	wg.Wait()
	slices.SortFunc(out, func(x, y *scannedServer) int { return strings.Compare(x.Name, y.Name) })
	writeJSON(w, http.StatusOK, out)
}

// storedKeyParam keeps the fingerprint, not the key, in job history.
func storedKeyParam(line string) string {
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return "[invalid key]"
	}
	return ssh.FingerprintSHA256(pub)
}
