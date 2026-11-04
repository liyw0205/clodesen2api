package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"clodesen2api/internal/config"
	"clodesen2api/internal/upstream"
)

const upstreamCacheTTL = time.Hour

func (s *Service) api(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/admin/api/accounts":
		switch r.Method {
		case http.MethodGet:
			s.listAccounts(w, r)
		case http.MethodPost:
			s.addAccount(w, r)
		default:
			w.Header().Set("Allow", "GET, POST")
			writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		}
	case r.URL.Path == "/admin/api/checkin" && r.Method == http.MethodPost:
		s.manualCheckin(w, r, "")
	case strings.HasPrefix(r.URL.Path, "/admin/api/accounts/"):
		s.accountRoute(w, r)
	case r.URL.Path == "/admin/api/settings":
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, map[string]string{"strategy": s.upstream.Strategy()})
		} else if r.Method == http.MethodPut {
			s.updateSettings(w, r)
		} else {
			w.Header().Set("Allow", "GET, PUT")
			writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		}
	case r.URL.Path == "/admin/api/config":
		s.configRoute(w, r)
	case r.URL.Path == "/admin/api/models":
		if r.Method == http.MethodGet {
			s.listModels(w, r)
		} else {
			w.Header().Set("Allow", "GET")
			writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		}
	case r.URL.Path == "/admin/api/update":
		s.updateRoute(w, r)
	case r.URL.Path == "/admin/api/credit-usage":
		if r.Method == http.MethodGet {
			s.listCreditUsage(w, r)
		} else {
			w.Header().Set("Allow", "GET")
			writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		}
	case r.URL.Path == "/admin/api/usage":
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, s.usage.Snapshot())
		} else {
			w.Header().Set("Allow", "GET")
			writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		}
	case r.URL.Path == "/admin/api/request-records":
		if r.Method == http.MethodGet {
			s.listRequestRecords(w, r)
		} else {
			w.Header().Set("Allow", "GET")
			writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		}
	case r.URL.Path == "/admin/api/redeem" && r.Method == http.MethodPost:
		s.redeem(w, r)
	case r.URL.Path == "/admin/api/redemption-history" && r.Method == http.MethodGet:
		s.listRedemptionHistory(w, r)
	default:
		writeError(w, http.StatusNotFound, "管理接口不存在")
	}
}

func (s *Service) listModels(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	force := forceRefresh(r)
	cached, cachedAt := s.modelCacheSnapshot()
	if !force && len(cached) > 0 {
		if time.Since(cachedAt) < upstreamCacheTTL {
			writeCachedJSON(w, cached, "hit")
			return
		}
		writeCachedJSON(w, cached, "stale")
		s.refreshModelsAsync()
		return
	}
	models, err := s.refreshModels(ctx, force)
	if err != nil {
		if len(cached) > 0 {
			writeCachedJSON(w, cached, "stale-on-error")
			return
		}
		writeError(w, http.StatusBadGateway, "读取上游模型列表失败")
		return
	}
	writeCachedJSON(w, models, "miss")
}

