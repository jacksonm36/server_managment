package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/jacksonm36/servermgr/panel/internal/store"
)

// audit records an action. actor defaults to the logged-in admin. Failures
// are logged but never fail the request.
func (a *API) audit(r *http.Request, actor, action, target string, detail any, success bool) {
	who := whoFrom(r)
	if actor != "" {
		who.Actor = actor
	}
	a.auditAs(r.Context(), who, action, target, detail, success)
}

// auditAs records an action outside an HTTP request (scheduler, playbooks).
func (a *API) auditAs(ctx context.Context, who Who, action, target string, detail any, success bool) {
	var raw json.RawMessage
	if detail != nil {
		raw, _ = json.Marshal(detail)
	}
	e := &store.AuditEntry{Actor: who.Actor, IP: who.IP, Action: action, Target: target, Detail: raw, Success: success}
	if err := a.Store.Audit(ctx, e); err != nil {
		a.Log.Error("audit", "action", action, "err", err)
	}
}

func queryInt(r *http.Request, key string, def, max int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil || n <= 0 {
		return def
	}
	return min(n, max)
}

func (a *API) handleListAudit(w http.ResponseWriter, r *http.Request) {
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	list, err := a.Store.ListAudit(r.Context(), queryInt(r, "limit", 100, 500), before, r.URL.Query().Get("action"))
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *API) handleRecentJobs(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.ListRecentJobs(r.Context(), queryInt(r, "limit", 100, 500))
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}
