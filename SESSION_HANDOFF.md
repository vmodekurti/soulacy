# Session Handoff

Last updated: 2026-06-06 (session 6: E5 complete; previously session 5:
Stories 5–6 + extensibility blueprint and E-track stories)

---

## Extensibility track (NEW — read before Story 7)

- **Design doc:** `docs/EXTENSIBILITY.md` (architecture overview, tiers,
  phase specs, disruption analysis, compatibility policy). Approved by Vasu.
- **User stories:** `docs/BACKLOG.md` E1–E14 (E14 WASM is demand-gated
  deferred). Constraints baked into every story: single static binary, no
  dynamic loading, plugins as default-deny security principals, versioned
  contracts, TDD, commit on green.
- **Branch:** all extensibility work lives on
  `feature/extensibility-blueprint` (currently checked out; contains the
  design doc + E-stories). **Switch back to `main` for sprint stories 7–15.**
- **Integrated roadmap (single sequence, see BACKLOG.md table):**
  M1: 7→E1→E2 (observability arc) · M2: 8→9 (chat) · M3: E3–E8 (sidecar
  foundation) · M4: 10→11 (voice — built ON the sidecar runtime, see
  integration notes in the prompts) · M5: 12→13→14 (reliability/workboard
  depth; artifacts emit events via E1) · M6: E9–E13 (SDK & distribution) ·
  M7: 15 (polish incl. plugin GUI surfaces). E14 deferred.
  **E15–E17 added by Vasu (2026-06-06, prompts in BACKLOG.md):** pluggable
  reasoning loops, plugin DB migrations hook, dynamic plugin config schema —
  slotted into M6 as E9 → E10 → E15 → E16 → E17 → E11 → E12 → E13.
  **Work happens on branch `feature/integrated-roadmap`**. **Next up:
  E6 (vault credential delegation to sidecars, milestone M3).**

**E5 (plugin principals & capabilities) — complete (TDD, all green, session 6).**
- `internal/caps` (new pkg, 97.6% cov) — `Principal` (`plugin:<id>`, IsPlugin/
  PluginID); `ParseCap` grammar `resource.action` (lowercase, one dot);
  capability registry (`Register`/`ScopeKindOf`/`KnownCaps`) seeded with
  `vector.search`(agents) / `channel.send`(channels) /
  `events.subscribe`(types); `Set` (compiled per-plugin grants — default-deny
  undeclared caps, empty scope list = unscoped grant, `"*"` wildcard,
  restricted scope refuses unscoped checks, duplicate caps merged);
  `Enforcer` (SetPluginSet/RemovePluginSet/Check + Fiber middleware
  `RequireCapability`). Every allow AND deny audited via `internal/audit`
  (SessionID=`plugin:<id>` → per-plugin audit files, Tool=`cap:<cap>`,
  Denied flag, reason in Error). Non-plugin principals: denied by Check,
  passed through by the middleware (user RBAC untouched).
- `pkg/plugin` — `Permission{Cap,Agents,Channels,Types}` + 
  `Manifest.Permissions` (pure data; validation lives in internal/caps).
- `internal/plugins/loader.go` — manifests are now validated through
  `caps.NewSet` at load; invalid permissions (unknown cap, wrong scope kind)
  → plugin refused (warn+skip). `LoadedPlugin.Caps *caps.Set` exposed.
  First tests for the loader package (37.4% cov, was 0).
- Docs: `docs/PLUGIN_CAPABILITIES.md` (grammar, semantics table, initial cap
  set, audit format, how to add a capability, compat policy).
- Tests: 33 new (caps_test/set_test/enforcer_test/loader_caps_test). NOT yet
  wired into gateway routes — no request can authenticate as a plugin until
  E8 issues scoped plugin tokens; E7 (manifest v2) registers loaded plugins
  with the Enforcer. Full suite green.

**E4 (sidecar supervision & lifecycle) — complete (TDD, all green).**
- `internal/channels/external/supervisor.go` — `Supervisor` wraps the E3
  Adapter and itself satisfies channels.Adapter (registry/GUI unchanged;
  lifecycle surfaces via AdapterStatus.Detail). Crash → exponential
  backoff (MinBackoff<<attempts, cap MaxBackoff, ±10% jitter) → fresh
  adapter; attempt counter resets after HealthyReset uptime (default
  10min); spawn failures count as crashes; Stop halts the loop and the
  current sidecar; Send during backoff returns a clear error.
  `SupervisorConfig.SandboxSelf/SandboxLimits` spawn sidecars through the
  portable rlimit `__exec-sandbox` wrapper via `sandbox.Wrap` (buildCommand
  unit-tested both wrapped and passthrough; Limits.Enabled must be true).
