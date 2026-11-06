package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"clodesen2api/internal/admission"
	"clodesen2api/internal/apikey"
	"clodesen2api/internal/config"
	"clodesen2api/internal/controlstore"
	"clodesen2api/internal/upstream"
	"clodesen2api/internal/usage"
)

func TestAdmissionCancellationAndQuota(t *testing.T) {
	started := make(chan struct{}, 2)
	var calls atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/user-auth" {
			io.WriteString(w, `{}`)
			return
		}
		calls.Add(1)
		_, _ = io.ReadAll(r.Body)
		started <- struct{}{}
		<-r.Context().Done()
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
	db, err := controlstore.Open(filepath.Join(t.TempDir(), "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	capacity, err := admission.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := apikey.OpenControl(db, "")
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := keys.Create(apikey.CreateRequest{Name: "limited", Limits: admission.Policy{MaxConcurrent: 1, DailyRequests: 1}})
	if err != nil {
		t.Fatal(err)
	}
	auth, _ := apikey.NewAuthenticator("", keys, false, "")
	handler := NewHandlerWithAdmission(up, usage.New(), auth, nil, capacity)
	call := func(ctx context.Context) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"model"}`)).WithContext(ctx)
		r.Header.Set("Authorization", "Bearer "+secret)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- call(ctx) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not start")
	}
	if w := call(context.Background()); w.Code != 429 || !strings.Contains(w.Body.String(), "key_concurrency_exceeded") {
		t.Fatalf("concurrency: %d %s", w.Code, w.Body.String())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not end upstream")
	}
	view, err := capacity.Snapshot(context.Background())
	if err != nil || view.Active != 0 {
		t.Fatalf("leak: %+v %v", view, err)
	}
	if w := call(context.Background()); w.Code != 429 || !strings.Contains(w.Body.String(), "request_quota_exceeded") {
		t.Fatalf("quota: %d %s", w.Code, w.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls %d", calls.Load())
	}
}
