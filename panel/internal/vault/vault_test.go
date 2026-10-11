package vault

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestVault(t *testing.T) {
	v, hdr, recovery, err := Create("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := v.Seal([]byte("token"))
	if err != nil || !IsSealed(sealed) {
		t.Fatal(sealed, err)
	}

	// A restart: only the stored header survives.
	raw, _ := json.Marshal(hdr)
	var stored Header
	_ = json.Unmarshal(raw, &stored)
	v2 := New(&stored)
	if _, err := v2.Open(sealed); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked vault opened a value: %v", err)
	}
	if err := v2.Unlock("wrong passphrase!"); !errors.Is(err, ErrBadPassphrase) {
		t.Fatal(err)
	}
	if err := v2.Unlock("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if b, err := v2.Open(sealed); err != nil || string(b) != "token" {
		t.Fatal(string(b), err)
	}

	// The recovery key works in any case, with or without dashes.
	v3 := New(&stored)
	if err := v3.Unlock(strings.ToLower(strings.ReplaceAll(recovery, "-", " "))); err != nil {
		t.Fatal(err)
	}

	// Changing the passphrase keeps old values readable.
	h2, err := v3.ChangePassphrase(recovery, "a brand new passphrase")
	if err != nil {
		t.Fatal(err)
	}
	v4 := New(h2)
	if err := v4.Unlock("correct horse battery"); err == nil {
		t.Fatal("old passphrase still works")
	}
	if err := v4.Unlock("a brand new passphrase"); err != nil {
		t.Fatal(err)
	}
	if b, _ := v4.Open(sealed); string(b) != "token" {
		t.Fatal("value lost after passphrase change")
	}
	v4.Lock()
	if _, err := v4.Seal([]byte("x")); !errors.Is(err, ErrLocked) {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "token") || strings.Contains(string(raw), "correct horse") {
		t.Fatal("header leaks a secret")
	}
}
