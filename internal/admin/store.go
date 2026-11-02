package admin

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"clodesen2api/internal/upstream"
)

type accountFile struct {
	Version  int                          `json:"version"`
	Strategy string                       `json:"strategy"`
	Accounts []upstream.AccountCredential `json:"accounts"`
}

type AccountStore struct {
	mu       sync.RWMutex
	path     string
	accounts []upstream.AccountCredential
	strategy string
}

func OpenAccountStore(path, strategy string, initial []upstream.AccountCredential) (*AccountStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("admin accounts file path is empty")
	}
	store := &AccountStore{path: path}
	data, err := os.ReadFile(path)
	if err == nil {
		var saved accountFile
		if err := json.Unmarshal(data, &saved); err != nil {
			return nil, fmt.Errorf("decode admin accounts file: %w", err)
		}
		if err := validateCredentials(saved.Accounts); err != nil {
			return nil, fmt.Errorf("invalid admin accounts file: %w", err)
		}
		store.accounts = copyCredentials(saved.Accounts)
		store.strategy = normalizeStrategy(saved.Strategy)
		if store.strategy == "" {
			store.strategy = normalizeStrategy(strategy)
		}
		if store.strategy == "" {
			store.strategy = "round_robin"
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, fmt.Errorf("restrict admin accounts file permissions: %w", err)
		}
		return store, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read admin accounts file: %w", err)
	}
	if err := validateCredentials(initial); err != nil {
		return nil, fmt.Errorf("initial accounts: %w", err)
	}
	store.accounts = copyCredentials(initial)
	store.strategy = normalizeStrategy(strategy)
	if store.strategy == "" {
		store.strategy = "round_robin"
	}
	if err := store.writeLocked(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *AccountStore) Accounts() []upstream.AccountCredential {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return copyCredentials(s.accounts)
}

func (s *AccountStore) Strategy() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.strategy
}

func (s *AccountStore) Replace(accounts []upstream.AccountCredential) error {
	if err := validateCredentials(accounts); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.accounts
	s.accounts = copyCredentials(accounts)
	if err := s.writeLocked(); err != nil {
		s.accounts = old
		return err
	}
	return nil
}

func (s *AccountStore) SetStrategy(strategy string) error {
	strategy = normalizeStrategy(strategy)
	if strategy == "" {
		return errors.New("strategy must be round_robin or random")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.strategy
	s.strategy = strategy
	if err := s.writeLocked(); err != nil {
		s.strategy = old
		return err
	}
	return nil
}

func (s *AccountStore) writeLocked() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create admin data directory: %w", err)
	}
	file, err := os.CreateTemp(dir, ".accounts-*.tmp")
	if err != nil {
		return fmt.Errorf("create admin data file: %w", err)
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("restrict admin data permissions: %w", err)
	}
	data := accountFile{Version: 1, Strategy: s.strategy, Accounts: s.accounts}
	if err := json.NewEncoder(file).Encode(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("encode admin data: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync admin data: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close admin data: %w", err)
	}
	if err := os.Rename(tempPath, s.path); err != nil {
		return fmt.Errorf("replace admin data: %w", err)
	}
	return nil
}

func validateCredentials(accounts []upstream.AccountCredential) error {
	if len(accounts) == 0 {
		return errors.New("at least one account is required")
	}
	if len(accounts) > 100 {
		return errors.New("at most 100 accounts are supported")
	}
	ids := make(map[string]struct{}, len(accounts))
	identifiers := make(map[string]struct{}, len(accounts))
	enabled := false
	for _, account := range accounts {
		if strings.TrimSpace(account.ID) == "" || strings.TrimSpace(account.Identifier) == "" || strings.TrimSpace(account.Password) == "" {
			return errors.New("account id, identifier, and password are required")
		}
		if _, exists := ids[account.ID]; exists {
			return errors.New("duplicate account id")
		}
		key := strings.ToLower(strings.TrimSpace(account.Identifier))
		if _, exists := identifiers[key]; exists {
			return errors.New("duplicate account identifier")
		}
		ids[account.ID] = struct{}{}
		identifiers[key] = struct{}{}
		enabled = enabled || account.Enabled
	}
	if !enabled {
		return errors.New("at least one account must be enabled")
	}
	return nil
}

func copyCredentials(accounts []upstream.AccountCredential) []upstream.AccountCredential {
	return append([]upstream.AccountCredential(nil), accounts...)
}

func normalizeStrategy(strategy string) string {
	strategy = strings.ToLower(strings.TrimSpace(strategy))
	if strategy == "round_robin" || strategy == "random" {
		return strategy
	}
	return ""
}

func newAccountID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}
