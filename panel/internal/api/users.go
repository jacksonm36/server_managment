package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jacksonm36/servermgr/panel/internal/store"
)

var (
	usernameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{1,31}$`)
	// Role and group names show up in the UI and audit log.
	groupNameRe = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N} ._-]{0,47}$`)
	colorRe     = regexp.MustCompile(`^(#[0-9a-fA-F]{6})?$`)
)

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil
}

// storeErr maps the store's sentinel errors to HTTP responses.
func (a *API) storeErr(w http.ResponseWriter, err error, what string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, what+" not found")
	case errors.Is(err, store.ErrExists):
		writeErr(w, http.StatusConflict, "that name is taken")
	case errors.Is(err, store.ErrLastAdmin):
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		a.internal(w, err)
	}
}

func (a *API) handleListPermissions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"permissions": permissions, "builtin": builtinPerms})
}

// ---- users ----

type userReq struct {
	Username           string  `json:"username"`
	DisplayName        string  `json:"display_name"`
	Email              string  `json:"email"`
	Password           string  `json:"password"`
	Role               string  `json:"role"`
	Disabled           bool    `json:"disabled"`
	MustChangePassword bool    `json:"must_change_password"`
	Groups             []int64 `json:"groups"`
	// TenantID puts the user in a tenant (null: a platform user). Left
	// out of an update, the user stays where they are.
	TenantID json.RawMessage `json:"tenant_id"`
}

// tenant parses TenantID: keep reports that it was left out.
func (req *userReq) tenant() (id *int64, keep bool, err error) {
	if len(req.TenantID) == 0 {
		return nil, true, nil
	}
	if string(req.TenantID) == "null" {
		return nil, false, nil
	}
	var n int64
	if err := json.Unmarshal(req.TenantID, &n); err != nil || n <= 0 {
		return nil, false, errors.New("tenant_id must be a tenant's id or null")
	}
	return &n, false, nil
}

func (a *API) checkUserReq(w http.ResponseWriter, r *http.Request, req *userReq) bool {
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	req.Email = strings.TrimSpace(req.Email)
	if len(req.DisplayName) > 100 {
		writeErr(w, http.StatusBadRequest, "display name is too long (max 100)")
		return false
	}
	if req.Email != "" {
		if addr, err := mail.ParseAddress(req.Email); err != nil || addr.Address != req.Email || len(req.Email) > 254 {
			writeErr(w, http.StatusBadRequest, "email address is not valid")
			return false
		}
	}
	ok, err := a.Store.RoleExists(r.Context(), req.Role)
	if err != nil {
		a.internal(w, err)
		return false
	}
	if !ok {
		writeErr(w, http.StatusBadRequest, "unknown role "+strconv.Quote(req.Role))
		return false
	}
	return true
}

func validPassword(w http.ResponseWriter, pw string) bool {
	if len(pw) < 12 || len(pw) > 256 {
		writeErr(w, http.StatusBadRequest, "password must be 12-256 characters")
		return false
	}
	return true
}

