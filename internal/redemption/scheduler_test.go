package redemption

import (
	"context"
	"testing"
	"time"

	"clodesen2api/internal/upstream"
)

type fakeClient struct {
	calls int
}

func (f *fakeClient) RedeemCodeAll(context.Context, string) []upstream.RedemptionResult {
	f.calls++
	return []upstream.RedemptionResult{{Status: "success"}}
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
