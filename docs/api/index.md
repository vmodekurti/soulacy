# API Reference

Soulacy exposes a REST API on the configured port (default `8080`). All endpoints require authentication unless noted.

## Base URL

```
http://localhost:8080/api/v1
```

## Authentication

Include an auth token in every request:

```
Authorization: Bearer <token>
```

Accepted token types: server API key (`sy_`), managed API key (`sk_`), or JWT (`eyJ`). See [Auth](../configuration/auth.md).

## Content type

```
Content-Type: application/json
```

## Response format

All responses are JSON. Errors follow this shape:

```json
{
  "error": "unauthorized",
  "message": "invalid or expired token",
  "status": 401
}
```

## Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/v1/chat` | [Chat with an agent](agents.md) |
| `POST` | `/api/v1/chat/stream` | [Stream a chat response](agents.md#streaming) |
| `GET` | `/api/v1/agents` | [List agents](agents.md#list-agents) |
| `GET` | `/api/v1/agents/{id}` | [Get agent details](agents.md#get-agent) |
| `POST` | `/api/v1/agents/validate` | Validate an agent definition |
| `GET` | `/api/v1/credentials` | [List credentials](credentials.md) |
| `POST` | `/api/v1/credentials` | [Store a credential](credentials.md#store-a-credential) |
| `DELETE` | `/api/v1/credentials/{id}` | [Delete a credential](credentials.md#delete-a-credential) |
| `GET` | `/api/v1/costs` | [Cost summary](costs.md) |
| `GET` | `/api/v1/costs/breakdown` | [Per-agent cost breakdown](costs.md#per-agent-breakdown) |
| `POST` | `/api/v1/admin/restart` | [Restart gateway](admin.md#restart-gateway) |
| `GET` | `/api/v1/health` | Health check |
