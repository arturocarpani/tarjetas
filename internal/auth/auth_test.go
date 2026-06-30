package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tanq16/expenseowl/internal/storage"
)

func newManager(t *testing.T) (*Manager, storage.Storage) {
	t.Helper()
	s, err := storage.InitializeJsonStore(storage.SystemConfig{StorageURL: t.TempDir()})
	if err != nil {
		t.Fatalf("InitializeJsonStore: %v", err)
	}
	return NewManager([]byte("test-secret-0123456789"), s), s
}

func TestHashAndCheckPassword(t *testing.T) {
	h, err := HashPassword("s3cret!")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if h == "s3cret!" {
		t.Fatal("hash must not equal the plaintext")
	}
	if !CheckPassword(h, "s3cret!") {
		t.Fatal("CheckPassword should accept the correct password")
	}
	if CheckPassword(h, "wrong") {
		t.Fatal("CheckPassword should reject the wrong password")
	}
}

func TestSessionTokenRoundTrip(t *testing.T) {
	m, _ := newManager(t)
	tok := m.IssueToken("user-123")
	uid, ok := m.ParseToken(tok)
	if !ok || uid != "user-123" {
		t.Fatalf("ParseToken = %q, %v; want user-123, true", uid, ok)
	}
}

func TestTamperedTokenRejected(t *testing.T) {
	m, _ := newManager(t)
	tok := m.IssueToken("user-123")
	// flip the last character of the signature
	bad := tok[:len(tok)-1]
	if tok[len(tok)-1] == 'a' {
		bad += "b"
	} else {
		bad += "a"
	}
	if _, ok := m.ParseToken(bad); ok {
		t.Fatal("tampered token must be rejected")
	}
	// a token signed with a different secret must not verify
	other := NewManager([]byte("a-totally-different-secret"), nil)
	if _, ok := m.ParseToken(other.IssueToken("user-123")); ok {
		t.Fatal("token signed with a different secret must be rejected")
	}
}

func TestRequireAuthBlocksAndAllows(t *testing.T) {
	m, s := newManager(t)
	u, err := s.CreateUser(storage.User{Username: "bob", PasswordHash: "x"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	var sawUser string
	protected := m.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cu, ok := CurrentUser(r.Context())
		if !ok {
			t.Error("expected user in context")
		}
		sawUser = cu.ID
		w.WriteHeader(http.StatusOK)
	}))

	// no cookie -> 401 (non-HTML request)
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/expenses", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no session: status = %d, want 401", rec.Code)
	}

	// valid cookie -> 200, user injected
	req := httptest.NewRequest(http.MethodGet, "/expenses", nil)
	req.AddCookie(&http.Cookie{Name: m.cookieName, Value: m.IssueToken(u.ID)})
	rec = httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid session: status = %d, want 200", rec.Code)
	}
	if sawUser != u.ID {
		t.Fatalf("injected user = %q, want %q", sawUser, u.ID)
	}
}
