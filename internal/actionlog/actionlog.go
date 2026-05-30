// Package actionlog records structured agent actions to two places:
//
//   1. A known per-agent log file (<dir>/<agent-id>.log) as JSON Lines — easy to
//      tail, watch from the GUI, or inspect with `tail -f`. This is the location
//      the UI polls.
//   2. A SQLite table (agent_events) for durable, queryable history that survives
//      restarts and supports cross-agent queries.
//
// Every agent execution emits events (run start, llm calls, tool calls/results,
// reply, errors) which flow through here via the gateway EventHub.
//
// PRODUCTION_AUDIT → HIGH/Performance: Append was the engine's #1 hot-path
// lock-while-doing-I/O finding. The previous implementation held a single
// global mutex while doing two synchronous fsyncs per event (file append +
// SQLite insert), serialising every agent through this writer. We now
// run a single buffered async writer goroutine: callers enqueue events
// non-blockingly, the writer drains them in batches, fsyncs once per
// batch, and inserts to SQLite in one transaction. Latency on the agent
// loop drops from ~ms-per-event to ~µs-per-event.
package actionlog

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/metrics"
	"github.com/soulacy/soulacy/internal/sqlitex"
	"github.com/soulacy/soulacy/pkg/message"
)

const (
	// maxFileBytes triggers pruning of a per-agent log file once exceeded.
	maxFileBytes = 1 << 20 // 1 MiB
	// keepLines is how many recent lines are retained when a file is pruned.
	keepLines = 2000

	// writerQueueSize is how many events can be buffered before Append
	// becomes a drop (with a warn log). Sized for ~1 second of high-rate
	// agent activity at 200 events/sec.
	writerQueueSize = 4096

	// batchMaxSize forces a flush even if the timeout hasn't elapsed —
	// caps memory growth under bursts.
	batchMaxSize = 256

	// batchFlushInterval is the maximum age of an unflushed event before
	// the writer forces a flush. Keeps the GUI's polling tail "fresh enough"
	// without thrashing the disk on every event.
	batchFlushInterval = 250 * time.Millisecond
)

const eventsSchema = `
CREATE TABLE IF NOT EXISTS agent_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id    TEXT NOT NULL,
    session_id  TEXT,
    type        TEXT NOT NULL,
    payload     TEXT,
    created_at  DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_events_agent ON agent_events(agent_id, created_at DESC);
`

// Logger writes agent action events to per-agent files and SQLite.
type Logger struct {
	dir string
	db  *sql.DB
	log *zap.Logger

	// queue receives events from Append; the writer goroutine drains it.
	queue chan message.Event
	wg    sync.WaitGroup
	stop  chan struct{}

	// pruneRequest is a non-blocking signal — when the writer notices a
	// file may need pruning it sends the path here; the prune goroutine
	// picks it up out-of-band. Buffered to avoid blocking the writer if
	// the prune goroutine is mid-rewrite.
	pruneRequest chan string
}

// New creates a Logger. dir is the per-agent log directory (created if missing);
// dbPath is the SQLite database for durable event history. The async writer
// goroutine is started before New returns; callers must invoke Close() on
// shutdown to flush any pending events.
func New(dir, dbPath string, log *zap.Logger) (*Logger, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("actionlog: create dir %s: %w", dir, err)
	}
	// PRODUCTION_AUDIT → F3 (2026-05-27): centralised WAL + NORMAL synchronous
	// + 30s busy_timeout + tuned pool via internal/sqlitex. Litestream
	// compatibility preserved (WAL is the only journal mode it streams).
	db, err := sqlitex.Open(dbPath, sqlitex.DefaultOptions())
	if err != nil {
		return nil, fmt.Errorf("actionlog: open sqlite %s: %w", dbPath, err)
	}
	for _, stmt := range strings.Split(eventsSchema, ";") {
		if s := strings.TrimSpace(stmt); s != "" {
			if _, err := db.Exec(s); err != nil {
				return nil, fmt.Errorf("actionlog: schema: %w", err)
			}
		}
	}
	l := &Logger{
		dir:          dir,
		db:           db,
		log:          log,
		queue:        make(chan message.Event, writerQueueSize),
		pruneRequest: make(chan string, 64),
		stop:         make(chan struct{}),
	}
	l.wg.Add(2)
	go l.run()
	go l.pruneLoop()
	return l, nil
}

