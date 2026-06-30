package api

import (
	"context"
	"net/http"

	"github.com/tanq16/expenseowl/internal/storage"
)

type contextKey string

const userContextKey contextKey = "currentUser"

// SessionCookieName is the cookie that carries the session token.
const SessionCookieName = "session"

// resolveUser reads the session cookie and returns the authenticated user.
func (h *Handler) resolveUser(r *http.Request) (storage.User, bool) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		return storage.User{}, false
	}
	userID, ok := h.sessions.Get(cookie.Value)
	if !ok {
		return storage.User{}, false
	}
	user, err := h.storage.GetUserByID(userID)
	if err != nil {
		return storage.User{}, false
	}
	return user, true
}

// currentUser returns the user injected into the request context by the auth
// middleware. The second return is false if the request is unauthenticated.
func currentUser(r *http.Request) (storage.User, bool) {
	user, ok := r.Context().Value(userContextKey).(storage.User)
	return user, ok
}

// RequirePage wraps a page handler: redirects to /login when unauthenticated.
func (h *Handler) RequirePage(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := h.resolveUser(r)
		if !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		ctx := context.WithValue(r.Context(), userContextKey, user)
		next(w, r.WithContext(ctx))
	}
}

// RequireAPI wraps an API handler: returns 401 JSON when unauthenticated.
func (h *Handler) RequireAPI(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := h.resolveUser(r)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "Authentication required"})
			return
		}
		ctx := context.WithValue(r.Context(), userContextKey, user)
		next(w, r.WithContext(ctx))
	}
}
