# Soulacy Backlog

Source of truth for all planned work. Progress is tracked in
`SESSION_HANDOFF.md`.

Two story sets, executed as **one integrated roadmap** (below):
- **Sprint stories 1–15** (provided by Vasu, 2026-06-06)
- **Extensibility stories E1–E14** (derived from `docs/EXTENSIBILITY.md`,
  2026-06-06) — make the framework extremely open to independent developers
  without compromising the single-binary model or the security stack
  (RBAC, vault, audit, sandbox).

## Integrated roadmap

Stories are sequenced into milestones so each track feeds the other: Story 7
produces the run-level data that E1/E2 publish; the sidecar foundation
(E3–E8) lands before the voice stories so realtime providers can be built as
supervised sidecars; the SDK work (E9–E12) comes after the protocols have
stabilized through real use.

| Order | Milestone | Stories (in order) | Status / notes |
|-------|-----------|--------------------|----------------|
| ✅ | M0 Foundation | 1, 2, 3, 4, 5, 6 | done (sessions 4–5) |
| ✅ | M1 Observability | **7 → E1 → E2** | done (session 5, branch feature/integrated-roadmap). Run metrics API+GUI; schema-v1 events on soulacy.events.>; signed webhooks w/ retries. See docs/EVENTS.md. |
| ✅ | M2 Chat depth | **8 → 9** | done (session 5). Branching via /history/:id/fork + engine seeding; per-reply token deltas diffing session metrics. |
| ✅ | M3 Sidecar foundation | **E3 → E4 → E5 → E6 → E7 → E8** | done (session 6). Protocol v1, supervised lifecycle, plugin principals/capabilities, vault credential delegation w/ rotation restart, manifest v2 (sidecar channels/providers/skills/GUI), GUI mounts at /plugins/:id/ui/ in sandboxed iframes with scoped splg_ tokens + default-deny API gate. Docs: PLUGIN_MANIFEST/CAPABILITIES/CREDENTIALS + EXTERNAL_CHANNEL_PROTOCOL. |
| 4 | M4 Voice | **10 → 11** | Story 10's spike must evaluate the External Channel Protocol/sidecar runtime (E3/E4) as the integration vehicle for OpenAI Realtime / Gemini Live; Story 11 should ship the voice bridge as a supervised sidecar with vault-delegated credentials (E6) rather than baking SDKs into the binary. |
| 5 | M5 Reliability & workboard depth | **12 → 13 → 14** | Story 12 reuses the duplicate-run guard pattern from Story 6; Story 13's artifacts attach to workboard runs and emit `run.artifact` events through E1 so observers see outputs; Story 14 events likewise. |
| 6 | M6 SDK & distribution | **E9 → E10 → E15 → E16 → E17 → E11 → E12 → E13** | structural investment, done once protocols are proven by M3/M4 consumers. E15 (pluggable reasoning) and E16 (plugin migrations) build directly on E9's SDK extraction; E17 (dynamic plugin config) feeds E13's install UX. |
| 7 | M7 Polish | **15** | scope now includes plugin GUI surfaces from E8/E13 (nav consistency, install/permission dialogs, empty states). |
| ⏸ | Deferred | E14 (WASM) | demand-gated; see EXTENSIBILITY.md §7. |

## Story prompts

### Story 1: Harden Auth And Secret Handling
Review Soulacy's authenticated GUI and backend config APIs for secret exposure
and auth-state UX. Fix any path where secrets such as channel bot tokens, API
keys, webhook secrets, or credentials are returned to the browser unredacted.
Update the unauthenticated UI state so auth failures are shown as
"Authentication required" rather than "Gateway Offline." Add focused tests for
config redaction and auth-error display behavior.

### Story 2: Improve Mobile And Responsive Layout
Audit the Soulacy GUI at desktop, tablet, and mobile breakpoints. Replace the
fixed sidebar behavior on small screens with a usable collapsed drawer or
mobile navigation pattern. Ensure main content has enough width, no accidental
horizontal scrolling, and primary actions remain reachable. Verify Dashboard,
Agents, Chat, Schedule, Config, Logs, and Providers on mobile.

### Story 3: Fix Modal Overflow And Form Accessibility
Audit all Soulacy GUI modals and dense forms for overflow and accessibility
issues. Make large modals scroll within the viewport with sticky footer
actions. Ensure inputs, selects, textareas, checkboxes, and radio groups are
programmatically associated with labels. Prioritize API key modal, New Agent
editor, Schedule editor, Channel editor, Provider editor, and Memory modals.

