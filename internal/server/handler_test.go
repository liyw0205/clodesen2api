package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"clodesen2api/internal/config"
	"clodesen2api/internal/openai"
	"clodesen2api/internal/upstream"
	"clodesen2api/internal/usage"
)

type receivedRequest struct {
	method string
	path   string
	query  string
	cookie string
}

func TestNewHandlerForwardsOnlyRebuiltAllowedPaths(t *testing.T) {
	var forwardCalls atomic.Int32
	var receivedMu sync.Mutex
	var received []receivedRequest

	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user-auth":
			w.Header().Set("Content-Type", "application/json")
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "test-session", Path: "/api"})
			_, _ = io.WriteString(w, `{}`)
		case "/api/openai" + openai.ModelsPath, "/api/openai" + openai.ChatCompletionsPath, "/api/openai" + openai.GenerationsPath, "/api/openai" + openai.EditsPath:
			cookie, err := r.Cookie("session")
			if err != nil || cookie.Value != "test-session" {
				http.Error(w, "missing login cookie", http.StatusUnauthorized)
				return
			}
			forwardCalls.Add(1)
			receivedMu.Lock()
			received = append(received, receivedRequest{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, cookie: cookie.Value})
			receivedMu.Unlock()

			w.Header().Set("X-Upstream-Trace", r.URL.Path)
			if r.URL.Path == "/api/openai"+openai.ChatCompletionsPath {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-test\"}\n\n")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/api/openai"+openai.GenerationsPath {
				w.WriteHeader(http.StatusCreated)
			}
			_, _ = io.WriteString(w, `{"data":[{"b64_json":"payload"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstreamServer.Close()

	cfg := &config.Config{}
	cfg.Upstream.BaseURL = upstreamServer.URL
	cfg.Upstream.OpenAIBaseURL = upstreamServer.URL + "/api/openai"
	cfg.Upstream.LoginPath = "/api/user-auth"
	up, err := upstream.New(cfg)
	if err != nil {
		t.Fatalf("new upstream: %v", err)
	}
	stats := usage.New()
	handler := NewHandlerWithUsage(up, stats)

	requests := []struct {
		name       string
		request    *http.Request
		wantStatus int
		wantPath   string
		wantBody   string
		wantType   string
		wantFlush  bool
	}{
		{
			name:       "models",
			request:    httptest.NewRequest(http.MethodGet, openai.ModelsPath+"?client_query=discarded", nil),
			wantStatus: http.StatusOK,
			wantPath:   "/api/openai" + openai.ModelsPath,
			wantBody:   `{"data":[{"b64_json":"payload"}]}`,
			wantType:   "application/json",
		},
		{
			name: "chat completions",
			request: func() *http.Request {
				req := httptest.NewRequest(http.MethodPost, openai.ChatCompletionsPath+"?client_query=discarded", strings.NewReader(`{"model":"gpt-4.1","messages":[{"role":"user","content":"hello"}],"stream":true,"tools":[{"type":"function","function":{"name":"weather"}}]}`))
				req.Header.Set("Content-Type", "application/json")
				return req
			}(),
			wantStatus: http.StatusOK,
			wantPath:   "/api/openai" + openai.ChatCompletionsPath,
			wantBody:   "data: {\"id\":\"chatcmpl-test\"}\n\n",
			wantType:   "text/event-stream",
			wantFlush:  true,
		},
		{
			name: "generation",
			request: func() *http.Request {
				req := httptest.NewRequest(http.MethodPost, openai.GenerationsPath+"?client_query=discarded", strings.NewReader(`{"model":"gpt-image-2.5-flare","prompt":"draw a cat","size":"1792x1024","n":99}`))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer client-token")
				return req
			}(),
			wantStatus: http.StatusCreated,
			wantPath:   "/api/openai" + openai.GenerationsPath,
			wantBody:   `{"data":[{"b64_json":"payload"}]}`,
			wantType:   "application/json",
		},
		{
			name:       "edit",
			request:    newEditRequest(t, 2),
			wantStatus: http.StatusOK,
			wantPath:   "/api/openai" + openai.EditsPath,
			wantBody:   `{"data":[{"b64_json":"payload"}]}`,
			wantType:   "application/json",
		},
	}

	for _, tt := range requests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, tt.request)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if got := rec.Header().Get("X-Upstream-Trace"); got != tt.wantPath {
				t.Fatalf("X-Upstream-Trace = %q, want %q", got, tt.wantPath)
			}
			if got, want := rec.Body.String(), tt.wantBody; got != want {
				t.Fatalf("response body = %q, want %q", got, want)
			}
			if got, want := rec.Header().Get("Content-Type"), tt.wantType; got != want {
				t.Fatalf("Content-Type = %q, want %q", got, want)
			}
			if tt.wantFlush && !rec.Flushed {
				t.Fatal("streaming response was not flushed")
			}
		})
	}

	receivedMu.Lock()
	gotReceived := append([]receivedRequest(nil), received...)
	receivedMu.Unlock()
	if got, want := len(gotReceived), len(requests); got != want {
		t.Fatalf("upstream request count = %d, want %d", got, want)
	}
	for index, got := range gotReceived {
		if got.path != requests[index].wantPath {
			t.Errorf("request %d path = %q, want %q", index, got.path, requests[index].wantPath)
		}
		if got.query != "" {
			t.Errorf("request %d query = %q, want empty", index, got.query)
		}
		if got.cookie != "test-session" {
			t.Errorf("request %d session cookie = %q, want test-session", index, got.cookie)
		}
	}
	if got, want := gotReceived[0].method, http.MethodGet; got != want {
		t.Errorf("models method = %s, want %s", got, want)
	}
	usageSnapshot := stats.Snapshot()
	if usageSnapshot.TotalRequests != 3 || usageSnapshot.Successes != 3 || usageSnapshot.Failures != 0 {
		t.Fatalf("usage totals = %+v, want 3 successful generation requests", usageSnapshot)
	}
	if len(usageSnapshot.Models) != 2 || usageSnapshot.Models[0].Model != "gpt-image-2.5-flare" {
		t.Fatalf("usage by model = %+v", usageSnapshot.Models)
	}
	requestRecords := stats.RequestRecords()
	if len(requestRecords) != 3 {
		t.Fatalf("request records = %d, want 3", len(requestRecords))
	}
	types := make(map[string]bool)
	for _, record := range requestRecords {
		types[record.Type] = true
		if record.Endpoint == openai.ChatCompletionsPath && record.Type != "stream" {
			t.Fatalf("chat request type = %q, want stream", record.Type)
		}
		if (record.Endpoint == openai.GenerationsPath || record.Endpoint == openai.EditsPath) && record.Model != "gpt-image-2.5-flare" {
			t.Fatalf("image model = %q, want gpt-image-2.5-flare", record.Model)
		}
		if record.Model == "gpt-image-2.5-flare" && record.Type != "image" {
			t.Fatalf("image request type = %q, want image", record.Type)
		}
		if record.Endpoint == openai.GenerationsPath && record.Prompt != "draw a cat" {
			t.Fatalf("generation prompt = %q, want %q", record.Prompt, "draw a cat")
		}
		if record.Endpoint == openai.EditsPath && record.Prompt != "draw a cat from the input images" {
			t.Fatalf("edit prompt = %q, want %q", record.Prompt, "draw a cat from the input images")
		}
	}
	if !types["stream"] || !types["image"] {
		t.Fatalf("request types = %+v", types)
	}

	forwarded := forwardCalls.Load()
	for _, path := range []string{
		"/",
		"/v1/models/",
		"/api/user-auth",
	} {
		t.Run("reject "+path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
			}
		})
	}

	if got := forwardCalls.Load(); got != forwarded {
		t.Fatalf("rejected requests reached upstream: got %d forward calls, want %d", got, forwarded)
	}
}

func TestAdminStaticAssetsAreServedWithSecurityHeaders(t *testing.T) {
	handler := NewHandler(nil)
	for _, path := range []string{"/admin/", "/admin/app.css", "/admin/app.js"} {
		t.Run(path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", recorder.Code)
			}
			if recorder.Body.Len() == 0 {
				t.Fatal("static asset response is empty")
			}
			if got := recorder.Header().Get("Content-Security-Policy"); got == "" {
				t.Fatal("Content-Security-Policy header is missing")
			}
			if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
			}
		})
	}
}

func TestRootRedirectsToAdmin(t *testing.T) {
	handler := NewHandler(nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusFound {
		t.Fatalf("root status = %d, want 302", recorder.Code)
	}
	if got := recorder.Header().Get("Location"); got != "/admin/" {
		t.Fatalf("root redirect = %q, want /admin/", got)
	}
}

func TestRejectedImageRequestIsRecorded(t *testing.T) {
	stats := usage.New()
	handler := NewHandlerWithUsage(nil, stats)
	req := httptest.NewRequest(http.MethodPost, openai.GenerationsPath, strings.NewReader(`{"prompt":""}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	records := stats.RequestRecords()
	if len(records) != 1 {
		t.Fatalf("request records = %d, want 1", len(records))
	}
	if got, want := records[0].Endpoint, openai.GenerationsPath; got != want {
		t.Fatalf("endpoint = %q, want %q", got, want)
	}
	if got, want := records[0].Type, "image"; got != want {
		t.Fatalf("type = %q, want %q", got, want)
	}
	if records[0].Success || records[0].Status != http.StatusBadRequest {
		t.Fatalf("record = %+v, want failed 400", records[0])
	}
}

