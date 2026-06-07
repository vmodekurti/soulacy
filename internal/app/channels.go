package app

// Channel wiring helpers (Story E10): adapter construction goes through the
// SDK factory registry; the host keeps only config-shape handling (multi-bot
// lists, adapter id derivation, system-agent guard).

import (
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/sdk/registry"
)

// buildChannel resolves a channel adapter through the SDK factory registry.
// The bot/channel config map is passed to the factory verbatim, plus the
// host-chosen adapter id (when non-empty) and the process logger under the
// documented host-internal "logger" key.
func buildChannel(name, adapterID string, cfg map[string]any, log *zap.Logger) (channels.Adapter, error) {
	m := make(map[string]any, len(cfg)+2)
	for k, v := range cfg {
		m[k] = v
	}
	if adapterID != "" {
		m["id"] = adapterID
	}
	m["logger"] = log
	a, ok, err := registry.NewChannel(name, m)
	if !ok {
		return nil, fmt.Errorf("no channel factory registered for %q (registered: %v)", name, registry.Channels())
	}
	if err != nil {
		return nil, err
	}
	return a, nil
}

// providerCfgMap converts a config.ProviderConfig struct into the schemaless
// map the SDK provider factories consume. Zero values are omitted so factory
// defaults apply.
func providerCfgMap(p config.ProviderConfig) map[string]any {
	m := map[string]any{}
	if p.BaseURL != "" {
		m["base_url"] = p.BaseURL
	}
	if p.APIKey != "" {
		m["api_key"] = p.APIKey
	}
	if p.Model != "" {
		m["model"] = p.Model
	}
	if p.KeepAlive != "" {
		m["keep_alive"] = p.KeepAlive
	}
	if p.Options != nil {
		m["options"] = p.Options
	}
	if p.PromptCaching {
		m["prompt_caching"] = true
	}
	if p.ExtendedThinking {
		m["extended_thinking"] = true
	}
	if p.ThinkingBudget != 0 {
		m["thinking_budget"] = p.ThinkingBudget
	}
	if p.SafetyLevel != "" {
		m["safety_level"] = p.SafetyLevel
	}
	if p.Organization != "" {
		m["organization"] = p.Organization
	}
	if p.ParallelToolCalls != nil {
		m["parallel_tool_calls"] = p.ParallelToolCalls
	}
	return m
}

// providerKeyFor returns the API key for an llm.providers entry, falling back
// to the named environment variable — the same precedence the provider
// factories apply. Used to wire reasoning loop backends (Story 16).
func providerKeyFor(cfg *config.Config, providerID, envVar string) string {
	if pc, ok := cfg.LLM.Providers[providerID]; ok && pc.APIKey != "" {
		return pc.APIKey
	}
	return os.Getenv(envVar)
}

func adapterIDForLog(channel string, index int, agentID string) string {
	if index == 0 {
		return channel
	}
	return channel + "-" + sanitizeID(agentID)
}

func externalChannelAgentAllowed(adapterID, agentID string, log *zap.Logger) bool {
	if strings.TrimSpace(agentID) != runtime.SystemAgentID {
		return true
	}
	log.Warn("external channel mapping skipped: system agent is web-only",
		zap.String("adapter_id", adapterID),
		zap.String("agent_id", agentID),
	)
	return false
}

// sanitizeID replaces characters that are not safe for adapter IDs or log
// fields with hyphens. Keeps letters, digits, hyphens, and underscores.
func sanitizeID(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}
