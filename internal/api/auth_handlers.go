package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/tanq16/expenseowl/internal/auth"
	"github.com/tanq16/expenseowl/internal/storage"
	"github.com/tanq16/expenseowl/internal/web"
)

// userDTO is the sanitized view of a user returned by the API (no password hash).
type userDTO struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	IsAdmin    bool   `json:"isAdmin"`
	TelegramID string `json:"telegramID"`
}

func toUserDTO(u storage.User) userDTO {
	return userDTO{ID: u.ID, Username: u.Username, IsAdmin: u.IsAdmin, TelegramID: u.TelegramID}
}

// ServeLoginPage serves the login form. If already authenticated, redirect home.
func (h *Handler) ServeLoginPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.resolveUser(r); ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	if err := web.ServeTemplate(w, "login.html"); err != nil {
		http.Error(w, "Failed to serve template", http.StatusInternalServerError)
	}
}

// Login validates credentials and sets a session cookie.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "Method not allowed"})
		return
	}
	ip := clientIP(r)
	if h.loginLimiter != nil && h.loginLimiter.blocked(ip) {
		writeJSON(w, http.StatusTooManyRequests, ErrorResponse{Error: "Demasiados intentos. Esperá unos minutos e intentá de nuevo."})
		return
	}
	var creds struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "Invalid request body"})
		return
	}
	// Always run a bcrypt comparison (even for unknown users) to avoid timing
	// enumeration; GetUserByUsername error → empty hash → VerifyPassword false.
	user, _ := h.storage.GetUserByUsername(creds.Username)
	if !auth.VerifyPassword(user.PasswordHash, creds.Password) {
		if h.loginLimiter != nil {
			h.loginLimiter.record(ip)
		}
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "Invalid username or password"})
		return
	}
	if h.loginLimiter != nil {
		h.loginLimiter.reset(ip)
	}
	token, err := h.sessions.Create(user.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "Failed to create session"})
		log.Printf("API ERROR: Failed to create session: %v\n", err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		MaxAge:   int(auth.TTL().Seconds()),
	})
	writeJSON(w, http.StatusOK, toUserDTO(user))
}

// Logout clears the session and cookie.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(SessionCookieName); err == nil {
		h.sessions.Delete(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// Me returns the current authenticated user (used by the frontend to gate the
// admin-only UI).
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "Authentication required"})
		return
	}
	writeJSON(w, http.StatusOK, toUserDTO(user))
}

// ------------------------------------------------------------
// User management (admin only)
// ------------------------------------------------------------

// requireAdmin reports whether the request is from an admin, writing a 403 if not.
func (h *Handler) requireAdmin(w http.ResponseWriter, r *http.Request) (storage.User, bool) {
	user, ok := currentUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "Authentication required"})
		return storage.User{}, false
	}
	if !user.IsAdmin {
		writeJSON(w, http.StatusForbidden, ErrorResponse{Error: "Admin privileges required"})
		return storage.User{}, false
	}
	return user, true
}

// Users handles GET (list) and POST (create) on /users.
func (h *Handler) Users(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listUsers(w, r)
	case http.MethodPost:
		h.createUser(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "Method not allowed"})
	}
}

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	users, err := h.storage.ListUsers()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "Failed to list users"})
		log.Printf("API ERROR: Failed to list users: %v\n", err)
		return
	}
	dtos := make([]userDTO, 0, len(users))
	for _, u := range users {
		dtos = append(dtos, toUserDTO(u))
	}
	writeJSON(w, http.StatusOK, dtos)
}

func (h *Handler) createUser(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	var req struct {
		Username   string `json:"username"`
		Password   string `json:"password"`
		IsAdmin    bool   `json:"isAdmin"`
		TelegramID string `json:"telegramID"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "Invalid request body"})
		return
	}
	req.Username = storage.SanitizeString(req.Username)
	if req.Username == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "Username and password are required"})
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "Failed to hash password"})
		return
	}
	req.TelegramID = strings.TrimSpace(req.TelegramID)
	// CreateUser only enforces username uniqueness, so guard the Telegram ID here
	// (same invariant as UpdateUserTelegramID) — two users sharing a chat.id would
	// make GetUserByTelegramID ambiguous for the bot.
	if req.TelegramID != "" {
		if _, err := h.storage.GetUserByTelegramID(req.TelegramID); err == nil {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "Telegram ID is already linked to another user"})
			return
		}
	}
	user := storage.User{Username: req.Username, PasswordHash: hash, IsAdmin: req.IsAdmin, TelegramID: req.TelegramID}
	if err := h.storage.CreateUser(user); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "success"})
}

// UpdateUserTelegramID links (or clears) a user's Telegram chat ID (admin only).
func (h *Handler) UpdateUserTelegramID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "Method not allowed"})
		return
	}
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	var req struct {
		ID         string `json:"id"`
		TelegramID string `json:"telegramID"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "Invalid request body"})
		return
	}
	if req.ID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "ID is required"})
		return
	}
	if err := h.storage.UpdateUserTelegramID(req.ID, strings.TrimSpace(req.TelegramID)); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "success"})
}

// DeleteUser removes a user (admin only). An admin cannot delete themselves.
func (h *Handler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "Method not allowed"})
		return
	}
	admin, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "ID parameter is required"})
		return
	}
	if id == admin.ID {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "You cannot delete your own account"})
		return
	}
	if err := h.storage.DeleteUser(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error()})
		return
	}
	// Cascade: remove the user's expenses, recurring rules and receipt files so
	// they don't linger orphaned (still counted in admin totals, unmanageable).
	h.cleanupUserData(id)
	h.sessions.DeleteByUser(id) // kill any active sessions of the deleted user
	writeJSON(w, http.StatusOK, map[string]string{"status": "success"})
}

// cleanupUserData deletes all expenses, recurring rules and receipt files owned
// by a user. Best-effort: logs errors but doesn't abort.
func (h *Handler) cleanupUserData(userID string) {
	if expenses, err := h.storage.GetAllExpenses(); err == nil {
		var ids []string
		for _, e := range expenses {
			if e.UserID == userID {
				ids = append(ids, e.ID)
				if e.ReceiptPath != "" {
					h.deleteReceipt(e.ReceiptPath)
				}
			}
		}
		if len(ids) > 0 {
			if err := h.storage.RemoveMultipleExpenses(ids); err != nil {
				log.Printf("API ERROR: cleanup expenses for user %s: %v\n", userID, err)
			}
		}
	} else {
		log.Printf("API ERROR: cleanup could not list expenses for user %s: %v\n", userID, err)
	}
	if recurring, err := h.storage.GetRecurringExpenses(); err == nil {
		for _, r := range recurring {
			if r.UserID == userID {
				if err := h.storage.RemoveRecurringExpense(r.ID, true); err != nil {
					log.Printf("API ERROR: cleanup recurring %s for user %s: %v\n", r.ID, userID, err)
				}
			}
		}
	}
}

// UpdateUserPassword resets a user's password (admin only).
func (h *Handler) UpdateUserPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "Method not allowed"})
		return
	}
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	var req struct {
		ID       string `json:"id"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "Invalid request body"})
		return
	}
	if req.ID == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "ID and password are required"})
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "Failed to hash password"})
		return
	}
	if err := h.storage.UpdateUserPassword(req.ID, hash); err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error()})
		return
	}
	// Revoke the user's existing sessions so a compromised one dies with the reset.
	h.sessions.DeleteByUser(req.ID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "success"})
}
