package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/jacksonm36/servermgr/panel/internal/store"
)

// Central CrowdSec management. The LAPI runs in the servermgr-crowdsec
// container on the panel host (scripts/crowdsec-central.sh); the panel drives
// it with cscli through `docker exec`.

const csContainer = "servermgr-crowdsec"

func cscli(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	// The panel unit runs with SupplementaryGroups=docker (NoNewPrivileges
	// rules out sudo), so it talks to the Docker socket directly.
	full := append([]string{"exec", csContainer, "cscli"}, args...)
	out, err := exec.CommandContext(ctx, "docker", full...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if i := strings.LastIndex(msg, "\n"); i >= 0 {
			msg = msg[i+1:]
		}
		return nil, fmt.Errorf("cscli %s: %s", args[0], msg)
	}
	return out, nil
}

func cscliJSON(ctx context.Context, v any, args ...string) error {
	out, err := cscli(ctx, append(args, "-o", "json")...)
	if err != nil {
		return err
	}
	if s := strings.TrimSpace(string(out)); s == "" || s == "null" {
		return nil
	}
	return json.Unmarshal(out, v)
}

func (a *API) lapiURL() string {
	host := os.Getenv("SM_PUBLIC_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	port := os.Getenv("SM_CROWDSEC_PORT")
	if port == "" {
		port = "8085"
	}
	return "https://" + net.JoinHostPort(host, port) + "/"
}

var csName = regexp.MustCompile(`[^a-z0-9-]+`)

func machineName(srv *store.Server) string {
	n := strings.Trim(csName.ReplaceAllString(strings.ToLower(srv.Name), "-"), "-")
	if len(n) > 24 {
		n = n[:24]
	}
	return n + "-" + srv.ID[:6]
}

const csAllowlist = "servermgr"

// ensureAllowlist creates the panel allowlist and adds the panel host and
// the given admin IP, so neither can ever be banned.
func (a *API) ensureAllowlist(ctx context.Context, adminIP string) {
	_, _ = cscli(ctx, "allowlists", "create", csAllowlist, "--description", "never ban: panel host and admins")
	vals := []string{"127.0.0.1"}
	if h := os.Getenv("SM_PUBLIC_HOST"); h != "" {
		vals = append(vals, h)
	}
	if ip := net.ParseIP(adminIP); ip != nil && !ip.IsLoopback() {
		vals = append(vals, adminIP)
	}
	_, _ = cscli(ctx, append([]string{"allowlists", "add", csAllowlist}, vals...)...)
}

// provisionCrowdSec (re)creates the server's machine + bouncer on the
// central LAPI and returns the agent params with the credentials added
// (Dispatch stores them redacted). The caller's map is not modified.
func (a *API) provisionCrowdSec(ctx context.Context, srv *store.Server, adminIP string, in map[string]string) (map[string]string, error) {
	name := machineName(srv)
	pw := genPassword(32)
	_, _ = cscli(ctx, "machines", "delete", name)
	if _, err := cscli(ctx, "machines", "add", name, "--password", pw, "-f", "/dev/null", "--force"); err != nil {
		return nil, fmt.Errorf("central CrowdSec: %v", err)
	}
	_, _ = cscli(ctx, "bouncers", "delete", name+"-fw")
	key, err := cscli(ctx, "bouncers", "add", name+"-fw", "-o", "raw")
	if err != nil {
		return nil, fmt.Errorf("central CrowdSec: %v", err)
	}
	a.ensureAllowlist(ctx, adminIP)
	out := map[string]string{"lapi_url": a.lapiURL(), "machine": name, "password": pw, "bouncer_key": strings.TrimSpace(string(key))}
	if in["trust_lan"] == "off" {
		out["trust_lan"] = "off"
	}
	return out, nil
}

func (a *API) deprovisionCrowdSec(ctx context.Context, srv *store.Server) {
	name := machineName(srv)
	_, _ = cscli(ctx, "machines", "delete", name)
	_, _ = cscli(ctx, "bouncers", "delete", name+"-fw")
}

func (a *API) handleCrowdSecInstall(w http.ResponseWriter, r *http.Request) {
	p := map[string]string{}
	if r.URL.Query().Get("trust_lan") == "off" {
		p["trust_lan"] = "off"
	}
	job, err := a.Dispatch(r.Context(), whoFrom(r), r.PathValue("id"), "crowdsec.install", p)
	if err != nil {
		a.writeDispatchErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (a *API) handleCrowdSecRemove(w http.ResponseWriter, r *http.Request) {
	job, err := a.Dispatch(r.Context(), whoFrom(r), r.PathValue("id"), "crowdsec.remove", nil)
	if err != nil {
		a.writeDispatchErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (a *API) handleCrowdSecOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var machines, bouncers, decisions, alerts []map[string]any
	if err := cscliJSON(ctx, &machines, "machines", "list"); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "error": err.Error()})
		return
	}
	_ = cscliJSON(ctx, &bouncers, "bouncers", "list")
	_ = cscliJSON(ctx, &decisions, "decisions", "list", "--limit", "500")
	_ = cscliJSON(ctx, &alerts, "alerts", "list", "--since", "168h", "--limit", "500")
	var allow map[string]any
	_ = cscliJSON(ctx, &allow, "allowlists", "inspect", csAllowlist)
	writeJSON(w, http.StatusOK, map[string]any{
		"available": true, "lapi_url": a.lapiURL(),
		"machines": nz(machines), "bouncers": nz(bouncers), "decisions": nz(decisions), "alerts": nz(alerts), "allowlist": allow,
	})
}

func nz(v []map[string]any) []map[string]any {
	if v == nil {
		return []map[string]any{}
	}
	return v
}

var (
	durationRe = regexp.MustCompile(`^\d{1,5}[smhd]$`)
	countryRe  = regexp.MustCompile(`^[A-Z]{2}$`)
	reasonRe   = regexp.MustCompile(`^[\w .,:/()-]{0,100}$`)
)

func (a *API) handleCrowdSecBan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type     string `json:"type"` // ip | range | country
		Value    string `json:"value"`
		Duration string `json:"duration"`
		Reason   string `json:"reason"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Value = strings.TrimSpace(req.Value)
	if req.Duration == "" {
		req.Duration = "4h"
	}
	if !durationRe.MatchString(req.Duration) || !reasonRe.MatchString(req.Reason) {
		writeErr(w, http.StatusBadRequest, "duration like 4h / 7d; reason: plain text")
		return
	}
	args := []string{"decisions", "add", "--duration", req.Duration, "--reason", strings.TrimSpace("servermgr " + whoFrom(r).Actor + ": " + req.Reason)}
	switch req.Type {
	case "ip":
		if net.ParseIP(req.Value) == nil {
			writeErr(w, http.StatusBadRequest, "invalid IP")
			return
		}
		args = append(args, "--ip", req.Value)
	case "range":
		if _, _, err := net.ParseCIDR(req.Value); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid CIDR range")
			return
		}
		args = append(args, "--range", req.Value)
	case "country":
		req.Value = strings.ToUpper(req.Value)
		if !countryRe.MatchString(req.Value) {
			writeErr(w, http.StatusBadRequest, "country must be a 2-letter code like CN")
			return
		}
		args = append(args, "--scope", "Country", "--value", req.Value)
	default:
		writeErr(w, http.StatusBadRequest, "type must be ip, range or country")
		return
	}
	if _, err := cscli(r.Context(), args...); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	a.audit(r, "", "crowdsec.ban", req.Value, map[string]string{"type": req.Type, "duration": req.Duration, "reason": req.Reason}, true)
	w.WriteHeader(http.StatusNoContent)
}

var decisionID = regexp.MustCompile(`^\d{1,12}$`)

func (a *API) handleCrowdSecUnban(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !decisionID.MatchString(id) {
		writeErr(w, http.StatusBadRequest, "bad decision id")
		return
	}
	if _, err := cscli(r.Context(), "decisions", "delete", "--id", id); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	a.audit(r, "", "crowdsec.unban", id, nil, true)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleCrowdSecAllow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string `json:"action"` // add | remove
		Value  string `json:"value"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	v := strings.TrimSpace(req.Value)
	if net.ParseIP(v) == nil {
		if _, _, err := net.ParseCIDR(v); err != nil {
			writeErr(w, http.StatusBadRequest, "value must be an IP or CIDR range")
			return
		}
	}
	if req.Action != "add" && req.Action != "remove" {
		writeErr(w, http.StatusBadRequest, "action must be add or remove")
		return
	}
	a.ensureAllowlist(r.Context(), "")
	if _, err := cscli(r.Context(), "allowlists", req.Action, csAllowlist, v); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	a.audit(r, "", "crowdsec.allowlist_"+req.Action, v, nil, true)
	w.WriteHeader(http.StatusNoContent)
}

var enrollKey = regexp.MustCompile(`^[a-z0-9]{20,40}$`)

// handleCrowdSecEnroll links the central LAPI to the CrowdSec Console. The
// key is entered in the panel only and never logged.
func (a *API) handleCrowdSecEnroll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if !enrollKey.MatchString(strings.TrimSpace(req.Key)) {
		writeErr(w, http.StatusBadRequest, "that does not look like a CrowdSec Console enroll key")
		return
	}
	args := []string{"console", "enroll", strings.TrimSpace(req.Key)}
	if n := strings.TrimSpace(req.Name); n != "" && reasonRe.MatchString(n) {
		args = append(args, "--name", n)
	}
	if _, err := cscli(r.Context(), args...); err != nil {
		writeErr(w, http.StatusBadGateway, errors.New(strings.ReplaceAll(err.Error(), req.Key, "••••")).Error())
		return
	}
	a.audit(r, "", "crowdsec.console_enroll", req.Name, nil, true)
	writeJSON(w, http.StatusOK, map[string]string{"result": "Enrollment requested: accept it in the CrowdSec Console, then restart the LAPI container."})
}
