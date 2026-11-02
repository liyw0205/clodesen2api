package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"clodesen2api/internal/openai"
)

// ModelPricing is the request-level credit price exposed by the upstream.
// Token-priced models may also include tokenPricing metadata in the raw model
// response, but this field remains useful as a display estimate.
type ModelPricing struct {
	Model             string          `json:"model"`
	CreditsPerRequest *int64          `json:"creditsPerRequest,omitempty"`
	Enabled           bool            `json:"enabled"`
	ModelType         string          `json:"modelType,omitempty"`
	TokenPricing      json.RawMessage `json:"tokenPricing,omitempty"`
}

type pricingResponse struct {
	Pricing struct {
		DefaultRequestCredits *int64         `json:"defaultRequestCredits"`
		Models                []ModelPricing `json:"models"`
	} `json:"pricing"`
}

// CreditUsageRecord is one account transaction related to model usage.
type CreditUsageRecord struct {
	ID           int64  `json:"id"`
	AccountID    string `json:"accountId"`
	Identifier   string `json:"identifier"`
	Model        string `json:"model"`
	Amount       int64  `json:"amount"`
	BalanceAfter int64  `json:"balanceAfter"`
	BalanceKnown bool   `json:"balanceKnown"`
	Type         string `json:"type"`
	Description  string `json:"description"`
	Status       string `json:"status"`
	CreatedAt    string `json:"createdAt"`
	Source       string `json:"source"`
}

type accountResponse struct {
	Account struct {
		Transactions []struct {
			ID           int64  `json:"id"`
			Amount       int64  `json:"amount"`
			BalanceAfter int64  `json:"balance_after"`
			Type         string `json:"type"`
			Description  string `json:"description"`
			Status       string `json:"status"`
			CreatedAt    string `json:"created_at"`
			Model        string `json:"model"`
		} `json:"transactions"`
	} `json:"account"`
}

// ListPricing reads the upstream pricing catalog. Pricing is shared by the
// service, so the first account that can authenticate is sufficient.
func (u *Upstream) ListPricing(ctx context.Context) (defaultCredits *int64, models []ModelPricing, err error) {
	accounts := u.accountsSnapshot()
	if len(accounts) == 0 {
		if strings.TrimSpace(u.cfg.Auth.Identifier) == "" {
			return nil, nil, errors.New("no upstream account configured")
		}
		accounts = []*Upstream{u}
	}
	var lastErr error
	for _, account := range accounts {
		if len(u.accountsSnapshot()) > 0 && !account.enabled {
			continue
		}
		body, requestErr := account.getJSON(ctx, "/api/credits?scope=pricing")
		if requestErr != nil {
			lastErr = requestErr
			continue
		}
		var payload pricingResponse
		if err := json.Unmarshal(body, &payload); err != nil {
			lastErr = fmt.Errorf("decode upstream pricing: %w", err)
			continue
		}
		if payload.Pricing.Models == nil {
			lastErr = errors.New("upstream pricing response has no models")
			continue
		}
		return payload.Pricing.DefaultRequestCredits, payload.Pricing.Models, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no enabled account available for pricing")
	}
	return nil, nil, lastErr
}

// ListCreditUsage returns recent model-related transactions from every account.
// Account failures are tolerated when at least one account responds.
func (u *Upstream) ListCreditUsage(ctx context.Context) ([]CreditUsageRecord, error) {
	accounts := u.accountsSnapshot()
	direct := len(accounts) == 0
	if direct {
		if strings.TrimSpace(u.cfg.Auth.Identifier) == "" {
			return nil, errors.New("no upstream account configured")
		}
		accounts = []*Upstream{u}
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	records := make([]CreditUsageRecord, 0)
	successes := 0
	var lastErr error
	for _, account := range accounts {
		account := account
		wg.Add(1)
		go func() {
			defer wg.Done()
			body, err := account.getJSON(ctx, "/api/account")
			if err != nil {
				mu.Lock()
				lastErr = err
				mu.Unlock()
				return
			}
			var payload accountResponse
			if err := json.Unmarshal(body, &payload); err != nil {
				mu.Lock()
				lastErr = err
				mu.Unlock()
				return
			}
			items := make([]CreditUsageRecord, 0, len(payload.Account.Transactions))
			for _, transaction := range payload.Account.Transactions {
				typeName := strings.ToLower(strings.TrimSpace(transaction.Type))
				if typeName != "model_usage" && !strings.Contains(typeName, "refund") {
					continue
				}
				if typeName != "model_usage" {
					typeName = "refund"
				}
				model := strings.TrimSpace(transaction.Model)
				if model == "" {
					model = modelFromDescription(transaction.Description)
				}
				items = append(items, CreditUsageRecord{
					ID: transaction.ID, AccountID: account.accountID,
					Identifier: account.cfg.Auth.Identifier, Model: model,
					Amount: transaction.Amount, BalanceAfter: transaction.BalanceAfter,
					BalanceKnown: true,
					Type:         typeName, Description: transaction.Description,
					Status: transaction.Status, CreatedAt: transaction.CreatedAt, Source: "upstream",
				})
			}
			mu.Lock()
			successes++
			records = append(records, items...)
			mu.Unlock()
		}()
	}
	wg.Wait()
	sort.SliceStable(records, func(i, j int) bool { return records[i].CreatedAt > records[j].CreatedAt })
	if successes == 0 && lastErr != nil {
		return nil, lastErr
	}
	return records, nil
}

func (u *Upstream) recordLocalCredit(request openai.Request) {
	model := strings.TrimSpace(request.Model)
	if model == "" {
		return
	}
	u.creditMu.RLock()
	recorder := u.creditRecorder
	credits := u.creditPricing[strings.ToLower(model)]
	u.creditMu.RUnlock()
	if recorder == nil {
		return
	}
	amount := int64(0)
	if credits > 0 {
		amount = -credits
	}
	recorder(CreditUsageRecord{
		ID:           -time.Now().UnixNano(),
		AccountID:    u.accountID,
		Identifier:   u.cfg.Auth.Identifier,
		Model:        model,
		Amount:       amount,
		BalanceKnown: false,
		Type:         "local_request",
		Description:  "本地实时记录",
		Status:       "completed",
		CreatedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		Source:       "local",
	})
}

func (u *Upstream) getJSON(ctx context.Context, path string) ([]byte, error) {
	if err := u.ensureLogin(ctx); err != nil {
		return nil, err
	}
	host := strings.TrimRight(u.cfg.Upstream.BaseURL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, host+path, nil)
	if err != nil {
		return nil, err
	}
	u.applyHeaders(req, host)
	resp, err := u.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("upstream %s returned HTTP %d", path, resp.StatusCode)
	}
	return body, nil
}

func modelFromDescription(description string) string {
	description = strings.TrimSpace(description)
	for _, prefix := range []string{"模型请求退款：", "模型请求：", "模型请求: ", "模型请求:"} {
		if strings.HasPrefix(description, prefix) {
			model := strings.TrimSpace(strings.TrimPrefix(description, prefix))
			if index := strings.IndexAny(model, "（("); index >= 0 {
				model = strings.TrimSpace(model[:index])
			}
			return model
		}
	}
	return ""
}
