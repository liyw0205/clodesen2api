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
	"time"

	"clodesen2api/internal/config"
	"clodesen2api/internal/httpx"
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

	accounts       []*Upstream
	strategy       string
	poolMu         sync.Mutex
	nextAccount    int
	cooldownUntil  map[*Upstream]time.Time
	creditMu       sync.RWMutex
	creditRecorder func(CreditUsageRecord)
	creditPricing  map[string]int64
}

const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

const insufficientCreditsCooldown = 30 * time.Minute

// New 构造上游客户端。cookiejar 用来自动保存 set-cookie。
func New(cfg *config.Config) (*Upstream, error) {
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
	return NewWithAccounts(cfg, accounts)
}

// AccountCredential is the persisted login data for one upstream account.
type AccountCredential struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Password   string `json:"password"`
	Enabled    bool   `json:"enabled"`
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
	strategy := strings.ToLower(strings.TrimSpace(cfg.Auth.Strategy))
	if strategy == "" {
		strategy = "round_robin"
	}
	if strategy != "round_robin" && strategy != "random" {
		return nil, fmt.Errorf("invalid auth.strategy %q (want round_robin or random)", cfg.Auth.Strategy)
	}

	pool := &Upstream{
		cfg:           cfg,
		strategy:      strategy,
		cooldownUntil: make(map[*Upstream]time.Time),
		creditPricing: make(map[string]int64),
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
		account.creditPricing = make(map[string]int64)
		pool.accounts = append(pool.accounts, account)
	}
	if len(pool.accounts) == 0 {
		return nil, errors.New("at least one upstream account is required")
	}
	return pool, nil
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
			cfg:         cfg,
			httpClient:  &http.Client{Transport: transport, Jar: jar, Timeout: mainTimeout},
			loginClient: &http.Client{Transport: transport, Jar: jar, Timeout: cfg.Upstream.LoginTimeout},
		}, nil
	}
	// request_timeout=0 表示不限超时（流式）
	return &Upstream{
		cfg:         cfg,
		httpClient:  &http.Client{Transport: transport, Jar: jar},
		loginClient: &http.Client{Transport: transport, Jar: jar, Timeout: cfg.Upstream.LoginTimeout},
	}, nil
}

