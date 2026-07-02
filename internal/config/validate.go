package config

import (
	"fmt"
	"strings"
	"time"
)

// Validate performs strict, fail-fast validation of a loaded Config (Story 5 /
// S8.1). Its whole purpose is to turn the framework's previous "silently fall
// back to a default" behaviour into a loud startup error, so that a typo like
// `tool_timeout: 120` (missing the `s`, which viper would happily keep as the
// string "120" and the runtime would then fail to parse and quietly default to
// 30s) is caught before the gateway serves a single request.
//
// It checks two classes of problem:
//
//  1. Duration strings that don't parse with time.ParseDuration.
//  2. Numeric values outside sane bounds (negative counts, ports out of range,
//     overlap >= chunk size, etc.).
//
// All problems are accumulated and returned together so the operator can fix
// them in one pass rather than one-error-per-restart.
func (c *Config) Validate() error {
	var errs []error

	// --- Durations: every string duration field must parse. ---
	dur := func(field, val string) {
		if strings.TrimSpace(val) == "" {
			return // empty means "use default" downstream; not an error here
		}
		if _, err := time.ParseDuration(val); err != nil {
			errs = append(errs, fmt.Errorf(
				"%s: %q is not a valid duration (did you forget the unit, e.g. %q?)",
				field, val, val+"s"))
		}
	}
	dur("runtime.tool_timeout", c.Runtime.ToolTimeout)
	dur("runtime.session_ttl", c.Runtime.SessionTTL)
	dur("auth.jwt_access_ttl", c.Auth.JWTAccessTTL)
	dur("auth.jwt_refresh_ttl", c.Auth.JWTRefreshTTL)
	dur("queue.nats_ack_wait", c.Queue.NATSAckWait)

	// --- Server ---
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		errs = append(errs, fmt.Errorf("server.port: %d is out of range (1–65535)", c.Server.Port))
	}

	// --- Runtime numeric bounds ---
	// max_concurrent_sessions sizes the worker pool; <=0 would mean "no workers"
	// and silently drop every channel message.
	if c.Runtime.MaxConcurrentSessions < 0 {
		errs = append(errs, fmt.Errorf("runtime.max_concurrent_sessions: %d must not be negative", c.Runtime.MaxConcurrentSessions))
	}
	// default_max_turns bounds the agentic loop. Negative is nonsensical; an
	// absurdly high value is a cost/runaway foot-gun (see Story 1).
	if c.Runtime.DefaultMaxTurns < 0 {
		errs = append(errs, fmt.Errorf("runtime.default_max_turns: %d must not be negative", c.Runtime.DefaultMaxTurns))
	}
	if c.Runtime.MaxTurnsCeiling < 0 {
		errs = append(errs, fmt.Errorf("runtime.max_turns_ceiling: %d must not be negative", c.Runtime.MaxTurnsCeiling))
	}
	// When both are set, the default must not exceed the hard ceiling.
	if c.Runtime.MaxTurnsCeiling > 0 && c.Runtime.DefaultMaxTurns > c.Runtime.MaxTurnsCeiling {
		errs = append(errs, fmt.Errorf(
			"runtime.default_max_turns (%d) exceeds runtime.max_turns_ceiling (%d)",
			c.Runtime.DefaultMaxTurns, c.Runtime.MaxTurnsCeiling))
	}
	if c.Runtime.MaxSessions < 0 {
		errs = append(errs, fmt.Errorf("runtime.max_sessions: %d must not be negative", c.Runtime.MaxSessions))
	}
	if c.Runtime.MaxHistoryTurns < 0 {
		errs = append(errs, fmt.Errorf("runtime.max_history_turns: %d must not be negative", c.Runtime.MaxHistoryTurns))
	}

	// --- Sandbox: all rlimit knobs are "0 = unlimited", negatives are invalid. ---
	if c.Runtime.Sandbox.CPUSeconds < 0 {
		errs = append(errs, fmt.Errorf("runtime.sandbox.cpu_seconds: %d must not be negative", c.Runtime.Sandbox.CPUSeconds))
	}
	if c.Runtime.Sandbox.MemoryMB < 0 {
		errs = append(errs, fmt.Errorf("runtime.sandbox.memory_mb: %d must not be negative", c.Runtime.Sandbox.MemoryMB))
	}
	if c.Runtime.Sandbox.OpenFiles < 0 {
		errs = append(errs, fmt.Errorf("runtime.sandbox.open_files: %d must not be negative", c.Runtime.Sandbox.OpenFiles))
	}
	if c.Runtime.Sandbox.FileSizeMB < 0 {
		errs = append(errs, fmt.Errorf("runtime.sandbox.file_size_mb: %d must not be negative", c.Runtime.Sandbox.FileSizeMB))
	}

	// --- Executor ---
	switch c.Executor.Backend {
	case "", "process", "pool", "docker", "ssh":
	default:
		errs = append(errs, fmt.Errorf("executor.backend: unsupported value %q", c.Executor.Backend))
	}
	if c.Executor.Backend == "pool" && c.Executor.Workers < 1 {
		errs = append(errs, fmt.Errorf("executor.workers: %d must be >= 1 when executor.backend is \"pool\"", c.Executor.Workers))
	}
	if c.Executor.Backend == "ssh" && strings.TrimSpace(c.Executor.SSHHost) == "" {
		errs = append(errs, fmt.Errorf("executor.ssh_host: required when executor.backend is \"ssh\""))
	}

	// --- Knowledge / RAG chunking ---
	if c.Knowledge.ChunkSize < 0 {
		errs = append(errs, fmt.Errorf("knowledge.chunk_size: %d must not be negative", c.Knowledge.ChunkSize))
	}
	if c.Knowledge.ChunkOverlap < 0 {
		errs = append(errs, fmt.Errorf("knowledge.chunk_overlap: %d must not be negative", c.Knowledge.ChunkOverlap))
	}
	if c.Knowledge.ChunkSize > 0 && c.Knowledge.ChunkOverlap >= c.Knowledge.ChunkSize {
		errs = append(errs, fmt.Errorf(
			"knowledge.chunk_overlap (%d) must be smaller than knowledge.chunk_size (%d)",
			c.Knowledge.ChunkOverlap, c.Knowledge.ChunkSize))
	}

	if len(errs) > 0 {
		return fmt.Errorf("invalid configuration:\n  - %s",
			strings.Join(errStrings(errs), "\n  - "))
	}
	return nil
}

func errStrings(errs []error) []string {
	out := make([]string, len(errs))
	for i, e := range errs {
		out[i] = e.Error()
	}
	return out
}
