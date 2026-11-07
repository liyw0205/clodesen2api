package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"clodesen2api/internal/admission"
	"clodesen2api/internal/apikey"
	"clodesen2api/internal/config"
	"clodesen2api/internal/controlstore"
	"clodesen2api/internal/modelpolicy"
	"clodesen2api/internal/upstream"
	"clodesen2api/internal/usage"
)

func TestMeteredImageCreditFailoverSettlesOnlyGeneration(t *testing.T) {
	for _, mode := range []string{"known", "unknown", "disconnected"} {
		t.Run(mode, func(t *testing.T) {
			var rejected, generated atomic.Int32
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/login" {
					var login struct{ Identifier string }
					_ = json.NewDecoder(r.Body).Decode(&login)
					http.SetCookie(w, &http.Cookie{Name: "account", Value: login.Identifier})
					fmt.Fprint(w, `{}`)
					return
				}
				if r.URL.Path == "/api/compute-route" {
					w.WriteHeader(404)
					return
				}
				if r.URL.Path == "/checkin" {
					fmt.Fprint(w, `{"enabled":true,"balance":127}`)
					return
				}
				if r.URL.Path != "/v1/images/generations" {
					w.WriteHeader(404)
					return
				}
				cookie, _ := r.Cookie("account")
				if cookie == nil {
					w.WriteHeader(401)
					return
				}
				if cookie.Value == "low" {
					rejected.Add(1)
					w.WriteHeader(400)
					fmt.Fprint(w, `{"error":{"code":"insufficient_credits"},"usage":{"credits":0}}`)
					return
				}
				generated.Add(1)
				if mode == "disconnected" {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
					return
				}
				if mode == "known" {
					fmt.Fprint(w, `{"data":[{"b64_json":"ok"}],"usage":{"credits":150}}`)
				} else {
					fmt.Fprint(w, `{"data":[{"b64_json":"ok"}]}`)
				}
			}))
			defer remote.Close()
			cfg := &config.Config{}
			cfg.Upstream.BaseURL, cfg.Upstream.OpenAIBaseURL, cfg.Upstream.LoginPath, cfg.Checkin.Path = remote.URL, remote.URL, "/login", "/checkin"
			up, err := upstream.NewWithAccounts(cfg, []upstream.AccountCredential{{ID: "low", Identifier: "low", Password: "pw", Enabled: true}, {ID: "enough", Identifier: "enough", Password: "pw", Enabled: true}})
			if err != nil {
				t.Fatal(err)
			}
			db, err := controlstore.Open(filepath.Join(t.TempDir(), "control.sqlite3"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			capacity, err := admission.Open(db)
			if err != nil {
				t.Fatal(err)
			}
			keys, err := apikey.OpenControl(db, "")
			if err != nil {
				t.Fatal(err)
			}
			_, secret, err := keys.Create(apikey.CreateRequest{Name: "image", Limits: admission.Policy{DailyCredits: 1000}})
			if err != nil {
				t.Fatal(err)
			}
			auth, err := apikey.NewAuthenticator("", keys, false, "")
			if err != nil {
				t.Fatal(err)
			}
			policies, err := modelpolicy.Open(db)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if err := policies.Register(ctx, []string{"image"}); err != nil {
				t.Fatal(err)
			}
			snapshot, err := policies.Snapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			_, err = policies.Put(ctx, snapshot.Revision, modelpolicy.Rule{SourceID: "image", PublicID: "image", UpstreamID: "image", Enabled: true, Metering: admission.Meter{Source: "upstream-usage-v1", Credits: 200}})
			if err != nil {
				t.Fatal(err)
			}
			h := NewHandlerWithAdmission(up, usage.New(), auth, policies, capacity)
			r := httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(`{"model":"image","prompt":"test"}`))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer "+secret)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			wantStatus := 200
			if mode == "disconnected" {
				wantStatus = 502
			}
			if w.Code != wantStatus || (mode != "disconnected" && !strings.Contains(w.Body.String(), "b64_json")) || rejected.Load() != 1 || generated.Load() != 1 {
				t.Fatalf("failover: %d %s rejected=%d generated=%d", w.Code, w.Body.String(), rejected.Load(), generated.Load())
			}
			view, err := capacity.Snapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, balance := range view.Balances {
				if mode == "known" {
					if balance.UsedCredits != 150 || balance.ReservedCredits != 0 {
						t.Fatalf("usage settlement: %+v", balance)
					}
				} else {
					if balance.UsedCredits != 0 || balance.ReservedCredits != 200 {
						t.Fatalf("unknown usage was released by rejected attempt: %+v", balance)
					}
				}
			}
		})
	}
}
