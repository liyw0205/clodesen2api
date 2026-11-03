package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type AccountStatus struct {
	ID                  string     `json:"id"`
	Identifier          string     `json:"identifier"`
	Enabled             bool       `json:"enabled"`
	RPMUsed             int        `json:"rpmUsed"`
	RPMLimit            int        `json:"rpmLimit"`
	CooldownUntil       *time.Time `json:"cooldownUntil,omitempty"`
	CooldownReason      string     `json:"cooldownReason,omitempty"`
	Balance             *int64     `json:"balance,omitempty"`
	CheckedIn           *bool      `json:"checkedIn,omitempty"`
	RedemptionEnabled   *bool      `json:"redemptionEnabled,omitempty"`
	RedemptionLimit     *int64     `json:"redemptionLimit,omitempty"`
	RedemptionRemaining *int64     `json:"redemptionRemaining,omitempty"`
	ResetAt             string     `json:"resetAt,omitempty"`
	RedemptionResetAt   string     `json:"redemptionResetAt,omitempty"`
	CreditType          string     `json:"creditType,omitempty"`
	ValidityDays        *int64     `json:"validityDays,omitempty"`
	RefreshedAt         time.Time  `json:"refreshedAt"`
	Error               string     `json:"error,omitempty"`
}

type RedemptionResult struct {
	AccountID  string `json:"accountId"`
	Identifier string `json:"identifier"`
	Status     string `json:"status"`
	Message    string `json:"message"`
	Credits    *int64 `json:"credits,omitempty"`
}

// RedeemCodeAll attempts the configured code once for every enabled account.
func (u *Upstream) RedeemCodeAll(ctx context.Context, code string) []RedemptionResult {
	accounts := u.accountsSnapshot()
	if len(accounts) == 0 {
		if strings.TrimSpace(u.cfg.Auth.Identifier) == "" {
			return nil
		}
		accounts = []*Upstream{u}
	}
	results := make([]RedemptionResult, 0, len(accounts))
	for _, account := range accounts {
		if !account.enabled && len(accounts) > 1 {
			continue
		}
		results = append(results, account.redeemOwnCode(ctx, code))
	}
	return results
}

func (u *Upstream) redeemOwnCode(ctx context.Context, code string) RedemptionResult {
	return u.redeemCodeForAccount(ctx, u.accountID, code)
}

// AccountCredentials returns a copy suitable for persistence. Passwords must
// never be serialized into an HTTP response.
func (u *Upstream) AccountCredentials() []AccountCredential {
	accounts := u.accountsSnapshot()
	if len(accounts) == 0 {
		return []AccountCredential{{
			ID:         accountIDFor(u.cfg.Auth.Identifier),
			Identifier: u.cfg.Auth.Identifier,
			Password:   u.cfg.Auth.Password,
			Enabled:    true,
		}}
	}
	credentials := make([]AccountCredential, 0, len(accounts))
	for _, account := range accounts {
		credentials = append(credentials, AccountCredential{
			ID:             account.accountID,
			Identifier:     account.cfg.Auth.Identifier,
			Password:       account.cfg.Auth.Password,
			Enabled:        account.enabled,
			CooldownUntil:  u.cooldownUntilFor(account),
			CooldownReason: u.cooldownReasonFor(account),
		})
	}
	return credentials
}

