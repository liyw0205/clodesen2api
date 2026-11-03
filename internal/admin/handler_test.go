package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"clodesen2api/internal/config"
	"clodesen2api/internal/upstream"
	"clodesen2api/internal/usage"
)

const testAdminToken = "a-long-test-token-that-is-not-a-real-secret-0123456789"
const testSetupToken = "setup-token-123"

func newAdminTestServer(t *testing.T) (*httptest.Server, *AccountStore, *upstream.Upstream, *usage.Collector) {
	t.Helper()
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		encoder := json.NewEncoder(w)
		switch r.URL.Path {
		case "/api/user-auth":
			_ = encoder.Encode(map[string]any{"user": map[string]string{"id": "test"}})
		case "/api/checkin":
			_ = encoder.Encode(map[string]any{"enabled": true, "checkedIn": true, "balance": 1234})
		case "/api/openai/v1/models":
			_ = encoder.Encode(map[string]any{"object": "list", "data": []map[string]string{{"id": "gpt-5.6", "object": "model", "owned_by": "test"}}})
		case "/api/credits":
			_ = encoder.Encode(map[string]any{"pricing": map[string]any{
				"defaultRequestCredits": 0,
				"models":                []map[string]any{{"model": "gpt-5.6", "creditsPerRequest": 60, "enabled": true}},
			}})
		case "/api/account":
			_ = encoder.Encode(map[string]any{"account": map[string]any{"transactions": []map[string]any{
				{"id": 17, "amount": -60, "balance_after": 1174, "type": "model_usage", "description": "模型请求：gpt-5.6", "status": "completed", "created_at": "2026-11-02T05:00:00Z"},
			}}})
		case "/api/limited-redemption":
			if r.Method == http.MethodGet {
				_ = encoder.Encode(map[string]any{"enabled": true, "remaining": 2, "limit": 3, "creditType": "daily", "validityDays": 7})
				return
			}
			var request map[string]string
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request["code"] != "test-code" {
				w.WriteHeader(http.StatusBadRequest)
				_ = encoder.Encode(map[string]string{"error": "invalid code"})
				return
			}
			_ = encoder.Encode(map[string]any{
				"redemption": map[string]int{"credits": 500},
				"status":     map[string]int{"remaining": 1},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(remote.Close)

	cfg := &config.Config{}
	cfg.Upstream.BaseURL = remote.URL
	cfg.Upstream.LoginPath = "/api/user-auth"
	cfg.Auth.AutoRelogin = true
	cfg.Auth.Strategy = "round_robin"
	cfg.Checkin.Path = "/api/checkin"
	cfg.Checkin.Timezone = "Asia/Shanghai"
	credentials := []upstream.AccountCredential{{
		ID: "account-one", Identifier: "one@example.test", Password: "private-password", Enabled: true,
	}}
	up, err := upstream.NewWithAccounts(cfg, credentials)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenAccountStore(filepath.Join(t.TempDir(), "data", "accounts.json"), "round_robin", credentials)
	if err != nil {
		t.Fatal(err)
	}
	stats := usage.New()
	server := httptest.NewServer(NewHandler(up, store, testAdminToken, stats))
	t.Cleanup(server.Close)
	return server, store, up, stats
}

func TestAdminSessionProtectsAccountDataAndMutation(t *testing.T) {
	server, store, up, stats := newAdminTestServer(t)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}

	response, _ := requestJSON(t, client, http.MethodGet, server.URL+"/admin/api/accounts", "", "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", response.StatusCode)
	}
	response, _ = requestJSON(t, client, http.MethodPost, server.URL+"/admin/api/session", jsonBody(t, map[string]string{"token": "wrong"}), "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong-token status = %d, want 401", response.StatusCode)
	}
	response, _ = requestJSON(t, client, http.MethodPost, server.URL+"/admin/api/session", jsonBody(t, map[string]string{"token": testAdminToken}), "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", response.StatusCode)
	}
	if cookie := response.Header.Get("Set-Cookie"); !strings.Contains(cookie, "HttpOnly") || !strings.Contains(cookie, "SameSite=Strict") {
		t.Fatalf("session cookie missing protections: %q", cookie)
	}

	response, body := requestJSON(t, client, http.MethodGet, server.URL+"/admin/api/accounts", "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("accounts status = %d, body=%s", response.StatusCode, body)
	}
	if strings.Contains(body, "private-password") || strings.Contains(body, "one@example.test") {
		t.Fatalf("account response leaked credentials: %s", body)
	}
	var result struct {
		AvailableCredits int64
		Accounts         []struct {
			Identifier          string
			Balance             int64
			RedemptionRemaining int64
		}
	}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatal(err)
	}
	if result.AvailableCredits != 1234 || len(result.Accounts) != 1 ||
		result.Accounts[0].Balance != 1234 || result.Accounts[0].RedemptionRemaining != 2 {
		t.Fatalf("unexpected account response: %+v", result)
	}

	response, body = requestJSON(t, client, http.MethodGet, server.URL+"/admin/api/models", "", "")
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "gpt-5.6") || !strings.Contains(body, "creditsPerRequest") {
		t.Fatalf("models status=%d body=%s", response.StatusCode, body)
	}
	response, body = requestJSON(t, client, http.MethodGet, server.URL+"/admin/api/credit-usage", "", "")
	if response.StatusCode != http.StatusOK || !strings.Contains(body, `"model":"gpt-5.6"`) || !strings.Contains(body, `"amount":-60`) {
		t.Fatalf("credit usage status=%d body=%s", response.StatusCode, body)
	}
	stats.Record("gpt-5.6", http.StatusOK, 40*time.Millisecond, nil)
	response, body = requestJSON(t, client, http.MethodGet, server.URL+"/admin/api/usage", "", "")
	if response.StatusCode != http.StatusOK || !strings.Contains(body, `"totalRequests":1`) || !strings.Contains(body, `"model":"gpt-5.6"`) {
		t.Fatalf("usage status=%d body=%s", response.StatusCode, body)
	}

	response, _ = requestJSON(t, client, http.MethodPut, server.URL+"/admin/api/settings", jsonBody(t, map[string]string{"strategy": "random"}), "http://evil.test")
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin mutation status = %d, want 403", response.StatusCode)
	}
	response, _ = requestJSON(t, client, http.MethodPut, server.URL+"/admin/api/settings", jsonBody(t, map[string]string{"strategy": "random"}), server.URL)
	if response.StatusCode != http.StatusOK || up.Strategy() != "random" || store.Strategy() != "random" {
		t.Fatalf("strategy update status=%d runtime=%s stored=%s", response.StatusCode, up.Strategy(), store.Strategy())
	}
}

