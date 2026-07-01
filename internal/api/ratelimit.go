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
func (r *rateLimiter) pruned(key string) []time.Time {
	cutoff := time.Now().Add(-r.window)
	kept := r.attempts[key][:0]
	for _, t := range r.attempts[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	r.attempts[key] = kept
	return kept
}

// clientIP extracts the client IP, honoring X-Forwarded-For (Railway/proxies)
// then falling back to RemoteAddr.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
			return first
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
