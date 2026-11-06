package admin

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"clodesen2api/internal/controlstore"
	"clodesen2api/internal/modelpolicy"
)

func (s *Service) modelPolicyRoute(w http.ResponseWriter, r *http.Request) {
	store := s.currentModelPolicies()
	if store == nil {
		writeError(w, 503, "模型策略不可用")
		return
	}
	if r.URL.Path == "/admin/api/model-policies" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeError(w, 405, "方法不支持")
			return
		}
		value, err := store.Snapshot(r.Context())
		if err != nil {
			writeError(w, 503, "模型策略读取失败")
			return
		}
		writeJSON(w, 200, value)
		return
	}
	source, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/admin/api/model-policies/"))
	if err != nil || source == "" {
		writeError(w, 400, "模型 ID 无效")
		return
	}
	var result modelpolicy.Snapshot
	switch r.Method {
	case http.MethodPut:
		var input struct {
			Revision int64            `json:"revision"`
			Rule     modelpolicy.Rule `json:"rule"`
		}
		if err := decodeJSON(w, r, &input); err != nil || input.Rule.SourceID != source || input.Revision < 1 {
			writeError(w, 400, "模型策略参数无效")
			return
		}
		result, err = store.Put(r.Context(), input.Revision, input.Rule)
	case http.MethodDelete:
		var input struct {
			Revision int64 `json:"revision"`
		}
		if err := decodeJSON(w, r, &input); err != nil || input.Revision < 1 {
			writeError(w, 400, "必须提供 revision")
			return
		}
		result, err = store.Delete(r.Context(), input.Revision, source)
	default:
		w.Header().Set("Allow", "PUT, DELETE")
		writeError(w, 405, "方法不支持")
		return
	}
	if errors.Is(err, controlstore.ErrConflict) {
		writeError(w, 409, "模型策略已更新，请刷新后重试")
		return
	}
	if errors.Is(err, controlstore.ErrMissing) {
		writeError(w, 404, "模型策略不存在")
		return
	}
	if err != nil {
		if errors.Is(err, modelpolicy.ErrInvalid) {
			writeError(w, 400, err.Error())
		} else {
			writeError(w, 503, "模型策略保存失败")
		}
		return
	}
	writeJSON(w, 200, result)
}
