// Package upstream 负责管理 cookie、登录，并把请求转发到 pai.zaiduyu.top。
package upstream

import (
	"bufio"
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	mathrand "math/rand"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"clodesen2api/internal/admission"
	"clodesen2api/internal/config"
	"clodesen2api/internal/httpx"
	"clodesen2api/internal/modelcooldown"
	"clodesen2api/internal/openai"
)

// Upstream 负责管理 cookie、登录、并把请求转发到 pai.zaiduyu.top。
type Upstream struct {
	cfg *config.Config

	httpClient *http.Client
	// loginClient 用独立短超时，避免登录卡住
	loginClient *http.Client

	mu          sync.Mutex
	loggedIn    bool
	lastRelogin time.Time
	accountID   string
	enabled     bool

	accounts             []*Upstream
	owner                *Upstream
	poolMode             bool
	strategy             string
	rpmLimit             int
	rpmInFlight          map[string]int
	fixedAccount         *Upstream
	poolMu               sync.Mutex
	creditCheckMu        sync.Mutex
	nextAccount          int
	cooldownUntil        map[*Upstream]time.Time
	cooldownReason       map[*Upstream]string
	accountStateRecorder func(accountID string, enabled bool, cooldownUntil *time.Time, cooldownReason string) error
	modelCooldowns       *modelcooldown.Store
	creditMu             sync.RWMutex
	creditRecorder       func(CreditUsageRecord)
	creditPricing        map[string]int64
	redemptionRecorder   func(string, RedemptionResult)
}

// RequestAccountSetter is optionally implemented by the response writer to
// expose the account selected for the current client request. It keeps account
// routing metadata out of the public Forward return signature.
type RequestAccountSetter interface {
	SetUpstreamAccount(accountID, identifier string)
}

type RequestAttemptRecorder interface {
	RecordUpstreamAttempt(accountID, identifier, model string, status int, duration time.Duration, wrote bool, errorCode string)
}

type attemptResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *attemptResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *attemptResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}
func (w *attemptResponseWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (w *attemptResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

const (
	insufficientCreditsCooldown        = 30 * time.Minute
	insufficientCreditsThreshold int64 = 100
)

const (
	cooldownReasonInsufficientCredits = "insufficient_credits"
	cooldownReasonTemporaryBan        = "temporary_ban"
)

// New 构造上游客户端。cookiejar 用来自动保存 set-cookie。
func New(cfg *config.Config) (*Upstream, error) {
	credentials, err := configuredAccounts(cfg)
	if err != nil {
		return nil, err
	}
	if len(credentials) == 0 {
		return newSession(cfg)
	}
	accounts := make([]AccountCredential, 0, len(credentials))
	for _, credential := range credentials {
		accounts = append(accounts, AccountCredential{
			ID:         accountIDFor(credential.identifier),
			Identifier: credential.identifier,
			Password:   credential.password,
			Enabled:    true,
		})
	}
	return NewWithAccounts(cfg, accounts)
}

// AccountCredential is the persisted login data for one upstream account.
type AccountCredential struct {
	ID             string     `json:"id"`
	Identifier     string     `json:"identifier"`
	Password       string     `json:"password"`
	Enabled        bool       `json:"enabled"`
	CooldownUntil  *time.Time `json:"cooldownUntil,omitempty"`
	CooldownReason string     `json:"cooldownReason,omitempty"`
}

// ConfiguredAccounts parses legacy single-account and multi-account config.
func ConfiguredAccounts(cfg *config.Config) ([]AccountCredential, error) {
	credentials, err := configuredAccounts(cfg)
	if err != nil {
		return nil, err
	}
	accounts := make([]AccountCredential, 0, len(credentials))
	for _, credential := range credentials {
		accounts = append(accounts, AccountCredential{
			ID:         accountIDFor(credential.identifier),
			Identifier: credential.identifier,
			Password:   credential.password,
			Enabled:    true,
		})
	}
	return accounts, nil
}

// NewWithAccounts creates a pool from an explicit account list.
func NewWithAccounts(cfg *config.Config, credentials []AccountCredential) (*Upstream, error) {
	immutable := *cfg
	cfg = &immutable
	strategy := strings.ToLower(strings.TrimSpace(cfg.Auth.Strategy))
	if strategy == "" {
		strategy = "fixed"
	}
	if strategy != "round_robin" && strategy != "random" && strategy != "fixed" {
		return nil, fmt.Errorf("invalid auth.strategy %q (want round_robin, random, or fixed)", cfg.Auth.Strategy)
	}
	rpmLimit := configuredRPM(cfg.Auth.RPM)
	if rpmLimit > 1000 {
		return nil, fmt.Errorf("invalid auth.rpm %d (want 1 to 1000)", cfg.Auth.RPM)
	}

	pool := &Upstream{
		cfg: cfg, poolMode: true,
		strategy:       strategy,
		rpmLimit:       rpmLimit,
		rpmInFlight:    make(map[string]int),
		cooldownUntil:  make(map[*Upstream]time.Time),
		cooldownReason: make(map[*Upstream]string),
		creditPricing:  make(map[string]int64),
	}
	for _, credential := range credentials {
		credential.Identifier = strings.TrimSpace(credential.Identifier)
		if credential.ID == "" {
			credential.ID = accountIDFor(credential.Identifier)
		}
		accountCfg := *cfg
		accountCfg.Auth.Accounts = ""
		accountCfg.Auth.Identifier = credential.Identifier
		accountCfg.Auth.Password = credential.Password
		account, err := newSession(&accountCfg)
		if err != nil {
			return nil, err
		}
		account.accountID = credential.ID
		account.enabled = credential.Enabled
		account.owner = pool
		account.creditPricing = make(map[string]int64)
		pool.accounts = append(pool.accounts, account)
		if credential.CooldownUntil != nil && (credential.CooldownUntil.After(time.Now()) || normalizeCooldownReason(credential.CooldownReason) == cooldownReasonInsufficientCredits) {
			pool.cooldownUntil[account] = credential.CooldownUntil.UTC()
			pool.cooldownReason[account] = normalizeCooldownReason(credential.CooldownReason)
		}
	}
	return pool, nil
}

// SetAccountStateRecorder persists runtime state changes such as an upstream
// restriction cooldown. It is optional so upstream remains usable standalone.
func (u *Upstream) SetAccountStateRecorder(recorder func(accountID string, enabled bool, cooldownUntil *time.Time, cooldownReason string) error) {
	u.poolMu.Lock()
	u.accountStateRecorder = recorder
	u.poolMu.Unlock()
}

// ApplyRuntimeConfig publishes only pool settings. Session configuration stays
// immutable; changing connection settings takes effect after restart.
func (u *Upstream) ApplyRuntimeConfig(cfg *config.Config) {
	if cfg == nil {
		return
	}
	strategy := strings.ToLower(strings.TrimSpace(cfg.Auth.Strategy))
	if strategy == "round_robin" || strategy == "random" || strategy == "fixed" {
		u.poolMu.Lock()
		u.strategy = strategy
		u.rpmLimit = configuredRPM(cfg.Auth.RPM)
		u.poolMu.Unlock()
	}
}

// SetCreditUsageRecorder receives successful model requests as local,
// real-time credit events. The callback is intentionally small so the
// upstream package does not depend on the admin persistence layer.
func (u *Upstream) SetCreditUsageRecorder(recorder func(CreditUsageRecord)) {
	accounts := u.accountsSnapshot()
	u.creditMu.Lock()
	u.creditRecorder = recorder
	pricing := cloneCreditPricing(u.creditPricing)
	u.creditMu.Unlock()
	for _, account := range accounts {
		account.creditMu.Lock()
		account.creditRecorder = recorder
		account.creditPricing = pricing
		account.creditMu.Unlock()
	}
}

// SetRedemptionRecorder receives every account redemption result, including
// redemptions triggered by the scheduled task.
func (u *Upstream) SetRedemptionRecorder(recorder func(string, RedemptionResult)) {
	accounts := u.accountsSnapshot()
	u.creditMu.Lock()
	u.redemptionRecorder = recorder
	u.creditMu.Unlock()
	for _, account := range accounts {
		account.creditMu.Lock()
		account.redemptionRecorder = recorder
		account.creditMu.Unlock()
	}
}

// SetCreditPricing updates request-level estimates used by local records.
func (u *Upstream) SetCreditPricing(prices []ModelPricing) {
	pricing := make(map[string]int64, len(prices))
	for _, price := range prices {
		if price.CreditsPerRequest == nil {
			continue
		}
		model := strings.ToLower(strings.TrimSpace(price.Model))
		if model != "" {
			pricing[model] = *price.CreditsPerRequest
		}
	}
	accounts := u.accountsSnapshot()
	u.creditMu.Lock()
	u.creditPricing = pricing
	recorder := u.creditRecorder
	u.creditMu.Unlock()
	for _, account := range accounts {
		account.creditMu.Lock()
		account.creditPricing = cloneCreditPricing(pricing)
		account.creditRecorder = recorder
		account.creditMu.Unlock()
	}
}

func cloneCreditPricing(source map[string]int64) map[string]int64 {
	copyOf := make(map[string]int64, len(source))
	for model, credits := range source {
		copyOf[model] = credits
	}
	return copyOf
}

type accountCredential struct {
	identifier string
	password   string
}

func configuredAccounts(cfg *config.Config) ([]accountCredential, error) {
	if strings.TrimSpace(cfg.Auth.Accounts) == "" {
		identifier := strings.TrimSpace(cfg.Auth.Identifier)
		if identifier == "" && strings.TrimSpace(cfg.Auth.Password) == "" {
			return nil, nil
		}
		return []accountCredential{{identifier: identifier, password: cfg.Auth.Password}}, nil
	}

	entries := strings.Split(cfg.Auth.Accounts, ",")
	accounts := make([]accountCredential, 0, len(entries))
	for i, entry := range entries {
		identifier, password, ok := strings.Cut(entry, ":")
		identifier = strings.TrimSpace(identifier)
		if !ok || identifier == "" || strings.TrimSpace(password) == "" {
			return nil, fmt.Errorf("invalid auth.accounts[%d]: expected identifier:password", i)
		}
		accounts = append(accounts, accountCredential{identifier: identifier, password: password})
	}
	return accounts, nil
}

func accountIDFor(identifier string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(identifier))))
	return hex.EncodeToString(sum[:])
}

