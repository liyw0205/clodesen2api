package upstream

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"clodesen2api/internal/openai"
)

func TestForwardAllocatesImageRoute(t *testing.T) {
	var routeCalls, forwardCalls int
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/compute-route":
			var body struct {
				RequestID string `json:"requestId"`
				Path      string `json:"path"`
				Model     string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode route request: %v", err)
			}
			if body.RequestID == "" || body.Path != "/api/openai/v1/images/generations" || body.Model != "gpt-image-2.5-flare" {
				t.Fatalf("unexpected route request: %+v", body)
			}
			routeCalls++
			origin := "https://" + strings.TrimPrefix(server.URL, "https://")
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"enabled":true,"ticket":"route-ticket","origin":"`+origin+`"}`)
		case "/api/openai/v1/images/generations":
			forwardCalls++
			if r.Header.Get("X-Qx-Compute-Ticket") != "route-ticket" ||
				r.Header.Get("X-Qx-Request-Id") == "" ||
				r.Header.Get("Idempotency-Key") != r.Header.Get("X-Qx-Request-Id") {
				t.Fatalf("missing compute route headers: %v", r.Header)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"b64_json":"payload"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	up := newTestUpstream(t, server.URL)
	client := server.Client()
	up.httpClient = client
	up.loginClient = client

	rec := httptest.NewRecorder()
	_, wrote := up.Forward(context.Background(), rec, openai.Request{
		Method:      http.MethodPost,
		Path:        openai.GenerationsPath,
		Model:       "gpt-image-2.5-flare",
		ContentType: "application/json",
		Body:        []byte(`{"model":"gpt-image-2.5-flare","prompt":"test"}`),
	})
	if !wrote {
		t.Fatal("Forward did not write a response")
	}
	if rec.Code != http.StatusOK || rec.Body.String() != `{"data":[{"b64_json":"payload"}]}` {
		t.Fatalf("response = %d %q", rec.Code, rec.Body.String())
	}
	if routeCalls != 1 || forwardCalls != 1 {
		t.Fatalf("route calls = %d, forward calls = %d; want 1 each", routeCalls, forwardCalls)
	}
}
