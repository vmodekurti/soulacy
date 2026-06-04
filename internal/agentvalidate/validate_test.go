package agentvalidate

import (
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/pkg/agent"
)

func TestDefinitionFlagsUnavailableModelWithAlternatives(t *testing.T) {
	def := &agent.Definition{
		ID:      "tool-agent",
		Trigger: agent.TriggerChannel,
		LLM:     agent.LLMConfig{Provider: "ollama", Model: "missing-model"},
		Tools:   []agent.ToolDef{{Name: "lookup", Inline: "print('ok')"}},
	}
	report := Definition(def, "", Options{
		Config: &config.Config{LLM: config.LLMConfig{
			DefaultProvider: "ollama",
			Providers: map[string]config.ProviderConfig{
				"ollama": {Model: "qwen2.5:72b"},
			},
		}},
		RegisteredProviders: []string{"ollama"},
		ProviderModels: map[string][]string{
			"ollama": {"nomic-embed-text:latest", "llama3.2:3b", "qwen2.5:72b"},
		},
	}, Report{})

	if !report.Valid {
		t.Fatalf("missing model should warn, not fail: %+v", report)
	}
	if report.Warnings == 0 {
		t.Fatalf("expected model warning, got %+v", report)
	}
	if !hasAlternative(report, "qwen2.5:72b") {
		t.Fatalf("expected qwen2.5:72b alternative, got %+v", report.Findings)
	}
}

func TestDefinitionFailsUnregisteredProvider(t *testing.T) {
	def := &agent.Definition{
		ID:      "agent",
		Trigger: agent.TriggerChannel,
		LLM:     agent.LLMConfig{Provider: "anthropic", Model: "claude-sonnet-4-5"},
	}
	report := Definition(def, "", Options{
		Config: &config.Config{LLM: config.LLMConfig{
			DefaultProvider: "ollama",
			Providers: map[string]config.ProviderConfig{
				"anthropic": {Model: "claude-sonnet-4-5"},
			},
		}},
		RegisteredProviders: []string{"ollama"},
	}, Report{})

	if report.Valid {
		t.Fatalf("expected invalid report for unregistered provider, got %+v", report)
	}
}

func TestDefinitionWarnsWeakToolModel(t *testing.T) {
	def := &agent.Definition{
		ID:      "tool-agent",
		Trigger: agent.TriggerChannel,
		LLM:     agent.LLMConfig{Provider: "ollama", Model: "llama3.2:3b"},
		Tools:   []agent.ToolDef{{Name: "lookup", Inline: "print('ok')"}},
	}
	report := Definition(def, "", Options{
		Config: &config.Config{LLM: config.LLMConfig{
			Providers: map[string]config.ProviderConfig{"ollama": {Model: "llama3.2:3b"}},
		}},
		RegisteredProviders: []string{"ollama"},
		ProviderModels:      map[string][]string{"ollama": {"llama3.2:3b", "qwen2.5:72b"}},
	}, Report{})

	if !hasFinding(report, "tool-heavy") {
		t.Fatalf("expected weak tool model warning, got %+v", report.Findings)
	}
}

func hasAlternative(report Report, value string) bool {
	for _, finding := range report.Findings {
		for _, alt := range finding.Alternatives {
			if alt == value {
				return true
			}
		}
	}
	return false
}

func hasFinding(report Report, needle string) bool {
	for _, finding := range report.Findings {
		if strings.Contains(finding.Message, needle) || strings.Contains(finding.Suggestion, needle) {
			return true
		}
	}
	return false
}
