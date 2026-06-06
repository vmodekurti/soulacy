// Package llm implements the LLM routing layer.
// All inference calls go through the Router, which selects the appropriate
// provider based on the agent's LLMConfig and falls back to the global default.
// Adding a new provider requires implementing the Provider interface and
// registering it with Register().
package llm

import (
	"context"
	"fmt"
	"sync"

	"github.com/soulacy/soulacy/pkg/message"
)

// CompletionRequest is the provider-agnostic input to an LLM call.
type CompletionRequest struct {
	Model       string
	Messages    []ChatMessage
	Tools       []ToolSchema
	Temperature float64
	MaxTokens   int
	Stream      bool

	// ResponseFormat hints the provider to constrain its output. Empty = free
	// text. "json" = the response must be a single JSON value (object/array).
	// "json_schema" + JSONSchema = the response must validate against the
	// supplied JSON Schema (where supported — OpenAI structured outputs,
	// Gemini responseSchema; on Anthropic/Ollama we fall back to JSON-mode
	// + post-validation in the engine).
	ResponseFormat string
	JSONSchema     map[string]any

	// ToolChoice constrains the model's tool-selection behaviour for this
	// single request. Empty = no constraint. Otherwise one of:
	//   "auto"        — model decides
	//   "none"        — must not call a tool
	//   "required"    — must call at least one tool
	//   "<tool_name>" — must call this specific tool
	// The caller (engine) is responsible for clearing this between turns so
	// only turn 1 forces delegation; subsequent turns can synthesise freely.
	ToolChoice string
}

// ChatMessage mirrors the role/content structure used by all major LLM APIs.
type ChatMessage struct {
	Role    string // "system", "user", "assistant", "tool"
	Content string
	// For tool result messages:
	ToolCallID string
	Name       string // tool name (for tool-role messages)
	// For assistant messages that include tool calls:
	ToolCalls []message.ToolCall
}

// ToolSchema is the JSON Schema description of a callable tool.
type ToolSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// CompletionResponse carries the LLM's answer back to the runtime.
type CompletionResponse struct {
	Content   string
	ToolCalls []message.ToolCall
	// Usage statistics
	InputTokens  int
	OutputTokens int
	// Prompt caching statistics (Anthropic only; zero on other providers).
	// CacheCreationTokens is the number of input tokens written to cache this
	// turn (billed at 1.25× standard input rate).
	// CacheReadTokens is the number of input tokens served from cache this
	// turn (billed at 0.1× standard input rate — 90% discount).
	CacheCreationTokens int
	CacheReadTokens     int
	// If Stream is true, tokens arrive on this channel. Closed when done.
	Stream <-chan string
}

// Provider is the interface every LLM backend must implement.
type Provider interface {
	// ID returns the provider's unique identifier (e.g. "ollama", "openai").
	ID() string

	// Complete sends a chat-completion request and returns the response.
	// ctx carries deadline and cancellation.
	Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error)

	// Models returns the list of available model identifiers.
	Models(ctx context.Context) ([]string, error)
}

// Router dispatches LLM calls to registered providers.
type Router struct {
	mu          sync.RWMutex
	providers   map[string]Provider
	defaultID   string
}

// NewRouter creates a Router with the given default provider ID.
func NewRouter(defaultProviderID string) *Router {
	return &Router{
		providers: make(map[string]Provider),
		defaultID: defaultProviderID,
	}
}

// Register adds a provider. Panics if called with a duplicate ID after init.
func (r *Router) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[p.ID()] = p
}

// Complete routes a request to the named provider (or the default if providerID is "").
func (r *Router) Complete(ctx context.Context, providerID string, req CompletionRequest) (*CompletionResponse, error) {
	if providerID == "" {
		providerID = r.defaultID
	}
	r.mu.RLock()
	p, ok := r.providers[providerID]
	r.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("llm: unknown provider %q (registered: %v)", providerID, r.providerIDs())
	}
	return p.Complete(ctx, req)
}

// Provider returns the registered provider with the given ID, or nil if none.
// If id is empty, the default provider is returned.
func (r *Router) Provider(id string) Provider {
	if id == "" {
		id = r.defaultID
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.providers[id]
}

// DefaultProvider returns the configured default provider ID.
func (r *Router) DefaultProvider() string {
	return r.defaultID
}

// ProviderIDs returns the IDs of all registered providers.
func (r *Router) ProviderIDs() []string {
	return r.providerIDs()
}

func (r *Router) providerIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.providers))
	for id := range r.providers {
		ids = append(ids, id)
	}
	return ids
}
