package admin

import (
	"testing"
	"time"

	"clodesen2api/internal/config"
)

func TestCacheStatusUsesResourceTTLAndInvalidation(t *testing.T) {
	cfg := &config.Config{Cache: config.CacheSettings{AccountsSeconds: 30, ModelsSeconds: 86400, CreditsSeconds: 900}}
	s := &Service{cfg: cfg, accountCache: []byte(`{"accounts":[]}`), accountCachedAt: time.Now().Add(-time.Minute), modelCachedAt: time.Now().Add(-time.Hour), creditUsageCachedAt: time.Now().Add(-time.Minute)}
	states := s.cacheStatus()
	for key, want := range map[string]string{"accounts": "stale", "models": "hit", "credits": "hit"} {
		if states[key].(map[string]any)["cacheState"] != want {
			t.Fatalf("%s=%v", key, states[key])
		}
	}
	if s.cacheTTL("models") != 24*time.Hour {
		t.Fatal("wrong model TTL")
	}
	cfg.Cache.AccountsSeconds = 86400
	s.invalidateAccountCache()
	if s.cacheStatus()["accounts"].(map[string]any)["cacheState"] != "stale" {
		t.Fatal("long TTL kept invalidated cache fresh")
	}
}
