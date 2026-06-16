# Robustness Hardening — Implementation Notes

Branch: `robustness/operability-hardening`. All changes build (`go build ./...`)
and ship with tests. Each item below is one commit.

## Landed (with tests)

| Story | Commit | What changed | Tests |
|-------|--------|--------------|-------|
| **S2.1 Panic isolation** | `0b10f00` | `recover()` inside `engine.Handle` converts a panic on the channel/cron worker path (which Fiber's recover does *not* cover) into a normal run error; records `soulacy_agent_panics_total`; preserves DLQ + failure-notifier behaviour. | `engine_panic_test.go` |
| **Story 5 / S8.1 Config validation** | `c494f52` | `Config.Validate()` runs at the end of `Load()`: parses every string duration (`tool_timeout`, `session_ttl`, `jwt_*_ttl`, `nats_ack_wait`) and range-checks numerics (port, max_turns vs ceiling, sessions, chunk overlap<size, pool workers, sandbox rlimits). `tool_timeout: 120` (missing `s`) now errors at startup naming the field. | `validate_test.go` |
| **Story 1 / S3.1 Cost budget** | `d0e5fc7` | New `budget:` block in SOUL.yaml (`max_tokens`, `max_llm_calls`). Engine accumulates usage and checks **before every `Router.Complete`**; halts with a terminal reply + `soulacy_agent_budget_halts_total` when exceeded. | `engine_budget_test.go` |
| **S3.2 max_turns ceiling** | `d0e5fc7` | `runtime.max_turns_ceiling` (default 50); engine clamps any agent's effective `max_turns` to it. | `engine_budget_test.go` |
| **Story 3 Timeout attribution** | `aa863a6` | LLM-call errors now name the clock that fired: run cancelled (shutdown) vs `run_timeout` exceeded waiting on provider X/model Y, with remediation. (`run_timeout` already floors unset/0 to the 5m worker default.) | covered via build + existing run tests |
| **Story 6 Secure defaults** | `630a71b` | `allow_shell: true` agent flag as a readable alias for `capabilities: [system]`. Default keeps shell built-ins OFF. (SEC-3 per-agent gate and SEC-4 non-localhost+empty-key bind refusal were already implemented.) | `allowshell_test.go` |

New Prometheus metrics: `soulacy_agent_panics_total`, `soulacy_agent_budget_halts_total`.

## Already implemented in the codebase (verified — no work needed)

- **SEC-4** non-localhost + empty `api_key` → refuse to start (`gateway/server.go checkAuthBindSafety`, `authbind_test.go`).
- **SEC-3** system/shell built-ins gated behind per-agent `capabilities:[system]` + server permit.
- SQLite **WAL + busy_timeout=30s**, crash-recovery **replay** of incomplete runs (`app/recover.go`), scheduler **missed-fire catch-up**, transactional KB ingest, correct subprocess stderr-drain.

## Remaining (recommended as individual passes — larger/riskier)

1. **Story 2 / S1.x — Provider/model boot validation.** `agentvalidate` already validates models *when* the provider model list is populated; make it mandatory by probing each provider's `Models()` at startup, failing agents (not the gateway) on a bad model with `ollama pull X` guidance, and auto-disabling cron agents after N consecutive failures. *Touches:* gateway startup wiring, `agentvalidate`, `scheduler`.
2. **S2.2 — Graceful shutdown & goroutine lifecycle.** Close the inbox on shutdown; register the session-eviction ticker and file-watcher goroutines on the closer stack; bounded drain. *Touches:* `app/wire.go` `Run()` (1.3k lines) + `channels.Registry` — highest regression risk; do behind the smoke tests in `improvements.md` TEST-2/3.
3. **Story 4 — Token-aware context management.** Per-model context table; estimate tokens for system+tools+history pre-call; trim oldest history to fit; on a 400 context-exceeded, auto-trim and retry once. *Touches:* engine loop + a new token-estimate/model-limits helper.
4. **S2.13 / S2.4 / S2.8 — Health depth, watcher supervision, channel send chunking.** Deep `/health` that calls each adapter's `Status()` + DB/provider ping; supervise the watcher goroutine (restart on fsnotify close); per-platform outbound chunking (Telegram 4096 / Discord 2000) + bounded send retry with auth-vs-transient classification. *Touches:* `gateway/api.go`, `runtime/watcher.go`, each `channels/*/adapter.go`.

## Build/test notes for this environment

CGO + sqlite-vec needs `sqlite3.h`; reuse the mattn/go-sqlite3 vendored header:
`CGO_CFLAGS="-I<dir-with-sqlite3.h>"`. The embedded GUI needs `internal/webui/dist/`
to exist (gitignored placeholder is fine for backend-only builds).
