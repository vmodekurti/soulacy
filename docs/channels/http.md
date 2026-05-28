# HTTP API Channel

The HTTP channel is always active and exposes a REST API for direct integration with web apps, mobile apps, and other services.

## Chat endpoint

```
POST /v1/agents/{agent_id}/chat
Authorization: Bearer <token>
Content-Type: application/json
```

### Request

```json
{
  "message": "What is the capital of France?",
  "session_id": "user-123",        // optional; omit to start a new session
  "attachments": [                  // optional
    {
      "type": "image",
      "url": "https://example.com/photo.jpg"
    }
  ]
}
```

### Response

```json
{
  "reply": "The capital of France is Paris.",
  "agent_id": "assistant",
  "session_id": "user-123",
  "tokens_used": {
    "input": 42,
    "output": 12
  }
}
```

---

## Streaming (SSE)

For real-time token streaming, add `Accept: text/event-stream`:

```bash
curl -N -X POST http://localhost:8080/v1/agents/assistant/chat \
  -H "Authorization: Bearer sy_your-key" \
  -H "Content-Type: application/json" \
  -H "Accept: text/event-stream" \
  -d '{"message": "Tell me a short story", "session_id": "u1"}'
```

Events:

```
data: {"delta": "Once"}
data: {"delta": " upon"}
data: {"delta": " a time"}
data: {"done": true, "session_id": "u1", "tokens_used": {"input": 12, "output": 24}}
```

---

## Session management

Sessions persist conversation history. Use the same `session_id` across requests to maintain context:

```bash
# Turn 1
curl -X POST .../agents/assistant/chat \
  -d '{"message": "My name is Alice", "session_id": "alice-session"}'

# Turn 2 — agent remembers the name
curl -X POST .../agents/assistant/chat \
  -d '{"message": "What is my name?", "session_id": "alice-session"}'
# Response: "Your name is Alice."
```

Sessions are stored in the configured storage backend and preserved across server restarts.

---

## Attachments

Pass images or documents in the request:

```json
{
  "message": "What does this chart show?",
  "session_id": "u1",
  "attachments": [
    {
      "type": "image",
      "url": "https://example.com/chart.png"
    }
  ]
}
```

Requires a vision-capable model (`gpt-4o`, `claude-3-5-sonnet`, etc.) configured for the agent.

---

## Authentication

The HTTP channel respects the full auth stack:

| Token type | Header |
|-----------|--------|
| Server API key | `Authorization: Bearer sy_...` |
| Managed API key | `Authorization: Bearer sk_...` |
| JWT | `Authorization: Bearer eyJ...` |

See [Auth configuration](../configuration/auth.md) for details.
