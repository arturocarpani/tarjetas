package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/tanq16/expenseowl/internal/ai"
	"github.com/tanq16/expenseowl/internal/storage"
	"github.com/tanq16/expenseowl/internal/telegram"
)

// pendingExpense is an extracted-but-not-yet-saved expense awaiting the user's
// confirmation/edits via the inline keyboard.
type pendingExpense struct {
	exp        storage.Expense
	userID     string
	cards      []string
	categories []string
	currency   string
	image      []byte // receipt image bytes, kept until the user confirms
	mediaType  string
	awaiting   string    // "" | "concept" | "date": what free-text reply we expect next
	messageID  int       // the bot message that carries the keyboard, so we can edit it
	editingID  string    // non-empty when editing an already-saved expense (UpdateExpense instead of AddExpense)
	updatedAt  time.Time // for TTL — a stale pending stops capturing later messages
}

// pendingTTL is how long a pending confirmation keeps capturing follow-up text
// (e.g. a concept/date edit) before it's considered abandoned.
const pendingTTL = 20 * time.Minute

// tgPendingStore keeps one in-flight confirmation per chat. In-memory (lost on
// restart, which only drops half-finished confirmations — acceptable).
type tgPendingStore struct {
	mu sync.Mutex
	m  map[int64]*pendingExpense
}

func newTGPendingStore() *tgPendingStore {
	return &tgPendingStore{m: map[int64]*pendingExpense{}}
}

// sweep drops pending confirmations past their TTL. Without it an abandoned
// photo confirmation pins its image bytes in memory until the same chat
// interacts again (the only place the lazy delete happens).
func (s *tgPendingStore) sweep() {
	s.mu.Lock()
	for chatID, p := range s.m {
		if time.Since(p.updatedAt) > pendingTTL {
			delete(s.m, chatID)
		}
	}
	s.mu.Unlock()
}

// EnableTelegram wires the Telegram bot into the handler. Kept separate from
// NewHandler so the existing constructor signature (and its tests) stay intact.
func (h *Handler) EnableTelegram(client *telegram.Client, extractor *ai.Extractor, webhookSecret string) {
	h.telegram = client
	h.extractor = extractor
	h.webhookSecret = webhookSecret
	h.tgPending = newTGPendingStore()
}

// TelegramEnabled reports whether the bot is configured.
func (h *Handler) TelegramEnabled() bool {
	return h.telegram != nil && h.extractor != nil
}

// TelegramWebhook receives updates from Telegram. It authenticates via the
// secret-token header, acknowledges immediately with 200, and processes the
// update in a goroutine so Telegram doesn't time out and retry.
func (h *Handler) TelegramWebhook(w http.ResponseWriter, r *http.Request) {
	if !h.TelegramEnabled() {
		http.Error(w, "Telegram bot not enabled", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "Method not allowed"})
		return
	}
	// A secret is mandatory: the route is public, so without a matching
	// X-Telegram-Bot-Api-Secret-Token header anyone could POST a forged update
	// for a linked chat.id. An empty configured secret rejects everything.
	// Constant-time compare to avoid leaking the secret via timing.
	provided := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
	if h.webhookSecret == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(h.webhookSecret)) != 1 {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "Invalid secret token"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // updates are small (photos are fetched separately)
	var update telegram.Update
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "Invalid request body"})
		return
	}
	// Acknowledge first; the actual work (Claude call, storage) runs async.
	w.WriteHeader(http.StatusOK)
	go h.processUpdate(update)
}

