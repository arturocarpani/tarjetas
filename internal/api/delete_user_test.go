package api

import (
	"testing"

	"github.com/tanq16/expenseowl/internal/auth"
	"github.com/tanq16/expenseowl/internal/storage"
)

func TestCleanupUserDataRemovesOnlyOwnersExpenses(t *testing.T) {
	s, err := storage.InitializeJsonStore(storage.SystemConfig{StorageURL: t.TempDir()})
	if err != nil {
		t.Fatalf("InitializeJsonStore: %v", err)
	}
	h := NewHandler(s, auth.NewSessionStore())
	h.SetReceiptsDir(t.TempDir())

	_ = s.AddExpense(storage.Expense{ID: "a1", UserID: "u1", Name: "x", Category: "Food", Amount: -1})
	_ = s.AddExpense(storage.Expense{ID: "a2", UserID: "u1", Name: "y", Category: "Food", Amount: -2})
	_ = s.AddExpense(storage.Expense{ID: "b1", UserID: "u2", Name: "z", Category: "Food", Amount: -3})

	h.cleanupUserData("u1")

	all, err := s.GetAllExpenses()
	if err != nil {
		t.Fatalf("GetAllExpenses: %v", err)
	}
	if len(all) != 1 || all[0].UserID != "u2" {
		t.Fatalf("expected only u2's expense to remain, got %d: %+v", len(all), all)
	}
}
