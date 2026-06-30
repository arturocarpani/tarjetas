// Package telegram implements a long-polling Telegram bot that turns a photo of
// a receipt into an expense for the linked ExpenseOwl user. It talks to the
// Telegram Bot API over plain net/http (no SDK) to keep dependencies minimal.
package telegram

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tanq16/expenseowl/internal/ai"
	"github.com/tanq16/expenseowl/internal/storage"
)

// LinkStore holds short-lived codes that link a Telegram chat to an app user.
// The app generates a code (authenticated); the user sends "/start <code>" to
// the bot, which consumes it and records the chat on the user.
type LinkStore struct {
	mu    sync.Mutex
	codes map[string]linkEntry
}

type linkEntry struct {
	userID  string
	expires time.Time
}

func NewLinkStore() *LinkStore { return &LinkStore{codes: map[string]linkEntry{}} }

// Generate creates a one-time code (valid 15 minutes) for userID.
func (l *LinkStore) Generate(userID string) string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	code := hex.EncodeToString(buf)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.codes[code] = linkEntry{userID: userID, expires: time.Now().Add(15 * time.Minute)}
	return code
}

// Consume validates and removes a code, returning the userID.
func (l *LinkStore) Consume(code string) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.codes[code]
	if !ok || time.Now().After(e.expires) {
		delete(l.codes, code)
		return "", false
	}
	delete(l.codes, code)
	return e.userID, true
}

// Bot is the running Telegram bot.
type Bot struct {
	token    string
	api      string
	fileBase string
	store    storage.Storage
	scanner  *ai.Scanner
	links    *LinkStore
	client   *http.Client

	mu       sync.Mutex
	pending  map[int64]ai.ExpenseDraft // chatID -> last scanned draft awaiting confirmation
	username string
}

func New(token string, store storage.Storage, scanner *ai.Scanner, links *LinkStore) *Bot {
	return &Bot{
		token:    token,
		api:      "https://api.telegram.org/bot" + token,
		fileBase: "https://api.telegram.org/file/bot" + token,
		store:    store,
		scanner:  scanner,
		links:    links,
		client:   &http.Client{Timeout: 90 * time.Second},
		pending:  map[int64]ai.ExpenseDraft{},
	}
}

// Username returns the bot's @username (empty until the poll loop has started).
func (b *Bot) Username() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.username
}

// Start launches the long-polling loop in a goroutine. It returns immediately.
func (b *Bot) Start(ctx context.Context) {
	if name, err := b.getMe(ctx); err == nil {
		b.mu.Lock()
		b.username = name
		b.mu.Unlock()
		log.Printf("Telegram bot @%s started", name)
	} else {
		log.Printf("Telegram bot: getMe failed (%v); polling anyway", err)
	}
	go b.loop(ctx)
}

func (b *Bot) loop(ctx context.Context) {
	var offset int64
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		updates, err := b.getUpdates(ctx, offset)
		if err != nil {
			// transient network/API error — back off briefly and retry
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			b.handleUpdate(ctx, u)
		}
	}
}

func (b *Bot) handleUpdate(ctx context.Context, u tgUpdate) {
	switch {
	case u.CallbackQuery != nil:
		b.handleCallback(ctx, u.CallbackQuery)
	case u.Message != nil && len(u.Message.Photo) > 0:
		b.handlePhoto(ctx, u.Message)
	case u.Message != nil && strings.HasPrefix(strings.TrimSpace(u.Message.Text), "/start"):
		b.handleStart(ctx, u.Message)
	case u.Message != nil && u.Message.Text != "":
		b.send(ctx, u.Message.Chat.ID, "Mandame una *foto* de un ticket y la cargo como gasto. Para vincular tu cuenta: en la app, Ajustes → Telegram → generá un código, y mandá /start <código>.", nil)
	}
}