### Story 4: Clean Up Logs UI
Improve the Soulacy Logs page so log output is readable and
production-friendly. Strip or render ANSI escape codes, preserve useful
severity coloring, and make long lines wrap or scroll predictably. Add
filtering for error/warn/info/debug if not already reliable. Verify log
display with real gateway logs.

### Story 5: Build Workboard MVP
Design and implement a Soulacy Workboard feature for async agent task
orchestration. Add a backend task model and API with statuses Todo, Running,
Needs Review, Done, and Failed. Build Workboard.svelte with Kanban columns,
task creation, assignment to an agent, run/retry actions, status updates, and
links to session/action logs. Keep the MVP focused and durable.

### Story 6: Connect Workboard To Agent Execution
Integrate Workboard tasks with Soulacy agent execution. A task should run
through the selected agent, capture session ID, action log path, start/end
timestamps, result summary, failure reason, and output artifacts where
available. Prevent duplicate concurrent runs for the same task. Add retry
behavior that preserves prior attempts.

### Story 7: Add Run Observability And Cost Signals
Add run-level observability across Chat, Schedule, Activity, and Workboard.
Show model/provider, duration, token counts, estimated cost, tool-call count,
and failure summary per run where data is available. Use existing
cost/actionlog infrastructure before adding new storage. Make the UI compact
and scannable.

### Story 8: Add Chat Checkpoints And Branching
Implement visual checkpoints and branching in Soulacy Chat. Users should be
able to fork a conversation from a prior assistant/user message, continue in
the new branch, and see which branch is active. Preserve session history
cleanly and avoid mixing events between branches. Add lightweight UI
affordances without making Chat feel cluttered.

### Story 9: Add Token Delta Indicators In Chat
Improve Chat Tester with token and cost feedback. For each assistant response,
show token delta, cumulative session tokens, model/provider, and estimated
cost when available. Keep indicators visually subtle but easy to inspect.
Ensure streaming/thinking sections and final messages attach the correct
metrics.

### Story 10: Add Realtime Voice Exploration Spike
Run a technical spike for realtime voice in Soulacy Chat. Compare OpenAI
Realtime and Gemini Live integration paths, including browser microphone
capture, WebRTC/WebSocket transport, interruption handling, authentication,
cost tracking, and provider configuration. Produce a small proof-of-concept or
implementation plan, but do not commit to full product integration yet.

*Integration (M4): the spike must also evaluate running the realtime bridge
as a supervised stdio sidecar (External Channel Protocol, E3/E4) so provider
SDKs and audio dependencies stay out of the core binary.*

### Story 11: Add Voice Panel MVP
After the voice spike, implement a minimal push-to-talk voice panel in Chat.
Support microphone permission flow, start/stop recording, stream audio to the
selected realtime provider, display transcript, and attach responses to the
current chat session. Include clear cost/status indicators and graceful
fallback when no realtime provider is configured.

*Integration (M4): implement the provider bridge as a supervised sidecar with
vault-delegated credentials (E4/E6) if the Story 10 spike confirms the
approach; the binary stays free of vendor audio SDKs.*

### Story 12: Improve Schedule Reliability And Missed Runs
Finish hardening Soulacy scheduled agents. Verify service restart behavior on
Linux, macOS, and Docker. Validate that schedule.run_missed_on_startup catches
up only the latest missed cron within missed_startup_window, persists
scheduler state correctly, and does not duplicate runs. Add UI copy and tests
for missed-run behavior.

### Story 13: Add Workboard Artifact Tracking
Extend Workboard tasks to track generated artifacts and output files. Detect
files produced during a run when possible, attach them to the task, and show
them in a task detail panel with open/download actions. Include artifact
metadata such as path, size, created time, and originating tool/run.

*Integration (M5): artifacts attach to workboard run records (Story 6 schema)
and emit `run.artifact` events through the E1 event layer so webhooks and
observers see produced files.*

### Story 14: Add Task Collaboration Primitives
Add lightweight collaboration primitives for Workboard tasks: comments,
reviewer notes, task owner, priority, tags, and due date. Keep the model
simple and local-first. Make task detail views support review workflows
without overwhelming the Kanban board.

### Story 15: Product Polish Pass
Perform a product polish pass on the Soulacy GUI. Fix stale branding such as
the browser title, tighten empty states, standardize button labels/icons,
reduce visual noise in dense pages, and ensure destructive actions are clearly
separated from routine actions. Prioritize consistency across Dashboard,
Agents, Chat, Schedule, Workboard, Config, Logs, and Providers.

*Integration (M7): scope includes the plugin-facing GUI surfaces added by
E8/E13 — plugin nav entries, iframe panels, install/permission dialogs — so
third-party UI feels native.*

