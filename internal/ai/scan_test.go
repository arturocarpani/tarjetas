package ai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestScanReceiptParsesToolUse(t *testing.T) {
	var gotAuth, gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		// confirm the request actually carries an image + forced tool_choice
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"type":"base64"`) {
			t.Errorf("request missing base64 image block: %s", body)
		}
		if !strings.Contains(string(body), `"record_expense"`) {
			t.Errorf("request missing forced tool: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"stop_reason":"tool_use",
			"content":[
				{"type":"text","text":"ok"},
				{"type":"tool_use","name":"record_expense","input":{
					"name":"Almuerzo en Cafe X","merchant":"Cafe X","amount":12.5,
					"currency":"USD","date":"2026-06-30","category":"Food","card":"Visa",
					"confidence":"high","notes":""}}
			]}`))
	}))
	defer srv.Close()

	s := &Scanner{APIKey: "test-key", Model: "claude-opus-4-8", Endpoint: srv.URL, client: srv.Client()}
	draft, err := s.ScanReceipt(context.Background(), []byte("fake-image-bytes"), "image/jpeg",
		[]string{"Food", "Travel"}, []string{"Visa", "Amex"})
	if err != nil {
		t.Fatalf("ScanReceipt: %v", err)
	}
	if draft.Merchant != "Cafe X" || draft.Amount != 12.5 || draft.Category != "Food" || draft.Card != "Visa" {
		t.Fatalf("unexpected draft: %+v", draft)
	}
	if draft.Currency != "usd" {
		t.Fatalf("currency should be normalized to lowercase, got %q", draft.Currency)
	}
	if gotAuth != "test-key" || gotVersion != anthropicVersion {
		t.Fatalf("headers wrong: auth=%q version=%q", gotAuth, gotVersion)
	}
}

func TestScanReceiptNotConfigured(t *testing.T) {
	s := &Scanner{APIKey: ""}
	if _, err := s.ScanReceipt(context.Background(), []byte("x"), "image/png", nil, nil); err != ErrNotConfigured {
		t.Fatalf("want ErrNotConfigured, got %v", err)
	}
}

func TestScanReceiptRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"stop_reason":"refusal","content":[]}`))
	}))
	defer srv.Close()
	s := &Scanner{APIKey: "k", Model: "m", Endpoint: srv.URL, client: srv.Client()}
	if _, err := s.ScanReceipt(context.Background(), []byte("x"), "image/jpeg", nil, nil); err == nil {
		t.Fatal("expected error on refusal")
	}
}
