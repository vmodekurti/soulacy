# Security Posture

Soulacy is designed first as a self-hosted, single-operator or same-trust-team
agent runtime. It can run powerful tools on the host, so treat any user who can
edit agent definitions, configure channels, or message a tool-enabled shared
agent as inside that trust boundary.

## Supported Trust Model

Recommended deployments:

- one operator on a laptop or workstation,
- one household or team that shares a trust boundary,
- one dedicated VPS or host for one organization or project.

Not recommended without additional isolation:

- hostile multi-tenant hosting,
- mutually untrusted users sharing one gateway,
- public bots with broad tool access,
- shared agents connected to personal credentials or sensitive host files.

For adversarial or mixed-trust use, run separate Soulacy gateways under separate
OS users, containers, VMs, or hosts, and use separate credentials.

## Gateway Authentication

Set `server.api_key` before binding to a non-loopback address.

```yaml
server:
  host: "127.0.0.1"
  port: 18789
  api_key: "sy_replace_with_a_long_random_secret"
```

When `server.api_key` is empty and the server binds to a non-loopback host,
Soulacy logs a startup warning because API endpoints are unauthenticated.

## Tool Execution Boundary

Python tools run as subprocesses. On Unix-like hosts, Soulacy can wrap those
subprocesses with the hidden `soulacy __exec-sandbox` runner, which applies
resource limits for CPU, memory, open files, and single-file output size.

This resource sandbox reduces runaway-tool blast radius. It is not a complete
filesystem, network, user, or kernel isolation boundary. A Python tool still
runs with the gateway process's OS user permissions unless you add external
isolation such as containers, VMs, or a dedicated OS account.

Recommended hardening:

```yaml
runtime:
  allow_system_tools: false
  allowed_tool_dirs:
    - "/opt/soulacy/tools"
  sandbox:
    enabled: true
    cpu_seconds: 30
    memory_mb: 512
    open_files: 256
    file_size_mb: 64
```

Use `allowed_tool_dirs` to prevent `python_file` entries from pointing at
unexpected host paths.

## System Tools

System tools such as shell execution and host file access require a double
opt-in:

1. `runtime.allow_system_tools: true` in `config.yaml`.
2. `system_tools: true` in the agent's `SOUL.yaml`.

Keep system tools disabled for agents reachable by shared or public channels.
When system tools are necessary, add confirmations:

```yaml
system_tools: true
confirm_tools:
  - shell_exec
  - write_file
```

## MCP and Built-in Allowlists

Prefer explicit allowlists for production agents:

```yaml
builtins:
  - web_search
  - kb_search
mcp_servers:
  - github
mcp_tools:
  - mcp__github__search_repositories
```

Use `builtins: []` or `mcp_servers: []` for agents that should not receive those
tools.

## Channel Exposure

Channel messages are untrusted input. For shared channels:

- restrict allowed users or groups where adapters support it,
- avoid broad MCP and system-tool access,
- use dedicated credentials for the bot,
- keep personal accounts and private files out of that runtime.

WhatsApp webhook requests are authenticated by Meta's signature verification.
They are intentionally not gated by `server.api_key`, because Meta cannot send
that header.

## Practical Baseline

For a conservative production baseline:

```yaml
server:
  host: "127.0.0.1"
  api_key: "sy_replace_with_a_long_random_secret"

runtime:
  allow_system_tools: false
  allowed_tool_dirs:
    - "/opt/soulacy/tools"
  sandbox:
    enabled: true
```

Then enable only the providers, channels, built-ins, MCP servers, and agent
tools needed for each deployment.
