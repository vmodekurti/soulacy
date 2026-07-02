# Browser Automation

Soulacy uses MCP as the browser automation sidecar path. The browser server runs
outside the gateway, exposes typed tools, and Soulacy injects them as
`mcp__browser__...` tools for agents and Studio workflows.

## Quick Setup

Open **MCP Servers** in the GUI, click **+ New Server**, then choose the
**Browser** quick-start template.

Equivalent `config.yaml`:

```yaml
mcp:
  servers:
    browser:
      transport: stdio
      command: npx
      args:
        - -y
        - "@playwright/mcp@latest"
        - --browser
        - chromium
```

Restart the gateway after saving MCP config. Once connected, the MCP page shows
the browser tools and their LLM-facing names.

## Agent Allowlist

For a narrow browser-enabled agent, explicitly allow the browser server:

```yaml
mcp_servers: [browser]
```

Or allow individual browser tools after you inspect the connected tool names:

```yaml
mcp_tools:
  - mcp__browser__browser_navigate
  - mcp__browser__browser_click
  - mcp__browser__browser_snapshot
```

Avoid wildcard MCP access for public or shared agents.

## Safety Notes

Browser automation can click, type, and submit forms. Treat it as an active tool
surface. Prefer dedicated browser profiles/accounts, domain-specific agents, and
human approval for workflows that spend money, send messages, or mutate records.
