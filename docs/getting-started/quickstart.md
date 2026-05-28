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
  type: sqlite
  path: ./soulacy.db

agents:
  dir: ./agents
```

---

## 2. Create an agent

```bash
mkdir agents
```

Create `agents/assistant.soul.yaml`:

```yaml title="agents/assistant.soul.yaml"
name: assistant
description: A helpful general-purpose assistant
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
curl -X POST http://localhost:8080/v1/agents/assistant/chat \
  -H "Authorization: Bearer your-server-api-key" \
  -H "Content-Type: application/json" \
  -d '{"message": "Hello! What can you do?"}'
```

Response:

```json
{
  "reply": "Hi! I'm a helpful assistant. I can answer questions, help with writing, analysis, and much more. What would you like to do?",
  "agent_id": "assistant",
  "session_id": "sess_abc123"
}
```

---

## 5. Add a Telegram bot (optional)

Get a bot token from [@BotFather](https://t.me/BotFather), then update your config:

```yaml title="config.yaml"
channels:
  telegram:
    token: "1234567890:AAH..."
    agents:
      - assistant
```

Restart — your agent now replies in Telegram.

---

## Next steps

- [Your First Agent](first-agent.md) — learn the full SOUL.yaml schema
- [Configuration Reference](../configuration/index.md) — all config options
- [Channels](../channels/index.md) — connect to Slack, Discord, WhatsApp
