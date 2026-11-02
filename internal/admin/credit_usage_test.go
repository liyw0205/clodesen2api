package admin

import (
	"testing"
	"time"

	"clodesen2api/internal/upstream"
)

func TestMergeCreditRecordsCalibratesLocalAndKeepsSevenDays(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	localTime := now.Add(-2 * time.Minute).Format(time.RFC3339)
	oldTime := now.Add(-8 * 24 * time.Hour).Format(time.RFC3339)
	local := []upstream.CreditUsageRecord{
		{ID: -1, AccountID: "account-one", Model: "gpt-5.6", Amount: -60, CreatedAt: localTime, Source: "local"},
		{ID: -2, AccountID: "account-one", Model: "old", Amount: -1, CreatedAt: oldTime, Source: "local"},
		{ID: -3, AccountID: "account-one", Model: "gpt-image-2", Amount: -100, CreatedAt: now.Add(-30 * time.Minute).Format(time.RFC3339), Source: "local"},
	}
	remote := []upstream.CreditUsageRecord{
		{ID: 10, AccountID: "account-one", Model: "gpt-5.6", Amount: -60, Type: "model_usage", CreatedAt: now.Add(-time.Minute).Format(time.RFC3339), Source: "upstream"},
	}

	merged := mergeCreditRecords(local, remote, now)
	if len(merged) != 2 {
		t.Fatalf("merged %d records, want calibrated remote plus unmatched local: %+v", len(merged), merged)
	}
	if merged[0].Source != "upstream" || merged[1].Source != "local" || merged[1].Model != "gpt-image-2" {
		t.Fatalf("merged records = %+v", merged)
	}
}

func TestCreditDirection(t *testing.T) {
	for _, test := range []struct {
		amount int64
		want   string
	}{
		{amount: 100, want: "increase"},
		{amount: -60, want: "decrease"},
		{amount: 0, want: "unchanged"},
	} {
		if got := creditDirection(test.amount); got != test.want {
			t.Errorf("creditDirection(%d) = %q, want %q", test.amount, got, test.want)
		}
	}
}
