package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jacksonm36/servermgr/panel/internal/hub"
	"github.com/jacksonm36/servermgr/panel/internal/store"
)

const alertInterval = 15 * time.Second

// metricValue extracts a rule's metric from a sample (percentages for
// cpu/mem/disk, raw 1-minute load average for load).
func metricValue(m hub.Metric, metric string) float64 {
	pct := func(used, total uint64) float64 {
		if total == 0 {
			return 0
		}
		return 100 * float64(used) / float64(total)
	}
	switch metric {
	case "cpu":
		return m.CPU
	case "mem":
		return pct(m.MemUsed, m.MemTotal)
	case "disk":
		return pct(m.DiskUsed, m.DiskTotal)
	case "load":
		return m.Load1
	}
	return 0
}

// evalMetric decides whether a threshold rule is breached: every sample in
// the last dur must exceed threshold, and the samples must cover (almost)
// the whole window, so a short spike or a fresh connection never fires.
// It also reports the latest value and whether that value is back to normal.
func evalMetric(samples []hub.Metric, metric string, threshold float64, dur time.Duration, now time.Time) (breached bool, latest float64, normal bool) {
	if len(samples) == 0 {
		return false, 0, false
	}
	latest = metricValue(samples[len(samples)-1], metric)
	normal = latest <= threshold
	from := now.Add(-dur).UnixMilli()
	covered := false
	n := 0
	for _, s := range samples {
		if s.TS < from {
			covered = true // we have data from before the window starts
			continue
		}
		n++
		if metricValue(s, metric) <= threshold {
			return false, latest, normal
		}
	}
	return covered && n > 0, latest, normal
}

func (a *API) RunAlerts(ctx context.Context) {
	t := time.NewTicker(alertInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		a.evaluateAlerts(ctx, time.Now())
	}
}

func (a *API) evaluateAlerts(ctx context.Context, now time.Time) {
	rules, err := a.Store.ListRules(ctx)
	if err != nil {
		a.Log.Error("alerts: rules", "err", err)
		return
	}
	servers, err := a.Store.ListServers(ctx)
	if err != nil {
		return
	}
	open, err := a.Store.OpenAlerts(ctx)
	if err != nil {
		return
	}
	openBy := map[string]*store.AlertEvent{}
	for _, e := range open {
		openBy[e.RuleID+"/"+e.ServerID] = e
	}
	for _, r := range rules {
		dur := time.Duration(r.DurationSec) * time.Second
		for _, s := range servers {
			if !r.AllServers && !slices.Contains(r.ServerIDs, s.ID) {
				continue
			}
			cur := openBy[r.ID+"/"+s.ID]
			var breached, normal bool
			var value float64
			if r.Metric == "offline" {
				online := a.Hub.Online(s.ID)
				since, ok := a.Hub.OfflineSince(s.ID)
				if !ok && s.LastSeenAt != nil {
					since = *s.LastSeenAt
				}
				if !online && !since.IsZero() {
					value = now.Sub(since).Minutes()
					breached = now.Sub(since) >= dur
				}
				normal = online
			} else {
				if !a.Hub.Online(s.ID) {
					continue // offline servers are the offline rule's job
				}
				breached, value, normal = evalMetric(a.Hub.Metrics(s.ID), r.Metric, r.Threshold, dur, now)
			}
			switch {
			case cur == nil && breached && r.Enabled:
				e := &store.AlertEvent{RuleID: r.ID, RuleName: r.Name, ServerID: s.ID, ServerName: s.Name, Metric: r.Metric, Value: value, Threshold: r.Threshold}
				if ok, err := a.Store.FireAlert(ctx, e); err == nil && ok {
					a.Log.Warn("alert firing", "rule", r.Name, "server", s.Name, "value", value)
					a.notify(r.ChannelIDs, alertMessage(e, r, true))
				}
			case cur != nil && (normal || !r.Enabled):
				if err := a.Store.ResolveAlert(ctx, cur.ID, value); err == nil {
					cur.Value = value
					a.Log.Info("alert resolved", "rule", r.Name, "server", s.Name)
					a.notify(r.ChannelIDs, alertMessage(cur, r, false))
				}
			}
		}
	}
}

func fmtValue(metric string, v float64) string {
	switch metric {
	case "load":
		return fmt.Sprintf("%.2f", v)
	case "offline":
		return fmt.Sprintf("%.0f min", v)
	}
	return fmt.Sprintf("%.1f%%", v)
}

