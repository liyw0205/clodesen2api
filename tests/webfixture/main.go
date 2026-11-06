// webfixture serves the embedded admin UI against a deterministic local upstream.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
				Messages []struct {
					Content string `json:"content"`
				} `json:"messages"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if len(body.Messages) > 0 && body.Messages[0].Content == "fixture:429" {
				w.Header().Set("Retry-After", "120")
				w.WriteHeader(429)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "rate_limit_exceeded"}})
				return
			}
			result = map[string]any{"id": "fixture-completion", "object": "chat.completion", "model": body.Model}
			if len(body.Messages) > 0 && body.Messages[0].Content == "fixture:metered" {
				result.(map[string]any)["usage"] = map[string]int{"total_tokens": 20, "credits": 3}
			}
		case "/api/checkin":
			result = map[string]any{"enabled": true, "checkedIn": true, "balance": 1234}
		case "/api/limited-redemption":
			result = map[string]any{"enabled": true, "remaining": 2, "limit": 3}
		case "/api/credits":
			result = map[string]any{"pricing": map[string]any{"models": []any{}}}
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