---

## Extensibility track (E1–E14)

Design authority: `docs/EXTENSIBILITY.md`. Standing constraints for every
story: single static binary preserved; no dynamic code loading; plugins are
distinct security principals with manifest-declared, default-deny
capabilities; all contracts versioned; TDD; commit on green.

Status for all E-stories is tracked in the Integrated roadmap table above
(milestones M1, M3, M6).

### Story prompts

### Story E1: Publish Internal Events To The Queue Backend
Define event schema v1 (explicit `schema` field; types message.in,
message.out, tool.call, tool.result, error, run.started, run.finished,
run.failed including Workboard runs) and publish every EventHub emission to
the existing queue backend (memory or NATS) without blocking the engine.
Keep WebSocket behavior unchanged. Add focused tests for schema shape,
non-blocking emit, and NATS subject layout. Document the schema and the
additive-fields compatibility rule.

### Story E2: Add Signed Outbound Webhooks
Add a `hooks:` config section mapping event-type and agent filters to HTTPS
endpoints. Deliver events from the queue buffer (never inline from Emit),
sign payloads with HMAC-SHA256 over `<timestamp>.<body>` in an
`X-Soulacy-Signature` header, retry with exponential backoff and jitter
(default 5 attempts), and write a `webhook.dead` audit entry on exhaustion.
Document the explicit best-effort delivery guarantee and signature
verification with examples. Add a Hooks section to the Config GUI.

### Story E3: Define The External Channel Protocol
Generalize the WhatsApp Web sidecar's NDJSON-over-stdio framing into a
documented External Channel Protocol v1: hello/hello_ack handshake with
integer protocol version negotiation, status, message, send, error, and
shutdown frames, unknown frames ignored for forward compatibility. Implement
a generic ExternalChannelAdapter that satisfies channels.Adapter and spawns
any declared command. Provide a reference sidecar in Node or Python and a
protocol conformance fixture. Do not force-migrate the existing WhatsApp Web
adapter.

### Story E4: Add Sidecar Supervision And Lifecycle
Supervise sidecar processes: handshake deadline, health tracking, crash
restart with exponential backoff and a healthy-reset window, graceful
shutdown (shutdown frame, SIGTERM grace, SIGKILL), and spawn through the
existing rlimit `__exec-sandbox` wrapper as the portable sandbox baseline.
Surface lifecycle state through AdapterStatus so the Channels GUI shows
sidecar health without new UI work. Test restart/backoff behavior with a
deliberately crashing fake sidecar.

### Story E5: Introduce Plugin Principals And Capabilities
Add a `plugin:<id>` security principal distinct from user roles. Define a
capability grammar (`cap` + scope, e.g. vector.search limited to listed
agents, channel.send limited to listed channels, events.subscribe limited to
listed types) declared in the plugin manifest, default-deny. Enforce at the
host-API boundary and record allow/deny decisions in the audit log. Keep the
existing user RBAC model untouched. Start with a small capability set and
document how new capabilities are added.

### Story E6: Delegate Credentials From The Vault To Sidecars
Let plugin manifests declare required credentials as vault paths scoped to
the plugin's namespace. Inject only the declared secrets into the sidecar's
environment at spawn. Restart the sidecar when a referenced credential is
rotated (the vault already versions secrets). Never write secrets to disk or
logs; document the env-transport limitations and the v2 handshake-delivery
option. Test that undeclared credentials are never visible to the subprocess.

### Story E7: Implement Plugin Manifest v2
Extend `plugin.yaml` with `manifest_schema: 2` supporting: sidecar channels,
OpenAI-compatible provider declarations, skills directories, GUI mounts,
credentials, and permissions — alongside the existing Python tools. Wire the
already-declared `pkg/plugin.Registry` so manifest channels become supervised
sidecar adapters and providers reuse the existing OpenAI-compatible wrapper.
Warn-and-skip on schema v1 manifests (no breakage). Validate manifests with
clear error messages and add loader tests for each contribution type.

### Story E8: Add Plugin GUI Mounts
Serve plugin static assets at `/plugins/<id>/ui/` and render them in the
Svelte shell inside a sandboxed iframe, with a nav entry taken from the
manifest. Issue the iframe a scoped plugin token bound to the `plugin:<id>`
principal and its declared capabilities — never the user's API key. Enforce
the token at the API layer and test that a plugin token cannot reach
endpoints outside its capability set.

