// Package audit provides an OPTIONAL append-only tool-call audit log in JSONL
// form. When enabled, every built-in tool execution is recorded as a JSON line
// in <audit_dir>/<YYYY-MM-DD>/<sessionID>.jsonl.
//
// DOC-4: this JSONL log is debug/convenience output and is DISABLED BY DEFAULT
// (runtime.audit_dir defaults to ""). It is NOT the authoritative record. The
// authoritative incident-reconstruction record is the SQLite action log in
// package internal/actionlog, which is always on. Treat these JSONL files as a
// redundant, best-effort convenience tail — see docs/security/audit.md.
package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

// Entry is one line in the audit log.
type Entry struct {
	Timestamp  time.Time      `json:"ts"`
	SessionID  string         `json:"session"`
	AgentID    string         `json:"agent"`
	Tool       string         `json:"tool"`
	Args       map[string]any `json:"args"`
	ResultLen  int            `json:"result_len"`
	DurationMS int64          `json:"duration_ms"`
	Denied     bool           `json:"denied,omitempty"`
	Error      string         `json:"error,omitempty"`
}

// secretPattern matches common secret field names so their values can be
// redacted before they are written to disk.
var secretPattern = regexp.MustCompile(`(?i)(api[_-]?key|password|secret|token|credential|auth)`)

// redactArgs returns a shallow copy of args with secret values replaced by
// "[REDACTED]". Only string values are inspected; nested objects are left
// as-is to avoid deep-copying large structures.
func redactArgs(args map[string]any) map[string]any {
	if len(args) == 0 {
		return args
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		if secretPattern.MatchString(k) {
			out[k] = "[REDACTED]"
		} else {
			out[k] = v
		}
	}
	return out
}

// Logger writes audit entries to a per-session JSONL file under dir.
// It is safe for concurrent use; each Log call acquires a per-file mutex.
type Logger struct {
	dir string
	mu  sync.Mutex
	fmu sync.Map // path → *sync.Mutex
}

// New creates a Logger that writes to dir (created if it doesn't exist).
// Pass "" to disable audit logging (Log becomes a no-op).
func New(dir string) *Logger {
	return &Logger{dir: dir}
}

// Log appends e to the audit file for e.SessionID.
// If the Logger was created with an empty dir, this is a no-op.
func (l *Logger) Log(e Entry) {
	if l == nil || l.dir == "" {
		return
	}

	e.Args = redactArgs(e.Args)

	day := e.Timestamp.UTC().Format("2006-01-02")
	dir := filepath.Join(l.dir, day)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return // best-effort; don't crash the agent run over audit I/O
	}

	// Sanitise session ID to make it a safe filename.
	safe := sessionSafe(e.SessionID)
	path := filepath.Join(dir, safe+".jsonl")

	// Per-file mutex prevents interleaved writes from concurrent tool calls.
	raw, _ := l.fmu.LoadOrStore(path, &sync.Mutex{})
	mu := raw.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()

	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(f, "%s\n", data)
}

// sessionSafe replaces any character that is not alphanumeric, dash, or
// underscore with an underscore so the session ID can be used as a filename.
var nonSafe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func sessionSafe(id string) string {
	s := nonSafe.ReplaceAllString(id, "_")
	if len(s) > 128 {
		s = s[:128]
	}
	if s == "" {
		s = "unknown"
	}
	return s
}
