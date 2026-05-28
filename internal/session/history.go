// history.go — SQLite-backed conversation history store for session package.
//
// ConversationEntry records one turn (user / assistant / system) in a
// multi-turn conversation.  Entries are keyed by session_id and agent_id,
// ordered by insertion id, and pruned automatically after 30 days.
package session

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/soulacy/soulacy/internal/sqlitex"
)

// ConversationEntry is one turn in a conversation.
type ConversationEntry struct {
	ID        int64     `json:"id"`
	SessionID string    `json:"session_id"`
	AgentID   string    `json:"agent_id"`
	Role      string    `json:"role"`    // "user" | "assistant" | "system"
	Content   string    `json:"content"`
	Tokens    int       `json:"tokens"`  // 0 if unknown
	CreatedAt time.Time `json:"created_at"`
}

// HistoryStore is the interface for persisting and retrieving conversation
// history entries.
type HistoryStore interface {
	// Append adds a conversation turn.
	Append(ctx context.Context, e ConversationEntry) error

	// Load returns the last `limit` entries for sessionID, oldest first.
	// If limit <= 0, returns all entries.
	Load(ctx context.Context, sessionID string, limit int) ([]ConversationEntry, error)

	// LoadForAgent returns the last `limit` entries across all sessions for
	// agentID, newest first (useful for admin inspection).
	LoadForAgent(ctx context.Context, agentID string, limit int) ([]ConversationEntry, error)

	// Prune deletes entries older than the given duration. Returns count deleted.
	Prune(ctx context.Context, olderThan time.Duration) (int64, error)

	// Close releases resources.
	Close() error
}

// ---------------------------------------------------------------------------
// NoopHistoryStore — all no-ops for degraded / testing mode.
// ---------------------------------------------------------------------------

// NoopHistoryStore implements HistoryStore with no-op methods.
type NoopHistoryStore struct{}

// Append is a no-op.
func (NoopHistoryStore) Append(_ context.Context, _ ConversationEntry) error { return nil }

// Load returns nil, nil.
func (NoopHistoryStore) Load(_ context.Context, _ string, _ int) ([]ConversationEntry, error) {
	return nil, nil
}

// LoadForAgent returns nil, nil.
func (NoopHistoryStore) LoadForAgent(_ context.Context, _ string, _ int) ([]ConversationEntry, error) {
	return nil, nil
}

// Prune is a no-op.
func (NoopHistoryStore) Prune(_ context.Context, _ time.Duration) (int64, error) { return 0, nil }

// Close is a no-op.
func (NoopHistoryStore) Close() error { return nil }

// ---------------------------------------------------------------------------
// SQLiteHistoryStore — full SQLite-backed implementation.
// ---------------------------------------------------------------------------

const historySchema = `
CREATE TABLE IF NOT EXISTS conversation_history (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id  TEXT NOT NULL,
    agent_id    TEXT NOT NULL,
    role        TEXT NOT NULL,
    content     TEXT NOT NULL,
    tokens      INTEGER NOT NULL DEFAULT 0,
    created_at  DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ch_session ON conversation_history(session_id, created_at);
CREATE INDEX IF NOT EXISTS idx_ch_agent   ON conversation_history(agent_id, created_at DESC);
`

// SQLiteHistoryStore is the SQLite-backed implementation of HistoryStore.
type SQLiteHistoryStore struct {
	db     *sql.DB
	stopCh chan struct{}
}

// NewSQLiteHistoryStore opens (or creates) the SQLite database at path,
// applies the conversation_history schema, and starts a background pruning
// goroutine that removes entries older than 30 days every 6 hours.
func NewSQLiteHistoryStore(path string) (*SQLiteHistoryStore, error) {
	db, err := sqlitex.Open(path, sqlitex.DefaultOptions())
	if err != nil {
		return nil, fmt.Errorf("session/history: open sqlite %s: %w", path, err)
	}

	for _, stmt := range splitStmts(historySchema) {
		if _, err := db.Exec(stmt); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("session/history: schema migration (%q): %w",
				truncate(stmt, 60), err)
		}
	}

	s := &SQLiteHistoryStore{
		db:     db,
		stopCh: make(chan struct{}),
	}

	go s.pruneLoop()

	return s, nil
}

