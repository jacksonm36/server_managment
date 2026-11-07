package dns

import (
	"context"
	"encoding/json"
	"strings"
)

// deSEC (desec.io). Record sets are changed with a bulk PATCH; an empty
// record list deletes the set. deSEC's minimum TTL is 3600.
type deSEC struct{ c *apiClient }

func newDeSEC(cfg map[string]string, hc httpDoer) (Provider, error) {
	return &rrsetProvider{minTTL: 3600, b: &deSEC{c: &apiClient{
		hc:      hc,
		base:    "https://desec.io/api/v1",
		headers: map[string]string{"Authorization": "Token " + strings.TrimSpace(cfg["api_token"])},
		errMsg: func(b []byte) string {
			var e struct {
				Detail string `json:"detail"`
			}
			_ = json.Unmarshal(b, &e)
			return e.Detail
		},
	}}}, nil
}

func (d *deSEC) zones(ctx context.Context) ([]Zone, error) {
	var res []struct {
		Name string `json:"name"`
	}
	if err := d.c.do(ctx, "GET", "/domains/", nil, &res); err != nil {
		return nil, err
	}
	out := make([]Zone, 0, len(res))
	for _, z := range res {
		out = append(out, Zone{ID: z.Name, Name: z.Name})
	}
	return out, nil
}

type desecRRSet struct {
	Subname string   `json:"subname"`
	Type    string   `json:"type"`
	TTL     int      `json:"ttl,omitempty"`
	Records []string `json:"records"`
}

func (d *deSEC) rrsets(ctx context.Context, zone Zone) ([]rrset, error) {
	var res []desecRRSet
	if err := d.c.do(ctx, "GET", "/domains/"+esc(zone.ID)+"/rrsets/", nil, &res); err != nil {
		return nil, err
	}
	out := make([]rrset, 0, len(res))
	for _, s := range res {
		out = append(out, rrset{Name: fqdn(zone, s.Subname), Type: s.Type, TTL: s.TTL, Values: s.Records})
	}
	return out, nil
}

func (d *deSEC) sub(zone Zone, name string) string {
	if name == zone.Name {
		return ""
	}
	return strings.TrimSuffix(name, "."+zone.Name)
}

func (d *deSEC) put(ctx context.Context, zone Zone, s rrset, _ bool) error {
	return d.c.do(ctx, "PATCH", "/domains/"+esc(zone.ID)+"/rrsets/",
		[]desecRRSet{{Subname: d.sub(zone, s.Name), Type: s.Type, TTL: s.TTL, Records: s.Values}}, nil)
}

func (d *deSEC) del(ctx context.Context, zone Zone, name, typ string) error {
	return d.c.do(ctx, "PATCH", "/domains/"+esc(zone.ID)+"/rrsets/",
		[]desecRRSet{{Subname: d.sub(zone, name), Type: typ, Records: []string{}}}, nil)
}
