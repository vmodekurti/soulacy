# GUI Tour

The built-in web GUI gives you a full control panel for every part of Soulacy — agents, chat, schedules, memory, knowledge, and operations — without touching a single YAML file.

Open it at:

```
http://localhost:18789
```

If pages show **🔒 Authentication required**, click the **🔑** button in the sidebar footer and paste the `server.api_key` from `~/.soulacy/config.yaml`.

The sidebar is split into three groups: **main** (day-to-day work), **ops** (integrations), and **system** (observability).

## Dashboard

Your at-a-glance health view: gateway status and version, agent counts, and a **Live Event Log** streaming every runtime event over WebSocket, with filter presets (All / Errors / Tools / LLM / Messages).

**Try first:** send a chat message in another tab and watch the events appear live.

## Build

A conversational agent builder — describe what you want in plain language, answer its follow-up questions, and it generates a complete SOUL.yaml you can preview and deploy in one click.

**Try first:** type "an agent that summarizes RSS articles every morning" and see the blueprint form fill itself in.

## Flow

A visual canvas for each agent: trigger, system prompt, memory, LLM, tools, and output rendered as a connected graph. Click any node to open an editable inspector and hit **Save Flow**. Agents defined with `workflow.nodes` render as a read-only graph instead — see [Flow view](../using/flow-view.md).

**Try first:** pick an agent in the left rail and click its LLM node.

## Agents

The full agent editor: identity, trigger, system prompt, LLM provider/model (with live model lists), memory scopes, reasoning strategy, brain memory layers, channels, tools, skills, knowledge bases, and peer agents.

- Tool **Python file** fields have a **📂 Browse** picker that lists every script in the tool catalog — pick instead of typing paths.
- Chip-picker fields (channels, skills, knowledge bases, peer agents, built-ins) have a **▾ browse** dropdown populated from what actually exists, so typos are impossible.
- **Validate** runs the same checks as `sy agent validate` and shows findings inline.
- **💬 Test** opens an inline playground; **&lt;/&gt; API** shows copy-paste cURL/Python/JS snippets for the agent.

**Try first:** select an agent, click **Validate**, then **💬 Test** and send it a message.

## Templates

A catalog of ready-made agents: four agentic workflows (Meeting Minutes & Action Items, Smart Inbox Triage, Competitor & Market Monitor, Document Compliance Auditor) plus starters. One click on **⊕ Create agent** instantiates a working agent. See [Templates](../using/templates.md).

**Try first:** create **Meeting Minutes & Action Items** and paste a transcript into Chat.

## Chat

The Chat Tester: pick an agent, send messages, and watch the **Thinking** panel narrate every LLM call, tool call, and reasoning step live. Each reply carries a per-turn token/cost delta. Hover any bubble to reveal **⑂ Fork** and branch the conversation from that point; the **🎤** button starts a realtime voice session. See [Chat](../using/chat.md) and [Voice](../using/voice.md).

**Try first:** ask anything, then expand **Thinking** under the reply.

## Brain Mem

The Brain Memory explorer for long-term agent memory: an **Episodic** timeline (search, write, clear), a **Procedural** rulebook editor with markdown preview — including version **History**, line-level **Diff vs current**, one-click **Roll back**, and a **🔒 Lock** toggle that freezes the rules — and a **Context Preview** tab showing exactly what gets injected into the system prompt. See [Memory](../using/memory.md).

**Try first:** open the Procedural tab and click **⧗ History**.

## Knowledge

Create and manage RAG knowledge bases: upload `.md`/`.txt`/`.pdf`/`.docx` files or paste text, inspect chunk counts, and run a **Test search** against any KB before wiring it to an agent. See [Knowledge bases](../using/knowledge.md).

**Try first:** create a KB, drop in one PDF, and test-search a phrase from it.

## Workboard

A kanban board (Todo → Running → Needs Review → Done → Failed) where each task can be assigned to an agent and run with **▶**. Tasks carry owner, priority, tags, and due dates; the editor shows run history, downloadable artifacts, and comments/review notes. See [Workboard](../using/workboard.md).

**Try first:** create a task, assign an agent, click **▶ Run**.

## Channels (ops)

Status and configuration for every channel adapter — Telegram, Discord, Slack, WhatsApp, WhatsApp Web (with QR pairing), and the always-on HTTP channel. Edit credentials, map bots to agents (multi-bot supported), and enable/disable adapters; a restart banner appears when changes need a gateway restart.

**Try first:** open a channel's **Edit** dialog to see which agent each bot routes to.

## Schedule (ops)

All cron agents in one table: next run, last run, missed-run policy, live "Running…" status, and countdowns. **▶ Run** triggers immediately, **📋 History** opens a slide-out of past runs with outputs, **👁 Watch** jumps to the live action log. See [Schedules](../using/schedules.md).

**Try first:** click **📋 History** on any cron agent.

## Skills (ops)

Browse installed Agent Skills (SKILL.md instruction packs), read their full instructions and resources, and install new ones — **⚡ From AgenticSkills** installs directly from an agenticskills.io URL, and **➕ Skill sources** lets you paste any URL (a skills.sh-style directory, a Soulacy package registry, or a Git host), have it reviewed and identified, then add it as a registry source.

**Try first:** click **➕ Skill sources**, paste `https://www.skills.sh/`, and hit **🔍 Review**.

## MCP (ops)

Manage MCP servers: **+ New Server** for manual stdio/HTTP specs (command, args, env, headers) with a connection test, or the **Glama** provisioner to import a server spec from a Glama URL.

**Try first:** expand an existing server row to see its tools and status.

## Plugins (ops)

Install plugins from a git URL, a sha256-checksummed archive, or a local directory. Every install is staged first — you review the requested permissions, credentials, and security findings before approving.

**Try first:** paste a plugin source and click **Clone & review** (nothing activates until you approve).

## Providers (ops)

LLM provider credentials and defaults: add keys for OpenAI, Anthropic, Google, Groq, Mistral, OpenRouter, or point at local Ollama; pick default models from live model lists; run connection tests; tune provider-specific options (Ollama context window, Anthropic extended thinking, and more).

**Try first:** click a provider's test button to verify it answers.

## Activity (system)

The per-agent action log from the SQLite action store: every run, LLM call, tool call/result, reasoning step, reply, and error with timestamps and summaries. Filter by type, enable **▶ Watch (2s)** for live polling, and see per-run token/cost totals. See [Dashboard & Activity](../using/dashboard.md).

**Try first:** select your busiest agent and click **▶ Watch (2s)**.

## Config (system)

Edit the live runtime configuration: log level/format, Python interpreter, tool timeout, max turns, max concurrent sessions, default LLM provider, agent/skill directories, and per-plugin settings. After saving, a banner offers a one-click **Restart Gateway**.

**Try first:** check that `python_bin` points at the interpreter where your tool dependencies are installed.

## Logs (system)

The gateway log tail: last 100–5000 lines, free-text filter, severity filter (error/warn/info/debug) with per-level counts, line wrap toggle, and 3-second auto-refresh.

**Try first:** filter on `error` after anything misbehaves — it is usually the fastest diagnosis.

!!! tip
    Everything in the GUI is also scriptable: the CLI (`sy`) and REST API (`/api/v1/...`) drive the same endpoints the pages use. Run `sy doctor` if any page shows unexpected errors.
