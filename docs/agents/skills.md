# Skills

Skills are reusable capability bundles that combine a system prompt fragment, tool set, and configuration — packaged so they can be mixed into any agent.

## What is a skill?

A skill is a YAML fragment you can `include` in a SOUL.yaml file:

```yaml title="skills/web-researcher.skill.yaml"
tools:
  - web_search
  - url_fetch
system_prompt_append: |
  When answering factual questions, search the web to find current information.
  Always cite your sources.
```

Apply it to an agent:

```yaml title="agents/assistant.soul.yaml"
name: assistant
model: gpt-4o
system_prompt: You are a helpful assistant.
skills:
  - web-researcher
channels:
  - http
```

The skill's tools and prompt fragment are merged into the agent at load time.

## Built-in skills

| Skill | Description |
|-------|-------------|
| `web-researcher` | Web search + URL fetch with citation instructions |
| `code-helper` | Code interpreter + coding-focused prompt |
| `data-analyst` | Calculator + code interpreter + data analysis prompt |
| `image-reader` | Image analysis tool + vision prompt |

## Creating a skill

Place a `.skill.yaml` file in your `skills/` directory (or configure `skills.dir` in config):

```yaml title="skills/customer-support.skill.yaml"
system_prompt_append: |
  You are a support agent for Acme Corp.
  - Only discuss Acme products.
  - Escalate billing issues to human support.
  - Be empathetic and concise.

tools: []  # no extra tools needed
```

Apply to multiple agents:

```yaml
# agents/telegram-support.soul.yaml
name: telegram-support
model: gpt-4o-mini
system_prompt: You help Acme customers via Telegram.
skills:
  - customer-support
channels:
  - telegram

# agents/web-support.soul.yaml
name: web-support
model: gpt-4o-mini
system_prompt: You help Acme customers via the web widget.
skills:
  - customer-support
channels:
  - http
```

## Skill directory

```yaml title="config.yaml"
agent_dirs:
  - ./agents
skill_dirs:
  - ./skills
```

## Multiple skills

Skills are merged in order — later skills override earlier ones for conflicting fields:

```yaml
skills:
  - web-researcher
  - customer-support   # this prompt append comes second
```
