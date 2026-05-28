# Storage

Soulacy uses a pluggable storage backend for conversation history, session data, the dead-letter queue, and agent cost records.

## Reference

```yaml
storage:
  type: sqlite          # sqlite | postgres | memory
  path: ./soulacy.db   # SQLite only

  # Postgres (when type: postgres)
  postgres:
    dsn: "postgres://user:pass@localhost:5432/soulacy?sslmode=disable"
    max_open_conns: 25
    max_idle_conns: 5
    conn_max_lifetime: 5m

  # Redis — used for rate limit counters and distributed locks
  redis:
    addr: localhost:6379
    password: ""
    db: 0
```

## Backends

### SQLite (default)

Zero-dependency, single-file database. Best for local dev, single-node deployments, and low-traffic bots.

```yaml
storage:
  type: sqlite
  path: /var/lib/soulacy/soulacy.db
```

SQLite is WAL-mode enabled automatically for better concurrent read performance.

### Postgres

Recommended for multi-replica or high-traffic deployments.

```yaml
storage:
  type: postgres
  postgres:
    dsn: "postgres://soulacy:secret@db:5432/soulacy?sslmode=require"
    max_open_conns: 25
```

Soulacy runs migrations automatically on startup — no `migrate` step needed.

### Memory

In-memory store with no persistence. Data is lost on restart. Useful for testing.

```yaml
storage:
  type: memory
```

## Redis

Redis is optional and used only for:

- Rate limit counters (distributed, across multiple nodes)
- Distributed locks

If Redis is not configured, rate limiting falls back to in-process counters (suitable for single-node).

```yaml
storage:
  redis:
    addr: redis:6379
    password: "your-redis-password"
```

## What is stored

| Data | Table / Key |
|------|-------------|
| Conversation history | `conversation_entries` |
| Session metadata | `sessions` |
| API keys (hashed) | `api_keys` |
| Dead-letter queue | `dead_letters` |
| Cost records | `cost_records` |
| Agent marketplace | `agent_registry` |
