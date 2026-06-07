package main

import (
	"testing"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/config"
)

func TestProviderCfgMapOmitsZeroValues(t *testing.T) {
	pt := false
	m := providerCfgMap(config.ProviderConfig{
		BaseURL:           "https://x",
		APIKey:            "k",
		ThinkingBudget:    100,
		ParallelToolCalls: &pt,
	})
	if m["base_url"] != "https://x" || m["api_key"] != "k" || m["thinking_budget"] != 100 {
		t.Fatalf("map = %v", m)
	}
	if _, present := m["model"]; present {
		t.Fatal("zero-value model must be omitted so factory defaults apply")
	}
	if _, present := m["prompt_caching"]; present {
		t.Fatal("false prompt_caching must be omitted")
	}
	if got, ok := m["parallel_tool_calls"].(*bool); !ok || *got != false {
		t.Fatalf("parallel_tool_calls = %v", m["parallel_tool_calls"])
	}
}

func TestBuildChannelInjectsIDAndLogger(t *testing.T) {
	a, err := buildChannel("telegram", "telegram-x", map[string]any{
		"token": "123:abc", "agent_id": "a",
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("buildChannel: %v", err)
	}
	if a.ID() != "telegram-x" {
		t.Fatalf("ID = %q", a.ID())
	}
	// source map must not be mutated
	src := map[string]any{"token": "123:abc"}
	if _, err := buildChannel("telegram", "tg2", src, zap.NewNop()); err != nil {
		t.Fatalf("buildChannel: %v", err)
	}
	if _, mutated := src["id"]; mutated {
		t.Fatal("buildChannel must not mutate the caller's config map")
	}
}

func TestBuildChannelUnknownName(t *testing.T) {
	if _, err := buildChannel("matrix", "", map[string]any{}, zap.NewNop()); err == nil {
		t.Fatal("unknown channel name must error (host falls back / warns)")
	}
}