### Story E9: Extract A Versioned Go SDK Module
Create `github.com/soulacy/soulacy/sdk` as a separate Go module with its own
semver. Promote channels.Adapter, llm.Provider, and the queue/vector/storage
backend interfaces into it, leaving type aliases at the old internal paths so
every existing file and test compiles unchanged. Re-export pkg/message
canonical types. Write the compatibility policy into the SDK README
(extension interfaces for additive methods, never widening existing ones).

### Story E10: Add Factory Registries And Decompose main.go
Add database/sql-style factory registries to the SDK (RegisterFactory for
channels, providers, backends) with init()-based driver self-registration and
a generated blank-import file in cmd/soulacy. Resolve config entries against
the registry with fallback to the existing hardcoded wiring (strangler);
delete the hardcoded paths only when all built-ins are registry-routed and
the suite is green. Extract an internal/app package exposing app.New(cfg,
opts...) and shrink cmd/soulacy/main.go to a thin composition root,
migrating one subsystem at a time with a green suite between steps.

### Story E11: Ship Conformance Test Kits
Export test suites from the SDK that extension authors run out-of-tree:
channeltest.RunAdapterSuite, providertest.RunProviderSuite, and a sidecar
protocol conformance runner that exercises handshake, framing, and lifecycle
against any sidecar command. Run the kits against all built-in
implementations in CI so the contract and the implementations cannot drift.

### Story E12: Build The Flavored-Binary Tool
Write `soulacy build --with <module>@<version>` (start as a script, promote
to a subcommand) that generates the blank-import registration file, resolves
modules, and compiles a single static binary containing the extra drivers.
Verify the output runs the conformance kits. Document the custom-distribution
workflow end to end with the Matrix-channel example.

### Story E13: Add Plugin Discovery And Install UX
Let users install plugins from a git URL or checksummed archive through the
GUI: show the manifest's requested capabilities and credentials for explicit
approval before activation, list installed plugins with enable/disable and
remove, and re-prompt when an updated manifest requests new permissions.
Local-first (no central marketplace dependency); design the metadata so a
registry/marketplace can be layered on later.

### Story E14: WASM Transform Sandbox (deferred)
Demand-gated. Only if a concrete need emerges that skills and sidecars cannot
serve: embed wazero (pure Go) to run uploaded WASM as pure bytes→bytes
transforms with hard context deadlines, no filesystem, no network, and no
host API beyond the input payload. Revisit the decision record in
docs/EXTENSIBILITY.md §7 before starting.

### Story E15: Pluggable Reasoning Loops
Extend the Soulacy reasoning engine to support custom, pluggable reasoning
strategies (such as Tree of Thought, Self-Reflection, or Consensus Swarms)
beyond the hardcoded ReAct and Plan-Execute loops. Promote the `LLMBackend`
and reasoning interfaces to the `pkg/` SDK, establish a
`RegisterReasoningStrategy` factory registry, and map agent
`reasoning.strategy` keys in `SOUL.yaml` to these registered strategy
executors at runtime. Include conformance tests verifying that a custom
reasoning loop can be successfully injected and run.

*Integration (M6): depends on E9's SDK module; the registry follows E10's
factory pattern; the conformance test joins the E11 kits.*

### Story E16: Plugin Database Migrations Hook
Add support for plugin-specific database schemas, enabling dynamic plugins
to create and manage their own SQLite tables without modifying the core
system database schemas. Expose a `RegisterMigration(name string, upSQL
string)` hook in the `pkg/storage` SDK that executing plugins can register
during `Init()`. Ensure these migrations are run transactionally during the
database boot phase and block plugins from executing raw DDL changes against
core system tables (like `agents` or `runs`). Add integration tests
verifying a plugin's ability to migrate and query its own tables.

*Integration (M6): depends on E9; migration namespace ties into the E5
plugin-principal model (a plugin may only touch its own `plugin_<id>_*`
tables); guard list must cover all core schemas (token_usage, agent_events,
conversation_history, workboard_tasks/runs, credentials, rbac).*

### Story E17: Dynamic Plugin Configuration Schema
Enhance the core gateway YAML parser and GUI configuration editor to support
dynamic, plugin-specific settings without throwing unmarshaling errors on
unrecognized keys. Update the configuration parser to collect arbitrary
top-level settings under a generic `plugins_config map[string]any`
dictionary, making this data accessible to plugins through the registry
initialization context. Ensure that the GUI configuration editor
(`Config.svelte`) preserves these custom data blocks unmutated when writing
configuration updates back to `config.yaml`.

*Integration (M6): pairs with E7 (manifest v2) and E13 (install UX shows
plugin settings); config GET redaction (Story 1's safeChannelsView pattern)
must extend to plugins_config so plugin secrets never reach the browser.*
