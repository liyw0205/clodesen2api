package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"clodesen2api/internal/config"
	"clodesen2api/internal/upstream"
)

type readinessFixture struct{ err error }

func (f *readinessFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }
func (f *readinessFixture) Ready(context.Context) error                      { return f.err }

func TestReadinessIsLocalAndDoesNotExposeDiagnostics(t *testing.T) {
	cfg := &config.Config{}
	cfg.Upstream.BaseURL = "https://example.test"
	cfg.Upstream.OpenAIBaseURL = "https://example.test/v1"
	cfg.Upstream.LoginPath = "/login"
	up, err := upstream.NewWithAccounts(cfg, []upstream.AccountCredential{{ID: "a", Identifier: "a", Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	admin := &readinessFixture{}
	handler := NewHandler(up, admin)
	for _, item := range []struct {
		method  string
		failure error
		want    int
	}{{"GET", nil, 200}, {"HEAD", nil, 200}, {"POST", nil, 405}, {"GET", errors.New("private database path"), 503}} {
		admin.err = item.failure
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(item.method, "/readyz", nil))
		if w.Code != item.want || strings.Contains(w.Body.String(), "private") || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("ready=%d %s", w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 {
		t.Fatal("liveness depends on readiness")
	}
	empty := NewHandler(nil)
	w = httptest.NewRecorder()
	empty.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != 503 {
		t.Fatal("empty upstream ready")
	}
}
