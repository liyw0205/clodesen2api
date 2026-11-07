package admin

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"clodesen2api/internal/backups"
	"clodesen2api/internal/config"
	"clodesen2api/internal/controlstore"
)

func BackupFiles(cfg *config.Config) map[string]string {
	return map[string]string{"config.toml": cfg.ConfigFilePath(), "admin.token": cfg.Admin.TokenFile}
}
func (s *Service) SetBackups(store *backups.Service) { s.mu.Lock(); s.backups = store; s.mu.Unlock() }
func (s *Service) backupRoute(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	store := s.backups
	s.mu.Unlock()
	if store == nil {
		writeError(w, 503, "备份服务不可用")
		return
	}
	if r.URL.Path == "/admin/api/backups/schedule" {
		if r.Method == http.MethodGet {
			view, err := store.Schedule(r.Context())
			if err != nil {
				writeError(w, 503, "读取备份计划失败")
				return
			}
			writeJSON(w, 200, view)
			return
		}
		if r.Method == http.MethodPut {
			var input struct {
				Revision int64                    `json:"revision"`
				Settings backups.ScheduleSettings `json:"settings"`
			}
			if err := decodeJSON(w, r, &input); err != nil {
				writeError(w, 400, "备份计划格式无效")
				return
			}
			view, err := store.SaveSchedule(r.Context(), input.Revision, input.Settings)
			if errors.Is(err, controlstore.ErrConflict) {
				writeError(w, 409, "备份计划已更新，请刷新重试")
				return
			}
			if err != nil {
				writeError(w, 400, "备份计划保存失败：请检查时间、时区和保留数量")
				return
			}
			writeJSON(w, 200, view)
			return
		}
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, 405, "方法不支持")
		return
	}
	if r.URL.Path == "/admin/api/backups/restore-status" && r.Method == http.MethodGet {
		status, err := store.Status()
		if err != nil {
			writeError(w, 503, "读取恢复状态失败")
			return
		}
		writeJSON(w, 200, status)
		return
	}
	if r.URL.Path == "/admin/api/backups/restore" && r.Method == http.MethodPost {
		var input struct {
			PlanID string `json:"planId"`
		}
		if err := decodeJSON(w, r, &input); err != nil {
			writeError(w, 400, "恢复计划无效")
			return
		}
		s.changeMu.Lock()
		defer s.changeMu.Unlock()
		s.mu.Lock()
		request := s.requestRestore
		s.mu.Unlock()
		if request == nil {
			writeError(w, 503, "当前启动方式不支持恢复重启")
			return
		}
		if err := s.usage.Flush(r.Context()); err != nil {
			writeError(w, 503, "恢复前审计保存失败")
			return
		}
		s.configMu.Lock()
		_, err := store.Create(r.Context(), true)
		s.configMu.Unlock()
		if err != nil {
			writeError(w, 503, "恢复前备份失败")
			return
		}
		if err := store.Arm(input.PlanID); err != nil {
			writeError(w, 409, "恢复计划已过期，请重新校验")
			return
		}
		s.mu.Lock()
		s.draining = true
		s.mu.Unlock()
		if !request() {
			_ = store.Disarm()
			s.mu.Lock()
			s.draining = false
			s.mu.Unlock()
			writeError(w, 409, "其他实例仍在运行，请先停止其他实例")
			return
		}
		writeJSON(w, 202, map[string]string{"state": "draining", "message": "恢复前备份已创建，服务即将重启"})
		return
	}
	if r.URL.Path == "/admin/api/backups" {
		switch r.Method {
		case http.MethodGet:
			records, err := store.List()
			if err != nil {
				writeError(w, 503, "读取备份失败")
				return
			}
			writeJSON(w, 200, map[string]any{"backups": records, "scope": "full", "containsCredentials": true})
		case http.MethodPost:
			if err := s.usage.Flush(r.Context()); err != nil {
				writeError(w, 503, "审计数据尚未保存")
				return
			}
			s.configMu.Lock()
			record, err := store.Create(r.Context(), false)
			s.configMu.Unlock()
			if err != nil {
				writeError(w, 503, "创建备份失败")
				return
			}
			writeJSON(w, 201, record)
		default:
			w.Header().Set("Allow", "GET, POST")
			writeError(w, 405, "方法不支持")
		}
		return
	}
	if r.URL.Path == "/admin/api/backups/validate" && r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 512<<20)
		defer r.Body.Close()
		plan, err := store.Prepare(r.Context(), r.Body)
		if err != nil {
			writeError(w, 400, "备份校验失败：格式、哈希、文件范围或数据库不符合要求")
			return
		}
		writeJSON(w, 200, plan)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/admin/api/backups/") && (r.Method == http.MethodGet || r.Method == http.MethodDelete) {
		id := strings.TrimPrefix(r.URL.Path, "/admin/api/backups/")
		if r.Method == http.MethodDelete {
			if err := store.Delete(id); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					writeError(w, 404, "备份不存在")
				} else {
					writeError(w, 400, "备份标识无效")
				}
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		path, err := store.Path(id)
		if err != nil {
			writeError(w, 404, "备份不存在")
			return
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			writeError(w, 404, "备份不存在")
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="`+id+`.zip"`)
		http.ServeFile(w, r, path)
		return
	}
	writeError(w, 404, "备份接口不存在")
}