var metricNames = map[string]string{"cpu": "CPU", "mem": "Memory", "disk": "Disk /", "load": "Load", "offline": "Offline"}

func alertMessage(e *store.AlertEvent, r *store.AlertRule, firing bool) Message {
	dur := (time.Duration(r.DurationSec) * time.Second).String()
	m := Message{Firing: firing, Rule: r.Name, Server: e.ServerName, Metric: r.Metric, Value: e.Value, Threshold: r.Threshold}
	if firing {
		if r.Metric == "offline" {
			m.Title = fmt.Sprintf("[FIRING] %s: %s has been offline for %s", r.Name, e.ServerName, fmtValue("offline", e.Value))
		} else {
			m.Title = fmt.Sprintf("[FIRING] %s: %s %s is %s (> %s for %s)", r.Name, e.ServerName, metricNames[r.Metric], fmtValue(r.Metric, e.Value), fmtValue(r.Metric, r.Threshold), dur)
		}
	} else {
		m.Title = fmt.Sprintf("[RESOLVED] %s on %s", r.Name, e.ServerName)
		if r.Metric != "offline" {
			m.Title += fmt.Sprintf(" (%s now %s)", metricNames[r.Metric], fmtValue(r.Metric, e.Value))
		}
	}
	return m
}

// ---- handlers ----

func (a *API) handleListRules(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.ListRules(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *API) handleSaveRule(w http.ResponseWriter, r *http.Request) {
	var req store.AlertRule
	if !readJSON(w, r, &req) {
		return
	}
	req.ID = r.PathValue("id")
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 100 {
		writeErr(w, http.StatusBadRequest, "name is required (max 100 characters)")
		return
	}
	if _, ok := metricNames[req.Metric]; !ok {
		writeErr(w, http.StatusBadRequest, "metric must be cpu, mem, disk, load or offline")
		return
	}
	if req.Metric == "offline" {
		if req.DurationSec < 30 || req.DurationSec > 86400 {
			writeErr(w, http.StatusBadRequest, "offline duration must be 30 s to 24 h")
			return
		}
	} else {
		// The panel keeps 15 minutes of samples per server.
		if req.DurationSec < 30 || req.DurationSec > 840 {
			writeErr(w, http.StatusBadRequest, "duration must be 30 s to 14 min")
			return
		}
		if req.Threshold <= 0 || (req.Metric != "load" && req.Threshold >= 100) {
			writeErr(w, http.StatusBadRequest, "threshold must be above 0 (and below 100 for percentages)")
			return
		}
	}
	if !req.AllServers && len(req.ServerIDs) == 0 {
		writeErr(w, http.StatusBadRequest, "pick servers or all servers")
		return
	}
	if req.AllServers {
		req.ServerIDs = []string{}
	}
	if req.ChannelIDs == nil {
		req.ChannelIDs = []string{}
	}
	for _, id := range req.ChannelIDs {
		if _, err := a.Store.GetChannel(r.Context(), id); err != nil {
			writeErr(w, http.StatusBadRequest, "notification channel not found")
			return
		}
	}
	if err := a.Store.SaveRule(r.Context(), &req); err != nil {
		if err == store.ErrNotFound {
			writeErr(w, http.StatusNotFound, "rule not found")
			return
		}
		a.internal(w, err)
		return
	}
	a.audit(r, "", "alert_rule.save", req.Name, map[string]any{"metric": req.Metric, "threshold": req.Threshold, "duration_sec": req.DurationSec, "enabled": req.Enabled}, true)
	writeJSON(w, http.StatusOK, req)
}

func (a *API) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	rule, err := a.Store.GetRule(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "rule not found")
		return
	}
	if err := a.Store.DeleteRule(r.Context(), rule.ID); err != nil {
		a.internal(w, err)
		return
	}
	a.audit(r, "", "alert_rule.delete", rule.Name, nil, true)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleListAlerts(w http.ResponseWriter, r *http.Request) {
	var list []*store.AlertEvent
	var err error
	if r.URL.Query().Get("state") == "firing" {
		list, err = a.Store.OpenAlerts(r.Context())
	} else {
		list, err = a.Store.ListAlertEvents(r.Context(), queryInt(r, "limit", 100, 500))
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}
