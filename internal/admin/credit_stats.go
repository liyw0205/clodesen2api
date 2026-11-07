package admin

import (
	"sort"
	"strings"

	"clodesen2api/internal/upstream"
)

type creditAccountStat struct {
	AccountID       string `json:"accountId"`
	Account         string `json:"account"`
	Requests        int    `json:"requests"`
	Refunds         int    `json:"refunds"`
	CreditsUsed     int64  `json:"creditsUsed"`
	CreditsRefunded int64  `json:"creditsRefunded"`
	NetCredits      int64  `json:"netCredits"`
	LastUsedAt      string `json:"lastUsedAt,omitempty"`
}

func isModelCreditRecord(record upstream.CreditUsageRecord) bool {
	return record.Type == "model_usage" || record.Type == "local_request" || record.Type == "refund"
}

func creditAccountStats(records []upstream.CreditUsageRecord) []creditAccountStat {
	stats := make(map[string]*creditAccountStat)
	for _, record := range records {
		if !isModelCreditRecord(record) {
			continue
		}
		key := record.AccountID
		if key == "" {
			key = record.Identifier
		}
		stat := stats[key]
		if stat == nil {
			stat = &creditAccountStat{AccountID: record.AccountID, Account: accountIdentifier(record.Identifier)}
			stats[key] = stat
		}
		if stat.Account == "" {
			stat.Account = accountIdentifier(record.Identifier)
		}
		switch record.Type {
		case "refund":
			stat.Refunds++
			if record.Amount > 0 {
				stat.CreditsRefunded += record.Amount
			}
		case "model_usage", "local_request":
			stat.Requests++
			if record.Amount < 0 {
				stat.CreditsUsed += -record.Amount
			}
		}
		stat.NetCredits = stat.CreditsUsed - stat.CreditsRefunded
		if latest, ok := parseCreditTime(stat.LastUsedAt); !ok || func() bool {
			current, valid := parseCreditTime(record.CreatedAt)
			return valid && current.After(latest)
		}() {
			stat.LastUsedAt = record.CreatedAt
		}
	}
	result := make([]creditAccountStat, 0, len(stats))
	for _, stat := range stats {
		result = append(result, *stat)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].NetCredits != result[j].NetCredits {
			return result[i].NetCredits > result[j].NetCredits
		}
		return result[i].Account < result[j].Account
	})
	return result
}

func creditTotals(records []upstream.CreditUsageRecord) map[string]int64 {
	var totals modelCreditStat
	for _, stat := range creditModelStats(records) {
		totals.Requests += stat.Requests
		totals.Refunds += stat.Refunds
		totals.CreditsUsed += stat.CreditsUsed
		totals.CreditsRefunded += stat.CreditsRefunded
	}
	totals.NetCredits = totals.CreditsUsed - totals.CreditsRefunded
	return map[string]int64{
		"requests": int64(totals.Requests), "refunds": int64(totals.Refunds),
		"creditsUsed": totals.CreditsUsed, "creditsRefunded": totals.CreditsRefunded,
		"netCredits": totals.NetCredits,
	}
}

func creditDirection(amount int64) string {
	switch {
	case amount > 0:
		return "increase"
	case amount < 0:
		return "decrease"
	default:
		return "unchanged"
	}
}

type modelCreditStat struct {
	Model           string `json:"model"`
	Requests        int    `json:"requests"`
	Refunds         int    `json:"refunds"`
	CreditsUsed     int64  `json:"creditsUsed"`
	CreditsRefunded int64  `json:"creditsRefunded"`
	NetCredits      int64  `json:"netCredits"`
	LastUsedAt      string `json:"lastUsedAt,omitempty"`
}

func creditModelStats(records []upstream.CreditUsageRecord) []modelCreditStat {
	stats := make(map[string]*modelCreditStat)
	for _, record := range records {
		if !isModelCreditRecord(record) {
			continue
		}
		model := strings.TrimSpace(record.Model)
		if model == "" {
			model = "未标注模型"
		}
		stat := stats[model]
		if stat == nil {
			stat = &modelCreditStat{Model: model}
			stats[model] = stat
		}
		switch record.Type {
		case "refund":
			stat.Refunds++
			if record.Amount > 0 {
				stat.CreditsRefunded += record.Amount
			}
		case "model_usage", "local_request":
			stat.Requests++
			if record.Amount < 0 {
				stat.CreditsUsed += -record.Amount
			}
		}
		stat.NetCredits = stat.CreditsUsed - stat.CreditsRefunded
		if latest, ok := parseCreditTime(stat.LastUsedAt); !ok || func() bool {
			current, valid := parseCreditTime(record.CreatedAt)
			return valid && current.After(latest)
		}() {
			stat.LastUsedAt = record.CreatedAt
		}
	}
	result := make([]modelCreditStat, 0, len(stats))
	for _, stat := range stats {
		result = append(result, *stat)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].NetCredits != result[j].NetCredits {
			return result[i].NetCredits > result[j].NetCredits
		}
		return result[i].Model < result[j].Model
	})
	return result
}

func creditConsumedTotal(records []upstream.CreditUsageRecord) (int64, bool) {
	if len(records) == 0 {
		return 0, false
	}
	totals := creditTotals(records)
	consumed := totals["netCredits"]
	if consumed < 0 {
		consumed = 0
	}
	return consumed, true
}