func TestAdminSessionInitialSetupPersistsToken(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(remote.Close)
	cfg := &config.Config{}
	cfg.Upstream.BaseURL = remote.URL
	cfg.Upstream.LoginPath = "/api/user-auth"
	credentials := []upstream.AccountCredential{{
		ID: "account-one", Identifier: "one@example.test", Password: "private-password", Enabled: true,
	}}
	up, err := upstream.NewWithAccounts(cfg, credentials)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenAccountStore(filepath.Join(t.TempDir(), "data", "accounts.json"), "round_robin", credentials)
	if err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(t.TempDir(), "admin.token")
	server := httptest.NewServer(NewHandlerWithTokenFile(up, store, "", tokenPath))
	t.Cleanup(server.Close)
	client := &http.Client{Jar: mustCookieJar(t)}

	response, body := requestJSON(t, client, http.MethodGet, server.URL+"/admin/api/session", "", "")
	if response.StatusCode != http.StatusOK || !strings.Contains(body, `"setupRequired":true`) {
		t.Fatalf("initial session status=%d body=%s", response.StatusCode, body)
	}
	response, _ = requestJSON(t, client, http.MethodPost, server.URL+"/admin/api/session", jsonBody(t, map[string]string{
		"token": testSetupToken, "confirm": "different-token",
	}), server.URL)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("mismatched setup status = %d, want 400", response.StatusCode)
	}
	response, _ = requestJSON(t, client, http.MethodPost, server.URL+"/admin/api/session", jsonBody(t, map[string]string{
		"token": testSetupToken, "confirm": testSetupToken,
	}), "http://evil.test")
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin setup status = %d, want 403", response.StatusCode)
	}
	response, _ = requestJSON(t, client, http.MethodPost, server.URL+"/admin/api/session", jsonBody(t, map[string]string{
		"token": testSetupToken, "confirm": testSetupToken,
	}), server.URL)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("setup status = %d", response.StatusCode)
	}
	data, err := os.ReadFile(tokenPath)
	if err != nil || strings.TrimSpace(string(data)) != testSetupToken {
		t.Fatalf("persisted token = %q, err=%v", strings.TrimSpace(string(data)), err)
	}
	if info, err := os.Stat(tokenPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token file permissions = %v, err=%v", info.Mode().Perm(), err)
	}

	response, body = requestJSON(t, client, http.MethodGet, server.URL+"/admin/api/session", "", "")
	if response.StatusCode != http.StatusOK || !strings.Contains(body, `"authenticated":true`) || strings.Contains(body, `"setupRequired":true`) {
		t.Fatalf("configured session status=%d body=%s", response.StatusCode, body)
	}
}

