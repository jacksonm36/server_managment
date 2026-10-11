package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jacksonm36/servermgr/panel/internal/store"
)

const (
	maxSteps       = 50
	stepTimeout    = 30 * time.Minute
	jobPollInitial = 500 * time.Millisecond
)

// validateSteps checks every step against the op allowlist. Secret
// parameters are refused: playbooks are stored in plain text.
func validateSteps(steps []store.Step) error {
	if len(steps) == 0 || len(steps) > maxSteps {
		return fmt.Errorf("a playbook needs 1-%d steps", maxSteps)
	}
	for i, st := range steps {
		spec, ok := opsByName[st.Op]
		if !ok {
			return fmt.Errorf("step %d: unknown op %q", i+1, st.Op)
		}
		if err := spec.checkParams(st.Params); err != nil {
			return fmt.Errorf("step %d: %v", i+1, err)
		}
		for k := range st.Params {
			if slices.Contains(secretParams, k) {
				return fmt.Errorf("step %d: %q cannot be stored in a playbook", i+1, k)
			}
		}
	}
	return nil
}

func (a *API) baseCtx() context.Context {
	if a.Ctx != nil {
		return a.Ctx
	}
	return context.Background()
}

// waitJob polls a job until it finishes or the timeout passes.
func (a *API) waitJob(ctx context.Context, id string, timeout time.Duration) (*store.Job, error) {
	deadline := time.Now().Add(timeout)
	delay := jobPollInitial
	for {
		j, err := a.Store.GetJob(ctx, id)
		if err != nil {
			return nil, err
		}
		if j.Status == "succeeded" || j.Status == "failed" {
			return j, nil
		}
		if time.Now().After(deadline) {
			return j, errors.New("timed out waiting for the agent")
		}
		select {
		case <-ctx.Done():
			return j, ctx.Err()
		case <-time.After(delay):
		}
		delay = min(delay*2, 3*time.Second)
	}
}

// StartPlaybook creates one run per server and executes them concurrently;
// steps within a run are sequential.
func (a *API) StartPlaybook(ctx context.Context, who Who, pb *store.Playbook, serverIDs []string) ([]*store.PlaybookRun, error) {
	if err := validateSteps(pb.Steps); err != nil {
		return nil, err
	}
	return a.startRuns(ctx, who, pb, serverIDs)
}

// startRuns starts pb on each server. Unlike StartPlaybook it accepts
// secret parameters (used by app installs): run history stores them
// redacted, while execution uses the real values held in memory only.
func (a *API) startRuns(ctx context.Context, who Who, pb *store.Playbook, serverIDs []string) ([]*store.PlaybookRun, error) {
	for i, st := range pb.Steps {
		spec, ok := opsByName[st.Op]
		if !ok {
			return nil, fmt.Errorf("step %d: unknown op %q", i+1, st.Op)
		}
		if err := spec.checkParams(st.Params); err != nil {
			return nil, fmt.Errorf("step %d: %v", i+1, err)
		}
	}
	runs := []*store.PlaybookRun{}
	for _, sid := range serverIDs {
		if _, err := a.Store.GetServer(ctx, sid); err != nil {
			continue
		}
		run := &store.PlaybookRun{ID: store.NewUUID(), PlaybookName: pb.Name, ServerID: sid, Status: "running", CreatedBy: who.Actor}
		if pb.ID != "" {
			id := pb.ID
			run.PlaybookID = &id
		}
		for _, st := range pb.Steps {
			shown := store.Step{Op: st.Op, Params: redact(st.Params), ContinueOnError: st.ContinueOnError}
			run.Steps = append(run.Steps, store.RunStep{Step: shown, Status: "pending"})
		}
		if err := a.Store.CreateRun(ctx, run); err != nil {
			return nil, err
		}
		runs = append(runs, run)
		go a.executeRun(who, run, pb.Steps, pb.Rollback)
	}
	a.auditAs(ctx, who, "playbook.run", pb.Name, map[string]any{"servers": len(runs)}, true)
	return runs, nil
}