func (h *Handler) processUpdate(update telegram.Update) {
	if update.CallbackQuery != nil {
		h.handleCallback(update.CallbackQuery)
		return
	}
	msg := update.Message
	if msg == nil {
		return
	}
	chatID := msg.Chat.ID

	user, err := h.storage.GetUserByTelegramID(fmt.Sprintf("%d", chatID))
	if err != nil {
		h.reply(chatID, fmt.Sprintf("No estás vinculado a ninguna cuenta. Tu chat ID es %d; pedile al admin que lo cargue en Settings.", chatID))
		return
	}

	text := msg.Text
	if text == "" {
		text = msg.Caption
	}
	fileID := msg.LargestPhotoID()

	// Slash commands take precedence (before edit-capture and expense parsing).
	if fileID == "" && strings.HasPrefix(strings.TrimSpace(text), "/") {
		h.handleCommand(chatID, user, strings.TrimSpace(text))
		return
	}

	// If we're waiting for a free-text edit (concept/date) and this is a plain
	// text message, treat it as the edit value rather than a new expense.
	if fileID == "" && strings.TrimSpace(text) != "" {
		if handled := h.applyTextEdit(chatID, text); handled {
			return
		}
	}

	var image []byte
	var mediaType string
	if fileID != "" {
		path, err := h.telegram.GetFile(fileID)
		if err != nil {
			log.Printf("TELEGRAM ERROR: getFile: %v\n", err)
			h.reply(chatID, "No pude descargar la foto. Probá de nuevo.")
			return
		}
		image, mediaType, err = h.telegram.DownloadFile(path)
		if err != nil {
			log.Printf("TELEGRAM ERROR: download: %v\n", err)
			h.reply(chatID, "No pude descargar la foto. Probá de nuevo.")
			return
		}
	}
	if strings.TrimSpace(text) == "" && image == nil {
		h.reply(chatID, "Mandame el gasto como texto o una foto del ticket.")
		return
	}

	categories, err := h.storage.GetCategories()
	if err != nil {
		log.Printf("TELEGRAM ERROR: categories: %v\n", err)
		h.reply(chatID, "Error interno al leer las categorías.")
		return
	}
	cards, _ := h.storage.GetCards()
	currency, _ := h.storage.GetCurrency()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	expense, err := h.extractor.Extract(ctx, text, image, mediaType, categories, cards, currency)
	if err != nil {
		log.Printf("TELEGRAM ERROR: extract: %v\n", err)
		h.reply(chatID, "No pude interpretar el gasto. Probá ser más específico (nombre, monto, categoría).")
		return
	}
	expense.UserID = user.ID
	expense.Currency = currency

	p := &pendingExpense{
		exp:        expense,
		userID:     user.ID,
		cards:      cards,
		categories: categories,
		currency:   currency,
		image:      image,
		mediaType:  mediaType,
	}
	msgID, err := h.telegram.SendMessageWithKeyboard(chatID, summaryText(p), mainKeyboard())
	if err != nil {
		log.Printf("TELEGRAM ERROR: send keyboard: %v\n", err)
		return
	}
	p.messageID = msgID
	p.updatedAt = time.Now()
	h.tgPending.mu.Lock()
	h.tgPending.m[chatID] = p
	h.tgPending.mu.Unlock()
}

// applyTextEdit consumes a free-text reply when a pending expense is awaiting a
// concept or date edit. Returns true if it handled the message.
func (h *Handler) applyTextEdit(chatID int64, text string) bool {
	h.tgPending.mu.Lock()
	p := h.tgPending.m[chatID]
	if p == nil || p.awaiting == "" {
		h.tgPending.mu.Unlock()
		return false
	}
	if time.Since(p.updatedAt) > pendingTTL {
		delete(h.tgPending.m, chatID) // stale → let this message start a new expense
		h.tgPending.mu.Unlock()
		return false
	}
	p.updatedAt = time.Now()
	var errMsg string
	switch p.awaiting {
	case "concept":
		name := storage.SanitizeString(text)
		if name == "" {
			errMsg = "El concepto no puede quedar vacío. Mandame un texto."
		} else {
			p.exp.Name = name
			p.awaiting = ""
		}
	case "date":
		parsed, ok := parseISODate(text)
		if !ok {
			errMsg = "Fecha inválida. Usá el formato AAAA-MM-DD (ej. 2026-06-30)."
		} else {
			p.exp.Date = parsed
			p.awaiting = ""
		}
	}
	summary := summaryText(p)
	msgID := p.messageID
	h.tgPending.mu.Unlock()

	if errMsg != "" {
		h.reply(chatID, errMsg)
		return true
	}
	if err := h.telegram.EditMessageText(chatID, msgID, summary, mainKeyboard()); err != nil {
		log.Printf("TELEGRAM ERROR: edit after text: %v\n", err)
	}
	return true
}

