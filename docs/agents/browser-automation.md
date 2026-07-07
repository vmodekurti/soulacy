# Browser Automation

Soulacy uses MCP as the browser automation sidecar path. The browser server runs
outside the gateway, exposes typed tools, and Soulacy injects them as
`mcp__browser__...` tools for agents and Studio workflows.

## Quick Setup

Open **MCP Servers** in the GUI, click **+ New Server**, then choose the
**Browser headless** quick-start template. This is the production default:
agents can browse in the background without opening Chrome windows on the
desktop.

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
        - --headless
```

Restart the gateway after saving MCP config. Once connected, the MCP page shows
the browser tools and their LLM-facing names.

For debugging, choose the **Browser visible** quick-start instead. It uses the
same server but omits `--headless`, so Chromium opens where you can watch it.

Soulacy also runs a lightweight process janitor around MCP tool calls on macOS
and Linux. If a browser MCP tool leaves short-lived child browser processes
behind after a call returns, Soulacy terminates those descendants so scheduled
agents do not slowly fill the desktop with stale browser windows.

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
