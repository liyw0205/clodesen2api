package admin

import "net/http"

func (s *Service) batchAccounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		return
	}
	var input struct {
		AccountIDs []string `json:"accountIds"`
		Enabled    *bool    `json:"enabled"`
	}
	if err := decodeJSON(w, r, &input); err != nil || input.Enabled == nil || len(input.AccountIDs) == 0 || len(input.AccountIDs) > 500 {
		writeError(w, http.StatusBadRequest, "请选择 1-500 个账号及启停状态")
		return
	}
	wanted := make(map[string]bool, len(input.AccountIDs))
	for _, id := range input.AccountIDs {
		if id == "" || wanted[id] {
			writeError(w, http.StatusBadRequest, "账号 ID 无效或重复")
			return
		}
		wanted[id] = true
	}
	s.changeMu.Lock()
	defer s.changeMu.Unlock()
	accounts, revision, err := s.store.Snapshot()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "账号存储不可用")
		return
	}
	found := 0
	for i := range accounts {
		if wanted[accounts[i].ID] {
			accounts[i].Enabled = *input.Enabled
			found++
		}
	}
	if found != len(wanted) {
		writeError(w, http.StatusNotFound, "部分账号不存在，未执行修改，请刷新后重试")
		return
	}
	if err := s.replaceAccountsLocked(accounts, revision); err != nil {
		writeAccountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": found, "enabled": *input.Enabled})
}
