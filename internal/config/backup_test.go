package config

import (
	"encoding/json"
	"testing"
)

func TestBackupConfigMapsTOMLAndValidatesOverrides(t *testing.T) {
	_, base := settingsFixture(t)
	base.Admin.TokenFile, base.Admin.AccountsFile = "data/admin.token", "data/accounts.json"
	raw := []byte("[upstream]\nbase_url = 'https://restored.test'\nopenai_base_url = 'https://restored.test/v1'\n[auth]\nrpm = 6\n")
	restored, err := BackupConfig(*base, raw, json.RawMessage(`{"schema":1,"values":{"auth.rpm":7}}`))
	if err != nil {
		t.Fatal(err)
	}
	if restored.Upstream.BaseURL != "https://restored.test" || restored.Upstream.OpenAIBaseURL != "https://restored.test/v1" || restored.Auth.RPM != 7 {
		t.Fatalf("wrong restored settings: %+v", restored.PublicSettings())
	}
	for _, invalid := range [][]byte{
		[]byte("[admin]\ntoken_file = 'elsewhere'"),
		[]byte("[admin]\naccounts_file = 'elsewhere'"),
		[]byte("[auth]\nrpm = -1"),
		[]byte("[upstream]\nbase_url = 'file:///invalid'"),
	} {
		if _, err := BackupConfig(*base, invalid, nil); err == nil {
			t.Fatalf("invalid backup accepted: %s", invalid)
		}
	}
	if _, err := BackupConfig(*base, nil, json.RawMessage(`{"schema":1,"values":{"checkin.timezone":"invalid/zone"}}`)); err == nil {
		t.Fatal("invalid override accepted")
	}
}
