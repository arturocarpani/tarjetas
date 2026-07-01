package api

import (
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tanq16/expenseowl/internal/auth"
	"github.com/tanq16/expenseowl/internal/storage"
)

func TestExportCSVDateFilterAndScope(t *testing.T) {
	s, err := storage.InitializeJsonStore(storage.SystemConfig{StorageURL: t.TempDir()})
	if err != nil {
		t.Fatalf("InitializeJsonStore: %v", err)
	}
	h := NewHandler(s, auth.NewSessionStore())

	mk := func(id, user string, day int) storage.Expense {
		return storage.Expense{ID: id, UserID: user, Name: "n", Category: "Food", Amount: -1,
			Date: time.Date(2026, 3, day, 12, 0, 0, 0, time.UTC)}
	}
	_ = s.AddExpense(mk("a", "u1", 5))
	_ = s.AddExpense(mk("b", "u1", 15))
	_ = s.AddExpense(mk("c", "u2", 15))

	// admin, date window 10..20 → only b and c
	rec := httptest.NewRecorder()
	req := asAdmin(httptest.NewRequest(http.MethodGet, "/export/csv?from=2026-03-10&to=2026-03-20", nil))
	h.ExportCSV(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	rows, _ := csv.NewReader(rec.Body).ReadAll()
	if len(rows) != 3 { // header + 2
		t.Fatalf("admin filtered rows = %d, want 3 (header+2)", len(rows))
	}

	// non-admin u1 → only their own (a and b), ignoring the other user
	rec = httptest.NewRecorder()
	req = asUser(httptest.NewRequest(http.MethodGet, "/export/csv", nil), "u1")
	h.ExportCSV(rec, req)
	rows, _ = csv.NewReader(rec.Body).ReadAll()
	if len(rows) != 3 { // header + a + b
		t.Fatalf("u1 rows = %d, want 3 (header+2 own)", len(rows))
	}
}