// ReplaceAccounts prepares replacement sessions before swapping the pool. In
// flight requests keep their old session pointers until they complete.
func (u *Upstream) ReplaceAccounts(credentials []AccountCredential) error {
	prepared := make([]*Upstream, 0, len(credentials))
	seenIDs := make(map[string]struct{}, len(credentials))
	seenIdentifiers := make(map[string]struct{}, len(credentials))
	for _, credential := range credentials {
		credential.Identifier = strings.TrimSpace(credential.Identifier)
		if credential.ID == "" {
			credential.ID = accountIDFor(credential.Identifier)
		}
		if credential.ID == "" || credential.Identifier == "" || strings.TrimSpace(credential.Password) == "" {
			return errors.New("account id, identifier, and password are required")
		}
		if _, exists := seenIDs[credential.ID]; exists {
			return errors.New("duplicate account id")
		}
		key := strings.ToLower(credential.Identifier)
		if _, exists := seenIdentifiers[key]; exists {
			return errors.New("duplicate account identifier")
		}
		seenIDs[credential.ID] = struct{}{}
		seenIdentifiers[key] = struct{}{}
		accountCfg := *u.cfg
		accountCfg.Auth.Accounts = ""
		accountCfg.Auth.Identifier = credential.Identifier
		accountCfg.Auth.Password = credential.Password
		account, err := newSession(&accountCfg)
		if err != nil {
			return err
		}
		account.accountID = credential.ID
		account.enabled = credential.Enabled
		account.owner = u
		u.creditMu.RLock()
		account.creditRecorder = u.creditRecorder
		account.creditPricing = cloneCreditPricing(u.creditPricing)
		account.redemptionRecorder = u.redemptionRecorder
		u.creditMu.RUnlock()
		prepared = append(prepared, account)
	}
	u.poolMu.Lock()
	defer u.poolMu.Unlock()
	fixedAccountID := ""
	if u.fixedAccount != nil {
		fixedAccountID = u.fixedAccount.accountID
	}
	oldCooldown := make(map[string]time.Time, len(u.cooldownUntil))
	oldReason := make(map[string]string, len(u.cooldownReason))
	for account, until := range u.cooldownUntil {
		oldCooldown[account.accountID] = until
		oldReason[account.accountID] = u.cooldownReason[account]
	}
	nextCooldown := make(map[*Upstream]time.Time)
	nextReason := make(map[*Upstream]string)
	for _, account := range prepared {
		if until := oldCooldown[account.accountID]; until.After(time.Now()) {
			nextCooldown[account] = until
			nextReason[account] = normalizeCooldownReason(oldReason[account.accountID])
		}
		for _, credential := range credentials {
			if credential.ID == account.accountID && credential.CooldownUntil != nil && credential.CooldownUntil.After(time.Now()) {
				if previous := nextCooldown[account]; credential.CooldownUntil.After(previous) {
					nextCooldown[account] = credential.CooldownUntil.UTC()
					nextReason[account] = normalizeCooldownReason(credential.CooldownReason)
				} else if nextReason[account] == "" {
					nextReason[account] = normalizeCooldownReason(credential.CooldownReason)
				}
				break
			}
		}
	}
	u.accounts = prepared
	u.fixedAccount = nil
	for _, account := range prepared {
		if account.accountID == fixedAccountID {
			u.fixedAccount = account
			break
		}
	}
	u.cooldownUntil = nextCooldown
	u.cooldownReason = nextReason
	if len(prepared) == 0 {
		u.nextAccount = 0
	} else {
		u.nextAccount %= len(prepared)
	}
	return nil
}

func (u *Upstream) Strategy() string {
	u.poolMu.Lock()
	defer u.poolMu.Unlock()
	return u.strategy
}

func (u *Upstream) SetStrategy(strategy string) error {
	strategy = strings.ToLower(strings.TrimSpace(strategy))
	if strategy != "round_robin" && strategy != "random" && strategy != "fixed" {
		return fmt.Errorf("invalid strategy %q", strategy)
	}
	u.poolMu.Lock()
	u.strategy = strategy
	u.poolMu.Unlock()
	return nil
}

// RPMUsage returns the current in-flight request count by account ID.
func (u *Upstream) RPMUsage() (map[string]int, int) {
	u.poolMu.Lock()
	defer u.poolMu.Unlock()
	limit := configuredRPM(u.rpmLimit)
	accounts := u.accounts
	if len(accounts) == 0 && !u.poolMode && strings.TrimSpace(u.cfg.Auth.Identifier) != "" {
		accounts = []*Upstream{u}
	}
	usage := make(map[string]int, len(accounts))
	for _, account := range accounts {
		usage[account.accountID] = u.rpmInFlight[account.accountID]
	}
	return usage, limit
}

