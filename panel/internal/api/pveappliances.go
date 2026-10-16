package api

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// Appliances are Proxmox's own container template catalog (aplinfo): the
// plain OS templates and the TurnKey Linux appliances (WordPress,
// Nextcloud, GitLab…), ready-made systems that set themselves up on first
// boot. Anyone who may see a node can browse it; downloading one into a
// storage is for platform admins, like other storage downloads.

type pveAppliance struct {
	Template    string `json:"template"`
	Package     string `json:"package"`
	Section     string `json:"section"`
	Headline    string `json:"headline"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version,omitempty"`
	OS          string `json:"os,omitempty"`
	Infopage    string `json:"infopage,omitempty"`
	Type        string `json:"type"`
}

var pveTemplateName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.+-]{0,200}\.tar\.(gz|xz|zst)$`)

func (a *API) aplinfo(ctx context.Context, s *pveScope, node string) ([]pveAppliance, error) {
	var list []pveAppliance
	if err := s.Cl.Do(ctx, http.MethodGet, "/nodes/"+url.PathEscape(node)+"/aplinfo", nil, &list); err != nil {
		return nil, err
	}
	out := list[:0]
	for _, ap := range list {
		if ap.Type == "lxc" && pveTemplateName.MatchString(ap.Template) {
			if len(ap.Description) > 2000 {
				ap.Description = ap.Description[:2000] + "…"
			}
			out = append(out, ap)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Section != out[j].Section {
			return out[i].Section < out[j].Section
		}
		return out[i].Package < out[j].Package
	})
	return out, nil
}

func (a *API) handlePVEAppliances(w http.ResponseWriter, r *http.Request) {
	node := r.PathValue("node")
	if !pveNode.MatchString(node) {
		writeErr(w, http.StatusBadRequest, "bad node")
		return
	}
	s, err := a.pveScope(r)
	if err != nil {
		scopeErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	list, err := a.aplinfo(ctx, s, node)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handlePVEApplianceDownload has the node fetch an appliance into a
// storage that holds container templates. Proxmox downloads it from its
// own catalog and checks its signature.
func (a *API) handlePVEApplianceDownload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Storage  string `json:"storage"`
		Template string `json:"template"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s, node, ok := a.pveAdminNode(w, r)
	if !ok {
		return
	}
	if !pveStorage.MatchString(req.Storage) || !pveTemplateName.MatchString(req.Template) {
		writeErr(w, http.StatusBadRequest, "pick a storage and an appliance")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	list, err := a.aplinfo(ctx, s, node)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if !slices.ContainsFunc(list, func(ap pveAppliance) bool { return ap.Template == req.Template }) {
		writeErr(w, http.StatusBadRequest, "not in this node's appliance catalog")
		return
	}
	var storages []pveNodeStorage
	if err := s.Cl.Do(ctx, http.MethodGet, "/nodes/"+url.PathEscape(node)+"/storage", url.Values{"content": {"vztmpl"}}, &storages); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if !slices.ContainsFunc(storages, func(st pveNodeStorage) bool {
		return st.Storage == req.Storage && st.Active == 1 && slices.Contains(strings.Split(st.Content, ","), "vztmpl")
	}) {
		writeErr(w, http.StatusBadRequest, req.Storage+" does not hold container templates on "+node)
		return
	}
	a.pveRun(w, r, s.Cl, node, http.MethodPost, "/nodes/"+url.PathEscape(node)+"/aplinfo",
		url.Values{"storage": {req.Storage}, "template": {req.Template}}, "proxmox.appliance_download", s.C.Name+"/"+node+"/"+req.Storage+":"+req.Template, 0)
}
