package upstream

import (
	"context"
	"testing"

	"clodesen2api/internal/config"
)

func TestRuntimeConfigAndAccountSyncPreserveSession(t *testing.T) {
	var cfg config.Config
	cfg.Upstream.BaseURL = "https://original.test"
	credentials := []AccountCredential{{ID: "a", Identifier: "user", Password: "pass", Enabled: true}}
	u, err := NewWithAccounts(&cfg, credentials)
	if err != nil {
		t.Fatal(err)
	}
	account := u.accountsSnapshot()[0]
	account.loggedIn = true
	cfg.Upstream.BaseURL = "https://changed.test"
	cfg.Auth.RPM, cfg.Auth.Strategy = 8, "random"
	u.ApplyRuntimeConfig(&cfg)
	if account.cfg.Upstream.BaseURL != "https://original.test" || u.cfg.Upstream.BaseURL != "https://original.test" {
		t.Fatal("startup session config mutated")
	}
	if err := u.ReplaceAccounts(credentials); err != nil {
		t.Fatal(err)
	}
	if u.accountsSnapshot()[0] != account || !account.loggedIn {
		t.Fatal("status synchronization discarded authenticated session")
	}
	if _, limit := u.RPMUsage(); limit != 8 || u.Strategy() != "random" {
		t.Fatal("hot pool config ignored")
	}
	if _, err := u.ProbeAccount(context.Background(), "missing"); err == nil {
		t.Fatal("probe silently used another account")
	}
}
