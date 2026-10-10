package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jacksonm36/servermgr/panel/internal/cronexpr"
	"github.com/jacksonm36/servermgr/panel/internal/store"
)

// RunScheduler fires due schedules at the top of every minute until ctx ends.
func (a *API) RunScheduler(ctx context.Context) {
	for {
		now := time.Now()
		next := now.Truncate(time.Minute).Add(time.Minute)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next) + 200*time.Millisecond):
		}
		a.fireDue(ctx, next)
	}
}

func (a *API) fireDue(ctx context.Context, minute time.Time) {
	list, err := a.Store.ListSchedules(ctx)
	if err != nil {
		a.Log.Error("scheduler: list", "err", err)
		return
	}
	for _, sc := range list {
		if !sc.Enabled {
			continue
		}
		e, err := cronexpr.Parse(sc.Cron)
		if err != nil || !e.Matches(minute) {
			continue
		}
		ok, err := a.Store.ClaimScheduleRun(ctx, sc.ID, minute)
		if err != nil || !ok {
			continue
		}
		go a.runSchedule(ctx, sc)
	}
}

// targets resolves which servers a schedule applies to right now.
func (a *API) targets(ctx context.Context, sc *store.Schedule) ([]string, error) {
	if !sc.AllServers {
		return sc.ServerIDs, nil
	}
	list, err := a.Store.ListServers(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(list))
	for _, s := range list {
		ids = append(ids, s.ID)
	}
	return ids, nil
}

// runSchedule executes a schedule once and records a one-line summary.
func (a *API) runSchedule(ctx context.Context, sc *store.Schedule) string {
	who := Who{Actor: "schedule: " + sc.Name, IP: "panel", Role: systemRole}
	ids, err := a.targets(ctx, sc)
	var result string
	switch {
	case err != nil:
		result = "error: " + err.Error()
	case sc.Kind == "playbook" && sc.PlaybookID != nil:
		pb, perr := a.Store.GetPlaybook(ctx, *sc.PlaybookID)
		if perr != nil {
			result = "error: playbook not found"
			break
		}
		online := []string{}
		for _, id := range ids {
			if a.Hub.Online(id) {
				online = append(online, id)
			}
		}
		runs, rerr := a.StartPlaybook(ctx, who, pb, online)
		if rerr != nil {
			result = "error: " + rerr.Error()
		} else {
			result = fmt.Sprintf("started %q on %d server(s)", pb.Name, len(runs))
			if skipped := len(ids) - len(online); skipped > 0 {
				result += fmt.Sprintf(", %d offline skipped", skipped)
			}
		}
	default:
		started, failed := 0, []string{}
		for _, id := range ids {
			if _, derr := a.Dispatch(ctx, who, id, sc.Op, sc.Params); derr != nil {
				failed = append(failed, derr.Error())
			} else {
				started++
			}
		}
		result = fmt.Sprintf("started on %d server(s)", started)
		if len(failed) > 0 {
			result += fmt.Sprintf(", %d skipped (%s)", len(failed), strings.Join(failed[:min(len(failed), 3)], "; "))
		}
	}
	if err := a.Store.SetScheduleResult(ctx, sc.ID, result); err != nil {
		a.Log.Error("scheduler: save result", "err", err)
	}
	a.Log.Info("schedule ran", "schedule", sc.Name, "result", result)
	return result
}

// ---- handlers ----

type scheduleView struct {
	*store.Schedule
	NextRun *time.Time `json:"next_run"`
}

func viewSchedule(sc *store.Schedule) scheduleView {
	v := scheduleView{Schedule: sc}
	if e, err := cronexpr.Parse(sc.Cron); err == nil && sc.Enabled {
		if n := e.Next(time.Now()); !n.IsZero() {
			v.NextRun = &n
		}
	}
	return v
}

func (a *API) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.ListSchedules(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	out := make([]scheduleView, 0, len(list))
	for _, sc := range list {
		out = append(out, viewSchedule(sc))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleSaveSchedule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string            `json:"name"`
		Cron       string            `json:"cron"`
		Kind       string            `json:"kind"`
		Op         string            `json:"op"`
		Params     map[string]string `json:"params"`
		PlaybookID string            `json:"playbook_id"`
		ServerIDs  []string          `json:"server_ids"`
		AllServers bool              `json:"all_servers"`
		Enabled    bool              `json:"enabled"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	sc := &store.Schedule{
		ID: r.PathValue("id"), Name: strings.TrimSpace(req.Name), Cron: strings.Join(strings.Fields(req.Cron), " "),
		Kind: req.Kind, ServerIDs: req.ServerIDs, AllServers: req.AllServers, Enabled: req.Enabled,
		CreatedBy: whoFrom(r).Actor, Params: map[string]string{},
	}
	if sc.Name == "" || len(sc.Name) > 100 {
		writeErr(w, http.StatusBadRequest, "name is required (max 100 characters)")
		return
	}
	e, err := cronexpr.Parse(sc.Cron)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "schedule: "+err.Error())
		return
	}
	if e.Next(time.Now()).IsZero() {
		writeErr(w, http.StatusBadRequest, "schedule never fires")
		return
	}
	if !sc.AllServers && (len(sc.ServerIDs) == 0 || len(sc.ServerIDs) > bulkMax) {
		writeErr(w, http.StatusBadRequest, "pick servers or all servers")
		return
	}
	if sc.AllServers {
		sc.ServerIDs = []string{}
	}
	switch sc.Kind {
	case "op":
		if err := validateSteps([]store.Step{{Op: req.Op, Params: req.Params}}); err != nil {
			writeErr(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "step 1: "))
			return
		}
		sc.Op = req.Op
		if req.Params != nil {
			sc.Params = req.Params
		}
	case "playbook":
		if _, err := a.Store.GetPlaybook(r.Context(), req.PlaybookID); err != nil {
			writeErr(w, http.StatusBadRequest, "playbook not found")
			return
		}
		sc.PlaybookID = &req.PlaybookID
	default:
		writeErr(w, http.StatusBadRequest, "kind must be op or playbook")
		return
	}
	if err := a.Store.SaveSchedule(r.Context(), sc); err != nil {
		if err == store.ErrNotFound {
			writeErr(w, http.StatusNotFound, "schedule not found")
			return
		}
		a.internal(w, err)
		return
	}
	a.audit(r, "", "schedule.save", sc.Name, map[string]any{"cron": sc.Cron, "kind": sc.Kind, "op": sc.Op, "enabled": sc.Enabled}, true)
	writeJSON(w, http.StatusOK, viewSchedule(sc))
}

func (a *API) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	sc, err := a.Store.GetSchedule(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "schedule not found")
		return
	}
	if err := a.Store.DeleteSchedule(r.Context(), sc.ID); err != nil {
		a.internal(w, err)
		return
	}
	a.audit(r, "", "schedule.delete", sc.Name, nil, true)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleRunScheduleNow(w http.ResponseWriter, r *http.Request) {
	sc, err := a.Store.GetSchedule(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "schedule not found")
		return
	}
	a.audit(r, "", "schedule.run_now", sc.Name, nil, true)
	result := a.runSchedule(a.baseCtx(), sc)
	writeJSON(w, http.StatusOK, map[string]string{"result": result})
}
