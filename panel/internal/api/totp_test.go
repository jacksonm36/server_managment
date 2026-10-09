package api

import (
	"testing"
	"time"
)

// RFC 6238 appendix B test vectors (SHA-1), truncated to 6 digits.
func TestTOTPVectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	cases := []struct {
		unix int64
		want string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
	}
	for _, c := range cases {
		if got := hotp(secret, uint64(c.unix/totpPeriod)); got != c.want {
			t.Errorf("t=%d: got %s want %s", c.unix, got, c.want)
		}
	}
}

func TestMatchTOTPWindow(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(1234567890, 0)
	prev := hotp(secret, uint64(now.Unix()/totpPeriod-1))
	if step := matchTOTP(secret, prev, now); step != now.Unix()/totpPeriod-1 {
		t.Fatalf("previous step not accepted: %d", step)
	}
	old := hotp(secret, uint64(now.Unix()/totpPeriod-3))
	if step := matchTOTP(secret, old, now); step != -1 {
		t.Fatalf("code 3 steps old accepted")
	}
}

func TestSealRoundTrip(t *testing.T) {
	a := &API{SecretKey: make([]byte, 32)}
	s, err := a.seal([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.open(s)
	if err != nil || string(got) != "hello" {
		t.Fatalf("round trip failed: %q %v", got, err)
	}
	a.SecretKey[0] = 1
	if _, err := a.open(s); err == nil {
		t.Fatal("open with wrong key succeeded")
	}
}

func TestRecoveryCodes(t *testing.T) {
	codes, hashes := newRecoveryCodes(3)
	for i, c := range codes {
		if len(c) != 11 || c[5] != '-' {
			t.Fatalf("bad format %q", c)
		}
		if string(hashRecovery(" "+toUpper(c)+" ")) != string(hashes[i]) {
			t.Fatalf("normalisation mismatch for %q", c)
		}
	}
}

func toUpper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}
