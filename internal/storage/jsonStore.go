package storage

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
)

// JSONStore implementats Storage interface - for JSON file storage
type jsonStore struct {
	configPath string
	filePath   string
	usersPath  string
	mu         sync.RWMutex
	usersMu    sync.RWMutex
	defaults   map[string]string // allows reusing defaults without querying for config
}

type expensesFileData struct {
	Expenses []Expense `json:"expenses"`
}

type usersFileData struct {
	Users []User `json:"users"`
}

func InitializeJsonStore(baseConfig SystemConfig) (*jsonStore, error) {
	configPath := filepath.Join(baseConfig.StorageURL, "config.json")
	filePath := filepath.Join(baseConfig.StorageURL, "expenses.json")
	usersPath := filepath.Join(baseConfig.StorageURL, "users.json")
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create storage directory: %v", err)
	}

	// create expenses file if it doesn't exist
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		initialData := expensesFileData{Expenses: []Expense{}}
		data, err := json.Marshal(initialData)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal initial data: %v", err)
		}
		if err := os.WriteFile(filePath, data, 0644); err != nil {
			return nil, fmt.Errorf("failed to create storage file: %v", err)
		}
		log.Println("Created expense storage file")
	} else {
		log.Println("Found existing expense storage file")
	}

	// create config file if it doesn't exist
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		initialConfig := Config{}
		initialConfig.SetBaseConfig()
		data, err := json.Marshal(initialConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal initial config: %v", err)
		}
		if err := os.WriteFile(configPath, data, 0644); err != nil {
			return nil, fmt.Errorf("failed to create config file: %v", err)
		}
		log.Println("Created expense storage config")
	} else {
		log.Println("Found existing expense storage config")
	}

	// create users file if it doesn't exist
	if _, err := os.Stat(usersPath); os.IsNotExist(err) {
		data, err := json.Marshal(usersFileData{Users: []User{}})
		if err != nil {
			return nil, fmt.Errorf("failed to marshal initial users data: %v", err)
		}
		if err := os.WriteFile(usersPath, data, 0644); err != nil {
			return nil, fmt.Errorf("failed to create users file: %v", err)
		}
		log.Println("Created users storage file")
	} else {
		log.Println("Found existing users storage file")
	}

	store := &jsonStore{
		configPath: configPath,
		filePath:   filePath,
		usersPath:  usersPath,
		defaults:   map[string]string{},
	}
	// Seed defaults from the persisted config so expenses added before an admin
	// re-saves settings (e.g. right after a restart) still get the configured
	// currency instead of an empty string.
	if cfg, err := store.GetConfig(); err == nil {
		store.defaults["currency"] = cfg.Currency
		store.defaults["start_date"] = fmt.Sprintf("%d", cfg.StartDate)
	}
	return store, nil
}

// ------------------------------------------------------------
// Users
// ------------------------------------------------------------

func (s *jsonStore) readUsersFile() (*usersFileData, error) {
	content, err := os.ReadFile(s.usersPath)
	if err != nil {
		return nil, err
	}
	var data usersFileData
	if err := json.Unmarshal(content, &data); err != nil {
		return nil, err
	}
	return &data, nil
}

