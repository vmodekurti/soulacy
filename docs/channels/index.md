# Channels Overview

Channels are adapters that connect agents to messaging platforms. Each channel handles authentication, message formatting, and webhook delivery for its platform.

## Supported channels

| Channel | Status | Config key |
|---------|--------|-----------|
| [HTTP](http.md) | ✅ Stable | — (always active) |
| [Telegram](telegram.md) | ✅ Stable | `channels.telegram` |
| [Slack](slack.md) | ✅ Stable | `channels.slack` |
| [Discord](discord.md) | ✅ Stable | `channels.discord` |
| [WhatsApp](whatsapp.md) | ✅ Stable | `channels.whatsapp` |

## How channels work

1. A user sends a message on a platform (Telegram, Slack, etc.)
2. The platform delivers a webhook to Soulacy
3. The channel adapter normalises the message into a `Message` struct
4. The runtime engine dispatches to the correct agent
5. The agent produces a reply
6. The channel adapter formats and sends the reply back

All channels share the same agent engine and conversation history — a user can switch channels mid-conversation and context is preserved.

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

## Channel-specific formatting

Each channel adapter handles platform-specific formatting automatically:

- **Telegram** — Markdown → MarkdownV2 escaping, inline buttons
- **Slack** — Markdown → Block Kit, thread replies
- **Discord** — Markdown → Discord markdown, embed cards
- **WhatsApp** — Plain text (WhatsApp does not support rich formatting)
- **HTTP** — Raw text or JSON, caller decides rendering