func (a *API) executeRun(who Who, run *store.PlaybookRun, real, rollback []store.Step) {
	ctx := a.baseCtx()
	failed := false
	for i := range run.Steps {
		st := &run.Steps[i]
		if failed {
			st.Status = "skipped"
			continue
		}
		st.Status = "running"
		_ = a.Store.UpdateRun(ctx, run)
		job, err := a.Dispatch(ctx, who, run.ServerID, real[i].Op, real[i].Params)
		if err == nil {
			st.JobID = job.ID
			_ = a.Store.UpdateRun(ctx, run)
			job, err = a.waitJob(ctx, job.ID, stepTimeout)
			if err == nil && job.Status == "failed" {
				err = errors.New(job.Error)
			}
		}
		if err != nil {
			st.Status, st.Error = "failed", err.Error()
			if !st.ContinueOnError {
				failed = true
			}
		} else {
			st.Status = "succeeded"
		}
	}
	failed = false
	for _, st := range run.Steps {
		if st.Status == "failed" {
			failed = true
		}
	}
	// Undo a failed run (the run stays "running" until this finishes); each
	// rollback step runs even if an earlier one fails.
	if failed && len(rollback) > 0 {
		for _, rb := range rollback {
			if rb.UndoFor < 0 || rb.UndoFor >= len(real) || run.Steps[rb.UndoFor].Status != "succeeded" {
				continue // never undo something this run did not create
			}
			st := store.RunStep{Step: store.Step{Op: rb.Op, Params: redact(rb.Params)}, Status: "running", Rollback: true}
			run.Steps = append(run.Steps, st)
			i := len(run.Steps) - 1
			_ = a.Store.UpdateRun(ctx, run)
			job, err := a.Dispatch(ctx, who, run.ServerID, rb.Op, rb.Params)
			if err == nil {
				run.Steps[i].JobID = job.ID
				job, err = a.waitJob(ctx, job.ID, stepTimeout)
				if err == nil && job.Status == "failed" {
					err = errors.New(job.Error)
				}
			}
			run.Steps[i].Status = "succeeded"
			if err != nil {
				run.Steps[i].Status, run.Steps[i].Error = "failed", err.Error()
			}
		}
	}
	run.Status = "succeeded"
	if failed {
		run.Status = "failed"
	}
	if err := a.Store.UpdateRun(ctx, run); err != nil {
		a.Log.Error("update playbook run", "run", run.ID, "err", err)
	}
	a.Hub.JobChanged(run.ServerID, "")
}

// ---- handlers ----

func (a *API) handleListPlaybooks(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.ListPlaybooks(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *API) handleSavePlaybook(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string       `json:"name"`
		Description string       `json:"description"`
		Steps       []store.Step `json:"steps"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 100 || len(req.Description) > 1000 {
		writeErr(w, http.StatusBadRequest, "name is required (max 100 characters)")
		return
	}
	if err := validateSteps(req.Steps); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	pb := &store.Playbook{ID: r.PathValue("id"), Name: req.Name, Description: req.Description, Steps: req.Steps}
	if pb.ID != "" {
		cur, err := a.Store.GetPlaybook(r.Context(), pb.ID)
		if err != nil {
			writeErr(w, http.StatusNotFound, "playbook not found")
			return
		}
		if cur.Builtin {
			writeErr(w, http.StatusForbidden, "built-in playbooks cannot be changed; duplicate it instead")
			return
		}
	}
	if err := a.Store.SavePlaybook(r.Context(), pb); err != nil {
		a.internal(w, err)
		return
	}
	a.audit(r, "", "playbook.save", pb.Name, map[string]any{"steps": len(pb.Steps)}, true)
	writeJSON(w, http.StatusOK, pb)
}

func (a *API) handleDeletePlaybook(w http.ResponseWriter, r *http.Request) {
	pb, err := a.Store.GetPlaybook(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "playbook not found")
		return
	}
	if pb.Builtin {
		writeErr(w, http.StatusForbidden, "built-in playbooks cannot be deleted")
		return
	}
	if err := a.Store.DeletePlaybook(r.Context(), pb.ID); err != nil {
		a.internal(w, err)
		return
	}
	a.audit(r, "", "playbook.delete", pb.Name, nil, true)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleRunPlaybook(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServerIDs []string `json:"server_ids"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.ServerIDs) == 0 || len(req.ServerIDs) > bulkMax {
		writeErr(w, http.StatusBadRequest, "select between 1 and 500 servers")
		return
	}
	pb, err := a.Store.GetPlaybook(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "playbook not found")
		return
	}
	runs, err := a.StartPlaybook(r.Context(), whoFrom(r), pb, req.ServerIDs)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, runs)
}

func (a *API) handleListRuns(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.ListRuns(r.Context(), queryInt(r, "limit", 50, 200))
	if err != nil {
		a.internal(w, err)
		return
	}
	ac := accessFrom(r.Context())
	list = slices.DeleteFunc(list, func(run *store.PlaybookRun) bool { return !ac.CanOn("server.view", run.ServerID) })
	writeJSON(w, http.StatusOK, list)
}
