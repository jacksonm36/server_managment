package api

import (
	"net/http"
	"net/url"
	"regexp"
	"time"
)

// Datacenter: cluster-wide Proxmox configuration as named collections
// (backup jobs, HA, pools, users and permissions, firewall, SDN, storage,
// replication, metric servers) plus read-only views (status, tasks, log).
// Each collection maps to one Proxmox API path; the UI lists, creates,
// edits and deletes entries with the same parameters Proxmox takes.
// Platform users with proxmox.manage only.

type pveCollection struct {
	Path      string // Proxmox API path of the list
	Nested    string // sub-path of an entry that lists its own items ("" = the entry itself)
	HasItems  bool   // entries hold items (security group rules, IP set entries, subnets)
	ReadOnly  bool
	Singleton bool // one object, read with GET and changed with PUT
	CreatePUT bool // created with PUT on the list (ACL)
}

var pveCollections = map[string]pveCollection{
	"status":       {Path: "/cluster/status", ReadOnly: true},
	"tasks":        {Path: "/cluster/tasks", ReadOnly: true},
	"log":          {Path: "/cluster/log", ReadOnly: true},
	"ha-status":    {Path: "/cluster/ha/status/current", ReadOnly: true},
	"ceph":         {Path: "/cluster/ceph/status", ReadOnly: true},
	"options":      {Path: "/cluster/options", Singleton: true},
	"backup":       {Path: "/cluster/backup"},
	"replication":  {Path: "/cluster/replication"},
	"ha-resources": {Path: "/cluster/ha/resources"},
	"ha-groups":    {Path: "/cluster/ha/groups"},
	"ha-rules":     {Path: "/cluster/ha/rules"},
	"pools":        {Path: "/pools"},
	"users":        {Path: "/access/users"},
	"groups":       {Path: "/access/groups"},
	"roles":        {Path: "/access/roles"},
	"realms":       {Path: "/access/domains"},
	"acl":          {Path: "/access/acl", CreatePUT: true},
	"fw-options":   {Path: "/cluster/firewall/options", Singleton: true},
	"fw-rules":     {Path: "/cluster/firewall/rules"},
	"fw-groups":    {Path: "/cluster/firewall/groups", HasItems: true},
	"fw-aliases":   {Path: "/cluster/firewall/aliases"},
	"fw-ipsets":    {Path: "/cluster/firewall/ipset", HasItems: true},
	"sdn-zones":    {Path: "/cluster/sdn/zones"},
	"sdn-vnets":    {Path: "/cluster/sdn/vnets", HasItems: true, Nested: "/subnets"},
	"storage":      {Path: "/storage"},
	"metrics":      {Path: "/cluster/metrics/server"},
}

var (
	pveDCKey  = regexp.MustCompile(`^[A-Za-z0-9@!._:+=-]{1,128}$`)
	pveDCItem = regexp.MustCompile(`^[A-Za-z0-9@!._:+=/-]{1,128}$`)
)

// pveDC resolves the collection and checks the caller is a platform user.
func (a *API) pveDC(w http.ResponseWriter, r *http.Request) (*pveScope, pveCollection, bool) {
	c, ok := pveCollections[r.PathValue("coll")]
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown datacenter section")
		return nil, c, false
	}
	s, err := a.pveScope(r)
	if err != nil {
		scopeErr(w, err)
		return nil, c, false
	}
	if s.TC != nil {
		writeErr(w, http.StatusNotFound, errNoCluster.Error())
		return nil, c, false
	}
	return s, c, true
}

// dcEntry is the path of {key} (and its {item}) in the collection.
func dcEntry(w http.ResponseWriter, r *http.Request, c pveCollection, withItem bool) (string, string, bool) {
	key := r.PathValue("key")
	if !pveDCKey.MatchString(key) {
		writeErr(w, http.StatusBadRequest, "bad entry")
		return "", "", false
	}
	p := c.Path + "/" + url.PathEscape(key)
	target := key
	if withItem {
		if !c.HasItems {
			writeErr(w, http.StatusNotFound, "this section has no items")
			return "", "", false
		}
		p += c.Nested
		if item := r.PathValue("item"); item != "" {
			if !pveDCItem.MatchString(item) {
				writeErr(w, http.StatusBadRequest, "bad item")
				return "", "", false
			}
			p += "/" + url.PathEscape(item)
			target += "/" + item
		}
	}
	return p, target, true
}