func (b *Bot) handleStart(ctx context.Context, m *tgMessage) {
	fields := strings.Fields(m.Text)
	if len(fields) < 2 {
		b.send(ctx, m.Chat.ID, "Para vincular tu cuenta, generá un código en la app (Ajustes → Telegram) y mandá: /start <código>.", nil)
		return
	}
	userID, ok := b.links.Consume(fields[1])
	if !ok {
		b.send(ctx, m.Chat.ID, "Código inválido o expirado. Generá uno nuevo en la app.", nil)
		return
	}
	user, err := b.store.GetUserByID(userID)
	if err != nil {
		b.send(ctx, m.Chat.ID, "No encontré tu usuario. Probá de nuevo.", nil)
		return
	}
	user.TelegramChatID = m.Chat.ID
	if err := b.store.UpdateUser(user); err != nil {
		b.send(ctx, m.Chat.ID, "No pude vincular la cuenta. Probá de nuevo.", nil)
		return
	}
	b.send(ctx, m.Chat.ID, fmt.Sprintf("✅ Cuenta vinculada como *%s*. Mandame una foto de un ticket cuando quieras.", user.Username), nil)
}

func (b *Bot) handlePhoto(ctx context.Context, m *tgMessage) {
	chatID := m.Chat.ID
	user, err := b.store.GetUserByTelegramChatID(chatID)
	if err != nil {
		b.send(ctx, chatID, "Primero vinculá tu cuenta: en la app, Ajustes → Telegram, generá un código y mandá /start <código>.", nil)
		return
	}
	if b.scanner == nil || !b.scanner.Enabled() {
		b.send(ctx, chatID, "El escaneo con IA no está configurado en el servidor.", nil)
		return
	}
	// Telegram sends multiple sizes; the last is the largest.
	largest := m.Photo[len(m.Photo)-1]
	data, err := b.downloadFile(ctx, largest.FileID)
	if err != nil {
		b.send(ctx, chatID, "No pude descargar la foto. Probá de nuevo.", nil)
		return
	}
	b.send(ctx, chatID, "🔎 Analizando el ticket…", nil)

	categories, _ := b.store.GetCategories(user.ID)
	cards, _ := b.store.GetCards(user.ID)
	draft, err := b.scanner.ScanReceipt(ctx, data, "image/jpeg", categories, cards)
	if err != nil {
		b.send(ctx, chatID, "No pude leer el ticket: "+err.Error(), nil)
		return
	}
	b.mu.Lock()
	b.pending[chatID] = draft
	b.mu.Unlock()

	summary := formatDraft(draft)
	keyboard := inlineKeyboard([][]inlineButton{{
		{Text: "✅ Crear", CallbackData: "create"},
		{Text: "❌ Cancelar", CallbackData: "cancel"},
	}})
	b.send(ctx, chatID, summary, keyboard)
}

func (b *Bot) handleCallback(ctx context.Context, cb *tgCallback) {
	if cb.Message == nil {
		return
	}
	chatID := cb.Message.Chat.ID
	b.answerCallback(ctx, cb.ID)

	user, err := b.store.GetUserByTelegramChatID(chatID)
	if err != nil {
		b.editMessage(ctx, chatID, cb.Message.MessageID, "Tu cuenta no está vinculada.")
		return
	}
	b.mu.Lock()
	draft, ok := b.pending[chatID]
	delete(b.pending, chatID)
	b.mu.Unlock()

	if cb.Data == "cancel" {
		b.editMessage(ctx, chatID, cb.Message.MessageID, "❌ Cancelado.")
		return
	}
	if !ok {
		b.editMessage(ctx, chatID, cb.Message.MessageID, "Ya no tengo los datos del ticket. Mandá la foto de nuevo.")
		return
	}

	exp := draftToExpense(draft)
	if err := exp.Validate(); err != nil {
		b.editMessage(ctx, chatID, cb.Message.MessageID, "No pude crear el gasto: "+err.Error())
		return
	}
	if err := b.store.AddExpense(user.ID, exp); err != nil {
		b.editMessage(ctx, chatID, cb.Message.MessageID, "Error guardando el gasto. Probá de nuevo.")
		return
	}
	b.editMessage(ctx, chatID, cb.Message.MessageID, "✅ Gasto creado: "+exp.Name+" — "+exp.Category)
}

func draftToExpense(d ai.ExpenseDraft) storage.Expense {
	name := strings.TrimSpace(d.Name)
	if name == "" {
		name = strings.TrimSpace(d.Merchant)
	}
	if name == "" {
		name = "Gasto"
	}
	category := strings.TrimSpace(d.Category)
	if category == "" {
		category = "Miscellaneous"
	}
	// Receipts are spending: store as a negative amount.
	amount := -math.Abs(d.Amount)
	date := time.Now()
	if d.Date != "" {
		if parsed, err := time.Parse("2006-01-02", d.Date); err == nil {
			date = parsed.UTC()
		}
	}
	return storage.Expense{
		Name:     name,
		Category: category,
		Card:     strings.TrimSpace(d.Card),
		Amount:   amount,
		Currency: strings.TrimSpace(d.Currency),
		Date:     date,
	}
}

