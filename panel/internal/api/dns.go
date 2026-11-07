package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jacksonm36/servermgr/panel/internal/dns"
	"github.com/jacksonm36/servermgr/panel/internal/store"
)

// ---- providers ----

func (a *API) dnsProvider(ctx context.Context, p *store.DNSProvider) (dns.Provider, error) {
	plain, err := a.openSecret(p.Config)
	if err != nil {
		return nil, err
	}
	cfg := map[string]string{}
	if err := json.Unmarshal(plain, &cfg); err != nil {
		return nil, err
	}
	return dns.New(p.Type, cfg)
}

// dnsZoneCache keeps each provider's zone list for a minute, so record
// calls don't list zones every time.
type dnsZoneCache struct {
	mu sync.Mutex
	m  map[string]dnsZones
}

type dnsZones struct {
	at    time.Time
	zones []dns.Zone
}

var zoneCache = &dnsZoneCache{m: map[string]dnsZones{}}

func (c *dnsZoneCache) forget(id string) {
	c.mu.Lock()
	delete(c.m, id)
	c.mu.Unlock()
}

func (a *API) dnsZones(ctx context.Context, p *store.DNSProvider, fresh bool) (dns.Provider, []dns.Zone, error) {
	prov, err := a.dnsProvider(ctx, p)
	if err != nil {
		return nil, nil, err
	}
	zoneCache.mu.Lock()
	c, ok := zoneCache.m[p.ID]
	zoneCache.mu.Unlock()
	if ok && !fresh && time.Since(c.at) < time.Minute {
		return prov, c.zones, nil
	}
	zones, err := prov.Zones(ctx)
	if err != nil {
		_ = a.Store.SetDNSZones(ctx, p.ID, nil, err.Error())
		return nil, nil, err
	}
	names := make([]string, len(zones))
	for i, z := range zones {
		names[i] = z.Name
	}
	_ = a.Store.SetDNSZones(ctx, p.ID, names, "")
	zoneCache.mu.Lock()
	zoneCache.m[p.ID] = dnsZones{at: time.Now(), zones: zones}
	zoneCache.mu.Unlock()
	return prov, zones, nil
}

func (a *API) handleDNSTypes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"providers": dns.Types, "record_types": dns.RecordTypes})
}

