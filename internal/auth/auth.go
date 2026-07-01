package auth

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// HashPassword returns a bcrypt hash of the given plaintext password.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// CheckPassword reports whether the plaintext password matches the hash.
func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// sessionTTL is how long a session cookie stays valid.
const sessionTTL = 7 * 24 * time.Hour

type session struct {
	userID  string
	expires time.Time
}

// SessionStore is an in-memory token -> userID store. Sessions are lost on
// restart (acceptable for a single-binary homelab/Railway deployment).
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]session
}

func NewSessionStore() *SessionStore {
	return &SessionStore{sessions: map[string]session{}}
}

// Create issues a new opaque session token bound to the given user.
func (s *SessionStore) Create(userID string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	s.mu.Lock()
	s.sessions[token] = session{userID: userID, expires: time.Now().Add(sessionTTL)}
	s.mu.Unlock()
	return token, nil
}

// Get returns the user ID for a token, or ("", false) if missing/expired.
func (s *SessionStore) Get(token string) (string, bool) {
	s.mu.RLock()
	sess, ok := s.sessions[token]
	s.mu.RUnlock()
	if !ok {
		return "", false
	}
	if time.Now().After(sess.expires) {
		s.Delete(token)
		return "", false
	}
	return sess.userID, true
}

// Delete removes a session (logout).
func (s *SessionStore) Delete(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

// TTL exposes the session lifetime for cookie Max-Age.
func TTL() time.Duration { return sessionTTL }