func mustCookieJar(t *testing.T) *cookiejar.Jar {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return jar
}

func TestAdminAccountManagementAndRedemptionPersist(t *testing.T) {
	server, store, up, _ := newAdminTestServer(t)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	response, _ := requestJSON(t, client, http.MethodPost, server.URL+"/admin/api/session", jsonBody(t, map[string]string{"token": testAdminToken}), "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", response.StatusCode)
	}

	response, body := requestJSON(t, client, http.MethodPost, server.URL+"/admin/api/accounts",
		jsonBody(t, map[string]string{"identifier": "two@example.test", "password": "second-secret"}), server.URL)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("add account status=%d body=%s", response.StatusCode, body)
	}
	if up.AccountCount() != 2 || len(store.Accounts()) != 2 {
		t.Fatalf("runtime/store account counts = %d/%d, want 2/2", up.AccountCount(), len(store.Accounts()))
	}
	info, err := os.Stat(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("account file permissions = %o, want 600", info.Mode().Perm())
	}

	response, body = requestJSON(t, client, http.MethodPost, server.URL+"/admin/api/redeem",
		jsonBody(t, map[string]string{"code": "test-code"}), server.URL)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("redeem status=%d body=%s", response.StatusCode, body)
	}
	var redeemed struct {
		SuccessCount int
		TotalCredits int
	}
	if err := json.Unmarshal([]byte(body), &redeemed); err != nil {
		t.Fatal(err)
	}
	if redeemed.SuccessCount != 2 || redeemed.TotalCredits != 1000 {
		t.Fatalf("redemption aggregate = %+v, want two accounts and 1000 credits", redeemed)
	}
	response, body = requestJSON(t, client, http.MethodGet, server.URL+"/admin/api/redemption-history", "", server.URL)
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "test-code") || !strings.Contains(body, "o***e@example.test") {
		t.Fatalf("redemption history status=%d body=%s", response.StatusCode, body)
	}

	reopened, err := OpenAccountStore(store.path, "round_robin", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.Accounts()) != 2 || reopened.Strategy() != "round_robin" {
		t.Fatalf("reopened store = %d accounts, strategy %s", len(reopened.Accounts()), reopened.Strategy())
	}
}

func TestAdminCheckinAndSingleAccountRefreshRoutes(t *testing.T) {
	server, _, _, _ := newAdminTestServer(t)
	client := &http.Client{Jar: mustCookieJar(t)}
	response, body := requestJSON(t, client, http.MethodPost, server.URL+"/admin/api/session", jsonBody(t, map[string]string{"token": testAdminToken}), "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login status=%d body=%s", response.StatusCode, body)
	}

	response, body = requestJSON(t, client, http.MethodPost, server.URL+"/admin/api/checkin", "", server.URL)
	if response.StatusCode != http.StatusOK || !strings.Contains(body, `"status":"success"`) || !strings.Contains(body, `"checkedIn":true`) {
		t.Fatalf("all checkin status=%d body=%s", response.StatusCode, body)
	}

	response, body = requestJSON(t, client, http.MethodPost, server.URL+"/admin/api/accounts/account-one/checkin", "", server.URL)
	if response.StatusCode != http.StatusOK || !strings.Contains(body, `"alreadyChecked":true`) {
		t.Fatalf("single checkin status=%d body=%s", response.StatusCode, body)
	}

	response, body = requestJSON(t, client, http.MethodPost, server.URL+"/admin/api/accounts/account-one/refresh", "", server.URL)
	if response.StatusCode != http.StatusOK || !strings.Contains(body, `"ok":true`) || !strings.Contains(body, `"balance":1234`) {
		t.Fatalf("single refresh status=%d body=%s", response.StatusCode, body)
	}
}

