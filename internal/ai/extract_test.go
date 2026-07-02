package ai

import (
	"testing"
	"time"
)

func TestToExpenseNegatesAmount(t *testing.T) {
	got := toExpense(extracted{Name: "Lunch", Amount: 4500, Category: "Food"}, nil)
	if got.Amount != -4500 {
		t.Fatalf("amount = %v, want -4500 (expenses stored negative)", got.Amount)
	}
	// already-negative input should not be double-negated
	got = toExpense(extracted{Name: "Lunch", Amount: -4500, Category: "Food"}, nil)
	if got.Amount != -4500 {
		t.Fatalf("amount = %v, want -4500", got.Amount)
	}
}

func TestToExpenseDefaultsDateToToday(t *testing.T) {
	got := toExpense(extracted{Name: "X", Amount: 1, Category: "Food", Date: ""}, nil)
	if got.Date.IsZero() {
		t.Fatal("date should default to now, got zero")
	}
	if d := time.Since(got.Date); d < -time.Minute || d > time.Minute {
		t.Fatalf("default date not near now: %v", got.Date)
	}
}

func TestToExpenseParsesDate(t *testing.T) {
	got := toExpense(extracted{Name: "X", Amount: 1, Category: "Food", Date: "2026-03-15"}, nil)
	if got.Date.Year() != 2026 || got.Date.Month() != time.March || got.Date.Day() != 15 {
		t.Fatalf("parsed date = %v, want 2026-03-15", got.Date)
	}
}

func TestToExpenseDropsUnknownCard(t *testing.T) {
	got := toExpense(extracted{Name: "X", Amount: 1, Category: "Food", Card: "Amex"}, []string{"Visa", "Master"})
	if got.Card != "" {
		t.Fatalf("card = %q, want empty (unknown card dropped)", got.Card)
	}
	// a case-insensitive match is canonicalized to the configured casing so
	// downstream (case-sensitive) card filtering keeps working
	got = toExpense(extracted{Name: "X", Amount: 1, Category: "Food", Card: "visa"}, []string{"Visa", "Master"})
	if got.Card != "Visa" {
		t.Fatalf("card = %q, want \"Visa\" (canonicalized to configured casing)", got.Card)
	}
}
