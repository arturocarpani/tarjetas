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
	db *sql.DB
}

// SQL queries as constants for reusability and clarity.
const (
	createUsersTableSQL = `
	CREATE TABLE IF NOT EXISTS users (
		id VARCHAR(36) PRIMARY KEY,
		username VARCHAR(255) UNIQUE NOT NULL,
		password_hash TEXT NOT NULL,
		is_admin BOOLEAN NOT NULL DEFAULT false,
		telegram_chat_id BIGINT NOT NULL DEFAULT 0,
		created_at TIMESTAMPTZ NOT NULL
	);`

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

	// config is keyed by user_id so each user owns exactly one config row.
	createConfigTableSQL = `
	CREATE TABLE IF NOT EXISTS config (
		user_id VARCHAR(36) PRIMARY KEY,
		categories TEXT NOT NULL,
		currency VARCHAR(255) NOT NULL,
		start_date INTEGER NOT NULL,
		cards TEXT
	);`

	// migrations: CREATE TABLE IF NOT EXISTS won't add columns to a pre-existing
	// table, so add the user-scoping / card columns idempotently here.
	migrateColumnsSQL = `
	ALTER TABLE expenses ADD COLUMN IF NOT EXISTS card TEXT;
	ALTER TABLE recurring_expenses ADD COLUMN IF NOT EXISTS card TEXT;
	ALTER TABLE config ADD COLUMN IF NOT EXISTS cards TEXT;
	ALTER TABLE expenses ADD COLUMN IF NOT EXISTS user_id VARCHAR(36) NOT NULL DEFAULT '';
	ALTER TABLE recurring_expenses ADD COLUMN IF NOT EXISTS user_id VARCHAR(36) NOT NULL DEFAULT '';`

	createIndexesSQL = `
	CREATE INDEX IF NOT EXISTS idx_expenses_user_date ON expenses(user_id, date DESC);
	CREATE INDEX IF NOT EXISTS idx_expenses_user_recurring ON expenses(user_id, recurring_id);`
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
	return &databaseStore{db: db}, nil
}

func makeDBURL(baseConfig SystemConfig) string {
	return fmt.Sprintf("postgres://%s:%s@%s?sslmode=%s", baseConfig.StorageUser, baseConfig.StoragePass, baseConfig.StorageURL, baseConfig.StorageSSL)
}

func createTables(db *sql.DB) error {
	for _, query := range []string{
		createUsersTableSQL,
		createExpensesTableSQL,
		createRecurringExpensesTableSQL,
		createConfigTableSQL,
		migrateColumnsSQL,
		createIndexesSQL,
	} {
		if _, err := db.Exec(query); err != nil {
			return err
		}
	}
	return nil
}

func (s *databaseStore) Close() error {
	return s.db.Close()
}

// isUniqueViolation reports whether err is a PostgreSQL unique-constraint
// violation (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	var pqErr *pq.Error
	if e, ok := err.(*pq.Error); ok {
		pqErr = e
	}
	return pqErr != nil && pqErr.Code == "23505"
}

// ---------------------------------------------------------------------------
// Users (global)
// ---------------------------------------------------------------------------

func (s *databaseStore) CreateUser(u User) (User, error) {
	if u.ID == "" {
		u.ID = uuid.New().String()
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now()
	}
	query := `
		INSERT INTO users (id, username, password_hash, is_admin, telegram_chat_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`
	_, err := s.db.Exec(query, u.ID, u.Username, u.PasswordHash, u.IsAdmin, u.TelegramChatID, u.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return User{}, ErrUsernameTaken
		}
		return User{}, fmt.Errorf("failed to create user: %v", err)
	}
	// Create the user's default config row.
	config := &Config{}
	config.SetBaseConfig()
	if err := s.saveConfig(u.ID, config); err != nil {
		return User{}, fmt.Errorf("failed to create default config for user: %v", err)
	}
	return u, nil
}

func scanUser(scanner interface{ Scan(...any) error }) (User, error) {
	var u User
	err := scanner.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.IsAdmin, &u.TelegramChatID, &u.CreatedAt)
	if err != nil {
		return User{}, err
	}
	return u, nil
}

const userSelectColumns = `id, username, password_hash, is_admin, telegram_chat_id, created_at`

func (s *databaseStore) GetUserByID(id string) (User, error) {
	query := `SELECT ` + userSelectColumns + ` FROM users WHERE id = $1`
	u, err := scanUser(s.db.QueryRow(query, id))
	if err != nil {
		if err == sql.ErrNoRows {
			return User{}, fmt.Errorf("user with ID %s: %w", id, ErrNotFound)
		}
		return User{}, fmt.Errorf("failed to get user: %v", err)
	}
	return u, nil
}

