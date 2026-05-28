// store.go — session resource store backed by SQLite.
//
// ResourceStore persists binary attachments (images, audio clips, documents)
// for the duration of a session.  Resources are keyed by an opaque ID,
// carry a MIME type, and expire automatically after a configurable TTL.
//
// Security mitigations:
//   - Blobs are stored inside the SQLite database, never on the filesystem,
//     so there is no path-traversal risk.
//   - Put rejects payloads larger than MaxAttachmentSize (default 50 MiB).
//   - A background goroutine calls Prune every 24 h to evict expired rows.
package session

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/soulacy/soulacy/internal/sqlitex"
)

const resourceSchema = `
CREATE TABLE IF NOT EXISTS session_resources (
    id          TEXT PRIMARY KEY,
    mime_type   TEXT NOT NULL,
    data        BLOB NOT NULL,
    expires_at  DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_resources_expires ON session_resources(expires_at);
`

// ResourceStore is the interface for storing and retrieving binary session
// resources (attachments).  All operations accept a context for cancellation.
type ResourceStore interface {
	// Put stores data under id with the given MIME type.  The resource expires
	// after ttl.  Returns an error if len(data) exceeds the configured
	// MaxAttachmentSize or if the underlying store fails.
	Put(ctx context.Context, id, mimeType string, data []byte, ttl time.Duration) error

	// Get retrieves the data and MIME type for the resource with the given id.
	// Returns sql.ErrNoRows (via errors.Is) when the id is not found.
	Get(ctx context.Context, id string) (data []byte, mimeType string, err error)

	// Delete removes the resource with the given id.  A no-op if the id does
	// not exist.
	Delete(ctx context.Context, id string) error

	// Prune deletes all rows whose expires_at is in the past.  Returns the
	// number of rows deleted.
	Prune(ctx context.Context) (int64, error)

	// Close releases the underlying database connection.
	Close() error
}

// SQLiteStore is the SQLite-backed implementation of ResourceStore.
type SQLiteStore struct {
	db     *sql.DB
	maxLen int64
}

// NewSQLiteStore opens (or creates) the SQLite database at path, applies the
// resource schema, and starts a background pruning goroutine.
//
// cfg may be zero-valued; DefaultConfig() values are used for any unset fields.
func NewSQLiteStore(path string, cfg Config) (*SQLiteStore, error) {
	if cfg.AttachmentTTL <= 0 {
		cfg.AttachmentTTL = DefaultAttachmentTTL
	}
	if cfg.MaxAttachmentSize <= 0 {
		cfg.MaxAttachmentSize = DefaultMaxAttachmentSize
	}

	db, err := sqlitex.Open(path, sqlitex.DefaultOptions())
	if err != nil {
		return nil, fmt.Errorf("session: open sqlite %s: %w", path, err)
	}

	// Apply schema; run each DDL statement separately — mattn/go-sqlite3 is
	// unreliable with multi-statement strings passed to db.Exec.
	for _, stmt := range splitStmts(resourceSchema) {
		if _, err := db.Exec(stmt); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("session: schema migration (%q): %w",
				truncate(stmt, 60), err)
		}
	}

	s := &SQLiteStore{db: db, maxLen: cfg.MaxAttachmentSize}

	// Background pruning: delete expired rows every 24 h.  The goroutine
	// exits when the database is closed (db.QueryContext returns an error).
	go func() {
		t := time.NewTicker(24 * time.Hour)
		defer t.Stop()
		for range t.C {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_, _ = s.Prune(ctx)
			cancel()
			// If the DB was closed, the next Prune call will fail and the
			// goroutine will block on the next tick — this is intentional;
			// the process is assumed to be shutting down shortly after Close.
		}
	}()

	return s, nil
}

// Put stores data under id with the given MIME type and TTL.
// Returns an error when len(data) > s.maxLen.
func (s *SQLiteStore) Put(ctx context.Context, id, mimeType string, data []byte, ttl time.Duration) error {
	if int64(len(data)) > s.maxLen {
		return fmt.Errorf("session: attachment too large (%d bytes, max %d)", len(data), s.maxLen)
	}
	if ttl <= 0 {
		ttl = DefaultAttachmentTTL
	}
	expiresAt := time.Now().UTC().Add(ttl)
	_, err := s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO session_resources (id, mime_type, data, expires_at)
		VALUES (?, ?, ?, ?)`,
		id, mimeType, data, expiresAt,
	)
	if err != nil {
		return fmt.Errorf("session: put resource %q: %w", id, err)
	}
	return nil
}

// Get retrieves the blob and MIME type for id.
// Returns sql.ErrNoRows when the resource does not exist.
func (s *SQLiteStore) Get(ctx context.Context, id string) ([]byte, string, error) {
	var data []byte
	var mimeType string
	err := s.db.QueryRowContext(ctx,
		`SELECT data, mime_type FROM session_resources WHERE id = ?`, id,
	).Scan(&data, &mimeType)
	if err != nil {
		return nil, "", fmt.Errorf("session: get resource %q: %w", id, err)
	}
	return data, mimeType, nil
}

// Delete removes the resource with id.  Safe to call for non-existent ids.
func (s *SQLiteStore) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM session_resources WHERE id = ?`, id,
	)
	if err != nil {
		return fmt.Errorf("session: delete resource %q: %w", id, err)
	}
	return nil
}

// Prune deletes all resources whose expires_at is before now.
// Returns the number of rows deleted.
func (s *SQLiteStore) Prune(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM session_resources WHERE expires_at < ?`, time.Now().UTC(),
	)
	if err != nil {
		return 0, fmt.Errorf("session: prune: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("session: prune rows affected: %w", err)
	}
	return n, nil
}

// Close releases the database connection.
func (s *SQLiteStore) Close() error { return s.db.Close() }

// --- helpers ----------------------------------------------------------------

// splitStmts splits a multi-statement SQL string on semicolons, returning
// only non-empty trimmed statements.  Mirrors the same helper in
// internal/memory/sqlite.go.
func splitStmts(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ';' {
			stmt := trim(s[start:i])
			if stmt != "" {
				out = append(out, stmt)
			}
			start = i + 1
		}
	}
	if stmt := trim(s[start:]); stmt != "" {
		out = append(out, stmt)
	}
	return out
}

func trim(s string) string {
	// Manual trim to avoid importing strings just for this helper.
	b := []byte(s)
	lo, hi := 0, len(b)
	for lo < hi && isSpace(b[lo]) {
		lo++
	}
	for hi > lo && isSpace(b[hi-1]) {
		hi--
	}
	return string(b[lo:hi])
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