func newSession(cfg *config.Config) (*Upstream, error) {
	immutable := *cfg
	cfg = &immutable
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("new cookie jar: %w", err)
	}

	transport := &http.Transport{
		// 流式响应不能设全局超时，这里只控制拨号/读首字节的超时
		ResponseHeaderTimeout: 60 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}

	mainTimeout := cfg.Upstream.RequestTimeout
	if mainTimeout > 0 {
		return &Upstream{
			cfg:            cfg,
			enabled:        true,
			cooldownUntil:  make(map[*Upstream]time.Time),
			cooldownReason: make(map[*Upstream]string),
			rpmLimit:       configuredRPM(cfg.Auth.RPM),
			rpmInFlight:    make(map[string]int),
			httpClient:     &http.Client{Transport: transport, Jar: jar, Timeout: mainTimeout},
			loginClient:    &http.Client{Transport: transport, Jar: jar, Timeout: cfg.Upstream.LoginTimeout},
		}, nil
	}
	// request_timeout=0 表示不限超时（流式）
	return &Upstream{
		cfg:            cfg,
		enabled:        true,
		cooldownUntil:  make(map[*Upstream]time.Time),
		cooldownReason: make(map[*Upstream]string),
		rpmLimit:       configuredRPM(cfg.Auth.RPM),
		rpmInFlight:    make(map[string]int),
		httpClient:     &http.Client{Transport: transport, Jar: jar},
		loginClient:    &http.Client{Transport: transport, Jar: jar, Timeout: cfg.Upstream.LoginTimeout},
	}, nil
}

// AccountCount 返回配置的上游账号数。
func (u *Upstream) AccountCount() int {
	u.poolMu.Lock()
	defer u.poolMu.Unlock()
	if len(u.accounts) > 0 {
		return len(u.accounts)
	}
	if !u.poolMode {
		return 1
	}
	return 0
}

func (u *Upstream) accountsSnapshot() []*Upstream {
	u.poolMu.Lock()
	defer u.poolMu.Unlock()
	return append([]*Upstream(nil), u.accounts...)
}

// Location 返回配置的签到时区，供 Checkin 计算日期。
func (u *Upstream) Location() *time.Location {
	if loc, err := time.LoadLocation(u.cfg.Checkin.Timezone); err == nil {
		return loc
	}
	return time.Local
}

// OpenAIBaseURL 返回 OpenAI 兼容接口的基础地址。登录和签到仍使用 BaseURL。
func (u *Upstream) OpenAIBaseURL() string {
	if baseURL := strings.TrimSpace(u.cfg.Upstream.OpenAIBaseURL); baseURL != "" {
		return strings.TrimRight(baseURL, "/")
	}
	return strings.TrimRight(u.cfg.Upstream.BaseURL, "/") + "/api/openai"
}

// userAgent 取配置里的 UA，空则回退到内置浏览器 UA。
func (u *Upstream) userAgent() string {
	if ua := strings.TrimSpace(u.cfg.Upstream.UserAgent); ua != "" {
		return ua
	}
	return browserUA
}

// applyHeaders 给每个出站请求注入浏览器必备 header，确保不被上游 WAF 拒绝。
func (u *Upstream) applyHeaders(req *http.Request, host string) {
	req.Header.Set("User-Agent", u.userAgent())
	req.Header.Set("Accept", "application/json, text/event-stream, */*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Origin", host)
	req.Header.Set("Referer", host+"/")
	// 让 Go 看起来像 XHR
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
}

// Login 走 /api/user-auth 登录拿 cookie。成功后 cookiejar 会自动保存 set-cookie。
func (u *Upstream) Login(ctx context.Context) error {
	accounts := u.accountsSnapshot()
	if len(accounts) > 0 {
		var loginErrors []error
		successes := 0
		for i, account := range accounts {
			if !account.enabled || !u.accountAvailable(account) {
				continue
			}
			if err := account.Login(ctx); err != nil {
				u.applyAccountRetryError(account, err)
				loginErrors = append(loginErrors, fmt.Errorf("account %d: %w", i+1, err))
				continue
			}
			successes++
		}
		if successes > 0 {
			return nil
		}
		if len(loginErrors) == 0 {
			return errors.New("all upstream accounts are disabled")
		}
		return errors.Join(loginErrors...)
	}
	if u.poolMode && (strings.TrimSpace(u.cfg.Auth.Identifier) == "" || strings.TrimSpace(u.cfg.Auth.Password) == "") {
		return errors.New("未配置上游账号")
	}

	u.mu.Lock()
	defer u.mu.Unlock()

	// 简单防抖：1 秒内重复 401 不重复登录
	if time.Since(u.lastRelogin) < time.Second {
		if u.loggedIn {
			return nil
		}
	}

	host := strings.TrimRight(u.cfg.Upstream.BaseURL, "/")
	body := map[string]string{
		"action":     "login",
		"identifier": u.cfg.Auth.Identifier,
		"password":   u.cfg.Auth.Password,
	}
	raw, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, host+u.cfg.Upstream.LoginPath, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("build login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	u.applyHeaders(req, host)

	resp, err := u.loginClient.Do(req)
	if err != nil {
		return fmt.Errorf("do login request: %w", err)
	}
	defer resp.Body.Close()

	// 读响应体用于错误信息
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))

	if resp.StatusCode/100 != 2 {
		u.loggedIn = false
		if restriction, ok := detectAccountRestriction(resp.StatusCode, respBody); ok {
			retryable := &safeAccountRetryError{
				err:            fmt.Errorf("login failed: upstream status %d", resp.StatusCode),
				cooldownUntil:  restriction.cooldownUntil,
				disableAccount: restriction.disableAccount,
			}
			u.restrictionOwner().applyAccountRetryError(u, retryable)
			return retryable
		}
		return fmt.Errorf("login failed: upstream status %d", resp.StatusCode)
	}
	if restriction, ok := detectAccountRestriction(resp.StatusCode, respBody); ok {
		u.loggedIn = false
		retryable := &safeAccountRetryError{
			err:            errors.New("login rejected by upstream"),
			cooldownUntil:  restriction.cooldownUntil,
			disableAccount: restriction.disableAccount,
		}
		u.restrictionOwner().applyAccountRetryError(u, retryable)
		return retryable
	}

	// 校验响应体不是错误 JSON（有些上游 200 也返回 {"error":...}）
	var probe struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(respBody, &probe); err == nil && probe.Error != "" {
		u.loggedIn = false
		return fmt.Errorf("login rejected by upstream: %s", probe.Error)
	}

	u.loggedIn = true
	u.lastRelogin = time.Now()
	return nil
}