func (s *databaseStore) GetUserByUsername(username string) (User, error) {
	query := `SELECT ` + userSelectColumns + ` FROM users WHERE username = $1`
	u, err := scanUser(s.db.QueryRow(query, username))
	if err != nil {
		if err == sql.ErrNoRows {
			return User{}, fmt.Errorf("user %s: %w", username, ErrNotFound)
		}
		return User{}, fmt.Errorf("failed to get user: %v", err)
	}
	return u, nil
}

func (s *databaseStore) GetUserByTelegramChatID(chatID int64) (User, error) {
	query := `SELECT ` + userSelectColumns + ` FROM users WHERE telegram_chat_id = $1`
	u, err := scanUser(s.db.QueryRow(query, chatID))
	if err != nil {
		if err == sql.ErrNoRows {
			return User{}, fmt.Errorf("user with telegram chat ID %d: %w", chatID, ErrNotFound)
		}
		return User{}, fmt.Errorf("failed to get user: %v", err)
	}
	return u, nil
}

func (s *databaseStore) ListUsers() ([]User, error) {
	query := `SELECT ` + userSelectColumns + ` FROM users ORDER BY created_at ASC`
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

func (s *databaseStore) UpdateUser(u User) error {
	query := `
		UPDATE users
		SET username = $1, password_hash = $2, is_admin = $3, telegram_chat_id = $4
		WHERE id = $5
	`
	result, err := s.db.Exec(query, u.Username, u.PasswordHash, u.IsAdmin, u.TelegramChatID, u.ID)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrUsernameTaken
		}
		return fmt.Errorf("failed to update user: %v", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %v", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("user with ID %s: %w", u.ID, ErrNotFound)
	}
	return nil
}

