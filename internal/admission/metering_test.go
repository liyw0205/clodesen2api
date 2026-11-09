package admission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"clodesen2api/internal/controlstore"
)

func TestResponsesUsageAcceptsOnlyTerminalIntegerCounts(t *testing.T) {
	for _, test := range []struct {
		body string
		want Amount
	}{
		{`{"type":"response.completed","response":{"usage":{"total_tokens":20,"credits":3}}}`, Amount{Tokens: 20, Credits: 3, TokensKnown: true, CreditsKnown: true}},
		{`{"type":"response.incomplete","response":{"usage":{"total_tokens":0}}}`, Amount{TokensKnown: true}},
		{`{"type":"response.failed","response":{"usage":{"credits":3}}}`, Amount{Credits: 3, CreditsKnown: true}},
		{`{"type":"response.created","response":{"usage":{"total_tokens":20,"credits":3}}}`, Amount{}},
		{`{"type":"response.completed","response":{"usage":{"total_tokens":-1,"credits":-1}}}`, Amount{}},
		{`{"type":"response.completed","response":{"usage":{"total_tokens":2.5,"credits":3}}}`, Amount{}},
		{`{"type":"response.completed","response":{"usage":null}}`, Amount{}},
	} {
		var got Amount
		ReportUsage(WithMeter(context.Background(), func(amount Amount) { got = amount }), []byte(test.body))
		if got != test.want {
			t.Fatalf("%s: got %+v, want %+v", test.body, got, test.want)
		}
	}
}

func TestBackupAdmissionValidationChecksUnprunedState(t *testing.T) {
	for _, field := range []string{"settings", "reservation-state", "expired-negative-balance"} {
		t.Run(field, func(t *testing.T) {
			v := state{Settings: Defaults(), Revision: 1}
			switch field {
			case "settings":
				v.Settings = Settings{}
			case "reservation-state":
				v.Reservations = map[string]Reservation{"id": {ID: "id", State: "invalid"}}
			case "expired-negative-balance":
				v.Balances = map[string]Balance{"old": {ReservedTokens: -1}}
			}
			raw, _ := json.Marshal(v)
			if err := ValidateDocument(raw); err == nil {
				t.Fatalf("accepted %s", field)
			}
		})
	}
}

func TestMeteringConcurrentStoresCannotOversubscribe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sqlite3")
	one, err := controlstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	two, err := controlstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer two.Close()
	first, err := Open(one)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(two)
	if err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := first
			if i%2 != 0 {
				s = second
			}
			p := Policy{DailyTokens: 500}
			l, err := s.Acquire(context.Background(), "shared-key", p, 1)
			if err != nil {
				t.Error(err)
				return
			}
			defer l.Release()
			err = l.AdmitMetered(context.Background(), "model", p, nil, nil, Meter{Source: "upstream-usage-v1", Tokens: 100}, fmt.Sprintf("req-%d", i))
			if err == nil {
				accepted.Add(1)
				return
			}
			var denied *Error
			if !errors.As(err, &denied) || denied.Code != "usage_quota_exceeded" {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if accepted.Load() != 5 {
		t.Fatalf("accepted %d, want 5", accepted.Load())
	}
	view, err := second.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range view.Balances {
		if b.UsedTokens != 0 || b.ReservedTokens != 500 {
			t.Fatalf("lost reservations: %+v", b)
		}
	}
}

