package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"clodesen2api/internal/upstream"
)

func (s *Service) listCreditUsage(w http.ResponseWriter, r *http.Request) {
	page := queryPositiveInt(r, "page", 1, 1, 1000000)
	pageSize := queryPositiveInt(r, "pageSize", 20, 1, 100)
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	force := forceRefresh(r)
	accountBalances := s.cachedAccountBalances()
	remoteRecords, cachedAt, ready := s.creditCacheSnapshot()
	localRecords := s.localCreditRecords()
	if !force && (ready || len(localRecords) > 0) {
		if ready && time.Since(cachedAt) < s.cacheTTL("credits") {
			writeCreditUsage(w, mergeCreditRecords(localRecords, remoteRecords, time.Now()), r, page, pageSize, "hit", accountBalances)
			return
		}
		if !ready || time.Since(cachedAt) >= s.cacheTTL("credits") {
			s.refreshCreditUsageAsync()
		}
		writeCreditUsage(w, mergeCreditRecords(localRecords, remoteRecords, time.Now()), r, page, pageSize, "stale", accountBalances)
		return
	}
	remoteRecords, err := s.refreshCreditUsage(ctx, force)
	if err != nil {
		if ready || len(localRecords) > 0 {
			writeCreditUsage(w, mergeCreditRecords(localRecords, remoteRecords, time.Now()), r, page, pageSize, "stale-on-error", accountBalances)
			return
		}
		writeError(w, http.StatusBadGateway, "读取积分使用记录失败")
		return
	}
	writeCreditUsage(w, mergeCreditRecords(localRecords, remoteRecords, time.Now()), r, page, pageSize, "miss", accountBalances)
}

func (s *Service) fetchCreditUsage(ctx context.Context, force bool) ([]upstream.CreditUsageRecord, error) {
	s.creditRefreshMu.Lock()
	defer s.creditRefreshMu.Unlock()
	if !force {
		if records, cachedAt, ready := s.creditCacheSnapshot(); ready && time.Since(cachedAt) < s.cacheTTL("credits") {
			return records, nil
		}
	}
	records, partial, err := s.upstream.ListCreditUsageWithFailures(ctx)
	s.partialRefresh("credits", partial)
	if err != nil {
		cached, _, _ := s.creditCacheSnapshot()
		return cached, err
	}
	s.cacheMu.Lock()
	if len(partial) > 0 {
		failed := make(map[string]bool, len(partial))
		for _, id := range partial {
			failed[id] = true
		}
		for _, record := range s.creditUsageCache {
			if failed[record.AccountID] {
				records = append(records, record)
			}
		}
	}
	s.creditUsageCache = append(s.creditUsageCache[:0], records...)
	s.creditUsageCachedAt = time.Now()
	s.creditUsageReady = true
	result := append([]upstream.CreditUsageRecord(nil), s.creditUsageCache...)
	s.cacheMu.Unlock()
	s.persistCache()
	return result, nil
}

func (s *Service) refreshCreditUsageAsync() {
	s.refreshes.start("credits", func(ctx context.Context) (any, error) { return s.fetchCreditUsage(ctx, false) })
}

func (s *Service) cachedAccountBalances() map[string]int64 {
	result := make(map[string]int64)
	cached, _ := s.accountCacheSnapshot()
	if len(cached) == 0 {
		return result
	}
	var payload struct {
		Accounts []struct {
			ID      string `json:"id"`
			Balance *int64 `json:"balance"`
		} `json:"accounts"`
	}
	if json.Unmarshal(cached, &payload) != nil {
		return result
	}
	for _, account := range payload.Accounts {
		if strings.TrimSpace(account.ID) != "" && account.Balance != nil {
			result[account.ID] = *account.Balance
		}
	}
	return result
}