func dcAction(r *http.Request, verb string) string {
	return "proxmox.dc_" + r.PathValue("coll") + "_" + verb
}

func (a *API) handlePVEDCList(w http.ResponseWriter, r *http.Request) {
	s, c, ok := a.pveDC(w, r)
	if !ok {
		return
	}
	a.pveGetRaw(w, r, s, c.Path, nil)
}

func (a *API) handlePVEDCGet(w http.ResponseWriter, r *http.Request) {
	s, c, ok := a.pveDC(w, r)
	if !ok {
		return
	}
	if c.ReadOnly || c.Singleton {
		writeErr(w, http.StatusNotFound, "this section has no entries")
		return
	}
	p, _, ok := dcEntry(w, r, c, false)
	if !ok {
		return
	}
	a.pveGetRaw(w, r, s, p, nil)
}

func (a *API) handlePVEDCCreate(w http.ResponseWriter, r *http.Request) {
	form, ok := readParams(w, r)
	if !ok {
		return
	}
	s, c, ok := a.pveDC(w, r)
	if !ok {
		return
	}
	if c.ReadOnly || c.Singleton {
		writeErr(w, http.StatusBadRequest, "nothing to create here")
		return
	}
	method := http.MethodPost
	if c.CreatePUT {
		method = http.MethodPut
	}
	a.pveRun(w, r, s.Cl, "", method, c.Path, form, dcAction(r, "create"), s.C.Name, 0)
}

// handlePVEDCUpdate changes an entry, or a singleton without {key}.
func (a *API) handlePVEDCUpdate(w http.ResponseWriter, r *http.Request) {
	form, ok := readParams(w, r)
	if !ok {
		return
	}
	s, c, ok := a.pveDC(w, r)
	if !ok {
		return
	}
	if c.ReadOnly {
		writeErr(w, http.StatusBadRequest, "this section is read-only")
		return
	}
	p, target := c.Path, s.C.Name
	if r.PathValue("key") != "" {
		if c.Singleton {
			writeErr(w, http.StatusBadRequest, "this section has no entries")
			return
		}
		var ok bool
		if p, target, ok = dcEntry(w, r, c, false); !ok {
			return
		}
	} else if !c.Singleton {
		writeErr(w, http.StatusBadRequest, "pick an entry")
		return
	}
	a.pveRun(w, r, s.Cl, "", http.MethodPut, p, form, dcAction(r, "update"), target, 0)
}

func (a *API) handlePVEDCDelete(w http.ResponseWriter, r *http.Request) {
	s, c, ok := a.pveDC(w, r)
	if !ok {
		return
	}
	if c.ReadOnly || c.Singleton || c.CreatePUT {
		writeErr(w, http.StatusBadRequest, "nothing to delete here")
		return
	}
	p, target, ok := dcEntry(w, r, c, false)
	if !ok {
		return
	}
	a.pveRun(w, r, s.Cl, "", http.MethodDelete, p, nil, dcAction(r, "delete"), target, 0)
}

// handlePVEDCItems lists, adds and removes the items of an entry.
func (a *API) handlePVEDCItems(w http.ResponseWriter, r *http.Request) {
	var form url.Values
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		var ok bool
		if form, ok = readParams(w, r); !ok {
			return
		}
	}
	s, c, ok := a.pveDC(w, r)
	if !ok {
		return
	}
	p, target, ok := dcEntry(w, r, c, true)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		a.pveGetRaw(w, r, s, p, nil)
	case http.MethodPost:
		a.pveRun(w, r, s.Cl, "", http.MethodPost, p, form, dcAction(r, "item_add"), target, 0)
	case http.MethodPut:
		a.pveRun(w, r, s.Cl, "", http.MethodPut, p, form, dcAction(r, "item_update"), target, 0)
	case http.MethodDelete:
		a.pveRun(w, r, s.Cl, "", http.MethodDelete, p, nil, dcAction(r, "item_delete"), target, 0)
	}
}

// handlePVESDNApply applies pending SDN changes on all nodes.
func (a *API) handlePVESDNApply(w http.ResponseWriter, r *http.Request) {
	s, err := a.pveScope(r)
	if err != nil {
		scopeErr(w, err)
		return
	}
	if s.TC != nil {
		writeErr(w, http.StatusNotFound, errNoCluster.Error())
		return
	}
	a.pveRun(w, r, s.Cl, "", http.MethodPut, "/cluster/sdn", nil, "proxmox.sdn_apply", s.C.Name, time.Duration(0))
}
