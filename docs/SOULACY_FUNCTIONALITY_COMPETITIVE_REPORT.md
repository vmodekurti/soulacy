# Soulacy Functionality And Competitive Roadmap

Date: 2026-07-01

## Executive Summary

Soulacy is best understood as a self-hosted agent runtime, not merely a personal assistant. Its strongest current advantages are: single-binary Go deployment, declarative `SOUL.yaml` agents, a broad local-first control plane, provider-agnostic LLM routing, MCP client support, scheduled agents, channel adapters, encrypted secrets, plugin/sidecar infrastructure, observability, Workboard, and a rich Svelte GUI.

The most important competitive lesson from OpenClaw and Hermes is that users do not buy "a runtime" in the abstract. They buy an always-on assistant that remembers, learns, reaches them where they already are, and completes real workflows. Soulacy already has much of the machinery. The next leap is to turn the machinery into a coherent, self-improving, multi-channel assistant experience.

OpenClaw competes on breadth, assistant feel, native companions, channels, voice, and "it just does things." Hermes competes on the learning loop: persistent memory, self-created skills, cross-session recall, and sub-agent execution. Soulacy can challenge both by becoming the most reliable local-first agent operating system: auditable, hackable, safe, extensible, and genuinely useful for scheduled and interactive work.

## What Is Implemented Today

### Runtime And Agent Model

Implemented:

- Single gateway binary with embedded GUI.
- `SOUL.yaml` per-agent configuration.
- Agent triggers: channel, cron, oneshot/internal-style peer execution.
- Agent hot reload through filesystem watcher.
- Per-agent LLM provider/model/temperature/max-token/tool-choice configuration.
- Agent-as-tool peer delegation through `agent__<id>`.
- Reasoning strategies, including ReAct and flow/workflow execution.
- Python tool execution and built-in system tools.
- Structured output enforcement paths.
- Run timeouts, max turns, and streaming flags.

Representative code/docs:

- `internal/runtime/engine.go`
- `internal/runtime/loader.go`
- `internal/runtime/watcher.go`
- `pkg/agent/types.go`
- `docs/agents/soul-yaml.md`
- `docs/FRAMEWORK_OVERVIEW.md`

### GUI

Implemented pages:

- Dashboard
- Agents
- Chat
- Studio
- Templates
- Brain Memory
- Knowledge
- Workboard
- Channels
- Schedule
- Skills
- MCP
- Plugins
- Providers
- Secrets
- Activity
- Config
- Logs

Notable implemented chat features:

- Chat sessions.
- Branch/fork support.
- Token/cost deltas.
- Message actions.
- Markdown rendering.
- Run metrics.
- Provider/model controls.
- Tool confirmation flow.

Representative files:

- `gui/src/pages/Chat.svelte`
- `gui/src/pages/Agents.svelte`
- `gui/src/pages/Studio.svelte`
- `gui/src/pages/Workboard.svelte`
- `gui/src/pages/Schedule.svelte`
- `gui/src/pages/Channels.svelte`
- `gui/src/lib/chatactions.js`
- `gui/src/lib/chatbranch.js`

### Studio

Implemented:

- Visual flow editor.
- Node palette and inspector.
- Trigger/output nodes.
- Tool/agent/python/branch style workflow concepts.
- Flow save/load.
- Build/refine/preflight/test-run infrastructure.
- Autowire, portization, template reference checking, output var inference.
- Composite blocks.
- Python node support and validation.
- Model-assisted build loop.
- Runtime troubleshooting loop: failed Activity sessions can be handed to Studio,
  diagnosed from real action-log evidence, repaired with the build/verify loop,
  and reviewed before saving the fixed agent.

Current product issue:

Studio has deep compiler/runtime machinery, but the user experience still asks users to reason in low-level graph terms. Entry/exit blocks and manual wiring are easy to misunderstand. The correct long-term direction is an intent-first workflow builder where the LLM proposes the plan, Studio renders it as a rich editable plan, and advanced users can manually add Python/tool/agent blocks.

