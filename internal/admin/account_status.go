package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"clodesen2api/internal/upstream"
)

func (s *Service) listAccounts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	force := forceRefresh(r)
	cached, cachedAt := s.accountCacheSnapshot()
	if !force && len(cached) > 0 {
		if time.Since(cachedAt) < s.cacheTTL("accounts") {
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
				delete(account, "weight")
			}
		}
		if updated, err := json.Marshal(payload); err == nil {
			body = updated
		}
	}
	writeCachedJSON(w, body, cacheState)
}

func (s *Service) fetchAccounts(ctx context.Context, force bool) ([]byte, error) {
	s.accountRefreshMu.Lock()
	defer s.accountRefreshMu.Unlock()
	if !force {
		if cached, cachedAt := s.accountCacheSnapshot(); len(cached) > 0 && time.Since(cachedAt) < s.cacheTTL("accounts") {
			return cached, nil
		}
	}
	statuses := s.upstream.AccountStatuses(ctx)
	partialFailures := []string{}
	for _, status := range statuses {
		if status.Enabled && status.CooldownUntil == nil && strings.TrimSpace(status.Error) != "" {
			partialFailures = append(partialFailures, status.ID)
		}
	}
	s.partialRefresh("accounts", partialFailures)
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

func accountStatusPayload(status upstream.AccountStatus) map[string]any {
	return map[string]any{
		"id": status.ID, "identifier": accountIdentifier(status.Identifier), "enabled": status.Enabled,
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
	s.refreshes.start("accounts", func(ctx context.Context) (any, error) { return s.fetchAccounts(ctx, false) })
}

func (s *Service) invalidateAccountCache() {
	s.cacheMu.Lock()
	if len(s.accountCache) > 0 {
		// Keep the last known status visible while the new account pool is
		// checked in the background after a mutation.
		s.accountCachedAt = time.Now().Add(-24 * time.Hour)
	} else {
		s.accountCachedAt = time.Time{}
	}
	s.cacheMu.Unlock()
	s.persistCache()
}
