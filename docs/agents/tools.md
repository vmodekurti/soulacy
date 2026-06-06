# Agent Tools

Soulacy agents can call tools during an LLM run. The runtime builds each
agent's tool catalog from five sources:

1. Python tools declared in that agent's `SOUL.yaml`.
2. Go-native built-ins such as `web_search`, `kb_search`, `read_skill`, and
   selected system tools.
3. MCP tools from configured MCP servers.
4. Peer agents exposed as `agent__<id>` tools.
5. Plugin-contributed tools, when plugins are enabled.

The model sees only the tools admitted by the agent definition and gateway
configuration.

## Python Tools

Declare agent-local tools under `tools:`:

```yaml
tools:
  - name: get_weather
    description: Fetch the current weather for a location.
    python_file: tools/get_weather.py
    timeout: 30s
    parameters:
      type: object
      properties:
        location: { type: string }
      required: [location]
```

The Python file should expose a function with the same name as the tool. The
runtime passes JSON arguments on stdin and captures stdout as the tool result.
Relative `python_file` paths resolve from the `SOUL.yaml` directory.

Use `timeout` for long-running tools. It uses Go duration syntax such as `30s`,
`5m`, or `1h`.

## Built-ins

`builtins` controls Go-native tools.

```yaml
builtins:
  - web_search
  - kb_search
```

Modes:

| YAML | Meaning |
|------|---------|
| Field absent | Default gated built-ins are offered. |
| `builtins: []` | No built-ins are offered. |
| `builtins: ["*"]` | All gated built-ins are allowed. |
| `builtins: [web_search]` | Only listed built-ins are allowed. |

Common built-ins:

| Tool | Gate | Description |
|------|------|-------------|
| `web_search` | Always visible unless filtered by `builtins` | Searches via the Ollama Web Search API. Requires an Ollama API key in config or `OLLAMA_API_KEY`. |
| `kb_search` | Agent must declare `knowledge:` | Searches configured knowledge bases. |
| `read_skill` | Agent must declare `skills:` | Reads the full body of an installed Agent Skill. |
| `read_skill_file` | Agent must declare `skills:` | Reads a resource file inside an installed skill. |

## System Tools

System tools provide host-level actions such as shell execution and file access.
They require a double opt-in:

1. Gateway config must allow them with `runtime.allow_system_tools: true`.
2. The agent must set `system_tools: true`.

```yaml
system_tools: true
confirm_tools:
  - shell_exec
  - write_file
```

Use `confirm_tools` for operations that should pause for operator approval.
`confirm_tools: ["*"]` requires confirmation for every built-in tool call.

System tools are powerful. On shared or exposed deployments, prefer leaving them
off and using narrower Python or MCP tools instead.

## MCP Tools

MCP tools are namespaced as `mcp__<server>__<tool>`.

```yaml
mcp_servers:
  - github
mcp_tools:
  - mcp__github__search_repositories
```

If both `mcp_servers` and `mcp_tools` are absent, legacy agents see all
connected MCP tools. Once either field is present, MCP becomes deny-by-default
and only matching servers or full tool names are exposed.

Use:

```yaml
mcp_servers: []
```

to expose no MCP tools, or:

```yaml
mcp_servers: ["*"]
mcp_tools: ["*"]
```

to expose every connected MCP tool explicitly.

## Peer-Agent Tools

Expose other agents with `agents:`:

```yaml
agents:
  - researcher
  - critic
```

The runtime registers those peers as `agent__researcher` and `agent__critic`.
When called, the target agent runs in a fresh internal session with its own
model, tools, memory, knowledge, and timeouts.

Use `llm.tool_choice` when delegation is mandatory:

```yaml
llm:
  provider: ollama
  model: qwen2.5:72b
  tool_choice: agent__researcher
```

Soulacy will auto-delegate before the first model turn for peer tools selected
this way, which helps with local models that ignore provider-level forced tool
choice.

## Knowledge Tools

Agents with at least one knowledge base get `kb_search` when built-ins permit it:

```yaml
knowledge:
  - finance-local
builtins:
  - kb_search
```

Knowledge bases are created and populated through the GUI or the
`/api/v1/knowledge` API. Each KB records its embedding provider and model so
different KBs can use different vector dimensions.

## Skill Tools

Agents with `skills:` get access to the skill catalog plus `read_skill` and
`read_skill_file`:

```yaml
skills:
  - csv-analysis
```

Only the skill name and description are injected initially. The model calls
`read_skill` when it needs the full instructions.

## Safety Checklist

- Prefer explicit `builtins`, `mcp_servers`, and `mcp_tools` allowlists for
  production agents.
- Use `builtins: []` for orchestrators that should only call peer agents.
- Keep `system_tools` off unless the agent truly needs host access.
- Add `confirm_tools` for destructive or high-cost actions.
- Run `sy agent validate path/to/SOUL.yaml` before deployment.
