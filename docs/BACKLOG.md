# Soulacy Backlog — 15 Stories

Source of truth for the current sprint (provided by Vasu, 2026-06-06).
Progress is tracked in `SESSION_HANDOFF.md`.

## Status

| # | Story | Status |
|---|-------|--------|
| 1 | Harden Auth And Secret Handling | ✅ done (session 4) |
| 2 | Improve Mobile And Responsive Layout | ✅ done (session 4) |
| 3 | Fix Modal Overflow And Form Accessibility | ✅ done (session 4) |
| 4 | Clean Up Logs UI | ✅ done (session 4) |
| 5 | Build Workboard MVP | ✅ done (session 5) |
| 6 | Connect Workboard To Agent Execution | ✅ done (session 5) |
| 7 | Add Run Observability And Cost Signals | ⬜ next |
| 8 | Add Chat Checkpoints And Branching | ⬜ |
| 9 | Add Token Delta Indicators In Chat | ⬜ |
| 10 | Add Realtime Voice Exploration Spike | ⬜ |
| 11 | Add Voice Panel MVP | ⬜ |
| 12 | Improve Schedule Reliability And Missed Runs | ⬜ |
| 13 | Add Workboard Artifact Tracking | ⬜ |
| 14 | Add Task Collaboration Primitives | ⬜ |
| 15 | Product Polish Pass | ⬜ |

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

### Story 11: Add Voice Panel MVP
After the voice spike, implement a minimal push-to-talk voice panel in Chat.
Support microphone permission flow, start/stop recording, stream audio to the
selected realtime provider, display transcript, and attach responses to the
current chat session. Include clear cost/status indicators and graceful
fallback when no realtime provider is configured.

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
