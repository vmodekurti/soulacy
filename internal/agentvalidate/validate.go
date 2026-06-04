// Package agentvalidate checks SOUL.yaml definitions before deployment.
package agentvalidate

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/pkg/agent"
	"gopkg.in/yaml.v3"
)

type Severity string

const (
	Warn  Severity = "warn"
	Error Severity = "error"
)

type Finding struct {
	Severity     Severity `json:"severity"`
	Field        string   `json:"field"`
	Message      string   `json:"message"`
	Suggestion   string   `json:"suggestion,omitempty"`
	Alternatives []string `json:"alternatives,omitempty"`
}

type Report struct {
	Path     string    `json:"path,omitempty"`
	AgentID  string    `json:"agent_id,omitempty"`
	Valid    bool      `json:"valid"`
	Warnings int       `json:"warnings"`
	Errors   int       `json:"errors"`
	Findings []Finding `json:"findings"`
}

type Options struct {
	Config              *config.Config
	RegisteredProviders []string
	ProviderModels      map[string][]string
	ConfiguredOnly      bool
}

func File(path string, opts Options) (Report, error) {
	report := Report{Path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		return report, err
	}
	return Bytes(data, path, opts), nil
}

func Bytes(data []byte, path string, opts Options) Report {
	report := Report{Path: path}
	strictDec := yaml.NewDecoder(bytes.NewReader(data))
	strictDec.KnownFields(true)
	var strict agent.Definition
	if err := strictDec.Decode(&strict); err != nil {
		report.add(Error, "SOUL.yaml", "unknown or malformed field: "+err.Error(), "", nil)
	}

	var def agent.Definition
	if err := yaml.Unmarshal(data, &def); err != nil {
		report.add(Error, "SOUL.yaml", "YAML parse failed: "+err.Error(), "", nil)
		report.Valid = false
		return report
	}
	return Definition(&def, path, opts, report)
}

func Definition(def *agent.Definition, path string, opts Options, report Report) Report {
	if def == nil {
		report.add(Error, "agent", "definition is nil", "", nil)
		report.Valid = false
		return report
	}
	report.AgentID = def.ID
	validateDefinitionShape(&report, def, path)
	validateLLMFit(&report, def, opts)
	if report.Findings == nil {
		report.Findings = []Finding{}
	}
	report.Valid = report.Errors == 0
	return report
}

func validateDefinitionShape(report *Report, def *agent.Definition, path string) {
	if strings.TrimSpace(def.ID) == "" {
		report.add(Error, "id", "required", "", nil)
	} else if strings.ContainsAny(def.ID, "/\\ \t\n\r") {
		report.add(Warn, "id", "avoid whitespace and path separators; the ID is used in file paths and tool names", "", nil)
	}
	if def.Trigger == "" {
		report.add(Warn, "trigger", "not set; runtime defaults may not match the intended activation mode", "", nil)
	}
	switch def.Trigger {
	case "", agent.TriggerChannel, agent.TriggerCron, agent.TriggerOneShot, agent.TriggerWebhook, agent.TriggerInternal:
	default:
		report.add(Error, "trigger", fmt.Sprintf("unsupported trigger %q", def.Trigger), "", nil)
	}
	if def.Trigger == agent.TriggerChannel && len(def.Channels) == 0 {
		report.add(Warn, "channels", "channel-triggered agents normally declare at least one channel", "", nil)
	}
	if (def.Trigger == agent.TriggerCron || def.Trigger == agent.TriggerOneShot) && def.Schedule == nil {
		report.add(Error, "schedule", "required for cron and oneshot triggers", "", nil)
	}
	if def.Schedule != nil {
		if def.Trigger == agent.TriggerCron && strings.TrimSpace(def.Schedule.Cron) == "" {
			report.add(Error, "schedule.cron", "required for cron trigger", "", nil)
		}
		if def.Trigger == agent.TriggerOneShot && def.Schedule.At.IsZero() {
			report.add(Error, "schedule.at", "required for oneshot trigger", "", nil)
		}
		validateDuration(report, "schedule.timeout", def.Schedule.Timeout)
	}
	validateDuration(report, "run_timeout", def.RunTimeout)
	if def.LLM.MaxTokens < 0 {
		report.add(Error, "llm.max_tokens", "must be zero or positive", "", nil)
	}
	if def.LLM.Temperature < 0 {
		report.add(Error, "llm.temperature", "must be zero or positive", "", nil)
	}
	if def.Memory.MaxTokens < 0 {
		report.add(Error, "memory.max_tokens", "must be zero or positive", "", nil)
	}
	for i, tool := range def.Tools {
		prefix := fmt.Sprintf("tools[%d]", i)
		if strings.TrimSpace(tool.Name) == "" {
			report.add(Error, prefix+".name", "required", "", nil)
		}
		if strings.TrimSpace(tool.PythonFile) == "" && strings.TrimSpace(tool.Inline) == "" {
			report.add(Warn, prefix, "tool has neither python_file nor inline implementation", "", nil)
		}
		if strings.TrimSpace(tool.PythonFile) != "" {
			validateLocalPath(report, prefix+".python_file", path, tool.PythonFile)
		}
		validateDuration(report, prefix+".timeout", tool.Timeout)
	}
	validateMCPList(report, "mcp_servers", def.MCPServers, false)
	validateMCPList(report, "mcp_tools", def.MCPTools, true)
}

