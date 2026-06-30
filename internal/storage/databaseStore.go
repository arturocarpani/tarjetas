package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// databaseStore implements the Storage interface for PostgreSQL.
type databaseStore struct {
	db       *sql.DB
	defaults map[string]string // allows reusing defaults without querying for config
}

// SQL queries as constants for reusability and clarity.
const (
	createExpensesTableSQL = `
	CREATE TABLE IF NOT EXISTS expenses (
		id VARCHAR(36) PRIMARY KEY,
		recurring_id VARCHAR(36),
		name VARCHAR(255) NOT NULL,
		category VARCHAR(255) NOT NULL,
		amount NUMERIC(10, 2) NOT NULL,
		currency VARCHAR(3) NOT NULL,
		date TIMESTAMPTZ NOT NULL,
		tags TEXT,
		card TEXT
	);`

	createRecurringExpensesTableSQL = `
	CREATE TABLE IF NOT EXISTS recurring_expenses (
		id VARCHAR(36) PRIMARY KEY,
		name VARCHAR(255) NOT NULL,
		amount NUMERIC(10, 2) NOT NULL,
		currency VARCHAR(3) NOT NULL,
		category VARCHAR(255) NOT NULL,
		start_date TIMESTAMPTZ NOT NULL,
		interval VARCHAR(50) NOT NULL,
		occurrences INTEGER NOT NULL,
		tags TEXT,
		card TEXT
	);`

	createConfigTableSQL = `
	CREATE TABLE IF NOT EXISTS config (
		id VARCHAR(255) PRIMARY KEY DEFAULT 'default',
		categories TEXT NOT NULL,
		currency VARCHAR(255) NOT NULL,
		start_date INTEGER NOT NULL,
		cards TEXT
	);`

	createUsersTableSQL = `
	CREATE TABLE IF NOT EXISTS users (
		id VARCHAR(36) PRIMARY KEY,
		username VARCHAR(255) NOT NULL UNIQUE,
		password_hash TEXT NOT NULL,
		is_admin BOOLEAN NOT NULL DEFAULT FALSE,
		telegram_id TEXT,
		created_at TIMESTAMPTZ NOT NULL
	);`

	// migrations for databases created before the "card"/"cards" columns existed.
	// CREATE TABLE IF NOT EXISTS won't add columns to a pre-existing table, so we
	// add them idempotently here. Same applies to the per-user "user_id" columns.
	migrateColumnsSQL = `
	ALTER TABLE expenses ADD COLUMN IF NOT EXISTS card TEXT;
	ALTER TABLE recurring_expenses ADD COLUMN IF NOT EXISTS card TEXT;
	ALTER TABLE config ADD COLUMN IF NOT EXISTS cards TEXT;
	ALTER TABLE expenses ADD COLUMN IF NOT EXISTS user_id TEXT;
	ALTER TABLE recurring_expenses ADD COLUMN IF NOT EXISTS user_id TEXT;
	ALTER TABLE users ADD COLUMN IF NOT EXISTS telegram_id TEXT;`
)

func InitializePostgresStore(baseConfig SystemConfig) (Storage, error) {
	dbURL := makeDBURL(baseConfig)
	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		return nil, fmt.Errorf("failed to open PostgreSQL database: %v", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping PostgreSQL database: %v", err)
	}
	log.Println("Connected to PostgreSQL database")

	if err := createTables(db); err != nil {
		return nil, fmt.Errorf("failed to create database tables: %v", err)
	}
	return &databaseStore{db: db, defaults: map[string]string{}}, nil
}

func makeDBURL(baseConfig SystemConfig) string {
	return fmt.Sprintf("postgres://%s:%s@%s?sslmode=%s", baseConfig.StorageUser, baseConfig.StoragePass, baseConfig.StorageURL, baseConfig.StorageSSL)
}

func createTables(db *sql.DB) error {
	for _, query := range []string{createExpensesTableSQL, createRecurringExpensesTableSQL, createConfigTableSQL, createUsersTableSQL, migrateColumnsSQL} {
		if _, err := db.Exec(query); err != nil {
			return err
		}
	}
	return nil
}

func (s *databaseStore) Close() error {
	return s.db.Close()
}

// ------------------------------------------------------------
// Users
// ------------------------------------------------------------

