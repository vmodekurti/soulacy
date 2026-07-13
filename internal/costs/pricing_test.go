package costs

import "testing"

func TestEstimateUSDExactProviderModel(t *testing.T) {
	table := PriceTable{
		"openai/gpt-test": {InputPerMTok: 1.5, OutputPerMTok: 6},
	}
	got := EstimateUSD(table, "OpenAI", "GPT-Test", 1000, 2000)
	want := 0.0135
	if got != want {
		t.Fatalf("EstimateUSD = %v, want %v", got, want)
	}
}

func TestEstimateUSDWildcardFallbacks(t *testing.T) {
	table := PriceTable{
		"openai/*":       {InputPerMTok: 1, OutputPerMTok: 2},
		"*/shared-model": {InputPerMTok: 3, OutputPerMTok: 4},
	}
	if got, want := EstimateUSD(table, "openai", "other", 1_000_000, 1_000_000), 3.0; got != want {
		t.Fatalf("provider wildcard = %v, want %v", got, want)
	}
	if got, want := EstimateUSD(table, "custom", "shared-model", 1_000_000, 1_000_000), 7.0; got != want {
		t.Fatalf("model wildcard = %v, want %v", got, want)
	}
}

func TestEstimateUSDUnknownOrInvalidPricingIsZero(t *testing.T) {
	table := PriceTable{
		"bad/model": {InputPerMTok: -1, OutputPerMTok: 1},
	}
	if got := EstimateUSD(table, "missing", "model", 100, 100); got != 0 {
		t.Fatalf("unknown EstimateUSD = %v, want 0", got)
	}
	if got := EstimateUSD(table, "bad", "model", 100, 100); got != 0 {
		t.Fatalf("invalid EstimateUSD = %v, want 0", got)
	}
}

func TestNormalizePriceKey(t *testing.T) {
	if got, want := NormalizePriceKey(" OpenAI / GPT-4.1-Mini "), "openai/gpt-4.1-mini"; got != want {
		t.Fatalf("NormalizePriceKey = %q, want %q", got, want)
	}
}
