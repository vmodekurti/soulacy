# Soulacy

**One binary. YAML agents. Runs anywhere — no cloud required.**

Soulacy is a self-hosted AI agent runtime. Write an agent in a single YAML file, point it at any LLM (Ollama, OpenAI, Anthropic, Groq, or anything OpenAI-compatible), and run it from a terminal or a $5 VPS with no infrastructure setup, no Docker orchestration, and no cloud dependency.

Think of it as Ollama — but for agents.

[![CI](https://github.com/vmodekurti/soulacy/actions/workflows/ci.yml/badge.svg)](https://github.com/vmodekurti/soulacy/actions/workflows/ci.yml)
[![Release](https://github.com/vmodekurti/soulacy/actions/workflows/release.yml/badge.svg)](https://github.com/vmodekurti/soulacy/releases/latest)
[![Docker](https://img.shields.io/badge/ghcr.io-vmodekurti%2Fsoulacy-blue?logo=docker)](https://github.com/vmodekurti/soulacy/pkgs/container/soulacy)
[![License: Apache 2.0](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)

## Why Soulacy

| | Soulacy | n8n / Flowise / Dify | LangGraph / AutoGen |
|---|---|---|---|
| **Deploy** | Single binary, zero deps | Docker + Postgres + Redis | Python package |
| **Config** | One YAML file per agent | Visual editor (brittle exports) | Code |
| **Runs on** | Laptop, VPS, Raspberry Pi | Needs a server stack | Dev machine |
| **LLM** | Any — local or cloud | Mostly cloud | Any |
| **No-code** | GUI included in binary | Yes | No |

The field is crowded with frameworks that assume you want to write Python and deploy to the cloud. Soulacy is for people who want agents that just run — the same way you `ollama run llama3`.

---

### Install

### One line — macOS & Linux

```bash
curl -fsSL https://raw.githubusercontent.com/vmodekurti/soulacy/main/install.sh | bash
```

What it does, with zero questions asked:

1. Downloads the matching release tarball when one exists; otherwise builds from source (requires `git`, Go 1.25+, and `npm` for the GUI build).
2. Builds the Svelte GUI and compiles `soulacy` (the gateway) + `sy` (the CLI).
3. Installs both binaries into `~/.local/bin` (no `sudo`).
4. Prints clear next steps + offers to launch the gateway on the spot.

When you run `soulacy serve` (either right away or later), the gateway prints a one-time banner with the URL and a freshly-generated API key. Then open <http://127.0.0.1:18789>, paste the key, and you're in. The runtime workspace (`~/.soulacy/soulspace/`), config file, starter agent, and API key are all created automatically on first launch — you never have to touch a config file.

Overrides:

```bash
# install from a branch / tag / sha
SOULACY_REF=feature/integrated-roadmap curl -fsSL https://raw.githubusercontent.com/vmodekurti/soulacy/feature/integrated-roadmap/install.sh | bash

# install system-wide (uses sudo)
SOULACY_PREFIX=/usr/local curl -fsSL https://raw.githubusercontent.com/vmodekurti/soulacy/main/install.sh | bash
```

### From a local checkout

```bash
git clone https://github.com/vmodekurti/soulacy
cd soulacy
./install.sh                  # same behavior; will offer LaunchAgent setup on macOS
```

### Docker — full stack (Postgres + Qdrant + GUI)

```bash
curl -O https://raw.githubusercontent.com/vmodekurti/soulacy/main/docker-compose.yml
curl -O https://raw.githubusercontent.com/vmodekurti/soulacy/main/.env.example
cp .env.example .env   # edit your LLM key and API key
docker compose up
```

Open [http://localhost:18789](http://localhost:18789).

### Docker — lightweight (SQLite, zero dependencies)

```bash
docker run -p 18789:18789 \
  -v ~/.soulacy:/home/soulacy/.soulacy \
  ghcr.io/vmodekurti/soulacy:latest
```

### Python SDK (experimental)

> **Experimental, not yet published to PyPI.** The Python SDK lives in
> [`sdk/python`](sdk/python) and currently has no test coverage. Install it
> from source until a release is published:
>
> ```bash
> pip install ./sdk/python
> ```

---

## Quick start

```bash
# List loaded agents
sy agent list

# Chat with an agent
sy chat --agent system "Hello!"

# Check gateway status
sy server status

# Validate an agent definition, including provider/model fit
sy agent validate examples/agents/hello-world/SOUL.yaml

# Run local diagnostics
sy doctor

# Stream live event log
sy logs --follow
```

---

## Configuration

Default config is created at `~/.soulacy/config.yaml` on first run.

```yaml
server:
  host: "127.0.0.1"
  port: 18789
  api_key: ""          # set to protect the gateway

llm:
  default_provider: ollama
  providers:
    ollama:
      base_url: "http://localhost:11434"
      model: "llama3"
```

All fields can be overridden with environment variables: `SOULACY_<SECTION>_<KEY>`.  
Full reference: [docs/configuration/index.md](docs/configuration/index.md)

---

## What an agent looks like

```yaml
# ~/.soulacy/agents/daily-briefing/SOUL.yaml
id: daily-briefing
name: Daily Briefing
trigger: cron
schedule:
  cron: "0 7 * * *"   # 7 AM every day
  output:
    channel: "telegram-daily-briefing"
    to: "123456789"
    bot_name: "Daily Briefing Bot"

llm:
  provider: ollama
  model: llama3.3:70b

tools:
  - name: get_weather
    python_file: tools/get_weather.py
    parameters:
      type: object
      properties:
        location: { type: string }
      required: [location]

system_prompt: |
  Every morning, fetch the weather for Chicago and send a
  2-sentence briefing to Telegram.
```

That's it. No boilerplate, no decorators, no SDK imports. Drop the file in, and Soulacy picks it up without a restart.

---

## Architecture

```
┌─────────────────────────────────────────────┐
│                  Gateway                     │
│  REST API  ·  WebSocket  ·  Embedded GUI     │
└──────────┬──────────────────────┬────────────┘
           │                      │
    ┌──────▼──────┐        ┌──────▼──────┐
    │   Runtime   │        │   Channels   │
    │  (engine)   │        │  Slack/TG/   │
    └──────┬──────┘        │  Discord/HTTP│
           │               └─────────────┘
    ┌──────▼──────────────────────────────┐
    │              Storage                │
    │  SQLite (default)  ·  Postgres      │
    │  sqlite-vec        ·  Qdrant        │
    └─────────────────────────────────────┘
```

---

## Development

```bash
git clone https://github.com/vmodekurti/soulacy
cd soulacy
make all          # builds GUI + Go binaries
make install      # installs to /usr/local/bin
make test         # runs Go tests
make docker-up    # starts full stack in Docker
```

See [docs/FRAMEWORK_OVERVIEW.md](docs/FRAMEWORK_OVERVIEW.md) for architecture details.

---

## License

Apache 2.0 — see [LICENSE](LICENSE).
