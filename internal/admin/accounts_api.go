package admin

import (
	"encoding/json"
	"net/http"
	"strings"

	"clodesen2api/internal/upstream"
)

func (s *Service) addAccount(w http.ResponseWriter, r *http.Request) {
	input, err := decodeObject(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return
	}
	for key := range input {
		if key != "identifier" && key != "password" {
			writeError(w, 400, "账号包含不支持的字段")
			return
		}
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
	accounts, revision, err := s.store.Snapshot()
	if err != nil {
		writeError(w, 503, "账号存储不可用")
		return
	}
	accounts = append(accounts, upstream.AccountCredential{ID: id, Identifier: identifier, Password: password, Enabled: true})
	if err := s.replaceAccountsLocked(accounts, revision); err != nil {
		writeAccountError(w, err)
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
	for key := range input {
		if key != "identifier" && key != "password" && key != "enabled" {
			writeError(w, 400, "账号包含不支持的字段")
			return
		}
	}
	s.changeMu.Lock()
	defer s.changeMu.Unlock()
	accounts, revision, err := s.store.Snapshot()
	if err != nil {
		writeError(w, 503, "账号存储不可用")
		return
	}
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
	if err := s.replaceAccountsLocked(accounts, revision); err != nil {
		writeAccountError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) deleteAccount(w http.ResponseWriter, id string) {
	s.changeMu.Lock()
	defer s.changeMu.Unlock()
	accounts, revision, err := s.store.Snapshot()
	if err != nil {
		writeError(w, 503, "账号存储不可用")
		return
	}
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
	if err := s.replaceAccountsLocked(next, revision); err != nil {
		writeAccountError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) replaceAccountsLocked(next []upstream.AccountCredential, revision int64) error {
	if err := validateCredentials(next); err != nil {
		return err
	}
	if err := s.store.ReplaceVersion(next, revision); err != nil {
		return err
	}
	if err := s.upstream.ReplaceAccounts(next); err != nil {
		return err
	}
	s.invalidateAccountCache()
	return nil
}

func accountIdentifier(identifier string) string {
	return identifier
}