Representative files:

- `internal/studio/*`
- `gui/src/pages/Studio.svelte`
- `gui/src/lib/studio/*`
- `docs/STUDIO_REDESIGN.md`
- `docs/STUDIO_PYTHON_TOOLS.md`

### LLM Providers

Implemented:

- Provider router.
- Built-in providers for OpenAI-compatible endpoints, Anthropic, Gemini, Ollama, Ollama Cloud style endpoints through OpenAI-compatible config.
- Provider model listing.
- Dynamic provider registration from GUI saves.
- Provider credential handling through config and encrypted vault.
- Retry/body replay fixes.
- Embedder abstraction for RAG.

Representative files:

- `internal/llm/router.go`
- `internal/llm/openai.go`
- `internal/llm/anthropic.go`
- `internal/llm/gemini.go`
- `internal/llm/ollama.go`
- `internal/gateway/api.go`
- `internal/secrets/*`

### Channels And Scheduled Output

Implemented:

- HTTP chat channel.
- Telegram.
- WhatsApp Web.
- Slack.
- Discord.
- Channel status and GUI configuration.
- Outbound-only Telegram mode for scheduled output.
- Scheduled output routing through `schedule.output`.
- Failure notifications through `notify_on_failure`.
- Channel binding safety for privileged agents.

Representative files:

- `internal/channels/*`
- `internal/app/wire_channels.go`
- `internal/scheduler/scheduler.go`
- `docs/channels/*`

### Scheduling

Implemented:

- Cron agents.
- One-shot style scheduling support in model/docs.
- Missed run behavior.
- Schedule page.
- Manual run/history/watch actions.
- Scheduled output delivery to configured channel adapter.

Representative files:

- `internal/scheduler/scheduler.go`
- `gui/src/pages/Schedule.svelte`
- `docs/using/schedules.md`

### Memory, Knowledge, And RAG

Implemented:

- Conversation history.
- Session forking.
- Memory store/archive layers.
- Agent memory and rule logs.
- Knowledge base UI/API.
- SQLite-backed document/chunk storage.
- Embedding registry.
- KB search tool.
- Brain memory UI concepts.

Representative files:

- `internal/memory/*`
- `internal/session/*`
- `internal/agentmemory/*`
- `internal/knowledge/*`
- `internal/llm/embed.go`
- `gui/src/pages/Memory.svelte`
- `gui/src/pages/Knowledge.svelte`

Gap:

Soulacy has memory infrastructure, but it does not yet feel like Hermes-style compounding intelligence. The missing product layer is a deliberate learning loop: detect solved tasks, extract reusable procedures/preferences, ask for confirmation when appropriate, create/update skills, and use those skills automatically next time.

### MCP, Skills, Plugins, Sidecars

Implemented:

- MCP client configuration and tool exposure.
- Skills loader using `SKILL.md` style.
- Skill source discovery and registry probing.
- Plugin manifest, plugin install gate, plugin capabilities.
- External Channel Protocol.
- External Storage Protocol.
- Sidecar supervision.
- Plugin credential delegation.
- Plugin GUI iframe mounting.
- Custom distribution build system.

Representative files/docs:

- `internal/mcp/*`
- `internal/skills/*`
- `internal/plugins/*`
- `internal/plugininstall/*`
- `internal/pkgregistry/*`
- `sdk/extchannel/*`
- `sdk/extstorage/*`
- `docs/PLUGIN_MANIFEST.md`
- `docs/PLUGIN_CAPABILITIES.md`
- `docs/EXTERNAL_CHANNEL_PROTOCOL.md`
- `docs/EXTERNAL_STORAGE_PROTOCOL.md`
- `docs/PACKAGE_REGISTRIES.md`

### Workboard And Observability

Implemented:

- Workboard task model.
- Kanban UI.
- Assignment to agents.
- Task runs/retries.
- Run metrics and costs.
- Action log and event stream.
- Event publishing and signed outbound webhook infrastructure.
- Costs API.
- Prometheus metrics.

