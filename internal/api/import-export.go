package api

import (
	"encoding/csv"
	"fmt"
	"log"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tanq16/expenseowl/internal/storage"
)

// csvSafe neutralizes spreadsheet formula/DDE injection: a text cell beginning
// with one of = + @ (or a tab/CR) is prefixed with a single quote so Excel and
// Sheets treat it as text. Applied only to free-text columns — never to the
// numeric Amount, which legitimately starts with '-' and must round-trip.
func csvSafe(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

// exports all expenses to CSV
func (h *Handler) ExportCSV(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "Method not allowed"})
		return
	}
	expenses, err := h.storage.GetAllExpenses()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "Failed to retrieve expenses"})
		log.Printf("API ERROR: Failed to retrieve expenses for CSV export: %v\n", err)
		return
	}
	user, _ := currentUser(r)

	// Optional filters (query params): from/to (YYYY-MM-DD, inclusive), card,
	// and userID (admin only). Non-admins are always scoped to their own data.
	q := r.URL.Query()
	from, hasFrom := parseDateOnly(q.Get("from"))
	to, hasTo := parseDateOnly(q.Get("to"))
	if hasTo {
		to = to.Add(24*time.Hour - time.Nanosecond) // include the whole "to" day
	}
	cardFilter := q.Get("card")
	userFilter := q.Get("userID")

	filtered := make([]storage.Expense, 0, len(expenses))
	for _, e := range expenses {
		if !user.IsAdmin && e.UserID != user.ID {
			continue
		}
		if user.IsAdmin && userFilter != "" && e.UserID != userFilter {
			continue
		}
		if cardFilter != "" && e.Card != cardFilter {
			continue
		}
		if hasFrom && e.Date.Before(from) {
			continue
		}
		if hasTo && e.Date.After(to) {
			continue
		}
		filtered = append(filtered, e)
	}
	expenses = filtered

	// Map user IDs to usernames for the User column (admin export).
	names := map[string]string{}
	if users, err := h.storage.ListUsers(); err == nil {
		for _, u := range users {
			names[u.ID] = u.Username
		}
	}

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", "attachment; filename=expenses.csv")
	writer := csv.NewWriter(w)
	defer writer.Flush()

	headers := []string{"ID", "Name", "Category", "Card", "Amount", "Currency", "Date", "Tags", "User"}
	if err := writer.Write(headers); err != nil {
		log.Printf("API ERROR: Failed to write CSV header: %v\n", err)
		return
	}

	for _, expense := range expenses {
		record := []string{
			expense.ID,
			csvSafe(expense.Name),
			csvSafe(expense.Category),
			csvSafe(expense.Card),
			strconv.FormatFloat(expense.Amount, 'f', 2, 64),
			expense.Currency,
			expense.Date.Format(time.RFC3339),
			csvSafe(strings.Join(expense.Tags, ",")),
			csvSafe(names[expense.UserID]),
		}
		if err := writer.Write(record); err != nil {
			log.Printf("API ERROR: Failed to write CSV record for expense ID %s: %v\n", expense.ID, err)
			continue
		}
	}
	log.Println("HTTP: Exported expenses to CSV")
}

