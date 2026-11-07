package config

import (
	"context"
	"testing"
)

func TestCacheSettingsValidationHotUpdateAndRestartPersistence(t *testing.T) {
	db, cfg := settingsFixture(t)
	store, err := OpenSettings(context.Background(), db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	view, err := store.View(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if view.Cache != (CacheSettings{3600, 3600, 3600}) {
		t.Fatalf("defaults=%+v", view.Cache)
	}
	for _, value := range []int{-1, 29, 86401} {
		invalid := view
		invalid.Cache.ModelsSeconds = value
		if _, _, err := store.Save(context.Background(), invalid); err == nil {
			t.Fatalf("cache accepted %d", value)
		}
	}
	view.Cache = CacheSettings{30, 86400, 900}
	saved, hot, err := store.Save(context.Background(), view)
	if err != nil {
		t.Fatal(err)
	}
	if hot.Cache != view.Cache || saved.Fields["cache.accountsSeconds"].Pending || saved.Fields["cache.accountsSeconds"].Mode != "hot" {
		t.Fatalf("not hot: %+v", saved.Fields)
	}
	reopened, err := OpenSettings(context.Background(), db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Startup().Cache != view.Cache {
		t.Fatal("cache settings lost on restart")
	}
	other, err := OpenSettings(context.Background(), db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	latest, _ := other.View(context.Background())
	latest.Cache.CreditsSeconds = 1800
	if _, _, err := other.Save(context.Background(), latest); err != nil {
		t.Fatal(err)
	}
	_, hot, err = store.ViewAfter(context.Background())
	if err != nil || hot.Cache.CreditsSeconds != 1800 {
		t.Fatal("cross-instance cache settings not refreshed")
	}
}
