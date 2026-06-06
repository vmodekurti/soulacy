# SOUL.yaml Reference

Every Soulacy agent is defined by one `SOUL.yaml` file under an agent directory,
normally `<agent_dir>/<agent_id>/SOUL.yaml`. The schema below reflects
`pkg/agent/types.go`, which is the source of truth used by the loader, API, GUI,
and validator.

## Minimal Agent

```yaml
id: assistant
name: Assistant
description: General-purpose helper

trigger: channel
channels:
  - http

llm:
  provider: openai
  model: gpt-4o-mini
  temperature: 0.2

system_prompt: |
  You are concise, helpful, and careful.

max_turns: 6
enabled: true
```

`id` is the stable runtime identifier. It is used in URLs, channel mappings,
logs, peer-agent tool names, and the agent directory name. `name` is display
text.

## Complete Example

```yaml
id: research-writer
name: Research Writer
description: Researches current topics, searches local knowledge, and writes a brief.
version: "1.0.0"
tags: [research, writing]
labels:
  owner: ops

trigger: channel
channels: [http, telegram]

llm:
  provider: anthropic
  model: claude-sonnet-4-6
  temperature: 0.2
  max_tokens: 2048
  allowed_providers: [anthropic]
  tool_choice: auto
  output_schema:
    type: object
    properties:
      summary: { type: string }
      sources:
        type: array
        items: { type: string }
    required: [summary, sources]

system_prompt: |
  Produce a short brief. Cite source filenames or URLs when available.

tools:
  - name: summarize_csv
    description: Summarize a CSV file and return compact JSON.
    python_file: tools/summarize_csv.py
    timeout: 2m
    parameters:
      type: object
      properties:
        path: { type: string }
      required: [path]

builtins:
  - web_search
  - kb_search

knowledge:
  - company-docs

skills:
  - csv-analysis

mcp_servers:
  - github
mcp_tools:
  - mcp__github__search_repositories

agents:
  - critic

system_tools: false
confirm_tools:
  - write_file
  - shell_exec

memory:
  read_scopes: [agent, session]
  write_scopes: [agent]
  max_tokens: 2000

reasoning:
  strategy: react
  max_steps: 6
  step_timeout: 30s
  total_timeout: 3m

brain_memory:
  episodic:
    enabled: true
    max_inject: 5
  semantic:
    enabled: true
    max_inject: 5
  procedural:
    enabled: true
    auto_update: false

schedule:
  cron: "0 8 * * *"
  run_missed_on_startup: true
  missed_startup_window: 24h
  output:
    channel: telegram
    to: "123456789"
    bot_name: "Daily Bot"
    template: "{reply}"

notify_on_failure:
  channel: telegram
  to: "123456789"
  include_error: true

security:
  passphrase: "change-me"
  passphrase_prompt: "Please provide the access passphrase."

max_turns: 10
stream_reply: true
enabled: true
run_timeout: 10m
```

## Identity

| Field | Type | Notes |
|-------|------|-------|
| `id` | string | Required stable runtime ID. Avoid whitespace and path separators. |
| `name` | string | Human-readable display name. |
| `description` | string | Also shown to other agents when this agent is exposed as a peer tool. |
| `version` | string | Optional package/marketplace metadata. |
| `tags` | string list | Optional filtering metadata. |
| `labels` | map | Optional operator metadata. |

## Trigger and Channels

`trigger` controls how the agent starts:

| Trigger | Meaning |
|---------|---------|
| `channel` | Inbound channel traffic such as HTTP, Telegram, Slack, Discord, or WhatsApp. |
| `cron` | Run on a cron schedule. Requires `schedule.cron`. |
| `oneshot` | Run once at `schedule.at`. |
| `webhook` | Reserved for webhook-triggered definitions. |
| `internal` | Callable by peer agents, not normally user-facing. |

`channels` declares the channel IDs this agent is intended to serve. The actual
platform credentials and inbound routing live in `config.yaml`.

## Schedule

Cron agents can optionally catch up after host downtime. When
`schedule.run_missed_on_startup` is `true`, Soulacy records completed cron runs
in the scheduler state file and, on startup, runs the latest missed fire if it
falls within `schedule.missed_startup_window` (default `24h`). Only one missed
fire is replayed per startup, so a long outage does not flood downstream
channels.

```yaml
trigger: cron
schedule:
  cron: "0 8 * * *"
  run_missed_on_startup: true
  missed_startup_window: 24h
```

## LLM

`llm.provider` and `llm.model` select the model. Empty `provider` falls back to
`llm.default_provider` in `config.yaml`.

Useful fields:

