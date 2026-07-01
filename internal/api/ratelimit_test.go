package api

import (
	"testing"
	"time"
)

func TestRateLimiterBlocksAfterMax(t *testing.T) {
	rl := newRateLimiter(3, time.Minute)
	key := "1.2.3.4"
	for i := 0; i < 3; i++ {
		if rl.blocked(key) {
			t.Fatalf("blocked too early at attempt %d", i)
		}
		rl.record(key)
	}
	if !rl.blocked(key) {
		t.Fatal("expected blocked after 3 failed attempts")
	}
	rl.reset(key)
	if rl.blocked(key) {
		t.Fatal("expected not blocked after reset")
	}
}
