# Soulacy

**One binary. YAML agents. Runs anywhere — no cloud required.**

Soulacy is a self-hosted AI agent runtime. Write an agent in a single YAML file, point it at any LLM (Ollama, OpenAI, Anthropic, Groq, or anything OpenAI-compatible), and run it from a terminal or a $5 VPS with no infrastructure setup, no Docker orchestration, and no cloud dependency.

Think of it as Ollama — but for agents.

[![CI](https://github.com/soulacy/soulacy/actions/workflows/ci.yml/badge.svg)](https://github.com/soulacy/soulacy/actions/workflows/ci.yml)
[![Release](https://github.com/soulacy/soulacy/actions/workflows/release.yml/badge.svg)](https://github.com/soulacy/soulacy/releases/latest)
[![Docker](https://ghcr.io/soulacy/soulacy)](https://ghcr.io/soulacy/soulacy)
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

### macOS — one line

```bash
brew install soulacy/tap/soulacy
```

Then start the gateway and open the dashboard:

```bash
brew services start soulacy
open http://localhost:18789
```

### macOS — from source

```bash
git clone https://github.com/soulacy/soulacy
cd soulacy
bash scripts/mac-install.sh
```

Builds the GUI, compiles the binaries, installs to `/usr/local/bin`, registers a LaunchAgent so it starts on login, and opens the browser.

### Linux — one line

```bash
curl -sSL https://raw.githubusercontent.com/soulacy/soulacy/main/scripts/install.sh | bash
```

Downloads the pre-built binary for your platform, installs it, and registers a `systemd` service that starts on boot.

### Docker — full stack (Postgres + Qdrant + GUI)

```bash
curl -O https://raw.githubusercontent.com/soulacy/soulacy/main/docker-compose.yml
curl -O https://raw.githubusercontent.com/soulacy/soulacy/main/.env.example
cp .env.example .env   # edit your LLM key and API key
docker compose up
```

Open [http://localhost:18789](http://localhost:18789).

### Docker — lightweight (SQLite, zero dependencies)

```bash
docker run -p 18789:18789 \
  -v ~/.soulacy:/home/soulacy/.soulacy \
  ghcr.io/soulacy/soulacy:latest
```

### Python SDK

```bash
pip install soulacy
```

---

## Quick start

```bash
# List loaded agents
sy agent list

# Chat with an agent
sy chat --agent hello-world "Hello!"

# Check gateway status
sy server status

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
Full reference: [docs/configuration.md](docs/configuration.md)

---

## What an agent looks like

```yaml
# ~/.soulacy/agents/daily-briefing/SOUL.yaml
id: daily-briefing
name: Daily Briefing
trigger: cron
schedule:
  cron: "0 7 * * *"   # 7 AM every day

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
git clone https://github.com/soulacy/soulacy
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
