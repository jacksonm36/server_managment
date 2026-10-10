package api

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/jacksonm36/servermgr/panel/internal/store"
)

// Roles, lowest to highest. admin: everything. operator: run anything on
// servers (incl. terminal), manage playbooks/schedules/alerts. viewer:
// read-only, plus the read-only status operations the UI needs.
var roleRank = map[string]int{"viewer": 1, "operator": 2, "admin": 3}

// systemRole is used by the scheduler, baselines and alerts.
const systemRole = "system"

// need wraps a handler with a minimum role.
func need(min string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ad := adminFrom(r.Context())
		if ad == nil || roleRank[ad.Role] < roleRank[min] {
			role := "unknown"
			if ad != nil {
				role = ad.Role
			}
			writeErr(w, http.StatusForbidden, "your role ("+role+") is not allowed to do this")
			return
		}
		h(w, r)
	}
}

// viewerOp reports whether a viewer may run op: read-only status checks,
// but not reading file contents, which may hold secrets.
func viewerOp(op string) bool {
	return readOnlyOps[op] && op != "files.read" && op != "compose.read"
}

func roleAllowsOp(role, op string) bool {
	switch role {
	case systemRole, "admin", "operator":
		return true
	case "viewer":
		return viewerOp(op)
	}
	return false
}

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{1,31}$`)

func (a *API) handleListUsers(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.ListUsers(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *API) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if !usernameRe.MatchString(req.Username) {
		writeErr(w, http.StatusBadRequest, "username: 2-32 letters, digits, . _ -")
		return
	}
	if _, ok := roleRank[req.Role]; !ok {
		writeErr(w, http.StatusBadRequest, "role must be admin, operator or viewer")
		return
	}
	if len(req.Password) < 12 || len(req.Password) > 256 {
		writeErr(w, http.StatusBadRequest, "password must be 12-256 characters")
		return
	}
	hash, err := HashPassword(req.Password)
	if err != nil {
		a.internal(w, err)
		return
	}
	id, err := a.Store.CreateUser(r.Context(), req.Username, hash, req.Role)
	if errors.Is(err, store.ErrExists) {
		writeErr(w, http.StatusConflict, "that username is taken")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	a.audit(r, "", "user.create", req.Username, map[string]string{"role": req.Role}, true)
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func userID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil
}

func (a *API) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := userID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	var req struct {
		Role     string `json:"role"`
		Disabled bool   `json:"disabled"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if _, ok := roleRank[req.Role]; !ok {
		writeErr(w, http.StatusBadRequest, "role must be admin, operator or viewer")
		return
	}
	if me := adminFrom(r.Context()); me.ID == id && (req.Disabled || req.Role != "admin") {
		writeErr(w, http.StatusBadRequest, "you cannot demote or disable yourself")
		return
	}
	err := a.Store.UpdateUser(r.Context(), id, req.Role, req.Disabled)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "user not found")
		return
	case errors.Is(err, store.ErrLastAdmin):
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		a.internal(w, err)
		return
	}
	a.audit(r, "", "user.update", strconv.FormatInt(id, 10), map[string]any{"role": req.Role, "disabled": req.Disabled}, true)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleResetUserPassword(w http.ResponseWriter, r *http.Request) {
	id, ok := userID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	var req struct {
		Password string `json:"password"`
		Reset2FA bool   `json:"reset_2fa"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Password) < 12 || len(req.Password) > 256 {
		writeErr(w, http.StatusBadRequest, "password must be 12-256 characters")
		return
	}
	hash, err := HashPassword(req.Password)
	if err != nil {
		a.internal(w, err)
		return
	}
	if err := a.Store.ResetPassword(r.Context(), id, hash, req.Reset2FA); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "user not found")
			return
		}
		a.internal(w, err)
		return
	}
	a.audit(r, "", "user.reset_password", strconv.FormatInt(id, 10), map[string]bool{"reset_2fa": req.Reset2FA}, true)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := userID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if adminFrom(r.Context()).ID == id {
		writeErr(w, http.StatusBadRequest, "you cannot delete yourself")
		return
	}
	err := a.Store.DeleteUser(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "user not found")
		return
	case errors.Is(err, store.ErrLastAdmin):
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		a.internal(w, err)
		return
	}
	a.audit(r, "", "user.delete", strconv.FormatInt(id, 10), nil, true)
	w.WriteHeader(http.StatusNoContent)
}
