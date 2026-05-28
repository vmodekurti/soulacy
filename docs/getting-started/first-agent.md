# Your First Agent

This guide walks through building a more capable agent step by step — one that uses tools, maintains conversation history, and enforces a persona.

## Minimal agent

Every agent starts as a SOUL.yaml file:

```yaml
name: helper
description: A simple helper agent
model: gpt-4o-mini
system_prompt: You are a helpful assistant.
channels:
  - http
```

Place this in your `agents/` directory and restart Soulacy.

---

## Add a persona

Use `system_prompt` to shape personality and scope:

```yaml
name: support-bot
description: Customer support for Acme Corp
model: gpt-4o
system_prompt: |
  You are a friendly support agent for Acme Corp.
  - Only answer questions about Acme products.
  - If you don't know, say so and offer to escalate.
  - Keep replies under 150 words unless the user asks for detail.
  - Never reveal internal pricing or roadmap details.
channels:
  - http
  - telegram
```

---

## Add tools

Tools let your agent call functions — fetch data, run code, search the web:

```yaml
name: researcher
description: Research assistant with web search
model: gpt-4o
system_prompt: |
  You are a research assistant. Use search to find current information.
tools:
  - web_search
  - url_fetch
channels:
  - http
```

See the [Built-in Tools reference](../agents/tools.md) for the full list.

---

## Set token limits

Control cost and latency per response:

```yaml
name: summarizer
model: gpt-4o-mini
system_prompt: Summarize documents concisely.
token_budget:
  max_input_tokens: 16000
  max_output_tokens: 512
channels:
  - http
```

---

## Add memory / session history

Session history is enabled by default when you configure a storage backend. The agent automatically receives the last N turns of conversation context.

To control context window:

```yaml
name: assistant
model: gpt-4o-mini
system_prompt: You are a helpful assistant.
session:
  history_turns: 20      # how many prior turns to include
channels:
  - http
```

---

## Multi-channel agent

Deploy the same agent to multiple channels simultaneously:

```yaml
name: concierge
description: Multi-channel concierge
model: gpt-4o
system_prompt: |
  You are a concierge assistant. Be polite and efficient.
channels:
  - http
  - telegram
  - slack
  - discord
```

Each channel uses its own adapter — the agent code is unchanged.

---

## Full example

```yaml title="agents/concierge.soul.yaml"
name: concierge
description: Full-featured concierge agent
model: gpt-4o
system_prompt: |
  You are a friendly concierge at Acme Inc.
  Help users with questions, research, and task management.
  Be concise. Use bullet points for lists.

tools:
  - web_search
  - url_fetch
  - calculator

token_budget:
  max_input_tokens: 32000
  max_output_tokens: 1024

session:
  history_turns: 30

channels:
  - http
  - telegram
  - slack
```

---

## Next steps

- [SOUL.yaml Reference](../agents/soul-yaml.md) — complete schema documentation
- [Workflow DAGs](../agents/workflow.md) — chain agents into pipelines
- [Configuration](../configuration/index.md) — server, LLM, and storage options
