# Telegram

Connect your agents to Telegram using the Bot API.

## Setup

### 1. Create a bot

1. Open Telegram and message [@BotFather](https://t.me/BotFather)
2. Send `/newbot` and follow the prompts
3. Copy the token: `1234567890:AAH...`

### 2. Configure Soulacy

```yaml title="config.yaml"
channels:
  telegram:
    token: "1234567890:AAH..."
    agents:
      - assistant          # which agents are reachable on Telegram
    webhook_url: "https://yourdomain.com/webhooks/telegram"   # optional
```

### 3. Webhook vs polling

**Polling (default)** — Soulacy polls Telegram for new updates. Works without a public URL. Best for development.

**Webhook** — Telegram pushes updates to your server. Requires a publicly reachable HTTPS URL. Best for production.

```yaml
channels:
  telegram:
    token: "..."
    mode: webhook                            # polling (default) | webhook
    webhook_url: "https://yourdomain.com/webhooks/telegram"
```

On startup, Soulacy automatically registers the webhook URL with Telegram.

---

## Multi-agent routing

Route different Telegram commands to different agents:

```yaml
channels:
  telegram:
    token: "..."
    routes:
      /start: assistant
      /research: researcher
      /summary: summarizer
    default_agent: assistant    # fallback for messages without a command
```

---

## Features

| Feature | Support |
|---------|---------|
| Text messages | ✅ |
| Photos / images | ✅ (passed to vision-capable agents) |
| Documents | ✅ (text extracted and passed as context) |
| Voice messages | 🔜 Planned |
| Inline keyboards | ✅ (agent can emit buttons) |
| Groups & channels | ✅ (mention the bot to trigger) |
| Commands | ✅ |

---

## Example: minimal Telegram bot

```yaml title="config.yaml"
server:
  api_key: "sy_..."
llm:
  default_provider: openai
  providers:
    openai:
      api_key: "sk-..."
storage:
  type: sqlite
  path: ./soulacy.db
channels:
  telegram:
    token: "1234567890:AAH..."
    agents: [assistant]
agents:
  dir: ./agents
```

```yaml title="agents/assistant.soul.yaml"
name: assistant
model: gpt-4o-mini
system_prompt: You are a helpful assistant on Telegram.
channels:
  - telegram
```

Start with `soulacy serve --config config.yaml` and message your bot on Telegram.