func (u *Upstream) AccountStatuses(ctx context.Context) []AccountStatus {
	accounts := u.accountsSnapshot()
	direct := len(accounts) == 0
	if len(accounts) == 0 {
		if strings.TrimSpace(u.cfg.Auth.Identifier) == "" {
			return nil
		}
		accounts = []*Upstream{u}
	}
	statuses := make([]AccountStatus, len(accounts))
	var cooldowns map[*Upstream]time.Time
	var cooldownReasons map[*Upstream]string
	u.poolMu.Lock()
	cooldowns = make(map[*Upstream]time.Time, len(u.cooldownUntil))
	cooldownReasons = make(map[*Upstream]string, len(u.cooldownReason))
	for account, until := range u.cooldownUntil {
		cooldowns[account] = until
		cooldownReasons[account] = u.cooldownReason[account]
	}
	rpmLimit := configuredRPM(u.rpmLimit)
	rpmUsed := make(map[*Upstream]int, len(accounts))
	for _, account := range accounts {
		rpmUsed[account] = u.rpmInFlight[account.accountID]
	}
	u.poolMu.Unlock()

	var wg sync.WaitGroup
	for i, account := range accounts {
		base := AccountStatus{
			ID:          account.accountID,
			Identifier:  account.cfg.Auth.Identifier,
			Enabled:     direct || account.enabled,
			RPMUsed:     rpmUsed[account],
			RPMLimit:    rpmLimit,
			RefreshedAt: time.Now(),
		}
		if until := cooldowns[account]; until.After(time.Now()) {
			base.CooldownUntil = &until
			base.CooldownReason = normalizeCooldownReason(cooldownReasons[account])
		}
		statuses[i] = base
		wg.Add(1)
		go func(index int, account *Upstream) {
			defer wg.Done()
			statuses[index] = account.refreshAccountStatus(ctx, statuses[index])
		}(i, account)
	}
	wg.Wait()
	return statuses
}

// RefreshAccountStatus fetches the current balance and redemption status for one account.
func (u *Upstream) RefreshAccountStatus(ctx context.Context, accountID string) (AccountStatus, error) {
	accounts := u.accountsSnapshot()
	if len(accounts) == 0 {
		if strings.TrimSpace(u.cfg.Auth.Identifier) == "" {
			return AccountStatus{}, errors.New("未配置上游账号")
		}
		if accountID != "" && accountID != u.accountID {
			return AccountStatus{}, errors.New("账号不存在")
		}
		status := AccountStatus{ID: u.accountID, Identifier: u.cfg.Auth.Identifier, Enabled: true, RefreshedAt: time.Now()}
		return u.refreshAccountStatus(ctx, status), nil
	}
	for _, account := range accounts {
		if account.accountID != accountID {
			continue
		}
		status := AccountStatus{ID: account.accountID, Identifier: account.cfg.Auth.Identifier, Enabled: account.enabled, RefreshedAt: time.Now()}
		return account.refreshAccountStatus(ctx, status), nil
	}
	return AccountStatus{}, errors.New("账号不存在")
}

func (u *Upstream) refreshAccountStatus(ctx context.Context, status AccountStatus) AccountStatus {
	owner := u.restrictionOwner()
	managed := u.owner != nil || u.enabled
	finalize := func() AccountStatus {
		if managed {
			status.Enabled = u.enabled
		}
		status.CooldownUntil = owner.cooldownUntilFor(u)
		status.CooldownReason = owner.cooldownReasonFor(u)
		return status
	}
	if managed {
		status.Enabled = u.enabled
		status.CooldownUntil = owner.cooldownUntilFor(u)
		status.CooldownReason = owner.cooldownReasonFor(u)
		if !u.enabled {
			status.Error = "账号已停用"
			return finalize()
		}
	}
	if err := u.ensureLogin(ctx); err != nil {
		status.Error = "登录失败"
		return finalize()
	}
	state, err := u.checkinOnce(ctx, http.MethodGet, strings.TrimRight(u.cfg.Upstream.BaseURL, "/"), true)
	if err != nil {
		status.Error = "读取积分失败"
	} else {
		status.CheckedIn = boolPointer(state.CheckedIn)
		if state.BalanceKnown {
			status.Balance = int64Pointer(state.Balance)
			if state.Balance >= insufficientCreditsThreshold {
				owner.clearCreditCooldown(u)
			} else {
				owner.coolDownUntil(u, time.Now().Add(insufficientCreditsCooldown), cooldownReasonInsufficientCredits)
			}
		}
	}

	quota, err := u.limitedRedemptionRequest(ctx, http.MethodGet, "")
	if err != nil {
		if status.Error == "" {
			status.Error = "读取兑换额度失败"
		}
		return finalize()
	}
	if raw := quota["enabled"]; len(raw) > 0 {
		var enabled bool
		if json.Unmarshal(raw, &enabled) == nil {
			status.RedemptionEnabled = &enabled
		}
	}
	status.RedemptionRemaining = parseOptionalInt(quota["remaining"])
	status.RedemptionLimit = parseOptionalInt(quota["limit"])
	status.ValidityDays = parseOptionalInt(quota["validityDays"])
	resetRaw := quota["resetAt"]
	if len(resetRaw) == 0 {
		resetRaw = quota["reset_at"]
	}
	status.ResetAt = normalizeResetAt(resetRaw)
	status.RedemptionResetAt = status.ResetAt
	_ = json.Unmarshal(quota["creditType"], &status.CreditType)
	return finalize()
}

