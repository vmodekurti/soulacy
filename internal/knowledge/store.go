// Package knowledge implements RAG storage: knowledge bases, documents,
// chunks, and vector search backed by SQLite + sqlite-vec.
//
// Layout
//
//	knowledge_bases — KB rows (one per named KB)
//	documents       — source files / pasted texts ingested into a KB
//	chunks          — text chunks produced by the chunker
//	vec_<kb_id>     — vec0 virtual table holding the embedding for each chunk
//	                  (one per KB so different KBs can use different dims)
//
// The vec0 extension is loaded automatically via sqlite_vec.Auto() into every
// subsequent connection opened by the mattn/go-sqlite3 driver. No external
// .dylib lookup is needed — the extension is compiled in via cgo.
package knowledge

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"

	"github.com/soulacy/soulacy/internal/sqlitex"
)

// KB describes a single knowledge base.
type KB struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Description       string    `json:"description"`
	EmbeddingProvider string    `json:"embedding_provider"`
	EmbeddingModel    string    `json:"embedding_model"`
	Dim               int       `json:"dim"`
	ChunkSize         int       `json:"chunk_size"`
	ChunkOverlap      int       `json:"chunk_overlap"`
	DocumentCount     int       `json:"document_count"`
	ChunkCount        int       `json:"chunk_count"`
	CreatedAt         time.Time `json:"created_at"`
}

// Document is one ingested source.
type Document struct {
	ID         string    `json:"id"`
	KBID       string    `json:"kb_id"`
	Title      string    `json:"title"`
	Source     string    `json:"source"`
	MIMEType   string    `json:"mime_type"`
	ByteSize   int64     `json:"byte_size"`
	SHA256     string    `json:"sha256"`
	ChunkCount int       `json:"chunk_count"`
	CreatedAt  time.Time `json:"created_at"`
}

// Chunk is one piece of a document, ready to embed or already embedded.
type Chunk struct {
	ID      string    `json:"id"`
	DocID   string    `json:"doc_id"`
	KBID    string    `json:"kb_id"`
	Ordinal int       `json:"ordinal"`
	Content string    `json:"content"`
	Vector  []float32 `json:"-"`
}

// SearchHit is one result from a vector search.
type SearchHit struct {
	ChunkID   string  `json:"chunk_id"`
	DocID     string  `json:"doc_id"`
	DocTitle  string  `json:"doc_title"`
	DocSource string  `json:"doc_source"`
	Ordinal   int     `json:"ordinal"`
	Content   string  `json:"content"`
	Distance  float64 `json:"distance"`
}

// Store is the SQLite-backed knowledge store.
type Store struct {
	db *sql.DB
	mu sync.RWMutex // guards CREATE/DROP of per-KB vec0 tables
}

var autoOnce sync.Once