func (h *Handler) handleCallback(cq *telegram.CallbackQuery) {
	defer func() {
		if err := h.telegram.AnswerCallbackQuery(cq.ID); err != nil {
			log.Printf("TELEGRAM ERROR: answerCallback: %v\n", err)
		}
	}()
	if cq.Message == nil {
		return
	}
	chatID := cq.Message.Chat.ID
	data := cq.Data

	// Post-save actions operate on an already-saved expense (no pending state).
	if strings.HasPrefix(data, "del:") {
		h.handleDeleteCallback(chatID, cq.Message.MessageID, strings.TrimPrefix(data, "del:"))
		return
	}
	if strings.HasPrefix(data, "edit:") {
		h.handleEditStart(chatID, cq.Message.MessageID, strings.TrimPrefix(data, "edit:"))
		return
	}

	h.tgPending.mu.Lock()
	p := h.tgPending.m[chatID]
	if p == nil || time.Since(p.updatedAt) > pendingTTL {
		delete(h.tgPending.m, chatID)
		h.tgPending.mu.Unlock()
		h.reply(chatID, "Esta confirmación expiró. Mandá el gasto de nuevo.")
		return
	}
	p.updatedAt = time.Now()
	var (
		toSave     *pendingExpense
		editText   string
		editMarkup telegram.InlineKeyboardMarkup
		doEdit     bool
		askPrompt  string
		cancelText string
	)

	switch {
	case data == "save":
		toSave = p
		delete(h.tgPending.m, chatID)
	case data == "cancel":
		delete(h.tgPending.m, chatID)
		cancelText = "❌ Cancelado. No se guardó nada."
	case data == "menu:main":
		editText, editMarkup, doEdit = summaryText(p), mainKeyboard(), true
	case data == "menu:card":
		editText, editMarkup, doEdit = cardMenuText(p), cardKeyboard(p.cards), true
	case data == "menu:cat":
		editText, editMarkup, doEdit = summaryText(p), catKeyboard(p.categories), true
	case data == "menu:cur":
		editText, editMarkup, doEdit = summaryText(p), curKeyboard(), true
	case data == "pick:cur:ars":
		p.exp.Currency = "ars"
		editText, editMarkup, doEdit = summaryText(p), mainKeyboard(), true
	case data == "pick:cur:usd":
		p.exp.Currency = "usd"
		editText, editMarkup, doEdit = summaryText(p), mainKeyboard(), true
	case data == "pick:card:none":
		p.exp.Card = ""
		editText, editMarkup, doEdit = summaryText(p), mainKeyboard(), true
	case strings.HasPrefix(data, "pick:card:"):
		if i, err := strconv.Atoi(strings.TrimPrefix(data, "pick:card:")); err == nil && i >= 0 && i < len(p.cards) {
			p.exp.Card = p.cards[i]
		}
		editText, editMarkup, doEdit = summaryText(p), mainKeyboard(), true
	case strings.HasPrefix(data, "pick:cat:"):
		if i, err := strconv.Atoi(strings.TrimPrefix(data, "pick:cat:")); err == nil && i >= 0 && i < len(p.categories) {
			p.exp.Category = p.categories[i]
		}
		editText, editMarkup, doEdit = summaryText(p), mainKeyboard(), true
	case data == "ask:concept":
		p.awaiting = "concept"
		askPrompt = "Mandame el concepto del gasto:"
	case data == "ask:date":
		p.awaiting = "date"
		askPrompt = "Mandame la fecha en formato AAAA-MM-DD (ej. 2026-06-30):"
	}
	msgID := p.messageID
	h.tgPending.mu.Unlock()

	switch {
	case toSave != nil:
		h.saveConfirmed(chatID, msgID, toSave)
	case cancelText != "":
		if err := h.telegram.EditMessagePlain(chatID, msgID, cancelText); err != nil {
			log.Printf("TELEGRAM ERROR: edit cancel: %v\n", err)
		}
	case askPrompt != "":
		h.reply(chatID, askPrompt)
	case doEdit:
		if err := h.telegram.EditMessageText(chatID, msgID, editText, editMarkup); err != nil {
			log.Printf("TELEGRAM ERROR: edit menu: %v\n", err)
		}
	}
}

