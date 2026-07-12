# Costs API

Soulacy records LLM token usage for every agent invocation. Estimated `cost_usd` is populated when `costs.pricing` is configured for the provider/model; unknown prices remain `0` rather than guessed. Use this API to monitor spend by agent, user, or time period.

## Cost summary

```
GET /v1/costs
Authorization: Bearer <token>
```

### Query parameters

| Parameter | Default | Description |
|-----------|---------|-------------|
| `period` | `30d` | Time period: `1d`, `7d`, `30d`, `90d`, or `all` |
| `agent_id` | — | Filter by agent ID |
| `user_id` | — | Filter by user ID |

### Response

```json
{
  "period": "30d",
  "total_cost_usd": 4.27,
  "total_tokens": {
    "input": 1240000,
    "output": 320000
  },
  "by_provider": {
    "openai": {
      "cost_usd": 3.92,
      "input_tokens": 1100000,
      "output_tokens": 300000
    },
    "anthropic": {
      "cost_usd": 0.35,
      "input_tokens": 140000,
      "output_tokens": 20000
    }
  }
}
```

---

## Per-agent breakdown

```
GET /v1/costs/breakdown
Authorization: Bearer <token>
```

### Query parameters

Same as summary (`period`, `agent_id`, `user_id`).

### Response

```json
{
  "period": "30d",
  "agents": [
    {
      "agent_id": "researcher",
      "model": "gpt-4o",
      "invocations": 142,
      "cost_usd": 3.15,
      "input_tokens": 890000,
      "output_tokens": 210000
    },
    {
      "agent_id": "assistant",
      "model": "gpt-4o-mini",
      "invocations": 1203,
      "cost_usd": 1.12,
      "input_tokens": 350000,
      "output_tokens": 110000
    }
  ]
}
```

---

## Per-invocation log

```
GET /v1/costs/log
Authorization: Bearer <token>
```

### Query parameters

| Parameter | Default | Description |
|-----------|---------|-------------|
| `limit` | `50` | Records per page |
| `offset` | `0` | Pagination offset |
| `agent_id` | — | Filter by agent |
| `since` | — | ISO 8601 timestamp |

### Response

```json
{
  "total": 1345,
  "records": [
    {
      "id": "cost_xyz",
      "agent_id": "researcher",
      "session_id": "sess_abc",
      "model": "gpt-4o",
      "provider": "openai",
      "input_tokens": 2100,
      "output_tokens": 480,
      "cost_usd": 0.0231,
      "created_at": "2026-05-28T11:22:00Z"
    }
  ]
}
```
