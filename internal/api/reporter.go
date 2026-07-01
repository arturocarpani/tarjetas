package api

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// StartMonthlyReporter launches a background loop that, on the configured
// period-start day, sends each linked Telegram user the total they spent in the
// period that just ended, and each admin a consolidated per-user breakdown. It
// persists the last-reported period to a file so a redeploy doesn't re-send.
// No-op if the bot isn't enabled.
func (h *Handler) StartMonthlyReporter() {
	if !h.TelegramEnabled() {
		return
	}
	go func() {
		// Check shortly after boot, then every 6 hours (survives redeploys and
		// doesn't depend on being up at an exact minute).
		h.maybeSendMonthlyReport()
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			h.maybeSendMonthlyReport()
		}
	}()
}

func (h *Handler) reportStateFile() string {
	if h.receiptsDir == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(h.receiptsDir), "last_report.txt")
}

func (h *Handler) maybeSendMonthlyReport() {
	startDay, err := h.storage.GetStartDate()
	if err != nil || startDay < 1 {
		startDay = 1
	}
	now := time.Now()
	curStart, prevStart := periodBounds(now, startDay)
	// Only fire on the period-start day.
	if !sameDay(now, curStart) {
		return
	}
	key := curStart.Format("2006-01-02")
	stateFile := h.reportStateFile()
	if stateFile != "" {
		if b, err := os.ReadFile(stateFile); err == nil && string(b) == key {
			return // already reported this period
		}
	}

	expenses, err := h.storage.GetAllExpenses()
	if err != nil {
		log.Printf("REPORTER ERROR: list expenses: %v\n", err)
		return
	}
	users, err := h.storage.ListUsers()
	if err != nil {
		log.Printf("REPORTER ERROR: list users: %v\n", err)
		return
	}

	// Sum per user over the just-ended period [prevStart, curStart).
	totals := map[string]float64{}
	counts := map[string]int{}
	for _, e := range expenses {
		if e.Amount < 0 && !e.Date.Before(prevStart) && e.Date.Before(curStart) {
			totals[e.UserID] += e.Amount
			counts[e.UserID]++
		}
	}

	period := fmt.Sprintf("%s al %s", prevStart.Format("02/01"), curStart.AddDate(0, 0, -1).Format("02/01"))
	nameByID := map[string]string{}
	for _, u := range users {
		nameByID[u.ID] = u.Username
	}

	for _, u := range users {
		if u.TelegramID == "" {
			continue
		}
		chatID, err := strconv.ParseInt(u.TelegramID, 10, 64)
		if err != nil {
			continue
		}
		if u.IsAdmin {
			h.reply(chatID, adminReport(period, totals, counts, nameByID))
		} else {
			h.reply(chatID, fmt.Sprintf("📊 Resumen del período (%s)\nGastos: %d\nTotal: %.2f", period, counts[u.ID], absF(totals[u.ID])))
		}
	}

	if stateFile != "" {
		if err := os.WriteFile(stateFile, []byte(key), 0644); err != nil {
			log.Printf("REPORTER ERROR: write state: %v\n", err)
		}
	}
	log.Printf("REPORTER: sent monthly report for period ending %s\n", key)
}

func adminReport(period string, totals map[string]float64, counts map[string]int, names map[string]string) string {
	var grand float64
	msg := fmt.Sprintf("📊 Resumen del período (%s) — consolidado\n", period)
	for id, total := range totals {
		grand += total
		name := names[id]
		if name == "" {
			name = "(sin dueño)"
		}
		msg += fmt.Sprintf("• %s: %.2f (%d)\n", name, absF(total), counts[id])
	}
	if len(totals) == 0 {
		msg += "Sin gastos en el período.\n"
	}
	msg += fmt.Sprintf("Total general: %.2f", absF(grand))
	return msg
}

func absF(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func sameDay(a, b time.Time) bool {
	return a.Year() == b.Year() && a.Month() == b.Month() && a.Day() == b.Day()
}

// periodBounds returns the start of the current period (the most recent
// occurrence of startDay at/before now) and the start of the previous period.
// startDay is clamped to each month's length.
func periodBounds(now time.Time, startDay int) (curStart, prevStart time.Time) {
	loc := now.Location()
	y, m := now.Year(), now.Month()
	curDay := clampDay(y, m, startDay)
	if now.Day() >= curDay {
		curStart = time.Date(y, m, curDay, 0, 0, 0, 0, loc)
	} else {
		py, pm := prevMonth(y, m)
		curStart = time.Date(py, pm, clampDay(py, pm, startDay), 0, 0, 0, 0, loc)
	}
	py, pm := prevMonth(curStart.Year(), curStart.Month())
	prevStart = time.Date(py, pm, clampDay(py, pm, startDay), 0, 0, 0, 0, loc)
	return curStart, prevStart
}

func prevMonth(y int, m time.Month) (int, time.Month) {
	if m == time.January {
		return y - 1, time.December
	}
	return y, m - 1
}

func daysInMonth(y int, m time.Month) int {
	return time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func clampDay(y int, m time.Month, day int) int {
	if dim := daysInMonth(y, m); day > dim {
		return dim
	}
	if day < 1 {
		return 1
	}
	return day
}
