# Installation

The fastest path is the one-command installer — it brings its own dependencies.

## One command (macOS / Linux — recommended)

```bash
curl -fsSL https://vmodekurti.github.io/soulacy/install.sh | bash
```

What it does:

1. Detects your OS/architecture (macOS & Linux, amd64 & arm64).
2. Downloads the latest release binaries — or, if no release is published yet,
   **builds from source automatically**, fetching private copies of Go and
   Node into `~/.soulacy/toolchain` when they're missing (no Homebrew, no
   system package changes).
3. Installs `soulacy` (the gateway) and `sy` (the CLI) to `/usr/local/bin`.
4. Creates your workspace at `~/.soulacy/soulspace` with a default config.
5. Offers to install [Ollama](https://ollama.com) and pull `llama3` so you
   have a local LLM out of the box.
6. Offers to start the gateway and open the GUI at `http://localhost:18789`.

!!! tip "Pin a version"
    `SOULACY_VERSION=v0.2.0 curl -fsSL https://vmodekurti.github.io/soulacy/install.sh | bash`

## Requirements

| | |
|---|---|
| OS | macOS 13+ or Linux (amd64 / arm64) |
| Tools | `curl` and `tar` — everything else is installed automatically |
| LLM | Ollama (local, free) **or** an OpenAI / Anthropic / Gemini API key |

## Build from source

```bash
git clone https://github.com/vmodekurti/soulacy.git
cd soulacy
make all          # GUI + gateway + CLI → ./bin/soulacy and ./bin/sy
sudo install -m755 bin/soulacy bin/sy /usr/local/bin/
```

`make all` needs Go 1.25+ and Node 18+ on your PATH (`make build` alone skips
the GUI — the binary embeds the web UI at compile time, so use `make all`).

## Docker

From a checkout (works today, builds the image locally):

```bash
git clone https://github.com/vmodekurti/soulacy.git && cd soulacy
docker compose up --build -d
```

The gateway listens on **18789**; state persists in the
`/home/soulacy/.soulacy` volume:

```bash
docker run -d --name soulacy \
  -p 18789:18789 \
  -v soulacy-data:/home/soulacy/.soulacy \
  ghcr.io/vmodekurti/soulacy:latest   # published with tagged releases
```

More (Compose details, reverse proxies): [Docker deployment guide](../deployment/docker.md).

## Pre-built binaries

Tagged releases publish `soulacy_<version>_<os>_<arch>.tar.gz` bundles
(each contains both `soulacy` and `sy`) on
[GitHub Releases](https://github.com/vmodekurti/soulacy/releases):

```bash
grep 'soulacy_v0.2.0_darwin_arm64.tar.gz' SHA256SUMS | shasum -a 256 -c -
tar -xzf soulacy_v0.2.0_darwin_arm64.tar.gz
sudo install -m755 soulacy sy /usr/local/bin/
```

Releases also include `release-manifest.json`, which records the release
version, source commit, generation time, and every artifact's OS, architecture,
byte size, and SHA-256 digest for installer and CI checks.

If the releases page is empty, use the one-command installer above — it
falls back to a source build automatically.

## Verify

```bash
sy version         # CLI + framework version
sy doctor          # checks workspace, config, providers, and the gateway
```

(`soulacy` itself takes no `version` subcommand — running it starts the gateway.)

## What's next?

1. `sy onboard` — the guided first-run path for provider, search, starter agent, update manifest, and auto-start.
2. Follow the [Quick Start](quickstart.md), then take the [GUI tour](gui-tour.md).
