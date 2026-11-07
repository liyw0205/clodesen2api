package admin

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"clodesen2api/internal/admission"
	"clodesen2api/internal/config"
	"clodesen2api/internal/controlstore"
	"clodesen2api/internal/jobs"
	"clodesen2api/internal/upstream"
)

func TestSystemReadinessDatabaseAdmissionAndDrain(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{}
	cfg.Upstream.BaseURL = "https://example.test"
	cfg.Upstream.OpenAIBaseURL = "https://example.test/v1"
	cfg.Upstream.LoginPath = "/login"
	up, err := upstream.NewWithAccounts(cfg, []upstream.AccountCredential{{ID: "a", Identifier: "a", Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	s := NewServiceWithConfig(up, nil, testAdminToken, "", nil)
	db, err := controlstore.Open(filepath.Join(t.TempDir(), "control.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s.SetControlStore(db)
	capacity, err := admission.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	s.SetAdmission(capacity)
	s.jobs, err = jobs.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	status := s.SystemStatus(ctx)
	if !status.Ready || !status.Database || !status.Admission || !status.JobsAvailable || status.Accounts.Available != 1 {
		t.Fatalf("status=%+v", status)
	}
	s.draining = true
	if s.Ready(ctx) == nil || s.SystemStatus(ctx).Ready {
		t.Fatal("draining service ready")
	}
	s.draining = false
	if err := db.Mutate(ctx, "inference-admission", func(json.RawMessage) (any, error) { return map[string]any{"settings": "invalid"}, nil }); err != nil {
		t.Fatal(err)
	}
	if s.Ready(ctx) == nil || s.SystemStatus(ctx).Ready {
		t.Fatal("invalid admission service ready")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if s.Ready(ctx) == nil || s.SystemStatus(ctx).Database {
		t.Fatal("closed database service ready")
	}
	w := httptest.NewRecorder()
	s.systemStatusRoute(w, httptest.NewRequest("POST", "/", nil))
	if w.Code != 405 {
		t.Fatalf("method=%d", w.Code)
	}
}
