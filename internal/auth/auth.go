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

// dummyHash equalizes login timing when a user is not found (hash == ""), so an
// attacker can't enumerate usernames by measuring response time.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("timing-equalizer"), bcrypt.DefaultCost)

// VerifyPassword always runs a bcrypt comparison — against the real hash, or a
// dummy one when hash is empty — then returns whether it matched. Use this on
// the login path instead of short-circuiting on "user not found".
func VerifyPassword(hash, password string) bool {
	if hash == "" {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return false
	}
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

// DeleteByUser removes every session belonging to a user (e.g. on password
// reset or account deletion) so compromised sessions don't outlive the change.
func (s *SessionStore) DeleteByUser(userID string) {
	s.mu.Lock()
	for token, sess := range s.sessions {
		if sess.userID == userID {
			delete(s.sessions, token)
		}
	}
	s.mu.Unlock()
}

// TTL exposes the session lifetime for cookie Max-Age.
func TTL() time.Duration { return sessionTTL }
