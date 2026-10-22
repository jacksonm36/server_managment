package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ZFS replication between two servers: the source sends snapshots over SSH
// with a key of its own, which the target only lets run `zfs recv` into
// one parent dataset. Setting it up takes a job on each side, in order,
// so the panel does it rather than the browser.

// replJobWait is how long each set-up step may take; a var for tests.
var replJobWait = 60 * time.Second

var (
	replDataset = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.:/-]{0,250}$`)
	replHost    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:-]{0,252}$`)
	replFrom    = regexp.MustCompile(`^[0-9A-Fa-f.:/]{2,64}$`)
)

// replStep runs an op on a server as the panel's own work and returns its
// result, or the job's error.
func (a *API) replStep(ctx context.Context, who Who, serverID, op string, params map[string]string, out any) error {
	who.Internal = true
	job, err := a.Dispatch(ctx, who, serverID, op, params)
	if err != nil {
		return err
	}
	j, err := a.waitJob(ctx, job.ID, replJobWait)
	if err != nil {
		return err
	}
	if j.Status != "succeeded" {
		return &DispatchError{http.StatusBadGateway, op + ": " + j.Error}
	}
	if out != nil && len(j.Result) > 0 {
		if err := json.Unmarshal(j.Result, out); err != nil {
			return fmt.Errorf("%s: %w", op, err)
		}
	}
	return nil
}

type replKey struct {
	PublicKey   string `json:"public_key"`
	AddressHint string `json:"address_hint"`
}

// GET /api/v1/servers/{id}/zfs/replication/key: the source's replication
// key and the address it most likely connects from.
func (a *API) handleZFSReplKey(w http.ResponseWriter, r *http.Request) {
	var k replKey
	if err := a.replStep(r.Context(), whoFrom(r), r.PathValue("id"), "zfs.repl_key", nil, &k); err != nil {
		a.writeDispatchErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, k)
}

type replRequest struct {
	Dataset      string `json:"dataset"`
	TargetServer string `json:"target_server"`
	TargetParent string `json:"target_parent"`
	TargetHost   string `json:"target_host"`
	TargetPort   int    `json:"target_port"`
	From         string `json:"from"`
	Schedule     string `json:"schedule"`
	Keep         int    `json:"keep"`
}

func (q *replRequest) check(source string) error {
	q.TargetHost = strings.TrimSpace(q.TargetHost)
	q.From = strings.TrimSpace(q.From)
	switch {
	case !replDataset.MatchString(q.Dataset) || strings.Contains(q.Dataset, ".."):
		return errors.New("bad dataset")
	case q.TargetServer == "" || q.TargetServer == source:
		return errors.New("pick another server as the target")
	case !replDataset.MatchString(q.TargetParent) || strings.Contains(q.TargetParent, ".."):
		return errors.New("bad target parent dataset")
	case !replHost.MatchString(q.TargetHost):
		return errors.New("enter the address the source reaches the target at")
	case q.From != "" && !replFrom.MatchString(q.From):
		return errors.New("bad source address")
	case q.TargetPort < 0 || q.TargetPort > 65535:
		return errors.New("bad SSH port")
	}
	switch q.Schedule {
	case "":
		q.Schedule = "daily"
	case "hourly", "daily", "weekly", "manual":
	default:
		return errors.New("schedule must be hourly, daily, weekly or manual")
	}
	if q.Keep == 0 {
		q.Keep = 3
	}
	if q.Keep < 1 || q.Keep > 100 {
		return errors.New("keep must be between 1 and 100")
	}
	return nil
}

