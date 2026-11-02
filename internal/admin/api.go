package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"clodesen2api/internal/upstream"
)

const upstreamCacheTTL = 12 * time.Hour

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
	case r.URL.Path == "/admin/api/models":
		if r.Method == http.MethodGet {
			s.listModels(w, r)
		} else {
			w.Header().Set("Allow", "GET")
			writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		}
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
	case r.URL.Path == "/admin/api/redeem" && r.Method == http.MethodPost:
		s.redeem(w, r)
	default:
		writeError(w, http.StatusNotFound, "管理接口不存在")
	}
}

func (s *Service) listModels(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	force := forceRefresh(r)
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if !force && !s.modelCachedAt.IsZero() && time.Since(s.modelCachedAt) < upstreamCacheTTL {
		writeRawJSON(w, s.modelCache)
		return
	}
	models, err := s.upstream.ListModels(ctx)
	if err != nil {
		if len(s.modelCache) > 0 {
			writeRawJSON(w, s.modelCache)
			return
		}
		writeError(w, http.StatusBadGateway, "读取上游模型列表失败")
		return
	}
	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(models, &payload); err != nil {
		writeError(w, http.StatusBadGateway, "上游模型列表格式无效")
		return
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
	s.modelCache = append(s.modelCache[:0], result...)
	s.modelCachedAt = time.Now()
	writeRawJSON(w, s.modelCache)
}

func (s *Service) listCreditUsage(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 500 {
		limit = 500
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	force := forceRefresh(r)
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if force || s.creditUsageCachedAt.IsZero() || time.Since(s.creditUsageCachedAt) >= upstreamCacheTTL {
		records, err := s.upstream.ListCreditUsage(ctx)
		if err != nil && s.creditUsageCachedAt.IsZero() {
			writeError(w, http.StatusBadGateway, "读取积分使用记录失败")
			return
		}
		if err == nil {
			s.creditUsageCache = append(s.creditUsageCache[:0], records...)
			s.creditUsageCachedAt = time.Now()
		}
	}
	records := mergeCreditRecords(s.localCreditRecords(), s.creditUsageCache, time.Now())
	if len(records) > limit {
		records = records[:limit]
	}
	items := make([]map[string]any, 0, len(records))
	for _, record := range records {
		item := map[string]any{
			"id": record.ID, "accountId": record.AccountID,
			"account": maskIdentifier(record.Identifier), "model": record.Model,
			"amount": record.Amount, "balanceAfter": nil,
			"type": record.Type, "description": record.Description,
			"status": record.Status, "createdAt": record.CreatedAt, "source": record.Source,
		}
		if record.BalanceKnown {
			item["balanceAfter"] = record.BalanceAfter
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": items, "refreshedAt": time.Now()})
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
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if !force && !s.accountCachedAt.IsZero() && time.Since(s.accountCachedAt) < upstreamCacheTTL {
		writeRawJSON(w, s.accountCache)
		return
	}
	statuses := s.upstream.AccountStatuses(ctx)
	accounts := make([]map[string]any, 0, len(statuses))
	var availableCredits int64
	availableKnown := true
	for _, status := range statuses {
		accounts = append(accounts, map[string]any{
			"id": status.ID, "identifier": maskIdentifier(status.Identifier), "enabled": status.Enabled,
			"cooldownUntil": status.CooldownUntil, "balance": status.Balance, "checkedIn": status.CheckedIn,
			"redemptionEnabled": status.RedemptionEnabled, "redemptionLimit": status.RedemptionLimit,
			"redemptionRemaining": status.RedemptionRemaining, "resetAt": status.ResetAt,
			"creditType": status.CreditType, "validityDays": status.ValidityDays,
			"refreshedAt": status.RefreshedAt, "error": status.Error,
		})
		if status.Enabled && (status.CooldownUntil == nil || !status.CooldownUntil.After(time.Now())) {
			if status.Balance == nil {
				availableKnown = false
			} else {
				availableCredits += *status.Balance
			}
		}
	}
	response := map[string]any{"accounts": accounts, "strategy": s.upstream.Strategy(), "refreshedAt": time.Now()}
	if availableKnown {
		response["availableCredits"] = availableCredits
	}
	result, _ := json.Marshal(response)
	s.accountCache = append(s.accountCache[:0], result...)
	s.accountCachedAt = time.Now()
	writeRawJSON(w, s.accountCache)
}

func (s *Service) invalidateAccountCache() {
	s.cacheMu.Lock()
	s.accountCache = nil
	s.accountCachedAt = time.Time{}
	s.cacheMu.Unlock()
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
		writeError(w, http.StatusBadRequest, "请选择轮询策略")
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