func (u *Upstream) RedeemCode(ctx context.Context, accountID, code string) RedemptionResult {
	return u.redeemCodeForAccount(ctx, accountID, code)
}

func (u *Upstream) redeemCodeForAccount(ctx context.Context, accountID, code string) (result RedemptionResult) {
	code = strings.TrimSpace(code)
	accounts := u.accountsSnapshot()
	if len(accounts) == 0 {
		if strings.TrimSpace(u.cfg.Auth.Identifier) == "" {
			return RedemptionResult{AccountID: accountID, Status: "error", Message: "未配置上游账号"}
		}
		accounts = []*Upstream{u}
	}
	var account *Upstream
	for _, candidate := range accounts {
		if candidate.accountID == accountID {
			account = candidate
			break
		}
	}
	if account == nil {
		return RedemptionResult{AccountID: accountID, Status: "error", Message: "账号不存在"}
	}
	result = RedemptionResult{AccountID: account.accountID, Identifier: account.cfg.Auth.Identifier}
	account.creditMu.RLock()
	recorder := account.redemptionRecorder
	account.creditMu.RUnlock()
	defer func() {
		if recorder != nil {
			recorder(code, result)
		}
	}()
	if !account.enabled && (u.poolMode || account != u) {
		result.Status, result.Message = "skipped", "账号已停用"
		return result
	}
	if code == "" || len(code) > 64 {
		result.Status, result.Message = "error", "兑换码不能为空且不能超过 64 个字符"
		return result
	}

	quota, err := account.limitedRedemptionRequest(ctx, http.MethodGet, "")
	if err != nil {
		result.Status, result.Message = "error", err.Error()
		return result
	}
	if raw := quota["enabled"]; len(raw) > 0 {
		var enabled bool
		if json.Unmarshal(raw, &enabled) == nil && !enabled {
			result.Status, result.Message = "skipped", "站点当前未开放兑换"
			return result
		}
	}
	if remaining := parseOptionalInt(quota["remaining"]); remaining != nil && *remaining <= 0 {
		result.Status, result.Message = "skipped", "该账号的兑换额度已用完"
		return result
	}

	payload, err := account.limitedRedemptionRequest(ctx, http.MethodPost, code)
	if err != nil {
		result.Status, result.Message = "error", err.Error()
		return result
	}
	var redeemed map[string]json.RawMessage
	if err := json.Unmarshal(payload["redemption"], &redeemed); err != nil || len(redeemed) == 0 {
		result.Status, result.Message = "error", "上游未确认兑换成功"
		return result
	}
	result.Credits = parseOptionalInt(redeemed["credits"])
	if result.Credits == nil {
		result.Status, result.Message = "error", "兑换响应缺少到账积分"
		return result
	}
	result.Status = "success"
	result.Message = fmt.Sprintf("到账 %d 积分", *result.Credits)
	if account.owner != nil {
		account.owner.clearCreditCooldown(account)
	} else {
		account.clearCreditCooldown(account)
	}
	return result
}

