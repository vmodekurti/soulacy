# SOUL.yaml Reference

Every agent is defined by a `*.soul.yaml` file in your agents directory. This page documents every field.

## Complete schema

```yaml
# ── Identity ──────────────────────────────────────────────────
name: my-agent                # required; used as the agent ID in URLs and logs
description: "What this agent does"
version: "1.0.0"              # optional; for marketplace/registry

# ── Model ─────────────────────────────────────────────────────
model: gpt-4o-mini            # required
# Formats:
#   gpt-4o-mini                 → uses default_provider
#   anthropic/claude-3-5-haiku → explicit provider prefix
#   ollama/llama3.2            → local Ollama model

# ── Prompt ────────────────────────────────────────────────────
system_prompt: |
  You are a helpful assistant.

# ── Tools ─────────────────────────────────────────────────────
tools:
  - web_search
  - url_fetch
  - calculator

# Restrict external MCP tools. If both fields are absent, legacy behavior
# allows every connected MCP server. If either field is present, MCP is
# deny-by-default and only matching entries are allowed.
mcp_servers:
  - rocketmoney
mcp_tools:
  - mcp__rocketmoney__get_transactions

# ── Token budget ──────────────────────────────────────────────
token_budget:
  max_input_tokens: 32000     # max context sent to LLM
  max_output_tokens: 1024     # max tokens in response

# ── Session / history ─────────────────────────────────────────
session:
  history_turns: 20           # prior turns to include in context

# ── Channels ──────────────────────────────────────────────────
channels:
  - http
  - telegram
  - slack
  - discord
  - whatsapp

# ── Workflow (optional) ───────────────────────────────────────
workflow:
  steps:
    - id: gather
      agent: researcher
      input:
        query: "{{input.message}}"
    - id: write
      agent: writer
      input:
        research: "{{steps.gather.output}}"
        topic: "{{input.message}}"
      depends_on: [gather]

# ── Capabilities ──────────────────────────────────────────────
capabilities:
  - web_search
  - code_execution

# ── Metadata ──────────────────────────────────────────────────
tags:
  - productivity
  - research
author: "Your Name"
```

---

## Field reference

### `name` (required)

Unique identifier for the agent. Used in API paths (`/v1/agents/{name}/chat`) and log output. Must match `[a-z0-9-]+`.

### `model` (required)

LLM model to use. Use a bare name for the default provider, or prefix with `provider/` to select a specific one.

```yaml
model: gpt-4o-mini                              # openai (default)
model: anthropic/claude-3-5-haiku-20241022      # anthropic
model: ollama/llama3.2                          # ollama (local)
model: together/meta-llama/Meta-Llama-3.1-70B  # together
model: qwen/qwen-max                            # qwen
```

### `system_prompt`

The agent's base instructions. Supports multi-line YAML block syntax (`|`). The system prompt is prepended to every conversation context.

### `tools`

List of built-in tools the agent can invoke. See [Built-in Tools](tools.md) for the full list.

### `mcp_servers` / `mcp_tools`

Restricts tools from configured MCP servers. If both fields are absent, the agent sees every connected MCP tool for backwards compatibility. Once either field is present, MCP is deny-by-default.

```yaml
mcp_servers:
  - rocketmoney

mcp_tools:
  - mcp__filesystem__read_file
```

Use `mcp_servers: []` to explicitly expose no MCP tools. Use `["*"]` or `["all"]` to explicitly expose all MCP tools.

### `token_budget`

| Field | Default | Description |
|-------|---------|-------------|
| `max_input_tokens` | provider max | Maximum tokens in the context window sent to the LLM |
| `max_output_tokens` | provider max | Maximum tokens the LLM may generate |

### `session.history_turns`

Number of prior conversation turns to include in context. Default: `20`. Set to `0` to disable memory (stateless agent).

### `channels`

Which messaging channels this agent is accessible on. Options: `http`, `telegram`, `slack`, `discord`, `whatsapp`.

### `workflow`

Defines a multi-step DAG pipeline. See [Workflow DAGs](workflow.md).

### `capabilities`

Declares what this agent can do — used by the capability gap detector to surface missing configuration at startup.

### `tags` / `author`

Metadata for the agent marketplace. Optional.
