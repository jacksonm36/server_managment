package api

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jacksonm36/servermgr/panel/internal/store"
)

// ---- secret sealing (TOTP secrets at rest) ----

// LoadOrCreateKey returns the 32-byte panel secret key, creating it (0600) on
// first start. It encrypts TOTP secrets in the database, so a database dump
// alone does not reveal them.
func LoadOrCreateKey(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		if len(b) != 32 {
			return nil, fmt.Errorf("%s: expected 32 bytes, got %d", path, len(b))
		}
		return b, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	b = make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return nil, err
	}
	return b, nil
}

func (a *API) seal(plain []byte) (string, error) {
	block, err := aes.NewCipher(a.SecretKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, plain, nil)), nil
}

func (a *API) open(sealed string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(a.SecretKey)
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

// ---- RFC 6238 TOTP (SHA-1, 6 digits, 30 s) ----

const totpPeriod = 30

func hotp(secret []byte, counter uint64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	m := hmac.New(sha1.New, secret)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", code%1_000_000)
}

// matchTOTP returns the matching time step within ±1 step of now, or -1.
func matchTOTP(secret []byte, code string, now time.Time) int64 {
	step := now.Unix() / totpPeriod
	for d := int64(-1); d <= 1; d++ {
		if subtle.ConstantTimeCompare([]byte(hotp(secret, uint64(step+d))), []byte(code)) == 1 {
			return step + d
		}
	}
	return -1
}

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// normRecovery lowercases a recovery code and drops separators.
func normRecovery(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' {
			return -1
		}
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		return r
	}, s)
}

func hashRecovery(code string) []byte {
	h := sha256.Sum256([]byte("servermgr-recovery:" + normRecovery(code)))
	return h[:]
}

func newRecoveryCodes(n int) (codes []string, hashes [][]byte) {
	enc := base32.StdEncoding.WithPadding(base32.NoPadding)
	for range n {
		b := make([]byte, 7)
		if _, err := rand.Read(b); err != nil {
			panic(err)
		}
		c := strings.ToLower(enc.EncodeToString(b))[:10]
		codes = append(codes, c[:5]+"-"+c[5:])
		hashes = append(hashes, hashRecovery(c))
	}
	return codes, hashes
}

// verifySecondFactor checks a TOTP code (single use per time step) or a
// one-time recovery code.
func (a *API) verifySecondFactor(r *http.Request, admin *store.Admin, code string) (bool, error) {
	code = strings.TrimSpace(code)
	if isDigits(code) && len(code) == 6 {
		secret, err := a.open(admin.TOTPSecret)
		if err != nil {
			return false, err
		}
		step := matchTOTP(secret, code, time.Now())
		if step < 0 {
			return false, nil
		}
		// Refuses a step that was already used (replay of a sniffed code).
		return a.Store.UseTOTPStep(r.Context(), admin.ID, step)
	}
	if len(normRecovery(code)) == 10 {
		ok, err := a.Store.UseRecoveryCode(r.Context(), admin.ID, hashRecovery(code))
		if ok {
			a.audit(r, admin.Username, "auth.recovery_code_used", "", nil, true)
		}
		return ok, err
	}
	return false, nil
}

// ---- account endpoints ----

func (a *API) handleAccount(w http.ResponseWriter, r *http.Request) {
	ad := adminFrom(r.Context())
	left, err := a.Store.RecoveryCodesLeft(r.Context(), ad.ID)
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"username": ad.Username, "totp_enabled": ad.TOTPEnabled, "recovery_codes_left": left,
	})
}

// reauth checks the current password for sensitive account changes.
func (a *API) reauth(w http.ResponseWriter, r *http.Request, password string) bool {
	ad := adminFrom(r.Context())
	ip := clientIP(r)
	if !a.limiter.allowed(ip) {
		writeErr(w, http.StatusTooManyRequests, "too many failed attempts, try again later")
		return false
	}
	if !VerifyPassword(password, ad.PasswordHash) {
		a.limiter.failed(ip)
		writeErr(w, http.StatusForbidden, "current password is wrong")
		return false
	}
	return true
}

