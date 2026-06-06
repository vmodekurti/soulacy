# Slack

Connect agents to Slack using the Slack Events API and Socket Mode.

## Setup

### 1. Create a Slack app

1. Go to [api.slack.com/apps](https://api.slack.com/apps) → **Create New App** → **From scratch**
2. Choose a name and workspace

### 2. Configure OAuth scopes

Under **OAuth & Permissions → Bot Token Scopes**, add:

- `app_mentions:read`
- `channels:history`
- `chat:write`
- `files:read` (optional, for document handling)
- `im:history`
- `im:write`

### 3. Enable Socket Mode (recommended)

Under **Socket Mode**, enable it and generate an **App-Level Token** with `connections:write` scope. This avoids needing a public webhook URL.

### 4. Enable Events

Under **Event Subscriptions**, enable and subscribe to:

- `app_mention`
- `message.im`

### 5. Install to workspace and copy tokens

- **Bot Token** (`xoxb-...`) — from OAuth & Permissions
- **App-Level Token** (`xapp-...`) — from Socket Mode (if using socket mode)
- **Signing Secret** — from Basic Information

### 6. Configure Soulacy

```yaml title="config.yaml"
channels:
  slack:
    enabled: true
    bot_token: "xoxb-..."
    app_token: "xapp-..."        # Socket Mode token
    agent_id: assistant
```

---

## Multiple Slack bots

Use `bots:` when you want separate Slack bot credentials mapped to separate agents:

```yaml title="config.yaml"
channels:
  slack:
    enabled: true
    bots:
      - bot_token: "xoxb-assistant"
        app_token: "xapp-assistant"
        agent_id: assistant
      - bot_token: "xoxb-support"
        app_token: "xapp-support"
        agent_id: support-agent
```

This registers two adapter IDs:

| Adapter ID | Agent |
|------------|-------|
| `slack` | `assistant` |
| `slack-support-agent` | `support-agent` |

You can configure this from the GUI: **Channels → Slack → Edit → Bot mappings**.

---

## Features

| Feature | Support |
|---------|---------|
| DM messages | ✅ |
| Channel mentions | ✅ |
| Thread replies | ✅ (agent replies in thread) |
| Block Kit formatting | ✅ |
| File uploads | ✅ (text extracted) |
| Slash commands | 🔜 Planned |
