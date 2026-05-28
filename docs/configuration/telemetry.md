# Telemetry

Soulacy emits OpenTelemetry traces and metrics, and tracks LLM cost per agent, user, and org.

## Reference

```yaml
telemetry:
  enabled: true

  # OpenTelemetry trace export
  otel:
    exporter: otlp          # otlp | jaeger | stdout | none
    endpoint: http://localhost:4318   # OTLP HTTP endpoint
    service_name: soulacy
    sample_rate: 1.0        # 1.0 = 100%, 0.1 = 10%

  # Prometheus metrics
  metrics:
    enabled: true
    path: /metrics          # scrape endpoint

  # Cost tracking
  cost:
    enabled: true
    # Cost records are stored in the database.
    # View via GET /v1/costs
```

## Traces

Every agent invocation produces an OTEL trace with spans for:

- HTTP request handling
- Auth middleware
- Agent engine dispatch
- LLM provider call (including model, token counts, latency)
- Tool calls (web_search, url_fetch, etc.)
- Channel adapter send

### Jaeger (local dev)

```bash
docker run -d \
  -p 16686:16686 \
  -p 4318:4318 \
  jaegertracing/all-in-one:latest
```

```yaml
telemetry:
  otel:
    exporter: otlp
    endpoint: http://localhost:4318
```

Open [http://localhost:16686](http://localhost:16686) to view traces.

### Stdout (debug)

```yaml
telemetry:
  otel:
    exporter: stdout
```

Prints trace JSON to the server log — useful for debugging without a collector.

## Metrics

Prometheus metrics are available at `/metrics`:

| Metric | Type | Description |
|--------|------|-------------|
| `soulacy_requests_total` | Counter | Total HTTP requests by method, path, status |
| `soulacy_agent_invocations_total` | Counter | Agent calls by agent ID and status |
| `soulacy_llm_tokens_total` | Counter | LLM tokens consumed by provider and model |
| `soulacy_llm_latency_seconds` | Histogram | LLM response latency |
| `soulacy_rate_limit_hits_total` | Counter | Rate limit rejections by user |

## Cost tracking

Soulacy records token usage and estimated cost for every LLM call. Cost per token is derived from provider pricing tables embedded in the binary (updated with each release).

```bash
# View cost summary
curl http://localhost:8080/v1/costs \
  -H "Authorization: Bearer sy_your-key"

# Filter by agent
curl "http://localhost:8080/v1/costs?agent_id=assistant&period=7d"
```

See the [Costs API reference](../api/costs.md) for full details.

## Disabling telemetry

```yaml
telemetry:
  enabled: false
```
