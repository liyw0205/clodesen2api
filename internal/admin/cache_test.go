package admin

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"clodesen2api/internal/upstream"
)

func TestPersistentAdminCacheRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "admin-cache.json")
	refreshed := time.Now().UTC().Truncate(time.Second)
	source := &Service{cachePath: path}
	source.accountCache = []byte(`{"accounts":[],"consumedCredits":0}`)
	source.accountCachedAt = refreshed
	source.modelCache = []byte(`{"object":"list","data":[]}`)
	source.modelCachedAt = refreshed
	source.creditUsageCache = []upstream.CreditUsageRecord{{AccountID: "account-one", Model: "gpt-5.6", Amount: -60}}
	source.creditUsageCachedAt = refreshed
	source.creditUsageReady = true
	source.persistCache()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("cache permissions = %o, want 600", info.Mode().Perm())
	}

	loaded := &Service{cachePath: path}
	loaded.loadPersistentCache()
	if string(loaded.accountCache) != string(source.accountCache) || string(loaded.modelCache) != string(source.modelCache) {
		t.Fatalf("cached JSON did not round trip: accounts=%s models=%s", loaded.accountCache, loaded.modelCache)
	}
	if !loaded.creditUsageReady || len(loaded.creditUsageCache) != 1 || loaded.creditUsageCache[0].Amount != -60 {
		t.Fatalf("credit cache did not round trip: ready=%v records=%+v", loaded.creditUsageReady, loaded.creditUsageCache)
	}
}

func TestHandlerWithoutConfigDoesNotPersistRuntimeCache(t *testing.T) {
	handler := NewHandlerWithConfig(nil, nil, "", "data/admin.token", nil)
	service, ok := handler.(*Service)
	if !ok {
		t.Fatal("handler is not an admin service")
	}
	if got := service.cachePath; got != "" {
		t.Fatalf("cache path = %q, want empty for an in-memory handler", got)
	}
}

func TestPersistentAdminCacheRefreshesLegacyCooldownStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin-cache.json")
	source := &Service{cachePath: path}
	source.accountCache = []byte(`{"accounts":[{"cooldownUntil":"2099-01-01T00:00:00Z"}]}`)
	source.accountCachedAt = time.Now().UTC()
	source.modelCache = []byte(`{"object":"list","data":[]}`)
	source.modelCachedAt = time.Now().UTC()
	source.persistCache()

	loaded := &Service{cachePath: path}
	loaded.loadPersistentCache()
	if len(loaded.accountCache) != 0 || !loaded.accountCachedAt.IsZero() {
		t.Fatalf("legacy account cache was retained: cache=%s cachedAt=%s", loaded.accountCache, loaded.accountCachedAt)
	}
	if len(loaded.modelCache) == 0 {
		t.Fatal("model cache should remain available")
	}
}

func TestPersistentAdminCacheRefreshesLegacyConsumedTotal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin-cache.json")
	source := &Service{cachePath: path}
	source.accountCache = []byte(`{"accounts":[]}`)
	source.accountCachedAt = time.Now().UTC()
	source.modelCache = []byte(`{"object":"list","data":[]}`)
	source.modelCachedAt = time.Now().UTC()
	source.persistCache()

	loaded := &Service{cachePath: path}
	loaded.loadPersistentCache()
	if len(loaded.accountCache) != 0 || !loaded.accountCachedAt.IsZero() {
		t.Fatalf("legacy consumed total cache was retained: cache=%s cachedAt=%s", loaded.accountCache, loaded.accountCachedAt)
	}
	if len(loaded.modelCache) == 0 {
		t.Fatal("model cache should remain available")
	}
}
