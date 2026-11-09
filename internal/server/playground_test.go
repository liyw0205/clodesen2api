package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"clodesen2api/internal/admin"
	"clodesen2api/internal/admission"
	"clodesen2api/internal/apikey"
	"clodesen2api/internal/config"
	"clodesen2api/internal/controlstore"
	"clodesen2api/internal/modelpolicy"
	"clodesen2api/internal/openai"
	"clodesen2api/internal/upstream"
	"clodesen2api/internal/usage"
)

func TestAdminPlaygroundSessionPoliciesAdmissionAuditAndStreaming(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	releaseStream := func() { once.Do(func() { close(release) }) }
	defer releaseStream()
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" || r.URL.Path == "/api/compute-route" {
			io.WriteString(w, `{}`)
			return
		}
		calls.Add(1)
		if r.URL.Path == openai.ChatCompletionsPath || r.URL.Path == openai.ResponsesPath {
			var payload struct {
				Model  string `json:"model"`
				Stream bool   `json:"stream"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Model != "source" {
				t.Errorf("upstream mapping: %+v %v", payload, err)
			}
			if payload.Stream {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: {\"model\":\"source\",\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
				w.(http.Flusher).Flush()
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				io.WriteString(w, "data: {\"model\":\"source\",\"choices\":[{\"delta\":{\"content\":\"second\"}}]}\n\ndata: [DONE]\n\n")
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"model":"source","choices":[]}`)
	}))
	defer remote.Close()
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.Upstream.BaseURL, cfg.Upstream.OpenAIBaseURL, cfg.Upstream.LoginPath = remote.URL, remote.URL, "/login"
	credentials := []upstream.AccountCredential{{ID: "one", Identifier: "one@example.test", Password: "fixture", Enabled: true}}
	up, err := upstream.NewWithAccounts(cfg, credentials)
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := admin.OpenAccountStore(filepath.Join(dir, "accounts.json"), "fixed", credentials)
	if err != nil {
		t.Fatal(err)
	}
	db, err := controlstore.Open(filepath.Join(dir, "control.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	policies, err := modelpolicy.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := policies.Register(ctx, []string{"source"}); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := policies.Snapshot(ctx)
	snapshot, err = policies.Put(ctx, snapshot.Revision, modelpolicy.Rule{SourceID: "source", PublicID: "public", UpstreamID: "source", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	capacity, err := admission.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := apikey.OpenControl(db, "")
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := keys.Create(apikey.CreateRequest{Name: "client-key"})
	if err != nil {
		t.Fatal(err)
	}
	auth, err := apikey.NewAuthenticator("", keys, false, "")
	if err != nil {
		t.Fatal(err)
	}
	stats := usage.New()
	panel := admin.NewServiceWithConfig(up, accounts, "test-admin-123", filepath.Join(dir, "token"), cfg, stats)
	panel.SetModelPolicies(policies)
	panel.SetAdmission(capacity)
	local := httptest.NewServer(NewHandlerWithAdmission(up, stats, auth, policies, capacity, panel))
	defer func() { releaseStream(); local.Close() }()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 4 * time.Second}
	call := func(client *http.Client, method, path, body, origin, bearer string) (int, string) {
		t.Helper()
		request, _ := http.NewRequest(method, local.URL+path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		if bearer != "" {
			request.Header.Set("Authorization", "Bearer "+bearer)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(data)
	}
	path := "/admin/api/playground/chat/completions"
	body := `{"model":"public","messages":[{"role":"user","content":"test"}]}`
	if status, data := call(client, "POST", path, body, "", secret); status != 401 || !strings.Contains(data, "admin_session_expired") {
		t.Fatalf("client key must not authorize management testing: %d %s", status, data)
	}
	if status, data := call(client, "POST", "/admin/api/session", `{"token":"test-admin-123"}`, "", ""); status != 200 {
		t.Fatalf("login: %d %s", status, data)
	}
	if status, data := call(client, "POST", openai.ChatCompletionsPath, body, "", ""); status != 401 {
		t.Fatalf("admin cookie must not authorize public API: %d %s", status, data)
	}
	if status, _ := call(client, "POST", path, body, "http://other.example.test", ""); status != 403 {
		t.Fatalf("cross-origin testing: %d", status)
	}
	if status, _ := call(client, "GET", path, "", "", ""); status != 405 {
		t.Fatalf("method whitelist: %d", status)
	}
	if status, _ := call(client, "POST", "/admin/api/playground/models", "{}", "", ""); status != 404 {
		t.Fatalf("path whitelist: %d", status)
	}
	if calls.Load() != 0 {
		t.Fatal("rejected requests reached upstream")
	}
	for _, test := range []struct{ path, body string }{
		{"/chat/completions", body},
		{"/responses", `{"model":"public","input":"test"}`},
		{"/images/generations", `{"model":"public","prompt":"test"}`},
		{"/images/edits", `{"model":"public","prompt":"test","image":{"url":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jY1sAAAAASUVORK5CYII="}}`},
	} {
		if status, data := call(client, "POST", "/admin/api/playground"+test.path, test.body, local.URL, ""); status != 200 {
			t.Fatalf("session testing %s: %d %s", test.path, status, data)
		}
	}
	var successful int
	for _, record := range stats.RequestRecords() {
		if !record.Success {
			continue
		}
		successful++
		if record.APIKeyID != "admin-playground" || record.APIKeyName != "管理测试台" || record.Model != "public" || record.UpstreamModel != "source" {
			t.Fatalf("management attribution: %+v", record)
		}
	}
	if successful != 4 {
		t.Fatalf("missing audit records: %d", successful)
	}
	view, _ := capacity.Snapshot(ctx)
	settings := view.Settings
	settings.MaxConcurrent = 1
	if _, err := capacity.Update(ctx, view.Revision, settings); err != nil {
		t.Fatal(err)
	}
	lease, err := capacity.Acquire(ctx, "occupied", admission.Policy{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if status, data := call(client, "POST", path, body, "", ""); status != 503 {
		t.Fatalf("management bypassed global capacity: %d %s", status, data)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest("POST", local.URL+path, strings.NewReader(`{"model":"public","stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	stream, err := client.Do(request)
	if err != nil {
		t.Fatal("stream headers and first chunk were buffered until completion:", err)
	}
	defer stream.Body.Close()
	reader := bufio.NewReader(stream.Body)
	first, err := reader.ReadString('\n')
	if err != nil || stream.StatusCode != 200 || !strings.Contains(first, "first") || !strings.Contains(first, `"model":"public"`) {
		t.Fatalf("first chunk before completion: %d %q %v", stream.StatusCode, first, err)
	}
	releaseStream()
	rest, err := io.ReadAll(reader)
	if err != nil || !strings.Contains(string(rest), "second") || !strings.Contains(string(rest), "[DONE]") {
		t.Fatalf("remaining chunks: %s %v", rest, err)
	}
	rule := snapshot.Models[0]
	rule.Enabled = false
	if _, err := policies.Put(ctx, snapshot.Revision, rule); err != nil {
		t.Fatal(err)
	}
	if status, data := call(client, "POST", path, body, "", ""); status != 403 || !strings.Contains(data, "model_policy_denied") {
		t.Fatalf("management bypassed model policy: %d %s", status, data)
	}
	if calls.Load() != 5 {
		t.Fatalf("unexpected upstream calls: %d", calls.Load())
	}
}