// AccountCount 返回配置的上游账号数。
func (u *Upstream) AccountCount() int {
	u.poolMu.Lock()
	defer u.poolMu.Unlock()
	if len(u.accounts) > 0 {
		return len(u.accounts)
	}
	return 1
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
			if !account.enabled {
				continue
			}
			if err := account.Login(ctx); err != nil {
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
		return fmt.Errorf("login failed: upstream status %d, body=%s", resp.StatusCode, string(respBody))
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
	if resp.StatusCode/100 != 2 {
		return imageRoute{}, fmt.Errorf("image route failed: upstream status %d, body=%s", resp.StatusCode, string(body))
	}

	var route computeRouteResponse
	if err := json.Unmarshal(body, &route); err != nil {
		return imageRoute{}, fmt.Errorf("decode image route response: %w (body=%s)", err, string(body))
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
	if len(u.accountsSnapshot()) == 0 {
		err, wrote, insufficient := u.forwardSingle(ctx, w, request)
		if insufficient != nil {
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

// NoAccountAvailableError is returned while every configured account is cooling down.
type NoAccountAvailableError struct {
	RetryAfter time.Duration
}

func (e *NoAccountAvailableError) Error() string {
	return "all upstream accounts are cooling down"
}

type safeAccountRetryError struct {
	err error
}

func (e *safeAccountRetryError) Error() string { return e.err.Error() }
func (e *safeAccountRetryError) Unwrap() error { return e.err }

func (u *Upstream) forwardPool(ctx context.Context, w http.ResponseWriter, request openai.Request) (error, bool) {
	candidates, retryAfter := u.selectAccounts()
	if len(candidates) == 0 {
		return &NoAccountAvailableError{RetryAfter: retryAfter}, false
	}

	var insufficient *bufferedResponse
	var lastErr error
	attempted := 0
	for _, candidate := range candidates {
		if !u.accountAvailable(candidate.account) {
			continue
		}
		attempted++
		err, wrote, creditResponse := candidate.account.forwardSingle(ctx, w, request)
		if creditResponse != nil {
			insufficient = creditResponse
			u.coolDown(candidate.account)
			log.Printf("[account] account %d exhausted credits; cooling down for %s", candidate.index+1, insufficientCreditsCooldown)
			continue
		}
		if err != nil {
			lastErr = err
			var retryable *safeAccountRetryError
			if errors.As(err, &retryable) {
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
	if attempted == 0 {
		_, retryAfter = u.selectAccounts()
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
	var earliest time.Time
	for i, account := range u.accounts {
		if !account.enabled {
			continue
		}
		until := u.cooldownUntil[account]
		if until.After(now) {
			if earliest.IsZero() || until.Before(earliest) {
				earliest = until
			}
			continue
		}
		candidates = append(candidates, accountCandidate{index: i, account: account})
	}
	if len(candidates) == 0 {
		if earliest.IsZero() {
			return nil, 0
		}
		return nil, time.Until(earliest)
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
	u.nextAccount = (u.nextAccount + 1) % len(u.accounts)
	ordered := make([]accountCandidate, 0, len(candidates))
	for offset := 0; offset < len(u.accounts); offset++ {
		index := (start + offset) % len(u.accounts)
		if !u.accounts[index].enabled {
			continue
		}
		if until := u.cooldownUntil[u.accounts[index]]; until.After(now) {
			continue
		}
		ordered = append(ordered, accountCandidate{index: index, account: u.accounts[index]})
	}
	return ordered, 0
}

func (u *Upstream) accountAvailable(account *Upstream) bool {
	u.poolMu.Lock()
	defer u.poolMu.Unlock()
	return !u.cooldownUntil[account].After(time.Now())
}

func (u *Upstream) coolDown(account *Upstream) {
	u.poolMu.Lock()
	for _, current := range u.accounts {
		if current == account {
			u.cooldownUntil[account] = time.Now().Add(insufficientCreditsCooldown)
			break
		}
	}
	u.poolMu.Unlock()
}

func (u *Upstream) forwardSingle(ctx context.Context, w http.ResponseWriter, request openai.Request) (error, bool, *bufferedResponse) {
	host := strings.TrimRight(u.cfg.Upstream.BaseURL, "/")
	openAIBaseURL := u.OpenAIBaseURL()

	// 首次/未登录先登录
	if err := u.ensureLogin(ctx); err != nil {
		return fmt.Errorf("ensure login: %w", &safeAccountRetryError{err: err}), false, nil
	}

	doForward := func() (*http.Response, error) {
		route := imageRoute{targetURL: openAIBaseURL + request.Path}
		if isImageRequest(request.Path) {
			var err error
			route, err = u.allocateImageRoute(ctx, host, openAIBaseURL, request)
			if err != nil {
				return nil, &safeAccountRetryError{err: err}
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
		return u.httpClient.Do(req)
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
		return fmt.Errorf("forward request: %w", err), false, nil
	}

	// 401 自动重新登录后重试一次
	if resp.StatusCode == http.StatusUnauthorized && u.cfg.Auth.AutoRelogin {
		_ = resp.Body.Close()
		// 标记失效
		u.mu.Lock()
		u.loggedIn = false
		u.mu.Unlock()

		if err := u.Login(ctx); err != nil {
			return fmt.Errorf("relogin on 401: %w", &safeAccountRetryError{err: err}), false, nil
		}
		// 再转一次
		resp, err = forwardWithRouteRetry()
		if err != nil {
			return fmt.Errorf("forward after relogin: %w", err), false, nil
		}
	}

	// 把上游响应原样回写给客户端（含流式 SSE）
	streaming := strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
	if !streaming {
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return fmt.Errorf("read upstream body: %w", readErr), false, nil
		}
		if insufficientCredits(resp.StatusCode, body) {
			return nil, false, &bufferedResponse{header: resp.Header.Clone(), status: resp.StatusCode, body: body}
		}
		// 上游生图接口返回的图片地址是站内相对路径（如
		// /api/generated-images/xxx），转发前改写成绝对地址，否则客户端拿到
		// 相对路径无法下载，表现为“生图成功但取不到图”。
		if rewritten, changed := httpx.RewriteJSONImageURLs(body, u.imageBaseURL()); changed {
			body = rewritten
		}
		httpx.CopyHeaders(w.Header(), resp.Header)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
			u.recordLocalCredit(request)
		}
		return nil, true, nil
	}

	reader := bufio.NewReader(resp.Body)
	if resp.StatusCode == http.StatusPaymentRequired {
		body, readErr := io.ReadAll(reader)
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
	if insufficientCredits(resp.StatusCode, prefix) {
		body, _ := io.ReadAll(reader)
		_ = resp.Body.Close()
		return nil, false, &bufferedResponse{
			header: resp.Header.Clone(), status: resp.StatusCode,
			body: append(prefix, body...),
		}
	}
	httpx.CopyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	writer := io.Writer(w)
	if flusher, ok := w.(http.Flusher); ok {
		writer = flushingWriter{writer: w, flusher: flusher}
	}
	if len(prefix) > 0 {
		if _, err := writer.Write(prefix); err != nil {
			_ = resp.Body.Close()
			return nil, true, nil
		}
	}
	_, err = io.Copy(writer, resp.Body)
	_ = resp.Body.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		// 客户端断开之类，不再处理
		return nil, true, nil
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
		return res, fmt.Errorf("checkin %s failed: status=%d body=%s", method, resp.StatusCode, string(body))
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return res, fmt.Errorf("decode checkin response: %w (body=%s)", err, string(body))
	}
	var balanceField struct {
		Balance *int64 `json:"balance"`
	}
	if err := json.Unmarshal(body, &balanceField); err != nil {
		return res, fmt.Errorf("decode checkin balance: %w", err)
	}
	res.BalanceKnown = balanceField.Balance != nil
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