func (s *jsonStore) writeUsersFile(data *usersFileData) error {
	content, err := json.MarshalIndent(data, "", "    ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.usersPath, content, 0644)
}

// writeFileAtomic writes data to a temp file in the same directory, fsyncs it,
// and renames it over the target. Rename is atomic on the same filesystem, so a
// crash/redeploy mid-write can never leave a truncated or empty file (which with
// os.WriteFile's O_TRUNC would lose ALL records, not just the last write).
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op if the rename succeeded
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func (s *jsonStore) CreateUser(user User) error {
	s.usersMu.Lock()
	defer s.usersMu.Unlock()
	data, err := s.readUsersFile()
	if err != nil {
		return fmt.Errorf("failed to read users file: %v", err)
	}
	for _, u := range data.Users {
		if u.Username == user.Username {
			return fmt.Errorf("username '%s' already exists", user.Username)
		}
	}
	if user.ID == "" {
		user.ID = uuid.New().String()
	}
	if user.CreatedAt.IsZero() {
		user.CreatedAt = time.Now()
	}
	data.Users = append(data.Users, user)
	return s.writeUsersFile(data)
}

func (s *jsonStore) GetUserByUsername(username string) (User, error) {
	s.usersMu.RLock()
	defer s.usersMu.RUnlock()
	data, err := s.readUsersFile()
	if err != nil {
		return User{}, err
	}
	for _, u := range data.Users {
		if u.Username == username {
			return u, nil
		}
	}
	return User{}, fmt.Errorf("user '%s' not found", username)
}

func (s *jsonStore) GetUserByID(id string) (User, error) {
	s.usersMu.RLock()
	defer s.usersMu.RUnlock()
	data, err := s.readUsersFile()
	if err != nil {
		return User{}, err
	}
	for _, u := range data.Users {
		if u.ID == id {
			return u, nil
		}
	}
	return User{}, fmt.Errorf("user with ID %s not found", id)
}

func (s *jsonStore) GetUserByTelegramID(telegramID string) (User, error) {
	s.usersMu.RLock()
	defer s.usersMu.RUnlock()
	if telegramID == "" {
		return User{}, fmt.Errorf("telegram ID cannot be empty")
	}
	data, err := s.readUsersFile()
	if err != nil {
		return User{}, err
	}
	for _, u := range data.Users {
		if u.TelegramID == telegramID {
			return u, nil
		}
	}
	return User{}, fmt.Errorf("user with Telegram ID %s not found", telegramID)
}

func (s *jsonStore) ListUsers() ([]User, error) {
	s.usersMu.RLock()
	defer s.usersMu.RUnlock()
	data, err := s.readUsersFile()
	if err != nil {
		return nil, err
	}
	return data.Users, nil
}

func (s *jsonStore) UpdateUserPassword(id, passwordHash string) error {
	s.usersMu.Lock()
	defer s.usersMu.Unlock()
	data, err := s.readUsersFile()
	if err != nil {
		return err
	}
	for i := range data.Users {
		if data.Users[i].ID == id {
			data.Users[i].PasswordHash = passwordHash
			return s.writeUsersFile(data)
		}
	}
	return fmt.Errorf("user with ID %s not found", id)
}

func (s *jsonStore) UpdateUserTelegramID(id, telegramID string) error {
	s.usersMu.Lock()
	defer s.usersMu.Unlock()
	data, err := s.readUsersFile()
	if err != nil {
		return err
	}
	// enforce uniqueness so two users can't share a chat.id
	if telegramID != "" {
		for i := range data.Users {
			if data.Users[i].TelegramID == telegramID && data.Users[i].ID != id {
				return fmt.Errorf("Telegram ID %s is already linked to another user", telegramID)
			}
		}
	}
	for i := range data.Users {
		if data.Users[i].ID == id {
			data.Users[i].TelegramID = telegramID
			return s.writeUsersFile(data)
		}
	}
	return fmt.Errorf("user with ID %s not found", id)
}

func (s *jsonStore) DeleteUser(id string) error {
	s.usersMu.Lock()
	defer s.usersMu.Unlock()
	data, err := s.readUsersFile()
	if err != nil {
		return err
	}
	found := false
	updated := make([]User, 0, len(data.Users))
	for _, u := range data.Users {
		if u.ID == id {
			found = true
		} else {
			updated = append(updated, u)
		}
	}
	if !found {
		return fmt.Errorf("user with ID %s not found", id)
	}
	data.Users = updated
	return s.writeUsersFile(data)
}

func (s *jsonStore) CountUsers() (int, error) {
	s.usersMu.RLock()
	defer s.usersMu.RUnlock()
	data, err := s.readUsersFile()
	if err != nil {
		return 0, err
	}
	return len(data.Users), nil
}

// primitive methods

func (s *jsonStore) readExpensesFile(path string) (*expensesFileData, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var data expensesFileData
	if err := json.Unmarshal(content, &data); err != nil {
		return nil, err
	}
	return &data, nil
}

func (s *jsonStore) writeExpensesFile(path string, data *expensesFileData) error {
	content, err := json.MarshalIndent(data, "", "    ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, content, 0644)
}

func (s *jsonStore) readConfigFile(path string) (*Config, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var data Config
	if err := json.Unmarshal(content, &data); err != nil {
		return nil, err
	}
	return &data, nil
}

func (s *jsonStore) writeConfigFile(path string, data *Config) error {
	content, err := json.MarshalIndent(data, "", "    ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, content, 0644)
}

// ------------------------------------------------------------
// JSONStore interface methods
// ------------------------------------------------------------

func (s *jsonStore) Close() error {
	return nil
}

func (s *jsonStore) GetConfig() (*Config, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readConfigFile(s.configPath)
}

// Basic Config Updates

func (s *jsonStore) GetCategories() ([]string, error) {
	config, err := s.GetConfig()
	if err != nil {
		return nil, err
	}
	return config.Categories, nil
}

func (s *jsonStore) UpdateCategories(categories []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.readConfigFile(s.configPath)
	if err != nil {
		return fmt.Errorf("failed to read config file: %v", err)
	}
	data.Categories = categories
	return s.writeConfigFile(s.configPath, data)
}

func (s *jsonStore) GetCards() ([]string, error) {
	config, err := s.GetConfig()
	if err != nil {
		return nil, err
	}
	return config.Cards, nil
}

func (s *jsonStore) UpdateCards(cards []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.readConfigFile(s.configPath)
	if err != nil {
		return fmt.Errorf("failed to read config file: %v", err)
	}
	data.Cards = cards
	return s.writeConfigFile(s.configPath, data)
}

func (s *jsonStore) GetCurrency() (string, error) {
	config, err := s.GetConfig()
	if err != nil {
		return "", err
	}
	return config.Currency, nil
}

func (s *jsonStore) UpdateCurrency(currency string) error {
	if !slices.Contains(SupportedCurrencies, currency) {
		return fmt.Errorf("invalid currency: %s", currency)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.readConfigFile(s.configPath)
	if err != nil {
		return fmt.Errorf("failed to read config file: %v", err)
	}
	data.Currency = currency
	s.defaults["currency"] = currency
	return s.writeConfigFile(s.configPath, data)
}

func (s *jsonStore) GetStartDate() (int, error) {
	config, err := s.GetConfig()
	if err != nil {
		return 0, err
	}
	return config.StartDate, nil
}

func (s *jsonStore) UpdateStartDate(startDate int) error {
	if startDate < 1 || startDate > 31 {
		return fmt.Errorf("invalid start date: %d", startDate)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.readConfigFile(s.configPath)
	if err != nil {
		return fmt.Errorf("failed to read config file: %v", err)
	}
	data.StartDate = startDate
	s.defaults["start_date"] = fmt.Sprintf("%d", startDate)
	return s.writeConfigFile(s.configPath, data)
}

func (s *jsonStore) GetRecurringExpenses() ([]RecurringExpense, error) {
	config, err := s.GetConfig()
	if err != nil {
		return nil, err
	}
	return config.RecurringExpenses, nil
}

func (s *jsonStore) GetRecurringExpense(id string) (RecurringExpense, error) {
	recurringExpenses, err := s.GetRecurringExpenses()
	if err != nil {
		return RecurringExpense{}, err
	}
	for _, r := range recurringExpenses {
		if r.ID == id {
			return r, nil
		}
	}
	return RecurringExpense{}, fmt.Errorf("recurring expense with ID %s not found", id)
}

func (s *jsonStore) AddRecurringExpense(recurringExpense RecurringExpense) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	config, err := s.readConfigFile(s.configPath)
	if err != nil {
		return fmt.Errorf("failed to read config file: %v", err)
	}
	if recurringExpense.ID == "" {
		recurringExpense.ID = uuid.New().String()
	}
	if recurringExpense.Currency == "" {
		recurringExpense.Currency = s.defaults["currency"]
	}
	config.RecurringExpenses = append(config.RecurringExpenses, recurringExpense)
	if err := s.writeConfigFile(s.configPath, config); err != nil {
		return fmt.Errorf("failed to write config file: %v", err)
	}
	expensesToAdd := generateExpensesFromRecurring(recurringExpense, false)
	return s.addMultipleLocked(expensesToAdd)
}

func (s *jsonStore) RemoveRecurringExpense(id string, removeAll bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	config, err := s.readConfigFile(s.configPath)
	if err != nil {
		return fmt.Errorf("failed to read config file: %v", err)
	}
	var found bool
	var updatedRecurringExpenses []RecurringExpense
	for _, r := range config.RecurringExpenses {
		if r.ID == id {
			found = true
		} else {
			updatedRecurringExpenses = append(updatedRecurringExpenses, r)
		}
	}
	if !found {
		return fmt.Errorf("recurring expense with ID %s not found", id)
	}
	config.RecurringExpenses = updatedRecurringExpenses
	expensesData, err := s.readExpensesFile(s.filePath)
	if err != nil {
		return fmt.Errorf("failed to read storage file: %v", err)
	}
	var updatedExpenses []Expense
	today := time.Now()
	for _, exp := range expensesData.Expenses {
		if exp.RecurringID != id {
			updatedExpenses = append(updatedExpenses, exp)
			continue
		}
		if !removeAll && !exp.Date.After(today) {
			updatedExpenses = append(updatedExpenses, exp)
		}
	}
	expensesData.Expenses = updatedExpenses
	if err := s.writeExpensesFile(s.filePath, expensesData); err != nil {
		return err
	}
	return s.writeConfigFile(s.configPath, config)
}

func (s *jsonStore) UpdateRecurringExpense(id string, recurringExpense RecurringExpense, updateAll bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	config, err := s.readConfigFile(s.configPath)
	if err != nil {
		return fmt.Errorf("failed to read config file: %v", err)
	}
	var found bool
	for i, r := range config.RecurringExpenses {
		if r.ID == id {
			recurringExpense.ID = id // Ensure ID is preserved
			if recurringExpense.Currency == "" {
				recurringExpense.Currency = s.defaults["currency"]
			}
			config.RecurringExpenses[i] = recurringExpense
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("recurring expense with ID %s not found", id)
	}
	expensesData, err := s.readExpensesFile(s.filePath)
	if err != nil {
		return fmt.Errorf("failed to read storage file: %v", err)
	}
	var remainingExpenses []Expense
	today := time.Now()
	for _, exp := range expensesData.Expenses {
		if exp.RecurringID != id {
			remainingExpenses = append(remainingExpenses, exp)
			continue
		}
		if !updateAll && !exp.Date.After(today) {
			remainingExpenses = append(remainingExpenses, exp)
		}
	}
	expensesData.Expenses = remainingExpenses
	expensesToAdd := generateExpensesFromRecurring(recurringExpense, !updateAll)
	expensesData.Expenses = append(expensesData.Expenses, expensesToAdd...)
	if err := s.writeExpensesFile(s.filePath, expensesData); err != nil {
		return err
	}
	return s.writeConfigFile(s.configPath, config)
}

// Expenses

func (s *jsonStore) GetAllExpenses() ([]Expense, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, err := s.readExpensesFile(s.filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read storage file: %v", err)
	}
	return data.Expenses, nil
}

func (s *jsonStore) GetExpense(id string) (Expense, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, err := s.readExpensesFile(s.filePath)
	if err != nil {
		return Expense{}, fmt.Errorf("failed to read storage file: %v", err)
	}
	for i, exp := range data.Expenses {
		if exp.ID == id {
			log.Printf("Retrieved expense with ID %s\n", id)
			return data.Expenses[i], nil
		}
	}
	return Expense{}, fmt.Errorf("expense with ID %s not found", id)
}

func (s *jsonStore) AddExpense(expense Expense) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.readExpensesFile(s.filePath)
	if err != nil {
		return fmt.Errorf("failed to read storage file: %v", err)
	}
	if expense.ID == "" {
		expense.ID = uuid.New().String()
	}
	if expense.Currency == "" {
		expense.Currency = s.defaults["currency"]
	}
	if expense.Date.IsZero() {
		expense.Date = time.Now()
	}
	data.Expenses = append(data.Expenses, expense)
	log.Printf("Added expense with ID %s\n", expense.ID)
	return s.writeExpensesFile(s.filePath, data)
}

func (s *jsonStore) RemoveExpense(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.readExpensesFile(s.filePath)
	if err != nil {
		return fmt.Errorf("failed to read storage file: %v", err)
	}
	found := false
	newExpenses := make([]Expense, 0, len(data.Expenses)-1)
	for _, exp := range data.Expenses {
		if exp.ID != id {
			newExpenses = append(newExpenses, exp)
		} else {
			found = true
		}
	}
	if !found {
		log.Printf("Expense with ID %s not found\n", id)
		return fmt.Errorf("expense with ID %s not found", id)
	}
	log.Printf("Deleted expense with ID %s\n", id)
	data.Expenses = newExpenses
	return s.writeExpensesFile(s.filePath, data)
}

// AddMultipleExpenses is the public, lock-taking entry point.
func (s *jsonStore) AddMultipleExpenses(expensesToAdd []Expense) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addMultipleLocked(expensesToAdd)
}

// addMultipleLocked appends expenses assuming s.mu is already held (callers that
// already hold the write lock, e.g. AddRecurringExpense, use this directly to
// avoid re-locking the non-reentrant RWMutex).
func (s *jsonStore) addMultipleLocked(expensesToAdd []Expense) error {
	if len(expensesToAdd) == 0 {
		return nil
	}
	data, err := s.readExpensesFile(s.filePath)
	if err != nil {
		return fmt.Errorf("failed to read storage file: %v", err)
	}
	data.Expenses = append(data.Expenses, expensesToAdd...)
	log.Printf("Added %d new recurring expense instances\n", len(expensesToAdd))
	return s.writeExpensesFile(s.filePath, data)
}

func (s *jsonStore) RemoveMultipleExpenses(ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(ids) == 0 {
		return nil
	}
	data, err := s.readExpensesFile(s.filePath)
	if err != nil {
		return fmt.Errorf("failed to read storage file: %v", err)
	}
	idsToRemove := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		idsToRemove[id] = struct{}{}
	}
	originalCount := len(data.Expenses)
	newExpenses := make([]Expense, 0, originalCount)
	for _, exp := range data.Expenses {
		if _, found := idsToRemove[exp.ID]; !found {
			newExpenses = append(newExpenses, exp)
		}
	}
	if len(newExpenses) == originalCount {
		log.Println("RemoveMultipleExpenses: no expenses found to remove")
		return nil
	}
	log.Printf("Removed %d expenses\n", originalCount-len(newExpenses))
	data.Expenses = newExpenses
	return s.writeExpensesFile(s.filePath, data)
}

func (s *jsonStore) UpdateExpense(id string, expense Expense) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.readExpensesFile(s.filePath)
	if err != nil {
		return fmt.Errorf("failed to read storage file: %v", err)
	}
	found := false
	for i, exp := range data.Expenses {
		if exp.ID == id {
			data.Expenses[i] = expense
			data.Expenses[i].ID = id
			if data.Expenses[i].Currency == "" {
				data.Expenses[i].Currency = s.defaults["currency"]
			}
			found = true
			break
		}
	}
	if !found {
		log.Printf("expense with ID %s not found\n", id)
		return fmt.Errorf("expense with ID %s not found", id)
	}
	log.Printf("Edited expense with ID %s\n", id)
	return s.writeExpensesFile(s.filePath, data)
}
