package upstream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"clodesen2api/internal/admission"
	"clodesen2api/internal/config"
	"clodesen2api/internal/openai"
)

func TestRouteRetryAndReloginShareAttemptBudget(t *testing.T) {
	for _, budget := range []int{1, 2} {
		t.Run(map[int]string{1: "one", 2: "two"}[budget], func(t *testing.T) {
			var forwards, logins atomic.Int32
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/login" {
					logins.Add(1)
					io.WriteString(w, `{}`)
					return
				}
				n := forwards.Add(1)
				if n == 1 {
					w.Header().Set("X-Qx-Route-Retry", "1")
					w.WriteHeader(503)
					return
				}
				w.WriteHeader(401)
			}))
			defer remote.Close()
			cfg := &config.Config{}
			cfg.Upstream.BaseURL, cfg.Upstream.OpenAIBaseURL, cfg.Upstream.LoginPath = remote.URL, remote.URL, "/login"
			cfg.Auth.AutoRelogin = true
			up, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			limits := admission.Defaults()
			limits.MaxAttempts = budget
			ctx := admission.WithLimits(context.Background(), limits)
			err, _ = up.Forward(ctx, httptest.NewRecorder(), openai.Request{Method: "POST", Path: openai.ChatCompletionsPath, Model: "model", ContentType: "application/json", Body: []byte(`{"model":"model"}`)})
			if err == nil || forwards.Load() != int32(budget) {
				t.Fatalf("attempts=%d budget=%d error=%v", forwards.Load(), budget, err)
			}
			if budget == 2 && logins.Load() != 2 {
				t.Fatalf("relogin not exercised: %d", logins.Load())
			}
		})
	}
}

func TestResponseReadFailureAndOversizeAreRejected(t *testing.T) {
	if _, err := readResponse(io.MultiReader(strings.NewReader("partial"), timeoutFailure{})); err == nil {
		t.Fatal("partial response accepted")
	}
	if _, err := readResponse(io.LimitReader(zeroReader{}, maxBufferedResponse+1)); err == nil {
		t.Fatal("oversize response accepted")
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

type timeoutFailure struct{}

func (timeoutFailure) Read([]byte) (int, error) { return 0, context.DeadlineExceeded }

func TestTimedBodyFirstByteAndIdle(t *testing.T) {
	for _, first := range []bool{true, false} {
		t.Run(map[bool]string{true: "first-byte", false: "idle"}[first], func(t *testing.T) {
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				if !first {
					io.WriteString(w, "x")
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			}))
			defer remote.Close()
			limits := admission.Defaults()
			limits.FirstByteTimeoutSeconds = 1
			limits.IdleTimeoutSeconds = 1
			req, _ := http.NewRequestWithContext(admission.WithLimits(context.Background(), limits), "GET", remote.URL, nil)
			resp, err := timedDo(remote.Client(), req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("timeout: %q %v", body, err)
			}
			if !first && string(body) != "x" {
				t.Fatalf("lost first byte: %q", body)
			}
		})
	}
}
