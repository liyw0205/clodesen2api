package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"clodesen2api/internal/config"
)

// newTestUpstream 构造一个指向上游测试服务器的 Upstream。
func newTestUpstream(t *testing.T, upstreamURL string) *Upstream {
	t.Helper()
	var cfg config.Config
	cfg.Upstream.BaseURL = upstreamURL
	cfg.Upstream.LoginPath = "/api/user-auth"
	cfg.Upstream.UserAgent = "test-agent"
	cfg.Auth.AutoRelogin = false
	cfg.Checkin.Path = "/api/checkin"
	cfg.Checkin.Timezone = "Asia/Shanghai"
	return &Upstream{
		cfg:         &cfg,
		httpClient:  &http.Client{},
		loginClient: &http.Client{},
		loggedIn:    true,
	}
}

// TestCheckinPostsWhenNotYetSigned 保证未签到时会真的发 POST，
// 而不是只做 GET 查询就当作成功（历史 bug）。
func TestCheckinPostsWhenNotYetSigned(t *testing.T) {
	var mu sync.Mutex
	var methods []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/checkin" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		mu.Lock()
		methods = append(methods, r.Method)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			// 查询状态：今天还没签到
			_, _ = w.Write([]byte(`{"enabled":true,"checkedIn":false,"balance":100}`))
			return
		}
		// POST：签到成功
		_, _ = w.Write([]byte(`{"enabled":true,"checkedIn":true,"reward":512,` +
			`"date":"2026-10-27","balance":612,"validityDays":1,"currentStreak":45}`))
	}))
	defer server.Close()

	up := newTestUpstream(t, server.URL)
	res, err := up.Checkin(context.Background())
	if err != nil {
		t.Fatalf("Checkin() error = %v", err)
	}

	mu.Lock()
	got := strings.Join(methods, ",")
	mu.Unlock()
	if got != "GET,POST" {
		t.Fatalf("request sequence = %q, want GET,POST", got)
	}
	if !res.CheckedIn || res.Reward != 512 || res.AlreadyChecked {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.CurrentStreak != 45 {
		t.Fatalf("CurrentStreak = %d, want 45", res.CurrentStreak)
	}
}

// TestCheckinSkipsPostWhenAlreadySigned 保证已签到时不再重复 POST。
func TestCheckinSkipsPostWhenAlreadySigned(t *testing.T) {
	var mu sync.Mutex
	var methods []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"enabled":true,"checkedIn":true,"date":"2026-10-27","balance":612}`))
	}))
	defer server.Close()

	up := newTestUpstream(t, server.URL)
	res, err := up.Checkin(context.Background())
	if err != nil {
		t.Fatalf("Checkin() error = %v", err)
	}

	mu.Lock()
	got := strings.Join(methods, ",")
	mu.Unlock()
	if got != "GET" {
		t.Fatalf("request sequence = %q, want GET only", got)
	}
	if !res.AlreadyChecked || res.Balance != 612 {
		t.Fatalf("unexpected result: %+v", res)
	}
}

// TestCheckinGetButPostFailsIsAnError 保证 POST 失败不会被当成签到成功。
func TestCheckinGetButPostFailsIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"enabled":true,"checkedIn":false,"balance":100}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer server.Close()

	up := newTestUpstream(t, server.URL)
	if _, err := up.Checkin(context.Background()); err == nil {
		t.Fatal("Checkin() error = nil, want error when POST fails")
	}
}

// TestCheckinDisabledUpstream 站点关闭签到时返回明确错误。
func TestCheckinDisabledUpstream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"enabled":false,"checkedIn":false}`))
	}))
	defer server.Close()

	up := newTestUpstream(t, server.URL)
	if _, err := up.Checkin(context.Background()); err == nil {
		t.Fatal("Checkin() error = nil, want error when checkin disabled")
	}
}

// TestCheckinResultDecodesUpstreamFields 锁定上游字段名，避免结构体漂移。
func TestCheckinResultDecodesUpstreamFields(t *testing.T) {
	raw := []byte(`{"enabled":true,"checkedIn":true,"reward":512,"date":"2026-10-26",` +
		`"balance":12,"validityDays":1,"makeupChances":26,"currentStreak":44}`)
	var res CheckinResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.ValidityDays != 1 || res.MakeupChances != 26 || res.CurrentStreak != 44 {
		t.Fatalf("unexpected decode: %+v", res)
	}
}

func TestRefreshAccountStatusDoesNotReadUnsupportedCreditBreakdown(t *testing.T) {
	var accountCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/checkin":
			_, _ = w.Write([]byte(`{"enabled":true,"checkedIn":true,"balance":120}`))
		case "/api/account":
			accountCalls++
			_, _ = w.Write([]byte(`{"account":{"balance":120}}`))
		case "/api/limited-redemption":
			_, _ = w.Write([]byte(`{"enabled":true,"remaining":2,"limit":3}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	up := newTestUpstream(t, server.URL)
	status := up.refreshAccountStatus(context.Background(), AccountStatus{ID: "test", Identifier: "test@example.test", Enabled: true})
	if status.Balance == nil || *status.Balance != 120 || accountCalls != 0 {
		t.Fatalf("status=%+v account endpoint calls=%d, want total balance without split lookup", status, accountCalls)
	}
}

func TestRefreshAccountStatusReadsTemporaryBanAccountFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/checkin":
			_, _ = w.Write([]byte(`{"enabled":true,"checkedIn":true,"balance":321}`))
		case "/api/limited-redemption":
			_, _ = w.Write([]byte(`{"enabled":true,"remaining":4,"limit":10}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	up := newTestUpstream(t, server.URL)
	up.enabled = true
	up.accountID = "temporary-ban"
	up.cooldownUntil = make(map[*Upstream]time.Time)
	up.cooldownReason = make(map[*Upstream]string)
	until := time.Now().Add(time.Hour).UTC()
	up.cooldownUntil[up] = until
	up.cooldownReason[up] = cooldownReasonTemporaryBan

	status := up.refreshAccountStatus(context.Background(), AccountStatus{ID: up.accountID, Identifier: "test@example.test", Enabled: true})
	if status.Balance == nil || *status.Balance != 321 || status.CheckedIn == nil || !*status.CheckedIn ||
		status.RedemptionRemaining == nil || *status.RedemptionRemaining != 4 || status.CooldownReason != cooldownReasonTemporaryBan {
		t.Fatalf("status=%+v, want readable fields and preserved temporary ban", status)
	}
}

func TestCheckinReadsNestedBalance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"enabled":true,"checkedIn":true,"account":{"balance":987}}`))
	}))
	defer server.Close()

	up := newTestUpstream(t, server.URL)
	result, err := up.Checkin(context.Background())
	if err != nil {
		t.Fatalf("Checkin() error = %v", err)
	}
	if !result.BalanceKnown || result.Balance != 987 {
		t.Fatalf("balance = %+v, want known balance 987", result)
	}
}

func TestNormalizeResetAtUsesChinaTime(t *testing.T) {
	got := normalizeResetAt(json.RawMessage(`"2026-11-02T00:00:00Z"`))
	if got != "2026-11-02T08:00:00+08:00" {
		t.Fatalf("normalizeResetAt() = %q", got)
	}
}