// pruneLoop runs every 6 hours and deletes entries older than 30 days.
// It exits when stopCh is closed.
func (s *SQLiteHistoryStore) pruneLoop() {
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_, _ = s.Prune(ctx, 30*24*time.Hour)
			cancel()
		}
	}
}

// Append inserts a new conversation entry with created_at set to now (UTC).
func (s *SQLiteHistoryStore) Append(ctx context.Context, e ConversationEntry) error {
	createdAt := time.Now().UTC().Format("2006-01-02 15:04:05")
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO conversation_history
		    (session_id, agent_id, role, content, tokens, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		e.SessionID, e.AgentID, e.Role, e.Content, e.Tokens, createdAt,
	)
	if err != nil {
		return fmt.Errorf("session/history: append: %w", err)
	}
	return nil
}

// Load returns conversation entries for sessionID, oldest first.
// If limit <= 0, all entries are returned.
// If limit > 0, the last `limit` entries are returned in chronological order.
func (s *SQLiteHistoryStore) Load(ctx context.Context, sessionID string, limit int) ([]ConversationEntry, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if limit <= 0 {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, session_id, agent_id, role, content, tokens, created_at
			 FROM conversation_history
			 WHERE session_id = ?
			 ORDER BY id ASC`,
			sessionID,
		)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, session_id, agent_id, role, content, tokens, created_at
			 FROM (
			     SELECT id, session_id, agent_id, role, content, tokens, created_at
			     FROM conversation_history
			     WHERE session_id = ?
			     ORDER BY id DESC
			     LIMIT ?
			 )
			 ORDER BY id ASC`,
			sessionID, limit,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("session/history: load session %q: %w", sessionID, err)
	}
	defer rows.Close()

	return scanEntries(rows)
}

// LoadForAgent returns the last `limit` entries for agentID across all
// sessions, newest first.  If limit <= 0, it is capped at 1000.
func (s *SQLiteHistoryStore) LoadForAgent(ctx context.Context, agentID string, limit int) ([]ConversationEntry, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, session_id, agent_id, role, content, tokens, created_at
		 FROM conversation_history
		 WHERE agent_id = ?
		 ORDER BY id DESC
		 LIMIT ?`,
		agentID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("session/history: load agent %q: %w", agentID, err)
	}
	defer rows.Close()

	return scanEntries(rows)
}

// Prune deletes entries older than olderThan. Returns the number of rows deleted.
func (s *SQLiteHistoryStore) Prune(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-olderThan).Format("2006-01-02 15:04:05")
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM conversation_history WHERE created_at < ?`, cutoff,
	)
	if err != nil {
		return 0, fmt.Errorf("session/history: prune: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("session/history: prune rows affected: %w", err)
	}
	return n, nil
}

// Close stops the background pruner and closes the database connection.
func (s *SQLiteHistoryStore) Close() error {
	close(s.stopCh)
	return s.db.Close()
}

// ---------------------------------------------------------------------------
// internal helpers
// ---------------------------------------------------------------------------

// scanEntries scans all rows into a []ConversationEntry slice.
func scanEntries(rows *sql.Rows) ([]ConversationEntry, error) {
	var out []ConversationEntry
	for rows.Next() {
		var e ConversationEntry
		var createdAtStr string
		if err := rows.Scan(
			&e.ID, &e.SessionID, &e.AgentID, &e.Role, &e.Content, &e.Tokens, &createdAtStr,
		); err != nil {
			return nil, fmt.Errorf("session/history: scan row: %w", err)
		}
		t, err := time.Parse("2006-01-02 15:04:05", createdAtStr)
		if err != nil {
			return nil, fmt.Errorf("session/history: parse created_at %q: %w", createdAtStr, err)
		}
		e.CreatedAt = t.UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}
