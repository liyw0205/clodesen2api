package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPublicSettingsDoesNotExposeRedemptionCode(t *testing.T) {
	var cfg Config
	cfg.Redemption.Code = "private-redemption-code"
	settings := cfg.PublicSettings()
	encoded, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), cfg.Redemption.Code) {
		t.Fatalf("public settings leaked redemption code: %s", encoded)
	}
	if !settings.Redemption.CodeConfigured {
		t.Fatal("CodeConfigured = false, want true")
	}
}
