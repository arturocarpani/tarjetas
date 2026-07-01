package api

import (
	"testing"
	"time"
)

func d(y int, m time.Month, day int) time.Time {
	return time.Date(y, m, day, 0, 0, 0, 0, time.UTC)
}

func TestPeriodBounds(t *testing.T) {
	cases := []struct {
		name          string
		now           time.Time
		startDay      int
		wantCur, wantPrev time.Time
	}{
		{"start1 midmonth", d(2026, 7, 15), 1, d(2026, 7, 1), d(2026, 6, 1)},
		{"start1 onday", d(2026, 7, 1), 1, d(2026, 7, 1), d(2026, 6, 1)},
		{"start15 before", d(2026, 7, 10), 15, d(2026, 6, 15), d(2026, 5, 15)},
		{"start15 after", d(2026, 7, 20), 15, d(2026, 7, 15), d(2026, 6, 15)},
		{"start31 feb clamps", d(2026, 2, 15), 31, d(2026, 1, 31), d(2025, 12, 31)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cur, prev := periodBounds(c.now, c.startDay)
			if !cur.Equal(c.wantCur) {
				t.Errorf("curStart = %s, want %s", cur.Format("2006-01-02"), c.wantCur.Format("2006-01-02"))
			}
			if !prev.Equal(c.wantPrev) {
				t.Errorf("prevStart = %s, want %s", prev.Format("2006-01-02"), c.wantPrev.Format("2006-01-02"))
			}
		})
	}
}