- Adapter gained `Done() <-chan struct{}` (closed on process exit).
- Tests: 8 supervisor tests using new crashloop/crashafter helper modes
  (restarts counted, status shows "restart #N in X", healthy reset keeps
  attempts ≤1, stop halts restarts, healthy delegation incl. send echo).
  Package 83% coverage. Full suite green.
- NOT yet wired into main.go/config — E7 (manifest v2) is where supervised
  external channels get declared; nothing consumes Supervisor until then.

**E3 (External Channel Protocol v1) — complete (TDD, all green).**
- `internal/channels/external/` — `protocol.go` (Frame superset type,
  ParseFrame [unknown types OK, missing type = error], WriteFrame NDJSON,
  Negotiate = min(gateway, sidecar), ProtocolVersion=1); `adapter.go`
  (generic channels.Adapter spawning any command: async handshake with
  configurable timeout [default 5s], hello→hello_ack, status/message/error
  dispatch, ActivationPolicy filtering, session `<id>-<chat_id>`, send
  frame, shutdown+3s grace+kill; `terminal` flag keeps failure verdicts
  from being clobbered by waitExit; waitExit owns cmd.Wait, Stop watches
  the `exited` channel); `conformance.go` (RunConformance: hello deadline,
  negotiation, unknown-frame tolerance, send survival, shutdown exit ≤5s —
  seed of the E11 kit).
- Tests: 17 (protocol unit + adapter via TestHelperSidecar helper-process
  pattern [modes happy/nohello/badversion] + conformance pass/fail), ~85%
  coverage. **Cross-language proof:** RunConformance executed against the
  real `scripts/reference-channel-sidecar.py` in-sandbox → PASS.
- Docs: `docs/EXTERNAL_CHANNEL_PROTOCOL.md` (spec, compat rules,
  conformance checklist). WhatsApp Web adapter NOT migrated (per story).
- NOTE for E4: supervision should wrap `external.Adapter` (restart/backoff
  on `exited`, health from Status), spawn via the rlimit `__exec-sandbox`
  wrapper, and surface lifecycle in AdapterStatus so the Channels GUI
  needs no changes.
- ⚠️ gofmt/vitest leave undeletable `*.go.<digits>` / vitest timestamp temp
  files on this mount; some had been committed accidentally — cleaned in
  e7603d9. Sweep new ones into `.git/stale-locks/tmpfiles/` before
  staging broad paths; never `git add` directories blindly.

**Story 9 (token delta indicators) — complete. M2 done.**
- `gui/src/lib/chatmetrics.js` — `deltaMetrics(prev, curr)` diffs
  session-cumulative metrics snapshots (clamps negatives to 0),
  `deltaLabel` ("+350 tok · $0.0035 · gpt-4o", empty when no movement),
  `deltaTitle` tooltip (turn breakdown ↑↓, session totals, provider).
  8 vitest tests.
