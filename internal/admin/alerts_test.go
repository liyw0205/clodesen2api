package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"clodesen2api/internal/alerts"
	"clodesen2api/internal/config"
	"clodesen2api/internal/controlstore"
	"clodesen2api/internal/jobs"
	"clodesen2api/internal/upstream"
	"clodesen2api/internal/usage"
)

func TestAlertAPIChannelConfigurationAndTestDelivery(t *testing.T) {
	ctx := context.Background()
	db, err := controlstore.Open(filepath.Join(t.TempDir(), "control.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewServiceWithConfig(nil, nil, testAdminToken, "", nil)
	s.SetControlStore(db)

	var deliveries atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/notify" || r.Method != http.MethodPost {
			t.Errorf("unexpected test delivery: %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if !bytes.Contains(body, []byte("告警测试")) {
			t.Errorf("missing test message: %s", body)
		}
		deliveries.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()

	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		s.api(response, req)
		return response
	}

	response := call(http.MethodGet, "/admin/api/alerts", "")
	if response.Code != http.StatusOK {
		t.Fatalf("GET status = %d: %s", response.Code, response.Body)
	}
	var view alerts.PublicView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Definitions) != 13 || view.Revision != 1 {
		t.Fatalf("unexpected channel definitions or initial revision: %d, %d", len(view.Definitions), view.Revision)
	}

	testPayload := map[string]any{
		"revision": 1, "channel": "webhook",
		"config": map[string]string{"url": receiver.URL + "/notify"},
	}
	response = call(http.MethodPost, "/admin/api/alerts/test", jsonBody(t, testPayload))
	if response.Code != http.StatusOK || deliveries.Load() != 1 {
		t.Fatalf("test delivery status = %d, deliveries = %d, body = %s", response.Code, deliveries.Load(), response.Body)
	}
	persisted, err := alerts.Load(ctx, db)
	if err != nil || len(persisted.Settings.Channels) != 0 || persisted.Revision != 1 {
		t.Fatalf("test delivery persisted draft: %+v, %v", persisted, err)
	}

	save := func(revision int64, channels map[string]alerts.ChannelSettings, clear []string) *httptest.ResponseRecorder {
		t.Helper()
		return call(http.MethodPut, "/admin/api/alerts", jsonBody(t, map[string]any{
			"revision":     revision,
			"settings":     alerts.Settings{Channels: channels, LowBalance: 100},
			"clearSecrets": clear,
		}))
	}
	channels := map[string]alerts.ChannelSettings{
		"webhook": {Enabled: true, Config: map[string]string{"url": receiver.URL + "/notify"}},
		"ntfy":    {Enabled: true, Config: map[string]string{"url": "http://127.0.0.1:8002", "topic": "ops"}},
	}
	response = save(1, channels, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("save status = %d: %s", response.Code, response.Body)
	}
	if strings.Contains(response.Body.String(), receiver.URL) {
		t.Fatal("GET/PUT response exposed a secret webhook URL")
	}
	persisted, err = alerts.Load(ctx, db)
	if err != nil || !persisted.Settings.Channels["webhook"].Enabled || !persisted.Settings.Channels["ntfy"].Enabled {
		t.Fatalf("channels were not independently saved: %+v, %v", persisted.Settings.Channels, err)
	}

	// Empty secret inputs preserve the saved value while ordinary settings change.
	response = save(2, map[string]alerts.ChannelSettings{
		"webhook": {Enabled: true, Config: map[string]string{}},
		"ntfy":    {Enabled: false, Config: map[string]string{"url": "http://127.0.0.1:8002", "topic": "ops"}},
	}, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("preserve-secret save status = %d: %s", response.Code, response.Body)
	}
	persisted, err = alerts.Load(ctx, db)
	if err != nil || persisted.Settings.Channels["webhook"].Config["url"] != receiver.URL+"/notify" || persisted.Settings.Channels["ntfy"].Enabled {
		t.Fatalf("secret was not preserved or channel state was coupled: %+v, %v", persisted.Settings.Channels, err)
	}

	response = save(3, map[string]alerts.ChannelSettings{
		"webhook": {Enabled: false, Config: map[string]string{}},
		"ntfy":    {Enabled: false, Config: map[string]string{"url": "http://127.0.0.1:8002", "topic": "ops"}},
	}, []string{"webhook.url"})
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), receiver.URL) {
		t.Fatalf("clear-secret save failed or leaked: %d %s", response.Code, response.Body)
	}
	persisted, err = alerts.Load(ctx, db)
	if err != nil || persisted.Settings.Channels["webhook"].Config["url"] != "" {
		t.Fatalf("explicit secret removal did not persist: %+v, %v", persisted.Settings.Channels["webhook"], err)
	}

	response = save(1, map[string]alerts.ChannelSettings{}, nil)
	if response.Code != http.StatusConflict {
		t.Fatalf("stale save status = %d: %s", response.Code, response.Body)
	}
	response = save(4, map[string]alerts.ChannelSettings{
		"smtp": {Enabled: true, Config: map[string]string{}},
	}, nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid enabled channel status = %d: %s", response.Code, response.Body)
	}

	failureReceiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer failureReceiver.Close()
	testPayload["revision"] = 4
	testPayload["config"] = map[string]string{"url": failureReceiver.URL}
	response = call(http.MethodPost, "/admin/api/alerts/test", jsonBody(t, testPayload))
	if response.Code != http.StatusBadGateway {
		t.Fatalf("failed test delivery status = %d: %s", response.Code, response.Body)
	}
	for _, item := range []struct {
		body string
		code int
	}{
		{`{"revision":1,"channel":"webhook","config":{}}`, http.StatusConflict},
		{`{"revision":4,"channel":"webhook","config":{}}`, http.StatusBadRequest},
		{`{"revision":4,"channel":"unknown","config":{}}`, http.StatusBadRequest},
		{`{"revision":4,"channel":"smtp","config":{"server":"bad/server"}}`, http.StatusBadRequest},
	} {
		response = call(http.MethodPost, "/admin/api/alerts/test", item.body)
		if response.Code != item.code {
			t.Fatalf("test request %s: status = %d, want %d: %s", item.body, response.Code, item.code, response.Body)
		}
	}
}

