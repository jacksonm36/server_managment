// Package vault encrypts the panel's stored secrets with a data key that only
// exists in memory. The data key is stored wrapped twice: by a key derived
// from the admin's passphrase (argon2id) and by a one-time recovery key.
// Neither is stored, so a copy of the disk and the database is not enough to
// read the secrets. After every restart the vault is locked until someone
// unlocks it with the passphrase or the recovery key.
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

var (
	ErrLocked        = errors.New("the secrets vault is locked; unlock it first")
	ErrNotConfigured = errors.New("the secrets vault is not set up")
	ErrBadPassphrase = errors.New("wrong passphrase or recovery key")
)

// prefix marks values sealed by the vault (older values use the panel key).
const prefix = "vault1:"

// Header is what gets stored (in the settings table). It holds no secret.
type Header struct {
	Version         int    `json:"v"`
	Time            uint32 `json:"t"`
	MemoryKiB       uint32 `json:"m"`
	Threads         uint8  `json:"p"`
	PassSalt        []byte `json:"pass_salt"`
	WrappedPass     []byte `json:"wrapped_pass"`
	RecoverySalt    []byte `json:"recovery_salt"`
	WrappedRecovery []byte `json:"wrapped_recovery"`
}

type Vault struct {
	mu  sync.RWMutex
	hdr *Header
	dek []byte
}

// New returns a locked vault for a stored header (nil: not set up).
func New(hdr *Header) *Vault { return &Vault{hdr: hdr} }

// Adopt makes v the vault created by Create, once its header is stored.
func (v *Vault) Adopt(created *Vault) {
	created.mu.RLock()
	hdr, dek := created.hdr, created.dek
	created.mu.RUnlock()
	v.mu.Lock()
	defer v.mu.Unlock()
	v.hdr, v.dek = hdr, dek
}

func (v *Vault) Configured() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.hdr != nil
}

func (v *Vault) Unlocked() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.dek != nil
}

// MinPassphrase is the shortest passphrase accepted.
const MinPassphrase = 12

// Create sets up a new vault, unlocked. It returns the header to store and
// the recovery key to show the admin once.
func Create(passphrase string) (*Vault, *Header, string, error) {
	if len(passphrase) < MinPassphrase {
		return nil, nil, "", errors.New("the passphrase must be at least 12 characters")
	}
	dek := random(32)
	recovery := newRecoveryKey()
	h := &Header{Version: 1, Time: 3, MemoryKiB: 64 * 1024, Threads: 4}
	var err error
	if h.PassSalt, h.WrappedPass, err = wrap(h, passphrase, dek); err != nil {
		return nil, nil, "", err
	}
	if h.RecoverySalt, h.WrappedRecovery, err = wrap(h, normRecovery(recovery), dek); err != nil {
		return nil, nil, "", err
	}
	return &Vault{hdr: h, dek: dek}, h, recovery, nil
}

// Unlock accepts the passphrase or the recovery key.
func (v *Vault) Unlock(secret string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.hdr == nil {
		return ErrNotConfigured
	}
	if dek, err := unwrap(v.hdr, secret, v.hdr.PassSalt, v.hdr.WrappedPass); err == nil {
		v.dek = dek
		return nil
	}
	if dek, err := unwrap(v.hdr, normRecovery(secret), v.hdr.RecoverySalt, v.hdr.WrappedRecovery); err == nil {
		v.dek = dek
		return nil
	}
	return ErrBadPassphrase
}

func (v *Vault) Lock() {
	v.mu.Lock()
	defer v.mu.Unlock()
	clear(v.dek)
	v.dek = nil
}

// ChangePassphrase re-wraps the data key; the vault must be unlocked and
// current must be the passphrase or the recovery key. The recovery key stays
// valid. Returns the header to store.
func (v *Vault) ChangePassphrase(current, next string) (*Header, error) {
	if len(next) < MinPassphrase {
		return nil, errors.New("the passphrase must be at least 12 characters")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.hdr == nil {
		return nil, ErrNotConfigured
	}
	_, e1 := unwrap(v.hdr, current, v.hdr.PassSalt, v.hdr.WrappedPass)
	_, e2 := unwrap(v.hdr, normRecovery(current), v.hdr.RecoverySalt, v.hdr.WrappedRecovery)
	if e1 != nil && e2 != nil {
		return nil, ErrBadPassphrase
	}
	if v.dek == nil {
		return nil, ErrLocked
	}
	h := *v.hdr
	var err error
	if h.PassSalt, h.WrappedPass, err = wrap(&h, next, v.dek); err != nil {
		return nil, err
	}
	v.hdr = &h
	return &h, nil
}

// IsSealed reports whether a stored value was sealed by the vault.
func IsSealed(s string) bool { return strings.HasPrefix(s, prefix) }

func (v *Vault) Seal(plain []byte) (string, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.hdr == nil {
		return "", ErrNotConfigured
	}
	if v.dek == nil {
		return "", ErrLocked
	}
	ct, err := gcmSeal(v.dek, plain)
	if err != nil {
		return "", err
	}
	return prefix + base64.StdEncoding.EncodeToString(ct), nil
}

func (v *Vault) Open(sealed string) ([]byte, error) {
	if !IsSealed(sealed) {
		return nil, errors.New("not a vault value")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, prefix))
	if err != nil {
		return nil, err
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.dek == nil {
		return nil, ErrLocked
	}
	return gcmOpen(v.dek, raw)
}

func kek(h *Header, secret string, salt []byte) []byte {
	return argon2.IDKey([]byte(secret), salt, h.Time, h.MemoryKiB, h.Threads, 32)
}

func wrap(h *Header, secret string, dek []byte) (salt, wrapped []byte, err error) {
	salt = random(16)
	wrapped, err = gcmSeal(kek(h, secret, salt), dek)
	return salt, wrapped, err
}

func unwrap(h *Header, secret string, salt, wrapped []byte) ([]byte, error) {
	if secret == "" || len(salt) == 0 {
		return nil, ErrBadPassphrase
	}
	return gcmOpen(kek(h, secret, salt), wrapped)
}

func gcmSeal(key, plain []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := random(gcm.NonceSize())
	return gcm.Seal(nonce, nonce, plain, nil), nil
}

func gcmOpen(key, raw []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize() {
		return nil, errors.New("sealed value too short")
	}
	return gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
}

func random(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

// newRecoveryKey is 25 bytes of randomness shown as 8 groups of 5 letters.
func newRecoveryKey() string {
	s := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(random(25))
	var groups []string
	for i := 0; i < len(s); i += 5 {
		groups = append(groups, s[i:min(i+5, len(s))])
	}
	return strings.Join(groups, "-")
}

// normRecovery makes the recovery key forgiving about case, spaces and dashes.
func normRecovery(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '-' || r == ' ':
			return -1
		case r >= 'a' && r <= 'z':
			return r - 32
		}
		return r
	}, s)
}
