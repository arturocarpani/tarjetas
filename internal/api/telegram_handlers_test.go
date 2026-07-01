package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tanq16/expenseowl/internal/ai"
	"github.com/tanq16/expenseowl/internal/auth"
	"github.com/tanq16/expenseowl/internal/storage"
	"github.com/tanq16/expenseowl/internal/telegram"
)

func newTelegramHandler(t *testing.T, secret string) *Handler {
	t.Helper()
	s, err := storage.InitializeJsonStore(storage.SystemConfig{StorageURL: t.TempDir()})
	if err != nil {
		t.Fatalf("InitializeJsonStore: %v", err)
	}
	h := NewHandler(s, auth.NewSessionStore())
	h.EnableTelegram(telegram.NewClient("dummy-token"), ai.NewExtractor(), secret)
	return h
}

func TestTelegramWebhookDisabledReturns404(t *testing.T) {
	s, _ := storage.InitializeJsonStore(storage.SystemConfig{StorageURL: t.TempDir()})
	h := NewHandler(s, auth.NewSessionStore())
	req := httptest.NewRequest(http.MethodPost, "/telegram/webhook", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	h.TelegramWebhook(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when bot not enabled", rec.Code)
	}
}

func TestTelegramWebhookRejectsBadSecret(t *testing.T) {
	h := newTelegramHandler(t, "expected-secret")
	req := httptest.NewRequest(http.MethodPost, "/telegram/webhook", strings.NewReader("{}"))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "wrong-secret")
	rec := httptest.NewRecorder()
	h.TelegramWebhook(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 on bad secret", rec.Code)
	}
}

func TestTelegramWebhookAcceptsValidSecret(t *testing.T) {
	h := newTelegramHandler(t, "expected-secret")
	// empty message → processUpdate returns immediately, no outbound network call
	req := httptest.NewRequest(http.MethodPost, "/telegram/webhook", strings.NewReader(`{"update_id":1}`))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "expected-secret")
	rec := httptest.NewRecorder()
	h.TelegramWebhook(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with valid secret", rec.Code)
	}
}

func TestTelegramWebhookRejectsEmptySecret(t *testing.T) {
	// An empty configured secret must reject everything (the route is public).
	h := newTelegramHandler(t, "")
	req := httptest.NewRequest(http.MethodPost, "/telegram/webhook", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	h.TelegramWebhook(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 when no secret is configured", rec.Code)
	}
}

func TestGetUserByTelegramIDRoundTrip(t *testing.T) {
	s, err := storage.InitializeJsonStore(storage.SystemConfig{StorageURL: t.TempDir()})
	if err != nil {
		t.Fatalf("InitializeJsonStore: %v", err)
	}
	if err := s.CreateUser(storage.User{ID: "u1", Username: "ana"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := s.UpdateUserTelegramID("u1", "123456"); err != nil {
		t.Fatalf("UpdateUserTelegramID: %v", err)
	}
	got, err := s.GetUserByTelegramID("123456")
	if err != nil {
		t.Fatalf("GetUserByTelegramID: %v", err)
	}
	if got.ID != "u1" {
		t.Fatalf("got user %q, want u1", got.ID)
	}
	// a second user cannot claim the same Telegram ID
	if err := s.CreateUser(storage.User{ID: "u2", Username: "beto"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := s.UpdateUserTelegramID("u2", "123456"); err == nil {
		t.Fatal("expected error linking a Telegram ID already used by another user")
	}
}
