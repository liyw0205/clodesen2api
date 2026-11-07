package upstream

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"clodesen2api/internal/admission"
	"clodesen2api/internal/config"
	"clodesen2api/internal/openai"
)

func TestRoutingPreservesAttemptBudgetAndCancel(t *testing.T) {
	for _, strategy := range []string{"fixed", "round_robin", "random"} {
		t.Run(strategy, func(t *testing.T) {
			var calls atomic.Int32
			var block atomic.Bool
			entered := make(chan struct{}, 1)
			cleanup := make(chan struct{})
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/login" {
					_, _ = io.WriteString(w, `{}`)
					return
				}
				_, _ = io.Copy(io.Discard, r.Body)
				calls.Add(1)
				if block.Load() {
					entered <- struct{}{}
					select {
					case <-r.Context().Done():
					case <-cleanup:
					}
					return
				}
				w.WriteHeader(503)
			}))
			defer remote.Close()
			defer close(cleanup)
			cfg := &config.Config{}
			cfg.Auth.Strategy = strategy
			cfg.Upstream.BaseURL = remote.URL
			cfg.Upstream.OpenAIBaseURL = remote.URL
			cfg.Upstream.LoginPath = "/login"
			u, err := NewWithAccounts(cfg, []AccountCredential{{ID: "a", Identifier: "a", Password: "p", Enabled: true}, {ID: "b", Identifier: "b", Password: "p", Enabled: true}})
			if err != nil {
				t.Fatal(err)
			}
			limits := admission.Defaults()
			limits.MaxAttempts = 1
			ctx := admission.WithLimits(context.Background(), limits)
			request := openai.Request{Method: "POST", Path: openai.ChatCompletionsPath, Model: "model", ContentType: "application/json", Body: []byte(`{"model":"model"}`)}
			_, _ = u.Forward(ctx, httptest.NewRecorder(), request)
			if calls.Load() != 1 || u.RoutingHealth().InFlight != 0 {
				t.Fatalf("budget or reservation changed: %d", calls.Load())
			}
			block.Store(true)
			cancelCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			done := make(chan struct{})
			go func() { defer close(done); _, _ = u.Forward(cancelCtx, httptest.NewRecorder(), request) }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("request never entered upstream")
			}
			cancel()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("cancel did not finish")
			}
			if calls.Load() != 2 || u.RoutingHealth().InFlight != 0 {
				t.Fatal("cancel retried or leaked a reservation")
			}
		})
	}
}

func TestRetiredRoutingStrategiesRejected(t *testing.T) {
	credentials := []AccountCredential{{ID: "a", Identifier: "a", Password: "p", Enabled: true}}
	u, err := NewWithAccounts(&config.Config{}, credentials)
	if err != nil {
		t.Fatal(err)
	}
	for _, strategy := range []string{"weighted", "least_inflight", "latency"} {
		cfg := &config.Config{}
		cfg.Auth.Strategy = strategy
		if _, err := NewWithAccounts(cfg, credentials); err == nil {
			t.Fatalf("retired strategy %s accepted on creation", strategy)
		}
		if err := u.SetStrategy(strategy); err == nil || u.Strategy() != "fixed" {
			t.Fatalf("retired strategy %s changed routing", strategy)
		}
	}
}
