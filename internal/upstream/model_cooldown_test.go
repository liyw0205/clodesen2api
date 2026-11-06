package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"clodesen2api/internal/config"
	"clodesen2api/internal/controlstore"
	"clodesen2api/internal/modelcooldown"
	"clodesen2api/internal/openai"
)

func TestModel429DoesNotParkOtherModelsAndSurvivesPoolRestart(t *testing.T) {
	var modelACalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/user-auth" {
			io.WriteString(w, `{}`)
			return
		}
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Model == "model-a" {
			modelACalls.Add(1)
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"code":"rate_limit_exceeded"}}`)
			return
		}
		io.WriteString(w, `{"id":"success"}`)
	}))
	defer server.Close()
	db, err := controlstore.Open(filepath.Join(t.TempDir(), "control.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cooldowns, err := modelcooldown.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Upstream.BaseURL = server.URL
	cfg.Upstream.OpenAIBaseURL = server.URL + "/api/openai"
	cfg.Upstream.LoginPath = "/api/user-auth"
	newPool := func() *Upstream {
		pool, err := NewWithAccounts(cfg, []AccountCredential{{ID: "one", Identifier: "first", Password: "pw", Enabled: true}})
		if err != nil {
			t.Fatal(err)
		}
		pool.SetModelCooldowns(cooldowns)
		return pool
	}
	forward := func(pool *Upstream, model string) (error, bool, *httptest.ResponseRecorder) {
		body, _ := json.Marshal(map[string]string{"model": model})
		w := httptest.NewRecorder()
		err, wrote := pool.Forward(context.Background(), w, openai.Request{Method: "POST", Path: openai.ChatCompletionsPath, Model: model, ContentType: "application/json", Body: body})
		return err, wrote, w
	}
	pool := newPool()
	if err, wrote, w := forward(pool, "model-a"); err != nil || !wrote || w.Code != 429 {
		t.Fatalf("initial: %v %v %d", err, wrote, w.Code)
	}
	if err, wrote, w := forward(pool, "model-b"); err != nil || !wrote || w.Code != 200 {
		t.Fatalf("other model: %v %v %d", err, wrote, w.Code)
	}
	pool = newPool()
	err, wrote, _ := forward(pool, "model-a")
	var unavailable *NoAccountAvailableError
	if wrote || !errors.As(err, &unavailable) || unavailable.RetryAfter <= 0 {
		t.Fatalf("restart cooldown: %v %v", err, wrote)
	}
	if modelACalls.Load() != 1 {
		t.Fatalf("cooled model retried %d times", modelACalls.Load())
	}
}

type disconnectedWriter struct{ header http.Header }

func (w *disconnectedWriter) Header() http.Header       { return w.header }
func (w *disconnectedWriter) WriteHeader(int)           {}
func (w *disconnectedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestStreamFailureAfterOutputNeverSwitchesAccounts(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/user-auth" {
			io.WriteString(w, `{}`)
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	cfg := &config.Config{}
	cfg.Upstream.BaseURL = server.URL
	cfg.Upstream.OpenAIBaseURL = server.URL
	cfg.Upstream.LoginPath = "/api/user-auth"
	cfg.Auth.Accounts = "one:pw,two:pw"
	pool, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	err, wrote := pool.Forward(context.Background(), &disconnectedWriter{header: make(http.Header)}, openai.Request{Method: "POST", Path: openai.ChatCompletionsPath, Model: "model", Stream: true})
	if err == nil || !wrote || calls.Load() != 1 {
		t.Fatalf("stream failure: %v wrote=%v calls=%d", err, wrote, calls.Load())
	}
}
