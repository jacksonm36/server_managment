package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/jacksonm36/servermgr/panel/internal/store"
)

// argon2id parameters (OWASP recommended minimum: m=19 MiB, t=2, p=1).
const (
	argonTime    = 2
	argonMemory  = 19 * 1024
	argonThreads = 1
	argonKeyLen  = 32
)

func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

func VerifyPassword(pw, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash is verified against when the username does not exist, so login
// timing does not reveal valid usernames.
var dummyHash, _ = HashPassword("not-a-real-password")

func randomToken() (raw string, hash []byte) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	return raw, hashToken(raw)
}

func hashToken(raw string) []byte {
	h := sha256.Sum256([]byte(raw))
	return h[:]
}

const (
	sessionCookie = "sm_session"
	sessionTTL    = 12 * time.Hour
)

type ctxKey struct{}

func adminFrom(ctx context.Context) *store.Admin {
	a, _ := ctx.Value(ctxKey{}).(*store.Admin)
	return a
}

// requireAuth checks the session cookie. Mutating requests must also carry
// the X-Requested-With header, which a cross-site form post cannot set.
func (a *API) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "not logged in")
			return
		}
		admin, err := a.Store.SessionAdmin(r.Context(), hashToken(c.Value))
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "session expired")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-Requested-With") != "servermgr" {
			writeErr(w, http.StatusForbidden, "missing X-Requested-With header")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, admin)))
	})
}

// loginLimiter allows a burst of failed logins per client IP, then slows down.
type loginLimiter struct {
	mu   sync.Mutex
	fail map[string][]time.Time
}

const (
	loginWindow   = 15 * time.Minute
	loginMaxFails = 10
)

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (l *loginLimiter) allowed(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := time.Now().Add(-loginWindow)
	kept := l.fail[ip][:0]
	for _, t := range l.fail[ip] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	l.fail[ip] = kept
	return len(kept) < loginMaxFails
}

func (l *loginLimiter) failed(ip string) {
	l.mu.Lock()
	l.fail[ip] = append(l.fail[ip], time.Now())
	l.mu.Unlock()
}

func (a *API) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !a.limiter.allowed(ip) {
		a.audit(r, "", "auth.login", "", map[string]string{"error": "rate limited"}, false)
		writeErr(w, http.StatusTooManyRequests, "too many failed logins, try again later")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		OTP      string `json:"otp"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	admin, err := a.Store.AdminByUsername(r.Context(), req.Username)
	if errors.Is(err, store.ErrNotFound) {
		VerifyPassword(req.Password, dummyHash)
		a.limiter.failed(ip)
		a.audit(r, truncate(req.Username, 64), "auth.login", "", map[string]string{"error": "unknown user"}, false)
		writeErr(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	if !VerifyPassword(req.Password, admin.PasswordHash) {
		a.limiter.failed(ip)
		a.audit(r, admin.Username, "auth.login", "", map[string]string{"error": "wrong password"}, false)
		writeErr(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	if admin.TOTPEnabled {
		if strings.TrimSpace(req.OTP) == "" {
			// The password was right; ask for the second factor without
			// counting this as a failed attempt.
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "enter your two-factor code", "totp_required": true})
			return
		}
		ok, err := a.verifySecondFactor(r, admin, req.OTP)
		if err != nil {
			a.internal(w, err)
			return
		}
		if !ok {
			a.limiter.failed(ip)
			a.audit(r, admin.Username, "auth.login", "", map[string]string{"error": "wrong two-factor code"}, false)
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid or already used two-factor code", "totp_required": true})
			return
		}
	}
	raw, hash := randomToken()
	if err := a.Store.CreateSession(r.Context(), hash, admin.ID, time.Now().Add(sessionTTL)); err != nil {
		a.internal(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: raw, Path: "/", HttpOnly: true,
		Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: int(sessionTTL.Seconds()),
	})
	a.audit(r, admin.Username, "auth.login", "", nil, true)
	writeJSON(w, http.StatusOK, map[string]string{"username": admin.Username})
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (a *API) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if ad, err := a.Store.SessionAdmin(r.Context(), hashToken(c.Value)); err == nil {
			a.audit(r, ad.Username, "auth.logout", "", nil, true)
		}
		_ = a.Store.DeleteSession(r.Context(), hashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"username": adminFrom(r.Context()).Username})
}
