# Admin API

Admin endpoints require `admin` role (server API key or JWT with `admin` role).

## Health check

No authentication required.

```
GET /v1/health
```

### Response

```json
{
  "status": "ok",
  "version": "0.1.0",
  "uptime_seconds": 3600
}
```

---

## Dead-letter queue (DLQ)

Failed agent invocations are pushed to the DLQ for inspection and retry.

### List DLQ items

```
GET /v1/admin/dlq
Authorization: Bearer sy_your-server-key
```

### Query parameters

| Parameter | Default | Description |
|-----------|---------|-------------|
| `queue` | — | Filter by agent ID |
| `limit` | `50` | Items per page |
| `offset` | `0` | Pagination offset |

### Response

```json
{
  "total": 3,
  "items": [
    {
      "id": "dlq_abc123",
      "queue": "researcher",
      "payload": { "session_id": "sess_xyz", "message": "..." },
      "error_msg": "context deadline exceeded",
      "attempts": 2,
      "created_at": "2026-05-28T09:15:00Z",
      "last_attempt_at": "2026-05-28T09:17:00Z"
    }
  ]
}
```

### Retry a DLQ item

Re-dispatches the failed message through the engine.

```
POST /v1/admin/dlq/{id}/retry
Authorization: Bearer sy_your-server-key
```

### Response

```json
{
  "id": "dlq_abc123",
  "status": "retried",
  "reply": "Here is the research you requested..."
}
```

### Delete a DLQ item

```
DELETE /v1/admin/dlq/{id}
Authorization: Bearer sy_your-server-key
```

```
204 No Content
```

---

## Agent marketplace

### List marketplace agents

```
GET /v1/admin/marketplace
Authorization: Bearer sy_your-server-key
```

### Response

```json
{
  "agents": [
    {
      "id": "community/web-researcher",
      "name": "Web Researcher",
      "description": "Deep research agent with web search",
      "author": "Soulacy Community",
      "tags": ["research", "web"],
      "installs": 142
    }
  ]
}
```

### Install a marketplace agent

```
POST /v1/admin/marketplace/install
Authorization: Bearer sy_your-server-key
Content-Type: application/json
```

```json
{
  "agent_id": "community/web-researcher"
}
```

```json
{
  "status": "installed",
  "path": "./agents/web-researcher.soul.yaml"
}
```
