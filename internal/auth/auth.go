// Package auth provides password hashing and signed cookie sessions for
// ExpenseOwl's multiuser mode. Sessions are stateless: the cookie carries
// "<userID>|<expiry>" signed with HMAC-SHA256 over a server secret.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tanq16/expenseowl/internal/storage"
	"golang.org/x/crypto/bcrypt"
)

type contextKey string

const userCtxKey contextKey = "expenseowl.user"

const defaultCookieName = "eo_session"

// Manager issues and verifies sessions and guards routes.
type Manager struct {
	secret     []byte
	store      storage.Storage
	cookieName string
	ttl        time.Duration
	Secure     bool // set Secure flag on the cookie (true when served over HTTPS)
}

// NewManager builds a session manager. secret must be non-empty and stable
// across restarts (otherwise all sessions are invalidated).
func NewManager(secret []byte, store storage.Storage) *Manager {
	return &Manager{
		secret:     secret,
		store:      store,
		cookieName: defaultCookieName,
		ttl:        30 * 24 * time.Hour,
	}
}

// HashPassword returns a bcrypt hash of pw.
func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

// CheckPassword reports whether pw matches the bcrypt hash.
func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

func (m *Manager) sign(payload string) string {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// IssueToken returns a signed session token for userID.
func (m *Manager) IssueToken(userID string) string {
	payload := userID + "|" + strconv.FormatInt(time.Now().Add(m.ttl).Unix(), 10)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + m.sign(payload)
}

// ParseToken validates a token and returns the userID if the signature is valid
// and the token has not expired.
func (m *Manager) ParseToken(token string) (string, bool) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return "", false
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", false
	}
	payload := string(payloadBytes)
	if !hmac.Equal([]byte(m.sign(payload)), []byte(parts[1])) {
		return "", false
	}
	seg := strings.SplitN(payload, "|", 2)
	if len(seg) != 2 {
		return "", false
	}
	exp, err := strconv.ParseInt(seg[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", false
	}
	return seg[0], true
}

// SetSessionCookie writes the session cookie for userID.
func (m *Manager) SetSessionCookie(w http.ResponseWriter, userID string) {
	http.SetCookie(w, &http.Cookie{
		Name:     m.cookieName,
		Value:    m.IssueToken(userID),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   m.Secure,
		Expires:  time.Now().Add(m.ttl),
		MaxAge:   int(m.ttl / time.Second),
	})
}

// ClearSessionCookie expires the session cookie.
func (m *Manager) ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     m.cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   m.Secure,
		MaxAge:   -1,
	})
}

// userFromRequest resolves the authenticated user from the request cookie.
func (m *Manager) userFromRequest(r *http.Request) (storage.User, bool) {
	c, err := r.Cookie(m.cookieName)
	if err != nil {
		return storage.User{}, false
	}
	uid, ok := m.ParseToken(c.Value)
	if !ok {
		return storage.User{}, false
	}
	u, err := m.store.GetUserByID(uid)
	if err != nil {
		return storage.User{}, false
	}
	return u, true
}

// RequireAuth wraps next, allowing only authenticated requests through and
// injecting the user into the request context. Unauthenticated HTML GETs are
// redirected to /login; everything else gets a 401 JSON response.
func (m *Manager) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := m.userFromRequest(r)
		if !ok {
			if r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html") {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), u)))
	})
}

// WithUser returns a context carrying u.
func WithUser(ctx context.Context, u storage.User) context.Context {
	return context.WithValue(ctx, userCtxKey, u)
}

// CurrentUser extracts the authenticated user from the context.
func CurrentUser(ctx context.Context) (storage.User, bool) {
	u, ok := ctx.Value(userCtxKey).(storage.User)
	return u, ok
}
