package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPublicSettingsReturnsRedemptionCodeForAuthenticatedPanel(t *testing.T) {
	var cfg Config
	cfg.Redemption.Code = "private-redemption-code"
	settings := cfg.PublicSettings()
	if settings.Redemption.Code != cfg.Redemption.Code {
		t.Fatalf("redemption code = %q, want %q", settings.Redemption.Code, cfg.Redemption.Code)
	}
	if !settings.Redemption.CodeConfigured {
		t.Fatal("CodeConfigured = false, want true")
	}
	if _, err := json.Marshal(settings); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateSourcePersistsCustomPrefix(t *testing.T) {
	cfg := &Config{}
	cfg.path = filepath.Join(t.TempDir(), "config.toml")
	cfg.Server.Addr = "0.0.0.0:6687"
	cfg.Upstream.BaseURL = "https://upstream.example"
	cfg.Upstream.OpenAIBaseURL = "https://upstream.example/api/openai"
	cfg.Upstream.LoginPath = "/api/user-auth"
	cfg.Upstream.LoginTimeout = 30 * time.Second
	cfg.Auth.Strategy = "round_robin"
	cfg.Checkin.Path = "/api/checkin"
	cfg.Checkin.At = "04:00"
	cfg.Checkin.Timezone = "Asia/Shanghai"
	cfg.Redemption.At = "04:30"

	settings := cfg.PublicSettings()
	settings.Update.Source = "custom"
	settings.Update.CustomPrefix = "https://mirror.example/{url}"
	if err := cfg.ApplyPublicSettings(settings); err != nil {
		t.Fatalf("ApplyPublicSettings() error = %v", err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	data, err := os.ReadFile(cfg.path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `source = "custom"`) || !strings.Contains(string(data), `custom_prefix = "https://mirror.example/{url}"`) {
		t.Fatalf("saved config omitted custom update source: %s", data)
	}
}

func TestPublicSettingsDefaultRPMAndFixedStrategyPersistence(t *testing.T) {
	cfg := &Config{}
	cfg.path = filepath.Join(t.TempDir(), "config.toml")
	cfg.Server.Addr = "0.0.0.0:6687"
	cfg.Upstream.BaseURL = "https://upstream.example"
	cfg.Upstream.OpenAIBaseURL = "https://upstream.example/api/openai"
	cfg.Upstream.LoginPath = "/api/user-auth"
	cfg.Upstream.LoginTimeout = 30 * time.Second
	cfg.Checkin.Path = "/api/checkin"
	cfg.Checkin.At = "04:00"
	cfg.Checkin.Timezone = "Asia/Shanghai"
	cfg.Redemption.At = "04:30"

	settings := cfg.PublicSettings()
	if settings.Auth.RPM != 4 {
		t.Fatalf("default RPM = %d, want 4", settings.Auth.RPM)
	}
	settings.Auth.Strategy = "fixed"
	settings.Auth.RPM = 7
	if err := cfg.ApplyPublicSettings(settings); err != nil {
		t.Fatalf("ApplyPublicSettings() error = %v", err)
	}
	if cfg.Auth.Strategy != "fixed" || cfg.Auth.RPM != 7 {
		t.Fatalf("applied account settings = strategy %q, RPM %d", cfg.Auth.Strategy, cfg.Auth.RPM)
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	data, err := os.ReadFile(cfg.path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "strategy = \"fixed\"") || !strings.Contains(string(data), "rpm = 7") {
		t.Fatalf("saved settings omitted fixed strategy or RPM: %s", data)
	}
}

func TestCreditCooldownThresholdValidationAndPersistence(t *testing.T) {
	_, cfg := settingsFixture(t)
	settings := cfg.PublicSettings()
	if settings.Auth.CreditCooldownThreshold != 100 {
		t.Fatalf("default threshold = %d, want 100", settings.Auth.CreditCooldownThreshold)
	}
	for _, threshold := range []int64{1, 200, 1000000000000} {
		settings.Auth.CreditCooldownThreshold = threshold
		if err := cfg.ApplyPublicSettings(settings); err != nil {
			t.Fatalf("threshold %d: %v", threshold, err)
		}
		if cfg.Auth.CreditCooldownThreshold != threshold {
			t.Fatalf("applied threshold = %d, want %d", cfg.Auth.CreditCooldownThreshold, threshold)
		}
	}
	for _, threshold := range []int64{0, -1, 1000000000001} {
		settings.Auth.CreditCooldownThreshold = threshold
		if err := cfg.ApplyPublicSettings(settings); err == nil {
			t.Fatalf("invalid threshold %d accepted", threshold)
		}
		if cfg.Auth.CreditCooldownThreshold != 1000000000000 {
			t.Fatal("invalid threshold changed config")
		}
	}
	for _, threshold := range []int64{0, 200} {
		cfg.Auth.CreditCooldownThreshold = threshold
		if err := cfg.Save(); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(cfg.path)
		if err != nil {
			t.Fatal(err)
		}
		want := "credit_cooldown_threshold = 100"
		if threshold == 200 {
			want = "credit_cooldown_threshold = 200"
		}
		if !strings.Contains(string(data), want) {
			t.Fatalf("saved config omitted threshold: want %q", want)
		}
	}
}