func (h *Handler) saveConfirmed(chatID int64, msgID int, p *pendingExpense) {
	p.exp.UserID = p.userID
	if p.exp.Currency == "" {
		p.exp.Currency = p.currency
	}
	if err := p.exp.Validate(); err != nil {
		h.telegram.EditMessagePlain(chatID, msgID, fmt.Sprintf("El gasto no es válido: %s", err.Error()))
		return
	}

	if p.editingID != "" {
		// Editing an already-saved expense.
		p.exp.ID = p.editingID
		if err := h.storage.UpdateExpense(p.editingID, p.exp); err != nil {
			log.Printf("TELEGRAM ERROR: update: %v\n", err)
			h.telegram.EditMessagePlain(chatID, msgID, "No pude actualizar el gasto. Probá de nuevo.")
			return
		}
	} else {
		// New expense. Assign the ID up front so we can name the receipt file and
		// attach delete/edit buttons to the confirmation.
		if p.exp.ID == "" {
			p.exp.ID = uuid.New().String()
		}
		if len(p.image) > 0 && h.receiptsDir != "" {
			if name, err := h.saveReceipt(p.exp.ID, p.image, p.mediaType); err != nil {
				log.Printf("TELEGRAM ERROR: save receipt: %v\n", err)
			} else {
				p.exp.ReceiptPath = name
			}
		}
		if err := h.storage.AddExpense(p.exp); err != nil {
			log.Printf("TELEGRAM ERROR: save: %v\n", err)
			h.telegram.EditMessagePlain(chatID, msgID, "No pude guardar el gasto. Probá de nuevo.")
			return
		}
	}
	if err := h.telegram.EditMessageText(chatID, msgID, formatConfirmation(p.exp), postSaveKeyboard(p.exp.ID)); err != nil {
		log.Printf("TELEGRAM ERROR: edit confirm: %v\n", err)
	}
}

const botHelpText = `🤖 Bot de gastos

Cargá un gasto de dos formas:
• Escribí el gasto en texto: "Almuerzo 4500 en restaurante"
• Mandá una foto del ticket y lo leo con IA

Después te muestro lo que entendí y podés:
• 💳 Elegir tarjeta y 🏷️ categoría con botones
• ✏️ Editar el concepto y 📅 la fecha
• ✅ Guardar o ❌ Cancelar

Y sobre un gasto ya guardado: 🗑️ Borrar o ✏️ Editar.

Comandos:
/resumen — total gastado este mes
/ultimos — tus últimos gastos
/cancelar — descartar el gasto en curso
/ayuda — esta ayuda`

var spanishMonths = []string{"enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre"}

func (h *Handler) handleCommand(chatID int64, user storage.User, text string) {
	fields := strings.Fields(text)
	cmd := strings.ToLower(fields[0])
	switch cmd {
	case "/start", "/help", "/ayuda":
		h.reply(chatID, botHelpText)
	case "/resumen":
		h.handleResumen(chatID, user)
	case "/ultimos", "/últimos":
		h.handleUltimos(chatID, user)
	case "/cancelar", "/cancel":
		h.tgPending.mu.Lock()
		_, had := h.tgPending.m[chatID]
		delete(h.tgPending.m, chatID)
		h.tgPending.mu.Unlock()
		if had {
			h.reply(chatID, "❌ Listo, descarté el gasto en curso.")
		} else {
			h.reply(chatID, "No hay ningún gasto en curso.")
		}
	default:
		h.reply(chatID, "Comando no reconocido.\n\n"+botHelpText)
	}
}

// userExpenses returns all expenses owned by a user.
func (h *Handler) userExpenses(userID string) []storage.Expense {
	all, err := h.storage.GetAllExpenses()
	if err != nil {
		return nil
	}
	out := make([]storage.Expense, 0, len(all))
	for _, e := range all {
		if e.UserID == userID {
			out = append(out, e)
		}
	}
	return out
}

