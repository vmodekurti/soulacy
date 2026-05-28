# Architecture Overview

Soulacy is a single Go binary with a layered architecture. This page describes how components fit together.

## High-level diagram

```
┌─────────────────────────────────────────────────────────┐
│                     Inbound layer                        │
│  HTTP API  │  Telegram  │  Slack  │  Discord  │  WhatsApp│
└──────┬──────┴─────┬──────┴────┬────┴─────┬─────┴────┬────┘
       │            │           │          │          │
       └────────────┴───────────┴──────────┴──────────┘
                              │
                    ┌─────────▼──────────┐
                    │   Auth middleware   │
                    │  (static key →     │
                    │   sk_ keys → JWT)  │
                    └─────────┬──────────┘
                              │
                    ┌─────────▼──────────┐
                    │  Runtime Engine    │
                    │  - Agent dispatch  │
                    │  - History append  │
                    │  - DLQ on failure  │
                    │  - Token tracking  │
                    └──┬──────┬──────┬───┘
                       │      │      │
            ┌──────────┘      │      └──────────┐
            │                 │                 │
  ┌─────────▼──────┐  ┌───────▼───────┐  ┌─────▼─────────┐
  │  LLM provider  │  │  Tool runner  │  │  Workflow DAG  │
  │  (OpenAI, etc) │  │  (web_search, │  │  executor      │
  └────────────────┘  │   url_fetch…) │  └───────────────┘
                       └───────────────┘
                              │
          ┌───────────────────┼───────────────────┐
          │                   │                   │
  ┌───────▼───────┐  ┌────────▼────────┐  ┌──────▼──────┐
  │  Storage      │  │  Rate limiter   │  │  Telemetry  │
  │  (SQLite /    │  │  (sliding       │  │  (OTEL +    │
  │   Postgres)   │  │   window)       │  │   Prometheus)│
  └───────────────┘  └─────────────────┘  └─────────────┘
```

---

## Component map

| Package | Path | Responsibility |
|---------|------|---------------|
| HTTP server | `cmd/soulacy/` | Entry point, dependency wiring |
| Auth engine | `internal/auth/` | Middleware chain, JWT, API keys, RBAC |
| Runtime engine | `internal/runtime/` | Agent dispatch, history, DLQ |
| LLM client | `internal/llm/` | Provider adapters (OpenAI, Anthropic, Ollama…) |
| Channel adapters | `internal/channels/` | Telegram, Slack, Discord, WhatsApp, HTTP |
| Session history | `internal/session/` | Conversation history store |
| Dead-letter queue | `internal/dlq/` | Failed message persistence |
| API keys | `internal/auth/apikeys/` | Managed `sk_` key lifecycle |
| Credential vault | `internal/vault/` | AES-256-GCM encrypted secrets |
| Rate limiter | `internal/ratelimit/` | Per-user/org sliding window |
| Cost tracker | `internal/costs/` | Token usage recording |
| Telemetry | `internal/telemetry/` | OTEL traces + Prometheus metrics |
| Agent builder | `internal/builder/` | SOUL.yaml parsing, validation, capability gap detection |
| Workflow executor | `internal/workflow/` | DAG resolution and parallel step execution |
| Marketplace | `internal/marketplace/` | Agent registry and install |

---

## Request lifecycle

A typical HTTP API request flows through:

1. **Fiber HTTP server** receives `POST /v1/agents/assistant/chat`
2. **Auth middleware** validates the bearer token (static key → `sk_` key → JWT)
3. **Rate limiter middleware** checks per-user request and token quotas
4. **Runtime engine** looks up the agent definition and builds the context (system prompt + history)
5. **LLM client** calls the configured provider with the assembled context
6. **Tool runner** handles any tool calls the LLM emits (web_search, etc.) in a loop
7. **History store** appends the user and assistant turns to the conversation log
8. **Cost tracker** records token usage against the user/agent/org
9. **OTEL** emits a trace span covering the entire request
10. Response is returned to the caller

If any step after auth panics or times out, the deferred **DLQ handler** pushes the message to the dead-letter queue for later inspection or retry.

---

## Design principles

**Single binary** — Soulacy compiles to a single self-contained binary with no runtime dependencies. Deploy it anywhere Go runs.

**Local interfaces** — Packages define minimal interfaces for their dependencies rather than importing concrete types. This keeps the dependency graph acyclic and makes testing straightforward.

**Pluggable storage** — The storage layer is abstracted behind interfaces. SQLite, Postgres, and in-memory backends are interchangeable.

**Layered auth** — Auth is checked once at the middleware layer. Downstream components receive a validated `Claims` struct and never touch raw tokens.

**Structured logging** — All logs use `zap` in JSON mode. Log level, correlation IDs, and trace context are consistent across components.

**Graceful shutdown** — The server handles `SIGINT`/`SIGTERM`, drains in-flight requests up to a configurable timeout, and closes storage connections cleanly.
