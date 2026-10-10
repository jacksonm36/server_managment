package api

import (
	"crypto/rand"
	"encoding/json"
	"math/big"
	"net/http"
	"regexp"
	"strings"

	"github.com/jacksonm36/servermgr/panel/internal/catalog"
	"github.com/jacksonm36/servermgr/panel/internal/store"
)

const pwAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"

func genPassword(n int) string {
	b := make([]byte, n)
	for i := range b {
		k, _ := rand.Int(rand.Reader, big.NewInt(int64(len(pwAlphabet))))
		b[i] = pwAlphabet[k.Int64()]
	}
	return string(b)
}

var nonIdent = regexp.MustCompile(`[^a-z0-9]+`)

// dbNames derives a database and user name from the domain.
func dbNames(domain string) (db, user string) {
	slug := strings.Trim(nonIdent.ReplaceAllString(strings.ToLower(domain), "_"), "_")
	if len(slug) > 24 {
		slug = slug[:24]
	}
	suffix := strings.ToLower(genPassword(4))
	return "app_" + slug + "_" + suffix, ("u_" + slug)[:min(len("u_"+slug), 26)] + "_" + suffix
}

func (a *API) handleListApps(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, catalog.List())
}

var adminUserRe = regexp.MustCompile(`^[a-zA-Z0-9._-]{3,40}$`)

// handleInstallApp creates the site, database and app on a server as one
// playbook run, and returns the generated credentials once.
func (a *API) handleInstallApp(w http.ResponseWriter, r *http.Request) {
	var req struct {
		App        string `json:"app"`
		Domain     string `json:"domain"`
		Aliases    string `json:"aliases"`
		PHPVersion string `json:"php_version"`
		Title      string `json:"title"`
		AdminUser  string `json:"admin_user"`
		AdminEmail string `json:"admin_email"`
		TLS        string `json:"tls"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	app, ok := catalog.Get(req.App)
	if !ok {
		writeErr(w, http.StatusBadRequest, "unknown app")
		return
	}
	srv, err := a.Store.GetServer(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "server not found")
		return
	}
	req.Domain = strings.ToLower(strings.TrimSpace(req.Domain))
	if req.Title == "" {
		req.Title = req.Domain
	}
	if app.Admin {
		if !adminUserRe.MatchString(req.AdminUser) {
			writeErr(w, http.StatusBadRequest, "admin user: 3-40 letters, digits, . _ -")
			return
		}
		if !strings.Contains(req.AdminEmail, "@") || len(req.AdminEmail) > 200 {
			writeErr(w, http.StatusBadRequest, "a valid admin email is required")
			return
		}
	}
	switch req.TLS {
	case "", "selfsigned", "letsencrypt":
	default:
		writeErr(w, http.StatusBadRequest, "tls must be empty, selfsigned or letsencrypt")
		return
	}
	scheme := "http"
	if req.TLS != "" {
		scheme = "https"
	}
	creds := map[string]string{"url": scheme + "://" + req.Domain + "/"}
	vars := map[string]string{"site_url": scheme + "://" + req.Domain, "title": req.Title,
		// Salts/keys for app config files (masked in job output like passwords).
		"secret_32": genPassword(32), "secret_64": genPassword(64)}
	steps := []store.Step{{Op: "site.create", Params: map[string]string{"domain": req.Domain, "aliases": req.Aliases, "type": "php", "php_version": req.PHPVersion}}}
	if app.Database {
		db, user := dbNames(req.Domain)
		pw := genPassword(24)
		vars["db_name"], vars["db_user"], vars["db_password"] = db, user, pw
		creds["db_name"], creds["db_user"], creds["db_password"] = db, user, pw
		steps = append(steps, store.Step{Op: "db.create", Params: map[string]string{"name": db, "user": user, "host": "localhost", "password": pw}})
	}
	if app.Admin {
		pw := genPassword(20)
		vars["admin_user"], vars["admin_email"], vars["admin_password"] = req.AdminUser, req.AdminEmail, pw
		creds["admin_url"] = scheme + "://" + req.Domain + app.AdminPath
		creds["admin_user"], creds["admin_password"] = req.AdminUser, pw
	}
	varsJSON, _ := json.Marshal(vars)
	steps = append(steps, store.Step{Op: "app.deploy", Params: map[string]string{
		"domain": req.Domain, "app": app.ID, "name": app.Name, "version": app.Version,
		"steps": catalog.StepsJSON(app.Install), "version_steps": catalog.StepsJSON(app.Ver), "vars": string(varsJSON),
	}})
	if req.TLS != "" {
		steps = append(steps, store.Step{Op: "site.tls", Params: map[string]string{"domain": req.Domain, "mode": req.TLS}})
	}
	// If anything fails, remove what this install created.
	// Step 0 is site.create and step 1 (if any) db.create.
	rollback := []store.Step{{Op: "site.delete", Params: map[string]string{"domain": req.Domain, "delete_files": "true"}, UndoFor: 0}}
	if db := vars["db_name"]; db != "" {
		rollback = append(rollback, store.Step{Op: "db.delete", Params: map[string]string{"name": db, "drop_users": "true"}, UndoFor: 1})
	}
	pb := &store.Playbook{Name: "Install " + app.Name + " on " + req.Domain, Steps: steps, Rollback: rollback}
	runs, err := a.startRuns(r.Context(), whoFrom(r), pb, []string{srv.ID})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	a.audit(r, "", "app.install", srv.Name, map[string]string{"app": app.ID, "domain": req.Domain}, true)
	writeJSON(w, http.StatusAccepted, map[string]any{"runs": runs, "credentials": creds, "notes": app.Notes})
}

func (a *API) handleUpdateApp(w http.ResponseWriter, r *http.Request) {
	var req struct {
		App string `json:"app"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	app, ok := catalog.Get(req.App)
	if !ok || !app.Updatable {
		writeErr(w, http.StatusBadRequest, "this app cannot be updated from the panel")
		return
	}
	domain := r.PathValue("domain")
	pb := &store.Playbook{Name: "Update " + app.Name + " on " + domain, Steps: []store.Step{{Op: "app.update", Params: map[string]string{
		"domain": domain, "steps": catalog.StepsJSON(app.Update), "version_steps": catalog.StepsJSON(app.Ver),
	}}}}
	runs, err := a.startRuns(r.Context(), whoFrom(r), pb, []string{r.PathValue("id")})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, runs)
}

func (a *API) handleUninstallApp(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DB string `json:"db"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	domain := r.PathValue("domain")
	steps := []store.Step{{Op: "site.delete", Params: map[string]string{"domain": domain, "delete_files": "true"}}}
	if req.DB != "" {
		steps = append(steps, store.Step{Op: "db.delete", Params: map[string]string{"name": req.DB, "drop_users": "true"}})
	}
	runs, err := a.startRuns(r.Context(), whoFrom(r), &store.Playbook{Name: "Uninstall app on " + domain, Steps: steps}, []string{r.PathValue("id")})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	a.audit(r, "", "app.uninstall", domain, map[string]string{"db": req.DB}, true)
	writeJSON(w, http.StatusAccepted, runs)
}
