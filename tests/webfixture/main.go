// webfixture serves the embedded admin UI against a deterministic local upstream.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"clodesen2api/internal/admin"
	"clodesen2api/internal/admission"
	"clodesen2api/internal/apikey"
	"clodesen2api/internal/backups"
	"clodesen2api/internal/config"
	"clodesen2api/internal/controlstore"
	"clodesen2api/internal/jobs"
	"clodesen2api/internal/modelcooldown"
	"clodesen2api/internal/modelpolicy"
	"clodesen2api/internal/server"
	"clodesen2api/internal/upstream"
	"clodesen2api/internal/usage"
)

const fixturePNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jY1sAAAAASUVORK5CYII="

func main() {
	dir, err := os.MkdirTemp("", "clodesen-web-fixture-")
	must(err)
	defer os.RemoveAll(dir)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var result any = map[string]any{}
		switch r.URL.Path {
		case "/api/user-auth":
			result = map[string]any{"user": map[string]string{"id": "fixture"}}
		case "/api/openai/v1/models":
			result = map[string]any{"object": "list", "data": []map[string]string{{"id": "allowed-model"}, {"id": "other-model"}}}
		case "/api/openai/v1/chat/completions":
			var body struct {
				Model    string `json:"model"`
				Stream   bool   `json:"stream"`
				Messages []struct {
					Content string `json:"content"`
				} `json:"messages"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Stream {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"fixture \"}}]}\n\n")
				w.(http.Flusher).Flush()
				if len(body.Messages) > 0 && body.Messages[0].Content == "fixture:streaming" {
					select {
					case <-r.Context().Done():
						return
					case <-time.After(10 * time.Second):
					}
				}
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"stream\"}}]}\n\ndata: [DONE]\n\n")
				return
			}
			if len(body.Messages) > 0 && body.Messages[0].Content == "fixture:429" {
				w.Header().Set("Retry-After", "120")
				w.WriteHeader(429)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "rate_limit_exceeded"}})
				return
			}
			result = map[string]any{"id": "fixture-completion", "object": "chat.completion", "model": body.Model, "choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": "fixture reply"}}}}
			if len(body.Messages) > 0 && body.Messages[0].Content == "fixture:metered" {
				result.(map[string]any)["usage"] = map[string]int{"total_tokens": 20, "credits": 3}
			}
		case "/api/openai/v1/responses":
			var body struct {
				Model  string `json:"model"`
				Input  string `json:"input"`
				Stream bool   `json:"stream"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Input == "" || body.Model == "" {
				http.Error(w, "missing Responses input/model", 400)
				return
			}
			text, status := "fixture responses reply", "completed"
			if body.Stream {
				text = "fixture responses stream"
			}
			response := map[string]any{"id": "resp_fixture", "object": "response", "model": body.Model, "status": status, "output": []any{map[string]any{"type": "message", "content": []any{map[string]string{"type": "output_text", "text": text}}}}, "usage": map[string]int{"input_tokens": 12, "output_tokens": 8, "total_tokens": 20, "credits": 3}}
			if body.Input == "fixture:incomplete" {
				status = "incomplete"
				response["status"] = status
				response["incomplete_details"] = map[string]string{"reason": "max_output_tokens"}
			}
			if body.Input == "fixture:failed" {
				status = "failed"
				response["status"] = status
				response["error"] = map[string]string{"message": "fixture response failed"}
			}
			if body.Stream {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, ": fixture heartbeat\r\n\r\n")
				delta, _ := json.Marshal(map[string]string{"type": "response.output_text.delta", "delta": text})
				fmt.Fprintf(w, "event: response.output_text.delta\r\ndata: %s\r\n\r\n", delta)
				if body.Input != "fixture:truncated" {
					terminal, _ := json.Marshal(map[string]any{"type": "response." + status, "response": response})
					fmt.Fprintf(w, "event: response.%s\r\ndata: %s\r\n\r\n", status, terminal)
				}
				return
			}
			result = response
		case "/api/openai/v1/images/generations":
			var body struct{ Model, Prompt, Size string }
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model == "" || body.Prompt == "" {
				http.Error(w, "missing generation model/prompt", 400)
				return
			}
			result = map[string]any{"data": []any{map[string]string{"b64_json": fixturePNG, "revised_prompt": "fixture generation:" + body.Size}}}
		case "/api/openai/v1/images/edits":
			if r.ParseMultipartForm(1<<20) != nil {
				http.Error(w, "invalid edit multipart", 400)
				return
			}
			defer r.MultipartForm.RemoveAll()
			files := r.MultipartForm.File["image"]
			if len(files) == 0 || r.FormValue("prompt") == "" || r.FormValue("model") == "" || r.FormValue("response_format") != "b64_json" {
				http.Error(w, "missing edit image/model/prompt/format", 400)
				return
			}
			for _, file := range files {
				reader, err := file.Open()
				if err != nil {
					http.Error(w, "unreadable edit image", 400)
					return
				}
				data, err := io.ReadAll(reader)
				reader.Close()
				if err != nil || base64.StdEncoding.EncodeToString(data) != fixturePNG {
					http.Error(w, "edit image bytes changed", 400)
					return
				}
			}
			result = map[string]any{"data": []any{map[string]string{"b64_json": fixturePNG, "revised_prompt": fmt.Sprintf("fixture edit:%d:%s", len(files), strings.TrimSpace(r.FormValue("size")))}}}
		case "/api/checkin":
			result = map[string]any{"enabled": true, "checkedIn": true, "balance": 1234}
		case "/api/limited-redemption":
			if r.Method == http.MethodPost {
				result = map[string]any{"redemption": map[string]int{"credits": 500}, "status": map[string]int{"remaining": 1}}
			} else {
				result = map[string]any{"enabled": true, "remaining": 2, "limit": 3}
			}
		case "/api/credits":
			result = map[string]any{"pricing": map[string]any{"models": []any{
				map[string]any{"model": "allowed-model", "creditsPerRequest": 100, "enabled": true},
				map[string]any{"model": "other-model", "tokenPricing": map[string]int{"input": 1, "output": 2}, "enabled": true},
			}}}
		case "/api/account":
			result = map[string]any{"account": map[string]any{"transactions": []any{}}}
		default:
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer remote.Close()
	cfg := &config.Config{}
	cfg.Upstream.BaseURL, cfg.Upstream.OpenAIBaseURL, cfg.Upstream.LoginPath = remote.URL, remote.URL+"/api/openai", "/api/user-auth"
	cfg.Admin.TokenFile = filepath.Join(dir, "admin.token")
	cfg.Server.Addr = "127.0.0.1:0"
	cfg.Upstream.LoginTimeout = 30 * time.Second
	cfg.Auth.Strategy = "fixed"
	cfg.Checkin.Path = "/api/checkin"
	cfg.Checkin.At, cfg.Checkin.Timezone = "04:00", "Asia/Shanghai"
	credentials := []upstream.AccountCredential{{ID: "fixture", Identifier: "fixture@example.test", Password: "fixture-password", Enabled: true}}
	up, err := upstream.NewWithAccounts(cfg, credentials)
	must(err)
	control, err := controlstore.Open(filepath.Join(dir, "control.sqlite3"))
	must(err)
	defer control.Close()
	settings, err := config.OpenSettings(context.Background(), control, cfg)
	must(err)
	accounts, err := admin.OpenControlAccounts(control, filepath.Join(dir, "accounts.json"), "fixed", credentials)
	must(err)
	capacity, err := admission.Open(control)
	must(err)
	policies, err := modelpolicy.Open(control)
	must(err)
	cooldowns, err := modelcooldown.Open(control)
	must(err)
	up.SetModelCooldowns(cooldowns)
	keys, err := apikey.OpenControl(control, filepath.Join(dir, "keys.json"))
	must(err)
	auth, err := apikey.NewAuthenticator("", keys, false, "")
	must(err)
	stats, err := usage.NewControl(control, filepath.Join(dir, "usage.json"))
	must(err)
	defer stats.Close()
	panel := admin.NewServiceWithConfig(up, accounts, "test-admin-123", cfg.Admin.TokenFile, cfg, stats)
	panel.SetKeyStore(keys)
	panel.SetControlStore(control)
	must(panel.MigrateControlData(context.Background()))
	panel.SetSettings(settings)
	panel.SetModelPolicies(policies)
	panel.SetModelCooldowns(cooldowns)
	panel.SetAdmission(capacity)
	backupStore, err := backups.New(control, filepath.Join(dir, "backups"), "fixture", nil)
	must(err)
	panel.SetBackups(backupStore)
	jobStore, err := jobs.Open(control)
	must(err)
	runner := panel.StartJobs(context.Background(), jobStore)
	defer runner.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	fmt.Println("WEB_FIXTURE_URL=http://" + listener.Addr().String())
	must(http.Serve(listener, panel.WithAccountSync(server.NewHandlerWithAdmission(up, stats, auth, policies, capacity, panel))))
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