// ensureLogin 首次调用或未登录时触发一次登录。
func (u *Upstream) ensureLogin(ctx context.Context) error {
	u.mu.Lock()
	logged := u.loggedIn
	u.mu.Unlock()
	if logged {
		return nil
	}
	return u.Login(ctx)
}

type computeRouteResponse struct {
	Enabled bool   `json:"enabled"`
	Ticket  string `json:"ticket"`
	Origin  string `json:"origin"`
}

type imageRoute struct {
	targetURL string
	requestID string
	ticket    string
}

// allocateImageRoute mirrors the web client handshake required by the
// upstream image service. Older/custom upstreams may not expose this endpoint;
// a 404/405 is treated as an opt-out and falls back to the configured URL.
func (u *Upstream) allocateImageRoute(ctx context.Context, host, openAIBaseURL string, request openai.Request) (imageRoute, error) {
	base, err := url.Parse(openAIBaseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return imageRoute{}, fmt.Errorf("invalid OpenAI base URL %q", openAIBaseURL)
	}

	requestID, err := newRequestID()
	if err != nil {
		return imageRoute{}, fmt.Errorf("create image request id: %w", err)
	}
	path := strings.TrimRight(base.EscapedPath(), "/") + request.Path
	if base.RawQuery != "" {
		path += "?" + base.RawQuery
	}
	routeBody, err := json.Marshal(map[string]string{
		"requestId": requestID,
		"path":      path,
		"model":     request.Model,
	})
	if err != nil {
		return imageRoute{}, fmt.Errorf("encode image route request: %w", err)
	}

	routeURL := strings.TrimRight(host, "/") + "/api/compute-route"
	routeReq, err := http.NewRequestWithContext(ctx, http.MethodPost, routeURL, bytes.NewReader(routeBody))
	if err != nil {
		return imageRoute{}, fmt.Errorf("build image route request: %w", err)
	}
	routeReq.Header.Set("Content-Type", "application/json")
	u.applyHeaders(routeReq, host)
	resp, err := u.loginClient.Do(routeReq)
	if err != nil {
		return imageRoute{}, fmt.Errorf("image route request: %w", err)
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return imageRoute{}, fmt.Errorf("read image route response: %w", readErr)
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return imageRoute{targetURL: openAIBaseURL + request.Path}, nil
	}
	if insufficientCredits(resp.StatusCode, body) {
		return imageRoute{}, &safeAccountRetryError{
			err: errors.New("image route rejected for insufficient credits"), creditRejected: true,
			response: &bufferedResponse{header: resp.Header.Clone(), status: resp.StatusCode, body: body},
		}
	}
	if resp.StatusCode/100 != 2 {
		upstreamResponse := &bufferedResponse{header: resp.Header.Clone(), status: resp.StatusCode, body: body}
		if restriction, ok := detectAccountRestriction(resp.StatusCode, body); ok {
			return imageRoute{}, &safeAccountRetryError{
				err:            fmt.Errorf("image route failed: upstream status %d", resp.StatusCode),
				response:       upstreamResponse,
				cooldownUntil:  restriction.cooldownUntil,
				disableAccount: restriction.disableAccount,
			}
		}
		return imageRoute{}, &safeAccountRetryError{
			err:      fmt.Errorf("image route failed: upstream status %d", resp.StatusCode),
			response: upstreamResponse,
		}
	}

	var route computeRouteResponse
	if err := json.Unmarshal(body, &route); err != nil {
		return imageRoute{}, fmt.Errorf("decode image route response: %w", err)
	}
	if !route.Enabled {
		return imageRoute{targetURL: openAIBaseURL + request.Path}, nil
	}
	if route.Ticket == "" || route.Origin == "" {
		return imageRoute{}, fmt.Errorf("image route response missing ticket or origin")
	}
	origin, err := url.Parse(route.Origin)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || (origin.Path != "" && origin.Path != "/") || origin.RawQuery != "" || origin.Fragment != "" {
		return imageRoute{}, fmt.Errorf("invalid image route origin %q", route.Origin)
	}

	return imageRoute{
		targetURL: strings.TrimRight(origin.String(), "/") + path,
		requestID: requestID,
		ticket:    route.Ticket,
	}, nil
}

