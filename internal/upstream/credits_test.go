package upstream

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"clodesen2api/internal/config"
	"clodesen2api/internal/openai"
)

func TestListPricingAndCreditUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user-auth":
			var body struct {
				Identifier string `json:"identifier"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			http.SetCookie(w, &http.Cookie{Name: "session", Value: body.Identifier, Path: "/"})
			_, _ = io.WriteString(w, `{}`)
		case "/api/credits":
			if r.URL.Query().Get("scope") != "pricing" {
				http.NotFound(w, r)
				return
			}
			_, _ = io.WriteString(w, `{"pricing":{"defaultRequestCredits":0,"models":[{"model":"gpt-image-2","creditsPerRequest":100,"enabled":true}]}}`)
		case "/api/account":
			_, _ = io.WriteString(w, `{"account":{"transactions":[{"id":7,"amount":-100,"balance_after":900,"type":"model_usage","description":"模型请求：gpt-image-2","status":"completed","created_at":"2026-11-02T05:00:00Z"},{"id":8,"amount":100,"balance_after":1000,"type":"refund","description":"模型请求退款：gpt-image-2","status":"completed","created_at":"2026-11-02T05:01:00Z"}]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := &config.Config{}
	cfg.Upstream.BaseURL = server.URL
	cfg.Upstream.LoginPath = "/api/user-auth"
	cfg.Auth.Strategy = "round_robin"
	pool, err := NewWithAccounts(cfg, []AccountCredential{{
		ID: "account-one", Identifier: "one@example.test", Password: "pw", Enabled: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, pricing, err := pool.ListPricing(context.Background())
	if err != nil || len(pricing) != 1 || pricing[0].CreditsPerRequest == nil || *pricing[0].CreditsPerRequest != 100 {
		t.Fatalf("pricing = %+v, err=%v", pricing, err)
	}
	records, err := pool.ListCreditUsage(context.Background())
	if err != nil || len(records) != 2 {
		t.Fatalf("records = %+v, err=%v", records, err)
	}
	if records[0].AccountID != "account-one" || records[0].Model != "gpt-image-2" || records[0].Amount != 100 || records[1].Model != "gpt-image-2" {
		t.Fatalf("records = %+v", records)
	}
}

func TestForwardRecordsLocalCreditUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user-auth":
			_, _ = io.WriteString(w, `{}`)
		case "/api/openai/v1/chat/completions":
			_, _ = io.WriteString(w, `{"id":"chatcmpl-test","choices":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := &config.Config{}
	cfg.Upstream.BaseURL = server.URL
	cfg.Upstream.OpenAIBaseURL = server.URL + "/api/openai"
	cfg.Upstream.LoginPath = "/api/user-auth"
	pool, err := NewWithAccounts(cfg, []AccountCredential{{
		ID: "account-one", Identifier: "one@example.test", Password: "pw", Enabled: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	credits := int64(60)
	pool.SetCreditPricing([]ModelPricing{{Model: "gpt-5.6", CreditsPerRequest: &credits}})
	recorded := make(chan CreditUsageRecord, 1)
	pool.SetCreditUsageRecorder(func(record CreditUsageRecord) { recorded <- record })

	response := httptest.NewRecorder()
	err, wrote := pool.Forward(context.Background(), response, openai.Request{
		Method: http.MethodPost, Path: openai.ChatCompletionsPath, Model: "gpt-5.6",
		ContentType: "application/json", Body: []byte(`{"model":"gpt-5.6"}`),
	})
	if err != nil || !wrote || response.Code != http.StatusOK {
		t.Fatalf("Forward() = (%v, %v), status=%d", err, wrote, response.Code)
	}
	select {
	case record := <-recorded:
		if record.AccountID != "account-one" || record.Model != "gpt-5.6" || record.Amount != -60 || record.Source != "local" {
			t.Fatalf("local record = %+v", record)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for local credit record")
	}
}