func (u *Upstream) limitedRedemptionRequest(ctx context.Context, method, code string) (map[string]json.RawMessage, error) {
	if err := u.ensureLogin(ctx); err != nil {
		return nil, errors.New("上游登录失败")
	}
	host := strings.TrimRight(u.cfg.Upstream.BaseURL, "/")
	endpoint := host + "/api/limited-redemption"
	var body io.Reader
	if method == http.MethodPost {
		encoded, _ := json.Marshal(map[string]string{"code": code})
		body = bytes.NewReader(encoded)
	}
	do := func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
		if err != nil {
			return nil, err
		}
		u.applyHeaders(req, host)
		req.Header.Set("Referer", host+"/#/limited-redemption")
		if method == http.MethodPost {
			req.Header.Set("Content-Type", "application/json")
		}
		return u.loginClient.Do(req)
	}

	resp, err := do()
	if err != nil {
		return nil, fmt.Errorf("兑换接口请求失败: %w", err)
	}
	responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	if readErr != nil {
		return nil, fmt.Errorf("读取兑换接口响应失败: %w", readErr)
	}
	if resp.StatusCode == http.StatusUnauthorized && u.cfg.Auth.AutoRelogin {
		u.mu.Lock()
		u.loggedIn = false
		u.mu.Unlock()
		if err := u.Login(ctx); err != nil {
			return nil, errors.New("上游重新登录失败")
		}
		if method == http.MethodPost {
			encoded, _ := json.Marshal(map[string]string{"code": code})
			body = bytes.NewReader(encoded)
		}
		resp, err = do()
		if err != nil {
			return nil, fmt.Errorf("重试兑换接口失败: %w", err)
		}
		responseBody, readErr = io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("读取兑换接口响应失败: %w", readErr)
		}
	}
	if resp.StatusCode/100 != 2 {
		if restriction, ok := detectAccountRestriction(resp.StatusCode, responseBody); ok {
			retryable := &safeAccountRetryError{
				err:            fmt.Errorf("兑换接口返回 HTTP %d", resp.StatusCode),
				cooldownUntil:  restriction.cooldownUntil,
				disableAccount: restriction.disableAccount,
			}
			u.restrictionOwner().applyAccountRetryError(u, retryable)
			return nil, retryable
		}
		return nil, fmt.Errorf("兑换接口返回 HTTP %d", resp.StatusCode)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(responseBody, &payload); err != nil {
		return nil, errors.New("兑换接口返回格式无效")
	}
	var upstreamError string
	if err := json.Unmarshal(payload["error"], &upstreamError); err == nil && upstreamError != "" {
		if restriction, ok := detectAccountRestriction(resp.StatusCode, responseBody); ok {
			retryable := &safeAccountRetryError{
				err:            errors.New(upstreamError),
				cooldownUntil:  restriction.cooldownUntil,
				disableAccount: restriction.disableAccount,
			}
			u.restrictionOwner().applyAccountRetryError(u, retryable)
			return nil, retryable
		}
		return nil, errors.New(upstreamError)
	}
	return payload, nil
}

func parseOptionalInt(raw json.RawMessage) *int64 {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var number int64
	if err := json.Unmarshal(raw, &number); err == nil {
		return &number
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return nil
	}
	return &parsed
}

func normalizeResetAt(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		text = strings.TrimSpace(text)
		if text == "" {
			return ""
		}
		if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
			return parsed.In(chinaLocation()).Format(time.RFC3339)
		}
		for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02T15:04:05", "2006-01-02T15:04:05.999999999"} {
			if parsed, err := time.ParseInLocation(layout, text, chinaLocation()); err == nil {
				return parsed.Format(time.RFC3339)
			}
		}
		return text
	}
	if seconds := parseOptionalInt(raw); seconds != nil {
		value := *seconds
		if value > 100000000000 {
			value /= 1000
		}
		return time.Unix(value, 0).In(chinaLocation()).Format(time.RFC3339)
	}
	return ""
}

func chinaLocation() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("UTC+8", 8*60*60)
	}
	return loc
}

func int64Pointer(value int64) *int64 { return &value }
func boolPointer(value bool) *bool    { return &value }