func (s *databaseStore) CreateUser(user User) error {
	if user.ID == "" {
		user.ID = uuid.New().String()
	}
	if user.CreatedAt.IsZero() {
		user.CreatedAt = time.Now()
	}
	query := `INSERT INTO users (id, username, password_hash, is_admin, telegram_id, created_at) VALUES ($1, $2, $3, $4, $5, $6)`
	_, err := s.db.Exec(query, user.ID, user.Username, user.PasswordHash, user.IsAdmin, nullString(user.TelegramID), user.CreatedAt)
	if err != nil {
		return fmt.Errorf("failed to create user: %v", err)
	}
	return nil
}

// nullString maps an empty string to SQL NULL so the unique-ish telegram_id
// column stays sparse rather than storing many empty strings.
func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func scanUser(scanner interface{ Scan(...any) error }) (User, error) {
	var u User
	var telegramID sql.NullString
	err := scanner.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.IsAdmin, &telegramID, &u.CreatedAt)
	if telegramID.Valid {
		u.TelegramID = telegramID.String
	}
	return u, err
}

func (s *databaseStore) GetUserByUsername(username string) (User, error) {
	query := `SELECT id, username, password_hash, is_admin, telegram_id, created_at FROM users WHERE username = $1`
	u, err := scanUser(s.db.QueryRow(query, username))
	if err != nil {
		if err == sql.ErrNoRows {
			return User{}, fmt.Errorf("user '%s' not found", username)
		}
		return User{}, fmt.Errorf("failed to get user: %v", err)
	}
	return u, nil
}

func (s *databaseStore) GetUserByID(id string) (User, error) {
	query := `SELECT id, username, password_hash, is_admin, telegram_id, created_at FROM users WHERE id = $1`
	u, err := scanUser(s.db.QueryRow(query, id))
	if err != nil {
		if err == sql.ErrNoRows {
			return User{}, fmt.Errorf("user with ID %s not found", id)
		}
		return User{}, fmt.Errorf("failed to get user: %v", err)
	}
	return u, nil
}

func (s *databaseStore) GetUserByTelegramID(telegramID string) (User, error) {
	if telegramID == "" {
		return User{}, fmt.Errorf("telegram ID cannot be empty")
	}
	query := `SELECT id, username, password_hash, is_admin, telegram_id, created_at FROM users WHERE telegram_id = $1`
	u, err := scanUser(s.db.QueryRow(query, telegramID))
	if err != nil {
		if err == sql.ErrNoRows {
			return User{}, fmt.Errorf("user with Telegram ID %s not found", telegramID)
		}
		return User{}, fmt.Errorf("failed to get user: %v", err)
	}
	return u, nil
}

func (s *databaseStore) ListUsers() ([]User, error) {
	query := `SELECT id, username, password_hash, is_admin, telegram_id, created_at FROM users ORDER BY created_at`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to query users: %v", err)
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan user: %v", err)
		}
		users = append(users, u)
	}
	return users, nil
}