func newRequestID() (string, error) {
	var raw [16]byte
	if _, err := cryptorand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func isImageRequest(path string) bool {
	return path == openai.GenerationsPath || path == openai.EditsPath
}

// Forward 把本地构造的 OpenAI 兼容请求转发到上游，返回上游的完整响应。
// 若上游回 401 且开启 auto-relogin，会重新登录后重试一次。
// 第二个返回值 wrote 表示响应头是否已经写给客户端（用于错误处理）。
func (u *Upstream) Forward(ctx context.Context, w http.ResponseWriter, request openai.Request) (error, bool) {
	var budget atomic.Int32
	budget.Store(int32(admission.Limits(ctx).MaxAttempts))
	ctx = context.WithValue(ctx, attemptBudgetKey{}, &budget)
	ctx, cancel := context.WithTimeout(ctx, time.Duration(admission.Limits(ctx).RequestTimeoutSeconds)*time.Second)
	defer cancel()
	u.recheckExpiredCreditCooldowns(ctx)
	if len(u.accountsSnapshot()) == 0 {
		if u.poolMode {
			return &NoAccountAvailableError{}, false
		}
		if !u.accountAvailable(u) {
			u.poolMu.Lock()
			retryAfter := time.Until(u.cooldownUntil[u])
			u.poolMu.Unlock()
			return &NoAccountAvailableError{RetryAfter: retryAfter}, false
		}
		if !u.reserveAccountRequest(u) {
			return &NoAccountAvailableError{RetryAfter: u.rpmRetryAfter()}, false
		}
		if wait := u.modelCooldownRemaining(ctx, request.Model); wait > 0 {
			u.releaseAccountRequest(u)
			return &NoAccountAvailableError{RetryAfter: wait}, false
		}
		err, wrote, insufficient := u.forwardReservedSingle(ctx, w, request, u)
		if err != nil {
			u.applyAccountRetryError(u, err)
			var retryable *safeAccountRetryError
			if !wrote && errors.As(err, &retryable) && retryable.response != nil {
				retryable.response.writeTo(w)
				return nil, true
			}
		}
		if insufficient != nil {
			u.handleCreditRejection(ctx, u)
			insufficient.writeTo(w)
			return nil, true
		}
		return err, wrote
	}
	return u.forwardPool(ctx, w, request)
}

type bufferedResponse struct {
	header http.Header
	status int
	body   []byte
}

func (response *bufferedResponse) writeTo(w http.ResponseWriter) {
	httpx.CopyHeaders(w.Header(), response.header)
	w.Header().Set("Content-Length", strconv.Itoa(len(response.body)))
	w.WriteHeader(response.status)
	_, _ = w.Write(response.body)
}

type accountCandidate struct {
	index   int
	account *Upstream
}

// NoAccountAvailableError is returned when no enabled account can accept a request.
type NoAccountAvailableError struct {
	RetryAfter time.Duration
}

func (e *NoAccountAvailableError) Error() string {
	return "all upstream accounts are cooling down or at their concurrency limit"
}

type safeAccountRetryError struct {
	err            error
	response       *bufferedResponse
	cooldownUntil  *time.Time
	disableAccount bool
	creditRejected bool
}

func (e *safeAccountRetryError) Error() string { return e.err.Error() }
func (e *safeAccountRetryError) Unwrap() error { return e.err }

func wrapSafeAccountRetryError(prefix string, err error) error {
	retryable := &safeAccountRetryError{err: err}
	var source *safeAccountRetryError
	if errors.As(err, &source) {
		retryable.response = source.response
		retryable.cooldownUntil = source.cooldownUntil
		retryable.disableAccount = source.disableAccount
		retryable.creditRejected = source.creditRejected
	}
	return fmt.Errorf("%s: %w", prefix, retryable)
}

func (u *Upstream) forwardPool(ctx context.Context, w http.ResponseWriter, request openai.Request) (error, bool) {
	candidates, retryAfter := u.selectAccounts()
	if len(candidates) == 0 {
		return &NoAccountAvailableError{RetryAfter: retryAfter}, false
	}
	imageRequest := isImageRequest(request.Path)

	var insufficient *bufferedResponse
	var retryableResponse *bufferedResponse
	var lastErr error
	attempted := 0
	for _, candidate := range candidates {
		if attempted >= admission.Limits(ctx).MaxAttempts {
			break
		}
		if wait := candidate.account.modelCooldownRemaining(ctx, request.Model); wait > 0 {
			if retryAfter <= 0 || wait < retryAfter {
				retryAfter = wait
			}
			continue
		}
		if !u.reserveAccountRequest(candidate.account) {
			continue
		}
		attempted++
		err, wrote, creditResponse := u.forwardReservedSingle(ctx, w, request, candidate.account)
		if err != nil && wrote {
			return err, true
		}
		if creditResponse != nil {
			insufficient = creditResponse
			u.handleCreditRejection(ctx, candidate.account)
			// An explicit balance rejection did not generate anything. It must
			// not consume the budget for potentially billable attempts.
			refundAttempt(ctx)
			attempted--
			continue
		}
		if err != nil {
			lastErr = err
			var retryable *safeAccountRetryError
			if errors.As(err, &retryable) {
				u.applyAccountRetryError(candidate.account, err)
				if retryable.cooldownUntil != nil {
					log.Printf("[account] account %d restricted; cooling down until %s", candidate.index+1, retryable.cooldownUntil.In(time.Local).Format(time.RFC3339))
				} else if retryable.disableAccount {
					log.Printf("[account] account %d restricted without an end time; disabling account", candidate.index+1)
				}
				if retryable.response != nil {
					retryableResponse = retryable.response
				}
				if imageRequest {
					if retryableResponse != nil {
						retryableResponse.writeTo(w)
						return nil, true
					}
					return err, false
				}
				continue
			}
			return err, false
		}
		if wrote {
			return nil, true
		}
	}
	if insufficient != nil {
		insufficient.writeTo(w)
		return nil, true
	}
	if retryableResponse != nil {
		retryableResponse.writeTo(w)
		return nil, true
	}
	if attempted == 0 {
		if retryAfter <= 0 {
			_, retryAfter = u.selectAccounts()
		}
		return &NoAccountAvailableError{RetryAfter: retryAfter}, false
	}
	if lastErr != nil {
		return lastErr, false
	}
	return errors.New("no upstream account completed the request"), false
}

func (u *Upstream) selectAccounts() ([]accountCandidate, time.Duration) {
	u.poolMu.Lock()
	defer u.poolMu.Unlock()

	now := time.Now()
	candidates := make([]accountCandidate, 0, len(u.accounts))
	var earliest time.Duration
	for i, account := range u.accounts {
		if !account.enabled {
			continue
		}
		until := u.cooldownUntil[account]
		if u.accountCoolingLocked(account, now) {
			wait := time.Until(until)
			if wait <= 0 {
				wait = time.Second
			}
			if earliest == 0 || wait < earliest {
				earliest = wait
			}
			continue
		}
		if u.rpmInFlight[account.accountID] >= configuredRPM(u.rpmLimit) {
			wait := time.Second
			if earliest == 0 || wait < earliest {
				earliest = wait
			}
			continue
		}
		candidates = append(candidates, accountCandidate{index: i, account: account})
	}
	if len(candidates) == 0 {
		return nil, earliest
	}

	if u.strategy == "random" {
		order := mathrand.Perm(len(candidates))
		shuffled := make([]accountCandidate, len(candidates))
		for i, index := range order {
			shuffled[i] = candidates[index]
		}
		return shuffled, 0
	}

	start := u.nextAccount % len(u.accounts)
	if u.strategy == "fixed" {
		if index := u.accountIndexLocked(u.fixedAccount); index >= 0 {
			start = index
		}
	} else {
		u.nextAccount = (u.nextAccount + 1) % len(u.accounts)
	}
	ordered := make([]accountCandidate, 0, len(candidates))
	for offset := 0; offset < len(u.accounts); offset++ {
		index := (start + offset) % len(u.accounts)
		account := u.accounts[index]
		if !account.enabled {
			continue
		}
		if u.accountCoolingLocked(account, now) {
			continue
		}
		if u.rpmInFlight[account.accountID] >= configuredRPM(u.rpmLimit) {
			continue
		}
		ordered = append(ordered, accountCandidate{index: index, account: account})
	}
	return ordered, 0
}

func configuredRPM(limit int) int {
	if limit <= 0 {
		return 4
	}
	return limit
}

func (u *Upstream) accountIndexLocked(account *Upstream) int {
	for index, candidate := range u.accounts {
		if candidate == account {
			return index
		}
	}
	return -1
}

func (u *Upstream) reserveAccountRequest(account *Upstream) bool {
	owner := account.owner
	if owner == nil {
		owner = account
	}
	owner.poolMu.Lock()
	defer owner.poolMu.Unlock()
	if (owner.poolMode && !account.enabled) || owner.accountCoolingLocked(account, time.Now()) {
		return false
	}
	if owner.rpmInFlight == nil {
		owner.rpmInFlight = make(map[string]int)
	}
	if owner.rpmInFlight[account.accountID] >= configuredRPM(owner.rpmLimit) {
		return false
	}
	owner.rpmInFlight[account.accountID]++
	if owner.strategy == "fixed" && owner.poolMode {
		owner.fixedAccount = account
	}
	return true
}

func (u *Upstream) releaseAccountRequest(account *Upstream) {
	owner := account.owner
	if owner == nil {
		owner = account
	}
	owner.poolMu.Lock()
	if owner.rpmInFlight[account.accountID] > 1 {
		owner.rpmInFlight[account.accountID]--
	} else {
		delete(owner.rpmInFlight, account.accountID)
	}
	owner.poolMu.Unlock()
}

func (u *Upstream) forwardReservedSingle(ctx context.Context, w http.ResponseWriter, request openai.Request, account *Upstream) (error, bool, *bufferedResponse) {
	defer u.releaseAccountRequest(account)
	identifier := ""
	if account.cfg != nil {
		identifier = account.cfg.Auth.Identifier
	}
	if setter, ok := w.(RequestAccountSetter); ok {
		setter.SetUpstreamAccount(account.accountID, identifier)
	}
	started := time.Now()
	tracked := &attemptResponseWriter{ResponseWriter: w}
	err, wrote, buffered := account.forwardSingle(ctx, tracked, request)
	var rejection *safeAccountRetryError
	if !wrote && errors.As(err, &rejection) && rejection.creditRejected {
		buffered, err = rejection.response, nil
	}
	if recorder, ok := w.(RequestAttemptRecorder); ok {
		status := tracked.status
		if buffered != nil {
			status = buffered.status
		}
		var retryable *safeAccountRetryError
		if errors.As(err, &retryable) && retryable.response != nil {
			status = retryable.response.status
		}
		code := ""
		if status >= 400 {
			code = fmt.Sprintf("upstream_http_%d", status)
		}
		if err != nil {
			code = "upstream_attempt_failed"
		}
		if errors.Is(err, context.Canceled) {
			code = "client_cancelled"
		}
		if errors.Is(err, context.DeadlineExceeded) {
			code = "upstream_timeout"
		}
		recorder.RecordUpstreamAttempt(account.accountID, identifier, request.Model, status, time.Since(started), wrote, code)
	}
	return err, wrote, buffered
}

func (u *Upstream) rpmRetryAfter() time.Duration {
	u.poolMu.Lock()
	defer u.poolMu.Unlock()
	now := time.Now()
	var earliest time.Duration
	accounts := u.accounts
	if len(accounts) == 0 && !u.poolMode {
		accounts = []*Upstream{u}
	}
	for _, account := range accounts {
		if !account.enabled {
			continue
		}
		until := u.cooldownUntil[account]
		if until.After(now) {
			wait := time.Until(until)
			if earliest == 0 || wait < earliest {
				earliest = wait
			}
			continue
		}
		if u.rpmInFlight[account.accountID] >= configuredRPM(u.rpmLimit) {
			wait := time.Second
			if earliest == 0 || wait < earliest {
				earliest = wait
			}
		}
	}
	return earliest
}

func (u *Upstream) accountAvailable(account *Upstream) bool {
	u.poolMu.Lock()
	defer u.poolMu.Unlock()
	return !u.accountCoolingLocked(account, time.Now())
}

func (u *Upstream) accountCoolingLocked(account *Upstream, now time.Time) bool {
	until, exists := u.cooldownUntil[account]
	return until.After(now) || (exists && normalizeCooldownReason(u.cooldownReason[account]) == cooldownReasonInsufficientCredits)
}

func (u *Upstream) cooldownUntilFor(account *Upstream) *time.Time {
	u.poolMu.Lock()
	until := u.cooldownUntil[account]
	u.poolMu.Unlock()
	if until.IsZero() || (!until.After(time.Now()) && u.cooldownReasonFor(account) != cooldownReasonInsufficientCredits) {
		return nil
	}
	until = until.UTC()
	return &until
}

func (u *Upstream) cooldownReasonFor(account *Upstream) string {
	u.poolMu.Lock()
	until := u.cooldownUntil[account]
	reason := u.cooldownReason[account]
	u.poolMu.Unlock()
	if until.IsZero() || (!until.After(time.Now()) && normalizeCooldownReason(reason) != cooldownReasonInsufficientCredits) {
		return ""
	}
	return normalizeCooldownReason(reason)
}

func (u *Upstream) cooldownIsTemporaryBan(account *Upstream) bool {
	return u.cooldownReasonFor(account) == cooldownReasonTemporaryBan
}

func (u *Upstream) clearCreditCooldown(account *Upstream) {
	var recorder func(string, bool, *time.Time, string) error
	changed := false
	enabled := false
	u.poolMu.Lock()
	if len(u.accounts) == 0 && account == u {
		if u.cooldownReason[account] == cooldownReasonInsufficientCredits || (u.cooldownReason[account] == "" && u.cooldownUntil[account].After(time.Now())) {
			delete(u.cooldownUntil, account)
			delete(u.cooldownReason, account)
		}
		u.poolMu.Unlock()
		return
	}
	for _, current := range u.accounts {
		if current != account {
			continue
		}
		if u.cooldownReason[account] != cooldownReasonInsufficientCredits {
			break
		}
		delete(u.cooldownUntil, account)
		delete(u.cooldownReason, account)
		changed = true
		enabled = current.enabled
		recorder = u.accountStateRecorder
		break
	}
	u.poolMu.Unlock()
	if changed && recorder != nil {
		if err := recorder(account.accountID, enabled, nil, ""); err != nil {
			log.Printf("[account] clear credit cooldown for account %s failed: %v", account.accountID, err)
		}
	}
}

// CalibrateCreditCooldowns checks balances independently of the admin page.
func (u *Upstream) CalibrateCreditCooldowns(ctx context.Context) {
	u.recheckExpiredCreditCooldowns(ctx)
}

func (u *Upstream) recheckExpiredCreditCooldowns(ctx context.Context) {
	accounts := u.accountsSnapshot()
	if !u.poolMode {
		accounts = []*Upstream{u}
	}
	for _, account := range accounts {
		if ctx.Err() != nil {
			return
		}
		if !account.creditCheckMu.TryLock() {
			continue
		}
		u.poolMu.Lock()
		until, exists := u.cooldownUntil[account]
		due := exists && !until.After(time.Now()) && normalizeCooldownReason(u.cooldownReason[account]) == cooldownReasonInsufficientCredits
		u.poolMu.Unlock()
		if due && (!u.poolMode || account.enabled) {
			// Keep the account quarantined even if the balance endpoint fails.
			u.coolDown(account)
			account.refreshCreditBalance(ctx)
		}
		account.creditCheckMu.Unlock()
	}
}

func (u *Upstream) refreshCreditBalance(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := u.ensureLogin(ctx); err != nil {
		return
	}
	_, _ = u.checkinOnce(ctx, http.MethodGet, strings.TrimRight(u.cfg.Upstream.BaseURL, "/"), true)
}

func (u *Upstream) applyCreditBalance(balance int64) {
	owner := u.restrictionOwner()
	if balance > insufficientCreditsThreshold {
		owner.clearCreditCooldown(u)
	} else {
		owner.coolDown(u)
	}
}

func (u *Upstream) handleCreditRejection(ctx context.Context, account *Upstream) {
	account.creditCheckMu.Lock()
	defer account.creditCheckMu.Unlock()
	u.coolDown(account)
	account.refreshCreditBalance(ctx)
}

func normalizeCooldownReason(reason string) string {
	switch strings.TrimSpace(reason) {
	case cooldownReasonTemporaryBan:
		return cooldownReasonTemporaryBan
	case cooldownReasonInsufficientCredits:
		return cooldownReasonInsufficientCredits
	default:
		// Keep legacy records usable; a newly observed restriction overwrites
		// this value with temporary_ban.
		return cooldownReasonInsufficientCredits
	}
}

func (u *Upstream) restrictionOwner() *Upstream {
	if u.owner != nil {
		return u.owner
	}
	return u
}

// applyAccountRetryError turns a restriction response into account state. The
// parent pool owns the cooldown map even when the HTTP request ran on a child
// account session.
func (u *Upstream) applyAccountRetryError(account *Upstream, err error) bool {
	var retryable *safeAccountRetryError
	if !errors.As(err, &retryable) {
		return false
	}
	owner := u.restrictionOwner()
	if retryable.cooldownUntil != nil {
		owner.coolDownUntil(account, *retryable.cooldownUntil, cooldownReasonTemporaryBan)
		return true
	}
	if retryable.disableAccount {
		owner.disableAccount(account)
		return true
	}
	return false
}

func (u *Upstream) coolDown(account *Upstream) {
	u.coolDownUntil(account, time.Now().Add(insufficientCreditsCooldown), cooldownReasonInsufficientCredits)
}

func (u *Upstream) coolDownUntil(account *Upstream, until time.Time, reason string) {
	if !until.After(time.Now()) {
		return
	}
	until = until.UTC()
	reason = normalizeCooldownReason(reason)
	var recorder func(string, bool, *time.Time, string) error
	changed := false
	enabled := false
	effectiveUntil := until
	effectiveReason := reason
	u.poolMu.Lock()
	if u.cooldownUntil == nil {
		u.cooldownUntil = make(map[*Upstream]time.Time)
	}
	if u.cooldownReason == nil {
		u.cooldownReason = make(map[*Upstream]string)
	}
	if len(u.accounts) == 0 && account == u {
		previous := u.cooldownUntil[account]
		previousReason := u.cooldownReason[account]
		if previousReason == "" || !previous.After(time.Now()) {
			previousReason = cooldownReasonInsufficientCredits
		}
		if until.After(previous) {
			u.cooldownUntil[account] = until
			effectiveUntil = until
		}
		if reason == cooldownReasonTemporaryBan || previousReason != cooldownReasonTemporaryBan {
			u.cooldownReason[account] = reason
		} else {
			u.cooldownReason[account] = previousReason
		}
		effectiveReason = normalizeCooldownReason(u.cooldownReason[account])
		changed = until.After(previous) || effectiveReason != previousReason
		u.poolMu.Unlock()
		return
	}
	for _, current := range u.accounts {
		if current == account {
			previous := u.cooldownUntil[account]
			previousReason := u.cooldownReason[account]
			if previousReason == "" || !previous.After(time.Now()) {
				previousReason = cooldownReasonInsufficientCredits
			}
			if until.After(previous) {
				u.cooldownUntil[account] = until
				effectiveUntil = until
			} else {
				effectiveUntil = previous
			}
			if reason == cooldownReasonTemporaryBan || previousReason != cooldownReasonTemporaryBan {
				u.cooldownReason[account] = reason
			} else {
				u.cooldownReason[account] = previousReason
			}
			effectiveReason = normalizeCooldownReason(u.cooldownReason[account])
			changed = until.After(previous) || effectiveReason != previousReason
			if changed {
				enabled = current.enabled
				recorder = u.accountStateRecorder
			}
			break
		}
	}
	u.poolMu.Unlock()
	if changed && recorder != nil {
		if err := recorder(account.accountID, enabled, &effectiveUntil, effectiveReason); err != nil {
			log.Printf("[account] persist cooldown for account %s failed: %v", account.accountID, err)
		}
	}
}

func (u *Upstream) disableAccount(account *Upstream) {
	var recorder func(string, bool, *time.Time, string) error
	changed := false
	u.poolMu.Lock()
	for _, current := range u.accounts {
		if current == account {
			if current.enabled {
				changed = true
			}
			current.enabled = false
			delete(u.cooldownUntil, current)
			delete(u.cooldownReason, current)
			recorder = u.accountStateRecorder
			break
		}
	}
	u.poolMu.Unlock()
	if changed && recorder != nil {
		if err := recorder(account.accountID, false, nil, ""); err != nil {
			log.Printf("[account] persist disabled state for account %s failed: %v", account.accountID, err)
		}
	}
}

func (u *Upstream) forwardSingle(ctx context.Context, w http.ResponseWriter, request openai.Request) (error, bool, *bufferedResponse) {
	host := strings.TrimRight(u.cfg.Upstream.BaseURL, "/")
	openAIBaseURL := u.OpenAIBaseURL()

	// 首次/未登录先登录
	if err := u.ensureLogin(ctx); err != nil {
		return wrapSafeAccountRetryError("ensure login", err), false, nil
	}

	doForward := func() (*http.Response, error) {
		if err := consumeAttempt(ctx); err != nil {
			return nil, err
		}
		route := imageRoute{targetURL: openAIBaseURL + request.Path}
		if isImageRequest(request.Path) {
			var err error
			route, err = u.allocateImageRoute(ctx, host, openAIBaseURL, request)
			if err != nil {
				return nil, err
			}
		}

		req, err := http.NewRequestWithContext(ctx, request.Method, route.targetURL, bytes.NewReader(request.Body))
		if err != nil {
			return nil, err
		}
		if request.ContentType != "" {
			req.Header.Set("Content-Type", request.ContentType)
		}
		u.applyHeaders(req, host)
		if route.ticket != "" {
			req.Header.Set("X-Qx-Compute-Ticket", route.ticket)
			req.Header.Set("X-Qx-Request-Id", route.requestID)
			req.Header.Set("Idempotency-Key", route.requestID)
		}
		return timedDo(u.httpClient, req)
	}

	forwardWithRouteRetry := func() (*http.Response, error) {
		for attempt := 0; attempt < 2; attempt++ {
			resp, err := doForward()
			if err != nil {
				return nil, err
			}
			if resp.Header.Get("X-Qx-Route-Retry") != "1" || attempt == 1 {
				return resp, nil
			}
			_ = resp.Body.Close()
		}
		return nil, errors.New("image route retry exhausted")
	}

	resp, err := forwardWithRouteRetry()
	if err != nil {
		return wrapSafeAccountRetryError("forward request", err), false, nil
	}

	// 401 自动重新登录后重试一次
	if resp.StatusCode == http.StatusUnauthorized && u.cfg.Auth.AutoRelogin {
		_ = resp.Body.Close()
		// 标记失效
		u.mu.Lock()
		u.loggedIn = false
		u.mu.Unlock()

		if err := u.Login(ctx); err != nil {
			return wrapSafeAccountRetryError("relogin on 401", err), false, nil
		}
		// 再转一次
		resp, err = forwardWithRouteRetry()
		if err != nil {
			return wrapSafeAccountRetryError("forward after relogin", err), false, nil
		}
	}

	// 把上游响应原样回写给客户端（含流式 SSE）
	streaming := strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
	if !streaming {
		body, readErr := readResponse(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return fmt.Errorf("read upstream body: %w", readErr), false, nil
		}
		if restriction, ok := detectAccountRestriction(resp.StatusCode, body); ok {
			return &safeAccountRetryError{
				err:            fmt.Errorf("upstream account restricted: status=%d", resp.StatusCode),
				response:       &bufferedResponse{header: resp.Header.Clone(), status: resp.StatusCode, body: body},
				cooldownUntil:  restriction.cooldownUntil,
				disableAccount: restriction.disableAccount,
			}, false, nil
		}
		if insufficientCredits(resp.StatusCode, body) {
			return nil, false, &bufferedResponse{header: resp.Header.Clone(), status: resp.StatusCode, body: body}
		}
		admission.ReportUsage(ctx, body)
		if retryableUpstreamStatus(resp.StatusCode) {
			if resp.StatusCode == http.StatusTooManyRequests {
				u.recordModelCooldown(request.Model, resp.Header.Get("Retry-After"))
			}
			return &safeAccountRetryError{
				err:      fmt.Errorf("upstream returned retryable status %d", resp.StatusCode),
				response: &bufferedResponse{header: resp.Header.Clone(), status: resp.StatusCode, body: body},
			}, false, nil
		}
		// 上游生图接口返回的图片地址是站内相对路径（如
		// /api/generated-images/xxx），转发前改写成绝对地址，否则客户端拿到
		// 相对路径无法下载，表现为“生图成功但取不到图”。
		if rewritten, changed := httpx.RewriteJSONImageURLs(body, u.imageBaseURL()); changed {
			body = rewritten
		}
		if openai.IsMapped(request) {
			body = openai.ResponseModel(body, request.PublicModel)
		}
		httpx.CopyHeaders(w.Header(), resp.Header)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(resp.StatusCode)
		if _, err := w.Write(body); err != nil {
			return fmt.Errorf("write upstream response: %w", err), true, nil
		}
		if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
			u.recordLocalCredit(request)
		}
		return nil, true, nil
	}

	streamUsage := &usageBody{ReadCloser: resp.Body, ctx: ctx}
	resp.Body = streamUsage
	reader := bufio.NewReader(resp.Body)
	if resp.StatusCode == http.StatusPaymentRequired {
		streamUsage.discard = true
		body, readErr := readResponse(reader)
		_ = resp.Body.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return fmt.Errorf("read upstream body: %w", readErr), false, nil
		}
		return nil, false, &bufferedResponse{header: resp.Header.Clone(), status: resp.StatusCode, body: body}
	}
	prefix, prefixErr := readSSEPrefix(reader)
	if prefixErr != nil && !errors.Is(prefixErr, io.EOF) {
		_ = resp.Body.Close()
		return fmt.Errorf("read upstream stream: %w", prefixErr), false, nil
	}
	if restriction, ok := detectAccountRestriction(resp.StatusCode, prefix); ok {
		body, readErr := readResponse(reader)
		_ = resp.Body.Close()
		if readErr != nil {
			return readErr, false, nil
		}
		return &safeAccountRetryError{
			err:           fmt.Errorf("upstream account restricted: status=%d", resp.StatusCode),
			response:      &bufferedResponse{header: resp.Header.Clone(), status: resp.StatusCode, body: append(prefix, body...)},
			cooldownUntil: restriction.cooldownUntil, disableAccount: restriction.disableAccount,
		}, false, nil
	}
	if insufficientCredits(resp.StatusCode, prefix) {
		streamUsage.discard = true
		body, readErr := readResponse(reader)
		_ = resp.Body.Close()
		if readErr != nil {
			return readErr, false, nil
		}
		return nil, false, &bufferedResponse{header: resp.Header.Clone(), status: resp.StatusCode, body: append(prefix, body...)}
	}
	if retryableUpstreamStatus(resp.StatusCode) {
		if resp.StatusCode == http.StatusTooManyRequests {
			u.recordModelCooldown(request.Model, resp.Header.Get("Retry-After"))
		}
		body, readErr := readResponse(reader)
		_ = resp.Body.Close()
		if readErr != nil {
			return readErr, false, nil
		}
		return &safeAccountRetryError{
			err: fmt.Errorf("upstream returned retryable status %d", resp.StatusCode),
			response: &bufferedResponse{
				header: resp.Header.Clone(), status: resp.StatusCode,
				body: append(prefix, body...),
			},
		}, false, nil
	}
	httpx.CopyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	writer := io.Writer(w)
	if flusher, ok := w.(http.Flusher); ok {
		writer = flushingWriter{writer: w, flusher: flusher}
	}
	if openai.IsMapped(request) {
		err = openai.CopyModelStream(writer, io.MultiReader(bytes.NewReader(prefix), reader), request.PublicModel)
	} else {
		if len(prefix) > 0 {
			if _, err := writer.Write(prefix); err != nil {
				_ = resp.Body.Close()
				return fmt.Errorf("write upstream stream: %w", err), true, nil
			}
		}
		_, err = io.Copy(writer, reader)
	}
	_ = resp.Body.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("copy upstream stream: %w", err), true, nil
	}
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		u.recordLocalCredit(request)
	}
	return nil, true, nil
}

