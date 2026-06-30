package storage

import (
	"testing"
	"time"
)

func newTestStore(t *testing.T) *jsonStore {
	t.Helper()
	s, err := InitializeJsonStore(SystemConfig{StorageURL: t.TempDir()})
	if err != nil {
		t.Fatalf("InitializeJsonStore: %v", err)
	}
	return s
}

func TestFreshConfigHasEmptyCards(t *testing.T) {
	s := newTestStore(t)
	cards, err := s.GetCards()
	if err != nil {
		t.Fatalf("GetCards: %v", err)
	}
	if len(cards) != 0 {
		t.Fatalf("expected no cards on a fresh store, got %v", cards)
	}
}

func TestUpdateAndGetCards(t *testing.T) {
	s := newTestStore(t)
	want := []string{"Visa", "Amex"}
	if err := s.UpdateCards(want); err != nil {
		t.Fatalf("UpdateCards: %v", err)
	}
	got, err := s.GetCards()
	if err != nil {
		t.Fatalf("GetCards: %v", err)
	}
	if len(got) != 2 || got[0] != "Visa" || got[1] != "Amex" {
		t.Fatalf("GetCards = %v, want %v", got, want)
	}
	// The card list must also be reflected in the full config payload.
	cfg, err := s.GetConfig()
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if len(cfg.Cards) != 2 {
		t.Fatalf("config.Cards = %v, want 2 entries", cfg.Cards)
	}
}

func TestExpenseCardRoundTrip(t *testing.T) {
	s := newTestStore(t)
	exp := Expense{Name: "Dinner", Category: "Food", Amount: -20, Card: "Visa", Date: time.Now()}
	if err := s.AddExpense(exp); err != nil {
		t.Fatalf("AddExpense: %v", err)
	}
	all, err := s.GetAllExpenses()
	if err != nil {
		t.Fatalf("GetAllExpenses: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 expense, got %d", len(all))
	}
	if all[0].Card != "Visa" {
		t.Fatalf("expense Card = %q, want %q", all[0].Card, "Visa")
	}
}

func TestRecurringExpenseCardPropagates(t *testing.T) {
	s := newTestStore(t)
	re := RecurringExpense{
		Name:        "Netflix",
		Category:    "Entertainment",
		Amount:      -10,
		Card:        "Amex",
		StartDate:   time.Now().AddDate(0, -2, 0),
		Interval:    "monthly",
		Occurrences: 3,
	}
	if err := s.AddRecurringExpense(re); err != nil {
		t.Fatalf("AddRecurringExpense: %v", err)
	}
	res, err := s.GetRecurringExpenses()
	if err != nil {
		t.Fatalf("GetRecurringExpenses: %v", err)
	}
	if len(res) != 1 || res[0].Card != "Amex" {
		t.Fatalf("recurring Card = %v, want Amex", res)
	}
	all, err := s.GetAllExpenses()
	if err != nil {
		t.Fatalf("GetAllExpenses: %v", err)
	}
	if len(all) == 0 {
		t.Fatalf("expected generated expense instances, got none")
	}
	for _, e := range all {
		if e.Card != "Amex" {
			t.Fatalf("generated expense Card = %q, want Amex", e.Card)
		}
	}
}
