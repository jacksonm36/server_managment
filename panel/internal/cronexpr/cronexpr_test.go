package cronexpr

import (
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, time.UTC)
	if err != nil {
		panic(err)
	}
	return t
}

func TestNext(t *testing.T) {
	cases := []struct{ expr, from, want string }{
		{"*/15 * * * *", "2026-10-10 13:07", "2026-10-10 13:15"},
		{"0 3 * * *", "2026-10-10 13:07", "2026-10-11 03:00"},
		{"0 3 * * *", "2026-10-10 02:59", "2026-10-10 03:00"},
		{"@hourly", "2026-10-10 13:00", "2026-10-10 14:00"},
		{"30 4 * * mon-fri", "2026-10-10 13:00", "2026-10-12 04:30"}, // Sat -> Mon
		{"0 0 1 * *", "2026-12-15 00:00", "2027-01-01 00:00"},
		{"0 0 * * 7", "2026-10-10 13:00", "2026-10-11 00:00"},     // 7 = Sunday
		{"0 12 13 * fri", "2026-10-10 13:00", "2026-10-13 12:00"}, // dom OR dow
		{"0 0 29 2 *", "2026-03-01 00:00", "2028-02-29 00:00"},
		{"5,35 9-17/4 * jan,oct *", "2026-10-10 13:36", "2026-10-10 17:05"},
	}
	for _, c := range cases {
		e, err := Parse(c.expr)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		if got := e.Next(at(c.from)); !got.Equal(at(c.want)) {
			t.Errorf("%s from %s: got %s, want %s", c.expr, c.from, got.Format("2006-01-02 15:04"), c.want)
		}
	}
}

func TestImpossible(t *testing.T) {
	e, err := Parse("0 0 30 2 *")
	if err != nil {
		t.Fatal(err)
	}
	if got := e.Next(at("2026-01-01 00:00")); !got.IsZero() {
		t.Errorf("Feb 30 matched %s", got)
	}
}

func TestParseErrors(t *testing.T) {
	for _, s := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "*/0 * * * *", "a * * * *", "5-1 * * * *", "* * * * 8", "@sometimes"} {
		if _, err := Parse(s); err == nil {
			t.Errorf("%q parsed", s)
		}
	}
}

func TestMatches(t *testing.T) {
	e, _ := Parse("*/10 8-18 * * 1-5")
	if !e.Matches(at("2026-10-12 08:20")) || e.Matches(at("2026-10-12 08:21")) || e.Matches(at("2026-10-11 08:20")) {
		t.Error("Matches wrong")
	}
}
