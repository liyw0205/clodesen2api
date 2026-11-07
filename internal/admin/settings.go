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

func (s *Service) configRoute(w http.ResponseWriter, r *http.Request) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	if s.cfg == nil {
		writeError(w, http.StatusServiceUnavailable, "配置管理不可用")
		return
	}
	if s.settings != nil {
		s.savedSettingsRoute(w, r)
		return
	}
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
	s.configMu.Lock()
	if s.settings != nil {
		view, err := s.settings.View(r.Context())
		if err == nil {
			view.Auth.Strategy = strategy
			var hot config.Config
			_, hot, err = s.settings.Save(r.Context(), view)
			if err == nil {
				s.publishSettings(hot)
			}
		}
		s.configMu.Unlock()
		if err != nil {
			settingsError(w, err)
			return
		}
		s.invalidateAccountCache()
		writeJSON(w, 200, map[string]string{"strategy": s.upstream.Strategy()})
		return
	}
	s.configMu.Unlock()
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