func (s *Service) refreshModels(ctx context.Context, force bool) ([]byte, error) {
	s.modelRefreshMu.Lock()
	defer s.modelRefreshMu.Unlock()
	if !force {
		if cached, cachedAt := s.modelCacheSnapshot(); len(cached) > 0 && time.Since(cachedAt) < upstreamCacheTTL {
			return cached, nil
		}
	}
	models, err := s.upstream.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(models, &payload); err != nil {
		return nil, errors.New("上游模型列表格式无效")
	}
	_, prices, pricingErr := s.upstream.ListPricing(ctx)
	localPrices := append([]upstream.ModelPricing(nil), prices...)
	priceByModel := make(map[string]upstream.ModelPricing, len(prices))
	for _, price := range prices {
		priceByModel[strings.ToLower(strings.TrimSpace(price.Model))] = price
	}
	for _, model := range payload.Data {
		id, _ := model["id"].(string)
		key := strings.ToLower(strings.TrimSpace(id))
		if price, ok := priceByModel[key]; ok {
			if price.CreditsPerRequest != nil {
				model["creditsPerRequest"] = *price.CreditsPerRequest
			}
			if len(price.TokenPricing) > 0 {
				model["billingType"] = "token"
				var tokenPricing any
				if json.Unmarshal(price.TokenPricing, &tokenPricing) == nil {
					model["tokenPricing"] = tokenPricing
				}
			} else {
				model["billingType"] = "request"
			}
			model["pricingSource"] = "upstream"
			continue
		}
		if credits, ok := fallbackCredits(id); ok {
			model["creditsPerRequest"] = credits
			model["billingType"] = "request"
			model["pricingSource"] = "fallback"
			localPrices = append(localPrices, upstream.ModelPricing{Model: id, CreditsPerRequest: &credits})
		} else if pricingErr != nil {
			model["pricingSource"] = "unavailable"
		}
	}
	s.upstream.SetCreditPricing(localPrices)
	result, _ := json.Marshal(map[string]any{"object": "list", "data": payload.Data})
	s.cacheMu.Lock()
	s.modelCache = append(s.modelCache[:0], result...)
	s.modelCachedAt = time.Now()
	result = cloneCacheBytes(s.modelCache)
	s.cacheMu.Unlock()
	s.persistCache()
	return result, nil
}

func (s *Service) refreshModelsAsync() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		_, _ = s.refreshModels(ctx, false)
	}()
}

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
		if ready && time.Since(cachedAt) < upstreamCacheTTL {
			writeCreditUsage(w, mergeCreditRecords(localRecords, remoteRecords, time.Now()), r, page, pageSize, "hit", accountBalances)
			return
		}
		if !ready || time.Since(cachedAt) >= upstreamCacheTTL {
			go s.refreshCreditUsageAsync()
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

func (s *Service) refreshCreditUsage(ctx context.Context, force bool) ([]upstream.CreditUsageRecord, error) {
	s.creditRefreshMu.Lock()
	defer s.creditRefreshMu.Unlock()
	if !force {
		if records, cachedAt, ready := s.creditCacheSnapshot(); ready && time.Since(cachedAt) < upstreamCacheTTL {
			return records, nil
		}
	}
	records, err := s.upstream.ListCreditUsage(ctx)
	if err != nil {
		cached, _, _ := s.creditCacheSnapshot()
		return cached, err
	}
	s.cacheMu.Lock()
	s.creditUsageCache = append(s.creditUsageCache[:0], records...)
	s.creditUsageCachedAt = time.Now()
	s.creditUsageReady = true
	result := append([]upstream.CreditUsageRecord(nil), s.creditUsageCache...)
	s.cacheMu.Unlock()
	s.persistCache()
	return result, nil
}

func (s *Service) refreshCreditUsageAsync() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		_, _ = s.refreshCreditUsage(ctx, false)
	}()
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
			"account": maskIdentifier(record.Identifier), "model": record.Model,
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
			label := maskIdentifier(record.Identifier)
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

