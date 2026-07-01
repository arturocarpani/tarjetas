package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tanq16/expenseowl/internal/auth"
	"github.com/tanq16/expenseowl/internal/storage"
)

func newCardHandler(t *testing.T) *Handler {
	t.Helper()
	s, err := storage.InitializeJsonStore(storage.SystemConfig{StorageURL: t.TempDir()})
	if err != nil {
		t.Fatalf("InitializeJsonStore: %v", err)
	}
	return NewHandler(s, auth.NewSessionStore())
}

// asAdmin returns a copy of req carrying an admin user in its context, as the
// auth middleware would inject for an authenticated admin.
func asAdmin(req *http.Request) *http.Request {
	admin := storage.User{ID: "admin-id", Username: "admin", IsAdmin: true}
	return req.WithContext(context.WithValue(req.Context(), userContextKey, admin))
}

func TestGetCardsHandlerReturnsCards(t *testing.T) {
	h := newCardHandler(t)
	if err := h.storage.UpdateCards([]string{"Visa", "Master"}); err != nil {
		t.Fatalf("seed UpdateCards: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/cards", nil)
	rec := httptest.NewRecorder()
	h.GetCards(rec, req)
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
	h := newCardHandler(t)
	body, _ := json.Marshal([]string{"Visa", "Cash"})
	req := asAdmin(httptest.NewRequest(http.MethodPut, "/cards/edit", bytes.NewReader(body)))
	rec := httptest.NewRecorder()
	h.UpdateCards(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	cards, err := h.storage.GetCards()
	if err != nil {
		t.Fatalf("GetCards: %v", err)
	}
	if len(cards) != 2 || cards[0] != "Visa" || cards[1] != "Cash" {
		t.Fatalf("persisted cards = %v, want [Visa Cash]", cards)
	}
}
