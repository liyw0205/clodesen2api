package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"clodesen2api/internal/modelpolicy"
	"clodesen2api/internal/upstream"
)

func (s *Service) listModels(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	force := forceRefresh(r)
	cached, cachedAt := s.modelCacheSnapshot()
	if !force && len(cached) > 0 {
		if time.Since(cachedAt) < s.cacheTTL("models") {
			writeCachedJSON(w, cached, "hit")
			return
		}
		writeCachedJSON(w, cached, "stale")
		s.refreshModelsAsync()
		return
	}
	models, err := s.refreshModels(ctx, force)
	if err != nil {
		if len(cached) > 0 {
			writeCachedJSON(w, cached, "stale-on-error")
			return
		}
		writeError(w, http.StatusBadGateway, "读取上游模型列表失败")
		return
	}
	writeCachedJSON(w, models, "miss")
}

func (s *Service) fetchModels(ctx context.Context, force bool) ([]byte, error) {
	s.modelRefreshMu.Lock()
	defer s.modelRefreshMu.Unlock()
	if !force {
		if cached, cachedAt := s.modelCacheSnapshot(); len(cached) > 0 && time.Since(cachedAt) < s.cacheTTL("models") {
			return cached, nil
		}
	}
	models, err := s.upstream.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(models, &payload); err != nil {
		return nil, errors.New("上游模型列表格式无效")
	}
	if len(payload.Data) == 0 {
		cached, _ := s.modelCacheSnapshot()
		var previous struct {
			Data []any `json:"data"`
		}
		if json.Unmarshal(cached, &previous) == nil && len(previous.Data) > 0 {
			return nil, errors.New("empty model response cannot replace known catalog")
		}
	}
	if policies := s.currentModelPolicies(); policies != nil {
		ids, err := modelpolicy.IDs(models)
		if err != nil {
			return nil, err
		}
		if err := policies.Register(ctx, ids); err != nil {
			return nil, err
		}
	}
	_, prices, pricingErr := s.upstream.ListPricing(ctx)
	localPrices := append([]upstream.ModelPricing(nil), prices...)
	priceByModel := make(map[string]upstream.ModelPricing, len(prices))
	for _, price := range prices {
		priceByModel[strings.ToLower(strings.TrimSpace(price.Model))] = price
	}
	for _, model := range payload.Data {
		id, _ := model["id"].(string)
		key := strings.ToLower(strings.TrimSpace(id))
		if price, ok := priceByModel[key]; ok {
			if price.CreditsPerRequest != nil {
				model["creditsPerRequest"] = *price.CreditsPerRequest
			}
			if len(price.TokenPricing) > 0 {
				model["billingType"] = "token"
				var tokenPricing any
				if json.Unmarshal(price.TokenPricing, &tokenPricing) == nil {
					model["tokenPricing"] = tokenPricing
				}
			} else {
				model["billingType"] = "request"
			}
			model["pricingSource"] = "upstream"
			continue
		}
		if credits, ok := fallbackCredits(id); ok {
			model["creditsPerRequest"] = credits
			model["billingType"] = "request"
			model["pricingSource"] = "fallback"
			localPrices = append(localPrices, upstream.ModelPricing{Model: id, CreditsPerRequest: &credits})
		} else if pricingErr != nil {
			model["pricingSource"] = "unavailable"
		}
	}
	s.upstream.SetCreditPricing(localPrices)
	result, _ := json.Marshal(map[string]any{"object": "list", "data": payload.Data})
	s.cacheMu.Lock()
	s.modelCache = append(s.modelCache[:0], result...)
	s.modelCachedAt = time.Now()
	result = cloneCacheBytes(s.modelCache)
	s.cacheMu.Unlock()
	s.persistCache()
	return result, nil
}

func (s *Service) refreshModelsAsync() {
	s.refreshes.start("models", func(ctx context.Context) (any, error) { return s.fetchModels(ctx, false) })
}