func (h *Handler) handleResumen(chatID int64, user storage.User) {
	now := time.Now()
	var total float64
	var count int
	for _, e := range h.userExpenses(user.ID) {
		if e.Date.Year() == now.Year() && e.Date.Month() == now.Month() {
			total += e.Amount
			count++
		}
	}
	h.reply(chatID, fmt.Sprintf("📊 %s %d\nGastos: %d\nTotal: %.2f", spanishMonths[int(now.Month())-1], now.Year(), count, math.Abs(total)))
}

func (h *Handler) handleUltimos(chatID int64, user storage.User) {
	exps := h.userExpenses(user.ID)
	if len(exps) == 0 {
		h.reply(chatID, "Todavía no tenés gastos cargados.")
		return
	}
	sort.Slice(exps, func(i, j int) bool { return exps[i].Date.After(exps[j].Date) })
	if len(exps) > 5 {
		exps = exps[:5]
	}
	var b strings.Builder
	b.WriteString("🧾 Tus últimos gastos:\n")
	for _, e := range exps {
		b.WriteString(fmt.Sprintf("• %s — %s — %.2f — %s\n", e.Date.Format("02/01"), e.Name, math.Abs(e.Amount), e.Category))
	}
	h.reply(chatID, strings.TrimRight(b.String(), "\n"))
}

func (h *Handler) handleDeleteCallback(chatID int64, msgID int, id string) {
	user, ok := h.resolveUserByChat(chatID)
	if !ok {
		return
	}
	exp, err := h.storage.GetExpense(id)
	if err != nil {
		h.telegram.EditMessagePlain(chatID, msgID, "No encontré el gasto (quizá ya lo borraste).")
		return
	}
	if !user.IsAdmin && exp.UserID != user.ID {
		h.telegram.EditMessagePlain(chatID, msgID, "No podés borrar este gasto.")
		return
	}
	if exp.ReceiptPath != "" {
		h.deleteReceipt(exp.ReceiptPath)
	}
	if err := h.storage.RemoveExpense(id); err != nil {
		log.Printf("TELEGRAM ERROR: delete: %v\n", err)
		h.telegram.EditMessagePlain(chatID, msgID, "No pude borrar el gasto. Probá de nuevo.")
		return
	}
	h.telegram.EditMessagePlain(chatID, msgID, fmt.Sprintf("🗑️ Borrado: %s — %.2f — %s", exp.Name, exp.Amount, exp.Category))
}

func (h *Handler) handleEditStart(chatID int64, msgID int, id string) {
	user, ok := h.resolveUserByChat(chatID)
	if !ok {
		return
	}
	exp, err := h.storage.GetExpense(id)
	if err != nil {
		h.telegram.EditMessagePlain(chatID, msgID, "No encontré el gasto.")
		return
	}
	if !user.IsAdmin && exp.UserID != user.ID {
		h.telegram.EditMessagePlain(chatID, msgID, "No podés editar este gasto.")
		return
	}
	categories, err := h.storage.GetCategories()
	if err != nil {
		h.telegram.EditMessagePlain(chatID, msgID, "Error interno al leer las categorías.")
		return
	}
	cards, _ := h.storage.GetCards()
	currency, _ := h.storage.GetCurrency()
	p := &pendingExpense{
		exp:        exp,
		userID:     user.ID,
		cards:      cards,
		categories: categories,
		currency:   currency,
		messageID:  msgID,
		editingID:  id,
		updatedAt:  time.Now(),
	}
	h.tgPending.mu.Lock()
	h.tgPending.m[chatID] = p
	h.tgPending.mu.Unlock()
	if err := h.telegram.EditMessageText(chatID, msgID, summaryText(p), mainKeyboard()); err != nil {
		log.Printf("TELEGRAM ERROR: edit start: %v\n", err)
	}
}

func (h *Handler) resolveUserByChat(chatID int64) (storage.User, bool) {
	user, err := h.storage.GetUserByTelegramID(fmt.Sprintf("%d", chatID))
	if err != nil {
		return storage.User{}, false
	}
	return user, true
}

// ------------------------------------------------------------
// Rendering helpers
// ------------------------------------------------------------

