// config.go — helpers for wiring agent SOUL.yaml reasoning config into LoopConfig.
package reasoning

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/soulacy/soulacy/pkg/agent"
)

// ProviderKeys carries API keys for cloud providers. Pass to DefaultBackendFor.
type ProviderKeys struct {
	AnthropicKey string
	OpenAIKey    string
	// GroqKey / TogetherKey etc. are just OpenAI-compatible — put them in OpenAIKey
	// and set a custom BaseURL on the returned backend if needed.
}

// DefaultBackendFor returns the right LLMBackend for the agent, derived
// entirely from def.LLM.Provider — no extra field in SOUL.yaml needed.
//
// Routing table:
//
//	ollama    → OllamaBackend    (local Ollama, no key required)
//	anthropic → AnthropicBackend (requires keys.AnthropicKey)
//	openai    → OpenAIBackend    (requires keys.OpenAIKey)
//	groq      → OpenAICompatible at api.groq.com/openai/v1
//	together  → OpenAICompatible at api.together.xyz/v1
//	<other>   → OllamaBackend   (safe fallback for unknown providers)
//
// The agent's llm.model is used for Think(). Plan/Reflect default to the
// best available model for the provider (see inline comments below).
func DefaultBackendFor(def *agent.Definition, keys ProviderKeys) LLMBackend {
	provider := strings.ToLower(strings.TrimSpace(def.LLM.Provider))
	model := strings.TrimSpace(def.LLM.Model)
	baseURL := strings.TrimSpace(def.LLM.BaseURL)

	switch provider {
	case "anthropic":
		if keys.AnthropicKey != "" {
			b := NewAnthropicBackend(baseURL, keys.AnthropicKey)
			if model != "" {
				b.ThinkModel = model
				b.PlanModel = model
				b.ReflectModel = model
			}
			return b
		}
		// No key configured — fall back to Ollama with a clear message in logs.
		// The validator will already have warned about the missing key.
		fallthrough

	case "openai":
		if keys.OpenAIKey != "" {
			ep := baseURL
			if ep == "" {
				ep = "https://api.openai.com/v1"
			}
			b := newOpenAICompatibleBackend(ep, keys.OpenAIKey, model)
			if model != "" {
				b.ThinkModel = model
				b.PlanReflectModel = model
			}
			return b
		}
		fallthrough

	case "groq":
		// Groq uses an OpenAI-compatible API. The base URL can be overridden
		// via llm.base_url; otherwise use the standard Groq endpoint.
		ep := baseURL
		if ep == "" {
			ep = "https://api.groq.com/openai/v1"
		}
		b := newOpenAICompatibleBackend(ep, keys.OpenAIKey, model)
		if model != "" {
			b.ThinkModel = model
			b.PlanReflectModel = model
		}
		return b

	case "together":
		ep := baseURL
		if ep == "" {
			ep = "https://api.together.xyz/v1"
		}
		b := newOpenAICompatibleBackend(ep, keys.OpenAIKey, model)
		if model != "" {
			b.ThinkModel = model
			b.PlanReflectModel = model
		}
		return b

	default:
		// ollama + any unknown provider
		b := NewOllamaBackend(baseURL)
		if model != "" {
			// Use the declared model for Think (hot path).
			// Plan/Reflect keep qwen2.5:72b for better JSON structure unless the
			// declared model is already a large qwen variant or if qwen2.5:72b is not installed.
			b.ThinkModel = model
			if isLargeQwen(model) {
				b.PlanReflectModel = model
			} else if !ollamaHasModel(baseURL, "qwen2.5:72b") {
				b.PlanReflectModel = model
			}
		}
		return b
	}
}

// ollamaHasModel queries Ollama's local tags API to verify if targetModel is installed.
func ollamaHasModel(baseURL, targetModel string) bool {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	client := &http.Client{Timeout: 200 * time.Millisecond}
	resp, err := client.Get(baseURL + "/api/tags")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return false
	}
	var payload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return false
	}
	for _, m := range payload.Models {
		if m.Name == targetModel || strings.HasPrefix(m.Name, targetModel+":") || strings.Contains(m.Name, targetModel) {
			return true
		}
	}
	return false
}

// isLargeQwen returns true for qwen models likely good enough for Plan/Reflect
// without needing to fall back to qwen2.5:72b.
func isLargeQwen(model string) bool {
	m := strings.ToLower(model)
	return strings.Contains(m, "qwen") &&
		(strings.Contains(m, "72b") || strings.Contains(m, "32b") || strings.Contains(m, "14b"))
}

// LoopConfigFromDefinition builds a LoopConfig from an agent Definition's
// reasoning: YAML block (RL-04). Returns (config, true) when the agent has a
// valid strategy configured; returns (zero, false) when the reasoning block is
// absent or strategy is empty (agent uses the classic single-call path).
func LoopConfigFromDefinition(def *agent.Definition, systemPrompt string) (LoopConfig, bool) {
	rc := def.Reasoning
	if rc.Strategy == "" {
		return LoopConfig{}, false
	}

	cfg := LoopConfig{
		Strategy:     LoopStrategy(rc.Strategy),
		SystemPrompt: systemPrompt,
	}

	cfg.MaxSteps = rc.MaxSteps
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = 8
	}

	cfg.MaxPlanSteps = rc.MaxPlanSteps
	if cfg.MaxPlanSteps <= 0 {
		cfg.MaxPlanSteps = 6
	}

	if rc.StepTimeout != "" {
		if d, err := time.ParseDuration(rc.StepTimeout); err == nil && d > 0 {
			cfg.StepTimeout = d
		}
	}
	if cfg.StepTimeout <= 0 {
		cfg.StepTimeout = 30 * time.Second
	}

	if rc.TotalTimeout != "" {
		if d, err := time.ParseDuration(rc.TotalTimeout); err == nil && d > 0 {
			cfg.TotalTimeout = d
		}
	}
	if cfg.TotalTimeout <= 0 {
		cfg.TotalTimeout = 180 * time.Second
	}

	// Collect tool names from the agent's tool definitions (RL-08).
	for _, t := range def.Tools {
		cfg.ToolNames = append(cfg.ToolNames, t.Name)
	}

	// Story E25: the "flow" strategy needs the agent's graph. Attached for
	// any strategy — only "flow" reads it; nil when the workflow declares
	// no nodes (the strategy then degrades gracefully).
	cfg.Flow = def.Workflow.FlowSpec()

	return cfg, true
}