func validateLLMFit(report *Report, def *agent.Definition, opts Options) {
	provider := strings.TrimSpace(def.LLM.Provider)
	if provider == "" && opts.Config != nil {
		provider = opts.Config.LLM.DefaultProvider
	}
	if provider == "" {
		report.add(Warn, "llm.provider", "no provider set and no default provider was available", "set llm.provider or llm.default_provider", providerAlternatives(opts))
		return
	}

	if len(def.LLM.AllowedProviders) > 0 && !contains(def.LLM.AllowedProviders, provider) {
		report.add(Error, "llm.allowed_providers", fmt.Sprintf("active provider %q is blocked by allowed_providers", provider), "add the provider to allowed_providers or select an allowed provider", def.LLM.AllowedProviders)
	}
	if opts.Config != nil {
		if _, ok := opts.Config.LLM.Providers[provider]; !ok {
			report.add(Warn, "llm.provider", fmt.Sprintf("provider %q is not configured in config.yaml", provider), "configure credentials/base_url for this provider or choose a configured provider", providerAlternatives(opts))
		}
	}
	if len(opts.RegisteredProviders) > 0 && !contains(opts.RegisteredProviders, provider) {
		report.add(Error, "llm.provider", fmt.Sprintf("provider %q is not registered in the running gateway", provider), "restart the gateway after editing config, or choose a registered provider", opts.RegisteredProviders)
	}

	model := strings.TrimSpace(def.LLM.Model)
	if model == "" && opts.Config != nil {
		if pc, ok := opts.Config.LLM.Providers[provider]; ok {
			model = strings.TrimSpace(pc.Model)
		}
	}
	if model == "" {
		report.add(Warn, "llm.model", "no model set; runtime will rely on provider defaults", "set llm.model explicitly for reproducible agent behavior", modelAlternatives(opts, provider))
		return
	}
	if models := modelsFor(opts, provider); len(models) > 0 && !contains(models, model) {
		report.add(Warn, "llm.model", fmt.Sprintf("model %q was not found for provider %q", model, provider), "choose one of the currently available models", bestModelAlternatives(models, def))
	}

	if likelyNeedsToolUse(def) && weakToolModel(provider, model) {
		report.add(Warn, "llm.model", fmt.Sprintf("model %q may be unreliable for tool-heavy agentic flows", model), "prefer a stronger tool-calling model for agents with tools, MCP, peer agents, knowledge search, or forced tool_choice", bestModelAlternatives(modelsFor(opts, provider), def))
	}
	if def.LLM.OutputSchema != nil && weakStructuredOutputModel(provider, model) {
		report.add(Warn, "llm.model", fmt.Sprintf("model %q may be unreliable for strict structured output", model), "prefer a model with stronger JSON/schema adherence", bestModelAlternatives(modelsFor(opts, provider), def))
	}
}

func validateDuration(report *Report, field, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	if _, err := time.ParseDuration(value); err != nil {
		report.add(Error, field, "invalid Go duration: "+err.Error(), "", nil)
	}
}

func validateLocalPath(report *Report, field, soulPath, value string) {
	if filepath.IsAbs(value) || soulPath == "" {
		return
	}
	candidate := filepath.Join(filepath.Dir(soulPath), value)
	if _, err := os.Stat(candidate); err != nil {
		report.add(Warn, field, "relative path not found from SOUL.yaml directory: "+value, "", nil)
	}
}

func validateMCPList(report *Report, field string, values *[]string, fullTool bool) {
	if values == nil {
		return
	}
	for i, raw := range *values {
		value := strings.TrimSpace(raw)
		item := fmt.Sprintf("%s[%d]", field, i)
		if value == "" {
			report.add(Error, item, "empty entries are not allowed", "", nil)
			continue
		}
		if value == "*" || strings.EqualFold(value, "all") {
			continue
		}
		if strings.ContainsAny(value, " \t\n\r") {
			report.add(Error, item, "must not contain whitespace", "", nil)
		}
		if fullTool && !strings.HasPrefix(value, "mcp__") {
			report.add(Error, item, "MCP tool entries must use full tool names like mcp__server__tool", "", nil)
		}
		if fullTool && strings.Count(value, "__") < 2 {
			report.add(Error, item, "MCP tool entries must include server and tool segments", "", nil)
		}
	}
}

