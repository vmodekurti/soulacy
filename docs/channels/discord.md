# Discord

Connect agents to Discord using the Discord Gateway (WebSocket).

## Setup

### 1. Create a Discord application

1. Go to [discord.com/developers/applications](https://discord.com/developers/applications)
2. Click **New Application**, give it a name
3. Navigate to **Bot** → **Add Bot**
4. Copy the **Token**

### 2. Set bot permissions

Under **OAuth2 → URL Generator**, select:

- Scopes: `bot`, `applications.commands`
- Bot Permissions: `Send Messages`, `Read Message History`, `View Channels`

### 3. Configure intents

Under **Bot → Privileged Gateway Intents**, enable:

- **Message Content Intent** (required to read message text)

### 4. Invite the bot to your server

Use the generated OAuth2 URL to add the bot to your server.

### 5. Configure Soulacy

```yaml title="config.yaml"
channels:
  discord:
    token: "MTI3..."    # Bot token
    agents:
      - assistant
    default_agent: assistant
```

---

## Routing by channel

```yaml
channels:
  discord:
    token: "..."
    routes:
      "support": support-agent      # channel name
      "research": researcher
    default_agent: assistant
```

---

## Features

| Feature | Support |
|---------|---------|
| Direct messages | ✅ |
| Server channel messages | ✅ (mention the bot) |
| Thread replies | ✅ |
| Embeds | ✅ |
| Slash commands | 🔜 Planned |
| Attachments | ✅ (images passed to vision agents) |

---

## Tips

- The bot only responds when directly mentioned (`@BotName`) in a server channel to avoid noise
- In DMs, all messages are processed without needing a mention
- Discord has a 2000-character message limit; long responses are automatically split across multiple messages
