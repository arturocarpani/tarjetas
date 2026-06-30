package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// jsonStore implements Storage on the local filesystem with a per-user layout:
//
//	<baseDir>/users.json                      -> all user records
//	<baseDir>/users/<userID>/expenses.json    -> that user's expenses
//	<baseDir>/users/<userID>/config.json      -> that user's config (incl. recurring)
//
// A single RWMutex guards all file access. All writes go through writeFileAtomic
// (temp file + fsync + rename) so a crash or full disk can never corrupt a store.
type jsonStore struct {
	baseDir   string
	usersPath string
	mu        sync.RWMutex
}

type usersFileData struct {
	Users []User `json:"users"`
}

type expensesFileData struct {
	Expenses []Expense `json:"expenses"`
}

func InitializeJsonStore(baseConfig SystemConfig) (*jsonStore, error) {
	baseDir := baseConfig.StorageURL
	if err := os.MkdirAll(filepath.Join(baseDir, "users"), 0o755); err != nil {
		return nil, fmt.Errorf("failed to create storage directory: %w", err)
	}
	s := &jsonStore{
		baseDir:   baseDir,
		usersPath: filepath.Join(baseDir, "users.json"),
	}
	if _, err := os.Stat(s.usersPath); errors.Is(err, os.ErrNotExist) {
		if err := s.writeUsers(&usersFileData{Users: []User{}}); err != nil {
			return nil, fmt.Errorf("failed to create users file: %w", err)
		}
	}
	return s, nil
}

func (s *jsonStore) Close() error { return nil }

// ---- atomic write + path helpers -------------------------------------------

// writeFileAtomic writes data to a temp file in the same directory, fsyncs it,
// and renames it over path (atomic on POSIX; replace-existing on Windows).
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed
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
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	// Best-effort durability of the rename itself.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func (s *jsonStore) userDir(userID string) string  { return filepath.Join(s.baseDir, "users", userID) }
func (s *jsonStore) expensesPath(uid string) string { return filepath.Join(s.userDir(uid), "expenses.json") }
func (s *jsonStore) configPath(uid string) string   { return filepath.Join(s.userDir(uid), "config.json") }

// ---- low-level (no locking; callers hold s.mu) -----------------------------

func (s *jsonStore) readUsers() (*usersFileData, error) {
	content, err := os.ReadFile(s.usersPath)
	if err != nil {
		return nil, err
	}
	var d usersFileData
	if err := json.Unmarshal(content, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *jsonStore) writeUsers(d *usersFileData) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.usersPath, b)
}

