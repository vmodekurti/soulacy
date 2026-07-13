package costs

import "strings"

// Pricing describes a model's USD price per 1 million input/output tokens.
type Pricing struct {
	InputPerMTok  float64
	OutputPerMTok float64
}

// PriceTable maps provider/model selectors to per-token prices. Selectors are
// matched in this order: provider/model, provider/*, */model.
type PriceTable map[string]Pricing

// EstimateUSD returns the estimated USD cost for one LLM call. Unknown or
// partially configured pricing returns 0 instead of guessing.
func EstimateUSD(table PriceTable, provider, model string, inputTokens, outputTokens int) float64 {
	if len(table) == 0 || inputTokens < 0 || outputTokens < 0 {
		return 0
	}
	price, ok := lookupPrice(table, provider, model)
	if !ok || price.InputPerMTok < 0 || price.OutputPerMTok < 0 {
		return 0
	}
	return (float64(inputTokens) / 1_000_000 * price.InputPerMTok) +
		(float64(outputTokens) / 1_000_000 * price.OutputPerMTok)
}

func lookupPrice(table PriceTable, provider, model string) (Pricing, bool) {
	p := normalizePricePart(provider)
	m := normalizePricePart(model)
	keys := []string{
		p + "/" + m,
		p + "/*",
		"*/" + m,
	}
	for _, key := range keys {
		if price, ok := table[key]; ok {
			return price, true
		}
	}
	return Pricing{}, false
}

func NormalizePriceKey(key string) string {
	key = strings.TrimSpace(strings.ToLower(key))
	if key == "" {
		return ""
	}
	parts := strings.SplitN(key, "/", 2)
	if len(parts) != 2 {
		return normalizePricePart(key)
	}
	return normalizePricePart(parts[0]) + "/" + normalizePricePart(parts[1])
}

func normalizePricePart(s string) string {
	return strings.TrimSpace(strings.ToLower(s))
}
