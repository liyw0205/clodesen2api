package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clodesen2api/internal/apikey"
	"clodesen2api/internal/usage"
)

func TestKeyManagementReturnsSecretOnlyOnCreateAndRotate(t *testing.T) {
	store, err := apikey.Open(filepath.Join(t.TempDir(), "api-keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{keyStore: store}
	create := httptest.NewRequest(http.MethodPost, "/admin/api/keys", strings.NewReader(`{"name":"panel-client","rpm":10}`))
	create.Header.Set("Content-Type", "application/json")
	created := httptest.NewRecorder()
	service.keysRoot(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", created.Code, created.Body.String())
	}
	var payload struct {
		Key    apikey.Key `json:"key"`
		Secret string     `json:"secret"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Secret == "" || payload.Key.ID == "" || payload.Key.Version != 1 {
		t.Fatalf("create payload = %+v", payload)
	}

	listed := httptest.NewRecorder()
	service.keysRoot(listed, httptest.NewRequest(http.MethodGet, "/admin/api/keys", nil))
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), payload.Secret) {
		t.Fatalf("list status/body = %d %s", listed.Code, listed.Body.String())
	}

	rotate := httptest.NewRequest(http.MethodPost, "/admin/api/keys/"+payload.Key.ID+"/rotate", strings.NewReader(`{"version":1}`))
	rotate.Header.Set("Content-Type", "application/json")
	rotated := httptest.NewRecorder()
	service.keyRoute(rotated, rotate)
	if rotated.Code != http.StatusOK || strings.Contains(rotated.Body.String(), payload.Secret) {
		t.Fatalf("rotate status/body = %d %s", rotated.Code, rotated.Body.String())
	}
}

func TestKeyAPIUpdateClearsLimitsReportsUsageAndConflicts(t *testing.T) {
	store, err := apikey.Open(filepath.Join(t.TempDir(), "keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	rpm, expires := 60, time.Now().Add(time.Hour)
	item, _, err := store.Create(apikey.CreateRequest{Name: "client", RPM: &rpm, ExpiresAt: &expires})
	if err != nil {
		t.Fatal(err)
	}
	stats := usage.New()
	stats.RecordRequest(usage.RequestRecord{APIKeyID: item.ID, Success: true, DurationMS: 20})
	service := &Service{keyStore: store, usage: stats}
	update := httptest.NewRecorder()
	service.keyRoute(update, httptest.NewRequest("PUT", "/admin/api/keys/"+item.ID, strings.NewReader(`{"version":1,"rpm":null,"expiresAt":null}`)))
	if update.Code != 200 {
		t.Fatalf("clear limits: %d %s", update.Code, update.Body.String())
	}
	saved, err := store.Get(item.ID)
	if err != nil || saved.RPM != nil || saved.ExpiresAt != nil {
		t.Fatalf("limits not cleared: %+v %v", saved, err)
	}
	conflict := httptest.NewRecorder()
	service.keyRoute(conflict, httptest.NewRequest("DELETE", "/admin/api/keys/"+item.ID+"?version=1", nil))
	if conflict.Code != 409 {
		t.Fatalf("stale delete: %d", conflict.Code)
	}
	view := httptest.NewRecorder()
	service.keyRoute(view, httptest.NewRequest("GET", "/admin/api/keys/"+item.ID+"/usage", nil))
	var summary usage.KeySummary
	if err := json.Unmarshal(view.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if view.Code != 200 || summary.Requests != 1 || summary.Successes != 1 {
		t.Fatalf("summary: %d %+v", view.Code, summary)
	}
}