| Field | Notes |
|-------|-------|
| `temperature` | Sampling temperature. |
| `max_tokens` | Output token cap. |
| `base_url` | Per-agent override for OpenAI-compatible endpoints. |
| `allowed_providers` | Optional guard; if set, the engine refuses to run on any provider outside this list. |
| `tool_choice` | First-turn tool policy: `auto`, `none`, `required`, or a specific tool name such as `agent__researcher`. |
| `output_schema` | JSON Schema enforced on the final reply with one repair attempt. |

## Tools

Agent-local tools are declared as objects with JSON Schema parameters and either
`python_file` or `inline` implementation.

```yaml
tools:
  - name: get_weather
    description: Fetch weather for a city.
    python_file: tools/get_weather.py
    parameters:
      type: object
      properties:
        location: { type: string }
      required: [location]
```

`timeout` uses Go duration syntax such as `30s`, `2m`, or `1h`. Relative
`python_file` paths resolve from the `SOUL.yaml` directory.

## Built-ins

`builtins` controls Go-native tools offered by the engine.

| Value | Meaning |
|-------|---------|
| Field absent | Default gating. Tools appear when their prerequisites are met. |
| `builtins: []` | No built-ins. Useful for pure peer-agent orchestrators. |
| `builtins: ["*"]` or `["all"]` | Explicitly allow all gated built-ins. |
| `builtins: [web_search, kb_search]` | Allow only the listed built-ins, still subject to their gates. |

Common built-ins include:

- `web_search` for Ollama Web Search API.
- `kb_search` when `knowledge:` is non-empty.
- `read_skill` and `read_skill_file` when `skills:` is non-empty.
- System tools such as `shell_exec`, `read_file`, and `write_file` only when
  both gateway config and the agent opt in.

## MCP Tools

Connected MCP tools are namespaced as `mcp__<server>__<tool>`.

If `mcp_servers` and `mcp_tools` are both absent, legacy behavior exposes all
connected MCP tools. Once either field is present, MCP is deny-by-default and a
tool must match one of the allowlists.

```yaml
mcp_servers:
  - rocketmoney
mcp_tools:
  - mcp__filesystem__read_file
```

Use an empty list to expose none, or `["*"]` / `["all"]` to expose all.

## Skills, Knowledge, and Peer Agents

`skills` exposes Agent Skills by name. `["*"]` exposes all installed skills.

`knowledge` exposes named knowledge bases through `kb_search`.

`agents` exposes peer agents as callable tools named `agent__<id>`. Self-calls
are dropped, and recursive peer calls are depth-limited.

## System Tools and Confirmation

`system_tools: true` opts the agent into OS-level built-ins, but those tools are
only available when `runtime.allow_system_tools: true` is also set in
`config.yaml`.

Use `confirm_tools` to require operator approval before sensitive built-ins run:

```yaml
system_tools: true
confirm_tools:
  - shell_exec
  - write_file
```

`confirm_tools: ["*"]` requires confirmation for every built-in tool call.

## Memory and Reasoning

`memory` controls classic session/cross-session memory injection:

```yaml
memory:
  read_scopes: [agent, session]
  write_scopes: [agent]
  max_tokens: 2000
```

`reasoning` enables the newer multi-step loop. Supported strategies are
`react`, `plan_execute`, and `auto`.

`brain_memory` controls the episodic, semantic, and procedural memory layers
when a composite brain-memory store is wired into the gateway.

## Schedule and Notifications

Cron and one-shot agents use `schedule`. By default scheduled replies are only
logged. Add `schedule.output` to publish successful results through a channel:

```yaml
trigger: cron
schedule:
  cron: "0 8 * * *"
  output:
    channel: telegram-financial-agent
    to: "123456789"
    bot_name: "Finance Bot"
    template: "{reply}"
```

Use `notify_on_failure` for errors, especially for cron agents:

```yaml
notify_on_failure:
  channel: telegram
  to: "123456789"
  include_error: true
```

## Workflow

`workflow` defines a checkpointed sequence of tool steps. When present, the
runtime delegates to the workflow executor instead of the free-form agent loop.

```yaml
workflow:
  steps:
    - id: gather
      tool: web_search
      input: '{"query":"{{.trigger}}"}'
      output: search
      on_error: retry
    - id: write
      tool: summarize_csv
      input: '{"text":{{.search}}}'
      output: brief
```

Each step supports `id`, `tool`, `prompt`, `if`, `on_error`, `input`, and
`output`.

## Validation

Validate before deploying:

```bash
sy agent validate path/to/SOUL.yaml
sy agent validate path/to/SOUL.yaml --json
```
