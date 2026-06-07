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

	sdkllm "github.com/soulacy/soulacy/sdk/llm"
)

// Canonical LLM contract types live in the versioned SDK (Story E9);
// these aliases keep every existing import path working unchanged.
type (
	// CompletionRequest is the provider-agnostic input to an LLM call.
	CompletionRequest = sdkllm.CompletionRequest
	// ChatMessage mirrors the role/content structure used by all major LLM APIs.
	ChatMessage = sdkllm.ChatMessage
	// ToolSchema is the JSON Schema description of a callable tool.
	ToolSchema = sdkllm.ToolSchema
	// CompletionResponse carries the LLM's answer back to the runtime.
	CompletionResponse = sdkllm.CompletionResponse
	// Provider is the interface every LLM inference backend implements.
	Provider = sdkllm.Provider
)

// Router dispatches LLM calls to registered providers.
type Router struct {
	mu        sync.RWMutex
	providers map[string]Provider
	defaultID string
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
