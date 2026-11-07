package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"clodesen2api/internal/admission"
	"clodesen2api/internal/apikey"
	"clodesen2api/internal/backups"
	"clodesen2api/internal/config"
	"clodesen2api/internal/controlstore"
	"clodesen2api/internal/jobs"
	"clodesen2api/internal/modelcooldown"
	"clodesen2api/internal/modelpolicy"
	"clodesen2api/internal/upstream"
	"clodesen2api/internal/usage"
)

const sessionCookie = "clodesen_admin"

type Service struct {
	upstream            *upstream.Upstream
	store               *AccountStore
	usage               *usage.Collector
	token               string
	tokenFile           string
	tokenMu             sync.RWMutex
	mu                  sync.Mutex
	changeMu            sync.Mutex
	sessions            map[string]time.Time
	cacheMu             sync.Mutex
	cacheWriteMu        sync.Mutex
	accountCache        []byte
	accountCachedAt     time.Time
	accountRefreshMu    sync.Mutex
	modelCache          []byte
	modelCachedAt       time.Time
	modelRefreshMu      sync.Mutex
	creditUsageCache    []upstream.CreditUsageRecord
	creditUsageCachedAt time.Time
	creditUsageReady    bool
	creditRefreshMu     sync.Mutex
	cachePath           string
	localCreditMu       sync.Mutex
	localCreditUsage    []upstream.CreditUsageRecord
	localCreditPath     string
	redemptionMu        sync.Mutex
	redemptionHistory   []RedemptionHistoryRecord
	redemptionPath      string
	cfg                 *config.Config
	settings            *config.SettingsStore
	configMu            sync.Mutex
	appVersion          string
	goarm               string
	restartProcess      func() error
	keyStore            *apikey.Store
	control             *controlstore.Store
	modelPolicies       *modelpolicy.Store
	modelCooldowns      *modelcooldown.Store
	admission           *admission.Store
	jobs                *jobs.Store
	refreshes           refreshGroup
	accountSyncMu       sync.Mutex
	accountRevision     int64
	backups             *backups.Service
	requestRestore      func() bool
	draining            bool
	loginSecurity       loginSecurity
	startedAt           time.Time
}

func (s *Service) SetAdmission(store *admission.Store) {
	s.mu.Lock()
	s.admission = store
	s.mu.Unlock()
}

func (s *Service) SetRestoreRequest(request func() bool) {
	s.mu.Lock()
	s.requestRestore = request
	s.mu.Unlock()
}
func (s *Service) Draining() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.draining }

func (s *Service) SetModelCooldowns(store *modelcooldown.Store) {
	s.mu.Lock()
	s.modelCooldowns = store
	s.mu.Unlock()
}

func (s *Service) SetModelPolicies(store *modelpolicy.Store) {
	s.mu.Lock()
	s.modelPolicies = store
	s.mu.Unlock()
}

func (s *Service) currentModelPolicies() *modelpolicy.Store {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.modelPolicies
}

func (s *Service) SetControlStore(store *controlstore.Store) {
	s.mu.Lock()
	s.control = store
	s.mu.Unlock()
}

type savedSession struct {
	Expires   time.Time `json:"expires"`
	TokenHash string    `json:"tokenHash"`
}

func sessionHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func (s *Service) changeSession(session string, expires *time.Time) error {
	s.mu.Lock()
	store := s.control
	s.mu.Unlock()
	if store == nil {
		return nil
	}
	tokenHash := sessionHash(s.currentToken())
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	return store.Mutate(ctx, "sessions", func(raw json.RawMessage) (any, error) {
		sessions := make(map[string]savedSession)
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &sessions); err != nil {
				return nil, err
			}
		}
		for id, item := range sessions {
			if !item.Expires.After(time.Now()) || item.TokenHash != tokenHash {
				delete(sessions, id)
			}
		}
		id := sessionHash(session)
		if expires == nil {
			delete(sessions, id)
		} else {
			if len(sessions) >= 128 {
				return nil, errors.New("session limit reached")
			}
			sessions[id] = savedSession{Expires: *expires, TokenHash: tokenHash}
		}
		return sessions, nil
	})
}

