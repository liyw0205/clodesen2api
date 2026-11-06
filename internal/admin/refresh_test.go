package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"clodesen2api/internal/config"
	"clodesen2api/internal/upstream"
)

func TestPartialCreditRefreshKeepsFailedAccountSnapshot(t *testing.T) {
	var fail atomic.Bool
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			var input struct {
				Identifier string `json:"identifier"`
			}
			_ = json.NewDecoder(r.Body).Decode(&input)
			http.SetCookie(w, &http.Cookie{Name: "account", Value: input.Identifier, Path: "/"})
			w.Write([]byte(`{}`))
			return
		}
		cookie, _ := r.Cookie("account")
		if cookie == nil {
			w.WriteHeader(401)
			return
		}
		if cookie.Value == "two" && fail.Load() {
			w.WriteHeader(500)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"account": map[string]any{"transactions": []map[string]any{{"id": 1, "model": "model", "type": "model_usage", "amount": -1, "created_at": time.Now().UTC().Format(time.RFC3339)}}}})
	}))
	defer remote.Close()
	cfg := &config.Config{}
	cfg.Upstream.BaseURL, cfg.Upstream.LoginPath = remote.URL, "/login"
	up, err := upstream.NewWithAccounts(cfg, []upstream.AccountCredential{{ID: "one", Identifier: "one", Password: "p", Enabled: true}, {ID: "two", Identifier: "two", Password: "p", Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{upstream: up}
	records, err := s.fetchCreditUsage(context.Background(), true)
	if err != nil || len(records) != 2 {
		t.Fatalf("initial %+v %v", records, err)
	}
	fail.Store(true)
	records, err = s.fetchCreditUsage(context.Background(), true)
	if err != nil || len(records) != 2 {
		t.Fatalf("partial erased records: %+v %v", records, err)
	}
	status := s.cacheStatus()["credits"].(map[string]any)
	if status["cacheState"] != "partial" || len(status["partialFailures"].([]string)) != 1 {
		t.Fatalf("partial not exposed: %+v", status)
	}
}

func TestRefreshCoalescesAndWaiterCancellation(t *testing.T) {
	var group refreshGroup
	var calls atomic.Int32
	start := make(chan struct{})
	release := make(chan struct{})
	fetch := func(context.Context) (any, error) { calls.Add(1); close(start); <-release; return "snapshot", nil }
	first := group.start("models", fetch)
	<-start
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := group.do(ctx, "models", fetch); err == nil {
		t.Fatal("cancelled waiter did not return")
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if call := group.start("models", fetch); call != first {
				t.Error("not shared")
			}
		}()
	}
	wg.Wait()
	close(release)
	<-first.done
	if calls.Load() != 1 || first.value != "snapshot" {
		t.Fatalf("calls %d result %v", calls.Load(), first.value)
	}
}
