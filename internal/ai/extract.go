// Package ai extracts a structured expense from a free-text message or a
// receipt photo using Claude (Opus 4.8) with strict tool use, so the model is
// forced to return a validated JSON object rather than prose.
package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/tanq16/expenseowl/internal/storage"
)

// Extractor wraps the Anthropic client and the model to use.
type Extractor struct {
	client anthropic.Client
	model  anthropic.Model
}

// NewExtractor builds an extractor. The Anthropic client reads ANTHROPIC_API_KEY
// from the environment.
func NewExtractor() *Extractor {
	return &Extractor{
		client: anthropic.NewClient(),
		model:  anthropic.ModelClaudeOpus4_8,
	}
}

// extracted mirrors the strict tool schema the model fills in.
type extracted struct {
	Name     string   `json:"name"`
	Amount   float64  `json:"amount"`
	Category string   `json:"category"`
	Card     string   `json:"card"`
	Date     string   `json:"date"`
	Tags     []string `json:"tags"`
}

// Extract turns a text message and/or receipt image into a partial Expense
// (UserID/Currency are filled in by the caller). The amount is stored as a
// negative value to match ExpenseOwl's convention. Categories/cards are passed
// so the model maps onto valid values.
func (e *Extractor) Extract(ctx context.Context, text string, image []byte, mediaType string, categories, cards []string, currency string) (storage.Expense, error) {
	if len(categories) == 0 {
		return storage.Expense{}, fmt.Errorf("no categories configured")
	}

	tool := anthropic.ToolParam{
		Name:        "record_expense",
		Description: anthropic.String("Record a single expense extracted from the user's message or receipt photo."),
		Strict:      anthropic.Bool(true),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Short merchant or expense name, e.g. 'Almuerzo restaurante'.",
				},
				"amount": map[string]any{
					"type":        "number",
					"description": "Total amount spent, as a positive number (no currency symbol).",
				},
				"category": map[string]any{
					"type":        "string",
					"enum":        categories,
					"description": "The single best-matching category from the allowed list.",
				},
				"card": map[string]any{
					"type":        "string",
					"description": "Card used, matching one of the allowed cards, or empty string if unknown.",
				},
				"date": map[string]any{
					"type":        "string",
					"description": "Expense date as YYYY-MM-DD, or empty string if not stated (defaults to today).",
				},
				"tags": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Optional tags; empty array if none.",
				},
			},
			Required: []string{"name", "amount", "category", "card", "date", "tags"},
			ExtraFields: map[string]any{
				"additionalProperties": false,
			},
		},
	}

	var sb strings.Builder
	sb.WriteString("You extract one corporate-card expense and call the record_expense tool.\n")
	sb.WriteString("Allowed categories: " + strings.Join(categories, ", ") + ".\n")
	if len(cards) > 0 {
		sb.WriteString("Allowed cards: " + strings.Join(cards, ", ") + ". Use empty string if none clearly applies.\n")
	} else {
		sb.WriteString("No cards are configured; always use an empty string for card.\n")
	}
	sb.WriteString("Map the expense to the closest allowed category. Amount is a positive number. ")
	sb.WriteString("For the date: if the receipt or message states a date (look for labels like 'FECHA', 'DATE', or a printed date), ")
	sb.WriteString("return it as YYYY-MM-DD; only if there is truly no date anywhere, use an empty string. ")
	sb.WriteString("For the name: if the user wrote a concept/description, prefer it; otherwise use the merchant name. ")
	sb.WriteString("Reply only via the tool call.")
	system := sb.String()

	blocks := []anthropic.ContentBlockParamUnion{}
	if image != nil {
		b64 := base64.StdEncoding.EncodeToString(image)
		blocks = append(blocks, anthropic.NewImageBlockBase64(mediaType, b64))
	}
	userText := strings.TrimSpace(text)
	if userText == "" {
		userText = "Extract the expense from the attached receipt photo."
	}
	blocks = append(blocks, anthropic.NewTextBlock(userText))

	resp, err := e.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     e.model,
		MaxTokens: 1024,
		System:    []anthropic.TextBlockParam{{Text: system}},
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(blocks...)},
		Tools:     []anthropic.ToolUnionParam{{OfTool: &tool}},
		ToolChoice: anthropic.ToolChoiceParamOfTool("record_expense"),
	})
	if err != nil {
		return storage.Expense{}, fmt.Errorf("claude request failed: %v", err)
	}

	var raw []byte
	for _, block := range resp.Content {
		if tu, ok := block.AsAny().(anthropic.ToolUseBlock); ok && tu.Name == "record_expense" {
			raw = []byte(tu.JSON.Input.Raw())
			break
		}
	}
	if len(raw) == 0 {
		return storage.Expense{}, fmt.Errorf("model did not return an expense")
	}

	var parsed extracted
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return storage.Expense{}, fmt.Errorf("failed to parse model output: %v", err)
	}

	return toExpense(parsed, cards), nil
}

// toExpense converts the model output into a partial Expense (no UserID/Currency).
func toExpense(p extracted, cards []string) storage.Expense {
	date := time.Now()
	if p.Date != "" {
		if parsed, err := time.Parse("2006-01-02", p.Date); err == nil {
			// keep the current time-of-day so it lands in the right day locally
			now := time.Now()
			date = time.Date(parsed.Year(), parsed.Month(), parsed.Day(), now.Hour(), now.Minute(), now.Second(), 0, now.Location())
		}
	}
	card := p.Card
	if card != "" && !containsFold(cards, card) {
		card = "" // model guessed a card we don't have; drop it
	}
	return storage.Expense{
		Name:     p.Name,
		Amount:   -math.Abs(p.Amount), // expenses are stored negative
		Category: p.Category,
		Card:     card,
		Date:     date,
		Tags:     p.Tags,
	}
}

func containsFold(list []string, v string) bool {
	for _, item := range list {
		if strings.EqualFold(item, v) {
			return true
		}
	}
	return false
}