Representative files:

- `internal/gateway/workboard.go`
- `internal/costs/*`
- `internal/actionlog/*`
- `internal/gateway/events.go`
- `gui/src/pages/Workboard.svelte`
- `gui/src/lib/RunMetrics.svelte`
- `docs/EVENTS.md`

### Security And Administration

Implemented:

- API key auth.
- RBAC structures/middleware.
- Encrypted credential vault.
- Global secrets API.
- Credential rotation/versioning support.
- Capability tiers for agent/channel exposure.
- Sandbox rlimits for subprocesses.
- Rate limiting.
- Config GUI.
- Doctor/onboard/daemon CLI work appears at least partially present.

Representative files:

- `internal/auth/*`
- `internal/rbac/*`
- `internal/credentials/*`
- `internal/secrets/*`
- `internal/tier/*`
- `internal/sandbox/*`
- `internal/ratelimit/*`
- `cmd/sy/*`

## Competitive Baseline

### OpenClaw

Public positioning: OpenClaw describes itself as a personal AI assistant that runs on your devices, answers on existing channels, can speak/listen on native platforms, renders a live Canvas, and supports many channels. Its GitHub README lists channels including WhatsApp, Telegram, Slack, Discord, Google Chat, Signal, iMessage, Teams, Matrix, LINE, Twitch, WeChat, WebChat, and more. It recommends `openclaw onboard --install-daemon` as the preferred setup path.

Competitive implication:

- OpenClaw feels like an always-on personal assistant first.
- Breadth of channels and native app story matters.
- Onboarding and daemonization are table stakes.
- Its ecosystem pitch is more vivid than Soulacy's runtime pitch.

### Hermes

Public positioning: Hermes describes itself as the agent that grows with you: persistent memory, automated skill creation, multi-platform reach, scheduled automations, parallel sub-agents, browser/web control, and multiple execution backends. The GitHub README emphasizes a built-in learning loop, skill creation from experience, self-improving skills, past-conversation search, and a deepening user model.

Competitive implication:

- Hermes owns the "learning loop" narrative.
- It turns memory into a product promise, not just infrastructure.
- Sub-agents, remote execution, and skill creation are core.
- Soulacy needs a first-class learning loop to compete directly.

## Where Soulacy Is Strongest

1. Local-first runtime architecture.
2. Declarative YAML agents that are easy to inspect, diff, and version.
3. Single-binary deployment model.
4. Deep gateway/API/GUI surface already implemented.
5. Strong plugin and sidecar architecture.
6. Capability/security model is more principled than simple contact pairing.
7. Scheduled agents and channel output are already practical.
8. Workboard + observability gives Soulacy operational depth.
9. Studio has serious compiler/runtime machinery underneath it.
10. Provider abstraction is flexible enough for most OpenAI-compatible ecosystems.

## Current Weaknesses

1. Product narrative is fragmented: runtime, Studio, Workboard, chat, schedules, plugins, memory all exist, but do not yet feel like one assistant.
2. Studio is too graph-centric for non-expert users.
3. Chat is improving but still behind ChatGPT/Claude polish: artifacts, files, search, project context, rich editing, citations, share/export, inline tool previews, and keyboard ergonomics need work.
4. Memory exists but does not yet compound into visible self-improvement.
5. Channel breadth is narrower than OpenClaw.
6. Native/mobile companion story is absent or not productized.
7. Voice exists as spike/MVP infrastructure but is not a differentiating user experience.
8. Browser/computer control is not yet a polished first-class capability.
9. Provider/key flows have had reliability issues; this hurts trust.
10. Documentation has many deep docs, but needs a crisp public product guide.

## Major Feature Additions To Challenge OpenClaw And Hermes

### 1. Soulacy Learning Loop

Goal: challenge Hermes directly.

Build a closed loop around every completed task:

