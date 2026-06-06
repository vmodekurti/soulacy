# Quick Start

Get a working AI agent deployed in under 5 minutes.

## 1. Create a config file

```bash
mkdir my-agents && cd my-agents
```

Create `config.yaml`:

```yaml title="config.yaml"
server:
  host: 0.0.0.0
  port: 8080
  api_key: "your-server-api-key"  # any secret string

llm:
  default_provider: openai
  providers:
    openai:
      api_key: "sk-..."

storage:
  backend: sqlite

agent_dirs:
  - ./agents
```

---

## 2. Create an agent

```bash
mkdir agents
```

Create `agents/assistant/SOUL.yaml`:

```yaml title="agents/assistant/SOUL.yaml"
id: assistant
name: Assistant
description: A helpful general-purpose assistant
trigger: channel
llm:
  provider: openai
  model: gpt-4o-mini
system_prompt: |
  You are a helpful, concise assistant.
channels:
  - http
```

---

## 3. Start the server

```bash
soulacy serve --config config.yaml
```

You should see:

```
✓ Loaded agent: assistant
✓ HTTP channel listening on :8080
✓ Soulacy is ready
```

---

## 4. Talk to your agent

```bash
curl -X POST http://localhost:8080/api/v1/chat \
  -H "Authorization: Bearer your-server-api-key" \
  -H "Content-Type: application/json" \
  -d '{"agent_id":"assistant","user_id":"quickstart","text":"Hello! What can you do?"}'
```

Response:

```json
{
  "reply": "Hi! I'm a helpful assistant..."
}
```

---

## 5. Add a Telegram bot (optional)

Get a bot token from [@BotFather](https://t.me/BotFather), then update your config:

```yaml title="config.yaml"
channels:
  telegram:
    enabled: true
    token: "1234567890:AAH..."
    agent_id: assistant
```

Restart — your agent now replies in Telegram.

---

## Next steps

- [Your First Agent](first-agent.md) — learn the full SOUL.yaml schema
- [Configuration Reference](../configuration/index.md) — all config options
- [Channels](../channels/index.md) — connect to Slack, Discord, WhatsApp
