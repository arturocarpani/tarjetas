package storage

import (
	"errors"
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

func newUser(t *testing.T, s Storage, username string) string {
	t.Helper()
	u, err := s.CreateUser(User{Username: username, PasswordHash: "hash"})
	if err != nil {
		t.Fatalf("CreateUser(%s): %v", username, err)
	}
	return u.ID
}

func TestUserCRUD(t *testing.T) {
	s := newTestStore(t)
	if n, _ := s.CountUsers(); n != 0 {
		t.Fatalf("fresh store should have 0 users, got %d", n)
	}
	u, err := s.CreateUser(User{Username: "alice", PasswordHash: "h", IsAdmin: true})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.ID == "" {
		t.Fatal("expected a generated user ID")
	}
	// case-insensitive username lookup
	got, err := s.GetUserByUsername("ALICE")
	if err != nil || got.ID != u.ID {
		t.Fatalf("GetUserByUsername = %+v, %v", got, err)
	}
	// duplicate username rejected
	if _, err := s.CreateUser(User{Username: "alice", PasswordHash: "h2"}); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("duplicate username: want ErrUsernameTaken, got %v", err)
	}
	// missing user
	if _, err := s.GetUserByID("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetUserByID(missing): want ErrNotFound, got %v", err)
	}
	// telegram link round-trip
	u.TelegramChatID = 4242
	if err := s.UpdateUser(u); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	tg, err := s.GetUserByTelegramChatID(4242)
	if err != nil || tg.ID != u.ID {
		t.Fatalf("GetUserByTelegramChatID = %+v, %v", tg, err)
	}
	// delete
	if err := s.DeleteUser(u.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if n, _ := s.CountUsers(); n != 0 {
		t.Fatalf("after delete want 0 users, got %d", n)
	}
}

func TestFreshUserHasDefaults(t *testing.T) {
	s := newTestStore(t)
	uid := newUser(t, s, "u")
	cfg, err := s.GetConfig(uid)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if cfg.Currency != "usd" || cfg.StartDate != 1 {
		t.Fatalf("defaults wrong: currency=%q startDate=%d", cfg.Currency, cfg.StartDate)
	}
	if len(cfg.Categories) == 0 {
		t.Fatalf("expected default categories")
	}
	cards, _ := s.GetCards(uid)
	if len(cards) != 0 {
		t.Fatalf("expected no cards on a fresh user, got %v", cards)
	}
}

func TestPerUserIsolation(t *testing.T) {
	s := newTestStore(t)
	a := newUser(t, s, "a")
	b := newUser(t, s, "b")

	if err := s.AddExpense(a, Expense{Name: "Coffee", Category: "Food", Amount: -3, Date: time.Now()}); err != nil {
		t.Fatalf("AddExpense(a): %v", err)
	}
	if err := s.UpdateCards(a, []string{"Visa"}); err != nil {
		t.Fatalf("UpdateCards(a): %v", err)
	}

	ax, _ := s.GetAllExpenses(a)
	bx, _ := s.GetAllExpenses(b)
	if len(ax) != 1 {
		t.Fatalf("user a should have 1 expense, got %d", len(ax))
	}
	if len(bx) != 0 {
		t.Fatalf("user b must NOT see user a's expenses, got %d", len(bx))
	}
	ac, _ := s.GetCards(a)
	bc, _ := s.GetCards(b)
	if len(ac) != 1 || len(bc) != 0 {
		t.Fatalf("cards not isolated: a=%v b=%v", ac, bc)
	}
}

func TestUpdateAndGetCards(t *testing.T) {
	s := newTestStore(t)
	uid := newUser(t, s, "u")
	want := []string{"Visa", "Amex"}
	if err := s.UpdateCards(uid, want); err != nil {
		t.Fatalf("UpdateCards: %v", err)
	}
	got, err := s.GetCards(uid)
	if err != nil {
		t.Fatalf("GetCards: %v", err)
	}
	if len(got) != 2 || got[0] != "Visa" || got[1] != "Amex" {
		t.Fatalf("GetCards = %v, want %v", got, want)
	}
}

func TestExpenseCardRoundTrip(t *testing.T) {
	s := newTestStore(t)
	uid := newUser(t, s, "u")
	if err := s.AddExpense(uid, Expense{Name: "Dinner", Category: "Food", Amount: -20, Card: "Visa", Date: time.Now()}); err != nil {
		t.Fatalf("AddExpense: %v", err)
	}
	all, err := s.GetAllExpenses(uid)
	if err != nil {
		t.Fatalf("GetAllExpenses: %v", err)
	}
	if len(all) != 1 || all[0].Card != "Visa" {
		t.Fatalf("expense Card round-trip failed: %+v", all)
	}
}

func TestExpenseCurrencyDefaultsToUserConfig(t *testing.T) {
	s := newTestStore(t)
	uid := newUser(t, s, "u")
	if err := s.AddExpense(uid, Expense{Name: "x", Category: "Food", Amount: -1, Date: time.Now()}); err != nil {
		t.Fatalf("AddExpense: %v", err)
	}
	all, _ := s.GetAllExpenses(uid)
	if all[0].Currency != "usd" {
		t.Fatalf("empty currency should default to user config 'usd', got %q", all[0].Currency)
	}
}

func TestRecurringExpenseCardPropagates(t *testing.T) {
	s := newTestStore(t)
	uid := newUser(t, s, "u")
	re := RecurringExpense{
		Name:        "Netflix",
		Category:    "Entertainment",
		Amount:      -10,
		Card:        "Amex",
		StartDate:   time.Now().AddDate(0, -2, 0),
		Interval:    "monthly",
		Occurrences: 3,
	}
	if err := s.AddRecurringExpense(uid, re); err != nil {
		t.Fatalf("AddRecurringExpense: %v", err)
	}
	res, err := s.GetRecurringExpenses(uid)
	if err != nil || len(res) != 1 || res[0].Card != "Amex" {
		t.Fatalf("recurring round-trip failed: %+v, %v", res, err)
	}
	all, err := s.GetAllExpenses(uid)
	if err != nil || len(all) == 0 {
		t.Fatalf("expected generated instances, got %d (%v)", len(all), err)
	}
	for _, e := range all {
		if e.Card != "Amex" {
			t.Fatalf("generated expense Card = %q, want Amex", e.Card)
		}
	}
}

func TestExpenseValidateCurrency(t *testing.T) {
	// supported code is normalized to lowercase
	e := Expense{Name: "x", Category: "Food", Amount: -1, Date: time.Now(), Currency: "USD"}
	if err := e.Validate(); err != nil {
		t.Fatalf("USD should be accepted: %v", err)
	}
	if e.Currency != "usd" {
		t.Fatalf("currency should be lowercased to 'usd', got %q", e.Currency)
	}
	// unsupported code is rejected
	bad := Expense{Name: "x", Category: "Food", Amount: -1, Date: time.Now(), Currency: "zzz"}
	if err := bad.Validate(); err == nil {
		t.Fatal("unsupported currency 'zzz' should be rejected")
	}
}
