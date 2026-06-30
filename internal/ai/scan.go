// Package ai turns a photo of a receipt into a structured expense draft using
// Anthropic's Messages API (vision + forced tool use). It talks to the API over
// plain net/http to keep the project's dependency footprint minimal.
package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// ExpenseDraft is the structured result of scanning a receipt. The user
// confirms/edits it before an expense is actually created.
type ExpenseDraft struct {
	Name       string  `json:"name"`       // short concept/description
	Merchant   string  `json:"merchant"`   // business name
	Amount     float64 `json:"amount"`     // total, positive number
	Currency   string  `json:"currency"`   // lowercase ISO code, or ""
	Date       string  `json:"date"`       // YYYY-MM-DD, or ""
	Category   string  `json:"category"`   // best fit from the user's categories
	Card       string  `json:"card"`       // best fit from the user's cards, or ""
	Confidence string  `json:"confidence"` // low | medium | high
	Notes      string  `json:"notes"`
}

// ErrNotConfigured is returned when no API key is set, so the API layer can
// surface a clear, actionable error instead of failing opaquely.
var ErrNotConfigured = errors.New("AI receipt scanning is not configured (set ANTHROPIC_API_KEY)")

const anthropicVersion = "2023-06-01"

// Scanner calls the Anthropic Messages API to extract expenses from images.
type Scanner struct {
	APIKey   string
	Model    string
	Endpoint string
	client   *http.Client
}

// NewScanner builds a Scanner from the environment: ANTHROPIC_API_KEY and an
// optional ANTHROPIC_MODEL (defaults to a current vision-capable model).
func NewScanner() *Scanner {
	model := os.Getenv("ANTHROPIC_MODEL")
	if model == "" {
		model = "claude-opus-4-8"
	}
	return &Scanner{
		APIKey:   os.Getenv("ANTHROPIC_API_KEY"),
		Model:    model,
		Endpoint: "https://api.anthropic.com/v1/messages",
		client:   &http.Client{Timeout: 90 * time.Second},
	}
}

// Enabled reports whether scanning is configured (an API key is present).
func (s *Scanner) Enabled() bool { return s.APIKey != "" }

var allowedMedia = map[string]bool{
	"image/jpeg": true, "image/png": true, "image/webp": true, "image/gif": true,
}

// recordExpenseSchema forces every field to be present (empty allowed for the
// optional ones) so unmarshalling the tool input is predictable.
var recordExpenseSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "name": {"type": "string", "description": "Short human label for the expense"},
    "merchant": {"type": "string", "description": "Business / merchant name, or empty"},
    "amount": {"type": "number", "description": "TOTAL paid, as a positive number; 0 if unreadable"},
    "currency": {"type": "string", "description": "lowercase ISO 4217 code if clearly shown, else empty"},
    "date": {"type": "string", "description": "YYYY-MM-DD if clearly shown, else empty"},
    "category": {"type": "string", "description": "single best fit from the provided category list"},
    "card": {"type": "string", "description": "best fit from the provided card list if indicated, else empty"},
    "confidence": {"type": "string", "enum": ["low", "medium", "high"]},
    "notes": {"type": "string"}
  },
  "required": ["name", "merchant", "amount", "currency", "date", "category", "card", "confidence", "notes"]
}`)

type imageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type contentBlock struct {
	Type   string       `json:"type"`
	Source *imageSource `json:"source,omitempty"`
	Text   string       `json:"text,omitempty"`
}

type apiTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type apiRequest struct {
	Model      string            `json:"model"`
	MaxTokens  int               `json:"max_tokens"`
	Tools      []apiTool         `json:"tools"`
	ToolChoice map[string]string `json:"tool_choice"`
	Messages   []map[string]any  `json:"messages"`
}

type apiResponse struct {
	StopReason string `json:"stop_reason"`
	Content    []struct {
		Type  string          `json:"type"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
}

// ScanReceipt sends the image to the model and returns a structured draft. The
// user's categories and cards are passed so the model can pick the best fit.
func (s *Scanner) ScanReceipt(ctx context.Context, image []byte, mediaType string, categories, cards []string) (ExpenseDraft, error) {
	if !s.Enabled() {
		return ExpenseDraft{}, ErrNotConfigured
	}
	if len(image) == 0 {
		return ExpenseDraft{}, errors.New("empty image")
	}
	if !allowedMedia[mediaType] {
		mediaType = "image/jpeg"
	}

	prompt := buildPrompt(categories, cards)
	reqBody := apiRequest{
		Model:     s.Model,
		MaxTokens: 1024,
		Tools: []apiTool{{
			Name:        "record_expense",
			Description: "Record the single expense shown in the receipt/ticket image.",
			InputSchema: recordExpenseSchema,
		}},
		ToolChoice: map[string]string{"type": "tool", "name": "record_expense"},
		Messages: []map[string]any{{
			"role": "user",
			"content": []contentBlock{
				{Type: "image", Source: &imageSource{Type: "base64", MediaType: mediaType, Data: base64.StdEncoding.EncodeToString(image)}},
				{Type: "text", Text: prompt},
			},
		}},
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return ExpenseDraft{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return ExpenseDraft{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", s.APIKey)
	req.Header.Set("anthropic-version", anthropicVersion)

	resp, err := s.client.Do(req)
	if err != nil {
		return ExpenseDraft{}, fmt.Errorf("AI request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode != http.StatusOK {
		return ExpenseDraft{}, fmt.Errorf("AI API returned %d: %s", resp.StatusCode, truncate(string(body), 300))
	}

	var parsed apiResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ExpenseDraft{}, fmt.Errorf("failed to parse AI response: %w", err)
	}
	if parsed.StopReason == "refusal" {
		return ExpenseDraft{}, errors.New("the AI declined to process this image")
	}
	for _, block := range parsed.Content {
		if block.Type == "tool_use" && block.Name == "record_expense" {
			var draft ExpenseDraft
			if err := json.Unmarshal(block.Input, &draft); err != nil {
				return ExpenseDraft{}, fmt.Errorf("failed to parse expense draft: %w", err)
			}
			draft.Currency = strings.ToLower(strings.TrimSpace(draft.Currency))
			return draft, nil
		}
	}
	return ExpenseDraft{}, errors.New("AI response contained no expense data")
}

func buildPrompt(categories, cards []string) string {
	cats := "Food, Groceries, Travel, Rent, Utilities, Entertainment, Healthcare, Shopping, Miscellaneous, Income"
	if len(categories) > 0 {
		cats = strings.Join(categories, ", ")
	}
	cardLine := "(no cards configured — leave card empty)"
	if len(cards) > 0 {
		cardLine = strings.Join(cards, ", ")
	}
	return fmt.Sprintf(`Extract a single expense from this photo of a receipt/ticket and record it with the record_expense tool.

Rules:
- amount: the TOTAL paid, as a positive number. If you cannot read a total, set amount to 0 and confidence to "low".
- currency: lowercase ISO 4217 code only if clearly indicated on the receipt (e.g. usd, eur, ars), otherwise "".
- date: YYYY-MM-DD only if clearly indicated, otherwise "".
- category: choose the single best fit from this list: %s. If none fit, use "Miscellaneous" if it is in the list, else "".
- card: if the receipt indicates a payment card/method that matches one of these, use it exactly: %s. Otherwise "".
- name: a short, human label (e.g. "Almuerzo en <merchant>").
- confidence: low / medium / high — how sure you are this is a valid receipt and the amount is correct.
If the image is not a receipt, set confidence to "low" and amount to 0.`, cats, cardLine)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