func summaryText(p *pendingExpense) string {
	card := p.exp.Card
	if card == "" {
		card = "(sin asignar)"
	}
	currency := p.exp.Currency
	if currency == "" {
		currency = p.currency
	}
	return fmt.Sprintf(
		"Revisá el gasto y confirmá:\n• Concepto: %s\n• Monto: %.2f\n• Categoría: %s\n• Fecha: %s\n• Tarjeta: %s\n• Moneda: %s",
		p.exp.Name, p.exp.Amount, p.exp.Category, p.exp.Date.Format("2006-01-02"), card, strings.ToUpper(currency),
	)
}

func cardMenuText(p *pendingExpense) string {
	if len(p.cards) == 0 {
		return summaryText(p) + "\n\n(No hay tarjetas cargadas. Agregalas en Settings → Tarjetas.)"
	}
	return summaryText(p) + "\n\nElegí la tarjeta:"
}

// postSaveKeyboard attaches delete/edit actions to a saved-expense confirmation.
func postSaveKeyboard(id string) telegram.InlineKeyboardMarkup {
	return telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{
		{{Text: "🗑️ Borrar", CallbackData: "del:" + id}, {Text: "✏️ Editar", CallbackData: "edit:" + id}},
	}}
}

func mainKeyboard() telegram.InlineKeyboardMarkup {
	return telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{
		{{Text: "✅ Guardar", CallbackData: "save"}, {Text: "❌ Cancelar", CallbackData: "cancel"}},
		{{Text: "💳 Tarjeta", CallbackData: "menu:card"}, {Text: "🏷️ Categoría", CallbackData: "menu:cat"}},
		{{Text: "✏️ Concepto", CallbackData: "ask:concept"}, {Text: "📅 Fecha", CallbackData: "ask:date"}},
		{{Text: "💱 Moneda", CallbackData: "menu:cur"}},
	}}
}

func curKeyboard() telegram.InlineKeyboardMarkup {
	return telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{
		{{Text: "Pesos (ARS)", CallbackData: "pick:cur:ars"}, {Text: "Dólares (USD)", CallbackData: "pick:cur:usd"}},
		{{Text: "⬅️ Volver", CallbackData: "menu:main"}},
	}}
}

func cardKeyboard(cards []string) telegram.InlineKeyboardMarkup {
	rows := buttonGrid("pick:card:", cards)
	rows = append(rows, []telegram.InlineKeyboardButton{{Text: "Sin tarjeta", CallbackData: "pick:card:none"}})
	rows = append(rows, []telegram.InlineKeyboardButton{{Text: "⬅️ Volver", CallbackData: "menu:main"}})
	return telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func catKeyboard(categories []string) telegram.InlineKeyboardMarkup {
	rows := buttonGrid("pick:cat:", categories)
	rows = append(rows, []telegram.InlineKeyboardButton{{Text: "⬅️ Volver", CallbackData: "menu:main"}})
	return telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// buttonGrid lays out one button per item (2 per row) with callback data
// "<prefix><index>".
func buttonGrid(prefix string, items []string) [][]telegram.InlineKeyboardButton {
	var rows [][]telegram.InlineKeyboardButton
	var row []telegram.InlineKeyboardButton
	for i, item := range items {
		row = append(row, telegram.InlineKeyboardButton{Text: item, CallbackData: prefix + strconv.Itoa(i)})
		if len(row) == 2 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	return rows
}

func parseISODate(s string) (time.Time, bool) {
	parsed, err := time.Parse("2006-01-02", strings.TrimSpace(s))
	if err != nil {
		return time.Time{}, false
	}
	now := time.Now()
	return time.Date(parsed.Year(), parsed.Month(), parsed.Day(), now.Hour(), now.Minute(), now.Second(), 0, now.Location()), true
}

func formatConfirmation(e storage.Expense) string {
	line := fmt.Sprintf("✅ Registrado: %s — %.2f %s — %s", e.Name, e.Amount, strings.ToUpper(e.Currency), e.Category)
	if e.Card != "" {
		line += " (" + e.Card + ")"
	}
	line += "\n" + e.Date.Format("2006-01-02")
	return line
}

func (h *Handler) reply(chatID int64, text string) {
	if err := h.telegram.SendMessage(chatID, text); err != nil {
		log.Printf("TELEGRAM ERROR: sendMessage: %v\n", err)
	}
}