func queryPositiveInt(r *http.Request, key string, fallback, min, max int) int {
	value := fallback
	if raw := strings.TrimSpace(r.URL.Query().Get(key)); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			value = parsed
		}
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
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
			stat = &creditAccountStat{AccountID: record.AccountID, Account: maskIdentifier(record.Identifier)}
			stats[key] = stat
		}
		if stat.Account == "" {
			stat.Account = maskIdentifier(record.Identifier)
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

func (s *Service) configRoute(w http.ResponseWriter, r *http.Request) {
	if s.cfg == nil {
		writeError(w, http.StatusServiceUnavailable, "配置管理不可用")
		return
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	switch r.Method {
	case http.MethodGet:
		settings := s.cfg.PublicSettings()
		if s.upstream != nil {
			settings.Auth.Strategy = s.upstream.Strategy()
			_, settings.Auth.RPM = s.upstream.RPMUsage()
		}
		writeJSON(w, http.StatusOK, settings)
	case http.MethodPut:
		var settings config.PublicSettings
		if err := decodeJSON(w, r, &settings); err != nil {
			writeError(w, http.StatusBadRequest, "配置格式无效")
			return
		}
		previous := s.cfg.PublicSettings()
		previousStrategy := ""
		if s.store != nil {
			previousStrategy = s.store.Strategy()
		}
		if err := s.cfg.ApplyPublicSettings(settings); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		strategyChanged := s.store != nil && previousStrategy != s.cfg.Auth.Strategy
		if strategyChanged {
			if err := s.store.SetStrategy(s.cfg.Auth.Strategy); err != nil {
				_ = s.cfg.ApplyPublicSettings(previous)
				writeError(w, http.StatusInternalServerError, "保存账号策略失败")
				return
			}
		}
		if err := s.cfg.Save(); err != nil {
			_ = s.cfg.ApplyPublicSettings(previous)
			if strategyChanged {
				_ = s.store.SetStrategy(previousStrategy)
			}
			writeError(w, http.StatusInternalServerError, "保存配置失败")
			return
		}
		if s.upstream != nil {
			s.upstream.ApplyRuntimeConfig(s.cfg)
		}
		writeJSON(w, http.StatusOK, map[string]any{"settings": s.cfg.PublicSettings(), "restartRequired": true})
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, http.StatusMethodNotAllowed, "方法不支持")
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

func forceRefresh(r *http.Request) bool {
	value := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("refresh")))
	return value == "1" || value == "true" || value == "yes"
}

func writeRawJSON(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (s *Service) listAccounts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	force := forceRefresh(r)
	cached, cachedAt := s.accountCacheSnapshot()
	if !force && len(cached) > 0 {
		if time.Since(cachedAt) < upstreamCacheTTL {
			s.writeAccountsJSON(w, cached, "hit")
			return
		}
		s.writeAccountsJSON(w, cached, "stale")
		s.refreshAccountsAsync()
		return
	}
	result, err := s.refreshAccounts(ctx, force)
	if err != nil {
		if len(cached) > 0 {
			s.writeAccountsJSON(w, cached, "stale-on-error")
			return
		}
		writeError(w, http.StatusBadGateway, "读取上游账号状态失败")
		return
	}
	s.writeAccountsJSON(w, result, "miss")
}

func (s *Service) writeAccountsJSON(w http.ResponseWriter, body []byte, cacheState string) {
	var payload map[string]any
	if s.upstream != nil && json.Unmarshal(body, &payload) == nil {
		usage, limit := s.upstream.RPMUsage()
		payload["strategy"] = s.upstream.Strategy()
		payload["rpmLimit"] = limit
		if accounts, ok := payload["accounts"].([]any); ok {
			for _, value := range accounts {
				account, ok := value.(map[string]any)
				if !ok {
					continue
				}
				id, _ := account["id"].(string)
				account["rpmUsed"] = usage[id]
				account["rpmLimit"] = limit
			}
		}
		if updated, err := json.Marshal(payload); err == nil {
			body = updated
		}
	}
	writeCachedJSON(w, body, cacheState)
}

func (s *Service) refreshAccounts(ctx context.Context, force bool) ([]byte, error) {
	s.accountRefreshMu.Lock()
	defer s.accountRefreshMu.Unlock()
	if !force {
		if cached, cachedAt := s.accountCacheSnapshot(); len(cached) > 0 && time.Since(cachedAt) < upstreamCacheTTL {
			return cached, nil
		}
	}
	statuses := s.upstream.AccountStatuses(ctx)
	if len(statuses) > 0 {
		healthy := 0
		for _, status := range statuses {
			// 冷却或手动禁用是有效的账号状态，不应被当成“全部刷新失败”。
			if strings.TrimSpace(status.Error) == "" || status.CooldownUntil != nil || !status.Enabled {
				healthy++
			}
		}
		if healthy == 0 {
			return nil, errors.New("上游账号状态全部读取失败")
		}
	}
	accounts := make([]map[string]any, 0, len(statuses))
	var availableCredits int64
	availableKnown := true
	for _, status := range statuses {
		accounts = append(accounts, accountStatusPayload(status))
		if status.Enabled && (status.CooldownUntil == nil || !status.CooldownUntil.After(time.Now())) {
			if status.Balance == nil {
				availableKnown = false
			} else {
				availableCredits += *status.Balance
			}
		}
	}
	creditRecords := mergeCreditRecords(s.localCreditRecords(), func() []upstream.CreditUsageRecord {
		records, _, _ := s.creditCacheSnapshot()
		return records
	}(), time.Now())
	consumedCredits, consumedKnown := creditConsumedTotal(creditRecords)
	response := map[string]any{"accounts": accounts, "strategy": s.upstream.Strategy(), "refreshedAt": time.Now(),
		"availableCredits": nil, "consumedCredits": nil}
	if availableKnown {
		response["availableCredits"] = availableCredits
	}
	if consumedKnown {
		response["consumedCredits"] = consumedCredits
	}
	result, _ := json.Marshal(response)
	s.cacheMu.Lock()
	s.accountCache = append(s.accountCache[:0], result...)
	s.accountCachedAt = time.Now()
	result = cloneCacheBytes(s.accountCache)
	s.cacheMu.Unlock()
	s.persistCache()
	return result, nil
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

func accountStatusPayload(status upstream.AccountStatus) map[string]any {
	return map[string]any{
		"id": status.ID, "identifier": maskIdentifier(status.Identifier), "enabled": status.Enabled,
		"rpmUsed": status.RPMUsed, "rpmLimit": status.RPMLimit,
		"cooldownUntil": status.CooldownUntil, "cooldownReason": status.CooldownReason, "balance": status.Balance,
		"checkedIn":         status.CheckedIn,
		"redemptionEnabled": status.RedemptionEnabled, "redemptionLimit": status.RedemptionLimit,
		"redemptionRemaining": status.RedemptionRemaining, "resetAt": status.ResetAt,
		"redemptionResetAt": status.RedemptionResetAt,
		"creditType":        status.CreditType, "validityDays": status.ValidityDays,
		"refreshedAt": status.RefreshedAt, "error": status.Error,
	}
}

func (s *Service) refreshAccountsAsync() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		_, _ = s.refreshAccounts(ctx, false)
	}()
}

