// store_test.go — round-trip tests for the knowledge store. Uses a temp
// SQLite file under t.TempDir() so each test gets a fresh schema. Tests
// real sqlite-vec because the autoOnce + cgo binding makes a stub awkward;
// build with CGO_ENABLED=1 (the project default).
package knowledge

import (
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kb.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestStore_CreateAndListKB(t *testing.T) {
	s := newTestStore(t)

	kb, err := s.CreateKB(KB{
		Name: "test-kb", Description: "for tests",
		EmbeddingProvider: "ollama", EmbeddingModel: "nomic-embed-text",
		Dim: 768, ChunkSize: 500, ChunkOverlap: 100,
	})
	if err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	if kb.ID == "" {
		t.Error("CreateKB should assign an ID")
	}
	if kb.Dim != 768 {
		t.Errorf("dim: got %d, want 768", kb.Dim)
	}

	kbs, err := s.ListKBs()
	if err != nil {
		t.Fatalf("ListKBs: %v", err)
	}
	if len(kbs) != 1 {
		t.Fatalf("ListKBs: got %d KBs, want 1", len(kbs))
	}
	if kbs[0].Name != "test-kb" {
		t.Errorf("name: got %q, want test-kb", kbs[0].Name)
	}
}

func TestStore_DuplicateKBNameRejected(t *testing.T) {
	s := newTestStore(t)
	base := KB{Name: "dup", EmbeddingProvider: "ollama", EmbeddingModel: "nomic-embed-text", Dim: 4}
	if _, err := s.CreateKB(base); err != nil {
		t.Fatalf("first CreateKB: %v", err)
	}
	if _, err := s.CreateKB(base); err == nil {
		t.Error("expected duplicate-name CreateKB to fail, got nil")
	}
}

func TestStore_AddDocumentAndSearch(t *testing.T) {
	s := newTestStore(t)
	kb, err := s.CreateKB(KB{
		Name: "search-kb",
		EmbeddingProvider: "test", EmbeddingModel: "fake",
		Dim: 4, ChunkSize: 100, ChunkOverlap: 0,
	})
	if err != nil {
		t.Fatalf("CreateKB: %v", err)
	}

	// Three chunks with simple distinct vectors so KNN ordering is predictable.
	chunks := []Chunk{
		{Content: "alpha document", Vector: []float32{1, 0, 0, 0}},
		{Content: "beta document",  Vector: []float32{0, 1, 0, 0}},
		{Content: "gamma document", Vector: []float32{0, 0, 1, 0}},
	}
	doc, err := s.AddDocument(kb, Document{Title: "vectors", Source: "test.md", MIMEType: "text/markdown"}, chunks)
	if err != nil {
		t.Fatalf("AddDocument: %v", err)
	}
	if doc.ChunkCount != 3 {
		t.Errorf("chunk_count: got %d, want 3", doc.ChunkCount)
	}

	// Query close to chunk 0's vector — it should rank first.
	hits, err := s.Search(kb, []float32{0.9, 0.1, 0, 0}, 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits: got %d, want 2", len(hits))
	}
	if hits[0].Content != "alpha document" {
		t.Errorf("top hit: got %q, want %q", hits[0].Content, "alpha document")
	}
	if hits[0].Distance > hits[1].Distance {
		t.Errorf("hits not sorted by distance ascending: %v vs %v", hits[0].Distance, hits[1].Distance)
	}
}

func TestStore_AddDocumentRejectsWrongDim(t *testing.T) {
	s := newTestStore(t)
	kb, _ := s.CreateKB(KB{Name: "dim-kb", EmbeddingProvider: "x", EmbeddingModel: "x", Dim: 4})
	bad := []Chunk{{Content: "wrong size", Vector: []float32{1, 0, 0}}} // 3 dims, not 4
	if _, err := s.AddDocument(kb, Document{Title: "bad"}, bad); err == nil {
		t.Error("expected AddDocument to reject mismatched-dim chunk, got nil")
	}
}

func TestStore_DeleteDocumentCascadesToChunks(t *testing.T) {
	s := newTestStore(t)
	kb, _ := s.CreateKB(KB{Name: "cascade", EmbeddingProvider: "x", EmbeddingModel: "x", Dim: 2})
	chunks := []Chunk{
		{Content: "a", Vector: []float32{1, 0}},
		{Content: "b", Vector: []float32{0, 1}},
	}
	doc, err := s.AddDocument(kb, Document{Title: "tmp"}, chunks)
	if err != nil {
		t.Fatalf("AddDocument: %v", err)
	}

	if err := s.DeleteDocument(kb.ID, doc.ID); err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}

	// After delete, the KB's chunk count should be zero (vec0 rows + chunks
	// table both purged).
	got, err := s.GetKB("cascade")
	if err != nil || got == nil {
		t.Fatalf("GetKB after delete: %v / nil=%v", err, got == nil)
	}
	if got.ChunkCount != 0 {
		t.Errorf("post-delete chunk count: got %d, want 0", got.ChunkCount)
	}
}

func TestStore_DeleteKBIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	if err := s.DeleteKB("never-existed"); err != nil {
		t.Errorf("deleting nonexistent KB should be no-op, got %v", err)
	}
}
