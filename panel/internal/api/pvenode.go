package api

import (
	"net/http"
	"net/url"
)

// Node details for the compute tree (platform users only: tenants never
// see nodes).

func (a *API) handlePVENodeStatus(w http.ResponseWriter, r *http.Request) {
	s, node, ok := a.pveAdminNode(w, r)
	if !ok {
		return
	}
	pvePass(w, r, s, nodePath(node)+"/status", nil)
}

// pveNodeRRDKeys are the node RRD columns the UI draws.
var pveNodeRRDKeys = []string{"time", "cpu", "iowait", "loadavg", "memused", "memtotal", "swapused", "netin", "netout", "rootused", "roottotal"}

func (a *API) handlePVENodeRRD(w http.ResponseWriter, r *http.Request) {
	tf := r.URL.Query().Get("timeframe")
	switch tf {
	case "":
		tf = "hour"
	case "hour", "day", "week", "month", "year":
	default:
		writeErr(w, http.StatusBadRequest, "timeframe: hour, day, week, month or year")
		return
	}
	s, node, ok := a.pveAdminNode(w, r)
	if !ok {
		return
	}
	var raw []map[string]any
	if err := s.Cl.Do(r.Context(), http.MethodGet, nodePath(node)+"/rrddata", url.Values{"timeframe": {tf}, "cf": {"AVERAGE"}}, &raw); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(raw))
	for _, p := range raw {
		q := make(map[string]any, len(pveNodeRRDKeys))
		for _, k := range pveNodeRRDKeys {
			if v, ok := p[k]; ok {
				q[k] = v
			}
		}
		out = append(out, q)
	}
	writeJSON(w, http.StatusOK, out)
}

// handlePVENodeTasks lists the node's recent tasks, all guests included.
func (a *API) handlePVENodeTasks(w http.ResponseWriter, r *http.Request) {
	s, node, ok := a.pveAdminNode(w, r)
	if !ok {
		return
	}
	form := url.Values{"limit": {"30"}}
	if r.URL.Query().Get("errors") == "1" {
		form.Set("errors", "1")
	}
	pvePass(w, r, s, nodePath(node)+"/tasks", form)
}
