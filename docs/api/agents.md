# Agents API

## Chat with an agent

```
POST /v1/agents/{agent_id}/chat
Authorization: Bearer <token>
Content-Type: application/json
```

### Request

```json
{
  "message": "Summarise the latest AI news",
  "session_id": "user-abc"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `message` | string | ✅ | User message text |
| `session_id` | string | — | Session ID for conversation history. Omit to start a new session. |
| `attachments` | array | — | List of file/image attachments |

### Attachments

```json
{
  "message": "What's in this image?",
  "session_id": "u1",
  "attachments": [
    {
      "type": "image",
      "url": "https://example.com/photo.jpg"
    },
    {
      "type": "document",
      "url": "https://example.com/report.pdf",
      "filename": "report.pdf"
    }
  ]
}
```

### Response

```json
{
  "reply": "Here's a summary of the latest AI news...",
  "agent_id": "assistant",
  "session_id": "user-abc",
  "tokens_used": {
    "input": 312,
    "output": 128
  }
}
```

### Streaming

Add `Accept: text/event-stream` to receive token-by-token streaming via Server-Sent Events.

```bash
curl -N -X POST http://localhost:8080/v1/agents/assistant/chat \
  -H "Authorization: Bearer sk_..." \
  -H "Content-Type: application/json" \
  -H "Accept: text/event-stream" \
  -d '{"message": "Tell me a joke", "session_id": "u1"}'
```

Events:

```
data: {"delta": "Why"}
data: {"delta": " don't"}
data: {"delta": " scientists"}
...
data: {"done": true, "tokens_used": {"input": 8, "output": 22}}
```

---

## List agents

```
GET /v1/agents
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
GET /v1/agents/{agent_id}
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
