package api

import "net/http"

// TelegramLink generates a one-time linking code for the current user and
// returns it along with a t.me deep link (when the bot username is known).
// The user opens the deep link (or sends "/start <code>" to the bot) to link
// their Telegram chat to their account.
func (h *Handler) TelegramLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "Method not allowed"})
		return
	}
	if h.tgLinks == nil {
		writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "Telegram bot is not configured on this server"})
		return
	}
	code := h.tgLinks.Generate(h.uid(r))
	resp := map[string]any{"code": code}
	if h.tgBot != nil && h.tgBot.Username() != "" {
		resp["botUsername"] = h.tgBot.Username()
		resp["deepLink"] = "https://t.me/" + h.tgBot.Username() + "?start=" + code
	}
	writeJSON(w, http.StatusOK, resp)
}