// parseDateOnly parses a YYYY-MM-DD query param; ok=false if empty/invalid.
func parseDateOnly(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// imports expenses from CSV
func (h *Handler) ImportCSV(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "Method not allowed"})
		return
	}
	importUser, _ := currentUser(r)
	if err := r.ParseMultipartForm(10 << 20); err != nil { // 10MB max file size
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "Could not parse multipart form"})
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "Error retrieving the file"})
		return
	}
	defer file.Close()
	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "Failed to read CSV file"})
		return
	}
	if len(records) < 2 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "CSV file must have a header and at least one data row"})
		return
	}

	header := records[0]
	colMap := make(map[string]int)
	for i, col := range header {
		colMap[strings.ToLower(strings.TrimSpace(col))] = i
	}
	// Check for mandatory columns
	requiredCols := []string{"name", "category", "amount", "date"}
	for _, col := range requiredCols {
		if _, ok := colMap[col]; !ok {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: fmt.Sprintf("Missing required column: %s", col)})
			return
		}
	}
	// Get optional column indices
	idIdx, idExists := colMap["id"]
	tagsIdx, tagsExists := colMap["tags"]
	currencyIdx, currencyExists := colMap["currency"]
	cardIdx, cardExists := colMap["card"]

	currentCategories, err := h.storage.GetCategories()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "Could not retrieve current categories"})
		return
	}
	categorySet := make(map[string]bool)
	for _, cat := range currentCategories {
		categorySet[strings.ToLower(cat)] = true
	}
	var newCategories []string
	var importedCount, skippedCount int
	// TODO: might be worth setting default currency when we have currency updation behavior
	currencyVal, err := h.storage.GetCurrency()
	if err != nil {
		log.Printf("Error: Could not retrieve currency, shutting down import: %v\n", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "Could not retrieve currency"})
		return
	}

	for i, record := range records[1:] {
		if len(record) != len(header) {
			log.Printf("Warning: Skipping row %d due to incorrect column count\n", i+2)
			skippedCount++
			continue
		}

		// Check if expense exists by ID, if provided - without doing a clash resolution
		csvID := ""
		if idExists {
			csvID = strings.TrimSpace(record[idIdx])
			if csvID != "" {
				if _, err := h.storage.GetExpense(csvID); err == nil {
					log.Printf("Info: Skipping row %d because expense with ID '%s' already exists\n", i+2, csvID)
					skippedCount++
					continue
				}
			}
		}

		// Check for currency field, if provided - default is retrieved
		localCurrency := currencyVal
		if currencyExists {
			currency := strings.ToLower(strings.TrimSpace(record[currencyIdx]))
			if !slices.Contains(storage.SupportedCurrencies, currency) {
				log.Printf("Warning: Skipping row %d due to invalid currency: %s\n", i+2, record[currencyIdx])
				skippedCount++
				continue
			}
			localCurrency = currency
		}

		amount, err := strconv.ParseFloat(record[colMap["amount"]], 64)
		if err != nil {
			log.Printf("Warning: Skipping row %d due to invalid amount: %s\n", i+2, record[colMap["amount"]])
			skippedCount++
			continue
		}
		date, err := parseDate(record[colMap["date"]])
		if err != nil {
			log.Printf("Warning: Skipping row %d due to invalid date: %v\n", i+2, err)
			skippedCount++
			continue
		}
		category := strings.TrimSpace(record[colMap["category"]])
		if _, ok := categorySet[strings.ToLower(category)]; !ok {
			newCategories = append(newCategories, category)
			categorySet[strings.ToLower(category)] = true // Add to set to handle duplicates in the same file
		}
		var tags []string
		if tagsExists {
			tagsStr := record[tagsIdx]
			if tagsStr != "" {
				tags = strings.Split(tagsStr, ",")
				for i := range tags {
					tags[i] = strings.TrimSpace(tags[i])
				}
			}
		}

		var card string
		if cardExists {
			card = strings.TrimSpace(record[cardIdx])
		}

		expense := storage.Expense{
			ID:       csvID, // preserve the exported ID so re-importing a backup is idempotent (empty → store generates one)
			UserID:   importUser.ID,
			Name:     strings.TrimSpace(record[colMap["name"]]),
			Category: category,
			Card:     card,
			Amount:   -math.Abs(amount), // expenses stored negative (matches ImportOldCSV and the frontend filter)
			Currency: localCurrency,
			Date:     date,
			Tags:     tags,
		}
		if err := expense.Validate(); err != nil {
			log.Printf("Warning: Skipping row %d due to validation error: %v\n", i+2, err)
			skippedCount++
			continue
		}
		if err := h.storage.AddExpense(expense); err != nil {
			log.Printf("Error: Could not add expense from row %d: %v\n", i+2, err)
			skippedCount++
			continue
		}
		importedCount++
		time.Sleep(10 * time.Millisecond) // Throttle to reduce storage overhead
	}

	if len(newCategories) > 0 {
		if err := h.storage.UpdateCategories(append(currentCategories, newCategories...)); err != nil {
			log.Printf("Warning: Failed to add new categories to config: %v\n", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":          "success",
		"total_processed": len(records) - 1,
		"imported":        importedCount,
		"skipped":         skippedCount,
		"new_categories":  newCategories,
	})
	log.Printf("HTTP: Imported %d expenses from CSV file. Skipped %d records.", importedCount, skippedCount)
}

