package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"clodesen2api/internal/controlstore"
)

func importControlFile(ctx context.Context, db *controlstore.Store, name, path string, fallback any, validate func([]byte) error) error {
	if _, err := db.Revision(ctx, name); err == nil {
		return nil
	} else if !errors.Is(err, controlstore.ErrMissing) {
		return err
	}
	value := fallback
	if strings.TrimSpace(path) != "" {
		file, err := os.Open(path)
		if err == nil {
			raw, readErr := io.ReadAll(io.LimitReader(file, (64<<20)+1))
			file.Close()
			if readErr != nil {
				return readErr
			}
			if len(raw) > 64<<20 {
				return errors.New("legacy control file exceeds import limit")
			}
			if err := validate(raw); err != nil {
				return err
			}
			value = json.RawMessage(raw)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return db.Import(ctx, name, path, value)
}

// MigrateControlData imports each legacy repository once; originals are retained
// for rollback and are no longer written by the production service.
func (s *Service) MigrateControlData(ctx context.Context) error {
	if s.control == nil {
		return nil
	}
	if err := importControlFile(ctx, s.control, "credit-ledger", s.localCreditPath, localCreditUsageFile{Version: 1}, func(raw []byte) error {
		var value localCreditUsageFile
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		if value.Version != 1 {
			return errors.New("unsupported credit ledger schema")
		}
		return nil
	}); err != nil {
		return err
	}
	if err := importControlFile(ctx, s.control, "redemption-history", s.redemptionPath, redemptionHistoryFile{Version: 1}, func(raw []byte) error {
		var value redemptionHistoryFile
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		if value.Version != 1 {
			return errors.New("unsupported redemption history schema")
		}
		return nil
	}); err != nil {
		return err
	}
	if err := importControlFile(ctx, s.control, "admin-cache", s.cachePath, persistentAdminCache{Version: persistentAdminCacheVersion}, func(raw []byte) error {
		var value persistentAdminCache
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		if value.Version != persistentAdminCacheVersion {
			return errors.New("unsupported admin cache schema")
		}
		return nil
	}); err != nil {
		return err
	}
	var cached persistentAdminCache
	if _, err := s.control.Load(ctx, "admin-cache", &cached); err != nil {
		return err
	}
	if accountCacheNeedsReasonRefresh(cached.Accounts) {
		cached.Accounts = nil
		cached.AccountCachedAt = time.Time{}
	}
	s.cacheMu.Lock()
	s.accountCache, s.accountCachedAt = cloneCacheBytes(cached.Accounts), cached.AccountCachedAt
	s.modelCache, s.modelCachedAt = cloneCacheBytes(cached.Models), cached.ModelCachedAt
	s.creditUsageCache, s.creditUsageCachedAt, s.creditUsageReady = cached.CreditUsage, cached.CreditUsageCachedAt, !cached.CreditUsageCachedAt.IsZero()
	s.cacheMu.Unlock()
	return nil
}

func (s *Service) saveControlCache(saved persistentAdminCache) error {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	return s.control.Mutate(ctx, "admin-cache", func(raw json.RawMessage) (any, error) {
		var current persistentAdminCache
		if err := json.Unmarshal(raw, &current); err != nil {
			return nil, err
		}
		if saved.AccountCachedAt.After(current.AccountCachedAt) {
			current.Accounts, current.AccountCachedAt = saved.Accounts, saved.AccountCachedAt
		}
		if saved.ModelCachedAt.After(current.ModelCachedAt) {
			current.Models, current.ModelCachedAt = saved.Models, saved.ModelCachedAt
		}
		if saved.CreditUsageCachedAt.After(current.CreditUsageCachedAt) {
			current.CreditUsage, current.CreditUsageCachedAt = saved.CreditUsage, saved.CreditUsageCachedAt
		}
		return current, nil
	})
}
