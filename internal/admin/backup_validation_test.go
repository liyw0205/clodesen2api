package admin

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clodesen2api/internal/admission"
	"clodesen2api/internal/config"
	"clodesen2api/internal/controlstore"
)

func TestRestoreRejectsSemanticallyInvalidControlDocuments(t *testing.T) {
	dir := t.TempDir()
	db, err := controlstore.Open(filepath.Join(dir, "control.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	values := map[string]any{
		"api-keys":            json.RawMessage(`{"version":1,"keys":[]}`),
		"model-policies":      json.RawMessage(`{"catalog":[],"rules":{}}`),
		"inference-admission": map[string]any{"settings": admission.Defaults(), "revision": 1},
		"accounts":            accountFile{Version: 1, Strategy: "fixed"},
		"settings":            json.RawMessage(`{"schema":1,"values":{}}`),
	}
	for name, value := range values {
		if _, err := db.Save(ctx, name, 0, value); err != nil {
			t.Fatal(err)
		}
	}
	base := config.Config{}
	base.Server.Addr = "127.0.0.1:6687"
	base.Upstream.BaseURL, base.Upstream.OpenAIBaseURL, base.Upstream.LoginPath = "https://example.test", "https://example.test/v1", "/login"
	base.Upstream.LoginTimeout = 30 * time.Second
	base.Auth.Strategy = "fixed"
	base.Checkin.Path, base.Checkin.At, base.Checkin.Timezone, base.Redemption.At = "/checkin", "04:00", "Asia/Shanghai", "04:30"
	if err := ValidateControlBackup(ctx, dir, base); err != nil {
		t.Fatal(err)
	}
	for _, strategy := range []string{"weighted", "least_inflight", "latency"} {
		revision, _ := db.Revision(ctx, "accounts")
		if _, err := db.Save(ctx, "accounts", revision, json.RawMessage(`{"version":1,"strategy":"`+strategy+`","accounts":[]}`)); err != nil {
			t.Fatal(err)
		}
		if err := ValidateControlBackup(ctx, dir, base); err != nil {
			t.Fatalf("legacy account backup rejected: %v", err)
		}
	}
	for name, invalid := range map[string]json.RawMessage{
		"api-keys": json.RawMessage(`{"version":99,"keys":[]}`),
		"accounts": json.RawMessage(`{"version":1,"strategy":"invalid"}`),
		"settings": json.RawMessage(`{"schema":1,"values":{"checkin.timezone":"invalid/zone"}}`),
	} {
		revision, err := db.Revision(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Save(ctx, name, revision, invalid); err != nil {
			t.Fatal(err)
		}
		if err := ValidateControlBackup(ctx, dir, base); err == nil {
			t.Fatalf("invalid %s accepted", name)
		} else if name != "settings" && !strings.Contains(err.Error(), name) {
			t.Fatalf("wrong failure: %v", err)
		}
		if _, err := db.Save(ctx, name, revision+1, values[name]); err != nil {
			t.Fatal(err)
		}
	}
}
