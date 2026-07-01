package auth

import "testing"

func TestVerifyPassword(t *testing.T) {
	hash, err := HashPassword("s3cret")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !VerifyPassword(hash, "s3cret") {
		t.Fatal("expected match for correct password")
	}
	if VerifyPassword(hash, "wrong") {
		t.Fatal("expected no match for wrong password")
	}
	// empty hash (unknown user) must return false without panicking
	if VerifyPassword("", "anything") {
		t.Fatal("expected false for empty hash")
	}
}

func TestDeleteByUser(t *testing.T) {
	s := NewSessionStore()
	t1, _ := s.Create("u1")
	t2, _ := s.Create("u1")
	t3, _ := s.Create("u2")
	s.DeleteByUser("u1")
	if _, ok := s.Get(t1); ok {
		t.Fatal("u1 session 1 should be gone")
	}
	if _, ok := s.Get(t2); ok {
		t.Fatal("u1 session 2 should be gone")
	}
	if _, ok := s.Get(t3); !ok {
		t.Fatal("u2 session should remain")
	}
}