func writeCachedJSON(w http.ResponseWriter, body []byte, cacheState string) {
	w.Header().Set("X-Admin-Cache", cacheState)
	writeRawJSON(w, body)
}

func (s *Service) invalidateAccountCache() {
	s.cacheMu.Lock()
	if len(s.accountCache) > 0 {
		// Keep the last known status visible while the new account pool is
		// checked in the background after a mutation.
		s.accountCachedAt = time.Now().Add(-upstreamCacheTTL)
	} else {
		s.accountCachedAt = time.Time{}
	}
	s.cacheMu.Unlock()
	s.persistCache()
}

func (s *Service) addAccount(w http.ResponseWriter, r *http.Request) {
	input, err := decodeObject(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return
	}
	identifier, err := readString(input, "identifier")
	if err != nil {
		writeError(w, http.StatusBadRequest, "账号不能为空")
		return
	}
	password, err := readString(input, "password")
	if err != nil {
		writeError(w, http.StatusBadRequest, "密码不能为空")
		return
	}
	id, err := newAccountID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法生成账号标识")
		return
	}
	s.changeMu.Lock()
	defer s.changeMu.Unlock()
	accounts := s.store.Accounts()
	accounts = append(accounts, upstream.AccountCredential{ID: id, Identifier: identifier, Password: password, Enabled: true})
	if err := s.replaceAccountsLocked(accounts); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (s *Service) accountRoute(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin/api/accounts/"), "/"), "/")
	if len(parts) == 2 && parts[0] != "" {
		s.accountAction(w, r, parts[0], parts[1])
		return
	}
	if len(parts) != 1 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "管理接口不存在")
		return
	}
	switch r.Method {
	case http.MethodPatch:
		s.updateAccount(w, r, parts[0])
	case http.MethodDelete:
		s.deleteAccount(w, parts[0])
	default:
		w.Header().Set("Allow", "PATCH, DELETE")
		writeError(w, http.StatusMethodNotAllowed, "方法不支持")
	}
}

