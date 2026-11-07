package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"clodesen2api/internal/admission"
	"clodesen2api/internal/config"
	"clodesen2api/internal/openai"
)

func TestCreditRejectionSwitchesImageAccounts(t *testing.T) {
	for _, path := range []string{openai.GenerationsPath, openai.EditsPath} {
		for _, balance := range []int64{80, 100, 127} {
			for _, mode := range []string{"json", "sse", "route"} {
				t.Run(fmt.Sprintf("%s/%d/%s", path, balance, mode), func(t *testing.T) {
					var mu sync.Mutex
					var calls []string
					remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						if r.URL.Path == "/login" {
							var login struct{ Identifier string }
							_ = json.NewDecoder(r.Body).Decode(&login)
							http.SetCookie(w, &http.Cookie{Name: "account", Value: login.Identifier})
							fmt.Fprint(w, `{}`)
							return
						}
						cookie, _ := r.Cookie("account")
						if cookie == nil {
							t.Error("missing login cookie")
							w.WriteHeader(401)
							return
						}
						if r.URL.Path == "/checkin" {
							fmt.Fprintf(w, `{"enabled":true,"balance":%d}`, balance)
							return
						}
						if r.URL.Path == "/api/compute-route" && mode != "route" {
							w.WriteHeader(404)
							return
						}
						if r.URL.Path != "/api/compute-route" && r.URL.Path != "/openai"+path {
							w.WriteHeader(404)
							return
						}
						if r.URL.Path == "/api/compute-route" && cookie.Value == "enough" {
							w.WriteHeader(404)
							return
						}
						mu.Lock()
						calls = append(calls, cookie.Value)
						mu.Unlock()
						if cookie.Value != "enough" {
							if mode == "sse" {
								w.Header().Set("Content-Type", "text/event-stream")
								fmt.Fprintf(w, "data: {\"error\":{\"message\":\"积分余额不足：本次需要 150 积分，当前可用 %d 积分\"}}\n\n", balance)
							} else {
								w.WriteHeader(http.StatusBadRequest)
								fmt.Fprintf(w, `{"error":{"message":"积分余额不足：本次需要 150 积分，当前可用 %d 积分"}}`, balance)
							}
							return
						}
						fmt.Fprint(w, `{"data":[{"b64_json":"ok"}]}`)
					}))
					defer remote.Close()
					cfg := &config.Config{}
					cfg.Upstream.BaseURL, cfg.Upstream.OpenAIBaseURL, cfg.Upstream.LoginPath = remote.URL, remote.URL+"/openai", "/login"
					cfg.Checkin.Path = "/checkin"
					cfg.Auth.Strategy = "fixed"
					credentials := []AccountCredential{}
					for _, id := range []string{"low1", "low2", "low3", "low4", "enough"} {
						credentials = append(credentials, AccountCredential{ID: id, Identifier: id, Password: "pw", Enabled: true})
					}
					pool, err := NewWithAccounts(cfg, credentials)
					if err != nil {
						t.Fatal(err)
					}
					limits := admission.Defaults()
					limits.MaxAttempts = 1
					rec := httptest.NewRecorder()
					err, wrote := pool.Forward(admission.WithLimits(context.Background(), limits), rec, openai.Request{Method: http.MethodPost, Path: path, Model: "image", ContentType: "application/json", Body: []byte(`{"model":"image"}`)})
					if err != nil || !wrote || rec.Code != 200 || rec.Body.String() != `{"data":[{"b64_json":"ok"}]}` {
						t.Fatalf("forward: %v %t %d %s", err, wrote, rec.Code, rec.Body.String())
					}
					if !reflect.DeepEqual(calls, []string{"low1", "low2", "low3", "low4", "enough"}) {
						t.Fatalf("account order: %v", calls)
					}
					for _, account := range pool.accounts[:4] {
						if cooling := !pool.accountAvailable(account); cooling != (balance <= 100) {
							t.Fatalf("balance=%d cooling=%t", balance, cooling)
						}
					}
					if pool.fixedAccount != pool.accounts[4] {
						t.Fatal("fixed routing did not retain successful replacement")
					}
				})
			}
		}
	}
}