func TestListAccountsCachesUpstreamUntilForcedRefresh(t *testing.T) {
	var checkinCalls atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user-auth":
			_, _ = io.WriteString(w, `{"user":{"id":"test"}}`)
		case "/api/checkin":
			checkinCalls.Add(1)
			_, _ = io.WriteString(w, `{"enabled":true,"checkedIn":true,"balance":1234}`)
		case "/api/limited-redemption":
			_, _ = io.WriteString(w, `{"enabled":true,"remaining":2,"limit":3}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer remote.Close()

	cfg := &config.Config{}
	cfg.Upstream.BaseURL = remote.URL
	cfg.Upstream.LoginPath = "/api/user-auth"
	cfg.Checkin.Path = "/api/checkin"
	up, err := upstream.NewWithAccounts(cfg, []upstream.AccountCredential{{
		ID: "account-one", Identifier: "one@example.test", Password: "pw", Enabled: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{upstream: up}

	request := httptest.NewRequest(http.MethodGet, "/admin/api/accounts", nil)
	first := httptest.NewRecorder()
	service.listAccounts(first, request)
	second := httptest.NewRecorder()
	service.listAccounts(second, request)
	if got := checkinCalls.Load(); got != 1 {
		t.Fatalf("normal account reads made %d upstream check-in calls, want 1", got)
	}
	if first.Header().Get("X-Admin-Cache") != "miss" || second.Header().Get("X-Admin-Cache") != "hit" {
		t.Fatalf("cache headers = first %q second %q", first.Header().Get("X-Admin-Cache"), second.Header().Get("X-Admin-Cache"))
	}
	forced := httptest.NewRequest(http.MethodGet, "/admin/api/accounts?refresh=1", nil)
	forcedResponse := httptest.NewRecorder()
	service.listAccounts(forcedResponse, forced)
	if got := checkinCalls.Load(); got != 2 {
		t.Fatalf("forced account read made %d upstream check-in calls, want 2", got)
	}
	if forcedResponse.Header().Get("X-Admin-Cache") != "miss" {
		t.Fatalf("forced cache header = %q, want miss", forcedResponse.Header().Get("X-Admin-Cache"))
	}
}

func TestRefreshAccountsKeepsCacheWhenAllUpstreamAccountsFail(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":"upstream unavailable"}`)
	}))
	defer remote.Close()

	cfg := &config.Config{}
	cfg.Upstream.BaseURL = remote.URL
	cfg.Upstream.LoginPath = "/api/user-auth"
	cfg.Upstream.LoginTimeout = time.Second
	cfg.Checkin.Path = "/api/checkin"
	up, err := upstream.NewWithAccounts(cfg, []upstream.AccountCredential{{
		ID: "account-one", Identifier: "one@example.test", Password: "pw", Enabled: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{
		upstream:        up,
		accountCache:    []byte(`{"accounts":[{"id":"account-one","balance":1234}]}`),
		accountCachedAt: time.Now().Add(-upstreamCacheTTL),
	}
	old := append([]byte(nil), service.accountCache...)
	if _, err := service.refreshAccounts(context.Background(), true); err == nil {
		t.Fatal("refreshAccounts() error = nil, want an upstream failure")
	}
	if string(service.accountCache) != string(old) {
		t.Fatalf("failed refresh replaced cached accounts: got %s want %s", service.accountCache, old)
	}
}

func requestJSON(t *testing.T, client *http.Client, method, target, body, origin string) (*http.Response, string) {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, string(data)
}

func jsonBody(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
