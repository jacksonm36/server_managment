package ops

import "testing"

func TestSQLString(t *testing.T) {
	cases := map[string]string{
		`abc`:         `'abc'`,
		`it's`:        `'it\'s'`,
		`back\slash`:  `'back\\slash'`,
		`x'; DROP --`: `'x\'; DROP --'`,
		"nul\x00byte": `'nul\0byte'`,
		"line\nbreak": `'line\nbreak'`,
		`\'`:          `'\\\''`,
	}
	for in, want := range cases {
		if got := sqlString(in); got != want {
			t.Errorf("sqlString(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestDBIdentifiers(t *testing.T) {
	for _, ok := range []string{"wp_site1", "APP", "a"} {
		if !dbIdent.MatchString(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"", "a-b", "a`b", "a;b", "db name", "x'"} {
		if dbIdent.MatchString(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, h := range []string{"localhost", "%", "10.0.1.%", "::1", "10.0.0.0/255.0.0.0"} {
		if !dbHostRe.MatchString(h) {
			t.Errorf("host %q rejected", h)
		}
	}
	if dbHostRe.MatchString("evil'host") {
		t.Error("quoted host accepted")
	}
}
