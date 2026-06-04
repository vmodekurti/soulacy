# Configuration Overview

Soulacy is configured via a single `config.yaml` file. Pass its path with `--config`:

```bash
soulacy serve --config /etc/soulacy/config.yaml
```

## Full structure

```yaml
server:        # HTTP server settings
  host: 0.0.0.0
  port: 8080
  api_key: "..."

llm:           # LLM provider registry
  default_provider: openai
  providers: { ... }

storage:       # Persistence backend
  backend: sqlite

auth:          # Authentication & RBAC
  jwt_secret: "..."
  api_keys: { ... }

channels:      # Messaging channel adapters
  telegram:
    enabled: true
    token: "..."
    agent_id: assistant

rate_limit:    # Per-user/org rate limiting
  enabled: true
  ...

telemetry:     # OpenTelemetry & cost tracking
  enabled: true
  ...

agent_dirs:    # Agent discovery
  - ./agents
```

## Environment variable overrides

Any config key can be overridden with an environment variable using the pattern `SOULACY__SECTION__KEY`:

```bash
export SOULACY__SERVER__API_KEY="my-secret"
export SOULACY__LLM__PROVIDERS__OPENAI__API_KEY="sk-..."
```

## Sections

| Section | Description |
|---------|-------------|
| [server](server.md) | Host, port, API key, TLS |
| [gui](gui.md) | Built-in web console, restart button, bot mappings |
| [llm](llm.md) | LLM providers and defaults |
| [storage](storage.md) | SQLite, Postgres, Redis |
| [auth](auth.md) | JWT, managed API keys, RBAC |
| [rate_limit](rate-limiting.md) | Per-user and per-org quotas |
| [telemetry](telemetry.md) | OTEL traces and cost tracking |

!!! warning "Keep secrets out of version control"
    Your `config.yaml` contains API keys and tokens. Add it to `.gitignore` and use environment variable overrides or a secrets manager in production.