func (s *databaseStore) DeleteUser(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %v", err)
	}
	defer tx.Rollback()

	res, err := tx.Exec(`DELETE FROM users WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("failed to delete user: %v", err)
	}
	rowsAffected, _ := res.RowsAffected()
	if rowsAffected == 0 {
		return fmt.Errorf("user with ID %s: %w", id, ErrNotFound)
	}

	if _, err := tx.Exec(`DELETE FROM expenses WHERE user_id = $1`, id); err != nil {
		return fmt.Errorf("failed to delete user expenses: %v", err)
	}
	if _, err := tx.Exec(`DELETE FROM recurring_expenses WHERE user_id = $1`, id); err != nil {
		return fmt.Errorf("failed to delete user recurring expenses: %v", err)
	}
	if _, err := tx.Exec(`DELETE FROM config WHERE user_id = $1`, id); err != nil {
		return fmt.Errorf("failed to delete user config: %v", err)
	}
	return tx.Commit()
}

func (s *databaseStore) CountUsers() (int, error) {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		return 0, fmt.Errorf("failed to count users: %v", err)
	}
	return count, nil
}

// ---------------------------------------------------------------------------
// Config (per user)
// ---------------------------------------------------------------------------

func (s *databaseStore) saveConfig(userID string, config *Config) error {
	categoriesJSON, err := json.Marshal(config.Categories)
	if err != nil {
		return fmt.Errorf("failed to marshal categories: %v", err)
	}
	cardsJSON, err := json.Marshal(config.Cards)
	if err != nil {
		return fmt.Errorf("failed to marshal cards: %v", err)
	}
	query := `
		INSERT INTO config (user_id, categories, cards, currency, start_date)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id) DO UPDATE SET
			categories = EXCLUDED.categories,
			cards = EXCLUDED.cards,
			currency = EXCLUDED.currency,
			start_date = EXCLUDED.start_date;
	`
	_, err = s.db.Exec(query, userID, string(categoriesJSON), string(cardsJSON), config.Currency, config.StartDate)
	return err
}

func (s *databaseStore) updateConfig(userID string, updater func(c *Config) error) error {
	config, err := s.GetConfig(userID)
	if err != nil {
		return err
	}
	if err := updater(config); err != nil {
		return err
	}
	return s.saveConfig(userID, config)
}

func (s *databaseStore) GetConfig(userID string) (*Config, error) {
	query := `SELECT categories, cards, currency, start_date FROM config WHERE user_id = $1`
	var categoriesStr, currency string
	var cardsStr sql.NullString
	var startDate int
	err := s.db.QueryRow(query, userID).Scan(&categoriesStr, &cardsStr, &currency, &startDate)

	if err != nil {
		if err == sql.ErrNoRows {
			config := &Config{}
			config.SetBaseConfig()
			if err := s.saveConfig(userID, config); err != nil {
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

	recurring, err := s.GetRecurringExpenses(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get recurring expenses for config: %v", err)
	}
	config.RecurringExpenses = recurring

	return &config, nil
}

func (s *databaseStore) GetCategories(userID string) ([]string, error) {
	config, err := s.GetConfig(userID)
	if err != nil {
		return nil, err
	}
	return config.Categories, nil
}

func (s *databaseStore) UpdateCategories(userID string, categories []string) error {
	return s.updateConfig(userID, func(c *Config) error {
		c.Categories = categories
		return nil
	})
}

func (s *databaseStore) GetCards(userID string) ([]string, error) {
	config, err := s.GetConfig(userID)
	if err != nil {
		return nil, err
	}
	return config.Cards, nil
}

func (s *databaseStore) UpdateCards(userID string, cards []string) error {
	return s.updateConfig(userID, func(c *Config) error {
		c.Cards = cards
		return nil
	})
}

func (s *databaseStore) GetCurrency(userID string) (string, error) {
	config, err := s.GetConfig(userID)
	if err != nil {
		return "", err
	}
	return config.Currency, nil
}

func (s *databaseStore) UpdateCurrency(userID string, currency string) error {
	if !slices.Contains(SupportedCurrencies, currency) {
		return fmt.Errorf("invalid currency: %s", currency)
	}
	return s.updateConfig(userID, func(c *Config) error {
		c.Currency = currency
		return nil
	})
}

func (s *databaseStore) GetStartDate(userID string) (int, error) {
	config, err := s.GetConfig(userID)
	if err != nil {
		return 0, err
	}
	return config.StartDate, nil
}

func (s *databaseStore) UpdateStartDate(userID string, startDate int) error {
	if startDate < 1 || startDate > 31 {
		return fmt.Errorf("invalid start date: %d", startDate)
	}
	return s.updateConfig(userID, func(c *Config) error {
		c.StartDate = startDate
		return nil
	})
}

// userDefaultCurrency returns the currency configured for the user, used when an
// expense is added without an explicit currency.
func (s *databaseStore) userDefaultCurrency(userID string) string {
	config, err := s.GetConfig(userID)
	if err != nil {
		return ""
	}
	return config.Currency
}

// ---------------------------------------------------------------------------
// Expenses (per user)
// ---------------------------------------------------------------------------

func scanExpense(scanner interface{ Scan(...any) error }) (Expense, error) {
	var expense Expense
	var tagsStr sql.NullString
	var recurringID sql.NullString
	var card sql.NullString
	err := scanner.Scan(&expense.ID, &recurringID, &expense.Name, &expense.Category, &expense.Amount, &expense.Currency, &expense.Date, &tagsStr, &card)
	if err != nil {
		return Expense{}, err
	}
	if recurringID.Valid {
		expense.RecurringID = recurringID.String
	}
	if card.Valid {
		expense.Card = card.String
	}
	if tagsStr.Valid && tagsStr.String != "" {
		if err := json.Unmarshal([]byte(tagsStr.String), &expense.Tags); err != nil {
			return Expense{}, fmt.Errorf("failed to parse tags for expense %s: %v", expense.ID, err)
		}
	}
	return expense, nil
}

func (s *databaseStore) GetAllExpenses(userID string) ([]Expense, error) {
	query := `SELECT id, recurring_id, name, category, amount, currency, date, tags, card FROM expenses WHERE user_id = $1 ORDER BY date DESC`
	rows, err := s.db.Query(query, userID)
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

func (s *databaseStore) GetExpense(userID string, id string) (Expense, error) {
	query := `SELECT id, recurring_id, name, category, amount, currency, date, tags, card FROM expenses WHERE user_id = $1 AND id = $2`
	expense, err := scanExpense(s.db.QueryRow(query, userID, id))
	if err != nil {
		if err == sql.ErrNoRows {
			return Expense{}, fmt.Errorf("expense with ID %s: %w", id, ErrNotFound)
		}
		return Expense{}, fmt.Errorf("failed to get expense: %v", err)
	}
	return expense, nil
}

func (s *databaseStore) AddExpense(userID string, expense Expense) error {
	if expense.ID == "" {
		expense.ID = uuid.New().String()
	}
	if expense.Currency == "" {
		expense.Currency = s.userDefaultCurrency(userID)
	}
	if expense.Date.IsZero() {
		expense.Date = time.Now()
	}
	tagsJSON, err := json.Marshal(expense.Tags)
	if err != nil {
		return err
	}
	query := `
		INSERT INTO expenses (id, user_id, recurring_id, name, category, amount, currency, date, tags, card)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`
	_, err = s.db.Exec(query, expense.ID, userID, expense.RecurringID, expense.Name, expense.Category, expense.Amount, expense.Currency, expense.Date, string(tagsJSON), expense.Card)
	return err
}

func (s *databaseStore) UpdateExpense(userID string, id string, expense Expense) error {
	tagsJSON, err := json.Marshal(expense.Tags)
	if err != nil {
		return err
	}
	if expense.Currency == "" {
		expense.Currency = s.userDefaultCurrency(userID)
	}
	query := `
		UPDATE expenses
		SET name = $1, category = $2, amount = $3, currency = $4, date = $5, tags = $6, recurring_id = $7, card = $8
		WHERE id = $9 AND user_id = $10
	`
	result, err := s.db.Exec(query, expense.Name, expense.Category, expense.Amount, expense.Currency, expense.Date, string(tagsJSON), expense.RecurringID, expense.Card, id, userID)
	if err != nil {
		return fmt.Errorf("failed to update expense: %v", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %v", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("expense with ID %s: %w", id, ErrNotFound)
	}
	return nil
}

func (s *databaseStore) RemoveExpense(userID string, id string) error {
	query := `DELETE FROM expenses WHERE id = $1 AND user_id = $2`
	result, err := s.db.Exec(query, id, userID)
	if err != nil {
		return fmt.Errorf("failed to delete expense: %v", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %v", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("expense with ID %s: %w", id, ErrNotFound)
	}
	return nil
}

func (s *databaseStore) AddMultipleExpenses(userID string, expenses []Expense) error {
	if len(expenses) == 0 {
		return nil
	}
	// reuse the single AddExpense method so the per-user currency default and
	// id/date generation logic stays in one place.
	for _, exp := range expenses {
		if err := s.AddExpense(userID, exp); err != nil {
			return err
		}
	}
	return nil
}

func (s *databaseStore) RemoveMultipleExpenses(userID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	query := `DELETE FROM expenses WHERE user_id = $1 AND id = ANY($2)`
	_, err := s.db.Exec(query, userID, pq.Array(ids))
	if err != nil {
		return fmt.Errorf("failed to delete multiple expenses: %v", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Recurring Expenses (per user)
// ---------------------------------------------------------------------------

func scanRecurringExpense(scanner interface{ Scan(...any) error }) (RecurringExpense, error) {
	var re RecurringExpense
	var tagsStr sql.NullString
	var card sql.NullString
	err := scanner.Scan(&re.ID, &re.Name, &re.Amount, &re.Currency, &re.Category, &re.StartDate, &re.Interval, &re.Occurrences, &tagsStr, &card)
	if err != nil {
		return RecurringExpense{}, err
	}
	if card.Valid {
		re.Card = card.String
	}
	if tagsStr.Valid && tagsStr.String != "" {
		if err := json.Unmarshal([]byte(tagsStr.String), &re.Tags); err != nil {
			return RecurringExpense{}, fmt.Errorf("failed to parse tags for recurring expense %s: %v", re.ID, err)
		}
	}
	return re, nil
}

func (s *databaseStore) GetRecurringExpenses(userID string) ([]RecurringExpense, error) {
	query := `SELECT id, name, amount, currency, category, start_date, interval, occurrences, tags, card FROM recurring_expenses WHERE user_id = $1`
	rows, err := s.db.Query(query, userID)
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

func (s *databaseStore) GetRecurringExpense(userID string, id string) (RecurringExpense, error) {
	query := `SELECT id, name, amount, currency, category, start_date, interval, occurrences, tags, card FROM recurring_expenses WHERE user_id = $1 AND id = $2`
	re, err := scanRecurringExpense(s.db.QueryRow(query, userID, id))
	if err != nil {
		if err == sql.ErrNoRows {
			return RecurringExpense{}, fmt.Errorf("recurring expense with ID %s: %w", id, ErrNotFound)
		}
		return RecurringExpense{}, fmt.Errorf("failed to get recurring expense: %v", err)
	}
	return re, nil
}

func (s *databaseStore) AddRecurringExpense(userID string, recurringExpense RecurringExpense) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %v", err)
	}
	defer tx.Rollback() // Rollback on error

	if recurringExpense.ID == "" {
		recurringExpense.ID = uuid.New().String()
	}
	if recurringExpense.Currency == "" {
		recurringExpense.Currency = s.userDefaultCurrency(userID)
	}
	tagsJSON, _ := json.Marshal(recurringExpense.Tags)
	ruleQuery := `
		INSERT INTO recurring_expenses (id, user_id, name, amount, currency, category, start_date, interval, occurrences, tags, card)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`
	_, err = tx.Exec(ruleQuery, recurringExpense.ID, userID, recurringExpense.Name, recurringExpense.Amount, recurringExpense.Currency, recurringExpense.Category, recurringExpense.StartDate, recurringExpense.Interval, recurringExpense.Occurrences, string(tagsJSON), recurringExpense.Card)
	if err != nil {
		return fmt.Errorf("failed to insert recurring expense rule: %v", err)
	}

	expensesToAdd := generateExpensesFromRecurring(recurringExpense, false)
	if err := copyInExpenses(tx, userID, expensesToAdd); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *databaseStore) UpdateRecurringExpense(userID string, id string, recurringExpense RecurringExpense, updateAll bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %v", err)
	}
	defer tx.Rollback()
	recurringExpense.ID = id // Ensure ID is preserved
	if recurringExpense.Currency == "" {
		recurringExpense.Currency = s.userDefaultCurrency(userID)
	}
	tagsJSON, _ := json.Marshal(recurringExpense.Tags)
	ruleQuery := `
		UPDATE recurring_expenses
		SET name = $1, amount = $2, category = $3, start_date = $4, interval = $5, occurrences = $6, tags = $7, currency = $8, card = $9
		WHERE id = $10 AND user_id = $11
	`
	res, err := tx.Exec(ruleQuery, recurringExpense.Name, recurringExpense.Amount, recurringExpense.Category, recurringExpense.StartDate, recurringExpense.Interval, recurringExpense.Occurrences, string(tagsJSON), recurringExpense.Currency, recurringExpense.Card, id, userID)
	if err != nil {
		return fmt.Errorf("failed to update recurring expense rule: %v", err)
	}
	rowsAffected, _ := res.RowsAffected()
	if rowsAffected == 0 {
		return fmt.Errorf("recurring expense with ID %s: %w", id, ErrNotFound)
	}

	if updateAll {
		_, err = tx.Exec(`DELETE FROM expenses WHERE user_id = $1 AND recurring_id = $2`, userID, id)
	} else {
		_, err = tx.Exec(`DELETE FROM expenses WHERE user_id = $1 AND recurring_id = $2 AND date > $3`, userID, id, time.Now())
	}
	if err != nil {
		return fmt.Errorf("failed to delete old expense instances for update: %v", err)
	}

	expensesToAdd := generateExpensesFromRecurring(recurringExpense, !updateAll)
	if err := copyInExpenses(tx, userID, expensesToAdd); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *databaseStore) RemoveRecurringExpense(userID string, id string, removeAll bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %v", err)
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM recurring_expenses WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("failed to delete recurring expense rule: %v", err)
	}
	rowsAffected, _ := res.RowsAffected()
	if rowsAffected == 0 {
		return fmt.Errorf("recurring expense with ID %s: %w", id, ErrNotFound)
	}

	if removeAll {
		_, err = tx.Exec(`DELETE FROM expenses WHERE user_id = $1 AND recurring_id = $2`, userID, id)
	} else {
		_, err = tx.Exec(`DELETE FROM expenses WHERE user_id = $1 AND recurring_id = $2 AND date > $3`, userID, id, time.Now())
	}
	if err != nil {
		return fmt.Errorf("failed to delete expense instances: %v", err)
	}
	return tx.Commit()
}

// copyInExpenses bulk-inserts the generated expense instances for a recurring
// expense, carrying the owning userID into each row. generateExpensesFromRecurring
// returns rows without a user_id, so it is supplied here.
func copyInExpenses(tx *sql.Tx, userID string, expensesToAdd []Expense) error {
	if len(expensesToAdd) == 0 {
		return nil
	}
	stmt, err := tx.Prepare(pq.CopyIn("expenses", "id", "user_id", "recurring_id", "name", "category", "amount", "currency", "date", "tags", "card"))
	if err != nil {
		return fmt.Errorf("failed to prepare copy in: %v", err)
	}
	defer stmt.Close()
	for _, exp := range expensesToAdd {
		expTagsJSON, _ := json.Marshal(exp.Tags)
		_, err = stmt.Exec(exp.ID, userID, exp.RecurringID, exp.Name, exp.Category, exp.Amount, exp.Currency, exp.Date, string(expTagsJSON), exp.Card)
		if err != nil {
			return fmt.Errorf("failed to execute copy in: %v", err)
		}
	}
	if _, err = stmt.Exec(); err != nil {
		return fmt.Errorf("failed to finalize copy in: %v", err)
	}
	return nil
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
