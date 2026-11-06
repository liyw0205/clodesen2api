package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"clodesen2api/internal/controlstore"
)

func (s *Service) SyncAccounts(ctx context.Context) error {
	if s.store == nil || s.store.control == nil || s.upstream == nil {
		return nil
	}
	s.accountSyncMu.Lock()
	defer s.accountSyncMu.Unlock()
	revision, err := s.store.control.Revision(ctx, "accounts")
	if err != nil {
		return err
	}
	if revision == s.accountRevision {
		return nil
	}
	accounts, current, err := s.store.Snapshot()
	if err != nil {
		return err
	}
	if err := s.upstream.ReplaceAccounts(accounts); err != nil {
		return err
	}
	strategy := s.store.Strategy()
	s.configMu.Lock()
	if s.settings != nil {
		strategy = s.cfg.Auth.Strategy
	}
	s.configMu.Unlock()
	if err := s.upstream.SetStrategy(strategy); err != nil {
		return err
	}
	s.accountRevision = current
	s.invalidateAccountCache()
	return nil
}

func (s *Service) WithAccountSync(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			if s.Draining() {
				w.Header().Set("Retry-After", "30")
				writeError(w, 503, "恢复准备中")
				return
			}
			if err := s.SyncSettings(r.Context()); err != nil {
				writeError(w, 503, "配置存储不可用")
				return
			}
			if err := s.SyncAccounts(r.Context()); err != nil {
				writeError(w, 503, "账号存储不可用")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
func writeAccountError(w http.ResponseWriter, err error) {
	if errors.Is(err, controlstore.ErrConflict) {
		writeError(w, 409, "账号已被其他实例更新，请刷新后重试")
		return
	}
	writeError(w, 400, "保存账号失败")
}