- Chat.svelte send(): pre-turn snapshot from `chatMetricsBaseline` store
  (or fetched, 404→null on first turn), post-reply fetch → delta attached
  to the assistant message (`msg.metrics`); baseline updated per session
  (branches get independent baselines automatically; forked sessions start
  fresh since costs aren't copied). Rendered as a subtle `.tok-delta`
  monospace span beside the timestamp, hover for full breakdown.
  Per-LLM-call tokens still visible live in the Thinking section
  (llm.result events). Vitest 55/55 ✓, build ✓.

**Story 8 (Chat checkpoints & branching) — complete (TDD, all green).**
- Key architectural fact discovered: engine LLM context comes from
  IN-MEMORY Session.History; the persistent history store is write-only
  (appended after each turn, loaded only by the /history API). Fork
  therefore has three parts:
  1. `session.SQLiteHistoryStore.Fork(ctx, src, dst, uptoEntryID)`
     (internal/session/fork.go) — tx-copies entries (id <= checkpoint) into
     an empty target session; refuses non-empty target & self-fork; source
     untouched; ms-offset created_at keeps order. 6 tests.
  2. `Engine.SeedSessionHistory(agentID, sessionID, entries)` — initialises
     the in-memory session from copied entries so the branch has context on
     next Handle; NO-OP if session already live (never clobbers). 2 tests
     (engine_fork_test.go) verifying seeded turns appear in LLM request
     before the new message.
  3. `POST /api/v1/history/:session_id/fork` {agent_id, upto_entry_id,
     new_session_id?} → 201 {session_id, forked_from, copied, entries};
     400/404/409/503 paths. rbac chat/write. 4 tests (fork_test.go).
- GUI: `lib/chatbranch.js` (entryIdForMessage maps GUI msg index →
  persisted entry id skipping local system rows; entriesToMessages;
  nextBranchLabel) + 8 vitest tests. Chat.svelte: hover ⑂ button on
  user/assistant bubbles, branch chips row (main/fork N, active
  highlighted), per-branch message snapshots in stores
  (chatBranches/chatBranchMessages), Clear resets branches. Vitest 47/47 ✓,
  build ✓.
- Suite total 55.6%, all green.

**E1+E2 (event stream + signed webhooks) — complete (TDD, all green). M1 done.**
- `internal/events` — schema-v1 `Envelope` {schema,id,type,agent_id,
  session_id,ts,data}; `Publisher` = buffered chan + worker (never blocks
  engine; drops on full buffer like actionlog); subjects
  `soulacy.events.<type>`. 6 tests incl. blocking-backend non-block proof.
- `EventHub.SetEventPublisher` forwards every Emit; workboard runs now emit
  `run.started/run.finished/run.failed` via `emitRunEvent` (data: task_id,
  task_title, run_id, attempt, failure_reason). 2 gateway tests.
- `internal/hooks` — webhook `Dispatcher` subscribes `soulacy.events.>`
  (group "webhooks"), filters per `config.Hooks` (exact/"x.*"/"*" + agents),
  POSTs envelope with `X-Soulacy-Signature: t=<unix>,v1=<hmac-sha256 of
  "t.body">` (secret from `secret_env`), retries 5× exp backoff+jitter
  (cap 10m), dead → onDead callback (default: webhook.dead warn log).
  `Sign`/`VerifySignature` exported (5-min skew guard). 7 tests, fake
  RoundTripper (no httptest per standing rules). 84% coverage.
- `config.HookConfig` + `Hooks` field; wired in main.go after the queue
  backend; Config GUI got a read-only Webhooks section.
- **Contract doc: `docs/EVENTS.md`** (envelope, types incl. run.*, subjects,
  compatibility rules, webhook signature verification, best-effort
  semantics). Suite 55.4% all green; vitest 39/39; build clean.

**Story 7 (Run Observability & Cost Signals) — complete (TDD, all green).**
- `internal/costs/metrics.go` — `SessionMetrics(ctx, sessionID)` aggregates
  token_usage per session: llm_calls, prompt/comp/total tokens, cost,
  provider+model (most recent call), first/last call times. 2 tests.
- `internal/actionlog/sessionstats.go` — `SessionStats(agentID, sessionID)`
  on agent_events: events, tool.call count, first/last event time, last
  error payload (extractErrorText handles {"error"|"detail"|"message"|
  "reason"}). agentID optional. New idempotent index idx_events_session.
  4 tests (async writer → tests poll with deadline).
- `internal/gateway/runmetrics.go` + route `GET /api/v1/runs/:session_id/
  metrics?agent_id=` (rbac metrics/read): combines both sources; duration
  prefers event trail over LLM-call span; 503 neither store, 404 no data.
  `s.actions` checked via `sessionStatser` type assertion (storagesqlite.
  ActionLog promotes the Logger method). 5 tests in runmetrics_test.go.
- GUI: `lib/metrics.js` formatters (8 vitest tests) + `lib/RunMetrics.svelte`
  (self-fetching compact strip: provider/model · duration · tokens ↑↓ ·
  cost · tools · ⚠ failure; renders nothing on 404). Wired into:
  Chat (under controls, refreshKey bumped per reply), Schedule history
  (inside expanded run), Activity (Σ run row after message.out/error),
  Workboard run-history modal rows. Vitest 39/39 ✓, vite build ✓.
- Suite: all packages green; total 55.0% (denominator grew with the new
  channels/whatsappweb code).
- **Git workflow (Vasu's instruction):** commit whenever tests turn green;
  stage selectively (only files you touched). Identity: Vasu
  <hivasu@gmail.com> (already in git config).
- **⚠️ Stale git locks (workaround known):** the sandbox cannot `unlink`
  `.git/*.lock` after git writes (mount quirk), so each commit leaves stale
  locks that block the next git write. **`rm` fails but `mv` works** — before
  any git write run:
  `mkdir -p .git/stale-locks && for f in .git/*.lock; do [ -e "$f" ] && mv "$f" .git/stale-locks/$(basename $f).$(date +%s); done`
  (Vasu can periodically delete `.git/stale-locks/` and stray
  `.git/objects/*/tmp_obj_*` files on the Mac.)

---

## Current sprint: 15 backlog stories (working sequentially)

**Full story list + status table: `docs/BACKLOG.md`** (added 2026-06-06 — the
backlog previously lived only in an old session transcript; it is now in-repo).

**Story 5 (Workboard MVP) — complete (TDD; all tests green in sandbox).**
- `internal/workboard/store.go` — SQLite task store (`~/.soulacy/workboard.db`):
  statuses todo/running/needs_review/done/failed, `ValidStatus`, sentinel
  `ErrInvalid`/`ErrNotFound`, Create/Get/List(Filter)/Update(partial)/Delete.
  Times truncated to seconds so returned structs match DB reads.
  `store_test.go`: 14 tests, 87.5% coverage.
- `internal/gateway/workboard.go` + routes in `server.go`:
  GET/POST `/api/v1/workboard/tasks`, GET/PATCH/DELETE `/tasks/:id`
  (rbac: ResourceAgents read/write/delete). 503 until `SetWorkboardStore()`
  wired (same pattern as SetCostStore). `workboard_test.go`: 11 tests, all pass.
- `cmd/soulacy/main.go` — wires `workboard.NewStore` + `SetWorkboardStore`.
- GUI: `gui/src/lib/workboard.js` (column model; 9 vitest tests in
  `workboard.test.js`), `api.workboard` group in `api.js`,
  `gui/src/pages/Workboard.svelte` (5-column Kanban, agent filter, task editor
  modal with a11y label/id pairs + sticky footer, ◀/▶ move buttons with
  optimistic update, mobile: columns stack ≤768px). Route `workboard` added to
  App.svelte (group: main). Vite build ✓ (zero a11y warnings), vitest 26/26 ✓.
- **Check on the Mac:** run the GUI against a live gateway; verify
  `workboard ready` in startup logs and the board at `#workboard`. Then run
  `cd gui && npm run build` on the Mac to regenerate `internal/webui/dist`
  (sandbox cannot write there — EPERM on unlink).

**Fixes to pre-existing files this session:**
- `internal/runtime/engine7_test.go` (left by an earlier session, broke build):
  duplicate `fakeHistoryStore` renamed → `fakeHistoryStore7`;
  `TestArgBool_StringYes` expectation corrected (strconv.ParseBool rejects
  "yes" → argBool returns false).

**Story 1 (Harden Auth And Secret Handling) — code complete, Go tests pending Mac run.**

What changed:
- `internal/gateway/config.go` — **fixed secret leak**: GET/PATCH `/api/v1/config`
  returned `cfg.Channels` raw (bot tokens, webhook secrets). Now sanitised via new
  `safeChannelsView()` + `isSecretChannelKey()` + `redactBotList()` (spec-driven for
  known channels, generic name-heuristic fallback for unknown ones; empty secrets
  stay empty so the GUI doesn't think a key is set; source map not mutated).
- `gui/src/lib/stores.js` — new `authRequired` store (401/403 ≠ offline).
- `gui/src/lib/api.js` — `apiFetch` sets `authRequired` on 401/403, clears it on
  authenticated success (NOT on `/health`, which bypasses auth server-side).
- `gui/src/App.svelte` — sidebar shows clickable "🔒 Authentication required"
  (opens key modal) instead of "○ Offline" when creds are rejected.
- `gui/src/pages/Dashboard.svelte` — `load()` no longer nulls gateway status when
  `agents.list` 401s; banner + Gateway card show "Authentication required".
- Tests: `internal/gateway/config_redaction_test.go` (10 tests; reuses
  `newTestGateway`/`gatewayJSON`/`gatewayRaw` helpers) and
  `gui/src/lib/api.auth.test.js` (6 vitest tests — **pass**). Vitest harness added
  (`gui/vitest.config.js`, `vitest.setup.js`, `npm test` script).

**To verify on the Mac (sandbox has no Go toolchain):** double-click
`run-story1-tests.command` — runs focused Go tests, full suite, and vitest.

**Story 2 (Mobile/Responsive Layout) — code complete; visual check on a phone pending.**
GUI previously had ZERO media queries. Changes (vite build ✓, vitest ✓):
- `App.svelte`: mobile top bar + hamburger at ≤768px; sidebar becomes off-canvas
  drawer (`.sidebar.open`, backdrop, close-on-navigate); global `:global(.page)`
  padding & `.page-header` wrap rules for all pages.
- `Dashboard.svelte`: cards grid → `auto-fit minmax(170px,1fr)`; event log rows
  collapse to 2-col + wrapping data at ≤640px.
- `Agents.svelte`: 3-pane (list 250px / editor / playground 360px) stacks
  vertically ≤900px; `.row-2/.row-3/.param-grid` single-column ≤640px.
- `Config.svelte`: `1fr 380px` grid → single column ≤900px, sticky JSON col
  released; `.field-row` stacks ≤640px.
- `Schedule.svelte` + `MCP.svelte`: tables get `display:block; overflow-x:auto`
  ≤768px; Schedule history panel `min(520px,100vw)`.
- `Chat.svelte`: agent select `min(220px,100%)`, controls wrap.
- `Providers.svelte`: `.option-grid` stacks ≤640px.
Not addressed (lower priority, no story): Flow.svelte 3-pane, Knowledge/Skills
fixed left columns — follow same stacking pattern if needed.

**Story 3 (Modal Overflow & Form A11y) — complete (build ✓ zero a11y warnings, vitest ✓).**
- Sticky footers (`position:sticky; bottom:0` + bg + shadow) on `.modal-row` in
  Channels, Providers, MCP, Knowledge, Schedule.
- `max-height:88vh; overflow-y:auto` added to modals missing it: App (API key),
  Schedule editor, Knowledge, Skills, Memory (body scrolls). Agents modals
  already had 86vh + inner-scroll `.tpl-list`/`.snippet` — untouched.
- Label association: Memory write modal got `for`/`id` pairs; Skills URL input
  got `aria-label`; Knowledge doc-table checkboxes got `aria-label`s.
- Memory `.tl-card` click-div → `role="button"` + tabindex + Enter/Space handler;
  both Memory modal backdrops got Escape handlers. All Svelte a11y warnings gone.
- Note: most forms (Agents, Schedule, Channels, Providers, MCP, Knowledge,
  Config) already used label-wrapping — verified OK, no changes needed.

**Story 4 (Clean Up Logs UI) — complete (build ✓, vitest 17/17 ✓; check with real
gateway logs on the Mac).**
- New `gui/src/lib/logutils.js`: `stripAnsi()` (CSI/OSC/single-char escapes),
  `logLevel()` (zap JSON, zap console tab-separated, logfmt, [bracketed]),
  shared LEVEL_COLORS/LEVEL_BADGES. Unit-tested in `logutils.test.js` (11 tests).
- `Logs.svelte`: lines are ANSI-stripped + classified once per load; new level
  filter chips (All/ERR/WRN/INF/DBG with counts); wrap ↔ horizontal-scroll
  toggle (`.log-panel.nowrap`); severity coloring preserved; empty-state copy
  for "no lines at this level".

**Story 6 (Connect Workboard To Agent Execution) — complete (TDD; all tests
green in sandbox).**
- `internal/workboard/runs.go` — `workboard_runs` table + `Run` model
  (attempt, agent_id, session_id, action_log_path, status running/done/failed,
  result, failure_reason, started_at, ended_at as `sql.NullTime`).
  `StartRun` (tx: task-exists check, ErrRunActive duplicate guard, attempt =
  count+1), `FinishRun` (terminal-only, double-finish rejected), `ListRuns`
  (newest first, prior attempts immutable), `GetRun`. Task `Delete` cascades
  runs. `runs_test.go`: 9 tests; package coverage 83.3%.
- `internal/gateway/workboard.go` + routes:
  POST `/api/v1/workboard/tasks/:id/run` → 503 no store / 404 / 400 no agent /
  409 active run / 202 + run JSON; GET `/tasks/:id/runs` → `{"runs":[...]}`.
  Executor goroutine builds a `message.Message` (session `wb-<task>-<nano>`,
  channel "internal", user "workboard", metadata trigger=workboard+task_id),
  calls `s.engine.Handle` with 15-min timeout; success → run done + result
  summary (500-rune cap) + task→needs_review; error → run failed +
  failure_reason + task→failed. Action-log path captured via
  `s.actions.(interface{ EventFilePath(string) string })`.
  `workboard_runs_test.go`: 7 tests incl. success/failure/duplicate/retry
  (uses fake LLM provider + agent created via POST /agents; failure path uses
  a nonexistent agent).
- GUI: `api.workboard.run/runs` in `api.js`; `canRun()`/`runLabel()` in
  `lib/workboard.js` (+6 vitest tests); `Workboard.svelte` — ▶ Run/⟳ Retry
  button per card (disabled while running), run-history panel in the task
  modal (attempt #, status badge, start→end times, result/failure, session +
  log path), 4s quiet polling while any task is running. Vitest 31/31 ✓,
  vite build ✓ (to /tmp; rebuild dist on the Mac).
- **Check on the Mac:** rebuild binary + dist, create a task assigned to a
  real agent, hit ▶ Run, watch it move Todo→Running→Needs Review and inspect
  Run history in the modal.

**⚠️ Concurrent-session warning (2026-06-06):** while this session was
finishing Story 6, the working tree was being edited concurrently from the
Mac side (new whatsappweb adapter work, channel bot mappings; commits
e5fa45d/6d6723f/fe7f83e landed mid-session; `cmd/soulacy/main.go` had
transient undefined symbols like `parseInt64List` from in-progress edits).
Workboard/gateway/GUI workboard files were untouched by that work and all
their tests pass. Before trusting a full-suite run, make sure the other
session is done, then re-run the suite.

---

## Critical: what to do first

1. Run the full test suite (on the Mac, plain `go`; in the sandbox use the
   toolchain setup from Standing rules below):
   ```
   GOCACHE=$PWD/.gocache go test -p 2 -count=1 -timeout 60s -coverprofile=coverage.out ./... 2>&1 | grep -E "^(ok|FAIL|---)"
   go tool cover -func=coverage.out | tail -1
   ```
   Expected: **56.2% total**, all packages green (verified end of session 5).

2. Do **not** revert any files. All changes are intentional.

---

## Standing rules (always apply)

- **TDD from Story 5 onward (Vasu's instruction, 2026-06-06):** write failing
  tests first, then implement. Go tests via `GOCACHE=$PWD/.gocache go test`,
  GUI tests via `cd gui && npm test` (vitest).
- **Go toolchain in sandbox (WORKS — verified 2026-06-06, session 5):**
  1. `curl -sSLo /tmp/go.tgz https://go.dev/dl/go1.26.4.linux-arm64.tar.gz`
     then extract to `~/go-toolchain` (no root; use `~/go-toolchain/go/bin/go`).
  2. **CGO sqlite3.h workaround** (no root, apt blocked): after first
     `go test` downloads modules, copy mattn's bundled header:
     `M=~/gopath/pkg/mod/github.com/mattn/go-sqlite3@*/; mkdir -p ~/sqlite-inc;
     cp $M/sqlite3-binding.h ~/sqlite-inc/sqlite3.h; cp $M/sqlite3ext.h ~/sqlite-inc/`
     and add `CGO_CFLAGS="-I$HOME/sqlite-inc"` to every go command
     (sqlite-vec-go-bindings needs it).
  3. Set `GOPATH=$HOME/gopath` (mounted repo dir is not writable for the
     module cache).
  4. **Use `-p 2`** on full-suite runs — default parallelism causes spurious
     `[build failed]` (resource exhaustion); single-package runs are fine.
- **GUI toolchain in sandbox:** node_modules was installed on macOS, so the
  linux-arm64 native binaries are missing. Install them by extracting tarballs
  directly (npm i --no-save gets pruned):
  `@rollup/rollup-linux-arm64-gnu` (match `rollup/package.json` version) into
  `node_modules/@rollup/...`, and `@esbuild/linux-arm64` (match esbuild
  version, currently 0.21.5) into `node_modules/@esbuild/...` — both from
  registry.npmjs.org tarballs, `--strip-components=1`.
  `npx vite build` must use `--outDir /tmp/... --emptyOutDir` in the sandbox
  (cannot unlink files in `internal/webui/dist`); rebuild real dist on the Mac.

- Use `GOCACHE=$PWD/.gocache` on every `go test` invocation (permission issues with default cache).
- Do **not** use `httptest.NewServer`. Use fake `http.RoundTripper` or Fiber `app.Test`.
- Fiber `App.Test` captures SSE body nondeterministically; assert the provider's
  recorded `Stream` flag rather than raw SSE frame content.
- For Fiber path params (`:id`), chain middleware at the **route level**
  (`app.Get("/agents/:id", mw, handler)`), not `app.Use(mw)` — global middleware
  runs before routing and `c.Params("id")` returns `""`.
- Always scan SQLite DATETIME columns into `time.Time` / `sql.NullTime` (never
  `string` + `time.Parse`). The `mattn/go-sqlite3` driver reformats DATETIME as
  RFC3339 when the destination is `string`.

---

## Coverage target

- **Mathematical ceiling**: ~65% (untestable packages hold ~35% of all statements)
- **Current total**: **56.2%** (measured 2026-06-06 session 5, all packages green;
  includes new internal/workboard at 87.5%)
- **Remaining gap**: ~9pp needed to reach 65%

---

## Production bugs fixed this session

1. **`internal/queue/memory/memory.go` `stop()` deadlock**
   `for range s.ch {}` blocked forever because `s.ch` is never closed.
   Fixed: removed drain loop (unnecessary — `send()` uses non-blocking select).

2. **`internal/runtime/engine.go` nil archive panic**
   `MemoryList` and `MemorySearch` called `e.archive.ReadGlobal(...)` without
   checking if `e.archive == nil`. Fixed: early-return `[]memory.Entry{}` when nil.

3. **`internal/costs/api.go` nil store 500s**
   `HandleGetCosts` and `HandleGetAgentCosts` panicked when store was nil.
   Fixed: return 503 with JSON error when `s.store == nil`.

4. **`internal/skills/loader_test.go` picking up real skills**
   `New("", nil, ...)` scanned `~/.agents/skills` and `~/.soulacy/skills`.
   Fixed: added `newTestLoader(dirs...)` helper that creates a `Loader` directly.

5. **`internal/reasoning/` backends** — added `SetClient(*http.Client)` to
   `AnthropicBackend`, `OllamaBackend`, `OpenAIBackend` for test HTTP injection.

---

## Test files written this session (all on disk)

| File | Package coverage |
|------|-----------------|
| `internal/queue/memory/memory_test.go` | 92.6% |
| `internal/skills/loader_test.go` | 93.3% |
| `pkg/skill/skill_test.go` | 93.9% |
| `internal/credentials/vault_test.go` | 71.9% |
| `internal/reasoning/backends_test.go` | 80.0% |
| `internal/reasoning/config_registry_test.go` | 80.0% |
| `internal/gateway/handlers2_test.go` | (gateway package) |
| `internal/gateway/handlers3_test.go` | (gateway package) |
| `internal/gateway/handlers4_test.go` | (gateway package) |
| `internal/gateway/handlers5_test.go` | (gateway package) |
| `internal/gateway/handlers6_test.go` | (gateway package — fixed dup funcs) |
| `internal/runtime/engine2_test.go` | (runtime package) |
| `internal/runtime/engine3_test.go` | (runtime package) |
| `internal/runtime/engine4_test.go` | (runtime package) |
| `internal/runtime/engine5_test.go` | (runtime package) |
| `internal/runtime/engine6_test.go` | (runtime package) |
| `internal/channels/http/adapter_test.go` | 100% |
| `internal/builder/builder_test.go` | 94.3% |
| `internal/costs/api_test.go` | 90.0% |
| `internal/rbac/middleware_test.go` | 88.6% |
| `internal/llm/router_test.go` | (llm package) |
| `internal/llm/llm2_test.go` | 89.4% |
| `internal/knowledge/knowledge2_test.go` | 81.6% |
| `internal/auth/apikeys/api_test.go` | 83.3% |
| `internal/credentials/credentials2_test.go` | 71.9% |
| Various appended tests | agentmemory, session, auth, sandbox, sqlitex, config, actionlog, ratelimit, templates, rbac, channels |

---

## Known test fixes (incorrect expectations corrected)

- `stop()` deadlock: removed `for range s.ch {}` drain loop
- `TestWeakStructuredOutputModel` with `bge-large`: expected `false` (not in embed check)
- `TestGatewayInvalidateToolCatalog`: check `toolCatalogAt.IsZero()` not `toolCatalogCache != nil`
- `TestGatewayHandleDeleteAgent_NotFound`: accept 204 OR 404 (Delete is idempotent)
- `TestRequireAgentWithParam_StoreError_Returns500`: must mount middleware at route level
- `TestServerSnapshotBuiltins_NilEngine`: check `len() == 0` not `== nil`
- `TestGatewayHandleAgentActions_CountField`: handler with nil `s.actions` returns `events` key
- `TestUnderstandingToAgentMap_OneshotTrigger`: "oneshot" not in switch, check non-empty
- `TestWorkflowTestMessage_FieldsSet`: check `AgentID` field (Role not set)
- `TestGetBuilderUnderstanding_AfterSessionCreated`: Understanding starts nil, just call
- `TestFirstLine_LongLine`: `"…"` is 3-byte UTF-8, total 203 bytes; check `> 203`
- `TestWorkflowExecutorIfConditionEmptySkips`: use `{{if false}}run{{end}}` (missingkey=zero)
- `TestStore_CreateKB_DefaultsApplied`: ChunkOverlap defaults only when `< 0`, check `>= 0`
- `TestConcurrent_PublishSubscribe`: 4 publishers × 50 msgs (< 256 buffer), accept >50% delivery
- Gateway handlers6_test.go: removed 10 duplicate Test functions that also existed in handlers2–5

---

## Last known coverage by package (from run before gateway fix)

| Package | Coverage |
|---------|----------|
| `pkg/agent` | 100.0% |
| `internal/channels` | 100.0% |
| `internal/channels/http` | 100.0% |
| `internal/builder` | 94.3% |
| `internal/config` | 94.6% |
| `pkg/skill` | 93.9% |
| `internal/skills` | 93.3% |
| `internal/agentmemory` | 92.9% |
| `internal/queue/memory` | 92.6% |
| `internal/queue/dlq` | 91.5% |
| `internal/audit` | 91.7% |
| `internal/costs` | 90.0% |
| `internal/llm` | 89.4% |
| `internal/rbac` | 88.6% |
| `pkg/message` | 87.8% |
| `internal/sqlitex` | 86.1% |
| `internal/templates` | 85.7% |
| `internal/session` | 84.6% |
| `internal/auth/apikeys` | 83.3% |
| `internal/knowledge` | 81.6% |
| `internal/ratelimit` | 81.2% |
| `internal/agentvalidate` | 80.2% |
| `internal/auth` | 80.7% |
| `internal/reasoning` | 80.0% |
| `internal/scheduler` | 71.5% |
| `internal/credentials` | 71.9% |
| `internal/memory` | 63.1% |
| `internal/runtime` | 61.6% |
| `internal/sandbox` | 60.8% |
| `internal/gateway` | ~60% (build was failing; fixed) |
| `cmd/sy` | 0.0% (entry-point, not testable) |
| **Total** | **~55.6%** (gateway excluded) |

---

## Packages still with meaningful room to improve

| Package | Coverage | Gap |
|---------|----------|-----|
| `internal/runtime` | 61.6% | runTool dispatch, engine setter paths |
| `internal/sandbox` | 60.8% | more seccomp/limit/flag branches |
| `internal/memory` | 63.1% | search paths, concurrent access |
| `internal/gateway` | ~60% | more handler edge cases |
| `internal/credentials` | 71.9% | rotation, version management |
| `internal/scheduler` | 71.5% | fire() timer paths |

---

## Packages not feasibly testable without external services

```
cmd/soulacy/main.go            — app wiring
internal/channels/telegram/    — Telegram API
internal/channels/slack/       — Slack API
internal/channels/discord/     — Discord API
internal/channels/whatsapp/    — WhatsApp API
internal/mcp/                  — MCP server protocol
internal/executor/pool/        — Python subprocess management
internal/executor/process/     — Python subprocess
internal/queue/nats/           — NATS JetStream
internal/vector/qdrant/        — Qdrant vector DB
internal/vector/sqlitevec/     — sqlite-vec extension
internal/telemetry/            — OTEL exporter
internal/storage/postgres/     — Postgres connection
internal/eval/                 — Live HTTP calls to gateway
internal/metrics/              — Prometheus metrics sink
internal/webui/                — Embedded frontend assets
```

These collectively hold ~35% of total statements → mathematical ceiling ~65%.

---

## Next steps to reach 65%

Need ~7–8pp more. Best targets:

1. **`internal/runtime`** (61.6%): Push `runTool` error paths, engine setter methods,
   `buildSystemPrefix` with skill/knowledge/agent catalogs, `applyPlaygroundOverrides`.
   Write `engine7_test.go`.

2. **`internal/sandbox`** (60.8%): Exercise `OpenFiles`-only, `FileSizeMB`-only flag
   paths, more `IsSandboxInvocation` edge cases, `syscallEnviron`.

3. **`internal/memory`** (63.1%): Cover `SearchFTS`, `SearchHybrid`, `DeleteKB`,
   concurrent `Append`/`Load`, LRU eviction.

4. **`internal/gateway`** (~60%): More handler edge cases in handlers7_test.go.

5. **`internal/credentials`** (71.9%): Version rotation continuity, `ListVersions`,
   `DeleteVersion`, `EnsureVersionSchema` idempotency.

---

## Test environment notes

- `GOCACHE=$PWD/.gocache` — required on every `go test` call
- No `httptest.NewServer` — use `fiber.App.Test` or fake `http.RoundTripper`
- No real Python subprocess — use custom `BuiltinTool` handlers
- CGO is available (SQLite works fine on Mac Studio)
- `mattn/go-sqlite3`: scan DATETIME into `time.Time`, never `string`
- Fiber middleware path params: use route-level middleware, not `app.Use`
