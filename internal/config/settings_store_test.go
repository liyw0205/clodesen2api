package config

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clodesen2api/internal/controlstore"
	"github.com/lfhy/flag"
)

func settingsFixture(t *testing.T) (*controlstore.Store, *Config) {
	t.Helper()
	dir := t.TempDir()
	db, err := controlstore.Open(filepath.Join(dir, "control.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cfg := &Config{path: filepath.Join(dir, "config.toml")}
	cfg.Server.Addr = "127.0.0.1:6687"
	cfg.Upstream.BaseURL, cfg.Upstream.OpenAIBaseURL = "https://example.test", "https://example.test/api/openai"
	cfg.Upstream.LoginPath, cfg.Upstream.LoginTimeout = "/api/login", 30*time.Second
	cfg.Auth.Strategy, cfg.Auth.RPM = "fixed", 4
	cfg.Checkin.Path, cfg.Checkin.At, cfg.Checkin.Timezone = "/api/checkin", "04:00", "Asia/Shanghai"
	cfg.Redemption.At, cfg.Redemption.Code = "04:30", "private-code"
	return db, cfg
}

func TestSettingsRevisionLocksAndRestartBoundary(t *testing.T) {
	db, cfg := settingsFixture(t)
	cfg.sources = map[string]string{"upstream.baseURL": "environment"}
	ctx := context.Background()
	store, err := OpenSettings(ctx, db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	view, err := store.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Fields["upstream.baseURL"].Locked || view.Redemption.Code != "" || !view.Redemption.CodeConfigured {
		t.Fatalf("unexpected metadata: %+v", view.Fields)
	}
	locked := view
	locked.Upstream.BaseURL = "https://different.test"
	if _, _, err := store.Save(ctx, locked); err == nil {
		t.Fatal("locked field accepted")
	}
	invalid := view
	invalid.Checkin.Timezone = "unknown/timezone"
	if _, _, err := store.Save(ctx, invalid); err == nil {
		t.Fatal("invalid timezone accepted")
	}
	view.Auth.RPM, view.Server.Addr = 9, "127.0.0.1:7788"
	saved, hot, err := store.Save(ctx, view)
	if err != nil {
		t.Fatal(err)
	}
	if hot.Auth.RPM != 9 || hot.Server.Addr != cfg.Server.Addr || !saved.Fields["server.addr"].Pending {
		t.Fatalf("incorrect runtime boundary: %+v", saved.Fields)
	}
	if hot.Redemption.Code != "private-code" {
		t.Fatal("redacted response cleared secret")
	}
	if _, _, err := store.Save(ctx, view); !errors.Is(err, controlstore.ErrConflict) {
		t.Fatalf("stale update: %v", err)
	}
	restarted, err := OpenSettings(ctx, db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Startup().Server.Addr != "127.0.0.1:7788" {
		t.Fatal("restart ignored saved setting")
	}
	other, err := OpenSettings(ctx, db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	latest, _ := other.View(ctx)
	latest.Auth.RPM = 11
	if _, _, err := other.Save(ctx, latest); err != nil {
		t.Fatal(err)
	}
	_, refreshed, err := store.ViewAfter(ctx)
	if err != nil || refreshed.Auth.RPM != 11 || refreshed.Server.Addr != cfg.Server.Addr {
		t.Fatalf("cross-store sync failed: %v", err)
	}
}

func TestSettingsCLIEnvironmentAndFileSource(t *testing.T) {
	fs := flag.NewFlagSet("settings-test", flag.ContinueOnError)
	var cfg Config
	fs.StringConfigVar(&cfg.Server.Addr, "addr", "server", "addr", "default", "")
	fs.StringConfigVar(&cfg.Auth.Strategy, "strategy", "auth", "strategy", "fixed", "")
	fs.StringConfigVar(&cfg.Checkin.At, "checkin-at", "checkin", "at", "04:00", "")
	if err := fs.Set("strategy", "random"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Set("addr", "cli"); err != nil {
		t.Fatal(err)
	}
	err := cfg.captureSources(fs, []string{"--addr=cli"}, func(name string) (string, bool) {
		return "environment", strings.HasSuffix(name, "SERVER_ADDR") || strings.HasSuffix(name, "CHECKIN_AT")
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Addr != "cli" || cfg.Checkin.At != "environment" || cfg.sources["server.addr"] != "cli" || cfg.sources["checkin.at"] != "environment" || cfg.sources["auth.strategy"] != "file" {
		t.Fatalf("incorrect precedence: %+v", cfg.sources)
	}
}

func TestLoadDefaultFileAndEnvironmentInProcess(t *testing.T) {
	if os.Getenv("CLODESEN_CONFIG_TEST_HELPER") == "1" {
		os.Args = []string{"config-helper"}
		cfg := Load()
		if cfg.Server.Addr != "127.0.0.1:7456" || cfg.Auth.Strategy != "random" {
			t.Fatal("default file or environment ignored")
		}
		return
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[server]\naddr = \"127.0.0.1:7456\"\n[auth]\nstrategy = \"fixed\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestLoadDefaultFileAndEnvironmentInProcess$")
	cmd.Dir, cmd.Env = dir, append(os.Environ(), "CLODESEN_CONFIG_TEST_HELPER=1", "CLODESEN2API_AUTH_STRATEGY=random")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helper failed: %v %s", err, output)
	}
}