func (s *databaseStore) UpdateUserPassword(id, passwordHash string) error {
	res, err := s.db.Exec(`UPDATE users SET password_hash = $1 WHERE id = $2`, passwordHash, id)
	if err != nil {
		return fmt.Errorf("failed to update password: %v", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("user with ID %s not found", id)
	}
	return nil
}

func (s *databaseStore) UpdateUserTelegramID(id, telegramID string) error {
	if telegramID != "" {
		var existing string
		err := s.db.QueryRow(`SELECT id FROM users WHERE telegram_id = $1`, telegramID).Scan(&existing)
		if err == nil && existing != id {
			return fmt.Errorf("Telegram ID %s is already linked to another user", telegramID)
		} else if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("failed to check telegram id: %v", err)
		}
	}
	res, err := s.db.Exec(`UPDATE users SET telegram_id = $1 WHERE id = $2`, nullString(telegramID), id)
	if err != nil {
		return fmt.Errorf("failed to update telegram id: %v", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("user with ID %s not found", id)
	}
	return nil
}

func (s *databaseStore) DeleteUser(id string) error {
	res, err := s.db.Exec(`DELETE FROM users WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("failed to delete user: %v", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("user with ID %s not found", id)
	}
	return nil
}

func (s *databaseStore) CountUsers() (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count)
	return count, err
}

func (s *databaseStore) saveConfig(config *Config) error {
	categoriesJSON, err := json.Marshal(config.Categories)
	if err != nil {
		return fmt.Errorf("failed to marshal categories: %v", err)
	}
	cardsJSON, err := json.Marshal(config.Cards)
	if err != nil {
		return fmt.Errorf("failed to marshal cards: %v", err)
	}
	query := `
		INSERT INTO config (id, categories, cards, currency, start_date)
		VALUES ('default', $1, $2, $3, $4)
		ON CONFLICT (id) DO UPDATE SET
			categories = EXCLUDED.categories,
			cards = EXCLUDED.cards,
			currency = EXCLUDED.currency,
			start_date = EXCLUDED.start_date;
	`
	_, err = s.db.Exec(query, string(categoriesJSON), string(cardsJSON), config.Currency, config.StartDate)
	s.defaults["currency"] = config.Currency
	s.defaults["start_date"] = fmt.Sprintf("%d", config.StartDate)
	return err
}

func (s *databaseStore) updateConfig(updater func(c *Config) error) error {
	config, err := s.GetConfig()
	if err != nil {
		return err
	}
	if err := updater(config); err != nil {
		return err
	}
	return s.saveConfig(config)
}

func (s *databaseStore) GetConfig() (*Config, error) {
	query := `SELECT categories, cards, currency, start_date FROM config WHERE id = 'default'`
	var categoriesStr, currency string
	var cardsStr sql.NullString
	var startDate int
	err := s.db.QueryRow(query).Scan(&categoriesStr, &cardsStr, &currency, &startDate)

	if err != nil {
		if err == sql.ErrNoRows {
			config := &Config{}
			config.SetBaseConfig()
			if err := s.saveConfig(config); err != nil {
				return nil, fmt.Errorf("failed to save initial default config: %v", err)
			}
			return config, nil
		}
		return nil, fmt.Errorf("failed to get config from db: %v", err)
	}

	var config Config
	config.Currency = currency
	config.StartDate = startDate
	if err := json.Unmarshal([]byte(categoriesStr), &config.Categories); err != nil {
		return nil, fmt.Errorf("failed to parse categories from db: %v", err)
	}
	if cardsStr.Valid && cardsStr.String != "" {
		if err := json.Unmarshal([]byte(cardsStr.String), &config.Cards); err != nil {
			return nil, fmt.Errorf("failed to parse cards from db: %v", err)
		}
	}

	recurring, err := s.GetRecurringExpenses()
	if err != nil {
		return nil, fmt.Errorf("failed to get recurring expenses for config: %v", err)
	}
	config.RecurringExpenses = recurring

	return &config, nil
}

func (s *databaseStore) GetCategories() ([]string, error) {
	config, err := s.GetConfig()
	if err != nil {
		return nil, err
	}
	return config.Categories, nil
}

func (s *databaseStore) UpdateCategories(categories []string) error {
	return s.updateConfig(func(c *Config) error {
		c.Categories = categories
		return nil
	})
}

func (s *databaseStore) GetCards() ([]string, error) {
	config, err := s.GetConfig()
	if err != nil {
		return nil, err
	}
	return config.Cards, nil
}

func (s *databaseStore) UpdateCards(cards []string) error {
	return s.updateConfig(func(c *Config) error {
		c.Cards = cards
		return nil
	})
}

func (s *databaseStore) GetCurrency() (string, error) {
	config, err := s.GetConfig()
	if err != nil {
		return "", err
	}
	return config.Currency, nil
}

func (s *databaseStore) UpdateCurrency(currency string) error {
	if !slices.Contains(SupportedCurrencies, currency) {
		return fmt.Errorf("invalid currency: %s", currency)
	}
	return s.updateConfig(func(c *Config) error {
		c.Currency = currency
		return nil
	})
}

func (s *databaseStore) GetStartDate() (int, error) {
	config, err := s.GetConfig()
	if err != nil {
		return 0, err
	}
	return config.StartDate, nil
}

func (s *databaseStore) UpdateStartDate(startDate int) error {
	if startDate < 1 || startDate > 31 {
		return fmt.Errorf("invalid start date: %d", startDate)
	}
	return s.updateConfig(func(c *Config) error {
		c.StartDate = startDate
		return nil
	})
}

func scanExpense(scanner interface{ Scan(...any) error }) (Expense, error) {
	var expense Expense
	var tagsStr sql.NullString
	var recurringID sql.NullString
	var card sql.NullString
	var userID sql.NullString
	err := scanner.Scan(&expense.ID, &recurringID, &expense.Name, &expense.Category, &expense.Amount, &expense.Currency, &expense.Date, &tagsStr, &card, &userID)
	if err != nil {
		return Expense{}, err
	}
	if recurringID.Valid {
		expense.RecurringID = recurringID.String
	}
	if card.Valid {
		expense.Card = card.String
	}
	if userID.Valid {
		expense.UserID = userID.String
	}
	if tagsStr.Valid && tagsStr.String != "" {
		if err := json.Unmarshal([]byte(tagsStr.String), &expense.Tags); err != nil {
			return Expense{}, fmt.Errorf("failed to parse tags for expense %s: %v", expense.ID, err)
		}
	}
	return expense, nil
}

func (s *databaseStore) GetAllExpenses() ([]Expense, error) {
	query := `SELECT id, recurring_id, name, category, amount, currency, date, tags, card, user_id FROM expenses ORDER BY date DESC`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to query expenses: %v", err)
	}
	defer rows.Close()

	var expenses []Expense
	for rows.Next() {
		expense, err := scanExpense(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan expense: %v", err)
		}
		expenses = append(expenses, expense)
	}
	return expenses, nil
}

func (s *databaseStore) GetExpense(id string) (Expense, error) {
	query := `SELECT id, recurring_id, name, category, amount, currency, date, tags, card, user_id FROM expenses WHERE id = $1`
	expense, err := scanExpense(s.db.QueryRow(query, id))
	if err != nil {
		if err == sql.ErrNoRows {
			return Expense{}, fmt.Errorf("expense with ID %s not found", id)
		}
		return Expense{}, fmt.Errorf("failed to get expense: %v", err)
	}
	return expense, nil
}

func (s *databaseStore) AddExpense(expense Expense) error {
	if expense.ID == "" {
		expense.ID = uuid.New().String()
	}
	if expense.Currency == "" {
		expense.Currency = s.defaults["currency"]
	}
	if expense.Date.IsZero() {
		expense.Date = time.Now()
	}
	tagsJSON, err := json.Marshal(expense.Tags)
	if err != nil {
		return err
	}
	query := `
		INSERT INTO expenses (id, recurring_id, name, category, amount, currency, date, tags, card, user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`
	_, err = s.db.Exec(query, expense.ID, expense.RecurringID, expense.Name, expense.Category, expense.Amount, expense.Currency, expense.Date, string(tagsJSON), expense.Card, expense.UserID)
	return err
}

func (s *databaseStore) UpdateExpense(id string, expense Expense) error {
	tagsJSON, err := json.Marshal(expense.Tags)
	if err != nil {
		return err
	}
	// TODO: revisit to maybe remove this later, might not be a good default for update
	if expense.Currency == "" {
		expense.Currency = s.defaults["currency"]
	}
	query := `
		UPDATE expenses
		SET name = $1, category = $2, amount = $3, currency = $4, date = $5, tags = $6, recurring_id = $7, card = $8, user_id = $9
		WHERE id = $10
	`
	result, err := s.db.Exec(query, expense.Name, expense.Category, expense.Amount, expense.Currency, expense.Date, string(tagsJSON), expense.RecurringID, expense.Card, expense.UserID, id)
	if err != nil {
		return fmt.Errorf("failed to update expense: %v", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %v", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("expense with ID %s not found", id)
	}
	return nil
}

func (s *databaseStore) RemoveExpense(id string) error {
	query := `DELETE FROM expenses WHERE id = $1`
	result, err := s.db.Exec(query, id)
	if err != nil {
		return fmt.Errorf("failed to delete expense: %v", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %v", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("expense with ID %s not found", id)
	}
	return nil
}

func (s *databaseStore) AddMultipleExpenses(expenses []Expense) error {
	if len(expenses) == 0 {
		return nil
	}
	// use the same addexpense method
	for _, exp := range expenses {
		if err := s.AddExpense(exp); err != nil {
			return err
		}
	}
	return nil
}

func (s *databaseStore) RemoveMultipleExpenses(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	query := `DELETE FROM expenses WHERE id = ANY($1)`
	_, err := s.db.Exec(query, pq.Array(ids))
	if err != nil {
		return fmt.Errorf("failed to delete multiple expenses: %v", err)
	}
	return nil
}

func scanRecurringExpense(scanner interface{ Scan(...any) error }) (RecurringExpense, error) {
	var re RecurringExpense
	var tagsStr sql.NullString
	var card sql.NullString
	var userID sql.NullString
	err := scanner.Scan(&re.ID, &re.Name, &re.Amount, &re.Currency, &re.Category, &re.StartDate, &re.Interval, &re.Occurrences, &tagsStr, &card, &userID)
	if err != nil {
		return RecurringExpense{}, err
	}
	if card.Valid {
		re.Card = card.String
	}
	if userID.Valid {
		re.UserID = userID.String
	}
	if tagsStr.Valid && tagsStr.String != "" {
		if err := json.Unmarshal([]byte(tagsStr.String), &re.Tags); err != nil {
			return RecurringExpense{}, fmt.Errorf("failed to parse tags for recurring expense %s: %v", re.ID, err)
		}
	}
	return re, nil
}

func (s *databaseStore) GetRecurringExpenses() ([]RecurringExpense, error) {
	query := `SELECT id, name, amount, currency, category, start_date, interval, occurrences, tags, card, user_id FROM recurring_expenses`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to query recurring expenses: %v", err)
	}
	defer rows.Close()
	var recurringExpenses []RecurringExpense
	for rows.Next() {
		re, err := scanRecurringExpense(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan recurring expense: %v", err)
		}
		recurringExpenses = append(recurringExpenses, re)
	}
	return recurringExpenses, nil
}

func (s *databaseStore) GetRecurringExpense(id string) (RecurringExpense, error) {
	query := `SELECT id, name, amount, currency, category, start_date, interval, occurrences, tags, card, user_id FROM recurring_expenses WHERE id = $1`
	re, err := scanRecurringExpense(s.db.QueryRow(query, id))
	if err != nil {
		if err == sql.ErrNoRows {
			return RecurringExpense{}, fmt.Errorf("recurring expense with ID %s not found", id)
		}
		return RecurringExpense{}, fmt.Errorf("failed to get recurring expense: %v", err)
	}
	return re, nil
}

func (s *databaseStore) AddRecurringExpense(recurringExpense RecurringExpense) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %v", err)
	}
	defer tx.Rollback() // Rollback on error

	if recurringExpense.ID == "" {
		recurringExpense.ID = uuid.New().String()
	}
	if recurringExpense.Currency == "" {
		recurringExpense.Currency = s.defaults["currency"]
	}
	tagsJSON, _ := json.Marshal(recurringExpense.Tags)
	ruleQuery := `
		INSERT INTO recurring_expenses (id, name, amount, currency, category, start_date, interval, occurrences, tags, card, user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`
	_, err = tx.Exec(ruleQuery, recurringExpense.ID, recurringExpense.Name, recurringExpense.Amount, recurringExpense.Currency, recurringExpense.Category, recurringExpense.StartDate, recurringExpense.Interval, recurringExpense.Occurrences, string(tagsJSON), recurringExpense.Card, recurringExpense.UserID)
	if err != nil {
		return fmt.Errorf("failed to insert recurring expense rule: %v", err)
	}

	expensesToAdd := generateExpensesFromRecurring(recurringExpense, false)
	if len(expensesToAdd) > 0 {
		stmt, err := tx.Prepare(pq.CopyIn("expenses", "id", "recurring_id", "name", "category", "amount", "currency", "date", "tags", "card", "user_id"))
		if err != nil {
			return fmt.Errorf("failed to prepare copy in: %v", err)
		}
		defer stmt.Close()
		for _, exp := range expensesToAdd {
			expTagsJSON, _ := json.Marshal(exp.Tags)
			_, err = stmt.Exec(exp.ID, exp.RecurringID, exp.Name, exp.Category, exp.Amount, exp.Currency, exp.Date, string(expTagsJSON), exp.Card, exp.UserID)
			if err != nil {
				return fmt.Errorf("failed to execute copy in: %v", err)
			}
		}
		if _, err = stmt.Exec(); err != nil {
			return fmt.Errorf("failed to finalize copy in: %v", err)
		}
	}
	return tx.Commit()
}

func (s *databaseStore) UpdateRecurringExpense(id string, recurringExpense RecurringExpense, updateAll bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %v", err)
	}
	defer tx.Rollback()
	recurringExpense.ID = id // Ensure ID is preserved
	if recurringExpense.Currency == "" {
		recurringExpense.Currency = s.defaults["currency"]
	}
	tagsJSON, _ := json.Marshal(recurringExpense.Tags)
	ruleQuery := `
		UPDATE recurring_expenses
		SET name = $1, amount = $2, category = $3, start_date = $4, interval = $5, occurrences = $6, tags = $7, currency = $8, card = $9, user_id = $10
		WHERE id = $11
	`
	res, err := tx.Exec(ruleQuery, recurringExpense.Name, recurringExpense.Amount, recurringExpense.Category, recurringExpense.StartDate, recurringExpense.Interval, recurringExpense.Occurrences, string(tagsJSON), recurringExpense.Currency, recurringExpense.Card, recurringExpense.UserID, id)
	if err != nil {
		return fmt.Errorf("failed to update recurring expense rule: %v", err)
	}
	rowsAffected, _ := res.RowsAffected()
	if rowsAffected == 0 {
		return fmt.Errorf("recurring expense with ID %s not found to update", id)
	}

	var deleteQuery string
	if updateAll {
		deleteQuery = `DELETE FROM expenses WHERE recurring_id = $1`
		_, err = tx.Exec(deleteQuery, id)
	} else {
		deleteQuery = `DELETE FROM expenses WHERE recurring_id = $1 AND date > $2`
		_, err = tx.Exec(deleteQuery, id, time.Now())
	}
	if err != nil {
		return fmt.Errorf("failed to delete old expense instances for update: %v", err)
	}

	expensesToAdd := generateExpensesFromRecurring(recurringExpense, !updateAll)
	if len(expensesToAdd) > 0 {
		stmt, err := tx.Prepare(pq.CopyIn("expenses", "id", "recurring_id", "name", "category", "amount", "currency", "date", "tags", "card", "user_id"))
		if err != nil {
			return fmt.Errorf("failed to prepare copy in for update: %v", err)
		}
		defer stmt.Close()
		for _, exp := range expensesToAdd {
			expTagsJSON, _ := json.Marshal(exp.Tags)
			_, err = stmt.Exec(exp.ID, exp.RecurringID, exp.Name, exp.Category, exp.Amount, exp.Currency, exp.Date, string(expTagsJSON), exp.Card, exp.UserID)
			if err != nil {
				return fmt.Errorf("failed to execute copy in for update: %v", err)
			}
		}
		if _, err = stmt.Exec(); err != nil {
			return fmt.Errorf("failed to finalize copy in for update: %v", err)
		}
	}
	return tx.Commit()
}

func (s *databaseStore) RemoveRecurringExpense(id string, removeAll bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %v", err)
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM recurring_expenses WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("failed to delete recurring expense rule: %v", err)
	}
	rowsAffected, _ := res.RowsAffected()
	if rowsAffected == 0 {
		return fmt.Errorf("recurring expense with ID %s not found", id)
	}

	var deleteQuery string
	if removeAll {
		deleteQuery = `DELETE FROM expenses WHERE recurring_id = $1`
		_, err = tx.Exec(deleteQuery, id)
	} else {
		deleteQuery = `DELETE FROM expenses WHERE recurring_id = $1 AND date > $2`
		_, err = tx.Exec(deleteQuery, id, time.Now())
	}
	if err != nil {
		return fmt.Errorf("failed to delete expense instances: %v", err)
	}
	return tx.Commit()
}

func generateExpensesFromRecurring(recExp RecurringExpense, fromToday bool) []Expense {
	var expenses []Expense
	currentDate := recExp.StartDate
	today := time.Now()
	occurrencesToGenerate := recExp.Occurrences
	if fromToday {
		for currentDate.Before(today) && (recExp.Occurrences == 0 || occurrencesToGenerate > 0) {
			switch recExp.Interval {
			case "daily":
				currentDate = currentDate.AddDate(0, 0, 1)
			case "weekly":
				currentDate = currentDate.AddDate(0, 0, 7)
			case "monthly":
				currentDate = currentDate.AddDate(0, 1, 0)
			case "yearly":
				currentDate = currentDate.AddDate(1, 0, 0)
			default:
				return expenses // Stop if interval is invalid
			}
			if recExp.Occurrences > 0 {
				occurrencesToGenerate--
			}
		}
	}
	limit := occurrencesToGenerate
	// if recExp.Occurrences == 0 {
	// 	limit = 2000 // Heuristic for "indefinite"
	// }

	for range limit {
		expense := Expense{
			ID:          uuid.New().String(),
			UserID:      recExp.UserID,
			RecurringID: recExp.ID,
			Name:        recExp.Name,
			Category:    recExp.Category,
			Card:        recExp.Card,
			Amount:      recExp.Amount,
			Currency:    recExp.Currency,
			Date:        currentDate,
			Tags:        recExp.Tags,
		}
		expenses = append(expenses, expense)
		switch recExp.Interval {
		case "daily":
			currentDate = currentDate.AddDate(0, 0, 1)
		case "weekly":
			currentDate = currentDate.AddDate(0, 0, 7)
		case "monthly":
			currentDate = currentDate.AddDate(0, 1, 0)
		case "yearly":
			currentDate = currentDate.AddDate(1, 0, 0)
		default:
			return expenses
		}
	}
	return expenses
}
