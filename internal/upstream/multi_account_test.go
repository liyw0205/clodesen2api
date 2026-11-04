package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"clodesen2api/internal/config"
	"clodesen2api/internal/openai"
)

func TestForwardCoolsInsufficientAccountAndRetriesNext(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user-auth":
			var login struct {
				Identifier string `json:"identifier"`
			}
			if err := json.NewDecoder(r.Body).Decode(&login); err != nil {
				t.Errorf("decode login: %v", err)
			}
			http.SetCookie(w, &http.Cookie{Name: "session", Value: login.Identifier, Path: "/"})
			_, _ = io.WriteString(w, `{}`)
		case "/api/openai" + openai.ModelsPath:
			cookie, err := r.Cookie("session")
			if err != nil {
				t.Errorf("read session cookie: %v", err)
				http.Error(w, "missing session", http.StatusUnauthorized)
				return
			}
			mu.Lock()
			calls = append(calls, cookie.Value)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if cookie.Value == "first" {
				_, _ = io.WriteString(w, `{"error":{"message":"Insufficient credits"}}`)
				return
			}
			_, _ = io.WriteString(w, `{"account":"`+cookie.Value+`"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := &config.Config{}
	cfg.Upstream.BaseURL = server.URL
	cfg.Upstream.OpenAIBaseURL = server.URL + "/api/openai"
	cfg.Upstream.LoginPath = "/api/user-auth"
	cfg.Auth.Accounts = "first:pw1,second:pw2"
	cfg.Auth.Strategy = "round_robin"
	pool, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		if err, wrote := pool.Forward(context.Background(), rec, openai.Request{Method: http.MethodGet, Path: openai.ModelsPath}); err != nil || !wrote {
			t.Fatalf("Forward() = (%v, %v)", err, wrote)
		}
		if rec.Code != http.StatusOK || rec.Body.String() == `{"error":{"message":"Insufficient credits"}}` {
			t.Fatalf("response %d = %d %q", i, rec.Code, rec.Body.String())
		}
	}

	mu.Lock()
	gotCalls := append([]string(nil), calls...)
	mu.Unlock()
	if want := []string{"first", "second", "second"}; !reflect.DeepEqual(gotCalls, want) {
		t.Fatalf("account calls = %v, want %v", gotCalls, want)
	}
	if remaining := time.Until(pool.cooldownUntil[pool.accounts[0]]); remaining > 30*time.Minute || remaining < 29*time.Minute {
		t.Fatalf("credit cooldown remaining = %s, want about 30m", remaining)
	}
}

func TestTemporaryBanDoesNotBlockCheckinOrRedemption(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user-auth":
			_, _ = io.WriteString(w, `{}`)
		case "/api/checkin":
			_, _ = io.WriteString(w, `{"enabled":true,"checkedIn":true,"balance":500}`)
		case "/api/limited-redemption":
			if r.Method == http.MethodGet {
				_, _ = io.WriteString(w, `{"enabled":true,"remaining":1}`)
				return
			}
			_, _ = io.WriteString(w, `{"redemption":{"credits":100}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := &config.Config{}
	cfg.Upstream.BaseURL = server.URL
	cfg.Upstream.LoginPath = "/api/user-auth"
	cfg.Checkin.Path = "/api/checkin"
	p, err := NewWithAccounts(cfg, []AccountCredential{{ID: "banned", Identifier: "banned@example.test", Password: "pw", Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Hour).UTC()
	p.cooldownUntil[p.accounts[0]] = until
	p.cooldownReason[p.accounts[0]] = cooldownReasonTemporaryBan

	if _, err := p.CheckinAccount(context.Background(), "banned"); err != nil {
		t.Fatalf("CheckinAccount() error = %v", err)
	}
	redemption := p.RedeemCode(context.Background(), "banned", "code")
	if redemption.Status != "success" {
		t.Fatalf("RedeemCode() = %+v, want success", redemption)
	}
	if !p.cooldownUntil[p.accounts[0]].Equal(until) || p.cooldownReason[p.accounts[0]] != cooldownReasonTemporaryBan {
		t.Fatalf("temporary ban cooldown changed: until=%s reason=%q", p.cooldownUntil[p.accounts[0]], p.cooldownReason[p.accounts[0]])
	}
}

func TestForwardRetriesStreamingInsufficientCredits(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user-auth":
			var login struct {
				Identifier string `json:"identifier"`
			}
			if err := json.NewDecoder(r.Body).Decode(&login); err != nil {
				t.Errorf("decode login: %v", err)
			}
			http.SetCookie(w, &http.Cookie{Name: "session", Value: login.Identifier, Path: "/"})
			_, _ = io.WriteString(w, `{}`)
		case "/api/openai" + openai.ChatCompletionsPath:
			cookie, err := r.Cookie("session")
			if err != nil {
				t.Errorf("read session cookie: %v", err)
				http.Error(w, "missing session", http.StatusUnauthorized)
				return
			}
			mu.Lock()
			calls = append(calls, cookie.Value)
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			if cookie.Value == "first" {
				_, _ = io.WriteString(w, "data: {\"error\":{\"message\":\"Insufficient points\"}}\n\n")
				return
			}
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := &config.Config{}
	cfg.Upstream.BaseURL = server.URL
	cfg.Upstream.OpenAIBaseURL = server.URL + "/api/openai"
	cfg.Upstream.LoginPath = "/api/user-auth"
	cfg.Auth.Accounts = "first:pw1,second:pw2"
	pool, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	rec := httptest.NewRecorder()
	err, wrote := pool.Forward(context.Background(), rec, openai.Request{
		Method: http.MethodPost, Path: openai.ChatCompletionsPath,
		ContentType: "application/json", Body: []byte(`{"model":"test","messages":[]}`),
	})
	if err != nil || !wrote {
		t.Fatalf("Forward() = (%v, %v)", err, wrote)
	}
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"content":"ok"`) {
		t.Fatalf("response = %d %q", rec.Code, rec.Body.String())
	}
	mu.Lock()
	gotCalls := append([]string(nil), calls...)
	mu.Unlock()
	if want := []string{"first", "second"}; !reflect.DeepEqual(gotCalls, want) {
		t.Fatalf("account calls = %v, want %v", gotCalls, want)
	}
}

func TestForwardCoolsAccountUntilUpstreamBanEnd(t *testing.T) {
	var mu sync.Mutex
	var routeAccounts []string
	banUntil := time.Now().Add(47 * time.Minute).UTC().Truncate(time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user-auth":
			var login struct {
				Identifier string `json:"identifier"`
			}
			if err := json.NewDecoder(r.Body).Decode(&login); err != nil {
				t.Errorf("decode login: %v", err)
			}
			http.SetCookie(w, &http.Cookie{Name: "session", Value: login.Identifier, Path: "/"})
			_, _ = io.WriteString(w, `{}`)
		case "/api/compute-route":
			cookie, err := r.Cookie("session")
			if err != nil {
				t.Fatalf("read session cookie: %v", err)
			}
			mu.Lock()
			routeAccounts = append(routeAccounts, cookie.Value)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if cookie.Value == "first" {
				w.WriteHeader(http.StatusForbidden)
				_, _ = fmt.Fprintf(w, `{"error":true,"code":"account_restricted","bannedUntil":%q,"message":"account restricted"}`, banUntil.Format(time.RFC3339Nano))
				return
			}
			_, _ = io.WriteString(w, `{"enabled":false}`)
		case "/api/openai" + openai.EditsPath:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"url":"https://example.test/image.png"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := &config.Config{}
	cfg.Upstream.BaseURL = server.URL
	cfg.Upstream.OpenAIBaseURL = server.URL + "/api/openai"
	cfg.Upstream.LoginPath = "/api/user-auth"
	cfg.Auth.Accounts = "first:pw1,second:pw2"
	cfg.Auth.Strategy = "round_robin"
	pool, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	rec := httptest.NewRecorder()
	err, wrote := pool.Forward(context.Background(), rec, openai.Request{
		Method: http.MethodPost, Path: openai.EditsPath, Model: "image-model",
		ContentType: "application/json", Body: []byte(`{"prompt":"test"}`),
	})
	if err != nil || !wrote || rec.Code != http.StatusForbidden {
		t.Fatalf("Forward() = (%v, %v), status=%d body=%q", err, wrote, rec.Code, rec.Body.String())
	}
	mu.Lock()
	gotRoutes := append([]string(nil), routeAccounts...)
	mu.Unlock()
	if want := []string{"first"}; !reflect.DeepEqual(gotRoutes, want) {
		t.Fatalf("route accounts = %v, want %v", gotRoutes, want)
	}
	remaining := time.Until(pool.cooldownUntil[pool.accounts[0]])
	if remaining < 46*time.Minute || remaining > 47*time.Minute {
		t.Fatalf("ban cooldown remaining = %s, want close to %s", remaining, 47*time.Minute)
	}

	nextResponse := httptest.NewRecorder()
	err, wrote = pool.Forward(context.Background(), nextResponse, openai.Request{
		Method: http.MethodPost, Path: openai.EditsPath, Model: "image-model",
		ContentType: "application/json", Body: []byte(`{"prompt":"test"}`),
	})
	if err != nil || !wrote || nextResponse.Code != http.StatusOK {
		t.Fatalf("next Forward() = (%v, %v), status=%d body=%q", err, wrote, nextResponse.Code, nextResponse.Body.String())
	}
	mu.Lock()
	gotRoutes = append([]string(nil), routeAccounts...)
	mu.Unlock()
	if want := []string{"first", "second"}; !reflect.DeepEqual(gotRoutes, want) {
		t.Fatalf("route accounts after next request = %v, want %v", gotRoutes, want)
	}
}

func TestDetectPermanentAccountRestrictionDisablesAccount(t *testing.T) {
	restriction, ok := detectAccountRestriction(http.StatusForbidden, []byte(`{"error":"账号已封禁"}`))
	if !ok || !restriction.disableAccount || restriction.cooldownUntil != nil {
		t.Fatalf("restriction = %+v, ok=%v; want permanent disable", restriction, ok)
	}
}

func TestParseRestrictionTimeAcceptsUnixMilliseconds(t *testing.T) {
	want := time.Unix(1793664000, 0).UTC()
	got, ok := parseRestrictionTime("1793664000000")
	if !ok || !got.Equal(want) {
		t.Fatalf("parseRestrictionTime() = %s, %v; want %s, true", got, ok, want)
	}
}

func TestLoginRestrictionRecordsCooldownForAccount(t *testing.T) {
	banUntil := time.Now().Add(35 * time.Minute).UTC().Truncate(time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var login struct {
			Identifier string `json:"identifier"`
		}
		if err := json.NewDecoder(r.Body).Decode(&login); err != nil {
			t.Errorf("decode login: %v", err)
			return
		}
		if login.Identifier == "banned" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprintf(w, `{"code":"account_restricted","bannedUntil":%q}`, banUntil.Format(time.RFC3339Nano))
			return
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()

	cfg := &config.Config{}
	cfg.Upstream.BaseURL = server.URL
	cfg.Upstream.LoginPath = "/api/user-auth"
	cfg.Auth.Accounts = "banned:pw1,healthy:pw2"
	pool, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	var recorded struct {
		id      string
		enabled bool
		until   time.Time
		reason  string
	}
	pool.SetAccountStateRecorder(func(id string, enabled bool, until *time.Time, reason string) error {
		recorded.id = id
		recorded.enabled = enabled
		recorded.reason = reason
		if until != nil {
			recorded.until = *until
		}
		return nil
	})
	if err := pool.Login(context.Background()); err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if recorded.id != pool.accounts[0].accountID || !recorded.enabled || !recorded.until.Equal(banUntil) || recorded.reason != cooldownReasonTemporaryBan {
		t.Fatalf("recorded state = %+v, want account %q enabled until %s", recorded, pool.accounts[0].accountID, banUntil)
	}
	if got := pool.cooldownUntil[pool.accounts[0]]; !got.Equal(banUntil) {
		t.Fatalf("cooldown = %s, want %s", got, banUntil)
	}
}

func TestForwardRetriesRetryableHTTPStatusWithNextAccount(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/openai"+openai.ModelsPath {
			mu.Lock()
			calls = append(calls, fmt.Sprintf("account-%d", len(calls)+1))
			callNumber := len(calls)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if callNumber == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `{"error":"temporary upstream failure"}`)
				return
			}
			_, _ = io.WriteString(w, `{"data":[{"id":"ok"}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	cfg := &config.Config{}
	cfg.Upstream.BaseURL = server.URL
	cfg.Upstream.OpenAIBaseURL = server.URL + "/api/openai"
	cfg.Upstream.LoginPath = "/api/user-auth"
	cfg.Auth.Accounts = "first:pw1,second:pw2"
	cfg.Auth.Strategy = "round_robin"
	pool, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	for _, account := range pool.accounts {
		account.loggedIn = true
	}
	for _, account := range pool.accounts {
		account.httpClient = &http.Client{Jar: account.httpClient.Jar}
	}

	rec := httptest.NewRecorder()
	err, wrote := pool.Forward(context.Background(), rec, openai.Request{Method: http.MethodGet, Path: openai.ModelsPath})
	if err != nil || !wrote {
		t.Fatalf("Forward() = (%v, %v)", err, wrote)
	}
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"ok"`) {
		t.Fatalf("response = %d %q", rec.Code, rec.Body.String())
	}
	mu.Lock()
	got := append([]string(nil), calls...)
	mu.Unlock()
	if want := []string{"account-1", "account-2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("account calls = %v, want %v", got, want)
	}
}

func TestForwardDoesNotRetryFailedImageRequestButNextRequestUsesRoundRobin(t *testing.T) {
	var mu sync.Mutex
	var imageAccounts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user-auth":
			var login struct {
				Identifier string `json:"identifier"`
			}
			if err := json.NewDecoder(r.Body).Decode(&login); err != nil {
				t.Errorf("decode login: %v", err)
			}
			http.SetCookie(w, &http.Cookie{Name: "session", Value: login.Identifier, Path: "/"})
			_, _ = io.WriteString(w, `{}`)
		case "/api/compute-route":
			w.WriteHeader(http.StatusNotFound)
		case "/api/openai" + openai.GenerationsPath:
			cookie, err := r.Cookie("session")
			if err != nil {
				t.Errorf("read session cookie: %v", err)
				http.Error(w, "missing session", http.StatusUnauthorized)
				return
			}
			mu.Lock()
			imageAccounts = append(imageAccounts, cookie.Value)
			call := len(imageAccounts)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if call == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `{"error":{"message":"temporary image failure"}}`)
				return
			}
			_, _ = io.WriteString(w, `{"data":[{"b64_json":"ok"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := &config.Config{}
	cfg.Upstream.BaseURL = server.URL
	cfg.Upstream.OpenAIBaseURL = server.URL + "/api/openai"
	cfg.Upstream.LoginPath = "/api/user-auth"
	cfg.Auth.Accounts = "first:pw1,second:pw2"
	cfg.Auth.Strategy = "round_robin"
	pool, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	request := openai.Request{
		Method: http.MethodPost, Path: openai.GenerationsPath, Model: "gpt-image-2",
		ContentType: "application/json", Body: []byte(`{"model":"gpt-image-2","prompt":"test"}`),
	}
	firstResponse := httptest.NewRecorder()
	err, wrote := pool.Forward(context.Background(), firstResponse, request)
	if err != nil || !wrote || firstResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("first Forward() = (%v, %v), status=%d body=%q", err, wrote, firstResponse.Code, firstResponse.Body.String())
	}

	mu.Lock()
	gotAccounts := append([]string(nil), imageAccounts...)
	mu.Unlock()
	if want := []string{"first"}; !reflect.DeepEqual(gotAccounts, want) {
		t.Fatalf("image accounts after failed request = %v, want %v", gotAccounts, want)
	}

	secondResponse := httptest.NewRecorder()
	err, wrote = pool.Forward(context.Background(), secondResponse, request)
	if err != nil || !wrote || secondResponse.Code != http.StatusOK {
		t.Fatalf("second Forward() = (%v, %v), status=%d body=%q", err, wrote, secondResponse.Code, secondResponse.Body.String())
	}
	mu.Lock()
	gotAccounts = append([]string(nil), imageAccounts...)
	mu.Unlock()
	if want := []string{"first", "second"}; !reflect.DeepEqual(gotAccounts, want) {
		t.Fatalf("image accounts after next request = %v, want %v", gotAccounts, want)
	}
}

func TestForwardRetriesNetworkErrorWithNextAccount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"ok"}]}`)
	}))
	defer server.Close()

	cfg := &config.Config{}
	cfg.Upstream.BaseURL = server.URL
	cfg.Upstream.OpenAIBaseURL = server.URL
	cfg.Upstream.LoginPath = "/api/user-auth"
	cfg.Auth.Accounts = "first:pw1,second:pw2"
	cfg.Auth.Strategy = "round_robin"
	pool, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	for _, account := range pool.accounts {
		account.loggedIn = true
	}
	pool.accounts[0].httpClient = &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("simulated network interruption")
	})}

	rec := httptest.NewRecorder()
	err, wrote := pool.Forward(context.Background(), rec, openai.Request{Method: http.MethodGet, Path: openai.ModelsPath})
	if err != nil || !wrote || rec.Code != http.StatusOK {
		t.Fatalf("Forward() = (%v, %v), status=%d body=%q", err, wrote, rec.Code, rec.Body.String())
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestForwardReturnsCooldownWhenEveryAccountUnavailable(t *testing.T) {
	cfg := &config.Config{}
	cfg.Auth.Accounts = "first:pw1,second:pw2"
	pool, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	for _, account := range pool.accounts {
		pool.cooldownUntil[account] = time.Now().Add(30 * time.Minute)
	}

	err, wrote := pool.Forward(context.Background(), httptest.NewRecorder(), openai.Request{Method: http.MethodGet, Path: openai.ModelsPath})
	if wrote {
		t.Fatal("Forward() wrote a response while every account was cooling down")
	}
	var unavailable *NoAccountAvailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error = %v, want NoAccountAvailableError", err)
	}
	if unavailable.RetryAfter <= 0 {
		t.Fatalf("RetryAfter = %s, want positive duration", unavailable.RetryAfter)
	}
}

func TestSelectAccountsRoundRobin(t *testing.T) {
	cfg := &config.Config{}
	cfg.Auth.Accounts = "first:pw1,second:pw2,third:pw3"
	cfg.Auth.Strategy = "round_robin"
	pool, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	for request, wantFirst := range []int{0, 1, 2, 0} {
		candidates, _ := pool.selectAccounts()
		if len(candidates) != 3 {
			t.Fatalf("request %d candidate count = %d, want 3", request, len(candidates))
		}
		if candidates[0].index != wantFirst {
			t.Errorf("request %d first account = %d, want %d", request, candidates[0].index, wantFirst)
		}
	}
}

func TestDefaultStrategyIsFixed(t *testing.T) {
	cfg := &config.Config{}
	cfg.Auth.Accounts = "first:pw1,second:pw2"
	pool, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got := pool.Strategy(); got != "fixed" {
		t.Fatalf("default strategy = %q, want fixed", got)
	}
}

func TestFixedStrategyUsesConcurrentCapacityAndReportsRPM(t *testing.T) {
	cfg := &config.Config{}
	cfg.Auth.Accounts = "first:pw1,second:pw2"
	cfg.Auth.Strategy = "fixed"
	cfg.Auth.RPM = 1
	pool, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	first, second := pool.accounts[0], pool.accounts[1]

	candidates, _ := pool.selectAccounts()
	if len(candidates) != 2 || candidates[0].account != first {
		t.Fatalf("initial fixed candidates = %v, want first account first", candidates)
	}
	if !pool.reserveAccountRequest(first) {
		t.Fatal("first account reservation failed")
	}
	if pool.reserveAccountRequest(first) {
		t.Fatal("first account exceeded its concurrent request limit")
	}
	candidates, _ = pool.selectAccounts()
	if len(candidates) != 1 || candidates[0].account != second {
		t.Fatalf("candidates after first account is full = %v, want second account", candidates)
	}
	if !pool.reserveAccountRequest(second) {
		t.Fatal("second account reservation failed")
	}
	if pool.fixedAccount != second {
		t.Fatal("fixed strategy did not move to the account used after saturation")
	}
	usage, limit := pool.RPMUsage()
	if usage[first.accountID] != 1 || usage[second.accountID] != 1 || limit != 1 {
		t.Fatalf("RPM usage = %v, limit=%d; want 1 per account, limit 1", usage, limit)
	}

	pool.releaseAccountRequest(first)
	usage, _ = pool.RPMUsage()
	if usage[first.accountID] != 0 || usage[second.accountID] != 1 {
		t.Fatalf("RPM usage after release = %v, want first 0 and second 1", usage)
	}
	pool.releaseAccountRequest(second)
}

func TestRPMReservationIsAtomicAcrossConcurrentCallers(t *testing.T) {
	cfg := &config.Config{}
	cfg.Auth.Accounts = "only:pw"
	cfg.Auth.RPM = 1
	pool, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	account := pool.accounts[0]
	var count int
	var countMu sync.Mutex
	var callers sync.WaitGroup
	for i := 0; i < 16; i++ {
		callers.Add(1)
		go func() {
			defer callers.Done()
			if pool.reserveAccountRequest(account) {
				countMu.Lock()
				count++
				countMu.Unlock()
			}
		}()
	}
	callers.Wait()
	if count != 1 {
		t.Fatalf("successful reservations = %d, want exactly one", count)
	}
	if usage, _ := pool.RPMUsage(); usage[account.accountID] != 1 {
		t.Fatalf("RPM usage = %v, want one in-flight request", usage)
	}
	pool.releaseAccountRequest(account)
}

func TestEveryStrategyHonorsPerAccountConcurrencyLimit(t *testing.T) {
	for _, strategy := range []string{"round_robin", "random", "fixed"} {
		t.Run(strategy, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Auth.Accounts = "first:pw1,second:pw2"
			cfg.Auth.Strategy = strategy
			cfg.Auth.RPM = 1
			pool, err := New(cfg)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			for _, account := range pool.accounts {
				if !pool.reserveAccountRequest(account) {
					t.Fatalf("could not occupy account %s", account.accountID)
				}
			}
			candidates, retryAfter := pool.selectAccounts()
			if len(candidates) != 0 || retryAfter <= 0 {
				t.Fatalf("selection with both accounts occupied = %v, retryAfter=%s", candidates, retryAfter)
			}
			pool.releaseAccountRequest(pool.accounts[0])
			candidates, _ = pool.selectAccounts()
			if len(candidates) != 1 || candidates[0].account != pool.accounts[0] {
				t.Fatalf("selection after slot release = %v, want account 0", candidates)
			}
			pool.releaseAccountRequest(pool.accounts[1])
		})
	}
}

func TestAccountReplacementKeepsInFlightRPMReservation(t *testing.T) {
	cfg := &config.Config{}
	cfg.Auth.Accounts = "first:pw1"
	cfg.Auth.RPM = 1
	pool, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	old := pool.accounts[0]
	if !pool.reserveAccountRequest(old) {
		t.Fatal("initial reservation failed")
	}
	if err := pool.ReplaceAccounts([]AccountCredential{{
		ID: old.accountID, Identifier: "first", Password: "new-password", Enabled: true,
	}}); err != nil {
		t.Fatalf("ReplaceAccounts() error = %v", err)
	}
	current := pool.accounts[0]
	if pool.reserveAccountRequest(current) {
		t.Fatal("replacement account exceeded the existing in-flight limit")
	}
	pool.releaseAccountRequest(old)
	if !pool.reserveAccountRequest(current) {
		t.Fatal("old request release did not free the replacement account slot")
	}
	pool.releaseAccountRequest(current)
}

func TestConfiguredAccountsSupportsLegacyAndMultiple(t *testing.T) {
	cfg := &config.Config{}
	cfg.Auth.Identifier = "legacy-user"
	cfg.Auth.Password = "legacy:password"
	accounts, err := configuredAccounts(cfg)
	if err != nil || !reflect.DeepEqual(accounts, []accountCredential{{identifier: "legacy-user", password: "legacy:password"}}) {
		t.Fatalf("legacy configured accounts = %#v, %v", accounts, err)
	}

	cfg.Auth.Accounts = "first:pw:with:colon, second:pw2"
	accounts, err = configuredAccounts(cfg)
	want := []accountCredential{{identifier: "first", password: "pw:with:colon"}, {identifier: "second", password: "pw2"}}
	if err != nil || !reflect.DeepEqual(accounts, want) {
		t.Fatalf("multiple configured accounts = %#v, %v; want %#v", accounts, err, want)
	}
}

func TestInsufficientCreditsDetection(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{name: "payment required", status: http.StatusPaymentRequired, body: `{"error":"payment required"}`, want: true},
		{name: "upstream english error", status: http.StatusOK, body: `{"error":{"code":"insufficient_quota"}}`, want: true},
		{name: "balance error code", status: http.StatusBadRequest, body: `{"error":{"code":"insufficient_user_balance"}}`, want: true},
		{name: "upstream chinese error", status: http.StatusBadRequest, body: `{"message":"积分不足"}`, want: true},
		{name: "unrelated quota error", status: http.StatusTooManyRequests, body: `{"error":"rate limit exceeded"}`, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := insufficientCredits(test.status, []byte(test.body)); got != test.want {
				t.Fatalf("insufficientCredits() = %v, want %v", got, test.want)
			}
		})
	}
}
