package api

import (
	"net/http"
	"os"
	"path/filepath"
)

// extForMediaType maps a Telegram image media type to a file extension.
func extForMediaType(mediaType string) string {
	switch mediaType {
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".jpg"
	}
}

// saveReceipt writes the image bytes under receiptsDir as "<expenseID><ext>" and
// returns the stored filename (not the full path, so it stays portable).
func (h *Handler) saveReceipt(expenseID string, data []byte, mediaType string) (string, error) {
	if h.receiptsDir == "" || len(data) == 0 {
		return "", nil
	}
	if err := os.MkdirAll(h.receiptsDir, 0755); err != nil {
		return "", err
	}
	name := expenseID + extForMediaType(mediaType)
	if err := os.WriteFile(filepath.Join(h.receiptsDir, name), data, 0644); err != nil {
		return "", err
	}
	return name, nil
}

// deleteReceipt removes a stored receipt file (best-effort; ignores errors).
func (h *Handler) deleteReceipt(name string) {
	if h.receiptsDir == "" || name == "" {
		return
	}
	_ = os.Remove(filepath.Join(h.receiptsDir, filepath.Base(name)))
}

// ServeReceipt streams the receipt image for an expense the caller is allowed to
// see (its owner, or any admin).
func (h *Handler) ServeReceipt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "Method not allowed"})
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "ID parameter is required"})
		return
	}
	expense, err := h.storage.GetExpense(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "Expense not found"})
		return
	}
	user, _ := currentUser(r)
	if !user.IsAdmin && expense.UserID != user.ID {
		writeJSON(w, http.StatusForbidden, ErrorResponse{Error: "You can only view your own receipts"})
		return
	}
	if expense.ReceiptPath == "" || h.receiptsDir == "" {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "No receipt for this expense"})
		return
	}
	// Guard against path traversal: only serve a bare filename from receiptsDir.
	name := filepath.Base(expense.ReceiptPath)
	full := filepath.Join(h.receiptsDir, name)
	f, err := os.Open(full)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "Receipt file missing"})
		return
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil {
		w.Header().Set("Cache-Control", "private, max-age=3600")
		http.ServeContent(w, r, name, info.ModTime(), f)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, name, expense.Date, f)
}