func (s *Service) accountAction(w http.ResponseWriter, r *http.Request, id, action string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		return
	}
	if !s.accountExists(id) {
		writeError(w, http.StatusNotFound, "账号不存在")
		return
	}
	switch action {
	case "checkin":
		s.manualCheckin(w, r, id)
	case "refresh":
		s.refreshAccount(w, r, id)
	default:
		writeError(w, http.StatusNotFound, "管理接口不存在")
	}
}

func (s *Service) accountExists(id string) bool {
	for _, account := range s.store.Accounts() {
		if account.ID == id {
			return true
		}
	}
	return false
}

func (s *Service) manualCheckin(w http.ResponseWriter, r *http.Request, accountID string) {
	var result upstream.CheckinResult
	var err error
	if accountID == "" {
		result, err = s.upstream.Checkin(r.Context())
	} else {
		result, err = s.upstream.CheckinAccount(r.Context(), accountID)
	}
	s.invalidateAccountCache()
	status := "success"
	message := "签到完成"
	if result.AlreadyChecked {
		message = "今日已经签到"
	}
	if err != nil {
		status = "error"
		if result.CheckedIn || result.AlreadyChecked || result.Reward > 0 {
			status = "partial"
		}
		message = err.Error()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": status == "success", "status": status, "message": message,
		"accountId": accountID, "checkedIn": result.CheckedIn,
		"alreadyChecked": result.AlreadyChecked, "reward": result.Reward,
		"balance": result.Balance, "date": result.Date,
	})
}

func (s *Service) refreshAccount(w http.ResponseWriter, r *http.Request, id string) {
	status, err := s.upstream.RefreshAccountStatus(r.Context(), id)
	s.invalidateAccountCache()
	if err != nil && status.Error == "" {
		status.Error = err.Error()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": err == nil && status.Error == "", "account": accountStatusPayload(status),
	})
}

func (s *Service) updateAccount(w http.ResponseWriter, r *http.Request, id string) {
	input, err := decodeObject(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return
	}
	s.changeMu.Lock()
	defer s.changeMu.Unlock()
	accounts := s.store.Accounts()
	found := false
	for i := range accounts {
		if accounts[i].ID != id {
			continue
		}
		found = true
		if raw, ok := input["identifier"]; ok {
			if err := json.Unmarshal(raw, &accounts[i].Identifier); err != nil {
				writeError(w, http.StatusBadRequest, "账号格式无效")
				return
			}
			accounts[i].Identifier = strings.TrimSpace(accounts[i].Identifier)
		}
		if raw, ok := input["password"]; ok {
			var password string
			if err := json.Unmarshal(raw, &password); err != nil {
				writeError(w, http.StatusBadRequest, "密码格式无效")
				return
			}
			if password != "" {
				accounts[i].Password = password
			}
		}
		if raw, ok := input["enabled"]; ok {
			if err := json.Unmarshal(raw, &accounts[i].Enabled); err != nil {
				writeError(w, http.StatusBadRequest, "启用状态格式无效")
				return
			}
		}
		break
	}
	if !found {
		writeError(w, http.StatusNotFound, "账号不存在")
		return
	}
	if err := s.replaceAccountsLocked(accounts); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) deleteAccount(w http.ResponseWriter, id string) {
	s.changeMu.Lock()
	defer s.changeMu.Unlock()
	accounts := s.store.Accounts()
	next := make([]upstream.AccountCredential, 0, len(accounts))
	found := false
	for _, account := range accounts {
		if account.ID == id {
			found = true
			continue
		}
		next = append(next, account)
	}
	if !found {
		writeError(w, http.StatusNotFound, "账号不存在")
		return
	}
	if err := s.replaceAccountsLocked(next); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) replaceAccountsLocked(next []upstream.AccountCredential) error {
	if err := validateCredentials(next); err != nil {
		return err
	}
	old := s.store.Accounts()
	if err := s.upstream.ReplaceAccounts(next); err != nil {
		return err
	}
	if err := s.store.Replace(next); err != nil {
		_ = s.upstream.ReplaceAccounts(old)
		return err
	}
	s.invalidateAccountCache()
	return nil
}