- Detect task success/failure.
- Extract reusable facts, preferences, procedures, and tool recipes.
- Propose memory writes with provenance and confidence.
- Auto-create or update `SKILL.md` files after repeated successful workflows.
- Run periodic reflection jobs.
- Search past sessions and memories during planning.
- Show "what I learned" in the GUI.

MVP stories:

- Add `learning.enabled` per agent. *(implemented)*
- Add a post-run learning pipeline fed by action logs. *(implemented)*
- Add activity-triggered learning proposals from real runs. *(implemented)*
- Add memory proposals UI: accept/reject/edit. *(implemented)*
- Add skill proposal UI: generated `SKILL.md`, tests/checklist, install button. *(implemented)*
- Add "why I remembered this" provenance on memory entries.

### 2. Intent-First Studio

Goal: make Studio richer and more intuitive than drag/drop flow builders.

Replace "build a graph manually" with:

- User describes desired automation.
- Studio generates a plan with lanes: Trigger, Gather, Think, Act, Verify, Deliver.
- Graph is a visualization of the plan, not the primary authoring model.
- Users can add Python/tool/agent blocks manually when they know what they want.
- LLM suggests when Python is useful, but user can override.
- Entry/exit blocks become implicit anchors, not draggable concepts.

MVP stories:

- Plan-first builder view.
- Natural language "add a step" command.
- Block recommendation engine.
- Rich block inspector with examples, test input, mock output.
- Inline preflight errors with "fix with AI".
- Trace replay on the graph.

### 3. ChatGPT/Claude-Class Chat

Goal: make Soulacy pleasant enough to live in.

Add:

- Project/workspace context selector.
- File uploads and attachments with previews.
- Artifact side panel for generated files, tables, charts, code, reports.
- Inline tool call cards with expandable stdout/stderr.
- Conversation search.
- Message edit + regenerate variants.
- Branch tree.
- Saved prompts/templates.
- Citations from KB/web/tool outputs.
- Export/share session.
- Better stop/resume/cancel controls.
- Keyboard shortcuts and command palette.

MVP stories:

- Artifact panel tied to run/artifact events.
- File upload ingestion into session context or KB.
- Message edit/regenerate.
- Search across session history.
- Tool result cards with copy/open/download.

### 4. OpenClaw-Class Channels Without Building 25 Adapters

Goal: compete on reach without maintaining dozens of native integrations.

Build:

- Generic webhook inbound channel.
- Generic outgoing webhook/channel adapter.
- MCP server mode exposing Soulacy agents as tools.
- Official channel sidecar templates for Signal, Matrix, iMessage, Google Chat.
- Channel marketplace via plugin/sidecar registry.

MVP stories:

- `webhook` channel with request mapping templates.
- `channel.send` generic HTTP adapter.
- `soulacy mcp serve` exposing agents, Workboard, schedules, and KB.
- Sidecar starter kit for a new channel.

### 5. Native Companion And Mobile Pairing

Goal: challenge OpenClaw's companion-app narrative.

Do not start with full native mobile apps. Start with a responsive PWA and pairing:

- Installable mobile web app.
- QR pairing from desktop.
- Push notifications.
- Approve/deny tool calls from phone.
- View active runs and schedule history.
- Send chat messages to agents.

MVP stories:

- Pairing token/QR system.
- Mobile-first Chat/Activity/Approvals pages.
- Web push notifications.
- "Approve tool" notification action.

### 6. Browser And Computer Control

Goal: make agents actually do more real-world work.

Add a first-class browser automation sidecar:

- Playwright sidecar with screenshot/click/type/extract tools.
- Browser session viewer in GUI.
- Permission model for domains and actions.
- Recorded traces.
- Reusable browser skills.

MVP stories:

- `browser` sidecar plugin through MCP/Playwright quick-start. *(implemented)*
- GUI live screenshot + current URL.
- Per-domain approval policy.
- Trace replay and artifact capture.

### 7. Remote Execution Backends