// Open opens (or creates) the knowledge DB at path and ensures the schema is
// present. sqlite-vec is auto-loaded into every new connection on the driver.
func Open(path string) (*Store, error) {
	autoOnce.Do(func() {
		// Registers a sqlite3_auto_extension hook so every subsequent
		// connection opened by the driver loads vec0. Safe to call multiple
		// times in theory but mattn warns about it, so we gate it.
		sqlite_vec.Auto()
	})

	// PRODUCTION_AUDIT → F3 (2026-05-27): WAL + NORMAL synchronous + 30s
	// busy_timeout + 256 MiB mmap (vec0 KNN reads scan rows linearly so
	// memory-mapped I/O helps notably) + foreign-key cascades + tuned pool.
	opts := sqlitex.DefaultOptions()
	opts.ForeignKeys = true
	opts.MMapSize = 256 << 20
	db, err := sqlitex.Open(path, opts)
	if err != nil {
		return nil, fmt.Errorf("knowledge: open sqlite %s: %w", path, err)
	}

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS knowledge_bases (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			description TEXT,
			embedding_provider TEXT NOT NULL,
			embedding_model TEXT NOT NULL,
			dim INTEGER NOT NULL,
			chunk_size INTEGER NOT NULL,
			chunk_overlap INTEGER NOT NULL,
			created_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS documents (
			id TEXT PRIMARY KEY,
			kb_id TEXT NOT NULL,
			title TEXT NOT NULL,
			source TEXT,
			mime_type TEXT,
			byte_size INTEGER,
			sha256 TEXT,
			created_at DATETIME NOT NULL,
			FOREIGN KEY(kb_id) REFERENCES knowledge_bases(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS chunks (
			id TEXT PRIMARY KEY,
			doc_id TEXT NOT NULL,
			kb_id TEXT NOT NULL,
			ordinal INTEGER NOT NULL,
			content TEXT NOT NULL,
			FOREIGN KEY(doc_id) REFERENCES documents(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_documents_kb ON documents(kb_id)`,
		`CREATE INDEX IF NOT EXISTS idx_chunks_doc ON chunks(doc_id)`,
		`CREATE INDEX IF NOT EXISTS idx_chunks_kb ON chunks(kb_id)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return nil, fmt.Errorf("knowledge: schema (%q): %w", s[:minInt(len(s), 60)], err)
		}
	}

	// Confirm vec0 is loaded so we fail fast instead of at first KB create.
	var vecVer string
	if err := db.QueryRow(`SELECT vec_version()`).Scan(&vecVer); err != nil {
		return nil, fmt.Errorf("knowledge: sqlite-vec not loaded (vec_version() failed): %w", err)
	}

	return &Store{db: db}, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// vecTable returns the per-KB virtual table name (safe — KB IDs are uuids
// with dashes replaced by underscores).
func vecTable(kbID string) string {
	return `"vec_` + strings.ReplaceAll(kbID, "-", "_") + `"`
}

// CreateKB inserts a new KB row and creates its vec0 virtual table.
// Name uniqueness is enforced by the UNIQUE index on knowledge_bases.name.
func (s *Store) CreateKB(kb KB) (*KB, error) {
	if kb.Name == "" {
		return nil, errors.New("knowledge: name is required")
	}
	if kb.Dim <= 0 {
		return nil, errors.New("knowledge: dim must be positive")
	}
	if kb.ID == "" {
		kb.ID = uuid.New().String()
	}
	if kb.ChunkSize <= 0 {
		kb.ChunkSize = 1000
	}
	if kb.ChunkOverlap < 0 {
		kb.ChunkOverlap = 200
	}
	if kb.CreatedAt.IsZero() {
		kb.CreatedAt = time.Now().UTC()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	_, err = tx.Exec(`INSERT INTO knowledge_bases
		(id, name, description, embedding_provider, embedding_model, dim, chunk_size, chunk_overlap, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		kb.ID, kb.Name, kb.Description, kb.EmbeddingProvider, kb.EmbeddingModel,
		kb.Dim, kb.ChunkSize, kb.ChunkOverlap, kb.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("knowledge: insert kb: %w", err)
	}

	// vec0 virtual table — one per KB, dim fixed.
	createVec := fmt.Sprintf(
		`CREATE VIRTUAL TABLE IF NOT EXISTS %s USING vec0(chunk_id TEXT PRIMARY KEY, embedding FLOAT[%d])`,
		vecTable(kb.ID), kb.Dim,
	)
	if _, err := tx.Exec(createVec); err != nil {
		return nil, fmt.Errorf("knowledge: create vec0 table: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &kb, nil
}

// ListKBs returns all knowledge bases with up-to-date doc/chunk counts.
func (s *Store) ListKBs() ([]KB, error) {
	rows, err := s.db.Query(`
		SELECT kb.id, kb.name, COALESCE(kb.description,''), kb.embedding_provider, kb.embedding_model,
		       kb.dim, kb.chunk_size, kb.chunk_overlap, kb.created_at,
		       (SELECT COUNT(*) FROM documents d WHERE d.kb_id = kb.id) AS doc_count,
		       (SELECT COUNT(*) FROM chunks c WHERE c.kb_id = kb.id) AS chunk_count
		FROM knowledge_bases kb
		ORDER BY kb.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []KB
	for rows.Next() {
		var kb KB
		if err := rows.Scan(
			&kb.ID, &kb.Name, &kb.Description, &kb.EmbeddingProvider, &kb.EmbeddingModel,
			&kb.Dim, &kb.ChunkSize, &kb.ChunkOverlap, &kb.CreatedAt,
			&kb.DocumentCount, &kb.ChunkCount,
		); err != nil {
			return nil, err
		}
		out = append(out, kb)
	}
	return out, rows.Err()
}

// GetKB looks up a KB by name (case-sensitive). Returns nil if not found.
func (s *Store) GetKB(name string) (*KB, error) {
	var kb KB
	err := s.db.QueryRow(`
		SELECT kb.id, kb.name, COALESCE(kb.description,''), kb.embedding_provider, kb.embedding_model,
		       kb.dim, kb.chunk_size, kb.chunk_overlap, kb.created_at,
		       (SELECT COUNT(*) FROM documents d WHERE d.kb_id = kb.id),
		       (SELECT COUNT(*) FROM chunks c WHERE c.kb_id = kb.id)
		FROM knowledge_bases kb WHERE kb.name = ?`, name).Scan(
		&kb.ID, &kb.Name, &kb.Description, &kb.EmbeddingProvider, &kb.EmbeddingModel,
		&kb.Dim, &kb.ChunkSize, &kb.ChunkOverlap, &kb.CreatedAt,
		&kb.DocumentCount, &kb.ChunkCount,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &kb, nil
}

// DeleteKB drops the KB row, all docs/chunks (via cascade), and the vec0 table.
func (s *Store) DeleteKB(name string) error {
	kb, err := s.GetKB(name)
	if err != nil {
		return err
	}
	if kb == nil {
		return nil // idempotent
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec(`DELETE FROM knowledge_bases WHERE id = ?`, kb.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE IF EXISTS ` + vecTable(kb.ID)); err != nil {
		return err
	}
	return tx.Commit()
}

// AddDocument writes a document row and its chunks (with embeddings) atomically.
// All chunks in `chunks` MUST have Vector populated and len(Vector) == kb.Dim.
func (s *Store) AddDocument(kb *KB, doc Document, chunks []Chunk) (*Document, error) {
	if kb == nil {
		return nil, errors.New("knowledge: kb is nil")
	}
	if doc.ID == "" {
		doc.ID = uuid.New().String()
	}
	doc.KBID = kb.ID
	if doc.CreatedAt.IsZero() {
		doc.CreatedAt = time.Now().UTC()
	}
	if doc.SHA256 == "" && len(chunks) > 0 {
		h := sha256.New()
		for _, c := range chunks {
			h.Write([]byte(c.Content))
		}
		doc.SHA256 = hex.EncodeToString(h.Sum(nil))
	}

	for i, c := range chunks {
		if len(c.Vector) != kb.Dim {
			return nil, fmt.Errorf("knowledge: chunk %d vector dim %d != kb dim %d", i, len(c.Vector), kb.Dim)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec(`INSERT INTO documents
		(id, kb_id, title, source, mime_type, byte_size, sha256, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		doc.ID, doc.KBID, doc.Title, doc.Source, doc.MIMEType, doc.ByteSize, doc.SHA256, doc.CreatedAt,
	); err != nil {
		return nil, fmt.Errorf("knowledge: insert document: %w", err)
	}

	chunkStmt, err := tx.Prepare(`INSERT INTO chunks (id, doc_id, kb_id, ordinal, content) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return nil, err
	}
	defer chunkStmt.Close()

	vecStmt, err := tx.Prepare(fmt.Sprintf(`INSERT INTO %s (chunk_id, embedding) VALUES (?, ?)`, vecTable(kb.ID)))
	if err != nil {
		return nil, fmt.Errorf("knowledge: prepare vec insert: %w", err)
	}
	defer vecStmt.Close()

	for i, c := range chunks {
		if c.ID == "" {
			c.ID = uuid.New().String()
		}
		c.DocID = doc.ID
		c.KBID = kb.ID
		c.Ordinal = i
		if _, err := chunkStmt.Exec(c.ID, c.DocID, c.KBID, c.Ordinal, c.Content); err != nil {
			return nil, fmt.Errorf("knowledge: insert chunk: %w", err)
		}
		blob, err := sqlite_vec.SerializeFloat32(c.Vector)
		if err != nil {
			return nil, fmt.Errorf("knowledge: serialize embedding: %w", err)
		}
		if _, err := vecStmt.Exec(c.ID, blob); err != nil {
			return nil, fmt.Errorf("knowledge: insert vec: %w", err)
		}
	}

	doc.ChunkCount = len(chunks)
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &doc, nil
}

// ListDocuments returns documents in a KB, newest first.
func (s *Store) ListDocuments(kbID string) ([]Document, error) {
	rows, err := s.db.Query(`
		SELECT d.id, d.kb_id, d.title, COALESCE(d.source,''), COALESCE(d.mime_type,''),
		       COALESCE(d.byte_size,0), COALESCE(d.sha256,''), d.created_at,
		       (SELECT COUNT(*) FROM chunks c WHERE c.doc_id = d.id) AS chunk_count
		FROM documents d WHERE d.kb_id = ? ORDER BY d.created_at DESC`, kbID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Document
	for rows.Next() {
		var d Document
		if err := rows.Scan(&d.ID, &d.KBID, &d.Title, &d.Source, &d.MIMEType,
			&d.ByteSize, &d.SHA256, &d.CreatedAt, &d.ChunkCount); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeleteDocument removes a document, cascading chunks, and clears the matching
// rows from the KB's vec0 table.
func (s *Store) DeleteDocument(kbID, docID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	// vec0 tables don't honour FK cascade, so wipe by id list first.
	if _, err := tx.Exec(fmt.Sprintf(
		`DELETE FROM %s WHERE chunk_id IN (SELECT id FROM chunks WHERE doc_id = ?)`,
		vecTable(kbID),
	), docID); err != nil {
		return fmt.Errorf("knowledge: delete vec rows: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM documents WHERE id = ? AND kb_id = ?`, docID, kbID); err != nil {
		return err
	}
	return tx.Commit()
}

// Search runs a vec0 KNN query against the KB's chunk table. `vector` must
// match the KB's dim. Returns up to topK hits ordered by ascending distance.
func (s *Store) Search(kb *KB, vector []float32, topK int) ([]SearchHit, error) {
	if kb == nil {
		return nil, errors.New("knowledge: kb is nil")
	}
	if len(vector) != kb.Dim {
		return nil, fmt.Errorf("knowledge: query vector dim %d != kb dim %d", len(vector), kb.Dim)
	}
	if topK <= 0 {
		topK = 5
	}

	blob, err := sqlite_vec.SerializeFloat32(vector)
	if err != nil {
		return nil, err
	}

	query := fmt.Sprintf(`
		SELECT v.chunk_id, v.distance, c.doc_id, c.ordinal, c.content,
		       d.title, COALESCE(d.source,'')
		FROM %s v
		JOIN chunks c ON c.id = v.chunk_id
		JOIN documents d ON d.id = c.doc_id
		WHERE v.embedding MATCH ?
		  AND v.k = ?
		ORDER BY v.distance`, vecTable(kb.ID))

	rows, err := s.db.Query(query, blob, topK)
	if err != nil {
		return nil, fmt.Errorf("knowledge: vec search: %w", err)
	}
	defer rows.Close()

	var hits []SearchHit
	for rows.Next() {
		var h SearchHit
		if err := rows.Scan(&h.ChunkID, &h.Distance, &h.DocID, &h.Ordinal, &h.Content, &h.DocTitle, &h.DocSource); err != nil {
			return nil, err
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
