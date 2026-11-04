package admin

import (
	"context"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"clodesen2api/internal/updater"
)

func (s *Service) updateRoute(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		status, err := s.checkUpdate(r.Context())
		if err != nil {
			writeError(w, http.StatusBadGateway, "检查更新失败，请稍后重试")
			return
		}
		writeJSON(w, http.StatusOK, status)
	case http.MethodPut:
		if s.cfg == nil {
			writeError(w, http.StatusServiceUnavailable, "配置管理不可用")
			return
		}
		var selection updater.SourceSelection
		if err := decodeJSON(w, r, &selection); err != nil {
			writeError(w, http.StatusBadRequest, "更新源配置格式无效")
			return
		}
		option, err := updater.ResolveSource(selection)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		selection.ID = option.ID
		selection.CustomPrefix = strings.TrimSpace(selection.CustomPrefix)
		s.configMu.Lock()
		previousSource, previousPrefix := s.cfg.Update.Source, s.cfg.Update.CustomPrefix
		s.cfg.Update.Source = selection.ID
		s.cfg.Update.CustomPrefix = selection.CustomPrefix
		if err := s.cfg.Save(); err != nil {
			s.cfg.Update.Source, s.cfg.Update.CustomPrefix = previousSource, previousPrefix
			s.configMu.Unlock()
			writeError(w, http.StatusInternalServerError, "保存更新源失败")
			return
		}
		s.configMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"source": selection.ID, "customPrefix": selection.CustomPrefix})
	case http.MethodPatch:
		var selection updater.SourceSelection
		if err := decodeJSON(w, r, &selection); err != nil {
			writeError(w, http.StatusBadRequest, "测速请求格式无效")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 18*time.Second)
		defer cancel()
		latency, err := updater.TestSource(ctx, selection)
		result := map[string]any{"source": selection.ID, "latencyMs": latency.Milliseconds(), "ok": err == nil}
		if err != nil {
			result["error"] = err.Error()
		}
		writeJSON(w, http.StatusOK, result)
	case http.MethodPost:
		status, err := s.checkUpdate(r.Context())
		if err != nil {
			writeError(w, http.StatusBadGateway, "检查更新失败，请稍后重试")
			return
		}
		if !status.UpdateAvailable {
			writeError(w, http.StatusConflict, "当前已经是最新版本")
			return
		}
		if !status.CanInstall {
			writeError(w, http.StatusNotImplemented, "此平台暂不支持面板内自动更新，请下载对应 ZIP 包")
			return
		}
		executable, err := os.Executable()
		if err == nil {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
			defer cancel()
			err = updater.Install(ctx, status, executable)
		}
		if err != nil {
			writeError(w, http.StatusBadGateway, "更新安装失败，请检查网络和程序目录权限")
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{
			"version": status.LatestVersion,
			"message": "更新已安装，服务即将重启",
		})
		s.mu.Lock()
		restart := s.restartProcess
		s.mu.Unlock()
		if restart != nil {
			go func() {
				time.Sleep(1500 * time.Millisecond)
				_ = restart()
			}()
		}
	default:
		w.Header().Set("Allow", "GET, PUT, PATCH, POST")
		writeError(w, http.StatusMethodNotAllowed, "方法不支持")
	}
}

func (s *Service) checkUpdate(ctx context.Context) (updater.Status, error) {
	s.mu.Lock()
	version, goarm := s.appVersion, s.goarm
	s.mu.Unlock()
	selection := updater.SourceSelection{ID: "builtin:gh-proxy-com"}
	s.configMu.Lock()
	if s.cfg != nil {
		selection = updater.SourceSelection{ID: s.cfg.Update.Source, CustomPrefix: s.cfg.Update.CustomPrefix}
	}
	s.configMu.Unlock()
	if version == "" {
		version = "dev"
	}
	if goarm == "" {
		goarm = "7"
	}
	requestCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return updater.Check(requestCtx, version, runtime.GOOS, runtime.GOARCH, goarm, selection)
}
