// Package cronexpr parses standard 5-field cron expressions
// (minute hour day-of-month month day-of-week) and @macros.
package cronexpr

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Expr struct {
	min, hour, dom, month, dow uint64 // bitsets
	domStar, dowStar           bool
}

var macros = map[string]string{
	"@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *", "@monthly": "0 0 1 * *",
	"@weekly": "0 0 * * 0", "@daily": "0 0 * * *", "@midnight": "0 0 * * *", "@hourly": "0 * * * *",
}

var monthNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
var dowNames = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}

func Parse(s string) (*Expr, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if m, ok := macros[s]; ok {
		s = m
	}
	f := strings.Fields(s)
	if len(f) != 5 {
		return nil, fmt.Errorf("cron expression needs 5 fields, got %d", len(f))
	}
	e := &Expr{domStar: f[2] == "*", dowStar: f[4] == "*"}
	var err error
	if e.min, err = field(f[0], 0, 59, nil); err != nil {
		return nil, fmt.Errorf("minute: %w", err)
	}
	if e.hour, err = field(f[1], 0, 23, nil); err != nil {
		return nil, fmt.Errorf("hour: %w", err)
	}
	if e.dom, err = field(f[2], 1, 31, nil); err != nil {
		return nil, fmt.Errorf("day of month: %w", err)
	}
	if e.month, err = field(f[3], 1, 12, monthNames); err != nil {
		return nil, fmt.Errorf("month: %w", err)
	}
	if e.dow, err = field(f[4], 0, 7, dowNames); err != nil {
		return nil, fmt.Errorf("day of week: %w", err)
	}
	if e.dow&(1<<7) != 0 { // 7 is Sunday too
		e.dow |= 1
	}
	return e, nil
}

func value(s string, names map[string]int) (int, error) {
	if n, ok := names[s]; ok {
		return n, nil
	}
	return strconv.Atoi(s)
}

func field(s string, lo, hi int, names map[string]int) (uint64, error) {
	var bits uint64
	for _, part := range strings.Split(s, ",") {
		rng, stepS, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepS)
			if err != nil || n < 1 {
				return 0, fmt.Errorf("bad step %q", stepS)
			}
			step = n
		}
		start, end := lo, hi
		if rng != "*" {
			a, b, isRange := strings.Cut(rng, "-")
			var err error
			if start, err = value(a, names); err != nil {
				return 0, fmt.Errorf("bad value %q", a)
			}
			end = start
			if isRange {
				if end, err = value(b, names); err != nil {
					return 0, fmt.Errorf("bad value %q", b)
				}
			} else if hasStep {
				end = hi
			}
		}
		if start < lo || end > hi || start > end {
			return 0, fmt.Errorf("%q is out of range %d-%d", part, lo, hi)
		}
		for v := start; v <= end; v += step {
			bits |= 1 << uint(v)
		}
	}
	return bits, nil
}

func has(bits uint64, v int) bool { return bits&(1<<uint(v)) != 0 }

func (e *Expr) dayMatches(t time.Time) bool {
	dom, dow := has(e.dom, t.Day()), has(e.dow, int(t.Weekday()))
	switch {
	case e.domStar && e.dowStar:
		return true
	case e.domStar:
		return dow
	case e.dowStar:
		return dom
	default: // both restricted: cron matches either
		return dom || dow
	}
}

// Matches reports whether t (to the minute) is a scheduled time.
func (e *Expr) Matches(t time.Time) bool {
	return has(e.min, t.Minute()) && has(e.hour, t.Hour()) && has(e.month, int(t.Month())) && e.dayMatches(t)
}

// Next returns the first scheduled time strictly after t, or the zero time
// if there is none within about five years (e.g. "0 0 30 2 *").
func (e *Expr) Next(t time.Time) time.Time {
	t = t.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		if !has(e.month, int(t.Month())) {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !e.dayMatches(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !has(e.hour, t.Hour()) {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, t.Location())
			continue
		}
		if !has(e.min, t.Minute()) {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return time.Time{}
}
