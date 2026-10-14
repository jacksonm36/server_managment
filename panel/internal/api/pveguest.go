package api

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jacksonm36/servermgr/panel/internal/proxmox"
)

// Guest detail: live status, configuration, performance history and tasks
// of one guest. All of it goes through pveGuest, so a tenant's user reaches
// only guests in their own pool, with their own token.

// pveSecretKeys are config keys never sent to the browser.
var pveSecretKeys = map[string]bool{"cipassword": true}

func (a *API) handlePVEGuest(w http.ResponseWriter, r *http.Request) {
	s, node, typ, vmid, ok := a.pveGuest(w, r)
	if !ok {
		return
	}
	path := proxmox.GuestPath(node, typ, vmid)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	var (
		wg             sync.WaitGroup
		status, config map[string]any
		stErr, cfgErr  error
		ips            []string
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		stErr = s.Cl.Do(ctx, http.MethodGet, path+"/status/current", nil, &status)
	}()
	go func() {
		defer wg.Done()
		cfgErr = s.Cl.Do(ctx, http.MethodGet, path+"/config", nil, &config)
	}()
	wg.Wait()
	if stErr != nil {
		writeErr(w, http.StatusBadGateway, stErr.Error())
		return
	}
	if cfgErr != nil {
		writeErr(w, http.StatusBadGateway, cfgErr.Error())
		return
	}
	for k := range config {
		if pveSecretKeys[k] {
			config[k] = "••••••"
		}
	}
	if status["status"] == "running" {
		actx, acancel := context.WithTimeout(ctx, 3*time.Second)
		ips, _ = s.Cl.GuestIPs(actx, node, typ, vmid)
		acancel()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"node": node, "type": typ, "vmid": vmid,
		"status": status, "config": config, "ips": ips,
	})
}

// pveRRDKeys are the RRD columns the UI draws.
var pveRRDKeys = []string{"time", "cpu", "maxcpu", "mem", "maxmem", "netin", "netout", "diskread", "diskwrite"}

func (a *API) handlePVEGuestRRD(w http.ResponseWriter, r *http.Request) {
	tf := r.URL.Query().Get("timeframe")
	switch tf {
	case "":
		tf = "hour"
	case "hour", "day", "week", "month", "year":
	default:
		writeErr(w, http.StatusBadRequest, "timeframe: hour, day, week, month or year")
		return
	}
	s, node, typ, vmid, ok := a.pveGuest(w, r)
	if !ok {
		return
	}
	var raw []map[string]any
	form := url.Values{"timeframe": {tf}, "cf": {"AVERAGE"}}
	if err := s.Cl.Do(r.Context(), http.MethodGet, proxmox.GuestPath(node, typ, vmid)+"/rrddata", form, &raw); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(raw))
	for _, p := range raw {
		q := make(map[string]any, len(pveRRDKeys))
		for _, k := range pveRRDKeys {
			if v, ok := p[k]; ok {
				q[k] = v
			}
		}
		out = append(out, q)
	}
	writeJSON(w, http.StatusOK, out)
}

type pveTask struct {
	UPID      string `json:"upid"`
	Type      string `json:"type"`
	User      string `json:"user"`
	Status    string `json:"status,omitempty"`
	StartTime int64  `json:"starttime"`
	EndTime   int64  `json:"endtime,omitempty"`
}

func (a *API) handlePVEGuestTasks(w http.ResponseWriter, r *http.Request) {
	s, node, _, vmid, ok := a.pveGuest(w, r)
	if !ok {
		return
	}
	var tasks []pveTask
	form := url.Values{"vmid": {strconv.Itoa(vmid)}, "limit": {"30"}, "source": {"all"}}
	if err := s.Cl.Do(r.Context(), http.MethodGet, "/nodes/"+url.PathEscape(node)+"/tasks", form, &tasks); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if tasks == nil {
		tasks = []pveTask{}
	}
	writeJSON(w, http.StatusOK, tasks)
}

// handlePVEGuestTaskLog returns the log of one task, which must be about
// this guest: its UPID names the node and the vmid.
func (a *API) handlePVEGuestTaskLog(w http.ResponseWriter, r *http.Request) {
	s, node, _, vmid, ok := a.pveGuest(w, r)
	if !ok {
		return
	}
	upid := r.URL.Query().Get("upid")
	f := strings.Split(upid, ":")
	if len(f) < 8 || f[0] != "UPID" || f[1] != node || f[6] != strconv.Itoa(vmid) {
		writeErr(w, http.StatusNotFound, "no such task for this guest")
		return
	}
	var lines []struct {
		N int    `json:"n"`
		T string `json:"t"`
	}
	form := url.Values{"limit": {"1000"}}
	if err := s.Cl.Do(r.Context(), http.MethodGet, "/nodes/"+url.PathEscape(node)+"/tasks/"+url.PathEscape(upid)+"/log", form, &lines); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.T)
		b.WriteByte('\n')
	}
	writeJSON(w, http.StatusOK, map[string]string{"log": b.String()})
}
