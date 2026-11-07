package alerts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"clodesen2api/internal/controlstore"
)

func TestNotificationsDefaultOffDedupeRecoveryAndRetry(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.sqlite3")
	db, err := controlstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var calls atomic.Int32
	var fail atomic.Bool
	var events []Event
	var mu sync.Mutex
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var e Event
		_ = json.NewDecoder(r.Body).Decode(&e)
		if r.Header.Get("Idempotency-Key") != e.ID {
			t.Error("missing deduplication key")
		}
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
		if fail.Load() {
			w.WriteHeader(503)
		}
	}))
	defer receiver.Close()
	now := time.Now()
	observed := map[string]bool{"no_accounts": true}
	if err := Observe(ctx, db, observed, now); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("default sent a notification")
	}
	v, err := Save(ctx, db, 1, Settings{Enabled: true, URL: receiver.URL, LowBalance: 100})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Save(ctx, db, 1, v.Settings); err != controlstore.ErrConflict {
		t.Fatalf("CAS %v", err)
	}
	second, err := controlstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			store := db
			if i%2 == 0 {
				store = second
			}
			if err := Observe(ctx, store, observed, now); err != nil {
				t.Error(err)
			}
		}(i)
	}
	workers.Wait()
	if calls.Load() != 1 {
		t.Fatalf("duplicate transition: %d", calls.Load())
	}
	if err := Observe(ctx, db, map[string]bool{"no_accounts": false}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || events[1].Active {
		t.Fatal("missing recovery")
	}
	fail.Store(true)
	if err := Observe(ctx, db, observed, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	v, _ = Load(ctx, db)
	if v.LastError != "delivery_failed" || len(v.Pending) != 1 {
		t.Fatalf("failure not persisted: %+v", v)
	}
	_ = Observe(ctx, db, observed, now.Add(2*time.Minute+time.Second))
	if calls.Load() != 3 {
		t.Fatal("retry ignored backoff")
	}
	fail.Store(false)
	_ = Observe(ctx, db, observed, now.Add(4*time.Minute))
	v, _ = Load(ctx, db)
	if calls.Load() != 4 || len(v.Pending) != 0 || v.LastError != "" {
		t.Fatalf("retry did not recover: %+v", v)
	}
	if events[2].ID != events[3].ID {
		t.Fatal("retry lost stable event ID")
	}
}

func TestThresholdChangesPreserveUnrelatedPendingAlerts(t *testing.T) {
	for _, name := range []string{"threshold_only", "threshold_and_channel"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db, err := controlstore.Open(filepath.Join(t.TempDir(), "control.sqlite3"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			settings := Settings{Enabled: true, LowBalance: 100, Channels: map[string]ChannelSettings{
				"webhook": {Enabled: true, Config: map[string]string{"url": "http://127.0.0.1:1/old"}},
			}}
			view := View{Schema: 2, Revision: 1, Settings: settings,
				Conditions: map[string]bool{"low_balance": true, "no_accounts": true},
				Pending: []Event{
					{ID: "balance", Channel: "webhook", Condition: "low_balance", Active: true, At: time.Now()},
					{ID: "accounts", Channel: "webhook", Condition: "no_accounts", Active: true, At: time.Now()},
				},
			}
			if _, err := db.Save(ctx, Document, 0, view); err != nil {
				t.Fatal(err)
			}
			settings.LowBalance = 200
			if name == "threshold_and_channel" {
				settings.Channels["webhook"].Config["url"] = "http://127.0.0.1:1/new"
			}
			view, err = Save(ctx, db, 1, settings)
			if err != nil {
				t.Fatal(err)
			}
			if len(view.Pending) != 1 || view.Pending[0].Condition != "no_accounts" || view.Conditions["low_balance"] {
				t.Fatalf("threshold update lost unrelated alerts or reused stale balance: %+v", view)
			}
			if name == "threshold_only" && view.Pending[0].ID != "accounts" {
				t.Fatal("unrelated pending alert lost its stable ID")
			}
		})
	}
}
