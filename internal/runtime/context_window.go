package runtime

import (
	"strings"

	"github.com/soulacy/soulacy/internal/llm"
)

// context_window.go — token-aware context management (Story 4 / S5.1).
//
// The engine has no way to ask a provider "will this fit?" before it calls, so
// a long history + many tools can blow past the model's context window and come
// back as an unrecoverable HTTP 400 mid-run. This file provides three cheap,
// provider-agnostic safeguards:
//
//   1. modelContextLimit — a conservative per-model context-window table.
//   2. estimateTokens     — a chars/4 heuristic over messages + tool schemas.
//   3. trimMessagesToFit  — drop oldest non-system turns until the estimate fits.
//
// Plus isContextExceededErr, used by the engine to auto-trim and retry once when
// a provider rejects the request as too large despite the proactive trim.

// defaultContextLimit is the conservative fallback when we don't recognise the
// model. Local models served via Ollama frequently default to a small num_ctx,
// so we err on the safe side.
const defaultContextLimit = 8192

// modelContextLimit returns a conservative token budget for the given
// provider/model. Matching is by lowercase substring on the model name so we
// catch versioned variants (claude-3-5-sonnet-20241022, gpt-4o-mini, …).
func modelContextLimit(provider, model string) int {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(m, "claude"):
		return 200000
	case strings.Contains(m, "gpt-4o"), strings.Contains(m, "gpt-4.1"),
		strings.Contains(m, "o1"), strings.Contains(m, "o3"), strings.Contains(m, "gpt-4-turbo"):
		return 128000
	case strings.Contains(m, "gpt-4"):
		return 32768
	case strings.Contains(m, "gpt-3.5"):
		return 16385
	case strings.Contains(m, "gemini"):
		return 1000000
	// Gemma 3 (and the larger 27B/31B variants) carry a 128k window; even Gemma 2
	// is 8k. Recognising "gemma" stops it falling through to the tiny default that
	// trimmed history every turn and made tool-using agents loop. The
	// context-exceeded retry safely catches the rare case a smaller local num_ctx
	// is actually in force.
	case strings.Contains(m, "gemma"):
		return 32768
	case strings.Contains(m, "mistral"), strings.Contains(m, "mixtral"), strings.Contains(m, "qwen"):
		return 32768
	case strings.Contains(m, "llama3.1"), strings.Contains(m, "llama-3.1"),
		strings.Contains(m, "llama3.2"), strings.Contains(m, "llama-3.2"):
		// Architecturally 128k, but local Ollama's default num_ctx is far smaller;
		// stay conservative so we don't rely on a num_ctx the operator may not
		// have set. (Hosted "cloud" providers run full context — handled below.)
		if isHostedCloudProvider(provider) {
			return 32768
		}
		return 8192
	}
	// Hosted cloud providers (e.g. ollama_cloud) run models at their FULL context,
	// unlike a local Ollama whose num_ctx defaults small. An unrecognised model on
	// such a provider should NOT get the tiny local default — that over-trims and
	// breaks multi-step tool use. Give it a generous-but-bounded budget; an actual
	// overflow is still caught by the context-exceeded retry.
	if isHostedCloudProvider(provider) {
		return 32768
	}
	return defaultContextLimit
}

// isHostedCloudProvider reports whether the provider is a hosted service that
// runs models at full context (so the small local-Ollama default doesn't apply).
func isHostedCloudProvider(provider string) bool {
	p := strings.ToLower(strings.TrimSpace(provider))
	return strings.Contains(p, "cloud") || strings.Contains(p, "openrouter") ||
		strings.Contains(p, "together") || strings.Contains(p, "groq") ||
		strings.Contains(p, "fireworks")
}

// estimateTokens is a deliberately rough chars/4 approximation of the prompt
// size: every message's content (and tool-call payloads) plus each tool
// schema's name/description/parameter surface. It overestimates slightly, which
// is the safe direction for a budget check.
func estimateTokens(msgs []llm.ChatMessage, tools []llm.ToolSchema) int {
	chars := 0
	for _, msg := range msgs {
		chars += len(msg.Content) + len(msg.Name)
		for _, tc := range msg.ToolCalls {
			// Arguments is a decoded map; approximate its serialized weight by
			// the number of keys (~a short field each).
			chars += len(tc.Name) + 40*len(tc.Arguments)
		}
	}
	for _, t := range tools {
		chars += len(t.Name) + len(t.Description)
		// A tool's JSON Schema isn't free; approximate its weight by the count
		// of declared parameter keys (each ~ a short line of schema).
		chars += 40 * len(t.Parameters)
	}
	return chars / 4
}

// trimMessagesToFit returns msgs trimmed so that estimateTokens(result, tools)
// is at or under inputBudget. Leading system messages are always preserved;
// the oldest NON-system messages are dropped first. To avoid leaving a dangling
// tool-result at the new front (which strict providers like Gemini reject), any
// leading tool-role messages after the system block are also dropped. Returns
// the (possibly unchanged) slice and the number of messages dropped.
func trimMessagesToFit(msgs []llm.ChatMessage, tools []llm.ToolSchema, inputBudget int) ([]llm.ChatMessage, int) {
	if inputBudget <= 0 || estimateTokens(msgs, tools) <= inputBudget {
		return msgs, 0
	}
	// Find the end of the leading system block (preserved verbatim).
	sysEnd := 0
	for sysEnd < len(msgs) && msgs[sysEnd].Role == "system" {
		sysEnd++
	}
	system := msgs[:sysEnd]
	rest := append([]llm.ChatMessage(nil), msgs[sysEnd:]...)

	dropped := 0
	for len(rest) > 1 && estimateTokens(append(append([]llm.ChatMessage(nil), system...), rest...), tools) > inputBudget {
		rest = rest[1:]
		dropped++
		// Don't start the surviving history on an orphan tool result.
		for len(rest) > 1 && rest[0].Role == "tool" {
			rest = rest[1:]
			dropped++
		}
	}
	// Final guard: a surviving history that STARTS with a tool result is an
	// orphan — its assistant function-call turn was trimmed away. Sending a
	// function response with no preceding function call makes strict providers
	// (e.g. Gemini) reject the whole request with a 400. Drop leading orphans
	// even down to empty; better to lose the result than to fail the call.
	for len(rest) > 0 && rest[0].Role == "tool" {
		rest = rest[1:]
		dropped++
	}
	out := append(append([]llm.ChatMessage(nil), system...), rest...)
	return out, dropped
}

// contextExceededMarkers are substrings that appear in the various providers'
// "your prompt is too big" errors.
var contextExceededMarkers = []string{
	"context length",
	"context_length_exceeded",
	"maximum context",
	"context window",
	"too many tokens",
	"prompt is too long",
	"input is too long",
	"reduce the length",
	"exceeds the maximum",
	"too large",
}

// isContextExceededErr reports whether err looks like a provider rejecting the
// request because the prompt exceeded the model's context window.
func isContextExceededErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range contextExceededMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