Goal: challenge Hermes' "runs anywhere" story.

Add remote executors:

- Local.
- Docker.
- SSH.
- Modal/RunPod/Daytona-style cloud backends later.

MVP stories:

- Executor abstraction for tools and Studio Python nodes. *(implemented)*
- SSH executor. *(implemented; vault credential helper still future)*
- Docker executor. *(implemented; volume policy still future)*
- Per-agent execution backend selection.

### 8. Assistant Persona And Operating Model

Goal: convert runtime into assistant.

Add an opinionated "Primary Assistant" layer:

- Persona setup.
- Connected accounts checklist.
- Daily heartbeat.
- Proactive suggestions.
- User preferences.
- Personal/team memory.
- Default automation templates.

MVP stories:

- `sy onboard` / GUI onboarding for assistant identity.
- First-run "what should I help with?" wizard.
- Daily check-in agent template.
- Proactive opportunity detector over recent activity and schedules.

### 9. Reliability And Trust Layer

Goal: make Soulacy feel safer than OpenClaw/Hermes.

Add:

- Provider health checks.
- Secret consistency checks.
- Run replay.
- Rollback for agent changes.
- Dry-run mode for tools.
- Policy engine for high-risk actions.
- Better failure notifications.

MVP stories:

- Provider/secrets doctor checks in the GUI and API. *(implemented)*
- `sy doctor` v2 with provider/vault consistency checks.
- Agent version history and rollback. *(implemented)*
- Run replay from action log. *(implemented)*
- Activity-to-Studio run debugging with action-log evidence and verified repair. *(implemented)*
- Policy prompts for shell/file/network actions.

### 10. Template Gallery And Vertical Packs

Goal: make value obvious in 10 minutes.

Ship high-quality templates:

- Daily stock screener.
- Flight deal finder.
- Meeting minutes.
- Inbox triage.
- Competitor monitor.
- Document compliance auditor.
- Personal finance monitor.
- GitHub issue triage.
- Research brief generator.

MVP stories:

- Template gallery with "Install" and "Try".
- Required secrets checklist.
- Test run with mocks.
- Schedule/channel output setup wizard.

## Priority Roadmap

### Phase 1: Trust And Product Cohesion

1. Provider/secrets doctor checks. *(implemented)*
2. Chat artifact panel.
3. File upload/session attachments.
4. Template gallery polish.
5. Failure notifications and schedule output QA.

### Phase 2: Learning Loop

1. Post-run memory extraction. *(implemented)*
2. Memory proposal UI. *(implemented)*
3. Skill proposal generation. *(implemented)*
4. Past-session semantic search in planning.
5. Periodic reflection jobs.

### Phase 3: Studio Reframe

1. Intent-first Studio plan view.
2. LLM-generated plan lanes.
3. Manual Python/tool block insertion.
4. Rich testing/mocking/replay.
5. One-click deploy to agent/schedule/channel.

### Phase 4: Reach

1. Generic webhook channel.
2. Mobile PWA pairing and approvals.
3. MCP server mode.
4. Browser automation sidecar. *(MCP quick-start implemented)*
5. SSH/Docker execution backends. *(implemented)*

## Strategic Positioning

The best positioning is:

> Soulacy is the local-first agent operating system: declarative, inspectable, schedulable, multi-channel, self-improving, and safe enough to trust with real work.

Do not try to beat OpenClaw by adding 25 channels manually. Beat it with a cleaner extension model, generic channel adapters, and a safer local-first core.

Do not try to beat Hermes by only adding more memory tables. Beat it with a visible learning loop that turns completed tasks into memories, procedures, and skills the user can inspect and approve.

## External References Checked

- OpenClaw website and GitHub README: positioning around personal assistant, channels, native apps, onboarding, and broad channel support.
- Hermes website and GitHub README: positioning around persistent memory, automated skill creation, built-in learning loop, multi-platform reach, subagents, scheduled automations, and remote execution backends.
