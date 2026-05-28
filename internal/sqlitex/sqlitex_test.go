package sqlitex

import (
	"strings"
	"testing"
)

// TestDSN_RequiredPragmas — every Open() call must come out of this
// helper with the four pragmas Litestream and the gateway depend on:
//
//   _journal_mode=WAL   — Litestream only streams WAL pages
//   _synchronous=NORMAL — safe with WAL, ~5-10x faster commits
//   _busy_timeout=…     — non-zero, otherwise concurrent writers fail
//                          immediately under contention
//   _cache_size=-20000  — per-connection 20 MiB page cache
//
// A regression that dropped any of these would not surface until traffic
// hit the locked-database path; this test catches it at build time.
func TestDSN_RequiredPragmas(t *testing.T) {
	dsn := DSN("/tmp/foo.db", DefaultOptions())
	required := []string{
		"_journal_mode=WAL",
		"_synchronous=NORMAL",
		"_busy_timeout=30000",
		"_cache_size=-20000",
		"_temp_store=MEMORY",
	}
	for _, p := range required {
		if !strings.Contains(dsn, p) {
			t.Errorf("DSN missing required pragma %q\n full: %s", p, dsn)
		}
	}
}

// TestDSN_OptionalPragmas — foreign keys and mmap_size are opt-in (only
// the knowledge store wants them). They must NOT appear when their flag
// is off, and MUST appear when on.
func TestDSN_OptionalPragmas(t *testing.T) {
	off := DSN("/tmp/foo.db", Options{})
	if strings.Contains(off, "_foreign_keys") {
		t.Errorf("foreign_keys leaked when not requested: %s", off)
	}
	if strings.Contains(off, "_mmap_size") {
		t.Errorf("mmap_size leaked when not requested: %s", off)
	}
	on := DSN("/tmp/foo.db", Options{ForeignKeys: true, MMapSize: 256 << 20})
	if !strings.Contains(on, "_foreign_keys=on") {
		t.Errorf("foreign_keys missing when requested: %s", on)
	}
	if !strings.Contains(on, "_mmap_size=268435456") {
		t.Errorf("mmap_size missing or wrong value: %s", on)
	}
}

// TestDSN_PreservesPath — the helper must not corrupt the path portion
// of the DSN. A buggy version that, say, url-encoded the path would
// happily compile but fail at sql.Open() with file-not-found.
func TestDSN_PreservesPath(t *testing.T) {
	for _, p := range []string{
		"/var/lib/soulacy/knowledge.db",
		"./local.db",
		"/tmp/with spaces.db", // valid on POSIX; sqlite handles it
	} {
		dsn := DSN(p, DefaultOptions())
		if !strings.HasPrefix(dsn, p+"?") {
			t.Errorf("DSN should start with %q?, got %q", p, dsn)
		}
	}
}
