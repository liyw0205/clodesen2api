package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"clodesen2api/internal/admission"
	"clodesen2api/internal/apikey"
	"clodesen2api/internal/config"
	"clodesen2api/internal/controlstore"
	"clodesen2api/internal/modelpolicy"
	"clodesen2api/internal/upstream"
	"clodesen2api/internal/usage"
)

func TestVerifiedUsageSettlesJSONAndStream(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "stream"}[stream], func(t *testing.T) {
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/login" {
					io.WriteString(w, `{}`)
					return
				}
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: {\"usage\":{\"total_tokens\":20,\"credits\":3}}\n\ndata: [DONE]\n\n")
				} else {
					io.WriteString(w, `{"choices":[],"usage":{"total_tokens":20,"credits":3}}`)
				}
			}))
			defer remote.Close()
			cfg := &config.Config{}
			cfg.Upstream.BaseURL, cfg.Upstream.OpenAIBaseURL, cfg.Upstream.LoginPath = remote.URL, remote.URL, "/login"
			up, err := upstream.New(cfg)
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
			_, secret, err := keys.Create(apikey.CreateRequest{Name: "metered", Limits: admission.Policy{DailyTokens: 100, DailyCredits: 20}})
			if err != nil {
				t.Fatal(err)
			}
			auth, _ := apikey.NewAuthenticator("", keys, false, "")
			policies, err := modelpolicy.Open(db)
			if err != nil {
				t.Fatal(err)
			}
			if err := policies.Register(context.Background(), []string{"model"}); err != nil {
				t.Fatal(err)
			}
			snapshot, _ := policies.Snapshot(context.Background())
			_, err = policies.Put(context.Background(), snapshot.Revision, modelpolicy.Rule{SourceID: "model", PublicID: "model", UpstreamID: "model", Enabled: true, Metering: admission.Meter{Source: "upstream-usage-v1", Tokens: 80, Credits: 10}})
			if err != nil {
				t.Fatal(err)
			}
			h := NewHandlerWithAdmission(up, usage.New(), auth, policies, capacity)
			call := func() *httptest.ResponseRecorder {
				body := `{"model":"model","stream":false}`
				if stream {
					body = `{"model":"model","stream":true}`
				}
				r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Authorization", "Bearer "+secret)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w
			}
			for i := 0; i < 2; i++ {
				if w := call(); w.Code != 200 {
					t.Fatalf("%d %s", w.Code, w.Body.String())
				}
			}
			if w := call(); w.Code != 429 {
				t.Fatalf("reservation bypass: %d %s", w.Code, w.Body.String())
			}
			view, _ := capacity.Snapshot(context.Background())
			for _, balance := range view.Balances {
				if balance.UsedTokens != 40 || balance.ReservedTokens != 0 || balance.UsedCredits != 6 {
					t.Fatalf("settlement: %+v", balance)
				}
			}
		})
	}
}