func formatDraft(d ai.ExpenseDraft) string {
	var sb strings.Builder
	sb.WriteString("🧾 *Ticket detectado*\n")
	if d.Merchant != "" {
		sb.WriteString("Comercio: " + d.Merchant + "\n")
	}
	sb.WriteString(fmt.Sprintf("Monto: %.2f %s\n", math.Abs(d.Amount), strings.ToUpper(d.Currency)))
	if d.Date != "" {
		sb.WriteString("Fecha: " + d.Date + "\n")
	}
	sb.WriteString("Categoría: " + d.Category + "\n")
	if d.Card != "" {
		sb.WriteString("Tarjeta: " + d.Card + "\n")
	}
	sb.WriteString("Confianza: " + d.Confidence + "\n\n¿Lo creo?")
	return sb.String()
}

// ---- Telegram API wire types & calls ---------------------------------------

type tgUpdate struct {
	UpdateID      int64       `json:"update_id"`
	Message       *tgMessage  `json:"message"`
	CallbackQuery *tgCallback `json:"callback_query"`
}

type tgMessage struct {
	MessageID int64         `json:"message_id"`
	Chat      tgChat        `json:"chat"`
	Text      string        `json:"text"`
	Photo     []tgPhotoSize `json:"photo"`
}

type tgChat struct {
	ID int64 `json:"id"`
}

type tgPhotoSize struct {
	FileID string `json:"file_id"`
}

type tgCallback struct {
	ID      string     `json:"id"`
	Data    string     `json:"data"`
	Message *tgMessage `json:"message"`
}

type inlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type inlineMarkup struct {
	InlineKeyboard [][]inlineButton `json:"inline_keyboard"`
}

func inlineKeyboard(rows [][]inlineButton) *inlineMarkup { return &inlineMarkup{InlineKeyboard: rows} }

func (b *Bot) getMe(ctx context.Context) (string, error) {
	var res struct {
		OK     bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	if err := b.call(ctx, "getMe", nil, &res); err != nil {
		return "", err
	}
	return res.Result.Username, nil
}

func (b *Bot) getUpdates(ctx context.Context, offset int64) ([]tgUpdate, error) {
	body := map[string]any{"offset": offset, "timeout": 30}
	var res struct {
		OK     bool       `json:"ok"`
		Result []tgUpdate `json:"result"`
	}
	if err := b.call(ctx, "getUpdates", body, &res); err != nil {
		return nil, err
	}
	return res.Result, nil
}

func (b *Bot) send(ctx context.Context, chatID int64, text string, markup *inlineMarkup) {
	body := map[string]any{"chat_id": chatID, "text": text, "parse_mode": "Markdown"}
	if markup != nil {
		body["reply_markup"] = markup
	}
	if err := b.call(ctx, "sendMessage", body, nil); err != nil {
		log.Printf("Telegram sendMessage failed: %v", err)
	}
}

func (b *Bot) editMessage(ctx context.Context, chatID, messageID int64, text string) {
	body := map[string]any{"chat_id": chatID, "message_id": messageID, "text": text}
	_ = b.call(ctx, "editMessageText", body, nil)
}

func (b *Bot) answerCallback(ctx context.Context, id string) {
	_ = b.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id}, nil)
}

func (b *Bot) downloadFile(ctx context.Context, fileID string) ([]byte, error) {
	var res struct {
		OK     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := b.call(ctx, "getFile", map[string]any{"file_id": fileID}, &res); err != nil {
		return nil, err
	}
	if res.Result.FilePath == "" {
		return nil, fmt.Errorf("no file_path in getFile response")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.fileBase+"/"+res.Result.FilePath, nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("file download returned %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 15<<20))
}

// call POSTs a JSON body to a Telegram API method and optionally decodes the result.
func (b *Bot) call(ctx context.Context, method string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.api+"/"+method, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram %s returned %d: %s", method, resp.StatusCode, string(data))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}
