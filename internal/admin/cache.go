package admin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"clodesen2api/internal/upstream"
)

// persistentAdminCache contains only masked account responses, model data,
// and credit records. Credentials and session tokens are never written here.
type persistentAdminCache struct {
	Version             int                          `json:"version"`
	Accounts            json.RawMessage              `json:"accounts,omitempty"`
	AccountCachedAt     time.Time                    `json:"accountCachedAt,omitempty"`
	Models              json.RawMessage              `json:"models,omitempty"`
	ModelCachedAt       time.Time                    `json:"modelCachedAt,omitempty"`
	CreditUsage         []upstream.CreditUsageRecord `json:"creditUsage,omitempty"`
	CreditUsageCachedAt time.Time                    `json:"creditUsageCachedAt,omitempty"`
}

const persistentAdminCacheVersion = 2

func (s *Service) loadPersistentCache() {
	data, err := os.ReadFile(s.cachePath)
	if err != nil {
		return
	}
	var saved persistentAdminCache
	if json.Unmarshal(data, &saved) != nil || saved.Version != persistentAdminCacheVersion {
		return
	}
	// Account responses cached before cooldownReason was added cannot safely
	// distinguish a temporary ban from exhausted credits. Refresh that part
	// once while retaining the model and usage caches.
	if accountCacheNeedsReasonRefresh(saved.Accounts) {
		saved.Accounts = nil
		saved.AccountCachedAt = time.Time{}
	}
	s.cacheMu.Lock()
	s.accountCache = append([]byte(nil), saved.Accounts...)
	s.accountCachedAt = saved.AccountCachedAt
	s.modelCache = append([]byte(nil), saved.Models...)
	s.modelCachedAt = saved.ModelCachedAt
	s.creditUsageCache = append([]upstream.CreditUsageRecord(nil), saved.CreditUsage...)
	s.creditUsageCachedAt = saved.CreditUsageCachedAt
	s.creditUsageReady = !saved.CreditUsageCachedAt.IsZero()
	s.cacheMu.Unlock()
}

func accountCacheNeedsReasonRefresh(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil {
		return true
	}
	if _, ok := envelope["consumedCredits"]; !ok {
		return true
	}
	var payload struct {
		Accounts []struct {
			CooldownUntil  *time.Time `json:"cooldownUntil"`
			CooldownReason string     `json:"cooldownReason"`
		} `json:"accounts"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return true
	}
	for _, account := range payload.Accounts {
		if account.CooldownUntil != nil && account.CooldownReason == "" {
			return true
		}
	}
	return false
}

func (s *Service) persistCache() {
	if s == nil || s.cachePath == "" {
		return
	}
	s.cacheMu.Lock()
	saved := persistentAdminCache{
		Version:             persistentAdminCacheVersion,
		Accounts:            append(json.RawMessage(nil), s.accountCache...),
		AccountCachedAt:     s.accountCachedAt,
		Models:              append(json.RawMessage(nil), s.modelCache...),
		ModelCachedAt:       s.modelCachedAt,
		CreditUsage:         append([]upstream.CreditUsageRecord(nil), s.creditUsageCache...),
		CreditUsageCachedAt: s.creditUsageCachedAt,
	}
	s.cacheMu.Unlock()

	s.cacheWriteMu.Lock()
	defer s.cacheWriteMu.Unlock()
	dir := filepath.Dir(s.cachePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	file, err := os.CreateTemp(dir, ".admin-cache-*.tmp")
	if err != nil {
		return
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return
	}
	if err := json.NewEncoder(file).Encode(saved); err != nil {
		_ = file.Close()
		return
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return
	}
	if err := file.Close(); err != nil {
		return
	}
	_ = os.Rename(tempPath, s.cachePath)
}

func cloneCacheBytes(value []byte) []byte {
	return append([]byte(nil), value...)
}

func (s *Service) accountCacheSnapshot() ([]byte, time.Time) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	return cloneCacheBytes(s.accountCache), s.accountCachedAt
}

func (s *Service) modelCacheSnapshot() ([]byte, time.Time) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	return cloneCacheBytes(s.modelCache), s.modelCachedAt
}

func (s *Service) creditCacheSnapshot() ([]upstream.CreditUsageRecord, time.Time, bool) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	return append([]upstream.CreditUsageRecord(nil), s.creditUsageCache...), s.creditUsageCachedAt, s.creditUsageReady
}