func (s *Service) updateSettings(w http.ResponseWriter, r *http.Request) {
	input, err := decodeObject(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return
	}
	strategy, err := readString(input, "strategy")
	if err != nil {
		writeError(w, http.StatusBadRequest, "请选择账号策略")
		return
	}
	s.changeMu.Lock()
	defer s.changeMu.Unlock()
	old := s.upstream.Strategy()
	if err := s.upstream.SetStrategy(strategy); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.SetStrategy(strategy); err != nil {
		_ = s.upstream.SetStrategy(old)
		writeError(w, http.StatusInternalServerError, "保存调度策略失败")
		return
	}
	s.invalidateAccountCache()
	writeJSON(w, http.StatusOK, map[string]string{"strategy": s.upstream.Strategy()})
}

func (s *Service) redeem(w http.ResponseWriter, r *http.Request) {
	input, err := decodeObject(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return
	}
	code, err := readString(input, "code")
	if err != nil || len(code) > 64 {
		writeError(w, http.StatusBadRequest, "兑换码不能为空且不能超过 64 个字符")
		return
	}
	var requested []string
	if raw, ok := input["accountIds"]; ok {
		if err := json.Unmarshal(raw, &requested); err != nil {
			writeError(w, http.StatusBadRequest, "账号选择格式无效")
			return
		}
	}
	selected := make(map[string]struct{}, len(requested))
	for _, id := range requested {
		selected[id] = struct{}{}
	}
	var accounts []upstream.AccountCredential
	for _, account := range s.store.Accounts() {
		if !account.Enabled {
			continue
		}
		if len(selected) > 0 {
			if _, ok := selected[account.ID]; !ok {
				continue
			}
		}
		accounts = append(accounts, account)
	}
	if len(accounts) == 0 {
		writeError(w, http.StatusBadRequest, "没有可兑换的启用账号")
		return
	}
	results := make([]map[string]any, 0, len(accounts))
	var successCount int
	var totalCredits int64
	for _, account := range accounts {
		outcome := s.upstream.RedeemCode(r.Context(), account.ID, code)
		results = append(results, map[string]any{
			"account": maskIdentifier(outcome.Identifier), "status": outcome.Status,
			"message": outcome.Message, "credits": outcome.Credits,
		})
		if outcome.Status == "success" {
			successCount++
			if outcome.Credits != nil {
				totalCredits += *outcome.Credits
			}
		}
	}
	s.invalidateAccountCache()
	writeJSON(w, http.StatusOK, map[string]any{
		"results": results, "successCount": successCount, "totalCount": len(results), "totalCredits": totalCredits,
	})
}

func (s *Service) listRedemptionHistory(w http.ResponseWriter, r *http.Request) {
	limit := queryPositiveInt(r, "limit", 50, 1, 100)
	records := s.redemptionHistorySnapshot()
	if records == nil {
		records = []RedemptionHistoryRecord{}
	}
	if len(records) > limit {
		records = records[len(records)-limit:]
	}
	for left, right := 0, len(records)-1; left < right; left, right = left+1, right-1 {
		records[left], records[right] = records[right], records[left]
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": records})
}

func decodeObject(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, error) {
	var input map[string]json.RawMessage
	if err := decodeJSON(w, r, &input); err != nil {
		return nil, err
	}
	if input == nil {
		return nil, errors.New("JSON object required")
	}
	return input, nil
}

func readString(input map[string]json.RawMessage, key string) (string, error) {
	var value string
	if err := json.Unmarshal(input[key], &value); err != nil || strings.TrimSpace(value) == "" {
		return "", errors.New("required string field is missing")
	}
	return strings.TrimSpace(value), nil
}

func maskIdentifier(identifier string) string {
	parts := strings.SplitN(identifier, "@", 2)
	if len(parts) == 2 {
		local := parts[0]
		if len(local) < 3 {
			local = local[:1] + "***"
		} else {
			local = local[:1] + "***" + local[len(local)-1:]
		}
		return local + "@" + parts[1]
	}
	if len(identifier) <= 2 {
		return "**"
	}
	return identifier[:2] + "***"
}
