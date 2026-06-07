# Quick Start

A working agent, the web GUI, and your first chat — in under five minutes.

## 1. Install

=== "macOS / Linux"

    ```bash
    curl -fsSL https://vmodekurti.github.io/soulacy/install.sh | bash
    ```

=== "From source"

    ```bash
    git clone https://github.com/vmodekurti/soulacy && cd soulacy
    make all          # builds the GUI + the soulacy and sy binaries into ./bin
    ```

More options (Docker, VPS, launchd service): [Installation](installation.md).

## 2. Run the setup wizard

```bash
sy setup
```

The wizard creates your workspace (`~/.soulacy/soulspace` — the [soulspace layout](../configuration/workspace.md)), asks which LLM provider to use, and can wire a channel.

!!! tip "No API key? Use Ollama."
    If you run [Ollama](https://ollama.com) locally, pick it in the wizard and Soulacy works fully offline — no cloud account needed.

## 3. Start the gateway

```bash
soulacy
```

The gateway starts on **http://localhost:18789** with the full web GUI: Dashboard, Agents, Chat, Workboard, Knowledge, Memory, Skills, Flow, Plugins, and more. Take the [GUI tour](gui-tour.md).

## 4. Talk to an agent

=== "GUI"

    Open **http://localhost:18789** → **Chat** → pick your agent → say hello.
    Watch the *Thinking* section show reasoning steps and tool calls live.

=== "CLI"

    ```bash
    sy chat --agent assistant "Summarize the latest AI news in 3 bullets"
    ```

=== "HTTP"

    ```bash
    curl -X POST http://localhost:18789/api/v1/agents/assistant/chat \
      -H "Authorization: Bearer $SOULACY_SERVER_API_KEY" \
      -H "Content-Type: application/json" \
      -d '{"message": "Hello!"}'
    ```

## 5. Make it yours

An agent is one YAML file. Open **Agents** in the GUI (or edit `~/.soulacy/soulspace/agents/assistant/SOUL.yaml`):

```yaml title="SOUL.yaml"
id: assistant
name: Assistant
trigger: channel
llm:
  provider: ollama        # or openai / anthropic / google / any OpenAI-compatible
  model: llama3
system_prompt: |
  You are a helpful, concise assistant.
channels:
  - http
```

Changes hot-reload — no restart. The full schema (tools, memory, reasoning, schedules, workflows) is in the [SOUL.yaml reference](../agents/soul-yaml.md).

## 6. Five things to try next

1. **Give it skills** — add the public skill directory and install one:
   ```bash
   sy registry add https://www.skills.sh/
   sy skill install anthropics/skills/skill-creator
   ```
   Every install passes a [security review](../extend/safety.md) before you consent. → [Skill sources](../extend/skill-sources.md)
2. **Put it on Telegram** — a bot token and two YAML lines. → [Channels](../channels/telegram.md)
3. **Schedule it** — run every morning, with automatic catch-up after downtime. → [Schedules](../using/schedules.md)
4. **Start from a template** — Meeting Minutes, Inbox Triage, Market Monitor, Compliance Auditor, ready in one click. → [Templates](../using/templates.md)
5. **Talk to it** — configure `voice:` and hold a realtime conversation in Chat. → [Voice](../using/voice.md)

## Where everything lives

| | |
|---|---|
| Workspace (agents, skills, data) | `~/.soulacy/soulspace/` — [layout](../configuration/workspace.md) |
| Config file | `config.yaml` in the workspace — [reference](../configuration/index.md) |
| GUI | `http://localhost:18789` — [tour](gui-tour.md) |
| CLI | `sy` — [reference](../cli/reference.md) |
