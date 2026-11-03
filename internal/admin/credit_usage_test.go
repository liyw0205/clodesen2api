package admin

import (
	"encoding/json"
	"net/http/httptest"
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

func TestWriteCreditUsagePaginatesFiltersAndAggregatesAccounts(t *testing.T) {
	records := []upstream.CreditUsageRecord{
		{ID: 1, AccountID: "one", Identifier: "one@example.test", Model: "gpt-5.6", Amount: -60, Type: "model_usage", CreatedAt: "2026-11-02T12:00:00Z", BalanceKnown: true, BalanceAfter: 940},
		{ID: 2, AccountID: "one", Identifier: "one@example.test", Model: "gpt-5.6", Amount: 60, Type: "refund", CreatedAt: "2026-11-02T12:01:00Z", BalanceKnown: true, BalanceAfter: 1000},
		{ID: 3, AccountID: "two", Identifier: "two@example.test", Amount: 500, Type: "redeem_code", CreatedAt: "2026-11-02T12:02:00Z", BalanceKnown: true, BalanceAfter: 500},
	}
	request := httptest.NewRequest("GET", "/admin/api/credit-usage?page=1&pageSize=1&direction=increase", nil)
	response := httptest.NewRecorder()
	writeCreditUsage(response, records, request, 1, 1, "hit")
	var payload struct {
		Records           []map[string]any    `json:"records"`
		Total             int                 `json:"total"`
		Page              int                 `json:"page"`
		PageSize          int                 `json:"pageSize"`
		TotalPages        int                 `json:"totalPages"`
		AccountStats      []creditAccountStat `json:"accountStats"`
		AvailableModels   []string            `json:"availableModels"`
		AvailableAccounts []map[string]string `json:"availableAccounts"`
		Totals            map[string]int64    `json:"totals"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Total != 2 || payload.Page != 1 || payload.PageSize != 1 || payload.TotalPages != 2 || len(payload.Records) != 1 {
		t.Fatalf("pagination payload = %+v", payload)
	}
	if len(payload.AccountStats) != 1 || payload.AccountStats[0].AccountID != "one" || payload.AccountStats[0].CreditsRefunded != 60 {
		t.Fatalf("account stats = %+v", payload.AccountStats)
	}
	if len(payload.AvailableModels) != 1 || payload.AvailableModels[0] != "gpt-5.6" || len(payload.AvailableAccounts) != 2 {
		t.Fatalf("filter options = models=%v accounts=%v", payload.AvailableModels, payload.AvailableAccounts)
	}
	if payload.Totals["netCredits"] != -60 || payload.Totals["creditsUsed"] != 0 {
		t.Fatalf("totals = %+v", payload.Totals)
	}
}

func TestWriteCreditUsageUsesCachedBalanceForLocalRecord(t *testing.T) {
	records := []upstream.CreditUsageRecord{{
		ID: -1, AccountID: "one", Identifier: "one@example.test", Model: "gpt-5.6",
		Amount: -60, Type: "local_request", CreatedAt: "2026-11-02T12:00:00Z", Source: "local",
	}}
	request := httptest.NewRequest("GET", "/admin/api/credit-usage", nil)
	response := httptest.NewRecorder()
	writeCreditUsage(response, records, request, 1, 20, "hit", map[string]int64{"one": 940})
	var payload struct {
		Records []map[string]any `json:"records"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Records) != 1 || payload.Records[0]["balanceAfter"] != float64(940) {
		t.Fatalf("local balance = %+v, want 940", payload.Records)
	}
}
