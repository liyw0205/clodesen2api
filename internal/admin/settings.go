package admin

import (
	"context"
	"errors"
	"net/http"

	"clodesen2api/internal/config"
	"clodesen2api/internal/controlstore"
)

func (s *Service) SetSettings(store *config.SettingsStore) {
	s.configMu.Lock()
	s.settings = store
	s.configMu.Unlock()
}

func (s *Service) SyncSettings(ctx context.Context) error {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	if s.settings == nil {
		return nil
	}
	_, hot, err := s.settings.ViewAfter(ctx)
	if err == nil {
		s.publishSettings(hot)
	}
	return err
}

func (s *Service) publishSettings(cfg config.Config) {
	s.cfg = &cfg
	if s.upstream != nil {
		s.upstream.ApplyRuntimeConfig(&cfg)
	}
}

func settingsError(w http.ResponseWriter, err error) {
	if errors.Is(err, controlstore.ErrConflict) {
		writeError(w, http.StatusConflict, "设置已被其他操作更新，请刷新后重试")
		return
	}
	writeError(w, http.StatusBadRequest, "保存设置失败：字段被锁定、取值无效或存储不可用")
}

func (s *Service) savedSettingsRoute(w http.ResponseWriter, r *http.Request) {
	// configRoute holds configMu for the complete read/save/publish operation.
	switch r.Method {
	case http.MethodGet:
		view, err := s.settings.View(r.Context())
		if err != nil {
			writeError(w, 503, "读取设置失败")
			return
		}
		writeJSON(w, 200, view)
	case http.MethodPut:
		var settings config.PublicSettings
		if err := decodeJSON(w, r, &settings); err != nil {
			writeError(w, 400, "配置格式无效")
			return
		}
		view, hot, err := s.settings.Save(r.Context(), settings)
		if err != nil {
			settingsError(w, err)
			return
		}
		s.publishSettings(hot)
		restart := false
		for _, field := range view.Fields {
			restart = restart || field.Pending
		}
		writeJSON(w, 200, map[string]any{"settings": view, "restartRequired": restart})
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, 405, "方法不支持")
	}
}
