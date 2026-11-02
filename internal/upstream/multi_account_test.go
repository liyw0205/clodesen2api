package upstream

import (
	"context"
	"encoding/json"
	"errors"
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