func (a *API) handleListUsers(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.ListUsers(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *API) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req userReq
	if !readJSON(w, r, &req) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if !usernameRe.MatchString(req.Username) {
		writeErr(w, http.StatusBadRequest, "username: 2-32 letters, digits, . _ -")
		return
	}
	if !a.checkUserReq(w, r, &req) || !validPassword(w, req.Password) {
		return
	}
	hash, err := HashPassword(req.Password)
	if err != nil {
		a.internal(w, err)
		return
	}
	tenant, _, err := req.tenant()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	u := &store.User{
		Username: req.Username, DisplayName: req.DisplayName, Email: req.Email, Role: req.Role,
		MustChangePassword: req.MustChangePassword, Groups: orEmpty(req.Groups), TenantID: tenant,
	}
	if err := a.Store.CreateUser(r.Context(), u, hash); err != nil {
		if errors.Is(err, store.ErrExists) {
			writeErr(w, http.StatusConflict, "that username is taken")
			return
		}
		a.storeErr(w, err, "user group")
		return
	}
	a.audit(r, "", "user.create", req.Username, map[string]any{"role": req.Role, "groups": u.Groups, "tenant_id": u.TenantID}, true)
	writeJSON(w, http.StatusCreated, map[string]any{"id": u.ID})
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func (a *API) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	var req userReq
	if !readJSON(w, r, &req) || !a.checkUserReq(w, r, &req) {
		return
	}
	if me := adminFrom(r.Context()); me.ID == id && (req.Disabled || (me.Role == roleAdmin && req.Role != roleAdmin)) {
		writeErr(w, http.StatusBadRequest, "you cannot demote or disable yourself")
		return
	}
	tenant, keep, err := req.tenant()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if me := adminFrom(r.Context()); me.ID == id && tenant != nil {
		writeErr(w, http.StatusBadRequest, "you cannot move yourself into a tenant")
		return
	}
	u := &store.User{ID: id, DisplayName: req.DisplayName, Email: req.Email, Role: req.Role, Disabled: req.Disabled, Groups: req.Groups, TenantID: tenant}
	if err := a.Store.UpdateUser(r.Context(), u, keep); err != nil {
		a.storeErr(w, err, "user, group or tenant")
		return
	}
	detail := map[string]any{"role": req.Role, "disabled": req.Disabled, "groups": req.Groups}
	if !keep {
		detail["tenant_id"] = tenant
	}
	a.audit(r, "", "user.update", strconv.FormatInt(id, 10), detail, true)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleResetUserPassword(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	var req struct {
		Password   string `json:"password"`
		Reset2FA   bool   `json:"reset_2fa"`
		MustChange bool   `json:"must_change"`
	}
	if !readJSON(w, r, &req) || !validPassword(w, req.Password) {
		return
	}
	hash, err := HashPassword(req.Password)
	if err != nil {
		a.internal(w, err)
		return
	}
	if err := a.Store.ResetPassword(r.Context(), id, hash, req.Reset2FA, req.MustChange); err != nil {
		a.storeErr(w, err, "user")
		return
	}
	a.audit(r, "", "user.reset_password", strconv.FormatInt(id, 10), map[string]bool{"reset_2fa": req.Reset2FA, "must_change": req.MustChange}, true)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleEndUserSessions(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	n, err := a.Store.EndSessions(r.Context(), id)
	if err != nil {
		a.internal(w, err)
		return
	}
	a.audit(r, "", "user.sign_out", strconv.FormatInt(id, 10), map[string]int64{"sessions": n}, true)
	writeJSON(w, http.StatusOK, map[string]int64{"ended": n})
}

func (a *API) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if adminFrom(r.Context()).ID == id {
		writeErr(w, http.StatusBadRequest, "you cannot delete yourself")
		return
	}
	if err := a.Store.DeleteUser(r.Context(), id); err != nil {
		a.storeErr(w, err, "user")
		return
	}
	a.audit(r, "", "user.delete", strconv.FormatInt(id, 10), nil, true)
	w.WriteHeader(http.StatusNoContent)
}

// ---- roles ----

func (a *API) handleListRoles(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.ListRoles(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, ro := range list {
		if ro.Builtin {
			ro.Permissions = builtinPerms[ro.Name]
		}
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *API) handleSaveRole(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Permissions []string `json:"permissions"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	ro := &store.Role{Name: strings.TrimSpace(req.Name), Description: strings.TrimSpace(req.Description)}
	if r.PathValue("id") != "" {
		var ok bool
		if ro.ID, ok = pathID(r); !ok {
			writeErr(w, http.StatusBadRequest, "bad id")
			return
		}
	}
	if !groupNameRe.MatchString(ro.Name) {
		writeErr(w, http.StatusBadRequest, "name: 1-48 letters, digits, spaces, . _ -")
		return
	}
	if _, builtin := builtinPerms[strings.ToLower(ro.Name)]; builtin {
		writeErr(w, http.StatusBadRequest, "that name belongs to a built-in role")
		return
	}
	if len(ro.Description) > 300 {
		writeErr(w, http.StatusBadRequest, "description is too long (max 300)")
		return
	}
	ro.Permissions = []string{}
	for _, p := range req.Permissions {
		if !permKnown(p) {
			writeErr(w, http.StatusBadRequest, "unknown permission "+strconv.Quote(p))
			return
		}
		if !slices.Contains(ro.Permissions, p) {
			ro.Permissions = append(ro.Permissions, p)
		}
	}
	if err := a.Store.SaveRole(r.Context(), ro); err != nil {
		a.storeErr(w, err, "custom role")
		return
	}
	a.audit(r, "", "role.save", ro.Name, map[string]any{"permissions": ro.Permissions}, true)
	writeJSON(w, http.StatusOK, ro)
}

func (a *API) handleDeleteRole(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	ro, err := a.Store.GetRole(r.Context(), id)
	if err != nil {
		a.storeErr(w, err, "role")
		return
	}
	if ro.Builtin {
		writeErr(w, http.StatusBadRequest, "built-in roles cannot be deleted")
		return
	}
	if err := a.Store.DeleteRole(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrInUse) {
			writeErr(w, http.StatusConflict, "this role is still in use (a user's main role or a grant); remove those first")
			return
		}
		a.storeErr(w, err, "role")
		return
	}
	a.audit(r, "", "role.delete", ro.Name, nil, true)
	w.WriteHeader(http.StatusNoContent)
}

// ---- user groups ----

func (a *API) handleListUserGroups(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.ListUserGroups(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *API) handleSaveUserGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string  `json:"name"`
		Description string  `json:"description"`
		Members     []int64 `json:"members"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	g := &store.UserGroup{Name: strings.TrimSpace(req.Name), Description: strings.TrimSpace(req.Description), Members: orEmpty(req.Members)}
	if r.PathValue("id") != "" {
		var ok bool
		if g.ID, ok = pathID(r); !ok {
			writeErr(w, http.StatusBadRequest, "bad id")
			return
		}
	}
	if !groupNameRe.MatchString(g.Name) || len(g.Description) > 300 {
		writeErr(w, http.StatusBadRequest, "name: 1-48 letters, digits, spaces, . _ - (description max 300)")
		return
	}
	if err := a.Store.SaveUserGroup(r.Context(), g); err != nil {
		a.storeErr(w, err, "group or user")
		return
	}
	a.audit(r, "", "user_group.save", g.Name, map[string]any{"members": g.Members}, true)
	writeJSON(w, http.StatusOK, g)
}

func (a *API) handleDeleteUserGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := a.Store.DeleteUserGroup(r.Context(), id); err != nil {
		a.storeErr(w, err, "group")
		return
	}
	a.audit(r, "", "user_group.delete", strconv.FormatInt(id, 10), nil, true)
	w.WriteHeader(http.StatusNoContent)
}

// ---- server groups ----

// handleListServerGroups lists groups with only the servers the caller can
// see, so groups can be used to filter and pick servers.
func (a *API) handleListServerGroups(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.ListServerGroups(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	ac := accessFrom(r.Context())
	if ac.Can("users.manage") {
		writeJSON(w, http.StatusOK, list)
		return
	}
	out := []*store.ServerGroup{}
	for _, g := range list {
		g.ServerIDs = slices.DeleteFunc(g.ServerIDs, func(id string) bool { return !ac.CanOn("server.view", id) })
		if len(g.ServerIDs) > 0 || ac.CanOnGroup("server.view", g.ID) {
			out = append(out, g)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleSaveServerGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Color       string   `json:"color"`
		ServerIDs   []string `json:"server_ids"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	g := &store.ServerGroup{Name: strings.TrimSpace(req.Name), Description: strings.TrimSpace(req.Description), Color: req.Color, ServerIDs: orEmpty(req.ServerIDs)}
	if r.PathValue("id") != "" {
		var ok bool
		if g.ID, ok = pathID(r); !ok {
			writeErr(w, http.StatusBadRequest, "bad id")
			return
		}
	}
	if !groupNameRe.MatchString(g.Name) || len(g.Description) > 300 || !colorRe.MatchString(g.Color) {
		writeErr(w, http.StatusBadRequest, "name: 1-48 letters, digits, spaces, . _ - (description max 300, color #rrggbb)")
		return
	}
	if err := a.Store.SaveServerGroup(r.Context(), g); err != nil {
		a.storeErr(w, err, "server group")
		return
	}
	a.audit(r, "", "server_group.save", g.Name, map[string]any{"servers": len(g.ServerIDs)}, true)
	writeJSON(w, http.StatusOK, g)
}

func (a *API) handleDeleteServerGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := a.Store.DeleteServerGroup(r.Context(), id); err != nil {
		a.storeErr(w, err, "server group")
		return
	}
	a.audit(r, "", "server_group.delete", strconv.FormatInt(id, 10), nil, true)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleSetServerGroups(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Groups []int64 `json:"groups"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	if _, err := a.Store.GetServer(r.Context(), id); err != nil {
		a.storeErr(w, err, "server")
		return
	}
	if err := a.Store.SetServerGroups(r.Context(), id, orEmpty(req.Groups)); err != nil {
		a.storeErr(w, err, "server group")
		return
	}
	a.audit(r, "", "server.groups", id, map[string]any{"groups": req.Groups}, true)
	w.WriteHeader(http.StatusNoContent)
}

// ---- grants ----

func (a *API) handleListGrants(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.ListGrants(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *API) handleCreateGrant(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID        *int64 `json:"user_id"`
		UserGroupID   *int64 `json:"user_group_id"`
		RoleID        int64  `json:"role_id"`
		ServerGroupID *int64 `json:"server_group_id"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if (req.UserID == nil) == (req.UserGroupID == nil) {
		writeErr(w, http.StatusBadRequest, "grant the role to either a user or a user group")
		return
	}
	g := &store.RoleGrant{AdminID: req.UserID, UserGroupID: req.UserGroupID, RoleID: req.RoleID, ServerGroupID: req.ServerGroupID}
	if err := a.Store.CreateGrant(r.Context(), g); err != nil {
		if errors.Is(err, store.ErrExists) {
			writeErr(w, http.StatusConflict, "that grant already exists")
			return
		}
		a.storeErr(w, err, "user, group or role")
		return
	}
	a.audit(r, "", "grant.create", strconv.FormatInt(g.ID, 10),
		map[string]any{"user_id": req.UserID, "user_group_id": req.UserGroupID, "role_id": req.RoleID, "server_group_id": req.ServerGroupID}, true)
	writeJSON(w, http.StatusCreated, g)
}

func (a *API) handleDeleteGrant(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := a.Store.DeleteGrant(r.Context(), id); err != nil {
		a.storeErr(w, err, "grant")
		return
	}
	a.audit(r, "", "grant.delete", strconv.FormatInt(id, 10), nil, true)
	w.WriteHeader(http.StatusNoContent)
}
