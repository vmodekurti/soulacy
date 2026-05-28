// Package sqlitevec wraps *memory.VectorStore to satisfy vector.Backend.
// No new dependencies: it reuses the sqlite-vec virtual table already embedded
// in the memory package. This adapter exists purely to provide the
// compile-time interface guarantee and adapt SearchResult → vector.Result.
package sqlitevec

import (
	"context"
	"fmt"

	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/internal/vector"
)

// compile-time interface check
var _ vector.Backend = (*Store)(nil)

// Store wraps *memory.VectorStore.
type Store struct {
	vs *memory.VectorStore
}

// New wraps an existing *memory.VectorStore in the vector.Backend interface.
func New(vs *memory.VectorStore) *Store {
	return &Store{vs: vs}
}

// Write embeds entry.Content and inserts it into the sqlite-vec index.
func (s *Store) Write(ctx context.Context, entry memory.Entry) error {
	return s.vs.Write(ctx, entry)
}

// Search embeds query and returns the topK most similar entries.
// agentID is used as a post-filter (sqlite-vec KNN returns global results;
// we filter by agentID after the scan when agentID is non-empty).
func (s *Store) Search(ctx context.Context, agentID, query string, topK int) ([]vector.Result, error) {
	// Ask for extra candidates when filtering by agentID so we still get topK
	// results after the filter (over-fetch heuristic: 3×).
	fetchK := topK
	if agentID != "" {
		fetchK = topK * 3
		if fetchK < 15 {
			fetchK = 15
		}
	}

	raw, err := s.vs.Search(ctx, query, fetchK)
	if err != nil {
		return nil, fmt.Errorf("sqlitevec: search: %w", err)
	}

	results := make([]vector.Result, 0, len(raw))
	for _, r := range raw {
		if agentID != "" && r.Entry.AgentID != agentID {
			continue
		}
		results = append(results, vector.Result{
			Entry:    r.Entry,
			Distance: r.Distance,
		})
		if len(results) >= topK {
			break
		}
	}
	return results, nil
}

// Close is a no-op; the underlying *sql.DB is owned by memory.SQLiteArchive
// and closed via MemoryBackend.Close().
func (s *Store) Close() error { return nil }
