package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"clodesen2api/internal/apikey"
	"clodesen2api/internal/config"
	"clodesen2api/internal/openai"
	"clodesen2api/internal/upstream"
	"clodesen2api/internal/usage"
)

func TestAPIKeyMiddlewareRejectsMissingAndAcceptsManagedKey(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user-auth":
			_, _ = io.WriteString(w, `{}`)
		case "/api/openai" + openai.ChatCompletionsPath:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"chatcmpl-test"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer remote.Close()
	cfg := &config.Config{}
	cfg.Upstream.BaseURL = remote.URL
	cfg.Upstream.OpenAIBaseURL = remote.URL + "/api/openai"
	cfg.Upstream.LoginPath = "/api/user-auth"
	up, err := upstream.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	store, err := apikey.Open(filepath.Join(t.TempDir(), "api-keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	item, secret, err := store.Create(apikey.CreateRequest{Name: "test-client", Models: []string{"gpt-4.1"}})
	if err != nil {
		t.Fatal(err)
	}
	auth, err := apikey.NewAuthenticator("", store, false, "")
	if err != nil {
		t.Fatal(err)
	}
	stats := usage.New()
	handler := NewHandlerWithAPIKey(up, stats, auth)
	body := strings.NewReader(`{"model":"gpt-4.1","messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, openai.ChatCompletionsPath, body)
	req.Header.Set("Content-Type", "application/json")
	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, req)
	if missing.Code != http.StatusUnauthorized || !strings.Contains(missing.Body.String(), "api_key_required") {
		t.Fatalf("missing key response = %d %s", missing.Code, missing.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, openai.ChatCompletionsPath, strings.NewReader(`{"model":"gpt-4.1","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+secret)
	accepted := httptest.NewRecorder()
	handler.ServeHTTP(accepted, req)
	if accepted.Code != http.StatusOK || !strings.Contains(accepted.Body.String(), "chatcmpl-test") {
		t.Fatalf("accepted response = %d %s (key %s)", accepted.Code, accepted.Body.String(), item.ID)
	}
	records := stats.RequestRecords()
	if len(records) != 2 || records[0].APIKeyID != item.ID || records[0].AuthResult != "authorized" || records[0].RequestID != accepted.Header().Get("X-Request-ID") || records[0].ClientIP == "" {
		t.Fatalf("request audit = %+v", records)
	}
	if records[1].AuthErrorCode != "api_key_required" || records[1].AuthResult != "rejected" {
		t.Fatalf("denial audit = %+v", records[1])
	}
	encoded, _ := json.Marshal(records)
	if strings.Contains(string(encoded), secret) {
		t.Fatal("audit leaked secret")
	}
}

type unreadBody struct{ t *testing.T }

func (b unreadBody) Read([]byte) (int, error) {
	b.t.Fatal("invalid key caused request body to be read")
	return 0, io.EOF
}
func (b unreadBody) Close() error { return nil }

func TestAPIKeyRejectsBeforeReadingBodyAndPreservesModelOnPolicyDenial(t *testing.T) {
	store, err := apikey.Open(filepath.Join(t.TempDir(), "keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	rpm := 1
	item, secret, err := store.Create(apikey.CreateRequest{Name: "limited", RPM: &rpm, Models: []string{"allowed"}})
	if err != nil {
		t.Fatal(err)
	}
	auth, err := apikey.NewAuthenticator("", store, false, "")
	if err != nil {
		t.Fatal(err)
	}
	stats := usage.New()
	handler := NewHandlerWithAPIKey(nil, stats, auth)
	req := httptest.NewRequest(http.MethodPost, openai.ChatCompletionsPath, nil)
	req.Body = unreadBody{t}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("unauthorized status %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodPost, openai.ChatCompletionsPath, strings.NewReader(`{"model":"denied","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", secret)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("model status %d %s", rec.Code, rec.Body.String())
	}
	denied := stats.RequestRecords()[0]
	if denied.APIKeyID != item.ID || denied.Model != "denied" || denied.AuthErrorCode != "model_not_allowed" {
		t.Fatalf("policy denial audit %+v", denied)
	}

	req = httptest.NewRequest(http.MethodPost, openai.ChatCompletionsPath, strings.NewReader(`{"messages":[]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", secret)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing model bypassed allowlist: %d", rec.Code)
	}
	if _, authErr := auth.Authenticate("127.0.0.1", "", "", secret, "allowed"); authErr != nil {
		t.Fatalf("denial consumed RPM: %v", authErr)
	}
	req = httptest.NewRequest(http.MethodPost, openai.ChatCompletionsPath, strings.NewReader(`{"model":"allowed","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", secret)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 429 || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("RPM status %d %s", rec.Code, rec.Body.String())
	}
}

func TestAPIKeyModelListingDoesNotConsumeInferenceRPM(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
	}))
	defer remote.Close()
	cfg := &config.Config{}
	cfg.Upstream.BaseURL, cfg.Upstream.OpenAIBaseURL = remote.URL, remote.URL
	up, err := upstream.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	store, err := apikey.Open(filepath.Join(t.TempDir(), "keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	rpm := 1
	_, secret, err := store.Create(apikey.CreateRequest{Name: "catalog", RPM: &rpm})
	if err != nil {
		t.Fatal(err)
	}
	auth, err := apikey.NewAuthenticator("", store, false, "")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandlerWithAPIKey(up, usage.New(), auth)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, openai.ModelsPath, nil)
		req.Header.Set("X-API-Key", secret)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("catalog %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	if _, err := auth.Authenticate("127.0.0.1", "", "", secret, "model"); err != nil {
		t.Fatalf("catalog consumed inference RPM: %v", err)
	}
}
