package admin

import (
	"encoding/json"
	"net/http"

	"clodesen2api/internal/upstream"
)

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
			"account": accountIdentifier(outcome.Identifier), "status": outcome.Status,
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
