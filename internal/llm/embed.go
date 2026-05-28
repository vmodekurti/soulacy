// embed.go — embedding client for the RAG pipeline.
//
// Embedders are intentionally lightweight and decoupled from the chat
// Provider interface — we want to add a brand-new embedding model without
// touching the chat router. Two providers ship:
//
//	ollama → POST <baseURL>/api/embed       (default: nomic-embed-text, 768 dims)
//	openai → POST <baseURL>/v1/embeddings   (default: text-embedding-3-small, 1536 dims)
//
// Either can target a custom baseURL via the embed.provider's config so
// OpenAI-compatible servers (Together, Groq, vLLM) work transparently.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Embedder turns text into fixed-dim float32 vectors.
type Embedder interface {
	ID() string
	// Embed returns one vector per input string. All vectors share Dim().
	Embed(ctx context.Context, model string, texts []string) ([][]float32, error)
	// Dim reports the embedding dimensionality for the named model. May make a
	// probe call if not cached. Returns 0 + error if the model is unknown.
	Dim(ctx context.Context, model string) (int, error)
}

// OllamaEmbedder calls Ollama's /api/embed endpoint.
type OllamaEmbedder struct {
	baseURL string
	client  *http.Client
	// dimCache memoises the dim per model so we don't probe twice.
	// Guarded by dimMu so concurrent kb_search calls embedding the same
	// (previously unseen) model don't race on the map. Without this, Go's
	// runtime panics on concurrent writes (PRODUCTION_AUDIT.md → HIGH).
	dimMu    sync.Mutex
	dimCache map[string]int
}

// NewOllamaEmbedder targets the given Ollama base URL (no trailing slash).
func NewOllamaEmbedder(baseURL string) *OllamaEmbedder {
	return &OllamaEmbedder{
		baseURL:  strings.TrimRight(baseURL, "/"),
		client:   SharedHTTPClient(60 * time.Second),
		dimCache: map[string]int{},
	}
}

func (e *OllamaEmbedder) ID() string { return "ollama" }

// ollamaEmbedRequest is the JSON body for /api/embed. Ollama accepts either a
// single string or a list under "input". We always pass a list.
type ollamaEmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type ollamaEmbedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
	Error      string      `json:"error,omitempty"`
}

// Embed sends the texts to Ollama in a single batched call.
func (e *OllamaEmbedder) Embed(ctx context.Context, model string, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if model == "" {
		model = "nomic-embed-text"
	}

	body, _ := json.Marshal(ollamaEmbedRequest{Model: model, Input: texts})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama embed: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("ollama embed: HTTP %d: %s", resp.StatusCode, string(raw))
	}

	var out ollamaEmbedResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("ollama embed: decode: %w (body=%s)", err, string(raw))
	}
	if out.Error != "" {
		return nil, fmt.Errorf("ollama embed: %s", out.Error)
	}
	if len(out.Embeddings) != len(texts) {
		return nil, fmt.Errorf("ollama embed: returned %d embeddings, expected %d", len(out.Embeddings), len(texts))
	}

	// Cache dim on success.
	if len(out.Embeddings) > 0 && len(out.Embeddings[0]) > 0 {
		e.dimMu.Lock()
		e.dimCache[model] = len(out.Embeddings[0])
		e.dimMu.Unlock()
	}
	return out.Embeddings, nil
}

// Dim returns the embedding size for the model, probing with a tiny request
// if we haven't seen it before.
func (e *OllamaEmbedder) Dim(ctx context.Context, model string) (int, error) {
	e.dimMu.Lock()
	d, ok := e.dimCache[model]
	e.dimMu.Unlock()
	if ok {
		return d, nil
	}
	vecs, err := e.Embed(ctx, model, []string{"probe"})
	if err != nil {
		return 0, err
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return 0, fmt.Errorf("ollama embed: probe returned empty vector for model %q", model)
	}
	return len(vecs[0]), nil
}

// OpenAIEmbedder calls /v1/embeddings on any OpenAI-compatible host.
type OpenAIEmbedder struct {
	baseURL string
	apiKey  string
	client  *http.Client
	// dimMu guards dimCache against concurrent kb_search calls embedding the
	// same previously-unseen model (PRODUCTION_AUDIT.md → HIGH).
	dimMu    sync.Mutex
	dimCache map[string]int
}

// NewOpenAIEmbedder constructs the embedder. baseURL defaults to
// https://api.openai.com when empty.
func NewOpenAIEmbedder(baseURL, apiKey string) *OpenAIEmbedder {
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	return &OpenAIEmbedder{
		baseURL:  strings.TrimRight(baseURL, "/"),
		apiKey:   apiKey,
		client:   SharedHTTPClient(60 * time.Second),
		dimCache: map[string]int{},
	}
}

func (e *OpenAIEmbedder) ID() string { return "openai" }

type openAIEmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type openAIEmbedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (e *OpenAIEmbedder) Embed(ctx context.Context, model string, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if model == "" {
		model = "text-embedding-3-small"
	}

	body, _ := json.Marshal(openAIEmbedRequest{Model: model, Input: texts})
	url := e.baseURL + "/v1/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai embed: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("openai embed: HTTP %d: %s", resp.StatusCode, string(raw))
	}

	var out openAIEmbedResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("openai embed: decode: %w (body=%s)", err, string(raw))
	}
	if out.Error != nil {
		return nil, fmt.Errorf("openai embed: %s", out.Error.Message)
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("openai embed: returned %d, expected %d", len(out.Data), len(texts))
	}
	vecs := make([][]float32, len(out.Data))
	for i, d := range out.Data {
		vecs[i] = d.Embedding
	}
	if len(vecs) > 0 && len(vecs[0]) > 0 {
		e.dimMu.Lock()
		e.dimCache[model] = len(vecs[0])
		e.dimMu.Unlock()
	}
	return vecs, nil
}

func (e *OpenAIEmbedder) Dim(ctx context.Context, model string) (int, error) {
	e.dimMu.Lock()
	d, ok := e.dimCache[model]
	e.dimMu.Unlock()
	if ok {
		return d, nil
	}
	vecs, err := e.Embed(ctx, model, []string{"probe"})
	if err != nil {
		return 0, err
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return 0, fmt.Errorf("openai embed: empty probe for %q", model)
	}
	return len(vecs[0]), nil
}

// EmbedderRegistry is a small lookup so the gateway can resolve an embedder
// by provider id at runtime. Construct one at startup and stash it on the
// Server.
//
// Currently Register is only called during boot, so reads after Register
// returns are race-free in practice. The mutex is here so a future
// "register an embedder at runtime" flow (e.g. via the Providers GUI) can't
// trip Go's concurrent-map-write detector. (PRODUCTION_AUDIT.md → HIGH.)
type EmbedderRegistry struct {
	mu   sync.RWMutex
	byID map[string]Embedder
}

// NewEmbedderRegistry constructs an empty registry.
func NewEmbedderRegistry() *EmbedderRegistry {
	return &EmbedderRegistry{byID: map[string]Embedder{}}
}

// Register adds an embedder, keyed by its ID().
func (r *EmbedderRegistry) Register(e Embedder) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[e.ID()] = e
}

// Get looks up an embedder by provider id. Returns nil if not registered.
func (r *EmbedderRegistry) Get(id string) Embedder {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byID[id]
}

// IDs returns all registered embedder IDs (stable order not guaranteed).
func (r *EmbedderRegistry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.byID))
	for id := range r.byID {
		out = append(out, id)
	}
	return out
}
