package admin

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"clodesen2api/internal/admission"
	"clodesen2api/internal/apikey"
	"clodesen2api/internal/usage"
)

func (s *Service) currentKeyStore() *apikey.Store {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.keyStore
}

func (s *Service) keysRoot(w http.ResponseWriter, r *http.Request) {
	store := s.currentKeyStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "API key 管理不可用")
		return
	}
	if err := store.Sync(); err != nil {
		writeError(w, http.StatusServiceUnavailable, "API key 存储暂不可用")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.refreshAudit(r)
		type keyView struct {
			apikey.Key
			Usage usage.KeySummary `json:"usage"`
			Quota map[string]any   `json:"quota,omitempty"`
		}
		s.mu.Lock()
		capacity := s.admission
		s.mu.Unlock()
		var budget admission.View
		if capacity != nil {
			var err error
			budget, err = capacity.Snapshot(r.Context())
			if err != nil {
				writeError(w, 503, "配额存储暂不可用")
				return
			}
		}
		stats := s.usage.KeySummaries(time.Now().Add(-24 * time.Hour))
		keys := make([]keyView, 0)
		items, err := store.ListChecked()
		if err != nil {
			writeError(w, 503, "API key 存储暂不可用")
			return
		}
		for _, item := range items {
			quota := map[string]any{}
			for id, counter := range budget.Counters {
				if strings.HasPrefix(id, item.ID+"/day/") {
					quota["dailyUsed"] = counter.Requests
					quota["dailyResetAt"] = counter.Until
				}
				if strings.HasPrefix(id, item.ID+"/month/") {
					quota["monthlyUsed"] = counter.Requests
					quota["monthlyResetAt"] = counter.Until
				}
			}
			for id, balance := range budget.Balances {
				if strings.HasPrefix(id, item.ID+"/day/") {
					quota["dailyMetering"] = balance
				}
				if strings.HasPrefix(id, item.ID+"/month/") {
					quota["monthlyMetering"] = balance
				}
			}
			keys = append(keys, keyView{Key: item, Usage: stats[item.ID], Quota: quota})
		}
		s.configMu.Lock()
		external, anonymous := false, false
		if s.cfg != nil {
			external, anonymous = s.cfg.API.Key != "", s.cfg.API.AllowNoAuth
		}
		s.configMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"keys": keys, "externalConfigured": external, "allowNoAuth": anonymous, "refreshedAt": time.Now()})
	case http.MethodPost:
		var input apikey.CreateRequest
		if err := decodeJSON(w, r, &input); err != nil {
			writeError(w, http.StatusBadRequest, "API key 参数格式无效")
			return
		}
		item, secret, err := store.Create(input)
		if err != nil {
			writeKeyError(w, err)
			return
		}
		// secret is returned only at creation/rotation time; it is never listed.
		writeJSON(w, http.StatusCreated, map[string]any{"key": item, "secret": secret})
	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, http.StatusMethodNotAllowed, "方法不支持")
	}
}

func (s *Service) keyRoute(w http.ResponseWriter, r *http.Request) {
	store := s.currentKeyStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "API key 管理不可用")
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/admin/api/keys/"), "/")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		writeError(w, http.StatusNotFound, "API key 不存在")
		return
	}
	id, err := url.PathUnescape(parts[0])
	if err != nil {
		writeError(w, http.StatusBadRequest, "API key ID 无效")
		return
	}
	if len(parts) == 2 && parts[1] == "usage" && r.Method == http.MethodGet {
		if _, err := store.Get(id); err != nil {
			writeKeyError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s.usage.KeySummaries(time.Now().Add(-24 * time.Hour))[id])
		return
	}
	if len(parts) == 1 {
		if r.Method != http.MethodPut && r.Method != http.MethodDelete {
			w.Header().Set("Allow", "PUT, DELETE")
			writeError(w, http.StatusMethodNotAllowed, "方法不支持")
			return
		}
		if r.Method == http.MethodDelete {
			version, ok := keyVersion(r)
			if !ok {
				var input struct {
					Version int `json:"version"`
				}
				if err := decodeJSON(w, r, &input); err == nil {
					version, ok = input.Version, input.Version > 0
				}
			}
			if !ok {
				writeError(w, http.StatusBadRequest, "必须提供有效的 version")
				return
			}
			if err := store.Delete(id, version); err != nil {
				writeKeyError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var input apikey.UpdateRequest
		if err := decodeJSON(w, r, &input); err != nil {
			writeError(w, http.StatusBadRequest, "API key 参数格式无效")
			return
		}
		if input.Version < 1 {
			writeError(w, http.StatusBadRequest, "必须提供有效的 version")
			return
		}
		item, err := store.Update(id, input.Version, input)
		if err != nil {
			writeKeyError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"key": item})
		return
	}
	if len(parts) == 2 && parts[1] == "rotate" && r.Method == http.MethodPost {
		var input struct {
			Version int `json:"version"`
		}
		if err := decodeJSON(w, r, &input); err != nil || input.Version < 1 {
			writeError(w, http.StatusBadRequest, "必须提供有效的 version")
			return
		}
		item, secret, err := store.Rotate(id, input.Version)
		if err != nil {
			writeKeyError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"key": item, "secret": secret})
		return
	}
	writeError(w, http.StatusNotFound, "API key 接口不存在")
}

func keyVersion(r *http.Request) (int, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("version"))
	version, err := strconv.Atoi(raw)
	return version, err == nil && version > 0
}

func writeKeyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, apikey.ErrNotFound):
		writeError(w, http.StatusNotFound, "API key 不存在")
	case errors.Is(err, apikey.ErrVersionConflict):
		writeError(w, http.StatusConflict, "API key 已被其他操作更新，请刷新后重试")
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}