func TestHandlerReturns503WhenAllAccountsAreCoolingDown(t *testing.T) {
	var modelCalls atomic.Int32
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user-auth":
			var login struct {
				Identifier string `json:"identifier"`
			}
			if err := json.NewDecoder(r.Body).Decode(&login); err != nil {
				t.Errorf("decode login: %v", err)
			}
			http.SetCookie(w, &http.Cookie{Name: "session", Value: login.Identifier, Path: "/"})
			_, _ = io.WriteString(w, `{}`)
		case "/api/openai" + openai.ModelsPath:
			modelCalls.Add(1)
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = io.WriteString(w, `{"error":"insufficient credits"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstreamServer.Close()

	cfg := &config.Config{}
	cfg.Upstream.BaseURL = upstreamServer.URL
	cfg.Upstream.OpenAIBaseURL = upstreamServer.URL + "/api/openai"
	cfg.Upstream.LoginPath = "/api/user-auth"
	cfg.Auth.Accounts = "first:pw1,second:pw2"
	up, err := upstream.New(cfg)
	if err != nil {
		t.Fatalf("new upstream: %v", err)
	}
	handler := NewHandler(up)

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, openai.ModelsPath, nil))
	if first.Code != http.StatusPaymentRequired {
		t.Fatalf("first status = %d, want %d; body=%s", first.Code, http.StatusPaymentRequired, first.Body.String())
	}

	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, openai.ModelsPath, nil))
	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("second status = %d, want %d; body=%s", second.Code, http.StatusServiceUnavailable, second.Body.String())
	}
	if got := second.Header().Get("Retry-After"); got == "" || got == "0" {
		t.Fatalf("Retry-After = %q, want positive seconds", got)
	}
	if got := modelCalls.Load(); got != 2 {
		t.Fatalf("upstream model calls = %d, want 2", got)
	}
}

func newEditRequest(t *testing.T, imageCount int) *http.Request {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", "gpt-image-2.5-flare"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("prompt", "draw a cat from the input images"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("response_format", "url"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("size", "1792x1024"); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < imageCount; index++ {
		fieldName := "image"
		if index%2 == 1 {
			fieldName = "image[]"
		}
		part, err := writer.CreateFormFile(fieldName, "client-controlled-name.png")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, openai.EditsPath, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}