func TestMeteringOverrunBlocksEvenPartialSettlement(t *testing.T) {
	for _, mode := range []string{"tokens", "credits", "partial"} {
		t.Run(mode, func(t *testing.T) {
			db, err := controlstore.Open(filepath.Join(t.TempDir(), "control.sqlite3"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			s, err := Open(db)
			if err != nil {
				t.Fatal(err)
			}
			p := Policy{DailyTokens: 1000, DailyCredits: 1000}
			actual := Amount{Tokens: 101, TokensKnown: true}
			switch mode {
			case "tokens":
				p.DailyCredits = 0
			case "credits":
				p.DailyTokens = 0
				actual = Amount{Credits: 101, CreditsKnown: true}
			}
			ctx := context.Background()
			meter := Meter{Source: "upstream-usage-v1", Tokens: 100, Credits: 100}
			l, err := s.Acquire(ctx, "key", p, 1)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Release()
			if err := l.AdmitMetered(ctx, "model", p, nil, nil, meter, "overrun"); err != nil {
				t.Fatal(err)
			}
			if err := l.Settle(ctx, actual); err != nil {
				t.Fatal(err)
			}
			view, err := s.Snapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			r := view.Reservations["overrun"]
			want := "overrun"
			if mode == "partial" {
				want = "unknown"
			}
			if r.State != want {
				t.Fatalf("state %+v", r)
			}
			next, err := s.Acquire(ctx, "key", p, 1)
			if err != nil {
				t.Fatal(err)
			}
			defer next.Release()
			err = next.AdmitMetered(ctx, "model", p, nil, nil, meter, "next")
			var denied *Error
			if !errors.As(err, &denied) || denied.Code != "metering_contract_exceeded" {
				t.Fatalf("overrun admitted: %v", err)
			}
			if mode == "partial" {
				if err := s.Reconcile(ctx, r.ID, r.Version, Amount{Tokens: 102, TokensKnown: true, Credits: 1, CreditsKnown: true}); err == nil {
					t.Fatal("changed verified amount")
				}
				if err := s.Reconcile(ctx, r.ID, r.Version, Amount{Tokens: 101, TokensKnown: true, Credits: 1, CreditsKnown: true}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestMeteringReservationUnknownReconcileAndIdempotence(t *testing.T) {
	db, err := controlstore.Open(filepath.Join(t.TempDir(), "control.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	policy := Policy{DailyTokens: 100, MonthlyTokens: 200, DailyCredits: 20}
	meter := Meter{Source: "upstream-usage-v1", Tokens: 80, Credits: 10}
	first, err := s.Acquire(ctx, "key", policy, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	if err := first.AdmitMetered(ctx, "model", policy, nil, nil, meter, "req-one"); err != nil {
		t.Fatal(err)
	}
	second, err := s.Acquire(ctx, "key", policy, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
	if err := second.AdmitMetered(ctx, "model", policy, nil, nil, meter, "req-two"); err == nil {
		t.Fatal("reserved tokens bypassed")
	}
	if err := first.Settle(ctx, Amount{Tokens: 20, TokensKnown: true}); err != nil {
		t.Fatal(err)
	}
	view, _ := s.Snapshot(ctx)
	r := view.Reservations["req-one"]
	if r.State != "unknown" {
		t.Fatalf("unknown credit discarded: %+v", r)
	}
	for _, b := range view.Balances {
		if b.UsedTokens != 20 || b.ReservedTokens != 0 || b.ReservedCredits != 10 {
			t.Fatalf("bad partial settlement: %+v", b)
		}
	}
	if err := s.Reconcile(ctx, r.ID, r.Version, Amount{Tokens: 20, TokensKnown: true, Credits: 5, CreditsKnown: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconcile(ctx, r.ID, r.Version, Amount{Tokens: 20, TokensKnown: true, Credits: 5, CreditsKnown: true}); err == nil {
		t.Fatal("stale reconciliation accepted")
	}
	if err := first.Settle(ctx, Amount{Tokens: 20, TokensKnown: true, Credits: 5, CreditsKnown: true}); err != nil {
		t.Fatal(err)
	}
	if err := second.AdmitMetered(ctx, "model", policy, nil, nil, meter, "req-two"); err != nil {
		t.Fatal(err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	view, _ = restarted.Snapshot(ctx)
	if view.Reservations["req-two"].State != "unknown" {
		t.Fatal("released unknown usage lost")
	}
	for _, b := range view.Balances {
		if b.UsedTokens != 20 || b.ReservedTokens != 80 || b.UsedCredits != 5 || b.ReservedCredits != 10 {
			t.Fatalf("restart lost budget: %+v", b)
		}
	}
}

func TestNoMeterDoesNotConsumeRequests(t *testing.T) {
	db, err := controlstore.Open(filepath.Join(t.TempDir(), "control.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, _ := Open(db)
	l, err := s.Acquire(context.Background(), "key", Policy{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	err = l.AdmitMetered(context.Background(), "model", Policy{DailyTokens: 1}, nil, nil, Meter{}, "id")
	var denied *Error
	if !errors.As(err, &denied) || denied.Code != "quota_metering_unavailable" {
		t.Fatalf("%v", err)
	}
	view, _ := s.Snapshot(context.Background())
	if len(view.Counters) != 0 || len(view.Reservations) != 0 {
		t.Fatal("rejection consumed quota")
	}
}
