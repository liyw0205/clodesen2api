package redemption

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"clodesen2api/internal/upstream"
)

type fakeClient struct {
	calls   int
	results []upstream.RedemptionResult
}

func (f *fakeClient) RedeemCodeAll(context.Context, string) []upstream.RedemptionResult {
	f.calls++
	return f.results
}

func TestSchedulerDoesNotRepeatFailedOrEmptyRedemption(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		results []upstream.RedemptionResult
	}{
		{name: "all errors", results: []upstream.RedemptionResult{{Status: "error"}, {Status: "error"}}},
		{name: "no account results"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeClient{results: test.results}
			scheduler, err := NewScheduler(client, "04:30", "code")
			if err != nil {
				t.Fatal(err)
			}
			first := time.Date(2026, 11, 3, 4, 30, 0, 0, loc)
			scheduler.maybeTick(context.Background(), first)
			scheduler.maybeTick(context.Background(), first.Add(30*time.Second))
			if client.calls != 1 {
				t.Fatalf("redeem calls = %d, want 1", client.calls)
			}
		})
	}
}

func TestSchedulerPersistsTriggerAcrossRestart(t *testing.T) {
	client := &fakeClient{results: []upstream.RedemptionResult{{Status: "error"}}}
	statePath := filepath.Join(t.TempDir(), "redemption-scheduler.json")
	scheduler, err := NewSchedulerWithStatePath(client, "04:30", "code", statePath)
	if err != nil {
		t.Fatal(err)
	}
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	dayOne := time.Date(2026, 11, 3, 4, 30, 0, 0, loc)
	scheduler.maybeTick(context.Background(), dayOne)

	restarted, err := NewSchedulerWithStatePath(client, "04:30", "code", statePath)
	if err != nil {
		t.Fatal(err)
	}
	restarted.maybeTick(context.Background(), dayOne.Add(time.Hour))
	if client.calls != 1 {
		t.Fatalf("same-day redeem calls after restart = %d, want 1", client.calls)
	}
	restarted.maybeTick(context.Background(), dayOne.Add(24*time.Hour))
	if client.calls != 2 {
		t.Fatalf("next-day redeem calls = %d, want 2", client.calls)
	}
}

func TestSchedulerUsesChinaDateAndRunsOncePerDay(t *testing.T) {
	client := &fakeClient{}
	scheduler, err := NewScheduler(client, "04:30", " code ")
	if err != nil {
		t.Fatal(err)
	}
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	scheduler.maybeTick(context.Background(), time.Date(2026, 11, 2, 20, 0, 0, 0, loc))
	scheduler.maybeTick(context.Background(), time.Date(2026, 11, 2, 23, 0, 0, 0, loc))
	scheduler.maybeTick(context.Background(), time.Date(2026, 11, 3, 4, 29, 0, 0, loc))
	scheduler.maybeTick(context.Background(), time.Date(2026, 11, 3, 4, 30, 0, 0, loc))
	if client.calls != 2 {
		t.Fatalf("redeem calls = %d, want 2", client.calls)
	}
}
