package api

import (
	"io"
	"net/http"
)

// ScanReceipt accepts a receipt image (multipart field "image"), runs it
// through the AI scanner, and returns a structured expense draft for the user
// to confirm. It does NOT create the expense — the client prefills the
// add-expense form from the draft and submits it via PUT /expense.
func (h *Handler) ScanReceipt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "Method not allowed"})
		return
	}
	if h.scanner == nil || !h.scanner.Enabled() {
		writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "AI receipt scanning is not configured on this server"})
		return
	}
	uid := h.uid(r)

	r.Body = http.MaxBytesReader(w, r.Body, 12<<20) // 12 MB cap
	if err := r.ParseMultipartForm(12 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "Could not parse upload (max 12MB)"})
		return
	}
	file, hdr, err := r.FormFile("image")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "Missing 'image' file field"})
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "Failed to read image"})
		return
	}
	mediaType := hdr.Header.Get("Content-Type")

	categories, _ := h.storage.GetCategories(uid)
	cards, _ := h.storage.GetCards(uid)

	draft, err := h.scanner.ScanReceipt(r.Context(), data, mediaType, categories, cards)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, ErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, draft)
}
