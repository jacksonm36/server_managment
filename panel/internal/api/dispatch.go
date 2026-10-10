package api

import (
	"context"
	"errors"
	"net/http"

	agentv1 "github.com/jacksonm36/servermgr/gen/agent/v1"
	"github.com/jacksonm36/servermgr/panel/internal/store"
)

// DispatchError carries the HTTP status a failed dispatch maps to.
type DispatchError struct {
	Code int
	Msg  string
}

func (e *DispatchError) Error() string { return e.Msg }

// Who identifies the origin of an action for the audit log.
type Who struct {
	Actor string // admin username, or "schedule: …" / "baseline"
	IP    string
	Role  string // user role, or systemRole for background work
}

// Dispatch validates op/params, records the job and sends it to the agent.
// It is shared by the per-server API, bulk actions, playbooks and schedules.
func (a *API) Dispatch(ctx context.Context, who Who, serverID, op string, params map[string]string) (*store.Job, error) {
	spec, ok := opsByName[op]
	if !ok {
		return nil, &DispatchError{http.StatusBadRequest, "unknown op " + op}
	}
	if err := spec.checkParams(params); err != nil {
		return nil, &DispatchError{http.StatusBadRequest, err.Error()}
	}
	if !roleAllowsOp(who.Role, op) {
		return nil, &DispatchError{http.StatusForbidden, "your role (" + who.Role + ") cannot run " + op}
	}
	srv, err := a.Store.GetServer(ctx, serverID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, &DispatchError{http.StatusNotFound, "server not found"}
	}
	if err != nil {
		return nil, err
	}
	if !a.Hub.Online(serverID) {
		return nil, &DispatchError{http.StatusConflict, srv.Name + " is offline"}
	}
	if params == nil {
		params = map[string]string{}
	}
	stored := redact(params)
	job := &store.Job{ID: store.NewUUID(), ServerID: serverID, Op: op, Params: stored, Status: "queued", CreatedBy: who.Actor}
	if err := a.Store.CreateJob(ctx, job); err != nil {
		return nil, err
	}
	msg := &agentv1.PanelMessage{Msg: &agentv1.PanelMessage_Job{Job: &agentv1.Job{Id: job.ID, Op: spec.op, Params: params}}}
	if err := a.Hub.Send(serverID, msg); err != nil {
		_ = a.Store.UpdateJob(ctx, job.ID, "failed", "", nil, err.Error(), true)
		a.auditAs(ctx, who, "job.create", srv.Name, map[string]any{"op": op, "params": stored, "job_id": job.ID, "error": err.Error()}, false)
		return nil, &DispatchError{http.StatusConflict, err.Error()}
	}
	if !readOnlyOps[op] {
		a.auditAs(ctx, who, "job.create", srv.Name, map[string]any{"op": op, "params": stored, "job_id": job.ID}, true)
	}
	a.Hub.JobChanged(serverID, job.ID)
	return job, nil
}

func (a *API) writeDispatchErr(w http.ResponseWriter, err error) {
	var de *DispatchError
	if errors.As(err, &de) {
		writeErr(w, de.Code, de.Msg)
		return
	}
	a.internal(w, err)
}

func whoFrom(r *http.Request) Who {
	w := Who{IP: clientIP(r)}
	if ad := adminFrom(r.Context()); ad != nil {
		w.Actor, w.Role = ad.Username, ad.Role
	}
	return w
}

func (a *API) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Op     string            `json:"op"`
		Params map[string]string `json:"params"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	job, err := a.Dispatch(r.Context(), whoFrom(r), r.PathValue("id"), req.Op, req.Params)
	if err != nil {
		a.writeDispatchErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

// bulkMax bounds one bulk request; it is a guard against mistakes, not load.
const bulkMax = 500

type bulkResult struct {
	ServerID string `json:"server_id"`
	JobID    string `json:"job_id,omitempty"`
	Error    string `json:"error,omitempty"`
}

func (a *API) handleBulk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServerIDs []string          `json:"server_ids"`
		Op        string            `json:"op"`
		Params    map[string]string `json:"params"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.ServerIDs) == 0 || len(req.ServerIDs) > bulkMax {
		writeErr(w, http.StatusBadRequest, "select between 1 and 500 servers")
		return
	}
	if _, ok := opsByName[req.Op]; !ok {
		writeErr(w, http.StatusBadRequest, "unknown op")
		return
	}
	who := whoFrom(r)
	out := make([]bulkResult, 0, len(req.ServerIDs))
	seen := map[string]bool{}
	for _, id := range req.ServerIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		res := bulkResult{ServerID: id}
		if job, err := a.Dispatch(r.Context(), who, id, req.Op, req.Params); err != nil {
			res.Error = err.Error()
		} else {
			res.JobID = job.ID
		}
		out = append(out, res)
	}
	a.auditAs(r.Context(), who, "bulk.run", "", map[string]any{"op": req.Op, "servers": len(out), "params": redact(req.Params)}, true)
	writeJSON(w, http.StatusAccepted, out)
}
