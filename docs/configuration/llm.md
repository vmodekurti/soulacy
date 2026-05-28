# LLM Providers

Soulacy supports multiple LLM providers simultaneously. Each agent picks its provider via the `model` field in SOUL.yaml.

## Reference

```yaml
llm:
  default_provider: openai   # used when model has no prefix
  timeout: 120s              # per-request LLM timeout

  providers:
    openai:
      api_key: "sk-..."
      base_url: https://api.openai.com/v1   # override for proxies

    anthropic:
      api_key: "sk-ant-..."

    ollama:
      base_url: http://localhost:11434

    together:
      api_key: "..."
      base_url: https://api.together.xyz/v1

    qwen:
      api_key: "..."
      base_url: https://dashscope.aliyuncs.com/compatible-mode/v1
```

## Selecting a model in SOUL.yaml

Use bare model names for the default provider, or prefix with `provider/`:

```yaml
# Uses default_provider (openai)
model: gpt-4o-mini

# Explicit provider prefix
model: anthropic/claude-3-5-sonnet-20241022
model: ollama/llama3.2
model: together/meta-llama/Meta-Llama-3.1-70B-Instruct-Turbo
model: qwen/qwen-max
```

## Supported providers

| Provider | Config key | Notes |
|----------|-----------|-------|
| OpenAI | `openai` | GPT-4o, GPT-4o-mini, o1, o3 |
| Anthropic | `anthropic` | Claude 3.5, Claude 3 Haiku |
| Ollama | `ollama` | Local models, no API key needed |
| Together AI | `together` | Open-source models at scale |
| Qwen (Alibaba) | `qwen` | Qwen-Max, Qwen-Plus, Qwen-Turbo |

## Custom / compatible endpoints

Any OpenAI-compatible API (vLLM, LM Studio, Groq, etc.) works via the `base_url` override:

```yaml
llm:
  providers:
    openai:
      api_key: "not-needed"
      base_url: http://localhost:1234/v1   # LM Studio
```

## Timeouts

```yaml
llm:
  timeout: 180s   # increase for slow models or long context
```

Per-agent token budgets are configured in [SOUL.yaml](../agents/soul-yaml.md#token_budget).