func (r *Report) add(severity Severity, field, message, suggestion string, alternatives []string) {
	r.Findings = append(r.Findings, Finding{
		Severity: severity, Field: field, Message: message,
		Suggestion: suggestion, Alternatives: alternatives,
	})
	switch severity {
	case Error:
		r.Errors++
	case Warn:
		r.Warnings++
	}
}

func providerAlternatives(opts Options) []string {
	if len(opts.RegisteredProviders) > 0 {
		return sortedCopy(opts.RegisteredProviders)
	}
	if opts.Config == nil {
		return nil
	}
	values := make([]string, 0, len(opts.Config.LLM.Providers))
	for id := range opts.Config.LLM.Providers {
		values = append(values, id)
	}
	return sortedCopy(values)
}

func modelsFor(opts Options, provider string) []string {
	if opts.ProviderModels != nil {
		if models := opts.ProviderModels[provider]; len(models) > 0 {
			return sortedCopy(models)
		}
	}
	return nil
}

func modelAlternatives(opts Options, provider string) []string {
	if models := modelsFor(opts, provider); len(models) > 0 {
		return models
	}
	if opts.Config != nil {
		if pc, ok := opts.Config.LLM.Providers[provider]; ok && pc.Model != "" {
			return []string{pc.Model}
		}
	}
	return nil
}

func bestModelAlternatives(models []string, def *agent.Definition) []string {
	if len(models) == 0 {
		return nil
	}
	type scored struct {
		name  string
		score int
	}
	scoredModels := make([]scored, 0, len(models))
	for _, model := range models {
		score := modelScore(model, likelyNeedsToolUse(def), def.LLM.OutputSchema != nil)
		scoredModels = append(scoredModels, scored{name: model, score: score})
	}
	sort.SliceStable(scoredModels, func(i, j int) bool {
		if scoredModels[i].score == scoredModels[j].score {
			return scoredModels[i].name < scoredModels[j].name
		}
		return scoredModels[i].score > scoredModels[j].score
	})
	limit := 3
	if len(scoredModels) < limit {
		limit = len(scoredModels)
	}
	out := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, scoredModels[i].name)
	}
	return out
}

func modelScore(model string, needsTools, needsJSON bool) int {
	m := strings.ToLower(model)
	score := 0
	for _, marker := range []string{"70b", "72b", "opus", "sonnet", "gpt-4", "4o", "gemini-2", "qwen3", "qwen2.5"} {
		if strings.Contains(m, marker) {
			score += 3
		}
	}
	for _, marker := range []string{"mini", "haiku", "flash", "32b", "14b"} {
		if strings.Contains(m, marker) {
			score += 1
		}
	}
	for _, marker := range []string{"embed", "embedding", "nomic", "bge"} {
		if strings.Contains(m, marker) {
			score -= 20
		}
	}
	if needsTools {
		for _, marker := range []string{"qwen", "gpt-4", "4o", "sonnet", "opus", "gemini"} {
			if strings.Contains(m, marker) {
				score += 2
			}
		}
	}
	if needsJSON && strings.Contains(m, "json") {
		score += 1
	}
	return score
}

func likelyNeedsToolUse(def *agent.Definition) bool {
	if def == nil {
		return false
	}
	return len(def.Tools) > 0 || len(def.Agents) > 0 || len(def.Knowledge) > 0 ||
		(def.Builtins != nil && len(*def.Builtins) > 0) ||
		def.MCPServers != nil || def.MCPTools != nil ||
		strings.TrimSpace(def.LLM.ToolChoice) == "required" ||
		strings.HasPrefix(strings.TrimSpace(def.LLM.ToolChoice), "agent__")
}

func weakToolModel(provider, model string) bool {
	m := strings.ToLower(model)
	if strings.Contains(m, "embed") || strings.Contains(m, "nomic") {
		return true
	}
	if provider == "ollama" {
		for _, weak := range []string{"llama3:8b", "llama3.1:8b", "llama3.2:1b", "llama3.2:3b", "mistral:7b", "gemma:2b", "gemma2:2b"} {
			if strings.Contains(m, weak) {
				return true
			}
		}
	}
	return false
}

func weakStructuredOutputModel(provider, model string) bool {
	return weakToolModel(provider, model)
}

func contains(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

func sortedCopy(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
