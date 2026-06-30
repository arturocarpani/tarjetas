package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/tanq16/expenseowl/internal/ai"
	"github.com/tanq16/expenseowl/internal/storage"
	"github.com/tanq16/expenseowl/internal/telegram"
)

// EnableTelegram wires the Telegram bot into the handler. Kept separate from
// NewHandler so the existing constructor signature (and its tests) stay intact.
func (h *Handler) EnableTelegram(client *telegram.Client, extractor *ai.Extractor, webhookSecret string) {
	h.telegram = client
	h.extractor = extractor
	h.webhookSecret = webhookSecret
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
	if h.webhookSecret != "" && r.Header.Get("X-Telegram-Bot-Api-Secret-Token") != h.webhookSecret {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "Invalid secret token"})
		return
	}
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
	var image []byte
	var mediaType string
	if fileID := msg.LargestPhotoID(); fileID != "" {
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
	if text == "" && image == nil {
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
	if err := expense.Validate(); err != nil {
		h.reply(chatID, fmt.Sprintf("El gasto no es válido: %s", err.Error()))
		return
	}
	if err := h.storage.AddExpense(expense); err != nil {
		log.Printf("TELEGRAM ERROR: save: %v\n", err)
		h.reply(chatID, "No pude guardar el gasto. Probá de nuevo.")
		return
	}
	h.reply(chatID, formatConfirmation(expense))
}

func formatConfirmation(e storage.Expense) string {
	line := fmt.Sprintf("✅ Registrado: %s — %.2f — %s", e.Name, e.Amount, e.Category)
	if e.Card != "" {
		line += " (" + e.Card + ")"
	}
	return line
}

func (h *Handler) reply(chatID int64, text string) {
	if err := h.telegram.SendMessage(chatID, text); err != nil {
		log.Printf("TELEGRAM ERROR: sendMessage: %v\n", err)
	}
}