// POST /api/v1/servers/{id}/zfs/replication sets up replication of a
// dataset to another server.
func (a *API) handleZFSReplCreate(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("id")
	var q replRequest
	if !readJSON(w, r, &q) {
		return
	}
	if err := q.check(source); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	who := whoFrom(r)
	if !who.Access.CanOn("server.system", q.TargetServer) {
		forbid(w, "server.system")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*replJobWait)
	defer cancel()
	target := q.Dataset + " -> " + q.TargetHost + ":" + q.TargetParent
	fail := func(err error) {
		a.audit(r, "", "zfs.replication_create", target, map[string]any{"server_id": source, "target_server": q.TargetServer, "error": err.Error()}, false)
		a.writeDispatchErr(w, err)
	}
	var k replKey
	if err := a.replStep(ctx, who, source, "zfs.repl_key", nil, &k); err != nil {
		fail(err)
		return
	}
	var auth struct {
		HostKeys []string `json:"host_keys"`
		SSHPort  int      `json:"ssh_port"`
	}
	if err := a.replStep(ctx, who, q.TargetServer, "zfs.repl_authorize",
		map[string]string{"public_key": k.PublicKey, "parent": q.TargetParent, "from": q.From}, &auth); err != nil {
		fail(err)
		return
	}
	if len(auth.HostKeys) == 0 {
		fail(&DispatchError{http.StatusBadGateway, "the target reported no SSH host keys"})
		return
	}
	port := q.TargetPort
	if port == 0 {
		port = auth.SSHPort
	}
	if port == 0 {
		port = 22
	}
	var out map[string]any
	if err := a.replStep(ctx, who, source, "zfs.repl_policy", map[string]string{
		"dataset": q.Dataset, "target_host": q.TargetHost, "target_port": strconv.Itoa(port), "target_parent": q.TargetParent,
		"host_keys": strings.Join(auth.HostKeys, "\n"), "schedule": q.Schedule, "keep": strconv.Itoa(q.Keep),
	}, &out); err != nil {
		fail(err)
		return
	}
	if out == nil {
		out = map[string]any{}
	}
	out["target_server"] = q.TargetServer
	a.audit(r, "", "zfs.replication_create", target, map[string]any{"server_id": source, "target_server": q.TargetServer, "schedule": q.Schedule}, true)
	writeJSON(w, http.StatusOK, out)
}

// DELETE /api/v1/servers/{id}/zfs/replication stops a replication and, when
// the target is named and online, takes the source's key off it.
func (a *API) handleZFSReplDelete(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("id")
	var q struct {
		Dataset      string `json:"dataset"`
		TargetHost   string `json:"target_host"`
		TargetServer string `json:"target_server"`
	}
	if !readJSON(w, r, &q) {
		return
	}
	if !replDataset.MatchString(q.Dataset) || !replHost.MatchString(q.TargetHost) {
		writeErr(w, http.StatusBadRequest, "bad dataset or target")
		return
	}
	who := whoFrom(r)
	if q.TargetServer != "" && !who.Access.CanOn("server.system", q.TargetServer) {
		forbid(w, "server.system")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*replJobWait)
	defer cancel()
	target := q.Dataset + " -> " + q.TargetHost
	var out map[string]any
	if err := a.replStep(ctx, who, source, "zfs.repl_policy", map[string]string{"dataset": q.Dataset, "target_host": q.TargetHost, "delete": "1"}, &out); err != nil {
		a.audit(r, "", "zfs.replication_delete", target, map[string]any{"server_id": source, "error": err.Error()}, false)
		a.writeDispatchErr(w, err)
		return
	}
	if out == nil {
		out = map[string]any{}
	}
	if q.TargetServer != "" && q.TargetServer != source && !replToHost(out, q.TargetHost) {
		// One key serves every replication from this source, so it stays
		// on the target while another one still goes there.
		var k replKey
		err := a.replStep(ctx, who, source, "zfs.repl_key", nil, &k)
		if err == nil {
			err = a.replStep(ctx, who, q.TargetServer, "zfs.repl_authorize", map[string]string{"public_key": k.PublicKey, "revoke": "1"}, nil)
		}
		if err != nil {
			out["warning"] = "replication stopped, but the key could not be removed from the target: " + err.Error()
		}
	}
	a.audit(r, "", "zfs.replication_delete", target, map[string]any{"server_id": source, "target_server": q.TargetServer}, true)
	writeJSON(w, http.StatusOK, out)
}

// replToHost reports whether a zfs.repl_policy result still lists a
// replication to host.
func replToHost(out map[string]any, host string) bool {
	list, _ := out["replications"].([]any)
	for _, x := range list {
		if m, ok := x.(map[string]any); ok && m["target_host"] == host {
			return true
		}
	}
	return false
}