func (a *API) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) || !a.reauth(w, r, req.Password) {
		return
	}
	ad := adminFrom(r.Context())
	if ad.TOTPEnabled {
		writeErr(w, http.StatusConflict, "two-factor authentication is already enabled")
		return
	}
	secret := make([]byte, 20)
	if _, err := rand.Read(secret); err != nil {
		a.internal(w, err)
		return
	}
	sealed, err := a.seal(secret)
	if err != nil {
		a.internal(w, err)
		return
	}
	if err := a.Store.SetTOTPPending(r.Context(), ad.ID, sealed); err != nil {
		a.internal(w, err)
		return
	}
	enc := b32.EncodeToString(secret)
	issuer := "servermgr"
	uri := fmt.Sprintf("otpauth://totp/%s:%s?secret=%s&issuer=%s&algorithm=SHA1&digits=6&period=%d",
		url.PathEscape(issuer), url.PathEscape(ad.Username), enc, url.QueryEscape(issuer), totpPeriod)
	writeJSON(w, http.StatusOK, map[string]string{"secret": enc, "otpauth_uri": uri})
}

func (a *API) handleTOTPEnable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	ad := adminFrom(r.Context())
	if ad.TOTPPending == "" {
		writeErr(w, http.StatusBadRequest, "start setup first")
		return
	}
	secret, err := a.open(ad.TOTPPending)
	if err != nil {
		a.internal(w, err)
		return
	}
	step := matchTOTP(secret, strings.TrimSpace(req.Code), time.Now())
	if step < 0 {
		writeErr(w, http.StatusBadRequest, "that code is not valid; check your device's clock and try again")
		return
	}
	codes, hashes := newRecoveryCodes(10)
	if err := a.Store.EnableTOTP(r.Context(), ad.ID, step, hashes); err != nil {
		a.internal(w, err)
		return
	}
	a.audit(r, "", "auth.totp_enabled", ad.Username, nil, true)
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

func (a *API) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !readJSON(w, r, &req) || !a.reauth(w, r, req.Password) {
		return
	}
	ad := adminFrom(r.Context())
	if !ad.TOTPEnabled {
		writeErr(w, http.StatusBadRequest, "two-factor authentication is not enabled")
		return
	}
	ok, err := a.verifySecondFactor(r, ad, req.Code)
	if err != nil {
		a.internal(w, err)
		return
	}
	if !ok {
		a.limiter.failed(clientIP(r))
		writeErr(w, http.StatusForbidden, "invalid two-factor code")
		return
	}
	if err := a.Store.DisableTOTP(r.Context(), ad.ID); err != nil {
		a.internal(w, err)
		return
	}
	a.audit(r, "", "auth.totp_disabled", ad.Username, nil, true)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleRecoveryRegenerate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) || !a.reauth(w, r, req.Password) {
		return
	}
	ad := adminFrom(r.Context())
	if !ad.TOTPEnabled {
		writeErr(w, http.StatusBadRequest, "two-factor authentication is not enabled")
		return
	}
	codes, hashes := newRecoveryCodes(10)
	if err := a.Store.ReplaceRecoveryCodes(r.Context(), ad.ID, hashes); err != nil {
		a.internal(w, err)
		return
	}
	a.audit(r, "", "auth.recovery_codes_regenerated", ad.Username, nil, true)
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

func (a *API) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !readJSON(w, r, &req) || !a.reauth(w, r, req.Current) {
		return
	}
	if len(req.New) < 12 || len(req.New) > 256 {
		writeErr(w, http.StatusBadRequest, "new password must be 12-256 characters")
		return
	}
	hash, err := HashPassword(req.New)
	if err != nil {
		a.internal(w, err)
		return
	}
	ad := adminFrom(r.Context())
	c, _ := r.Cookie(sessionCookie)
	if err := a.Store.UpdatePassword(r.Context(), ad.ID, hash, hashToken(c.Value)); err != nil {
		a.internal(w, err)
		return
	}
	a.audit(r, "", "auth.password_changed", ad.Username, map[string]string{"note": "other sessions signed out"}, true)
	w.WriteHeader(http.StatusNoContent)
}