func TestScheduledAlertsObserveRuntimeAndUnknownBalanceDoesNotRecover(t *testing.T) {
	ctx := context.Background()
	db, err := controlstore.Open(filepath.Join(t.TempDir(), "control.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stats, err := usage.NewControl(db, "")
	if err != nil {
		t.Fatal(err)
	}
	defer stats.Close()
	cfg := &config.Config{}
	cfg.Upstream.BaseURL, cfg.Upstream.OpenAIBaseURL, cfg.Upstream.LoginPath = "http://unused.test", "http://unused.test/v1", "/login"
	credentials := []upstream.AccountCredential{{ID: "a", Identifier: "fixture", Password: "fixture-password", Enabled: true}}
	up, err := upstream.NewWithAccounts(cfg, credentials)
	if err != nil {
		t.Fatal(err)
	}
	s := NewServiceWithConfig(up, nil, testAdminToken, "", cfg, stats)
	s.SetControlStore(db)
	s.jobs, err = jobs.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	var count atomic.Int32
	var last atomic.Pointer[alerts.Event]
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event alerts.Event
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			t.Error(err)
		}
		last.Store(&event)
		count.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()
	if _, err := alerts.Save(ctx, db, 1, alerts.Settings{Enabled: true, URL: receiver.URL, LowBalance: 100}); err != nil {
		t.Fatal(err)
	}
	cache := func(balance any, at time.Time) {
		t.Helper()
		raw, err := json.Marshal(map[string]any{"accounts": []map[string]any{{"enabled": true, "balance": balance}}})
		if err != nil {
			t.Fatal(err)
		}
		s.cacheMu.Lock()
		s.accountCache, s.accountCachedAt = raw, at
		s.cacheMu.Unlock()
	}
	observe := func(condition string, active bool) {
		t.Helper()
		before := count.Load()
		s.scheduleAlerts(ctx, time.Now())
		event := last.Load()
		if count.Load() != before+1 || event == nil || event.Condition != condition || event.Active != active {
			t.Fatalf("expected %s=%v, calls %d -> %d, got %+v", condition, active, before, count.Load(), event)
		}
	}
	cache(20, time.Now())
	observe("low_balance", true)
	for _, state := range []struct {
		balance any
		at      time.Time
	}{{nil, time.Now()}, {200, time.Now().Add(-2 * time.Hour)}, {20, time.Now()}} {
		cache(state.balance, state.at)
		s.scheduleAlerts(ctx, time.Now())
		if count.Load() != 1 {
			t.Fatal("unknown, stale or unchanged balance generated a false transition")
		}
	}
	cache(200, time.Now())
	observe("low_balance", false)
	credentials[0].Enabled = false
	if err := up.ReplaceAccounts(credentials); err != nil {
		t.Fatal(err)
	}
	observe("no_accounts", true)
	credentials[0].Enabled = true
	if err := up.ReplaceAccounts(credentials); err != nil {
		t.Fatal(err)
	}
	observe("no_accounts", false)
	var fail atomic.Bool
	fail.Store(true)
	runner := jobs.Start(ctx, s.jobs, func(context.Context, string, string, json.RawMessage) (any, error) {
		if fail.Load() {
			return nil, errors.New("fixture failure")
		}
		return nil, nil
	})
	defer runner.Close()
	finishJob := func(kind string) {
		t.Helper()
		job, _, err := s.jobs.Submit(ctx, kind, []string{"a"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			list, err := s.jobs.List(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, candidate := range list {
				if candidate.ID == job.ID && (candidate.State == "succeeded" || candidate.State == "failed") {
					return
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("fixture job did not finish")
	}
	finishJob("checkin")
	observe("job_failure", true)
	fail.Store(false)
	finishJob("probe")
	s.scheduleAlerts(ctx, time.Now())
	if count.Load() != 5 {
		t.Fatal("unrelated successful job cleared checkin failure")
	}
	finishJob("checkin")
	observe("job_failure", false)
	stats.StorageFailure()
	observe("audit_degraded", true)
	stats.RecordRequest(usage.RequestRecord{RequestID: "recovery", CreatedAt: time.Now(), Stage: "completed", Model: "m", Success: true})
	if err := stats.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	observe("audit_degraded", false)
}
