package config

import (
	"context"
	"encoding/json"
	"testing"
)

func TestRetiredStoredStrategiesMigrateButCannotBeSaved(t *testing.T) {
	for _, strategy := range []string{"weighted", "least_inflight", "latency"} {
		t.Run(strategy, func(t *testing.T) {
			db, cfg := settingsFixture(t)
			ctx := context.Background()
			raw, _ := json.Marshal(strategy)
			if _, err := db.Save(ctx, "settings", 0, settingsDocument{Schema: 1, Values: map[string]json.RawMessage{"auth.strategy": raw}}); err != nil {
				t.Fatal(err)
			}
			store, err := OpenSettings(ctx, db, cfg)
			if err != nil {
				t.Fatal(err)
			}
			view, err := store.View(ctx)
			if err != nil || view.Auth.Strategy != "fixed" || store.Startup().Auth.Strategy != "fixed" {
				t.Fatalf("stored strategy not migrated: %+v, %v", view.Auth, err)
			}
			invalid := view
			invalid.Auth.Strategy = strategy
			if _, _, err := store.Save(ctx, invalid); err == nil {
				t.Fatal("retired strategy accepted by settings save")
			}
			if _, _, err := store.Save(ctx, view); err != nil {
				t.Fatal(err)
			}
			var doc settingsDocument
			if _, err := db.Load(ctx, "settings", &doc); err != nil || string(doc.Values["auth.strategy"]) != `"fixed"` {
				t.Fatalf("next save did not normalize persisted strategy: %v", err)
			}
			for _, overrides := range []json.RawMessage{nil, json.RawMessage(`{"schema":1,"values":{"auth.strategy":` + string(raw) + `}}`)} {
				restored, err := BackupConfig(*cfg, []byte("[auth]\nstrategy = '"+strategy+"'\n"), overrides)
				if err != nil || restored.Auth.Strategy != "fixed" {
					t.Fatalf("legacy backup failed: %s, %v", restored.Auth.Strategy, err)
				}
			}
			cfg.Auth.Strategy = strategy
			fromFile, err := OpenSettings(ctx, db, cfg)
			if err != nil || fromFile.Startup().Auth.Strategy != "fixed" {
				t.Fatalf("legacy startup configuration failed: %v", err)
			}
		})
	}
	if NormalizeStrategy("unknown") != "" || StoredStrategy("unknown") != "" {
		t.Fatal("unknown strategy accepted")
	}
}
