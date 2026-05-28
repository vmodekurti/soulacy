// Package qdrant implements vector.Backend against a Qdrant vector database
// using its REST HTTP API (no gRPC / protobuf dependency).
//
// Qdrant REST endpoints used:
//
//	PUT  /collections/{col}/points          — upsert one point
//	POST /collections/{col}/points/search   — KNN search
//	PUT  /collections/{col}                 — create collection (one-time setup)
//
// Authentication: pass api_key in the Authorization header when Qdrant is
// configured with an API key ("api-key" auth type).
//
// Collection naming: each agentID gets its own Qdrant collection by default
// (col = "soulacy_" + agentID). When agentID is empty (global search), the
// caller passes agentID="" and the search fans out across all known collections.
// For a shared-collection deployment, set VectorCollection in the config and
// filtering is done via payload field "agent_id".
package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/internal/vector"
)

// compile-time interface check
var _ vector.Backend = (*Store)(nil)

// Store implements vector.Backend using Qdrant's REST API.
type Store struct {
	baseURL    string // e.g. "http://localhost:6333"
	collection string // Qdrant collection name
	apiKey     string // optional; sent as "api-key" header
	dims       int    // embedding dimensions
	embedder   memory.Embedder
	client     *http.Client
}

// Config holds constructor parameters.
type Config struct {
	// BaseURL is the Qdrant server URL, e.g. "http://localhost:6333".
	BaseURL string
	// Collection is the Qdrant collection to use. Created automatically.
	Collection string
	// APIKey is the optional Qdrant API key.
	APIKey string
	// Dims is the embedding dimensionality (must match the embedder).
	Dims int
	// Embedder converts text to float vectors.
	Embedder memory.Embedder
}

// New creates a Store and ensures the Qdrant collection exists.
// Returns an error if the collection cannot be created or verified.
func New(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.Collection == "" {
		cfg.Collection = "soulacy_memory"
	}
	s := &Store{
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		collection: cfg.Collection,
		apiKey:     cfg.APIKey,
		dims:       cfg.Dims,
		embedder:   cfg.Embedder,
		client:     &http.Client{Timeout: 15 * time.Second},
	}
	if err := s.ensureCollection(ctx); err != nil {
		return nil, fmt.Errorf("qdrant: ensure collection: %w", err)
	}
	return s, nil
}

// ---------------------------------------------------------------------------
// Backend implementation
// ---------------------------------------------------------------------------

// Write embeds entry.Content and upserts it as a Qdrant point.
// The point payload contains agentID, sessionID, scope, content, etc. for
// filtering and join-back after search.
func (s *Store) Write(ctx context.Context, entry memory.Entry) error {
	vec, err := s.embedder.Embed(ctx, entry.Content)
	if err != nil {
		return fmt.Errorf("qdrant: embed: %w", err)
	}

	// Qdrant point ID must be a UUID or unsigned integer.
	pointID := entry.ID
	if pointID == "" {
		pointID = uuid.New().String()
	}

	payload := map[string]any{
		"agent_id":   entry.AgentID,
		"session_id": entry.SessionID,
		"scope":      string(entry.Scope),
		"provenance": string(entry.Provenance),
		"key":        entry.Key,
		"content":    entry.Content,
		"created_at": entry.CreatedAt.UTC().Format(time.RFC3339),
	}
	if entry.ExpiresAt != nil {
		payload["expires_at"] = entry.ExpiresAt.UTC().Format(time.RFC3339)
	}

	body, _ := json.Marshal(map[string]any{
		"points": []map[string]any{
			{
				"id":      pointID,
				"vector":  vec,
				"payload": payload,
			},
		},
	})

	url := fmt.Sprintf("%s/collections/%s/points", s.baseURL, s.collection)
	if err := s.do(ctx, http.MethodPut, url, body, nil); err != nil {
		return fmt.Errorf("qdrant: upsert: %w", err)
	}
	return nil
}

// Search embeds query and returns the topK most similar entries.
// When agentID is non-empty a payload filter is applied so only that agent's
// memories are returned.
func (s *Store) Search(ctx context.Context, agentID, query string, topK int) ([]vector.Result, error) {
	if topK <= 0 {
		topK = 5
	}

	vec, err := s.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("qdrant: embed query: %w", err)
	}

	req := map[string]any{
		"vector":       vec,
		"limit":        topK,
		"with_payload": true,
	}
	if agentID != "" {
		req["filter"] = map[string]any{
			"must": []map[string]any{
				{
					"key": "agent_id",
					"match": map[string]any{
						"value": agentID,
					},
				},
			},
		}
	}

	body, _ := json.Marshal(req)
	url := fmt.Sprintf("%s/collections/%s/points/search", s.baseURL, s.collection)

	var resp struct {
		Result []struct {
			Score   float64        `json:"score"`
			Payload map[string]any `json:"payload"`
		} `json:"result"`
	}
	if err := s.do(ctx, http.MethodPost, url, body, &resp); err != nil {
		return nil, fmt.Errorf("qdrant: search: %w", err)
	}

	results := make([]vector.Result, 0, len(resp.Result))
	for _, hit := range resp.Result {
		e := payloadToEntry(hit.Payload)
		// Qdrant returns score (higher=better); convert to distance (lower=better).
		results = append(results, vector.Result{
			Entry:    e,
			Distance: 1.0 - float64(hit.Score),
		})
	}
	return results, nil
}

// Close is a no-op (HTTP client has no persistent connection lifecycle).
func (s *Store) Close() error { return nil }

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// ensureCollection creates the Qdrant collection if it doesn't already exist.
// Uses a cosine distance metric (appropriate for normalised text embeddings).
func (s *Store) ensureCollection(ctx context.Context) error {
	// Check if it exists first (PUT is idempotent in newer Qdrant, but older
	// versions return 400 when recreating).
	checkURL := fmt.Sprintf("%s/collections/%s", s.baseURL, s.collection)
	if err := s.do(ctx, http.MethodGet, checkURL, nil, nil); err == nil {
		return nil // already exists
	}

	createBody, _ := json.Marshal(map[string]any{
		"vectors": map[string]any{
			"size":     s.dims,
			"distance": "Cosine",
		},
	})
	putURL := fmt.Sprintf("%s/collections/%s", s.baseURL, s.collection)
	return s.do(ctx, http.MethodPut, putURL, createBody, nil)
}

// do executes an HTTP request and decodes the JSON response into out (if non-nil).
// Returns an error for non-2xx responses.
func (s *Store) do(ctx context.Context, method, url string, body []byte, out any) error {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.apiKey != "" {
		req.Header.Set("api-key", s.apiKey)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// payloadToEntry converts a Qdrant point payload map back into a memory.Entry.
func payloadToEntry(p map[string]any) memory.Entry {
	str := func(key string) string {
		v, _ := p[key].(string)
		return v
	}
	e := memory.Entry{
		AgentID:    str("agent_id"),
		SessionID:  str("session_id"),
		Scope:      memory.Scope(str("scope")),
		Provenance: memory.ProvenanceLabel(str("provenance")),
		Key:        str("key"),
		Content:    str("content"),
	}
	if ts := str("created_at"); ts != "" {
		t, _ := time.Parse(time.RFC3339, ts)
		e.CreatedAt = t
	}
	if ts := str("expires_at"); ts != "" {
		t, _ := time.Parse(time.RFC3339, ts)
		e.ExpiresAt = &t
	}
	return e
}
