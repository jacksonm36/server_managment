package dns

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Hetzner, deSEC and PowerDNS store record sets (name + type → TTL and a
// list of values in zone-file syntax). rrsetProvider turns those into
// single records and back.

type rrset struct {
	Name   string // full name, no trailing dot
	Type   string
	TTL    int
	Values []string // zone-file syntax: quoted TXT, host names with a dot
}

type rrsetBackend interface {
	zones(ctx context.Context) ([]Zone, error)
	rrsets(ctx context.Context, zone Zone) ([]rrset, error)
	// put creates or replaces a whole set.
	put(ctx context.Context, zone Zone, set rrset, exists bool) error
	del(ctx context.Context, zone Zone, name, typ string) error
}

type rrsetProvider struct {
	b      rrsetBackend
	minTTL int
}

func (p *rrsetProvider) Zones(ctx context.Context) ([]Zone, error) { return p.b.zones(ctx) }

func (p *rrsetProvider) Records(ctx context.Context, zone Zone) ([]Record, error) {
	sets, err := p.b.rrsets(ctx, zone)
	if err != nil {
		return nil, err
	}
	out := []Record{}
	for _, s := range sets {
		for _, v := range s.Values {
			r := fromWire(s.Type, v)
			r.Name, r.TTL = s.Name, s.TTL
			r.ID = s.Name + "|" + s.Type + "|" + v
			out = append(out, r)
		}
	}
	sortRecords(out)
	return out, nil
}

func (p *rrsetProvider) find(ctx context.Context, zone Zone, name, typ string) (*rrset, error) {
	sets, err := p.b.rrsets(ctx, zone)
	if err != nil {
		return nil, err
	}
	for i := range sets {
		if sets[i].Name == name && sets[i].Type == typ {
			return &sets[i], nil
		}
	}
	return nil, nil
}

func (p *rrsetProvider) ttl(t int) int { return max(t, p.minTTL) }

func (p *rrsetProvider) Create(ctx context.Context, zone Zone, r Record) error {
	v, err := toWire(r)
	if err != nil {
		return err
	}
	cur, err := p.find(ctx, zone, r.Name, r.Type)
	if err != nil {
		return err
	}
	if cur == nil {
		return p.b.put(ctx, zone, rrset{Name: r.Name, Type: r.Type, TTL: p.ttl(r.TTL), Values: []string{v}}, false)
	}
	if slices.Contains(cur.Values, v) {
		return fmt.Errorf("%s %s %s already exists", r.Name, r.Type, r.Content)
	}
	if r.Type == "CNAME" {
		return fmt.Errorf("%s already has a CNAME", r.Name)
	}
	cur.Values = append(cur.Values, v)
	cur.TTL = p.ttl(r.TTL)
	return p.b.put(ctx, zone, *cur, true)
}

func (p *rrsetProvider) Update(ctx context.Context, zone Zone, old, r Record) error {
	if old.Name != r.Name || old.Type != r.Type {
		if err := p.Create(ctx, zone, r); err != nil {
			return err
		}
		return p.Delete(ctx, zone, old)
	}
	oldV, err := toWire(old)
	if err != nil {
		return err
	}
	v, err := toWire(r)
	if err != nil {
		return err
	}
	cur, err := p.find(ctx, zone, r.Name, r.Type)
	if err != nil {
		return err
	}
	if cur == nil {
		return p.Create(ctx, zone, r)
	}
	vals := []string{}
	for _, x := range cur.Values {
		if x != oldV && x != v {
			vals = append(vals, x)
		}
	}
	cur.Values = append(vals, v)
	cur.TTL = p.ttl(r.TTL)
	return p.b.put(ctx, zone, *cur, true)
}

func (p *rrsetProvider) Delete(ctx context.Context, zone Zone, r Record) error {
	v, err := toWire(r)
	if err != nil {
		return err
	}
	cur, err := p.find(ctx, zone, r.Name, r.Type)
	if err != nil {
		return err
	}
	if cur == nil {
		return nil
	}
	vals := slices.DeleteFunc(slices.Clone(cur.Values), func(x string) bool { return x == v })
	if len(vals) == len(cur.Values) {
		return fmt.Errorf("%s %s %s not found", r.Name, r.Type, r.Content)
	}
	if len(vals) == 0 {
		return p.b.del(ctx, zone, r.Name, r.Type)
	}
	cur.Values = vals
	return p.b.put(ctx, zone, *cur, true)
}

// ---- zone-file values ----

func hostDot(h string) string {
	h = strings.TrimSpace(h)
	if h == "" || strings.HasSuffix(h, ".") {
		return h
	}
	return h + "."
}

// quoteTXT splits text into quoted strings of at most 255 bytes.
func quoteTXT(s string) string {
	if strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`) && len(s) > 1 {
		return s // already in zone-file form
	}
	var parts []string
	for len(s) > 255 {
		parts = append(parts, s[:255])
		s = s[255:]
	}
	parts = append(parts, s)
	for i, p := range parts {
		parts[i] = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(p) + `"`
	}
	return strings.Join(parts, " ")
}

// unquoteTXT joins the quoted strings of a TXT value.
func unquoteTXT(v string) string {
	var b strings.Builder
	in, esc := false, false
	for _, c := range v {
		switch {
		case esc:
			b.WriteRune(c)
			esc = false
		case c == '\\' && in:
			esc = true
		case c == '"':
			in = !in
		case in:
			b.WriteRune(c)
		}
	}
	if b.Len() == 0 && !strings.Contains(v, `"`) {
		return v
	}
	return b.String()
}

func toWire(r Record) (string, error) {
	prio := 10
	if r.Priority != nil {
		prio = *r.Priority
	}
	switch r.Type {
	case "TXT":
		return quoteTXT(r.Content), nil
	case "CNAME", "NS":
		return hostDot(r.Content), nil
	case "MX":
		return fmt.Sprintf("%d %s", prio, hostDot(r.Content)), nil
	case "SRV":
		f := strings.Fields(r.Content)
		if len(f) != 3 {
			return "", fmt.Errorf("SRV content must be: weight port target")
		}
		return fmt.Sprintf("%d %s %s %s", prio, f[0], f[1], hostDot(f[2])), nil
	}
	return r.Content, nil
}

func fromWire(typ, v string) Record {
	r := Record{Type: typ, Content: v}
	switch typ {
	case "TXT":
		r.Content = unquoteTXT(v)
	case "CNAME", "NS":
		r.Content = strings.TrimSuffix(v, ".")
	case "MX":
		if f := strings.Fields(v); len(f) == 2 {
			if p, err := strconv.Atoi(f[0]); err == nil {
				r.Priority, r.Content = &p, strings.TrimSuffix(f[1], ".")
			}
		}
	case "SRV":
		if f := strings.Fields(v); len(f) == 4 {
			if p, err := strconv.Atoi(f[0]); err == nil {
				r.Priority, r.Content = &p, f[1]+" "+f[2]+" "+strings.TrimSuffix(f[3], ".")
			}
		}
	}
	return r
}