func (a *API) handleListDNSProviders(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.ListDNSProviders(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleSaveDNSProvider adds or changes a provider. The settings are tested
// by listing zones before anything is saved.
func (a *API) handleSaveDNSProvider(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string            `json:"name"`
		Type   string            `json:"type"`
		Config map[string]string `json:"config"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	t, ok := dns.TypeByID(req.Type)
	if !ok {
		writeErr(w, http.StatusBadRequest, "unknown provider type")
		return
	}
	if req.Name == "" {
		req.Name = t.Name
	}
	if len(req.Name) > 100 {
		writeErr(w, http.StatusBadRequest, "name is too long")
		return
	}
	cfg := map[string]string{}
	for _, f := range t.Fields {
		v := strings.TrimSpace(req.Config[f.Key])
		if len(v) > 4096 {
			writeErr(w, http.StatusBadRequest, f.Label+" is too long")
			return
		}
		cfg[f.Key] = v
	}
	p := &store.DNSProvider{ID: r.PathValue("id"), Name: req.Name, Type: req.Type}
	if p.ID != "" {
		cur, err := a.Store.GetDNSProvider(r.Context(), p.ID)
		if err != nil {
			writeErr(w, http.StatusNotFound, "provider not found")
			return
		}
		// Settings are never sent back to the browser, so a blank field on
		// update keeps the stored value.
		if cur.Type == req.Type {
			if plain, err := a.openSecret(cur.Config); err == nil {
				old := map[string]string{}
				_ = json.Unmarshal(plain, &old)
				for _, f := range t.Fields {
					if cfg[f.Key] == "" || cfg[f.Key] == secretMask {
						cfg[f.Key] = old[f.Key]
					}
				}
			} else {
				a.secretErr(w, err)
				return
			}
		}
	}
	prov, err := dns.New(req.Type, cfg)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	zones, err := prov.Zones(ctx)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "could not list zones with these settings: "+err.Error())
		return
	}
	for _, z := range zones {
		p.Zones = append(p.Zones, z.Name)
	}
	plain, _ := json.Marshal(cfg)
	if p.Config, err = a.sealSecret(plain); err != nil {
		a.secretErr(w, err)
		return
	}
	if err := a.Store.SaveDNSProvider(r.Context(), p); err != nil {
		a.internal(w, err)
		return
	}
	zoneCache.forget(p.ID)
	a.audit(r, "", "dns.provider_save", p.Name, map[string]any{"type": p.Type, "zones": len(zones)}, true)
	writeJSON(w, http.StatusOK, p)
}

func (a *API) handleDeleteDNSProvider(w http.ResponseWriter, r *http.Request) {
	p, err := a.Store.GetDNSProvider(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "provider not found")
		return
	}
	if err := a.Store.DeleteDNSProvider(r.Context(), p.ID); err != nil {
		a.internal(w, err)
		return
	}
	zoneCache.forget(p.ID)
	a.audit(r, "", "dns.provider_delete", p.Name, nil, true)
	w.WriteHeader(http.StatusNoContent)
}

// ---- zones and records ----

func (a *API) dnsError(w http.ResponseWriter, err error) {
	var apiErr *dns.APIError
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
	case errors.As(err, &apiErr):
		writeErr(w, http.StatusBadGateway, err.Error())
	default:
		a.secretErr(w, err)
	}
}

func (a *API) handleDNSZones(w http.ResponseWriter, r *http.Request) {
	p, err := a.Store.GetDNSProvider(r.Context(), r.PathValue("id"))
	if err != nil {
		a.dnsError(w, err)
		return
	}
	_, zones, err := a.dnsZones(r.Context(), p, r.URL.Query().Get("fresh") == "1")
	if err != nil {
		a.dnsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, zones)
}

// zoneFor loads the provider and the zone named in the path.
func (a *API) zoneFor(r *http.Request) (*store.DNSProvider, dns.Provider, dns.Zone, error) {
	p, err := a.Store.GetDNSProvider(r.Context(), r.PathValue("id"))
	if err != nil {
		return nil, nil, dns.Zone{}, err
	}
	prov, zones, err := a.dnsZones(r.Context(), p, false)
	if err != nil {
		return nil, nil, dns.Zone{}, err
	}
	id := r.PathValue("zone")
	for _, z := range zones {
		if z.ID == id || z.Name == id {
			return p, prov, z, nil
		}
	}
	return nil, nil, dns.Zone{}, store.ErrNotFound
}

func (a *API) handleDNSRecords(w http.ResponseWriter, r *http.Request) {
	_, prov, zone, err := a.zoneFor(r)
	if err != nil {
		a.dnsError(w, err)
		return
	}
	recs, err := prov.Records(r.Context(), zone)
	if err != nil {
		a.dnsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"zone": zone, "records": recs})
}

// handleDNSRecordChange creates (POST), updates (PUT) or deletes (DELETE)
// one record. Update and delete take the record as listed, so record-set
// providers know which value to change.
func (a *API) handleDNSRecordChange(w http.ResponseWriter, r *http.Request) {
	p, prov, zone, err := a.zoneFor(r)
	if err != nil {
		a.dnsError(w, err)
		return
	}
	var req struct {
		Old    *dns.Record `json:"old"`
		Record dns.Record  `json:"record"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	action := ""
	switch r.Method {
	case http.MethodPost:
		action = "dns.record_create"
		if err = dns.Validate(zone, &req.Record); err == nil {
			err = prov.Create(ctx, zone, req.Record)
		}
	case http.MethodPut:
		action = "dns.record_update"
		if req.Old == nil {
			writeErr(w, http.StatusBadRequest, "old record is required")
			return
		}
		if err = dns.Validate(zone, &req.Record); err == nil {
			err = prov.Update(ctx, zone, *req.Old, req.Record)
		}
	case http.MethodDelete:
		action = "dns.record_delete"
		req.Record.Name = strings.TrimSuffix(req.Record.Name, ".")
		err = prov.Delete(ctx, zone, req.Record)
	}
	detail := map[string]any{"provider": p.Name, "type": req.Record.Type, "content": truncate(req.Record.Content, 200)}
	if err != nil {
		a.audit(r, "", action, req.Record.Name, map[string]any{"provider": p.Name, "error": err.Error()}, false)
		var apiErr *dns.APIError
		if errors.As(err, &apiErr) {
			writeErr(w, http.StatusBadGateway, err.Error())
		} else {
			writeErr(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	a.audit(r, "", action, req.Record.Name, detail, true)
	w.WriteHeader(http.StatusNoContent)
}

// ---- name → zone helpers (sites, the panel certificate) ----

// dnsMatch is the provider and zone that host a name.
type dnsMatch struct {
	Provider *store.DNSProvider `json:"provider"`
	Zone     dns.Zone           `json:"zone"`
	prov     dns.Provider
}

// findZone finds the most specific zone, across all providers, that holds
// name (www.shop.example.com → shop.example.com before example.com).
func (a *API) findZone(ctx context.Context, name string) (*dnsMatch, error) {
	name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	list, err := a.Store.ListDNSProviders(ctx)
	if err != nil {
		return nil, err
	}
	var best *dnsMatch
	for _, p := range list {
		// The stored zone names avoid calling every provider.
		hit := slices.ContainsFunc(p.Zones, func(z string) bool { return name == z || strings.HasSuffix(name, "."+z) })
		if !hit {
			continue
		}
		prov, zones, err := a.dnsZones(ctx, p, false)
		if err != nil {
			continue
		}
		for _, z := range zones {
			if (name == z.Name || strings.HasSuffix(name, "."+z.Name)) && (best == nil || len(z.Name) > len(best.Zone.Name)) {
				best = &dnsMatch{Provider: p, Zone: z, prov: prov}
			}
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no DNS provider in the panel hosts %s", name)
	}
	return best, nil
}

// handleDNSLookup says which provider hosts a name and what A/AAAA/CNAME
// records it has now.
func (a *API) handleDNSLookup(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	m, err := a.findZone(r.Context(), name)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"found": false, "error": err.Error()})
		return
	}
	recs, err := m.prov.Records(r.Context(), m.Zone)
	if err != nil {
		a.dnsError(w, err)
		return
	}
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	cur := []dns.Record{}
	for _, rec := range recs {
		if rec.Name == name && (rec.Type == "A" || rec.Type == "AAAA" || rec.Type == "CNAME") {
			cur = append(cur, rec)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"found": true, "provider": m.Provider, "zone": m.Zone, "records": cur})
}

// pointName makes name resolve to the given addresses: it replaces the
// name's A/AAAA records (and a CNAME, which can't sit next to them).
func (a *API) pointName(ctx context.Context, name string, addrs []netip.Addr, proxied *bool, ttl int) (*dnsMatch, error) {
	m, err := a.findZone(ctx, name)
	if err != nil {
		return nil, err
	}
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	recs, err := m.prov.Records(ctx, m.Zone)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, ip := range addrs {
		want[ip.String()] = true
	}
	for _, rec := range recs {
		if rec.Name != name {
			continue
		}
		switch {
		case rec.Type == "CNAME", (rec.Type == "A" || rec.Type == "AAAA") && !want[rec.Content]:
			if err := m.prov.Delete(ctx, m.Zone, rec); err != nil {
				return nil, err
			}
		case rec.Type == "A" || rec.Type == "AAAA":
			delete(want, rec.Content) // already there
		}
	}
	for _, ip := range addrs {
		if !want[ip.String()] {
			continue
		}
		typ := "A"
		if ip.Is6() {
			typ = "AAAA"
		}
		rec := dns.Record{Type: typ, Name: name, Content: ip.String(), TTL: ttl, Proxied: proxied}
		if err := dns.Validate(m.Zone, &rec); err != nil {
			return nil, err
		}
		if err := m.prov.Create(ctx, m.Zone, rec); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// handleDNSPoint points a host name at a server: {"name", "addresses",
// "proxied"}. Used by the Sites tab and the DNS page.
func (a *API) handleDNSPoint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string   `json:"name"`
		Addresses []string `json:"addresses"`
		Proxied   *bool    `json:"proxied"`
		TTL       int      `json:"ttl"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	var addrs []netip.Addr
	for _, s := range req.Addresses {
		ip, err := netip.ParseAddr(strings.TrimSpace(s))
		if err != nil {
			writeErr(w, http.StatusBadRequest, "not an IP address: "+s)
			return
		}
		if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
			writeErr(w, http.StatusBadRequest, s+" can't be used in DNS")
			return
		}
		addrs = append(addrs, ip.Unmap())
	}
	if len(addrs) == 0 || len(addrs) > 8 {
		writeErr(w, http.StatusBadRequest, "give 1 to 8 addresses")
		return
	}
	if req.TTL == 0 {
		req.TTL = 300
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	m, err := a.pointName(ctx, req.Name, addrs, req.Proxied, req.TTL)
	if err != nil {
		a.audit(r, "", "dns.point", req.Name, map[string]any{"error": err.Error()}, false)
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	a.audit(r, "", "dns.point", req.Name, map[string]any{"provider": m.Provider.Name, "addresses": req.Addresses}, true)
	writeJSON(w, http.StatusOK, map[string]any{"provider": m.Provider.Name, "zone": m.Zone.Name})
}
