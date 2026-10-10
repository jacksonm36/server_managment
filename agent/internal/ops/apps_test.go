package ops

import (
	"strings"
	"testing"
)

func TestMaskSecrets(t *testing.T) {
	var got []string
	logf := maskSecrets(func(f string, a ...any) { got = append(got, a[0].(string)) }, map[string]string{"db_password": "S3cretPassw0rd", "title": "S3cretPassw0rd-not-a-secret-key"})
	logf("wp config create --dbpass='%s'", "S3cretPassw0rd")
	if strings.Contains(got[0], "S3cretPassw0rd") {
		t.Fatalf("secret leaked: %s", got[0])
	}
}

func TestSubstAndPaths(t *testing.T) {
	if _, err := subst("--x=${nope}", map[string]string{}); err == nil {
		t.Fatal("unknown variable accepted")
	}
	v, _ := subst("--dbname=${db_name}", map[string]string{"db_name": "a; rm -rf /"})
	if v != "--dbname=a; rm -rf /" {
		t.Fatalf("substitution changed the value: %q", v)
	}
	s := &Site{Domain: "x.test"}
	for _, bad := range []string{"../../etc/passwd", "../y.test/public"} {
		if p, err := insideSite(s, bad); err == nil && !strings.HasPrefix(p, s.home()) {
			t.Errorf("%q escaped to %s", bad, p)
		}
	}
}