func readSSEPrefix(reader *bufio.Reader) ([]byte, error) {
	var prefix bytes.Buffer
	hasData := false
	for {
		line, err := reader.ReadString('\n')
		_, _ = prefix.WriteString(line)
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "data:") {
			hasData = true
		}
		if strings.TrimSpace(line) == "" && hasData {
			return prefix.Bytes(), nil
		}
		if err != nil {
			return prefix.Bytes(), err
		}
	}
}

func insufficientCredits(status int, body []byte) bool {
	if status == http.StatusPaymentRequired {
		return true
	}
	if status >= 200 && status < 300 {
		payload := bytes.TrimSpace(body)
		for _, line := range bytes.Split(payload, []byte("\n")) {
			if bytes.HasPrefix(line, []byte("data:")) {
				payload = bytes.TrimSpace(line[5:])
				break
			}
		}
		var envelope struct {
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal(payload, &envelope) != nil || len(envelope.Error) == 0 || string(envelope.Error) == "null" {
			return false
		}
	}
	message := strings.ToLower(string(body))
	for _, marker := range []string{
		"insufficient_credit", "insufficient credit", "insufficient balance", "insufficient points",
		"insufficient_quota", "not enough credits", "not enough points", "out of credits", "out of credit",
		"credit exhausted", "quota exceeded", "no credits", "no points",
		"积分不足", "点数不足", "积分用完", "积分已用完", "积分耗尽", "积分为0", "没有可用积分",
		"余额不足", "余额用完", "额度不足", "可用额度不足",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	if strings.Contains(message, "insufficient") && (strings.Contains(message, "credit") ||
		strings.Contains(message, "balance") || strings.Contains(message, "point") || strings.Contains(message, "quota")) {
		return true
	}
	return false
}

type accountRestriction struct {
	cooldownUntil  *time.Time
	disableAccount bool
}

func detectAccountRestriction(status int, body []byte) (accountRestriction, bool) {
	var payload map[string]json.RawMessage
	_ = json.Unmarshal(body, &payload)

	readString := func(values map[string]json.RawMessage, keys ...string) string {
		for _, key := range keys {
			raw, ok := values[key]
			if !ok {
				continue
			}
			var value string
			if json.Unmarshal(raw, &value) == nil && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
			var number json.Number
			if json.Unmarshal(raw, &number) == nil && strings.TrimSpace(number.String()) != "" {
				return number.String()
			}
		}
		return ""
	}

	code := readString(payload, "code", "errorCode", "error_code")
	message := readString(payload, "message", "detail")
	untilText := readString(payload, "bannedUntil", "banned_until", "banUntil", "ban_until", "blockedUntil", "blocked_until", "suspendedUntil", "suspended_until", "restrictedUntil", "restricted_until", "restrictionUntil", "restriction_until")
	if rawError, ok := payload["error"]; ok {
		var nested map[string]json.RawMessage
		if json.Unmarshal(rawError, &nested) == nil {
			if code == "" {
				code = readString(nested, "code", "errorCode", "error_code")
			}
			if message == "" {
				message = readString(nested, "message", "detail")
			}
			if untilText == "" {
				untilText = readString(nested, "bannedUntil", "banned_until", "banUntil", "ban_until", "blockedUntil", "blocked_until", "suspendedUntil", "suspended_until", "restrictedUntil", "restricted_until", "restrictionUntil", "restriction_until")
			}
		} else if message == "" {
			_ = json.Unmarshal(rawError, &message)
		}
	}

	lowerCode := strings.ToLower(strings.TrimSpace(code))
	lowerMessage := strings.ToLower(strings.TrimSpace(message))
	bodyText := strings.ToLower(string(body))
	matched := accountRestrictionCode(lowerCode) || accountRestrictionMessage(lowerMessage)
	if !matched && status == http.StatusForbidden {
		matched = strings.Contains(bodyText, "banneduntil") || strings.Contains(bodyText, "account_restricted")
	}
	if !matched {
		return accountRestriction{}, false
	}

	if until, ok := parseRestrictionTime(untilText); ok && until.After(time.Now()) {
		return accountRestriction{cooldownUntil: &until}, true
	}
	return accountRestriction{disableAccount: true}, true
}

func accountRestrictionCode(code string) bool {
	if code == "" {
		return false
	}
	for _, marker := range []string{
		"account_restricted", "account_banned", "account_suspended", "account_blocked", "account_disabled",
		"account_ban", "account_suspend", "user_banned", "user_suspended", "forbidden_account",
		"banned", "suspended", "blocked", "temporary_ban", "temporarily_banned", "temp_ban",
	} {
		if code == marker {
			return true
		}
	}
	return strings.Contains(code, "account") && (strings.Contains(code, "ban") ||
		strings.Contains(code, "suspend") || strings.Contains(code, "restrict") || strings.Contains(code, "block"))
}

func accountRestrictionMessage(message string) bool {
	if message == "" {
		return false
	}
	englishAccount := strings.Contains(message, "account") && (strings.Contains(message, "ban") ||
		strings.Contains(message, "suspend") || strings.Contains(message, "restrict") || strings.Contains(message, "block"))
	chineseAccount := strings.Contains(message, "账号") && (strings.Contains(message, "封") ||
		strings.Contains(message, "限制") || strings.Contains(message, "冻结") || strings.Contains(message, "停用"))
	return englishAccount || chineseAccount
}

func parseRestrictionTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	if unix, err := strconv.ParseInt(value, 10, 64); err == nil {
		if unix > 100000000000 {
			unix /= 1000
		}
		parsed := time.Unix(unix, 0)
		return parsed, !parsed.IsZero()
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, true
		}
		if parsed, err := time.ParseInLocation(layout, value, time.UTC); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func retryableUpstreamStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooEarly ||
		status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

type flushingWriter struct {
	writer  io.Writer
	flusher http.Flusher
}

func (w flushingWriter) Write(data []byte) (int, error) {
	n, err := w.writer.Write(data)
	w.flusher.Flush()
	return n, err
}

// CheckinResult 解析 /api/checkin 的响应字段，用于日志和幂等判断。
type CheckinResult struct {
	Enabled        bool   `json:"enabled"`
	CheckedIn      bool   `json:"checkedIn"`
	Reward         int64  `json:"reward"`
	Date           string `json:"date"`
	Balance        int64  `json:"balance"`
	BalanceKnown   bool   `json:"-"`
	ValidityDays   int64  `json:"validityDays"`
	CurrentStreak  int64  `json:"currentStreak"`
	MakeupChances  int64  `json:"makeupChances"`
	AlreadyChecked bool   `json:"-"` // 本次调用前就已签到
}

// Checkin 执行一次签到。上游接口分两步：
//  1. GET  /api/checkin?month=YYYY-MM  查询当天状态
//  2. POST /api/checkin                真正签到
//
// 只发 GET 永远不会签到成功，这是此前“签到疑似失效”的根因。
func (u *Upstream) Checkin(ctx context.Context) (CheckinResult, error) {
	accounts := u.accountsSnapshot()
	if len(accounts) > 0 {
		var result CheckinResult
		var checkinErrors []error
		gotResult := false
		for i, account := range accounts {
			if !account.enabled {
				continue
			}
			accountResult, err := account.Checkin(ctx)
			if err != nil {
				checkinErrors = append(checkinErrors, fmt.Errorf("account %d: %w", i+1, err))
				continue
			}
			if !gotResult {
				result = accountResult
				gotResult = true
			}
		}
		if len(checkinErrors) > 0 {
			return result, errors.Join(checkinErrors...)
		}
		if !gotResult {
			return result, errors.New("all upstream accounts are disabled")
		}
		return result, nil
	}

	host := strings.TrimRight(u.cfg.Upstream.BaseURL, "/")
	var res CheckinResult

	if err := u.ensureLogin(ctx); err != nil {
		return res, fmt.Errorf("ensure login: %w", err)
	}

	// 第一步：查询状态。已签到则直接返回，避免重复请求。
	state, err := u.checkinOnce(ctx, http.MethodGet, host, true)
	if err != nil {
		return res, err
	}
	if state.Enabled == false && state.CheckedIn == false && state.Date == "" {
		return state, fmt.Errorf("checkin state unavailable (empty response)")
	}
	if state.CheckedIn {
		state.AlreadyChecked = true
		return state, nil
	}
	if !state.Enabled {
		return state, fmt.Errorf("checkin is disabled by upstream")
	}

	// 第二步：真正签到。
	fresh, err := u.checkinOnce(ctx, http.MethodPost, host, false)
	if err != nil {
		return res, err
	}
	// POST 响应字段更全（validityDays / currentStreak 等），保留状态里的余额兜底。
	if fresh.Balance == 0 && state.Balance != 0 {
		fresh.Balance = state.Balance
	}
	if fresh.Date == "" {
		fresh.Date = state.Date
	}
	if !fresh.CheckedIn && fresh.Reward == 0 {
		return fresh, fmt.Errorf("checkin returned no reward")
	}
	return fresh, nil
}

// CheckinAccount performs the two-step check-in flow for one configured account.
func (u *Upstream) CheckinAccount(ctx context.Context, accountID string) (CheckinResult, error) {
	accounts := u.accountsSnapshot()
	if len(accounts) == 0 {
		if strings.TrimSpace(u.cfg.Auth.Identifier) == "" {
			return CheckinResult{}, errors.New("未配置上游账号")
		}
		if accountID != "" && accountID != u.accountID {
			return CheckinResult{}, errors.New("账号不存在")
		}
		return u.Checkin(ctx)
	}
	for _, account := range accounts {
		if account.accountID != accountID {
			continue
		}
		if !account.enabled {
			return CheckinResult{}, errors.New("账号已停用")
		}
		result, err := account.Checkin(ctx)
		return result, err
	}
	return CheckinResult{}, errors.New("账号不存在")
}

// checkinOnce 发一次签到相关请求；withMonth=true 时附带 month 参数（查询用）。
// 401 时自动重登并重试一次。
func (u *Upstream) checkinOnce(ctx context.Context, method, host string, withMonth bool) (CheckinResult, error) {
	var res CheckinResult
	path := u.cfg.Checkin.Path
	if withMonth {
		path += "?month=" + time.Now().In(u.checkinLocation()).Format("2006-01")
	}
	target := host + path

	do := func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, method, target, nil)
		if err != nil {
			return nil, err
		}
		u.applyHeaders(req, host)
		req.Header.Set("Referer", host+"/#/checkin")
		return u.loginClient.Do(req)
	}

	resp, err := do()
	if err != nil {
		return res, fmt.Errorf("checkin request: %w", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	_ = resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized && u.cfg.Auth.AutoRelogin {
		u.mu.Lock()
		u.loggedIn = false
		u.mu.Unlock()
		if loginErr := u.Login(ctx); loginErr != nil {
			return res, fmt.Errorf("relogin on 401: %w", loginErr)
		}
		resp, err = do()
		if err != nil {
			return res, fmt.Errorf("checkin retry after relogin: %w", err)
		}
		body, _ = io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		_ = resp.Body.Close()
	}

	if resp.StatusCode/100 != 2 {
		if restriction, ok := detectAccountRestriction(resp.StatusCode, body); ok {
			retryable := &safeAccountRetryError{
				err:            fmt.Errorf("checkin %s failed: status=%d", method, resp.StatusCode),
				cooldownUntil:  restriction.cooldownUntil,
				disableAccount: restriction.disableAccount,
			}
			u.restrictionOwner().applyAccountRetryError(u, retryable)
			return res, retryable
		}
		return res, fmt.Errorf("checkin %s failed: status=%d", method, resp.StatusCode)
	}
	if restriction, ok := detectAccountRestriction(resp.StatusCode, body); ok {
		retryable := &safeAccountRetryError{
			err:            fmt.Errorf("checkin %s rejected by upstream", method),
			cooldownUntil:  restriction.cooldownUntil,
			disableAccount: restriction.disableAccount,
		}
		u.restrictionOwner().applyAccountRetryError(u, retryable)
		return res, retryable
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return res, fmt.Errorf("decode checkin response: %w", err)
	}
	var balanceField struct {
		Balance      *int64 `json:"balance"`
		BalanceAfter *int64 `json:"balanceAfter"`
		Account      struct {
			Balance      *int64 `json:"balance"`
			BalanceAfter *int64 `json:"balanceAfter"`
		} `json:"account"`
	}
	if err := json.Unmarshal(body, &balanceField); err != nil {
		return res, fmt.Errorf("decode checkin balance: %w", err)
	}
	balance := balanceField.Balance
	if balance == nil {
		balance = balanceField.BalanceAfter
	}
	if balance == nil {
		balance = balanceField.Account.Balance
	}
	if balance == nil {
		balance = balanceField.Account.BalanceAfter
	}
	if balance != nil {
		res.Balance = *balance
	}
	res.BalanceKnown = balance != nil
	if res.BalanceKnown {
		u.applyCreditBalance(res.Balance)
	}
	return res, nil
}

// checkinLocation 返回签到时区，非法时回退到 Asia/Shanghai。
func (u *Upstream) checkinLocation() *time.Location {
	if loc, err := time.LoadLocation(u.cfg.Checkin.Timezone); err == nil {
		return loc
	}
	return time.FixedZone("CST", 8*3600)
}

// imageBaseURL 返回用于改写生图响应里相对图片地址的基础域名。
// 上游返回形如 /api/generated-images/xxx 的站内路径，客户端拿到相对路径
// 无法下载，这里补成上游的绝对地址。
func (u *Upstream) imageBaseURL() string {
	return strings.TrimRight(strings.TrimSpace(u.cfg.Upstream.BaseURL), "/")
}