func writeCreditUsage(w http.ResponseWriter, records []upstream.CreditUsageRecord, r *http.Request, page, pageSize int, cacheState string, balanceMaps ...map[string]int64) {
	var accountBalances map[string]int64
	if len(balanceMaps) > 0 {
		accountBalances = balanceMaps[0]
	}
	availableModels, availableAccounts := creditFilterOptions(records)
	filtered := filterCreditRecords(records, r)
	modelStats := creditModelStats(filtered)
	accountStats := creditAccountStats(filtered)
	pageCount := (len(filtered) + pageSize - 1) / pageSize
	if pageCount == 0 {
		pageCount = 1
	}
	if page > pageCount {
		page = pageCount
	}
	start := (page - 1) * pageSize
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + pageSize
	if end > len(filtered) {
		end = len(filtered)
	}
	pageRecords := filtered[start:end]
	items := make([]map[string]any, 0, len(pageRecords))
	for _, record := range pageRecords {
		item := map[string]any{
			"id": record.ID, "accountId": record.AccountID,
			"account": accountIdentifier(record.Identifier), "model": record.Model,
			"amount": record.Amount, "direction": creditDirection(record.Amount), "balanceAfter": nil,
			"type": record.Type, "description": record.Description,
			"status": record.Status, "createdAt": record.CreatedAt, "source": record.Source,
		}
		if record.BalanceKnown {
			item["balanceAfter"] = record.BalanceAfter
		} else if balance, ok := accountBalances[record.AccountID]; ok {
			// Local real-time events do not know the exact post-transaction
			// balance; show the latest cached account balance instead of `--`.
			item["balanceAfter"] = balance
		}
		items = append(items, item)
	}
	w.Header().Set("X-Admin-Cache", cacheState)
	writeJSON(w, http.StatusOK, map[string]any{
		"records": items, "modelStats": modelStats, "accountStats": accountStats,
		"availableModels": availableModels, "availableAccounts": availableAccounts,
		"total": len(filtered), "page": page, "pageSize": pageSize, "totalPages": pageCount,
		"hasPrevious": page > 1, "hasNext": page < pageCount,
		"totals": creditTotals(filtered), "refreshedAt": time.Now(),
	})
}

func creditFilterOptions(records []upstream.CreditUsageRecord) ([]string, []map[string]string) {
	models := make(map[string]struct{})
	accounts := make(map[string]string)
	for _, record := range records {
		if model := strings.TrimSpace(record.Model); model != "" {
			models[model] = struct{}{}
		}
		id := strings.TrimSpace(record.AccountID)
		if id == "" {
			id = strings.TrimSpace(record.Identifier)
		}
		if id != "" {
			label := accountIdentifier(record.Identifier)
			if label == "" {
				label = id
			}
			accounts[id] = label
		}
	}
	modelOptions := make([]string, 0, len(models))
	for model := range models {
		modelOptions = append(modelOptions, model)
	}
	sort.Strings(modelOptions)
	accountIDs := make([]string, 0, len(accounts))
	for id := range accounts {
		accountIDs = append(accountIDs, id)
	}
	sort.Strings(accountIDs)
	accountOptions := make([]map[string]string, 0, len(accountIDs))
	for _, id := range accountIDs {
		accountOptions = append(accountOptions, map[string]string{"id": id, "label": accounts[id]})
	}
	return modelOptions, accountOptions
}

func filterCreditRecords(records []upstream.CreditUsageRecord, r *http.Request) []upstream.CreditUsageRecord {
	model := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("model")))
	accountID := strings.TrimSpace(r.URL.Query().Get("accountId"))
	direction := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("direction")))
	if model == "" && accountID == "" && direction == "" {
		return records
	}
	filtered := make([]upstream.CreditUsageRecord, 0, len(records))
	for _, record := range records {
		if model != "" && strings.ToLower(strings.TrimSpace(record.Model)) != model {
			continue
		}
		if accountID != "" && record.AccountID != accountID {
			continue
		}
		switch direction {
		case "increase":
			if record.Amount <= 0 {
				continue
			}
		case "decrease":
			if record.Amount >= 0 {
				continue
			}
		case "unchanged":
			if record.Amount != 0 {
				continue
			}
		}
		filtered = append(filtered, record)
	}
	return filtered
}
