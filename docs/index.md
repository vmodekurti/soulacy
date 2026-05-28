# Soulacy

**Deploy production-grade AI agents in minutes — not months.**

Soulacy is an open-source multi-agent orchestration platform. Define agents in a simple YAML file, connect them to any LLM, and deploy to Telegram, Slack, Discord, WhatsApp, or HTTP with a single binary.

<div class="grid cards" markdown>

-   :material-rocket-launch: **Ship fast**

    ---

    One YAML file per agent. One binary to run. Zero boilerplate.

    [:octicons-arrow-right-24: Quick Start](getting-started/quickstart.md)

-   :material-brain: **Any LLM**

    ---

    OpenAI, Anthropic, Ollama, Together, Qwen — swap providers without changing agent code.

    [:octicons-arrow-right-24: LLM Configuration](configuration/llm.md)

-   :material-message-flash: **Every channel**

    ---

    Telegram, Slack, Discord, WhatsApp, and plain HTTP out of the box.

    [:octicons-arrow-right-24: Channels](channels/index.md)

-   :material-security: **Enterprise-ready auth**

    ---

    JWT, API keys, RBAC roles, credential vault, and per-user rate limits.

    [:octicons-arrow-right-24: Auth](configuration/auth.md)

-   :material-chart-line: **Full observability**

    ---

    OpenTelemetry traces, cost tracking per agent/org, and a built-in dashboard.

    [:octicons-arrow-right-24: Telemetry](configuration/telemetry.md)

-   :material-graph: **Workflow DAGs**

    ---

    Chain agents into multi-step pipelines with typed inputs, outputs, and retries.

    [:octicons-arrow-right-24: Workflows](agents/workflow.md)

</div>

## Install

=== "Homebrew (macOS)"

    ```bash
    brew tap vmodekurti/soulacy
    brew install soulacy
    ```

=== "Docker"

    ```bash
    docker run -v $(pwd)/config.yaml:/app/config.yaml \
      ghcr.io/vmodekurti/soulacy:latest
    ```

=== "Go install"

    ```bash
    go install github.com/soulacy/soulacy/cmd/soulacy@latest
    ```

=== "Binary release"

    Download from [GitHub Releases](https://github.com/vmodekurti/soulacy/releases) and place `soulacy` on your `$PATH`.

## Define an agent

```yaml title="agents/assistant.soul.yaml"
name: assistant
description: A helpful AI assistant
model: gpt-4o-mini
system_prompt: |
  You are a helpful assistant. Be concise and friendly.
channels:
  - telegram
  - http
```

## Run

```bash
soulacy serve --config config.yaml
```

That's it. Your agent is live.

---

!!! tip "New here?"
    Follow the [Quick Start](getting-started/quickstart.md) to have a working agent deployed in under 5 minutes.