func (s *jsonStore) readConfig(userID string) (*Config, error) {
	content, err := os.ReadFile(s.configPath(userID))
	if errors.Is(err, os.ErrNotExist) {
		c := &Config{}
		c.SetBaseConfig()
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(content, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *jsonStore) writeConfig(userID string, c *Config) error {
	if err := os.MkdirAll(s.userDir(userID), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.configPath(userID), b)
}

func (s *jsonStore) readExpenses(userID string) (*expensesFileData, error) {
	content, err := os.ReadFile(s.expensesPath(userID))
	if errors.Is(err, os.ErrNotExist) {
		return &expensesFileData{Expenses: []Expense{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var d expensesFileData
	if err := json.Unmarshal(content, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *jsonStore) writeExpenses(userID string, d *expensesFileData) error {
	if err := os.MkdirAll(s.userDir(userID), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.expensesPath(userID), b)
}

// appendExpenses adds rows without locking; callers must already hold s.mu.
func (s *jsonStore) appendExpenses(userID string, toAdd []Expense) error {
	if len(toAdd) == 0 {
		return nil
	}
	d, err := s.readExpenses(userID)
	if err != nil {
		return err
	}
	d.Expenses = append(d.Expenses, toAdd...)
	return s.writeExpenses(userID, d)
}

// defaultCurrency returns the user's configured currency (used when an expense
// is saved without one). Reads under the caller's existing lock.
func (s *jsonStore) defaultCurrency(userID string) string {
	c, err := s.readConfig(userID)
	if err != nil {
		return ""
	}
	return c.Currency
}

// ---- Users -----------------------------------------------------------------

func (s *jsonStore) CreateUser(u User) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.readUsers()
	if err != nil {
		return User{}, err
	}
	for _, e := range d.Users {
		if strings.EqualFold(e.Username, u.Username) {
			return User{}, ErrUsernameTaken
		}
	}
	if u.ID == "" {
		u.ID = uuid.New().String()
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now()
	}
	d.Users = append(d.Users, u)
	if err := s.writeUsers(d); err != nil {
		return User{}, err
	}
	c := &Config{}
	c.SetBaseConfig()
	if err := s.writeConfig(u.ID, c); err != nil {
		return User{}, err
	}
	if err := s.writeExpenses(u.ID, &expensesFileData{Expenses: []Expense{}}); err != nil {
		return User{}, err
	}
	return u, nil
}

func (s *jsonStore) GetUserByID(id string) (User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, err := s.readUsers()
	if err != nil {
		return User{}, err
	}
	for _, u := range d.Users {
		if u.ID == id {
			return u, nil
		}
	}
	return User{}, ErrNotFound
}

func (s *jsonStore) GetUserByUsername(username string) (User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, err := s.readUsers()
	if err != nil {
		return User{}, err
	}
	for _, u := range d.Users {
		if strings.EqualFold(u.Username, username) {
			return u, nil
		}
	}
	return User{}, ErrNotFound
}

func (s *jsonStore) GetUserByTelegramChatID(chatID int64) (User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if chatID == 0 {
		return User{}, ErrNotFound
	}
	d, err := s.readUsers()
	if err != nil {
		return User{}, err
	}
	for _, u := range d.Users {
		if u.TelegramChatID == chatID {
			return u, nil
		}
	}
	return User{}, ErrNotFound
}

func (s *jsonStore) ListUsers() ([]User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, err := s.readUsers()
	if err != nil {
		return nil, err
	}
	return d.Users, nil
}

func (s *jsonStore) UpdateUser(u User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.readUsers()
	if err != nil {
		return err
	}
	for i, e := range d.Users {
		if e.ID == u.ID {
			d.Users[i] = u
			return s.writeUsers(d)
		}
	}
	return ErrNotFound
}

func (s *jsonStore) DeleteUser(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.readUsers()
	if err != nil {
		return err
	}
	found := false
	out := d.Users[:0]
	for _, u := range d.Users {
		if u.ID == id {
			found = true
			continue
		}
		out = append(out, u)
	}
	if !found {
		return ErrNotFound
	}
	d.Users = out
	if err := s.writeUsers(d); err != nil {
		return err
	}
	return os.RemoveAll(s.userDir(id))
}

func (s *jsonStore) CountUsers() (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, err := s.readUsers()
	if err != nil {
		return 0, err
	}
	return len(d.Users), nil
}

// ---- Config (per user) -----------------------------------------------------

func (s *jsonStore) GetConfig(userID string) (*Config, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readConfig(userID)
}

func (s *jsonStore) GetCategories(userID string) ([]string, error) {
	c, err := s.GetConfig(userID)
	if err != nil {
		return nil, err
	}
	return c.Categories, nil
}

func (s *jsonStore) UpdateCategories(userID string, categories []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.readConfig(userID)
	if err != nil {
		return err
	}
	c.Categories = categories
	return s.writeConfig(userID, c)
}

func (s *jsonStore) GetCards(userID string) ([]string, error) {
	c, err := s.GetConfig(userID)
	if err != nil {
		return nil, err
	}
	return c.Cards, nil
}

func (s *jsonStore) UpdateCards(userID string, cards []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.readConfig(userID)
	if err != nil {
		return err
	}
	c.Cards = cards
	return s.writeConfig(userID, c)
}

func (s *jsonStore) GetCurrency(userID string) (string, error) {
	c, err := s.GetConfig(userID)
	if err != nil {
		return "", err
	}
	return c.Currency, nil
}

func (s *jsonStore) UpdateCurrency(userID string, currency string) error {
	if !slicesContains(SupportedCurrencies, currency) {
		return fmt.Errorf("invalid currency: %s", currency)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.readConfig(userID)
	if err != nil {
		return err
	}
	c.Currency = currency
	return s.writeConfig(userID, c)
}

func (s *jsonStore) GetStartDate(userID string) (int, error) {
	c, err := s.GetConfig(userID)
	if err != nil {
		return 0, err
	}
	return c.StartDate, nil
}

func (s *jsonStore) UpdateStartDate(userID string, startDate int) error {
	if startDate < 1 || startDate > 31 {
		return fmt.Errorf("invalid start date: %d", startDate)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.readConfig(userID)
	if err != nil {
		return err
	}
	c.StartDate = startDate
	return s.writeConfig(userID, c)
}

// ---- Recurring expenses (per user) -----------------------------------------

func (s *jsonStore) GetRecurringExpenses(userID string) ([]RecurringExpense, error) {
	c, err := s.GetConfig(userID)
	if err != nil {
		return nil, err
	}
	return c.RecurringExpenses, nil
}

func (s *jsonStore) GetRecurringExpense(userID string, id string) (RecurringExpense, error) {
	res, err := s.GetRecurringExpenses(userID)
	if err != nil {
		return RecurringExpense{}, err
	}
	for _, r := range res {
		if r.ID == id {
			return r, nil
		}
	}
	return RecurringExpense{}, ErrNotFound
}

func (s *jsonStore) AddRecurringExpense(userID string, re RecurringExpense) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.readConfig(userID)
	if err != nil {
		return err
	}
	if re.ID == "" {
		re.ID = uuid.New().String()
	}
	if re.Currency == "" {
		re.Currency = c.Currency
	}
	c.RecurringExpenses = append(c.RecurringExpenses, re)
	if err := s.writeConfig(userID, c); err != nil {
		return err
	}
	return s.appendExpenses(userID, generateExpensesFromRecurring(re, false))
}

func (s *jsonStore) RemoveRecurringExpense(userID string, id string, removeAll bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.readConfig(userID)
	if err != nil {
		return err
	}
	found := false
	kept := make([]RecurringExpense, 0, len(c.RecurringExpenses))
	for _, r := range c.RecurringExpenses {
		if r.ID == id {
			found = true
			continue
		}
		kept = append(kept, r)
	}
	if !found {
		return ErrNotFound
	}
	c.RecurringExpenses = kept
	data, err := s.readExpenses(userID)
	if err != nil {
		return err
	}
	today := time.Now()
	keptExp := make([]Expense, 0, len(data.Expenses))
	for _, exp := range data.Expenses {
		if exp.RecurringID != id {
			keptExp = append(keptExp, exp)
			continue
		}
		if !removeAll && !exp.Date.After(today) {
			keptExp = append(keptExp, exp) // preserve past instances
		}
	}
	data.Expenses = keptExp
	if err := s.writeExpenses(userID, data); err != nil {
		return err
	}
	return s.writeConfig(userID, c)
}

func (s *jsonStore) UpdateRecurringExpense(userID string, id string, re RecurringExpense, updateAll bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.readConfig(userID)
	if err != nil {
		return err
	}
	found := false
	for i, r := range c.RecurringExpenses {
		if r.ID == id {
			re.ID = id
			if re.Currency == "" {
				re.Currency = c.Currency
			}
			c.RecurringExpenses[i] = re
			found = true
			break
		}
	}
	if !found {
		return ErrNotFound
	}
	data, err := s.readExpenses(userID)
	if err != nil {
		return err
	}
	today := time.Now()
	kept := make([]Expense, 0, len(data.Expenses))
	for _, exp := range data.Expenses {
		if exp.RecurringID != id {
			kept = append(kept, exp)
			continue
		}
		if !updateAll && !exp.Date.After(today) {
			kept = append(kept, exp)
		}
	}
	kept = append(kept, generateExpensesFromRecurring(re, !updateAll)...)
	data.Expenses = kept
	if err := s.writeExpenses(userID, data); err != nil {
		return err
	}
	return s.writeConfig(userID, c)
}

// ---- Expenses (per user) ---------------------------------------------------

func (s *jsonStore) GetAllExpenses(userID string) ([]Expense, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, err := s.readExpenses(userID)
	if err != nil {
		return nil, err
	}
	return d.Expenses, nil
}

func (s *jsonStore) GetExpense(userID string, id string) (Expense, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, err := s.readExpenses(userID)
	if err != nil {
		return Expense{}, err
	}
	for _, exp := range d.Expenses {
		if exp.ID == id {
			return exp, nil
		}
	}
	return Expense{}, ErrNotFound
}

func (s *jsonStore) AddExpense(userID string, expense Expense) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.readExpenses(userID)
	if err != nil {
		return err
	}
	if expense.ID == "" {
		expense.ID = uuid.New().String()
	}
	if expense.Currency == "" {
		expense.Currency = s.defaultCurrency(userID)
	}
	if expense.Date.IsZero() {
		expense.Date = time.Now()
	}
	d.Expenses = append(d.Expenses, expense)
	return s.writeExpenses(userID, d)
}

func (s *jsonStore) RemoveExpense(userID string, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.readExpenses(userID)
	if err != nil {
		return err
	}
	found := false
	out := make([]Expense, 0, len(d.Expenses))
	for _, exp := range d.Expenses {
		if exp.ID == id {
			found = true
			continue
		}
		out = append(out, exp)
	}
	if !found {
		return ErrNotFound
	}
	d.Expenses = out
	return s.writeExpenses(userID, d)
}

func (s *jsonStore) AddMultipleExpenses(userID string, expenses []Expense) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendExpenses(userID, expenses)
}

func (s *jsonStore) RemoveMultipleExpenses(userID string, ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(ids) == 0 {
		return nil
	}
	d, err := s.readExpenses(userID)
	if err != nil {
		return err
	}
	remove := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		remove[id] = struct{}{}
	}
	out := make([]Expense, 0, len(d.Expenses))
	for _, exp := range d.Expenses {
		if _, drop := remove[exp.ID]; !drop {
			out = append(out, exp)
		}
	}
	d.Expenses = out
	return s.writeExpenses(userID, d)
}

func (s *jsonStore) UpdateExpense(userID string, id string, expense Expense) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.readExpenses(userID)
	if err != nil {
		return err
	}
	found := false
	for i, exp := range d.Expenses {
		if exp.ID == id {
			expense.ID = id
			if expense.Currency == "" {
				expense.Currency = s.defaultCurrency(userID)
			}
			d.Expenses[i] = expense
			found = true
			break
		}
	}
	if !found {
		return ErrNotFound
	}
	return s.writeExpenses(userID, d)
}

// slicesContains is a tiny local helper to avoid importing slices in this file.
func slicesContains(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}
