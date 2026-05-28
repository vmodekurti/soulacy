# Soulacy

**Self-hosted agentic AI framework.** Define agents in YAML, connect them to any LLM, run them locally with full control over your data.

[![CI](https://github.com/soulacy/soulacy/actions/workflows/ci.yml/badge.svg)](https://github.com/soulacy/soulacy/actions/workflows/ci.yml)
[![Release](https://github.com/soulacy/soulacy/actions/workflows/release.yml/badge.svg)](https://github.com/soulacy/soulacy/releases/latest)
[![Docker](https://ghcr.io/soulacy/soulacy)](https://ghcr.io/soulacy/soulacy)
[![License: Apache 2.0](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)

---

## Install

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
