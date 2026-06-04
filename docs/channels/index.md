# Channels Overview

Channels are adapters that connect agents to messaging platforms. Each channel handles platform authentication, inbound message normalization, outbound replies, and live connection status.

## Supported channels

| Channel | Status | Config key |
|---------|--------|-----------|
| [HTTP](http.md) | ✅ Stable | — (always active) |
| [Telegram](telegram.md) | ✅ Stable | `channels.telegram` |
| [Slack](slack.md) | ✅ Stable | `channels.slack` |
| [Discord](discord.md) | ✅ Stable | `channels.discord` |
| [WhatsApp](whatsapp.md) | ✅ Stable | `channels.whatsapp` |

## How channels route to agents

There are two related configuration layers:

1. `config.yaml` chooses which agent a channel adapter routes inbound messages to.
2. The agent's `SOUL.yaml` `channels:` list declares which channels that agent is intended to be reachable on.

For a simple single-bot channel, `agent_id` is the routing target:

```yaml title="config.yaml"
channels:
  telegram:
    enabled: true
    token: "1234567890:AAH..."
    agent_id: assistant
```

```yaml title="agents/assistant/SOUL.yaml"
id: assistant
trigger: channel
channels:
  - telegram
  - http
```

At runtime, Telegram inbound messages become messages with `channel: telegram` and `agent_id: assistant`. The engine handles the message and the channel registry sends the reply back through the same adapter ID.

## Multi-bot mappings

Telegram, Slack, and Discord support multiple bot credentials under the same channel type. This is useful when you want separate platform bots mapped to separate agents.

```yaml title="config.yaml"
channels:
  telegram:
    enabled: true
    bots:
      - token: "BOT_TOKEN_FOR_SYSTEM"
        agent_id: system
        allowed_user_ids: [123456789]
      - token: "BOT_TOKEN_FOR_FINANCE"
        agent_id: financial-agent
        allowed_user_ids: [123456789]
```

The first bot keeps the canonical adapter ID. Additional bots get deterministic adapter IDs:

| Adapter ID | Agent |
|------------|-------|
| `telegram` | `system` |
| `telegram-financial-agent` | `financial-agent` |

The same pattern applies to Slack and Discord:

- first bot: `slack` or `discord`
- later bots: `slack-<agent_id>` or `discord-<agent_id>`

## Managing mappings in the GUI

Open **Channels** in the web UI:

- Channel cards show **Agent mappings**, including adapter ID, agent ID, and connection state.
- Click **Edit** on Telegram, Slack, or Discord to manage **Bot mappings**.
- Bot mapping rows provide an agent ID dropdown populated from your installed agents.
- After saving channel settings, click **Restart Gateway** from the banner to reconnect adapters.

## Multi-channel agents

An agent can be available on multiple channels simultaneously:

```yaml title="agents/assistant.soul.yaml"
name: assistant
model: gpt-4o-mini
system_prompt: You are a helpful assistant.
channels:
  - http
  - telegram
  - slack
  - discord
```

The channel must also be configured in `config.yaml`; the agent-side list alone does not create platform credentials.

## Channel-specific formatting

Each channel adapter handles platform-specific formatting automatically:

- **Telegram** — Markdown → MarkdownV2 escaping, inline buttons
- **Slack** — Markdown → Block Kit, thread replies
- **Discord** — Markdown → Discord markdown, embed cards
- **WhatsApp** — Plain text (WhatsApp does not support rich formatting)
- **HTTP** — Raw text or JSON, caller decides rendering
