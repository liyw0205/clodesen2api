package admin

import (
	"strings"

	"clodesen2api/internal/upstream"
)

// fallbackModelCredits keeps the panel useful when the upstream pricing
// endpoint is temporarily unavailable. Values are request-level estimates.
var fallbackModelCredits = map[string]int64{
	"cldude-5.5":                200,
	"deepseek-v4.1-flash":       50,
	"gpt-5.6-sol":               60,
	"gpt-5.6-terra":             80,
	"gpt-6-astra":               1,
	"gpt-6-sol":                 60,
	"gpt-6.1-sol":               70,
	"gpt-image-2":               100,
	"gpt-image-2-4k":            450,
	"gpt-image-2.5-flare":       150,
	"gpt-image-2.5-flare-4k":    500,
	"gpt-image-2.5-sunburst":    150,
	"gpt-image-2.5-sunburst-4k": 550,
	"grok-imagine-image-2.0":    100,
	"midjourney-v8.2":           1400,
	"mimo-v2.5":                 15,
	"minimax-m3":                15,
	"nano-banana-2":             400,
	"nano-banana-2.1":           400,
}

func fallbackCredits(model string) (int64, bool) {
	credits, ok := fallbackModelCredits[strings.ToLower(strings.TrimSpace(model))]
	return credits, ok
}

func fallbackPricing() []upstream.ModelPricing {
	prices := make([]upstream.ModelPricing, 0, len(fallbackModelCredits))
	for model, credits := range fallbackModelCredits {
		value := credits
		prices = append(prices, upstream.ModelPricing{Model: model, CreditsPerRequest: &value})
	}
	return prices
}
