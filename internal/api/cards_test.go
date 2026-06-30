package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tanq16/expenseowl/internal/auth"
	"github.com/tanq16/expenseowl/internal/storage"
)

func newCardHandler(t *testing.T) (*Handler, string) {
	t.Helper()
	s, err := storage.InitializeJsonStore(storage.SystemConfig{StorageURL: t.TempDir()})
	if err != nil {
		t.Fatalf("InitializeJsonStore: %v", err)
	}
	h := NewHandler(s, auth.NewManager([]byte("test-secret"), s), nil)
	u, err := s.CreateUser(storage.User{Username: "tester", PasswordHash: "x"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return h, u.ID
}

// authReq builds a request that already carries an authenticated user in its
// context, as the RequireAuth middleware would.
func authReq(method, target string, body []byte, uid string) *http.Request {
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, target, bytes.NewReader(body))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	return r.WithContext(auth.WithUser(r.Context(), storage.User{ID: uid}))
}

func TestGetCardsHandlerReturnsCards(t *testing.T) {
	h, uid := newCardHandler(t)
	if err := h.storage.UpdateCards(uid, []string{"Visa", "Master"}); err != nil {
		t.Fatalf("seed UpdateCards: %v", err)
	}
	rec := httptest.NewRecorder()
	h.GetCards(rec, authReq(http.MethodGet, "/cards", nil, uid))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var got []string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}
	if len(got) != 2 || got[0] != "Visa" || got[1] != "Master" {
		t.Fatalf("got %v, want [Visa Master]", got)
	}
}

func TestUpdateCardsHandlerPersists(t *testing.T) {
	h, uid := newCardHandler(t)
	body, _ := json.Marshal([]string{"Visa", "Cash"})
	rec := httptest.NewRecorder()
	h.UpdateCards(rec, authReq(http.MethodPut, "/cards/edit", body, uid))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	cards, err := h.storage.GetCards(uid)
	if err != nil {
		t.Fatalf("GetCards: %v", err)
	}
	if len(cards) != 2 || cards[0] != "Visa" || cards[1] != "Cash" {
		t.Fatalf("persisted cards = %v, want [Visa Cash]", cards)
	}
}

func TestAddExpenseAppliesDateDefaultBeforeValidate(t *testing.T) {
	// M4 regression guard: an expense with no date must succeed (default to now),
	// not be rejected by Validate.
	h, uid := newCardHandler(t)
	body := []byte(`{"name":"Coffee","category":"Food","amount":-3.5}`)
	rec := httptest.NewRecorder()
	h.AddExpense(rec, authReq(http.MethodPut, "/expense", body, uid))
	if rec.Code != http.StatusOK {
		t.Fatalf("AddExpense without date: status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	all, _ := h.storage.GetAllExpenses(uid)
	if len(all) != 1 || all[0].Date.IsZero() {
		t.Fatalf("expense should be stored with a defaulted date, got %+v", all)
	}
}
