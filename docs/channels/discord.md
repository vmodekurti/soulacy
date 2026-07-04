# Discord

Connect agents to Discord using the Discord Gateway (WebSocket).

## Setup

### 1. Create a Discord application

1. Go to [discord.com/developers/applications](https://discord.com/developers/applications)
2. Click **New Application**, give it a name
3. Navigate to **Bot** → **Add Bot**
4. Copy the **Token**. Soulacy accepts either the raw token or `Bot <token>`.

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
    enabled: true
    token: "MTI3..."    # raw bot token; "Bot MTI3..." also works
    agent_id: assistant
    guild_id: ""        # optional: restrict to one guild/server
```

---

## Multiple Discord bots

Use `bots:` when you want separate Discord bot credentials mapped to separate agents:

```yaml title="config.yaml"
channels:
  discord:
    enabled: true
    bots:
      - token: "BOT_TOKEN_1"
        agent_id: assistant
        guild_id: ""
      - token: "BOT_TOKEN_2"
        agent_id: moderator-agent
        guild_id: ""
```

This registers two adapter IDs:

| Adapter ID | Agent |
|------------|-------|
| `discord` | `assistant` |
| `discord-moderator-agent` | `moderator-agent` |

You can configure this from the GUI: **Channels → Discord → Edit → Bot mappings**.

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
