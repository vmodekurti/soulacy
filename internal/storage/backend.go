// Package storage defines provider-agnostic interfaces for Soulacy's two
// durable data stores:
//
//   - ActionLogBackend — structured append-only event log (agent runs, tool calls, etc.)
//   - MemoryBackend    — long-term agent memory archive with scoped retrieval
//
// Two implementations ship out of the box:
//
//	internal/storage/sqlite   — embedded SQLite (zero-dependency, default)
//	internal/storage/postgres — PostgreSQL via pgx/v5 (production / multi-node)
//
// The active backend is selected at startup from config.yaml:
//
//	storage:
//	  backend: sqlite   # or "postgres"
//	  postgres_dsn: "postgres://user:pass@host:5432/soulacy"
//
// Engine code imports only this package; it never references sqlite or postgres
// directly, so swapping backends requires only a config change.
package storage

import (
	"time"

	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/pkg/message"
)

// ActionLogBackend is the interface satisfied by every action-log implementation.
// It mirrors the public surface of internal/actionlog.Logger so the engine never
// needs to know which backend is active.
type ActionLogBackend interface {
	// Append enqueues one event for durable storage. Must never block the caller.
	Append(ev message.Event)

	// Tail returns the most recent limit events for agentID, oldest-first.
	Tail(agentID string, limit int) ([]message.Event, error)

	// EventFilePath returns the on-disk log path for agentID. The HTTP tail
	// handler serves this file directly (range-request friendly). Backends that
	// don't use flat files should return a best-effort path or empty string.
	EventFilePath(agentID string) string

	// IncompleteMessageIns returns raw JSON payloads of message.in events that
	// have no corresponding outcome (message.out / error) since `since`. Used
	// by the startup recovery pass to re-enqueue crashed runs.
	IncompleteMessageIns(since time.Time) ([][]byte, error)

	// CountMessageInAttempts returns how many message.in events exist for
	// (agentID, sessionID) since `since`. Used by the poison-pill guard.
	CountMessageInAttempts(agentID, sessionID string, since time.Time) (int, error)

	// MarkDeadLetter writes a synchronous message.dead_letter event so the next
	// boot's IncompleteMessageIns query skips the poisoned session.
	MarkDeadLetter(agentID, sessionID, reason string) error

	// Close flushes pending events and releases all held resources.
	Close() error
}

// MemoryBackend is the interface satisfied by every memory-archive implementation.
// It mirrors the public surface of internal/memory.SQLiteArchive.
type MemoryBackend interface {
	// Archive persists a memory entry. Duplicate IDs are silently ignored.
	Archive(entry memory.Entry) error

	// Search performs a substring/FTS search across memory content for agentID.
	Search(agentID, query string, limit int) ([]memory.Entry, error)

	// ReadByScope returns archived entries for (agentID, sessionID, scope), newest-first.
	ReadByScope(agentID, sessionID string, scope memory.Scope, limit int) ([]memory.Entry, error)

	// ReadGlobal returns the most recent entries across all sessions for agentID.
	ReadGlobal(agentID string, limit int) ([]memory.Entry, error)

	// Prune deletes entries older than before for agentID. Returns rows deleted.
	Prune(agentID string, before time.Time) (int64, error)

	// Close releases all held resources.
	Close() error
}