func TestExpiredCreditCooldownRequiresBalanceRecheck(t *testing.T) {
	for _, balance := range []int64{-1, 80, 100, 101} {
		t.Run(fmt.Sprint(balance), func(t *testing.T) {
			var checks atomic.Int32
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/login" {
					fmt.Fprint(w, `{}`)
					return
				}
				if r.URL.Path == "/checkin" {
					checks.Add(1)
					if balance < 0 {
						w.WriteHeader(503)
						return
					}
					fmt.Fprintf(w, `{"enabled":true,"balance":%d}`, balance)
					return
				}
				w.WriteHeader(404)
			}))
			defer remote.Close()
			cfg := &config.Config{}
			cfg.Upstream.BaseURL, cfg.Upstream.LoginPath, cfg.Checkin.Path = remote.URL, "/login", "/checkin"
			expired := time.Now().Add(-time.Second)
			pool, err := NewWithAccounts(cfg, []AccountCredential{{ID: "low", Identifier: "low", Password: "pw", Enabled: true, CooldownUntil: &expired, CooldownReason: cooldownReasonInsufficientCredits}})
			if err != nil {
				t.Fatal(err)
			}
			account := pool.accounts[0]
			if pool.accountAvailable(account) || pool.reserveAccountRequest(account) {
				t.Fatal("expired credit cooldown became usable without checking balance")
			}
			var wg sync.WaitGroup
			for i := 0; i < 10; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); pool.CalibrateCreditCooldowns(context.Background()) }()
			}
			wg.Wait()
			if checks.Load() != 1 {
				t.Fatalf("balance checks=%d", checks.Load())
			}
			if available := pool.accountAvailable(account); available != (balance > 100) {
				t.Fatalf("balance=%d available=%t", balance, available)
			}
			if balance <= 100 && !pool.cooldownUntil[account].After(time.Now()) {
				t.Fatal("cooldown not extended")
			}
		})
	}
}

func TestCreditErrorDetectionIgnoresGeneratedContent(t *testing.T) {
	for _, body := range []string{
		`{"choices":[{"message":{"content":"insufficient credits"}}]}`,
		"data: {\"choices\":[{\"delta\":{\"content\":\"insufficient credits\"}}]}\n\n",
		`{"data":[{"b64_json":"ok"}],"revised_prompt":"积分不足"}`,
	} {
		if insufficientCredits(200, []byte(body)) {
			t.Fatalf("generated content treated as rejection: %s", body)
		}
	}
	if !insufficientCredits(200, []byte(": keepalive\n\nevent: error\ndata: {\"error\":{\"message\":\"insufficient credits\"}}\n\n")) {
		t.Fatal("SSE error after heartbeat not detected")
	}
}

func TestExpiredTemporaryBanCanBecomeCreditCooldown(t *testing.T) {
	cfg := &config.Config{}
	pool, err := NewWithAccounts(cfg, []AccountCredential{{ID: "a", Identifier: "a", Password: "pw", Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	account := pool.accounts[0]
	pool.cooldownUntil[account] = time.Now().Add(-time.Second)
	pool.cooldownReason[account] = cooldownReasonTemporaryBan
	account.applyCreditBalance(80)
	if pool.cooldownReasonFor(account) != cooldownReasonInsufficientCredits {
		t.Fatal("expired ban hid credit cooldown")
	}
}

func TestSuccessfulAccountTasksOnlyClearCooldownAfterBalanceRecovery(t *testing.T) {
	for _, balance := range []int64{80, 100, 101} {
		t.Run(fmt.Sprint(balance), func(t *testing.T) {
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/user-auth":
					fmt.Fprint(w, `{}`)
				case "/api/checkin":
					fmt.Fprintf(w, `{"enabled":true,"checkedIn":true,"balance":%d}`, balance)
				case "/api/limited-redemption":
					if r.Method == http.MethodGet {
						fmt.Fprint(w, `{"enabled":true,"remaining":1}`)
					} else {
						fmt.Fprint(w, `{"redemption":{"credits":1}}`)
					}
				default:
					w.WriteHeader(404)
				}
			}))
			defer remote.Close()
			cfg := &config.Config{}
			cfg.Upstream.BaseURL, cfg.Upstream.LoginPath, cfg.Checkin.Path = remote.URL, "/api/user-auth", "/api/checkin"
			pool, err := NewWithAccounts(cfg, []AccountCredential{{ID: "a", Identifier: "a", Password: "pw", Enabled: true}})
			if err != nil {
				t.Fatal(err)
			}
			account := pool.accounts[0]
			pool.coolDown(account)
			if _, err := pool.CheckinAccount(context.Background(), "a"); err != nil {
				t.Fatal(err)
			}
			if pool.accountAvailable(account) != (balance > 100) {
				t.Fatal("checkin cleared cooldown without enough credits")
			}
			pool.coolDown(account)
			if result := pool.RedeemCode(context.Background(), "a", "code"); result.Status != "success" {
				t.Fatalf("redeem: %+v", result)
			}
			if pool.accountAvailable(account) != (balance > 100) {
				t.Fatal("redemption cleared cooldown without enough credits")
			}
		})
	}
}
