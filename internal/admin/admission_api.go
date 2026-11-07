package admin

import (
	"errors"
	"net/http"

	"clodesen2api/internal/admission"
	"clodesen2api/internal/controlstore"
)

func (s *Service) reconcileAdmission(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	store := s.admission
	s.mu.Unlock()
	if store == nil {
		writeError(w, 503, "计量存储不可用")
		return
	}
	var input struct {
		ID      string           `json:"id"`
		Version int64            `json:"version"`
		Actual  admission.Amount `json:"actual"`
	}
	if err := decodeJSON(w, r, &input); err != nil || input.Version < 1 {
		writeError(w, 400, "对账参数无效")
		return
	}
	if err := store.Reconcile(r.Context(), input.ID, input.Version, input.Actual); err != nil {
		writeError(w, 409, "用量已更新或缺少完整计量，请刷新后重试")
		return
	}
	writeJSON(w, 200, map[string]bool{"reconciled": true})
}

func (s *Service) admissionRoute(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	store := s.admission
	s.mu.Unlock()
	if store == nil {
		writeError(w, 503, "请求准入不可用")
		return
	}
	if r.Method == http.MethodGet {
		view, err := store.Snapshot(r.Context())
		if err != nil {
			writeError(w, 503, "请求准入存储不可用")
			return
		}
		writeJSON(w, 200, view)
		return
	}
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, 405, "方法不支持")
		return
	}
	var input struct {
		Revision int64              `json:"revision"`
		Settings admission.Settings `json:"settings"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, 400, "参数无效")
		return
	}
	if err := input.Settings.Validate(); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	view, err := store.Update(r.Context(), input.Revision, input.Settings)
	if errors.Is(err, controlstore.ErrConflict) {
		writeError(w, 409, "准入设置已更新，请刷新重试")
		return
	}
	if err != nil {
		writeError(w, 503, "保存请求准入失败")
		return
	}
	writeJSON(w, 200, view)
}
