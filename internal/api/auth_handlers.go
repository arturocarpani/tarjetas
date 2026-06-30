package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/tanq16/expenseowl/internal/auth"
	"github.com/tanq16/expenseowl/internal/storage"
	"github.com/tanq16/expenseowl/internal/web"
)

// userView is the API-safe projection of a user (never exposes the password hash).
type userView struct {
	ID             string    `json:"id"`
	Username       string    `json:"username"`
	IsAdmin        bool      `json:"isAdmin"`
	TelegramLinked bool      `json:"telegramLinked"`
	CreatedAt      time.Time `json:"createdAt"`
}

func toUserView(u storage.User) userView {
	return userView{
		ID:             u.ID,
		Username:       u.Username,
		IsAdmin:        u.IsAdmin,
		TelegramLinked: u.TelegramChatID != 0,
		CreatedAt:      u.CreatedAt,
	}
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// ServeLogin serves the login/setup page (public).
func (h *Handler) ServeLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "Method not allowed"})
		return
	}
	w.Header().Set("Content-Type", "text/html")
	if err := web.ServeTemplate(w, "login.html"); err != nil {
		http.Error(w, "Failed to serve template", http.StatusInternalServerError)
	}
}

// AuthStatus reports whether first-run setup is needed and who is logged in (public).
func (h *Handler) AuthStatus(w http.ResponseWriter, r *http.Request) {
	n, err := h.storage.CountUsers()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "Failed to read users"})
		return
	}
	resp := map[string]any{"needsSetup": n == 0, "authenticated": false}
	if cu, ok := auth.CurrentUser(r.Context()); ok {
		resp["authenticated"] = true
		resp["user"] = toUserView(cu)
	}
	writeJSON(w, http.StatusOK, resp)
}

// Setup creates the first (admin) user and logs them in. Only works while no
// users exist; otherwise 403.
func (h *Handler) Setup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "Method not allowed"})
		return
	}
	n, err := h.storage.CountUsers()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "Failed to read users"})
		return
	}
	if n > 0 {
		writeJSON(w, http.StatusForbidden, ErrorResponse{Error: "Setup already completed"})
		return
	}
	var c credentials
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&c); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "Invalid request body"})
		return
	}
	if err := validateCredentials(c); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}
	hash, err := auth.HashPassword(c.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "Failed to hash password"})
		return
	}
	u, err := h.storage.CreateUser(storage.User{Username: c.Username, PasswordHash: hash, IsAdmin: true})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "Failed to create admin user"})
		return
	}
	h.auth.SetSessionCookie(w, u.ID)
	writeJSON(w, http.StatusOK, toUserView(u))
}

// Login verifies credentials and sets the session cookie (public).
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "Method not allowed"})
		return
	}
	var c credentials
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&c); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "Invalid request body"})
		return
	}
	u, err := h.storage.GetUserByUsername(c.Username)
	if err != nil || !auth.CheckPassword(u.PasswordHash, c.Password) {
		// uniform message: do not reveal whether the username exists
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "Invalid username or password"})
		return
	}
	h.auth.SetSessionCookie(w, u.ID)
	writeJSON(w, http.StatusOK, toUserView(u))
}

// Logout clears the session cookie.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "Method not allowed"})
		return
	}
	h.auth.ClearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "success"})
}

// Me returns the current authenticated user (behind auth middleware).
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	cu, ok := auth.CurrentUser(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "unauthorized"})
		return
	}
	writeJSON(w, http.StatusOK, toUserView(cu))
}

// --- Admin user management (behind auth; admin only) ------------------------

func (h *Handler) requireAdmin(w http.ResponseWriter, r *http.Request) (storage.User, bool) {
	cu, ok := auth.CurrentUser(r.Context())
	if !ok || !cu.IsAdmin {
		writeJSON(w, http.StatusForbidden, ErrorResponse{Error: "admin only"})
		return storage.User{}, false
	}
	return cu, true
}

// Users handles GET (list) and POST (create) on /users (admin only).
func (h *Handler) Users(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		users, err := h.storage.ListUsers()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "Failed to list users"})
			return
		}
		views := make([]userView, 0, len(users))
		for _, u := range users {
			views = append(views, toUserView(u))
		}
		writeJSON(w, http.StatusOK, views)
	case http.MethodPost:
		var c struct {
			credentials
			IsAdmin bool `json:"isAdmin"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&c); err != nil {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "Invalid request body"})
			return
		}
		if err := validateCredentials(c.credentials); err != nil {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
			return
		}
		hash, err := auth.HashPassword(c.Password)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "Failed to hash password"})
			return
		}
		u, err := h.storage.CreateUser(storage.User{Username: c.Username, PasswordHash: hash, IsAdmin: c.IsAdmin})
		if err != nil {
			if errors.Is(err, storage.ErrUsernameTaken) {
				writeJSON(w, http.StatusConflict, ErrorResponse{Error: "Username already taken"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "Failed to create user"})
			return
		}
		writeJSON(w, http.StatusCreated, toUserView(u))
	default:
		writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "Method not allowed"})
	}
}

// DeleteUser removes a user by ?id= (admin only). An admin cannot delete itself.
func (h *Handler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	cu, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodDelete {
		writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "Method not allowed"})
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "id parameter is required"})
		return
	}
	if id == cu.ID {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "cannot delete your own account"})
		return
	}
	if err := h.storage.DeleteUser(id); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "user not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "Failed to delete user"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "success"})
}

// ResetPassword sets a user's password (admin only).
func (h *Handler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "Method not allowed"})
		return
	}
	var body struct {
		ID       string `json:"id"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "Invalid request body"})
		return
	}
	if len(body.Password) < 8 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "password must be at least 8 characters"})
		return
	}
	u, err := h.storage.GetUserByID(body.ID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "user not found"})
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "Failed to hash password"})
		return
	}
	u.PasswordHash = hash
	if err := h.storage.UpdateUser(u); err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "Failed to update user"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "success"})
}

func validateCredentials(c credentials) error {
	if len(c.Username) < 3 {
		return errors.New("username must be at least 3 characters")
	}
	if len(c.Password) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	return nil
}
