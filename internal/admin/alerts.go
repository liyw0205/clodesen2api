package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"clodesen2api/internal/alerts"
	"clodesen2api/internal/controlstore"
)

func (s *Service) alertsRoute(w http.ResponseWriter, r *http.Request) {
	if s.control == nil {
		writeError(w, 503, "通知存储不可用")
		return
	}
	if r.Method == http.MethodGet {
		view, err := alerts.Load(r.Context(), s.control)
		if err != nil {
			writeError(w, 503, "通知存储不可用")
			return
		}
		writeJSON(w, 200, alerts.Public(view))
		return
	}
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, 405, "方法不支持")
		return
	}
	var input struct {
		Revision     int64           `json:"revision"`
		Settings     alerts.Settings `json:"settings"`
		ClearSecrets []string        `json:"clearSecrets"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, 400, "通知配置无效")
		return
	}
	view, err := alerts.SaveWithClear(r.Context(), s.control, input.Revision, input.Settings, input.ClearSecrets)
	if errors.Is(err, controlstore.ErrConflict) {
		writeError(w, 409, "通知设置已更新，请刷新重试")
		return
	}
	if errors.Is(err, alerts.ErrInvalid) {
		writeError(w, 400, err.Error())
		return
	}
	if err != nil {
		writeError(w, 503, "保存通知设置失败")
		return
	}
	writeJSON(w, 200, alerts.Public(view))
}

func (s *Service) testAlertChannel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, 405, "方法不支持")
		return
	}
	if s.control == nil {
		writeError(w, 503, "通知存储不可用")
		return
	}
	var input struct {
		Revision     int64             `json:"revision"`
		Channel      string            `json:"channel"`
		Config       map[string]string `json:"config"`
		ClearSecrets []string          `json:"clearSecrets"`
	}
	if decodeJSON(w, r, &input) != nil {
		writeError(w, 400, "通知配置无效")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	err := alerts.Test(ctx, s.control, input.Revision, input.Channel, alerts.ChannelSettings{Config: input.Config}, input.ClearSecrets)
	switch {
	case errors.Is(err, controlstore.ErrConflict):
		writeError(w, 409, "通知设置已更新，请刷新重试")
	case errors.Is(err, alerts.ErrInvalid):
		writeError(w, 400, err.Error())
	case err != nil:
		writeError(w, 502, "测试通知发送失败，请检查渠道配置和网络")
	default:
		writeJSON(w, 200, map[string]string{"message": "测试通知已发送"})
	}
}

func (s *Service) scheduleAlerts(ctx context.Context, now time.Time) {
	if s.control == nil || s.Draining() {
		return
	}
	view, err := alerts.Load(ctx, s.control)
	if err != nil || !view.Settings.Enabled {
		return
	}
	status := s.SystemStatus(ctx)
	observed := map[string]bool{"no_accounts": status.Accounts.Available == 0, "audit_degraded": status.Audit.Degraded}
	if status.JobsAvailable {
		s.mu.Lock()
		jobStore := s.jobs
		s.mu.Unlock()
		if list, err := jobStore.List(ctx); err == nil {
			latest := map[string]time.Time{}
			failed := map[string]bool{}
			for _, job := range list {
				if job.State == "queued" || job.State == "running" || job.State == "cancelled" {
					continue
				}
				if job.UpdatedAt.After(latest[job.Kind]) {
					latest[job.Kind] = job.UpdatedAt
					failed[job.Kind] = job.State != "succeeded"
				}
			}
			observed["job_failure"] = false
			for _, failure := range failed {
				if failure {
					observed["job_failure"] = true
				}
			}
		}
	}
	raw, at := s.accountCacheSnapshot()
	if len(raw) > 0 && time.Since(at) < s.cacheTTL("accounts") {
		var payload struct {
			Accounts []struct {
				Enabled bool   `json:"enabled"`
				Balance *int64 `json:"balance"`
			} `json:"accounts"`
		}
		if json.Unmarshal(raw, &payload) == nil {
			var total int64
			known := true
			enabled := 0
			for _, account := range payload.Accounts {
				if account.Enabled {
					enabled++
					if account.Balance == nil {
						known = false
					} else {
						total += *account.Balance
					}
				}
			}
			if known && enabled > 0 {
				observed["low_balance"] = total < view.Settings.LowBalance
			}
		}
	}
	_ = alerts.Observe(ctx, s.control, observed, now)
}
