# Soulacy

**One binary. YAML agents. Runs anywhere — no cloud required.**

Soulacy is a self-hosted AI agent runtime. Write an agent in a single YAML file, point it at any LLM (Ollama, OpenAI, Anthropic, Gemini, or anything OpenAI-compatible), and run it from a laptop, a $5 VPS, or a Raspberry Pi — with a full web GUI, chat, voice, scheduling, memory, skills, and plugins built into the one binary.

Think of it as Ollama — but for agents.

```bash
# install, set up, talk to your first agent — under five minutes
curl -fsSL https://vmodekurti.github.io/soulacy/install.sh | bash
sy setup
sy chat --agent assistant "What can you do?"
```

[Get started :material-rocket-launch:](getting-started/quickstart.md){ .md-button .md-button--primary }
[Tour the GUI :material-monitor:](getting-started/gui-tour.md){ .md-button }

---

## What you can build

<div class="grid cards" markdown>

-   :material-file-document-edit: **Agents from one YAML file**

    ---

    Identity, LLM, tools, memory, schedule — one `SOUL.yaml` per agent. Edit in the GUI or your editor; changes hot-reload.

    [:octicons-arrow-right-24: SOUL.yaml reference](agents/soul-yaml.md)

-   :material-chat-processing: **Chat with branching & voice**

    ---

    Fork a conversation from any message, watch reasoning steps live, see per-reply token costs, or hold a realtime voice conversation.

    [:octicons-arrow-right-24: Chat](using/chat.md) · [Voice](using/voice.md)

-   :material-message-flash: **Every channel**

    ---

    Telegram, Slack, Discord, WhatsApp, HTTP out of the box — or any platform via a sidecar process in the language of your choice.

    [:octicons-arrow-right-24: Channels](channels/index.md)

-   :material-puzzle: **Skills & plugins, safely**

    ---

    Install skills from skills.sh, GitHub, or your own registry. Every install runs a security pipeline; plugins are sandboxed, default-deny principals.

    [:octicons-arrow-right-24: Installing skills](extend/installing-skills.md) · [Skill sources](extend/skill-sources.md)

-   :material-brain: **Memory that learns**

    ---

    Session/agent/global memory scopes, semantic vector search, and versioned procedural rulebooks the agent can update — with locks, diffs, and rollback.

    [:octicons-arrow-right-24: Memory & rulebooks](using/memory.md)

-   :material-graph: **Workflows & flow graphs**

    ---

    Linear steps or cyclic graphs with conditional edges and bounded loops — checkpointed, crash-resumable, rendered live on the Flow page.

    [:octicons-arrow-right-24: Flow graphs](agents/flows.md)

-   :material-calendar-clock: **Scheduling that catches up**

    ---

    Cron agents with missed-run catch-up after downtime, a workboard with tasks, comments, and downloadable run artifacts.

    [:octicons-arrow-right-24: Schedules](using/schedules.md) · [Workboard](using/workboard.md)

-   :material-shield-check: **Observable & governable**

    ---

    Every run emits schema-versioned events: live activity feed, signed webhooks, costs per agent, rate limits, RBAC, audit logs.

    [:octicons-arrow-right-24: Events & webhooks](configuration/events.md)

</div>

## Five-minute tour

1. **Install** — one line on [macOS](deployment/macos.md), [Linux](deployment/linux.md), or [Docker](deployment/docker.md), then `sy setup` walks you through providers and channels. → [Installation](getting-started/installation.md)
2. **Meet the GUI** — everything lives at `http://localhost:18789`: Dashboard, Agents, Chat, Workboard, Knowledge, Memory, Skills, Flow, Plugins. → [GUI tour](getting-started/gui-tour.md)
3. **Write an agent** — a complete `SOUL.yaml` walkthrough: prompt, tools, memory, schedule. → [Your first agent](getting-started/first-agent.md)
4. **Give it skills** — `sy registry add https://www.skills.sh/` then `sy skill install anthropics/skills/skill-creator`. → [Skill sources](extend/skill-sources.md)
5. **Put it to work** — bind a Telegram bot, schedule a daily run, or start from a shipped [workflow template](using/templates.md).

## Why Soulacy

| | Soulacy | n8n / Flowise / Dify | LangGraph / AutoGen |
|---|---|---|---|
| **Deploy** | Single binary, zero deps | Docker + Postgres + Redis | Python package |
| **Config** | One YAML file per agent | Visual editor (brittle exports) | Code |
| **Runs on** | Laptop, VPS, Raspberry Pi | Needs a server stack | Dev machine |
| **LLM** | Any — local or cloud | Mostly cloud | Any |
| **No-code** | GUI included in binary | Yes | No |
| **Extensible** | Skills, plugins, sidecars in any language | JS nodes | Python |

## Where to next

- **Users**: [Quick Start](getting-started/quickstart.md) → [GUI Tour](getting-started/gui-tour.md) → [Using Soulacy](using/chat.md)
- **Agent authors**: [SOUL.yaml Reference](agents/soul-yaml.md) → [Tools](agents/tools.md) → [Reasoning](agents/reasoning.md) → [Flow Graphs](agents/flows.md)
- **Operators**: [Configuration](configuration/index.md) → [Events & Webhooks](configuration/events.md) → [Upgrades](deployment/upgrades.md)
- **Extenders**: [Plugins](extend/plugins.md) → [Custom Channels](channels/sidecars.md) → [Custom Distributions](extend/custom-distributions.md) → [Specs](architecture/specs.md)
