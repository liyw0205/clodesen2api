package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"clodesen2api/internal/config"
	"clodesen2api/internal/controlstore"
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
	control  *controlstore.Store
	revision int64
}

func OpenControlAccounts(control *controlstore.Store, path, strategy string, initial []upstream.AccountCredential) (*AccountStore, error) {
	s := &AccountStore{path: path, control: control}
	if _, err := control.Revision(context.Background(), "accounts"); errors.Is(err, controlstore.ErrMissing) {
		if _, err := os.Stat(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		var saved accountFile
		if data, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(data, &saved); err != nil {
				return nil, err
			}
			if saved.Version != 1 {
				return nil, errors.New("unsupported account file version")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		} else {
			saved = accountFile{Version: 1, Strategy: strategy, Accounts: initial}
		}
		if err := validateCredentials(saved.Accounts); err != nil {
			return nil, err
		}
		if saved.Strategy == "" {
			saved.Strategy = "fixed"
		}
		saved.Strategy = config.StoredStrategy(saved.Strategy)
		if saved.Strategy == "" {
			return nil, errors.New("invalid account strategy")
		}
		if err := control.Import(context.Background(), "accounts", path, saved); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	_, _, err := s.Snapshot()
	return s, err
}
func (s *AccountStore) syncLocked() error {
	if s.control == nil {
		return nil
	}
	var saved accountFile
	revision, err := s.control.Load(context.Background(), "accounts", &saved)
	if err != nil {
		return err
	}
	saved.Strategy = config.StoredStrategy(saved.Strategy)
	if saved.Version != 1 || saved.Strategy == "" {
		return errors.New("invalid account store")
	}
	if err := validateCredentials(saved.Accounts); err != nil {
		return err
	}
	s.accounts = copyCredentials(saved.Accounts)
	s.strategy = saved.Strategy
	s.revision = revision
	return nil
}
func (s *AccountStore) Snapshot() ([]upstream.AccountCredential, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.syncLocked(); err != nil {
		return nil, 0, err
	}
	return copyCredentials(s.accounts), s.revision, nil
}
func (s *AccountStore) ReplaceVersion(accounts []upstream.AccountCredential, revision int64) error {
	if s.control == nil {
		return s.Replace(accounts)
	}
	if err := validateCredentials(accounts); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.syncLocked(); err != nil {
		return err
	}
	if revision != s.revision {
		return controlstore.ErrConflict
	}
	next, err := s.control.Save(context.Background(), "accounts", revision, accountFile{Version: 1, Strategy: s.strategy, Accounts: accounts})
	if err != nil {
		return err
	}
	s.accounts = copyCredentials(accounts)
	s.revision = next
	return nil
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
		store.strategy = config.StoredStrategy(saved.Strategy)
		if store.strategy == "" {
			store.strategy = normalizeStrategy(strategy)
		}
		if store.strategy == "" {
			store.strategy = "fixed"
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
		store.strategy = "fixed"
	}
	if err := store.writeLocked(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *AccountStore) Accounts() []upstream.AccountCredential {
	accounts, _, _ := s.Snapshot()
	return accounts
}

func (s *AccountStore) Strategy() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.syncLocked()
	return s.strategy
}

func (s *AccountStore) Replace(accounts []upstream.AccountCredential) error {
	if s.control != nil {
		_, revision, err := s.Snapshot()
		if err != nil {
			return err
		}
		return s.ReplaceVersion(accounts, revision)
	}
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
		return errors.New("invalid account strategy")
	}
	if s.control != nil {
		return s.mutate(func(saved *accountFile) error { saved.Strategy = strategy; return nil })
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

// UpdateRuntimeState persists a state change learned from an upstream
// response. It intentionally does not alter credentials, so an automatic
// cooldown cannot overwrite a password or identifier update.
func (s *AccountStore) UpdateRuntimeState(accountID string, enabled bool, cooldownUntil *time.Time, cooldownReason string) error {
	if s.control != nil {
		return s.mutate(func(saved *accountFile) error {
			for i := range saved.Accounts {
				account := &saved.Accounts[i]
				if account.ID != accountID {
					continue
				}
				account.Enabled = enabled
				account.CooldownUntil = cooldownUntil
				account.CooldownReason = cooldownReason
				return nil
			}
			return errors.New("account not found")
		})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.accounts {
		if s.accounts[index].ID != accountID {
			continue
		}
		oldEnabled := s.accounts[index].Enabled
		oldCooldown := s.accounts[index].CooldownUntil
		oldReason := s.accounts[index].CooldownReason
		s.accounts[index].Enabled = enabled
		if cooldownUntil == nil {
			s.accounts[index].CooldownUntil = nil
			s.accounts[index].CooldownReason = ""
		} else {
			until := cooldownUntil.UTC()
			s.accounts[index].CooldownUntil = &until
			s.accounts[index].CooldownReason = strings.TrimSpace(cooldownReason)
		}
		if err := s.writeLocked(); err != nil {
			s.accounts[index].Enabled = oldEnabled
			s.accounts[index].CooldownUntil = oldCooldown
			s.accounts[index].CooldownReason = oldReason
			return err
		}
		return nil
	}
	return fmt.Errorf("account %q not found", accountID)
}

func (s *AccountStore) mutate(change func(*accountFile) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.control.Mutate(context.Background(), "accounts", func(raw json.RawMessage) (any, error) {
		var saved accountFile
		if err := json.Unmarshal(raw, &saved); err != nil {
			return nil, err
		}
		if saved.Version != 1 {
			return nil, errors.New("invalid accounts schema")
		}
		saved.Strategy = config.StoredStrategy(saved.Strategy)
		if saved.Strategy == "" {
			return nil, errors.New("invalid account strategy")
		}
		if err := change(&saved); err != nil {
			return nil, err
		}
		if err := validateCredentials(saved.Accounts); err != nil {
			return nil, err
		}
		return saved, nil
	})
	if err != nil {
		return err
	}
	return s.syncLocked()
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
	if len(accounts) > 100 {
		return errors.New("at most 100 accounts are supported")
	}
	ids := make(map[string]struct{}, len(accounts))
	identifiers := make(map[string]struct{}, len(accounts))
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
	}
	return nil
}

func copyCredentials(accounts []upstream.AccountCredential) []upstream.AccountCredential {
	result := append([]upstream.AccountCredential(nil), accounts...)
	for i := range result {
		if result[i].CooldownUntil != nil {
			until := *result[i].CooldownUntil
			result[i].CooldownUntil = &until
		}
	}
	return result
}

func normalizeStrategy(strategy string) string {
	return config.NormalizeStrategy(strategy)
}

func newAccountID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}
