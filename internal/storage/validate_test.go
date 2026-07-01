package storage

import (
	"strings"
	"testing"
	"time"
)

func TestExpenseValidateSanitizesCategory(t *testing.T) {
	e := Expense{Name: "x", Category: `<img src=x onerror=alert(1)>`, Amount: -1, Date: time.Now()}
	if err := e.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if strings.ContainsAny(e.Category, "<>\"") {
		t.Fatalf("category not sanitized (XSS risk): %q", e.Category)
	}
}

func TestRecurringValidateSanitizesCategory(t *testing.T) {
	r := RecurringExpense{Name: "x", Category: `<script>`, Amount: -1, Occurrences: 3, StartDate: time.Now(), Interval: "monthly"}
	if err := r.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if strings.ContainsAny(r.Category, "<>") {
		t.Fatalf("recurring category not sanitized: %q", r.Category)
	}
}

func TestWriteFileAtomicRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/data.json"
	if err := writeFileAtomic(path, []byte("hello"), 0644); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}
	// overwrite should replace cleanly
	if err := writeFileAtomic(path, []byte("world"), 0644); err != nil {
		t.Fatalf("writeFileAtomic overwrite: %v", err)
	}
}
