package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"clodesen2api/internal/apikey"
	"clodesen2api/internal/config"
	"clodesen2api/internal/controlstore"
	"clodesen2api/internal/modelpolicy"
	"clodesen2api/internal/openai"
	"clodesen2api/internal/upstream"
	"clodesen2api/internal/usage"
)

func TestPolicyFiltersModelsAndAuthorizesAliasesByStableSource(t *testing.T) {
	for _, endpoint := range []string{openai.ChatCompletionsPath, openai.ResponsesPath} {
		t.Run(endpoint, func(t *testing.T) { testPolicyFiltersModelsAndAuthorizesAliases(t, endpoint) })
	}
}

func testPolicyFiltersModelsAndAuthorizesAliases(t *testing.T, endpoint string) {
	var calls atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user-auth":
			io.WriteString(w, `{}`)
		case "/api/openai/v1/models":
			io.WriteString(w, `{"data":[{"id":"source-a"},{"id":"source-b"}]}`)
		case "/api/openai" + endpoint:
			calls.Add(1)
			var payload struct {
				Model  string `json:"model"`
				Stream bool   `json:"stream"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if payload.Model != "source-a" {
				t.Errorf("upstream model: %s", payload.Model)
			}
			if payload.Stream {
				w.Header().Set("Content-Type", "text/event-stream")
				if endpoint == openai.ResponsesPath {
					io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"model\":\"source-a\",\"status\":\"completed\"}}\n\n")
				} else {
					io.WriteString(w, "data: {\"model\":\"source-a\",\"choices\":[]}\n\ndata: [DONE]\n\n")
				}
			} else {
				io.WriteString(w, `{"model":"source-a","choices":[]}`)
			}
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
	db, err := controlstore.Open(filepath.Join(t.TempDir(), "control.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	policies, err := modelpolicy.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := policies.Register(ctx, []string{"source-a", "source-b"}); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := policies.Snapshot(ctx)
	snapshot, err = policies.Put(ctx, snapshot.Revision, modelpolicy.Rule{SourceID: "source-a", PublicID: "public-a", UpstreamID: "source-a", Enabled: true, Endpoints: []string{endpoint}})
	if err != nil {
		t.Fatal(err)
	}
	keys, err := apikey.OpenControl(db, "")
	if err != nil {
		t.Fatal(err)
	}
	rpm := 2
	_, secret, err := keys.Create(apikey.CreateRequest{Name: "allowed", Models: []string{"source-a"}, RPM: &rpm})
	if err != nil {
		t.Fatal(err)
	}
	_, other, err := keys.Create(apikey.CreateRequest{Name: "denied", Models: []string{"source-b"}})
	if err != nil {
		t.Fatal(err)
	}
	auth, _ := apikey.NewAuthenticator("", keys, false, "")
	stats := usage.New()
	handler := NewHandlerWithPolicies(up, stats, auth, policies)
	call := func(path, body, key string) *httptest.ResponseRecorder {
		method := http.MethodPost
		if path == openai.ModelsPath {
			method = http.MethodGet
		}
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+key)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for i := 0; i < 2; i++ {
		listed := call(openai.ModelsPath, "", secret)
		if listed.Code != 200 || !strings.Contains(listed.Body.String(), "public-a") || strings.Contains(listed.Body.String(), "source-") {
			t.Fatalf("list: %d %s", listed.Code, listed.Body.String())
		}
	}
	if w := call(endpoint, `{"model":"public-a"}`, ""); w.Code != 401 {
		t.Fatalf("authentication bypass: %d", w.Code)
	}
	if w := call(openai.EditsPath, `{"model":"public-a","prompt":"edit","image":{"url":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jY1sAAAAASUVORK5CYII="}}`, secret); w.Code != 403 {
		t.Fatalf("endpoint capability bypass: %d %s", w.Code, w.Body.String())
	}
	for _, stream := range []bool{false, true} {
		body := `{"model":"public-a","stream":false}`
		if stream {
			body = `{"model":"public-a","stream":true}`
		}
		w := call(endpoint, body, secret)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "public-a") || strings.Contains(w.Body.String(), "source-a") {
			t.Fatalf("mapped response: %d %s", w.Code, w.Body.String())
		}
	}
	if w := call(endpoint, `{"model":"source-a"}`, secret); w.Code != 403 {
		t.Fatalf("hidden source bypass: %d", w.Code)
	}
	if w := call(endpoint, `{"model":"public-a"}`, other); w.Code != 403 {
		t.Fatalf("key bypass: %d", w.Code)
	}
	rule := snapshot.Models[0]
	rule.Enabled = false
	if _, err := policies.Put(ctx, snapshot.Revision, rule); err != nil {
		t.Fatal(err)
	}
	if w := call(endpoint, `{"model":"public-a"}`, secret); w.Code != 403 {
		t.Fatalf("disabled bypass: %d", w.Code)
	}
	if calls.Load() != 2 {
		t.Fatalf("unexpected upstream calls: %d", calls.Load())
	}
	for _, record := range stats.RequestRecords() {
		if record.Success && record.Endpoint == endpoint && (record.SourceModel != "source-a" || record.UpstreamModel != "source-a" || record.PolicyRevision == 0) {
			t.Fatalf("audit mapping missing: %+v", record)
		}
	}
}
