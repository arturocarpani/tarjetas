package api

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// rateLimiter is a simple in-memory sliding-window limiter keyed by a string
// (e.g. client IP). Used to throttle login attempts. Lost on restart, which is
// fine — it only bounds brute-force velocity.
type rateLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
	max      int
	window   time.Duration
}

func newRateLimiter(max int, window time.Duration) *rateLimiter {
	return &rateLimiter{attempts: map[string][]time.Time{}, max: max, window: window}
}

// blocked reports whether key has already hit the limit within the window.
func (r *rateLimiter) blocked(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pruned(key)) >= r.max
}

// record adds a failed attempt for key.
func (r *rateLimiter) record(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts[key] = append(r.pruned(key), time.Now())
}

// reset clears attempts for key (e.g. on a successful login).
func (r *rateLimiter) reset(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.attempts, key)
}

// pruned returns key's attempts within the window (caller holds the lock).
// When nothing remains it drops the key entirely so the map doesn't accumulate
// an entry per IP that ever probed the limiter.
func (r *rateLimiter) pruned(key string) []time.Time {
	cutoff := time.Now().Add(-r.window)
	kept := r.attempts[key][:0]
	for _, t := range r.attempts[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(r.attempts, key)
		return nil
	}
	r.attempts[key] = kept
	return kept
}

// cleanup prunes expired attempts across all keys. Meant to be called
// periodically by a janitor since pruned() otherwise only runs for keys that
// are actively probed.
func (r *rateLimiter) cleanup() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key := range r.attempts {
		r.pruned(key)
	}
}

// clientIP extracts the client IP used as the rate-limit key. Behind a single
// trusted proxy (Railway) the real client address is the RIGHT-most value the
// proxy appends to X-Forwarded-For; the left-most entries are supplied by the
// client and must never be trusted, otherwise an attacker rotating a fake
// X-Forwarded-For defeats the limiter entirely. Falls back to RemoteAddr.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if last := strings.TrimSpace(parts[len(parts)-1]); last != "" {
			return last
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
