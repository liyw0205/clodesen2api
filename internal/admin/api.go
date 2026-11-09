package admin

import (
	"net/http"
	"strings"
)

func (s *Service) api(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/admin/api/playground/chat/completions" || r.URL.Path == "/admin/api/playground/responses" || r.URL.Path == "/admin/api/playground/images/generations" || r.URL.Path == "/admin/api/playground/images/edits":
		s.playgroundRoute(w, r)
	case r.URL.Path == "/admin/api/alerts/test":
		s.testAlertChannel(w, r)
	case r.URL.Path == "/admin/api/alerts":
		s.alertsRoute(w, r)
	case r.URL.Path == "/admin/api/reports":
		s.reportsRoute(w, r)
	case r.URL.Path == "/admin/api/system-status":
		s.systemStatusRoute(w, r)
	case r.URL.Path == "/admin/api/login-events":
		s.listLoginEvents(w, r)
	case r.URL.Path == "/admin/api/admission/reconcile" && r.Method == http.MethodPost:
		s.reconcileAdmission(w, r)
	case r.URL.Path == "/admin/api/backups" || strings.HasPrefix(r.URL.Path, "/admin/api/backups/"):
		s.backupRoute(w, r)
	case r.URL.Path == "/admin/api/cache-status" && r.Method == http.MethodGet:
		writeJSON(w, 200, s.cacheStatus())
	case r.URL.Path == "/admin/api/jobs" || strings.HasPrefix(r.URL.Path, "/admin/api/jobs/"):
		s.jobsRoute(w, r)
	case r.URL.Path == "/admin/api/admission":
		s.admissionRoute(w, r)
	case r.URL.Path == "/admin/api/model-cooldowns":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeError(w, 405, "方法不支持")
			return
		}
		s.mu.Lock()
		store := s.modelCooldowns
		s.mu.Unlock()
		if store == nil {
			writeError(w, 503, "模型冷却不可用")
			return
		}
		writeJSON(w, 200, store.Snapshot(r.Context()))
	case r.URL.Path == "/admin/api/model-policies" || strings.HasPrefix(r.URL.Path, "/admin/api/model-policies/"):
		s.modelPolicyRoute(w, r)
	case r.URL.Path == "/admin/api/keys":
		s.keysRoot(w, r)
	case strings.HasPrefix(r.URL.Path, "/admin/api/keys/"):
		s.keyRoute(w, r)
	case r.URL.Path == "/admin/api/accounts":
		switch r.Method {
		case http.MethodGet:
			s.listAccounts(w, r)
		case http.MethodPost:
			s.addAccount(w, r)
		default:
			w.Header().Set("Allow", "GET, POST")
			writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		}
	case r.URL.Path == "/admin/api/checkin" && r.Method == http.MethodPost:
		s.manualCheckin(w, r, "")
	case r.URL.Path == "/admin/api/accounts/batch":
		s.batchAccounts(w, r)
	case strings.HasPrefix(r.URL.Path, "/admin/api/accounts/"):
		s.accountRoute(w, r)
	case r.URL.Path == "/admin/api/settings":
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, map[string]string{"strategy": s.upstream.Strategy()})
		} else if r.Method == http.MethodPut {
			s.updateSettings(w, r)
		} else {
			w.Header().Set("Allow", "GET, PUT")
			writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		}
	case r.URL.Path == "/admin/api/config":
		s.configRoute(w, r)
	case r.URL.Path == "/admin/api/models":
		if r.Method == http.MethodGet {
			s.listModels(w, r)
		} else {
			w.Header().Set("Allow", "GET")
			writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		}
	case r.URL.Path == "/admin/api/update":
		s.updateRoute(w, r)
	case r.URL.Path == "/admin/api/credit-usage":
		if r.Method == http.MethodGet {
			s.listCreditUsage(w, r)
		} else {
			w.Header().Set("Allow", "GET")
			writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		}
	case r.URL.Path == "/admin/api/usage":
		if r.Method == http.MethodGet {
			s.refreshAudit(r)
			writeJSON(w, http.StatusOK, s.usage.Snapshot())
		} else {
			w.Header().Set("Allow", "GET")
			writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		}
	case r.URL.Path == "/admin/api/request-records":
		if r.Method == http.MethodGet {
			s.listRequestRecords(w, r)
		} else {
			w.Header().Set("Allow", "GET")
			writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		}
	case r.URL.Path == "/admin/api/redeem" && r.Method == http.MethodPost:
		s.redeem(w, r)
	case r.URL.Path == "/admin/api/redemption-history" && r.Method == http.MethodGet:
		s.listRedemptionHistory(w, r)
	default:
		writeError(w, http.StatusNotFound, "管理接口不存在")
	}
}

func (s *Service) playgroundRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		return
	}
	s.mu.Lock()
	handler := s.playground
	s.mu.Unlock()
	if handler == nil {
		writeError(w, http.StatusServiceUnavailable, "调用测试不可用")
		return
	}
	request := r.Clone(r.Context())
	request.URL.Path = "/v1" + strings.TrimPrefix(r.URL.Path, "/admin/api/playground")
	request.URL.RawPath = ""
	handler.ServeHTTP(w, request)
}