// handles importing from ExpenseOwl < v4.0
// TODO: remove this in the future
func (h *Handler) ImportOldCSV(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "Method not allowed"})
		return
	}
	importUser, _ := currentUser(r)
	if err := r.ParseMultipartForm(10 << 20); err != nil { // 10MB max file size
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "Could not parse multipart form"})
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "Error retrieving the file"})
		return
	}
	defer file.Close()
	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "Failed to read CSV file"})
		return
	}
	if len(records) < 2 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "CSV file must have a header and at least one data row"})
		return
	}

	header := records[0]
	colMap := make(map[string]int)
	for i, col := range header {
		colMap[strings.ToLower(strings.TrimSpace(col))] = i
	}
	requiredCols := []string{"name", "category", "amount", "date"}
	for _, col := range requiredCols {
		if _, ok := colMap[col]; !ok {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: fmt.Sprintf("Missing required column: %s", col)})
			return
		}
	}

	currentCategories, err := h.storage.GetCategories()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "Could not retrieve current categories"})
		return
	}
	categorySet := make(map[string]bool)
	for _, cat := range currentCategories {
		categorySet[strings.ToLower(cat)] = true
	}
	var newCategories []string
	var importedCount, skippedCount int

	for i, record := range records[1:] {
		if len(record) != len(header) {
			log.Printf("Warning: Skipping row %d due to incorrect column count\n", i+2)
			skippedCount++
			continue
		}
		amount, err := strconv.ParseFloat(record[colMap["amount"]], 64)
		if err != nil {
			log.Printf("Warning: Skipping row %d due to invalid amount: %s\n", i+2, record[colMap["amount"]])
			skippedCount++
			continue
		}
		date, err := parseDate(record[colMap["date"]])
		if err != nil {
			log.Printf("Warning: Skipping row %d due to invalid date: %v\n", i+2, err)
			skippedCount++
			continue
		}
		category := strings.TrimSpace(record[colMap["category"]])
		if _, ok := categorySet[strings.ToLower(category)]; !ok {
			newCategories = append(newCategories, category)
			categorySet[strings.ToLower(category)] = true // Add to set to handle duplicates in the same file
		}

		// all imported rows are expenses, stored as negative amounts
		amountUpdated := -math.Abs(amount)
		expense := storage.Expense{
			UserID:   importUser.ID,
			Name:     strings.TrimSpace(record[colMap["name"]]),
			Category: category,
			Amount:   amountUpdated,
			Date:     date,
		}
		if err := expense.Validate(); err != nil {
			log.Printf("Warning: Skipping row %d due to validation error: %v\n", i+2, err)
			skippedCount++
			continue
		}
		if err := h.storage.AddExpense(expense); err != nil {
			log.Printf("Error: Could not add expense from row %d: %v\n", i+2, err)
			skippedCount++
			continue
		}
		importedCount++
		time.Sleep(10 * time.Millisecond)
	}

	if len(newCategories) > 0 {
		if err := h.storage.UpdateCategories(append(currentCategories, newCategories...)); err != nil {
			log.Printf("Warning: Failed to add new categories to config: %v\n", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":          "success",
		"total_processed": len(records) - 1,
		"imported":        importedCount,
		"skipped":         skippedCount,
		"new_categories":  newCategories,
	})
	log.Printf("HTTP: Imported %d expenses from CSV file. Skipped %d records.", importedCount, skippedCount)
}

func parseDate(dateStr string) (time.Time, error) {
	dateFormats := []string{
		time.RFC3339,
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02 15:04:05",
		"2006-01-02",
		"2006-1-2",
		"2006/01/02",
		"2006/1/2",
	}
	for _, format := range dateFormats {
		if d, err := time.Parse(format, dateStr); err == nil {
			return d.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unable to parse date: %s", dateStr)
}
