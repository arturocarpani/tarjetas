package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tanq16/expenseowl/internal/auth"
	"github.com/tanq16/expenseowl/internal/storage"
)

func asUser(req *http.Request, id string) *http.Request {
	u := storage.User{ID: id, Username: id}
	return req.WithContext(context.WithValue(req.Context(), userContextKey, u))
}

func TestServeReceiptOwnershipGate(t *testing.T) {
	s, err := storage.InitializeJsonStore(storage.SystemConfig{StorageURL: t.TempDir()})
	if err != nil {
		t.Fatalf("InitializeJsonStore: %v", err)
	}
	h := NewHandler(s, auth.NewSessionStore())
	h.SetReceiptsDir(t.TempDir())

	data := []byte("\xff\xd8\xff jpeg-ish bytes")
	name, err := h.saveReceipt("e1", data, "image/jpeg")
	if err != nil {
		t.Fatalf("saveReceipt: %v", err)
	}
	if name != "e1.jpg" {
		t.Fatalf("receipt name = %q, want e1.jpg", name)
	}
	if err := s.AddExpense(storage.Expense{ID: "e1", UserID: "u1", Name: "x", Category: "Food", Amount: -1, ReceiptPath: name}); err != nil {
		t.Fatalf("AddExpense: %v", err)
	}

	// owner: 200 + bytes
	rec := httptest.NewRecorder()
	h.ServeReceipt(rec, asUser(httptest.NewRequest(http.MethodGet, "/receipt?id=e1", nil), "u1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("owner status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != string(data) {
		t.Fatalf("owner received wrong bytes")
	}

	// other non-admin: 403
	rec = httptest.NewRecorder()
	h.ServeReceipt(rec, asUser(httptest.NewRequest(http.MethodGet, "/receipt?id=e1", nil), "u2"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-owner status = %d, want 403", rec.Code)
	}

	// admin: 200
	rec = httptest.NewRecorder()
	h.ServeReceipt(rec, asAdmin(httptest.NewRequest(http.MethodGet, "/receipt?id=e1", nil)))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin status = %d, want 200", rec.Code)
	}
}
