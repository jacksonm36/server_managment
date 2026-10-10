package api

import (
	"net/http"
	"time"
)

const settingBaseline = "default_baseline_playbook"

// ApplyBaseline runs the playbook chosen at enrollment the first time a new
// server connects. It is called from the gRPC server after the agent's Hello.
func (a *API) ApplyBaseline(serverID string) {
	ctx := a.baseCtx()
	pbID, ok, err := a.Store.ClaimBaseline(ctx, serverID)
	if err != nil {
		a.Log.Error("baseline: claim", "server", serverID, "err", err)
		return
	}
	if !ok {
		return
	}
	pb, err := a.Store.GetPlaybook(ctx, pbID)
	if err != nil {
		a.Log.Warn("baseline: playbook gone", "server", serverID, "playbook", pbID)
		return
	}
	// The hub registers the stream right after Hello; give it a moment.
	for i := 0; i < 20 && !a.Hub.Online(serverID); i++ {
		time.Sleep(250 * time.Millisecond)
	}
	if _, err := a.StartPlaybook(ctx, Who{Actor: "baseline", IP: "panel", Role: systemRole}, pb, []string{serverID}); err != nil {
		a.Log.Error("baseline: start", "server", serverID, "err", err)
		return
	}
	a.Log.Info("baseline started", "server", serverID, "playbook", pb.Name)
}

func (a *API) handleGetBaseline(w http.ResponseWriter, r *http.Request) {
	var id string
	if err := a.Store.GetSetting(r.Context(), settingBaseline, &id); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"playbook_id": id})
}

func (a *API) handleSetBaseline(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PlaybookID string `json:"playbook_id"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	name := "none"
	if req.PlaybookID != "" {
		pb, err := a.Store.GetPlaybook(r.Context(), req.PlaybookID)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "playbook not found")
			return
		}
		name = pb.Name
	}
	if err := a.Store.SetSetting(r.Context(), settingBaseline, req.PlaybookID); err != nil {
		a.internal(w, err)
		return
	}
	a.audit(r, "", "settings.baseline", name, nil, true)
	writeJSON(w, http.StatusOK, map[string]string{"playbook_id": req.PlaybookID})
}