// Path returns the known on-disk log location for an agent.
func (l *Logger) Path(agentID string) string {
	return filepath.Join(l.dir, sanitize(agentID)+".log")
}

// Append enqueues one event for the writer goroutine. Never blocks the
// caller; if the queue is full, the event is dropped and a warn is logged
// (preserving the engine's progress is more important than complete logs).
func (l *Logger) Append(ev message.Event) {
	if ev.AgentID == "" {
		return
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	select {
	case l.queue <- ev:
		metrics.ActionlogQueueDepth.Set(float64(len(l.queue)))
	default:
		metrics.ActionlogDropsTotal.Inc()
		l.log.Warn("actionlog: queue full, dropping event",
			zap.String("agent", ev.AgentID),
			zap.String("type", ev.Type),
			zap.Int("queue_size", writerQueueSize),
		)
	}
}

// run is the single writer goroutine. Reads events off the queue, batches
// up to batchMaxSize OR batchFlushInterval, then writes them all at once.
func (l *Logger) run() {
	defer l.wg.Done()
	batch := make([]message.Event, 0, batchMaxSize)
	timer := time.NewTimer(batchFlushInterval)
	defer timer.Stop()

	flush := func() {
		// Always re-arm the timer first, regardless of whether the batch is
		// empty. Previously the timer was only reset after a non-empty flush,
		// so a timer-fire at startup (before any events arrived) would leave
		// the timer permanently dead — all subsequent events would accumulate
		// in the batch but never be written until the process exited.
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(batchFlushInterval)

		if len(batch) == 0 {
			return
		}
		metrics.ActionlogBatchSize.Observe(float64(len(batch)))
		l.flush(batch)
		batch = batch[:0]
		metrics.ActionlogQueueDepth.Set(float64(len(l.queue)))
	}

	for {
		select {
		case ev, ok := <-l.queue:
			if !ok {
				flush()
				return
			}
			batch = append(batch, ev)
			if len(batch) >= batchMaxSize {
				flush()
			}
		case <-timer.C:
			flush()
		case <-l.stop:
			// Drain whatever is queued, then exit.
			drain := true
			for drain {
				select {
				case ev := <-l.queue:
					batch = append(batch, ev)
					if len(batch) >= batchMaxSize {
						flush()
					}
				default:
					drain = false
				}
			}
			flush()
			return
		}
	}
}

// flush writes a batch of events: appends to per-agent files (one open
// per file across the batch) and bulk-inserts to SQLite in one transaction.
// Errors are logged per-event; we never block the engine.
func (l *Logger) flush(batch []message.Event) {
	// Group by agent so each file is opened/closed once per batch instead of
	// once per event.
	byAgent := make(map[string][]message.Event, len(batch))
	for _, ev := range batch {
		byAgent[ev.AgentID] = append(byAgent[ev.AgentID], ev)
	}
	for agentID, events := range byAgent {
		l.writeFileBatch(agentID, events)
	}
	l.writeDBBatch(batch)
}

func (l *Logger) writeFileBatch(agentID string, events []message.Event) {
	path := l.Path(agentID)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		l.log.Warn("actionlog: open file", zap.String("path", path), zap.Error(err))
		return
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, ev := range events {
		line, err := json.Marshal(ev)
		if err != nil {
			l.log.Warn("actionlog: marshal event", zap.Error(err))
			continue
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			l.log.Warn("actionlog: write file", zap.Error(err))
			return
		}
	}
	if err := w.Flush(); err != nil {
		l.log.Warn("actionlog: flush file", zap.Error(err))
	}
	// Request prune asynchronously (non-blocking). The prune goroutine
	// rate-limits and dedupes work so a steady stream of writes doesn't
	// continually re-rewrite the file.
	// (PRODUCTION_AUDIT → LOW/Reliability)
	select {
	case l.pruneRequest <- path:
	default:
		// Channel full — prune backlog is healthy enough on its own; skip.
	}
}

func (l *Logger) writeDBBatch(events []message.Event) {
	tx, err := l.db.Begin()
	if err != nil {
		l.log.Warn("actionlog: begin tx", zap.Error(err))
		return
	}
	stmt, err := tx.Prepare(`INSERT INTO agent_events (agent_id, session_id, type, payload, created_at) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		l.log.Warn("actionlog: prepare insert", zap.Error(err))
		return
	}
	defer stmt.Close()
	for _, ev := range events {
		payload, _ := json.Marshal(ev.Payload)
		if _, err := stmt.Exec(ev.AgentID, ev.SessionID, ev.Type, string(payload), ev.Timestamp); err != nil {
			l.log.Warn("actionlog: sqlite insert", zap.Error(err))
		}
	}
	if err := tx.Commit(); err != nil {
		l.log.Warn("actionlog: commit", zap.Error(err))
	}
}

// Tail returns the most recent events for an agent from its log file, oldest-first.
func (l *Logger) Tail(agentID string, limit int) ([]message.Event, error) {
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	path := l.Path(agentID)

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []message.Event{}, nil // no actions logged yet
		}
		return nil, err
	}
	defer f.Close()

	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		if t := sc.Text(); strings.TrimSpace(t) != "" {
			lines = append(lines, t)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}

	events := make([]message.Event, 0, len(lines))
	for _, ln := range lines {
		var ev message.Event
		if err := json.Unmarshal([]byte(ln), &ev); err == nil {
			events = append(events, ev)
		}
	}
	return events, nil
}

// IncompleteMessageIns returns the payload JSON of every message.in event
// recorded in the SQLite history that does NOT have a corresponding
// message.out or error event after it for the same agent + session +
// inbound message ID. Used by the gateway at boot to re-enqueue runs that
// were in flight when the process died.
// (PRODUCTION_AUDIT → F2, 2026-05-27)
//
// `since` bounds the scan to avoid re-running ancient messages on every
// boot (e.g. operators leaving the host off for a weekend shouldn't blast
// 3-day-old prompts back at users).
//
// Sessions that have been marked with a message.dead_letter event are
// excluded automatically — they have already been quarantined by a
// previous recovery pass (poison-pill guard).
//
// Returns raw JSON payloads rather than message.Message structs because
// the actionlog package doesn't import pkg/message's full schema layer;
// callers unmarshal into whichever type matches their dispatch path.
func (l *Logger) IncompleteMessageIns(since time.Time) ([][]byte, error) {
	// Pull message.in candidates first, then prove each one DIDN'T complete.
	// A single SQL with NOT EXISTS would be denser but harder to reason
	// about; this two-step keeps the matching logic in Go where it's easy
	// to evolve as we add new event types (e.g. a future message.cancelled).
	rows, err := l.db.Query(`
		SELECT agent_id, session_id, payload, created_at
		  FROM agent_events
		 WHERE type = 'message.in'
		   AND created_at >= ?
		 ORDER BY created_at ASC
	`, since)
	if err != nil {
		return nil, fmt.Errorf("actionlog: query message.in: %w", err)
	}
	defer rows.Close()

	type candidate struct {
		agentID, sessionID, payload string
		createdAt                   time.Time
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.agentID, &c.sessionID, &c.payload, &c.createdAt); err != nil {
			return nil, fmt.Errorf("actionlog: scan: %w", err)
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	// Now check completion. A run is "complete" if any message.out OR
	// error event landed for (agent, session) AFTER the message.in.
	// Multiple messages can share a session (long-running chats); pair
	// them by timestamp ordering, not by message ID — the engine doesn't
	// emit a back-reference in message.out events.
	//
	// Dead-lettered sessions (message.dead_letter exists for the same
	// agent+session) are skipped — the poison-pill guard already quarantined
	// them and there's no point re-enqueuing them again.
	out := make([][]byte, 0, len(candidates))
	for _, c := range candidates {
		// Skip dead-lettered sessions.
		var deadLetter int
		_ = l.db.QueryRow(`
			SELECT 1 FROM agent_events
			 WHERE agent_id = ? AND session_id = ? AND type = 'message.dead_letter'
			 LIMIT 1
		`, c.agentID, c.sessionID).Scan(&deadLetter)
		if deadLetter == 1 {
			continue
		}

		var hasOutcome int
		err := l.db.QueryRow(`
			SELECT 1 FROM agent_events
			 WHERE agent_id = ?
			   AND session_id = ?
			   AND created_at > ?
			   AND type IN ('message.out', 'error')
			 LIMIT 1
		`, c.agentID, c.sessionID, c.createdAt).Scan(&hasOutcome)
		if err == sql.ErrNoRows {
			out = append(out, []byte(c.payload))
		} else if err != nil {
			// Tolerate per-row failures: log and keep scanning. We'd
			// rather recover 9/10 in-flight runs than abort the recovery
			// pass on one weird row.
			l.log.Warn("actionlog: recovery outcome check failed",
				zap.String("agent", c.agentID),
				zap.String("session", c.sessionID),
				zap.Error(err))
		}
	}
	return out, nil
}

// CountMessageInAttempts returns how many message.in events exist for a
// given (agentID, sessionID) pair since `since`. Each represents one
// time the engine was asked to handle that session — including the
// original run and any crash-recovery replays. Used by the poison-pill
// guard in recover.go.
func (l *Logger) CountMessageInAttempts(agentID, sessionID string, since time.Time) (int, error) {
	var count int
	err := l.db.QueryRow(`
		SELECT COUNT(*) FROM agent_events
		 WHERE agent_id = ?
		   AND session_id = ?
		   AND type = 'message.in'
		   AND created_at >= ?
	`, agentID, sessionID, since).Scan(&count)
	return count, err
}

// MarkDeadLetter writes a message.dead_letter event directly to SQLite
// (synchronous, bypasses the async queue) so it is visible to the NEXT
// boot's IncompleteMessageIns query even if the process exits immediately
// after. This is only called by the startup poison-pill guard and should
// NOT be called on the hot path.
func (l *Logger) MarkDeadLetter(agentID, sessionID, reason string) error {
	payload, _ := json.Marshal(map[string]string{
		"reason":     reason,
		"quarantine": "poison-pill guard (too many crash-recovery attempts)",
	})
	_, err := l.db.Exec(
		`INSERT INTO agent_events (agent_id, session_id, type, payload, created_at) VALUES (?, ?, 'message.dead_letter', ?, ?)`,
		agentID, sessionID, string(payload), time.Now().UTC(),
	)
	return err
}

// pruneLoop coalesces pending prune requests and runs each pruneIfLarge in
// its own iteration. Dedupes on a per-tick basis: many writes to the same
// file produce one rewrite per pruneTickInterval, not N. Worst case: the
// file briefly exceeds maxFileBytes by one tick's worth of writes — fine,
// the cap is soft. (PRODUCTION_AUDIT → LOW/Reliability)
func (l *Logger) pruneLoop() {
	defer l.wg.Done()
	const pruneTickInterval = 10 * time.Second
	tick := time.NewTicker(pruneTickInterval)
	defer tick.Stop()

	pending := make(map[string]struct{})
	for {
		select {
		case path, ok := <-l.pruneRequest:
			if !ok {
				return
			}
			pending[path] = struct{}{}
		case <-tick.C:
			for path := range pending {
				l.pruneIfLarge(path)
			}
			pending = make(map[string]struct{})
		case <-l.stop:
			// Drain remaining requests, prune once each, then exit.
			drain := true
			for drain {
				select {
				case path := <-l.pruneRequest:
					pending[path] = struct{}{}
				default:
					drain = false
				}
			}
			for path := range pending {
				l.pruneIfLarge(path)
			}
			return
		}
	}
}

// pruneIfLarge rewrites a log file keeping only the last keepLines once it grows
// past maxFileBytes. Called by the prune goroutine outside the agent path so
// the long-rewrite stall (PRODUCTION_AUDIT → LOW) doesn't block Append OR
// the writer goroutine's batch flushes.
func (l *Logger) pruneIfLarge(path string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() < maxFileBytes {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	f.Close()
	if len(lines) <= keepLines {
		return
	}
	lines = lines[len(lines)-keepLines:]
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// Close stops the writer goroutine, drains any pending events, and releases
// the SQLite handle. Safe to call multiple times.
func (l *Logger) Close() error {
	select {
	case <-l.stop:
		// already closed
	default:
		close(l.stop)
	}
	l.wg.Wait()
	if l.db != nil {
		return l.db.Close()
	}
	return nil
}

// sanitize makes an agent ID safe to use as a filename.
func sanitize(id string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, id)
}
