// Package vector defines the provider-agnostic interface for Soulacy's
// semantic (vector) memory tier.
//
// Two implementations ship out of the box:
//
//	internal/vector/sqlitevec — sqlite-vec virtual table (zero-dependency, default)
//	internal/vector/qdrant    — Qdrant via REST API (production scale-out)
//
// Active backend is selected at startup from config.yaml:
//
//	memory:
//	  vector_db: sqlite-vec   # or "qdrant"
//	  vector_url: ""          # Qdrant URL when vector_db == "qdrant"
//
// Engine and memory subsystem code imports only this package; they never
// reference a concrete implementation directly.
package vector

import (
	"context"

	"github.com/soulacy/soulacy/internal/memory"
)

// Result is one hit from a semantic search.
type Result struct {
	Entry    memory.Entry
	Distance float64 // cosine or L2 distance; lower = more similar
}

// Backend is the interface satisfied by every vector-store implementation.
type Backend interface {
	// Write embeds entry.Content and stores it in the vector index.
	Write(ctx context.Context, entry memory.Entry) error

	// Search embeds query and returns the topK most similar entries.
	// An empty agentID searches across all agents (Qdrant supports filters;
	// sqlite-vec searches the full index and returns results for any agent).
	Search(ctx context.Context, agentID, query string, topK int) ([]Result, error)

	// Close releases all held resources (connections, goroutines, etc.).
	Close() error
}
