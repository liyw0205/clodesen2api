package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"clodesen2api/internal/config"
	"clodesen2api/internal/openai"
	"clodesen2api/internal/upstream"
	"clodesen2api/internal/usage"
)

type panicResponseWriter struct{ *httptest.ResponseRecorder }

func (w panicResponseWriter) Write([]byte) (int, error) { panic("response write failed") }

func TestPanicAfterResponseHeaderIsAuditedAsInterrupted(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{}`)
	}))
	defer remote.Close()
	cfg := &config.Config{}
	cfg.Upstream.BaseURL, cfg.Upstream.OpenAIBaseURL = remote.URL, remote.URL
	up, err := upstream.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	stats := usage.New()
	handler := NewHandlerWithUsage(up, stats)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected response writer panic")
			}
		}()
		r := httptest.NewRequest("POST", openai.ChatCompletionsPath, strings.NewReader(`{"model":"model","messages":[]}`))
		r.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(panicResponseWriter{httptest.NewRecorder()}, r)
	}()
	records := stats.RequestRecords()
	if len(records) != 1 || records[0].Stage != "interrupted" || records[0].Success || records[0].Error != "internal_panic" {
		t.Fatalf("panic recorded as completed: %+v", records)
	}
	if totals := stats.Snapshot(); totals.Successes != 0 || totals.Failures != 1 {
		t.Fatalf("panic aggregate: %+v", totals)
	}
}

func TestAuditDoesNotPersistArbitraryParametersOrUpstreamSecrets(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/user-auth" {
			io.WriteString(w, `{}`)
			return
		}
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"message":"echoed sk-secret-from-upstream","code":"untrusted-secret-code"}}`)
	}))
	defer remote.Close()
	cfg := &config.Config{}
	cfg.Upstream.BaseURL = remote.URL
	cfg.Upstream.OpenAIBaseURL = remote.URL
	cfg.Upstream.LoginPath = "/api/user-auth"
	up, err := upstream.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	stats := usage.New()
	handler := NewHandlerWithUsage(up, stats)
	r := httptest.NewRequest("POST", openai.ChatCompletionsPath, strings.NewReader(`{"model":"model","messages":[{"role":"user","content":"secret-message"}],"metadata":{"token":"secret-metadata"},"tools":[{"type":"function","function":{"name":"secret-function"}}],"temperature":0.3}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "sk-secret-from-upstream") {
		t.Fatalf("client response changed: %d %s", w.Code, w.Body.String())
	}
	data, _ := json.Marshal(stats.RequestRecords())
	for _, secret := range []string{"sk-secret-from-upstream", "untrusted-secret-code", "secret-message", "secret-metadata", "secret-function"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("audit leaked %q", secret)
		}
	}
	record := stats.RequestRecords()[0]
	if record.Error != "upstream_http_400" || len(record.Attempts) != 1 || record.Attempts[0].Status != 400 || record.Attempts[0].ID == "" {
		t.Fatalf("audit: %+v", record)
	}
}
