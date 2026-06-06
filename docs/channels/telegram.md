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
    enabled: true
    token: "1234567890:AAH..."
    agent_id: assistant
    allowed_user_ids: []   # optional allowlist of Telegram user IDs
```

Telegram currently uses long polling by default, so it works on a laptop or private server without a public HTTPS URL.

### 3. Allow specific Telegram users

Set `allowed_user_ids` to restrict who can talk to the bot:

```yaml
channels:
  telegram:
    enabled: true
    token: "..."
    agent_id: assistant
    allowed_user_ids: [123456789, 987654321]
```

---

## Multiple Telegram bots

Use `bots:` when you want one Telegram bot per agent:

```yaml
channels:
  telegram:
    enabled: true
    bots:
      - token: "BOT_TOKEN_1"
        agent_id: assistant
        allowed_user_ids: [123456789]
      - token: "BOT_TOKEN_2"
        agent_id: financial-agent
        allowed_user_ids: [123456789]
```

This registers two adapter IDs:

| Adapter ID | Agent |
|------------|-------|
| `telegram` | `assistant` |
| `telegram-financial-agent` | `financial-agent` |

You can configure this from the GUI: **Channels → Telegram → Edit → Bot mappings**.

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
    enabled: true
    token: "1234567890:AAH..."
    agent_id: assistant
agent_dirs:
  - ./agents
```

```yaml title="agents/assistant/SOUL.yaml"
id: assistant
name: Assistant
trigger: channel
llm:
  provider: openai
  model: gpt-4o-mini
system_prompt: You are a helpful assistant on Telegram.
channels:
  - telegram
```

Start with `soulacy serve --config config.yaml` and message your bot on Telegram.
