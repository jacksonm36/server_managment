package api

import (
	"errors"
	"net/http"

	agentv1 "github.com/jacksonm36/servermgr/gen/agent/v1"
	"github.com/jacksonm36/servermgr/panel/internal/store"
)

// handleListSessions lists the signed-in user's own sessions.
func (a *API) handleListSessions(w http.ResponseWriter, r *http.Request) {
	ad := adminFrom(r.Context())
	var cur []byte
	if c, err := r.Cookie(sessionCookie); err == nil {
		cur = hashToken(c.Value)
	}
	list, err := a.Store.ListSessions(r.Context(), ad.ID, cur)
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": list, "idle_minutes": int(store.SessionIdle.Minutes())})
}

// handleEndSession signs out one of the user's own sessions.
func (a *API) handleEndSession(w http.ResponseWriter, r *http.Request) {
	ad := adminFrom(r.Context())
	err := a.Store.DeleteSessionByID(r.Context(), ad.ID, r.PathValue("sid"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "session not found")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	a.audit(r, "", "auth.session_end", r.PathValue("sid"), nil, true)
	w.WriteHeader(http.StatusNoContent)
}

// handleCancelJob asks the agent to stop a running job. Whoever may run the
// op on that server may cancel it.
func (a *API) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	j, err := a.Store.GetJob(r.Context(), r.PathValue("id"))
	if err != nil || !accessFrom(r.Context()).CanOn("server.view", j.ServerID) {
		writeErr(w, http.StatusNotFound, "job not found")
		return
	}
	if !accessFrom(r.Context()).CanOn(opPerm(j.Op), j.ServerID) {
		writeErr(w, http.StatusForbidden, "you may not run this job, so you can't cancel it")
		return
	}
	if j.Status != "running" && j.Status != "queued" {
		writeErr(w, http.StatusConflict, "the job has already finished")
		return
	}
	if err := a.Hub.Send(j.ServerID, &agentv1.PanelMessage{Msg: &agentv1.PanelMessage_JobCancel{JobCancel: &agentv1.JobCancel{JobId: j.ID}}}); err != nil {
		writeErr(w, http.StatusConflict, "the server is offline: "+err.Error())
		return
	}
	a.audit(r, "", "job.cancel", j.Op, map[string]string{"job": j.ID, "server": j.ServerID}, true)
	w.WriteHeader(http.StatusAccepted)
}

func (a *API) handleGetRetention(w http.ResponseWriter, r *http.Request) {
	ret, err := a.Store.GetRetention(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ret)
}

func (a *API) handleSetRetention(w http.ResponseWriter, r *http.Request) {
	var req store.Retention
	if !readJSON(w, r, &req) {
		return
	}
	for _, d := range []int{req.JobsDays, req.AuditDays, req.AlertsDays, req.RunsDays} {
		if d < 0 || d > 3650 {
			writeErr(w, http.StatusBadRequest, "days must be 0 (keep forever) to 3650")
			return
		}
	}
	if err := a.Store.SetRetention(r.Context(), req); err != nil {
		a.internal(w, err)
		return
	}
	a.audit(r, "", "settings.retention", "", req, true)
	writeJSON(w, http.StatusOK, req)
}
