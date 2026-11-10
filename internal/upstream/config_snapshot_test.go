package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"clodesen2api/internal/config"
)

func TestEmptyPoolDoesNotUseLegacyConfigAccount(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	var cfg config.Config
	cfg.Upstream.BaseURL = server.URL
	cfg.Upstream.LoginPath = "/api/user-auth"
	cfg.Auth.Identifier, cfg.Auth.Password = "legacy-user", "legacy-password"
	for _, removeLast := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty at startup", true: "last account removed"}[removeLast], func(t *testing.T) {
			var credentials []AccountCredential
			if removeLast {
				credentials = []AccountCredential{{ID: "a", Identifier: "user", Password: "pass", Enabled: true}}
			}
			u, err := NewWithAccounts(&cfg, credentials)
			if err != nil {
				t.Fatal(err)
			}
			if removeLast {
				if err := u.ReplaceAccounts(nil); err != nil {
					t.Fatal(err)
				}
			}
			ctx := context.Background()
			if u.AccountCount() != 0 || len(u.AccountCredentials()) != 0 || len(u.AccountStatuses(ctx)) != 0 {
				t.Fatal("empty pool exposed a legacy account")
			}
			if _, err := u.RefreshAccountStatus(ctx, ""); err == nil {
				t.Fatal("empty pool refreshed a legacy account")
			}
			if err := u.Login(ctx); err == nil {
				t.Fatal("empty pool attempted legacy login")
			}
			if _, err := u.CheckinAccount(ctx, ""); err == nil {
				t.Fatal("empty pool attempted legacy checkin")
			}
			if len(u.RedeemCodeAll(ctx, "code")) != 0 || u.RedeemCode(ctx, "", "code").Message != "未配置上游账号" {
				t.Fatal("empty pool attempted legacy redemption")
			}
			if _, _, err := u.ListPricing(ctx); err == nil {
				t.Fatal("empty pool read legacy pricing")
			}
			if _, _, err := u.ListCreditUsageWithFailures(ctx); err == nil {
				t.Fatal("empty pool read legacy credit usage")
			}
			if _, err := u.ListModels(ctx); err == nil {
				t.Fatal("empty pool read legacy models")
			}
			if requests.Load() != 0 {
				t.Fatal("empty pool sent an upstream request")
			}
		})
	}
}

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
