# API Reference

Soulacy exposes a REST API on the configured port (default `8080`). All endpoints require authentication unless noted.

## Base URL

```
http://localhost:8080/v1
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
| `POST` | `/v1/agents/{id}/chat` | [Chat with an agent](agents.md) |
| `GET` | `/v1/agents` | [List agents](agents.md#list-agents) |
| `GET` | `/v1/agents/{id}` | [Get agent details](agents.md#get-agent) |
| `POST` | `/v1/auth/login` | [Issue a JWT](auth.md#login) |
| `POST` | `/v1/admin/api-keys` | [Create a managed API key](auth.md#create-api-key) |
| `DELETE` | `/v1/admin/api-keys/{id}` | [Revoke an API key](auth.md#revoke-api-key) |
| `GET` | `/v1/credentials` | [List credentials](credentials.md) |
| `POST` | `/v1/credentials` | [Store a credential](credentials.md#store-a-credential) |
| `DELETE` | `/v1/credentials/{id}` | [Delete a credential](credentials.md#delete-a-credential) |
| `GET` | `/v1/costs` | [Cost summary](costs.md) |
| `GET` | `/v1/costs/breakdown` | [Per-agent cost breakdown](costs.md#per-agent-breakdown) |
| `GET` | `/v1/admin/dlq` | [Dead-letter queue](admin.md#dead-letter-queue-dlq) |
| `POST` | `/v1/admin/dlq/{id}/retry` | [Retry a DLQ item](admin.md#retry-a-dlq-item) |
| `GET` | `/v1/health` | Health check (no auth required) |
