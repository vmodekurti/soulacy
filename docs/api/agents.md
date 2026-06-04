# Agents API

## Chat with an agent

```
POST /api/v1/chat
Authorization: Bearer <token>
Content-Type: application/json
```

### Request

```json
{
  "agent_id": "assistant",
  "user_id": "user-abc",
  "text": "Summarise the latest AI news"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `agent_id` | string | ✅ | Agent to invoke |
| `text` | string | ✅ | User message text |
| `user_id` | string | — | Stable user/session key. Defaults to `api-user`. |
| `username` | string | — | Display name. Defaults to `user_id`. |
| `overrides` | object | — | One-run playground/test overrides. Does not mutate `SOUL.yaml`. |

### One-run overrides

```json
{
  "agent_id": "assistant",
  "user_id": "lab",
  "text": "Answer deterministically.",
  "overrides": {
    "provider": "ollama",
    "model": "qwen2.5:72b",
    "temperature": 0,
    "max_tokens": 800,
    "max_turns": 4,
    "tool_choice": "none"
  }
}
```

### Response

```json
{
  "reply": "Here's a summary of the latest AI news..."
}
```

### Streaming

Use `/api/v1/chat/stream` to receive Server-Sent Events.

```bash
curl -N -X POST http://localhost:8080/api/v1/chat/stream \
  -H "Authorization: Bearer sk_..." \
  -H "Content-Type: application/json" \
  -d '{"agent_id":"assistant","user_id":"u1","text":"Tell me a joke"}'
```

Events:

```
data: Why
data:  don't
data:  scientists
...
data: [DONE]
```

---

## List agents

```
GET /api/v1/agents
Authorization: Bearer <token>
```

### Response

```json
{
  "agents": [
    {
      "id": "assistant",
      "description": "A helpful general-purpose assistant",
      "model": "gpt-4o-mini",
      "channels": ["http", "telegram"],
      "tools": ["web_search"]
    },
    {
      "id": "researcher",
      "description": "Deep research agent",
      "model": "gpt-4o",
      "channels": ["http"],
      "tools": ["web_search", "url_fetch"]
    }
  ]
}
```

---

## Get agent

```
GET /api/v1/agents/{agent_id}
Authorization: Bearer <token>
```

### Response

```json
{
  "id": "assistant",
  "description": "A helpful general-purpose assistant",
  "model": "gpt-4o-mini",
  "system_prompt": "You are a helpful assistant.",
  "channels": ["http", "telegram"],
  "tools": ["web_search"],
  "token_budget": {
    "max_input_tokens": 32000,
    "max_output_tokens": 1024
  }
}
```
