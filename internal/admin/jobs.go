package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"clodesen2api/internal/jobs"
)

func (s *Service) StartJobs(ctx context.Context, store *jobs.Store) *jobs.Runner {
	s.mu.Lock()
	s.jobs = store
	s.mu.Unlock()
	return jobs.Start(ctx, store, s.executeJob)
}

func (s *Service) executeJob(ctx context.Context, kind, target string, params json.RawMessage) (any, error) {
	if err := s.SyncSettings(ctx); err != nil {
		return nil, err
	}
	if err := s.SyncAccounts(ctx); err != nil {
		return nil, err
	}
	defer s.invalidateAccountCache()
	switch kind {
	case "checkin":
		result, err := s.upstream.CheckinAccount(ctx, target)
		return map[string]any{"checkedIn": result.CheckedIn, "alreadyChecked": result.AlreadyChecked, "reward": result.Reward, "balance": result.Balance}, err
	case "probe":
		count, err := s.upstream.ProbeAccount(ctx, target)
		return map[string]any{"accountId": target, "modelCount": count, "ok": err == nil}, err
	case "refresh":
		result, err := s.upstream.RefreshAccountStatus(ctx, target)
		if err == nil && result.Error != "" {
			err = errors.New("account_refresh_failed")
		}
		return map[string]any{"accountId": target, "balance": result.Balance, "ok": err == nil && result.Error == ""}, err
	case "redeem":
		var input struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(params, &input); err != nil {
			return nil, err
		}
		result := s.upstream.RedeemCode(ctx, target, input.Code)
		payload := map[string]any{"accountId": target, "status": result.Status, "credits": result.Credits}
		if result.Status != "success" {
			return payload, errors.New("redemption_failed")
		}
		return payload, nil
	}
	return nil, errors.New("unsupported job kind")
}

func (s *Service) jobsRoute(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	store := s.jobs
	s.mu.Unlock()
	if store == nil {
		writeError(w, 503, "后台任务不可用")
		return
	}
	if r.URL.Path == "/admin/api/jobs" {
		switch r.Method {
		case http.MethodGet:
			list, err := store.List(r.Context())
			if err != nil {
				writeError(w, 503, "读取任务失败")
				return
			}
			writeJSON(w, 200, map[string]any{"jobs": list})
		case http.MethodPost:
			var input struct {
				Kind       string   `json:"kind"`
				AccountIDs []string `json:"accountIds"`
				Code       string   `json:"code"`
			}
			if err := decodeJSON(w, r, &input); err != nil {
				writeError(w, 400, "任务参数无效")
				return
			}
			if input.Kind == "redeem" && (strings.TrimSpace(input.Code) == "" || len(input.Code) > 64) {
				writeError(w, 400, "兑换码无效")
				return
			}
			known := map[string]bool{}
			for _, account := range s.store.Accounts() {
				if account.Enabled {
					known[account.ID] = true
				}
			}
			if len(input.AccountIDs) == 0 {
				for id := range known {
					input.AccountIDs = append(input.AccountIDs, id)
				}
			}
			for _, id := range input.AccountIDs {
				if !known[id] {
					writeError(w, 400, "账号不存在或已停用")
					return
				}
			}
			var params any
			if input.Kind == "redeem" {
				params = map[string]string{"code": strings.TrimSpace(input.Code)}
			}
			job, duplicate, err := store.Submit(r.Context(), input.Kind, input.AccountIDs, params)
			if err != nil {
				writeJobError(w, err)
				return
			}
			writeJSON(w, 202, map[string]any{"job": job, "duplicate": duplicate})
		default:
			w.Header().Set("Allow", "GET, POST")
			writeError(w, 405, "方法不支持")
		}
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/admin/api/jobs/"), "/")
	if len(parts) == 1 && r.Method == http.MethodGet {
		list, err := store.List(r.Context())
		if err != nil {
			writeJobError(w, err)
			return
		}
		for _, job := range list {
			if job.ID == parts[0] {
				writeJSON(w, 200, job)
				return
			}
		}
		writeError(w, 404, "任务不存在")
		return
	}
	if len(parts) == 2 && r.Method == http.MethodPost {
		switch parts[1] {
		case "cancel":
			if err := store.Cancel(r.Context(), parts[0]); err != nil {
				writeJobError(w, err)
				return
			}
			w.WriteHeader(204)
			return
		case "retry":
			job, duplicate, err := store.Retry(r.Context(), parts[0])
			if err != nil {
				writeJobError(w, err)
				return
			}
			writeJSON(w, 202, map[string]any{"job": job, "duplicate": duplicate})
			return
		}
	}
	writeError(w, 404, "任务接口不存在")
}
func writeJobError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, jobs.ErrMissing):
		writeError(w, 404, "任务不存在")
	case errors.Is(err, jobs.ErrCapacity):
		w.Header().Set("Retry-After", "3")
		writeError(w, 503, "任务队列已满")
	case errors.Is(err, jobs.ErrUnsafeRetry):
		writeError(w, 409, "此任务不能自动重试，请核实上游结果")
	default:
		writeError(w, 400, "任务操作失败")
	}
}