func (s *Service) SetKeyStore(store *apikey.Store) {
	s.mu.Lock()
	s.keyStore = store
	s.mu.Unlock()
}

func (s *Service) SetUpdater(version, goarm string, restart func() error) {
	s.mu.Lock()
	s.appVersion = strings.TrimSpace(version)
	s.goarm = strings.TrimSpace(goarm)
	s.restartProcess = restart
	s.mu.Unlock()
}

func NewHandler(up *upstream.Upstream, store *AccountStore, token string, collectors ...*usage.Collector) http.Handler {
	return NewHandlerWithTokenFile(up, store, token, "data/admin.token", collectors...)
}

func NewHandlerWithTokenFile(up *upstream.Upstream, store *AccountStore, token, tokenFile string, collectors ...*usage.Collector) http.Handler {
	return NewHandlerWithConfig(up, store, token, tokenFile, nil, collectors...)
}

func NewHandlerWithConfig(up *upstream.Upstream, store *AccountStore, token, tokenFile string, cfg *config.Config, collectors ...*usage.Collector) http.Handler {
	return NewServiceWithConfig(up, store, token, tokenFile, cfg, collectors...)
}

func NewServiceWithConfig(up *upstream.Upstream, store *AccountStore, token, tokenFile string, cfg *config.Config, collectors ...*usage.Collector) *Service {
	collector := usage.New()
	if len(collectors) > 0 && collectors[0] != nil {
		collector = collectors[0]
	}
	tokenFile = strings.TrimSpace(tokenFile)
	var creditPath, cachePath, redemptionPath string
	if cfg != nil {
		creditPath = filepath.Join("data", "credit-usage.json")
		cachePath = filepath.Join("data", "admin-cache.json")
		redemptionPath = filepath.Join("data", "redemption-history.json")
		if tokenFile != "" {
			creditPath = filepath.Join(filepath.Dir(tokenFile), "credit-usage.json")
			cachePath = filepath.Join(filepath.Dir(tokenFile), "admin-cache.json")
			redemptionPath = filepath.Join(filepath.Dir(tokenFile), "redemption-history.json")
		}
	}
	service := &Service{
		upstream: up, store: store, usage: collector,
		token: strings.TrimSpace(token), tokenFile: tokenFile,
		sessions: make(map[string]time.Time), localCreditPath: creditPath, cachePath: cachePath,
		redemptionPath: redemptionPath, cfg: cfg,
	}
	service.startedAt = time.Now()
	trustedProxies := ""
	if cfg != nil {
		trustedProxies = cfg.API.TrustedProxies
	}
	service.loginSecurity.resolver, _ = apikey.NewAuthenticator("", nil, false, trustedProxies)
	service.localCreditUsage = loadLocalCreditUsage(creditPath)
	service.redemptionHistory = loadRedemptionHistory(redemptionPath)
	service.loadPersistentCache()
	if up != nil {
		up.SetCreditPricing(fallbackPricing())
		up.SetCreditUsageRecorder(service.recordLocalCredit)
		up.SetRedemptionRecorder(service.recordRedemption)
	}
	return service
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Path == "/admin/api/session" {
		s.handleSession(w, r)
		return
	}
	if s.currentToken() == "" || s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "管理接口未配置，请设置 admin.token 后重启服务")
		return
	}
	if !s.authenticated(r) {
		writeError(w, http.StatusUnauthorized, "请先登录管理后台")
		return
	}
	if s.Draining() && isMutation(r.Method) {
		writeError(w, 503, "恢复准备中，暂停接受新操作")
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
	if isMutation(r.Method) && !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "跨站请求已拒绝")
		return
	}
	s.api(w, r)
}

