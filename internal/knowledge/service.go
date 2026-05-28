// service.go — high-level RAG facade used by the runtime and gateway.
//
// Service combines the SQLite-backed Store with an embedding registry so
// callers don't have to think about which embedder to pick for which KB.
// The runtime engine talks ONLY to Service (no direct Store access) so
// future backends (e.g. a remote vector DB) can be swapped in transparently.
package knowledge

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/soulacy/soulacy/internal/llm"
)

// KBSummary is the lightweight view of a KB used when listing what's
// available to an agent (no schema details, just name + size).
type KBSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	DocCount    int    `json:"document_count"`
	ChunkCount  int    `json:"chunk_count"`
}

// Service is the RAG facade.
type Service struct {
	Store     *Store
	Embedders *llm.EmbedderRegistry

	// embedCache memoises query embeddings to avoid re-embedding the same
	// query when the agent iterates (PRODUCTION_AUDIT → HIGH/Caching).
	// Bounded LRU keyed by provider|model|query. ~200ms saved per cache hit.
	embedCacheMu sync.Mutex
	embedCache   *embedLRU
}

const embedCacheMaxEntries = 256

// NewService wires a Store and an EmbedderRegistry together.
func NewService(store *Store, embedders *llm.EmbedderRegistry) *Service {
	return &Service{
		Store:      store,
		Embedders:  embedders,
		embedCache: newEmbedLRU(embedCacheMaxEntries),
	}
}

// ListAvailable returns KB summaries for the requested names. Names that
// don't resolve to an existing KB are silently dropped (they may have been
// declared in SOUL.yaml before the KB was created). Supports the "*" / "all"
// wildcards used by Skills for symmetry.
func (s *Service) ListAvailable(names []string) []KBSummary {
	if s == nil || s.Store == nil {
		return nil
	}
	wantAll := false
	for _, n := range names {
		if n == "*" || n == "all" {
			wantAll = true
			break
		}
	}
	if wantAll {
		kbs, err := s.Store.ListKBs()
		if err != nil {
			return nil
		}
		out := make([]KBSummary, 0, len(kbs))
		for _, kb := range kbs {
			out = append(out, KBSummary{Name: kb.Name, Description: kb.Description, DocCount: kb.DocumentCount, ChunkCount: kb.ChunkCount})
		}
		return out
	}
	out := make([]KBSummary, 0, len(names))
	for _, n := range names {
		kb, err := s.Store.GetKB(n)
		if err != nil || kb == nil {
			continue
		}
		out = append(out, KBSummary{Name: kb.Name, Description: kb.Description, DocCount: kb.DocumentCount, ChunkCount: kb.ChunkCount})
	}
	return out
}

// Search embeds the query, runs a KNN against the named KB, and returns a
// pre-formatted text block ready for the LLM to consume as a tool result.
func (s *Service) Search(ctx context.Context, kbName, query string, topK int) (string, error) {
	if s == nil || s.Store == nil {
		return "", errors.New("knowledge: service not configured")
	}
	if strings.TrimSpace(query) == "" {
		return "", errors.New("kb_search: query is required")
	}
	if topK <= 0 {
		topK = 5
	}

	kb, err := s.Store.GetKB(kbName)
	if err != nil {
		return "", err
	}
	if kb == nil {
		return "", fmt.Errorf("kb_search: knowledge base %q not found", kbName)
	}

	embedder := s.Embedders.Get(kb.EmbeddingProvider)
	if embedder == nil {
		return "", fmt.Errorf("kb_search: no embedder registered for provider %q (KB %q)", kb.EmbeddingProvider, kbName)
	}

	// Cache hit avoids a round-trip to the embedder. Cache key includes the
	// provider+model so different KBs configured with different embedders
	// don't share a stale entry.
	cacheKey := kb.EmbeddingProvider + "|" + kb.EmbeddingModel + "|" + query
	var queryVec []float32
	s.embedCacheMu.Lock()
	if cached, ok := s.embedCache.get(cacheKey); ok {
		queryVec = cached
	}
	s.embedCacheMu.Unlock()
	if queryVec == nil {
		vecs, err := embedder.Embed(ctx, kb.EmbeddingModel, []string{query})
		if err != nil {
			return "", fmt.Errorf("kb_search: embed query: %w", err)
		}
		if len(vecs) == 0 || len(vecs[0]) == 0 {
			return "", fmt.Errorf("kb_search: empty embedding for query")
		}
		queryVec = vecs[0]
		s.embedCacheMu.Lock()
		s.embedCache.put(cacheKey, queryVec)
		s.embedCacheMu.Unlock()
	}

	hits, err := s.Store.Search(kb, queryVec, topK)
	if err != nil {
		return "", err
	}
	if len(hits) == 0 {
		return fmt.Sprintf("No matching passages found in knowledge base %q for query %q.", kbName, query), nil
	}

	return FormatHits(kbName, query, hits), nil
}

// FormatHits renders search results as a compact XML-ish block. Designed to
// minimise tokens while staying easy for the LLM to parse: each chunk is its
// own tag with source/score attributes.
func FormatHits(kbName, query string, hits []SearchHit) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<kb_results kb=%q query=%q hits=%d>\n", kbName, query, len(hits)))
	for i, h := range hits {
		// Trim absurdly long chunks so a single hit can't blow the context.
		content := h.Content
		if len(content) > 1500 {
			content = content[:1500] + "…"
		}
		sb.WriteString(fmt.Sprintf("  <chunk index=%d source=%q title=%q distance=%.4f>\n",
			i+1, h.DocSource, h.DocTitle, h.Distance))
		sb.WriteString("    ")
		sb.WriteString(strings.ReplaceAll(content, "\n", "\n    "))
		sb.WriteString("\n  </chunk>\n")
	}
	sb.WriteString("</kb_results>")
	return sb.String()
}

// ─── embedLRU: bounded LRU for query embeddings ────────────────────────────
//
// Tiny hand-rolled LRU. `list.List` for recency order; map for O(1) lookup.
// Not safe for concurrent access — Service guards it with embedCacheMu.

type embedLRU struct {
	max     int
	order   *list.List
	entries map[string]*list.Element
}

type embedEntry struct {
	key string
	vec []float32
}

func newEmbedLRU(max int) *embedLRU {
	return &embedLRU{
		max:     max,
		order:   list.New(),
		entries: make(map[string]*list.Element, max),
	}
}

func (c *embedLRU) get(k string) ([]float32, bool) {
	el, ok := c.entries[k]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*embedEntry).vec, true
}

func (c *embedLRU) put(k string, v []float32) {
	if el, ok := c.entries[k]; ok {
		el.Value.(*embedEntry).vec = v
		c.order.MoveToFront(el)
		return
	}
	el := c.order.PushFront(&embedEntry{key: k, vec: v})
	c.entries[k] = el
	if c.order.Len() > c.max {
		evict := c.order.Back()
		if evict != nil {
			c.order.Remove(evict)
			delete(c.entries, evict.Value.(*embedEntry).key)
		}
	}
}