func (s *Service) handleSession(w http.ResponseWriter, r *http.Request) {
	if isMutation(r.Method) && !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "跨站请求已拒绝")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": s.store != nil, "authenticated": s.authenticated(r),
			"setupRequired": s.store != nil && s.currentToken() == "",
		})
	case http.MethodPost:
		if s.store == nil {
			writeError(w, http.StatusServiceUnavailable, "管理接口未配置账号存储")
			return
		}
		ip := s.loginIP(r)
		wait, err := s.admitLogin(r.Context(), ip, time.Now())
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "登录保护存储不可用")
			return
		}
		if wait > 0 {
			rejectLoginWait(w, wait)
			return
		}
		event := "invalid_request"
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			if err := s.recordLogin(ctx, ip, event, time.Now()); err != nil {
				log.Printf("[admin] login event storage failed")
			}
		}()
		var body struct {
			Token   string `json:"token"`
			Confirm string `json:"confirm"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "请求格式无效")
			return
		}
		body.Token = strings.TrimSpace(body.Token)
		current := s.currentToken()
		if current == "" {
			if !sameOrigin(r) {
				writeError(w, http.StatusForbidden, "跨站请求已拒绝")
				return
			}
			if err := ValidateSetupToken(body.Token); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			if body.Token != strings.TrimSpace(body.Confirm) {
				writeError(w, http.StatusBadRequest, "两次输入的管理令牌不一致")
				return
			}
			s.tokenMu.Lock()
			if s.token == "" {
				if err := SaveToken(s.tokenFile, body.Token); err != nil {
					s.tokenMu.Unlock()
					writeError(w, http.StatusInternalServerError, "保存管理令牌失败")
					return
				}
				s.token = body.Token
			}
			current = s.token
			s.tokenMu.Unlock()
		}
		if len(body.Token) != len(current) || subtle.ConstantTimeCompare([]byte(body.Token), []byte(current)) != 1 {
			event = "failure"
			writeError(w, http.StatusUnauthorized, "令牌不正确")
			return
		}
		event = "session_failed"
		if s.createSession(w, r) {
			event = "success"
		}
	case http.MethodDelete:
		if cookie, err := r.Cookie(sessionCookie); err == nil {
			if err := s.changeSession(cookie.Value, nil); err != nil {
				writeError(w, 503, "退出登录失败，请重试")
				return
			}
			s.mu.Lock()
			delete(s.sessions, cookie.Value)
			s.mu.Unlock()
		}
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/admin", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		writeError(w, http.StatusMethodNotAllowed, "方法不支持")
	}
}

func (s *Service) createSession(w http.ResponseWriter, r *http.Request) bool {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		writeError(w, http.StatusInternalServerError, "无法创建登录会话")
		return false
	}
	session, expires := hex.EncodeToString(raw), time.Now().Add(12*time.Hour)
	if err := s.changeSession(session, &expires); err != nil {
		writeError(w, 503, "无法保存登录会话")
		return false
	}
	s.mu.Lock()
	if s.control == nil {
		s.sessions[session] = expires
	}
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: session, Path: "/admin", HttpOnly: true,
		Secure:   r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"),
		SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: 12 * 60 * 60,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
	return true
}

func (s *Service) currentToken() string {
	s.tokenMu.RLock()
	defer s.tokenMu.RUnlock()
	return s.token
}

func (s *Service) authenticated(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	s.mu.Lock()
	store := s.control
	s.mu.Unlock()
	if store != nil {
		var sessions map[string]savedSession
		if _, err := store.Load(r.Context(), "sessions", &sessions); err != nil {
			return false
		}
		item, ok := sessions[sessionHash(cookie.Value)]
		return ok && item.Expires.After(time.Now()) && item.TokenHash == sessionHash(s.currentToken())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	expires, ok := s.sessions[cookie.Value]
	if !ok || !expires.After(time.Now()) {
		delete(s.sessions, cookie.Value)
		return false
	}
	return true
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host != r.Host || parsed.Path != "" || parsed.User != nil {
		return false
	}
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	if secure {
		return parsed.Scheme == "https"
	}
	return parsed.Scheme == "http"
}

func isMutation(method string) bool {
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("multiple JSON values")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
