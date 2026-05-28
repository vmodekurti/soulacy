# Installation

## Requirements

| Requirement | Version |
|-------------|---------|
| Go | 1.25+ (only if building from source) |
| Docker | 24+ (optional) |
| OS | macOS 13+, Linux (amd64/arm64), Windows 11 |

---

## Homebrew (macOS — recommended)

```bash
brew tap vmodekurti/soulacy
brew install soulacy
soulacy version
```

---

## Docker

```bash
# Pull latest
docker pull ghcr.io/vmodekurti/soulacy:latest

# Run with your config
docker run -d \
  --name soulacy \
  -p 8080:8080 \
  -v $(pwd)/config.yaml:/app/config.yaml \
  -v $(pwd)/agents:/app/agents \
  ghcr.io/vmodekurti/soulacy:latest
```

Prefer Compose? See the [Docker deployment guide](../deployment/docker.md).

---

## Pre-built binaries

1. Go to [GitHub Releases](https://github.com/vmodekurti/soulacy/releases)
2. Download the archive for your platform:
   - `soulacy_darwin_arm64.tar.gz` — Apple Silicon
   - `soulacy_darwin_amd64.tar.gz` — Intel Mac
   - `soulacy_linux_amd64.tar.gz` — Linux x86-64
   - `soulacy_linux_arm64.tar.gz` — Linux ARM
3. Extract and move to your `$PATH`:

```bash
tar -xzf soulacy_darwin_arm64.tar.gz
sudo mv soulacy /usr/local/bin/
soulacy version
```

---

## Go install

```bash
go install github.com/soulacy/soulacy/cmd/soulacy@latest
```

Ensure `$(go env GOPATH)/bin` is on your `$PATH`.

---

## Build from source

```bash
git clone https://github.com/vmodekurti/soulacy.git
cd soulacy
make build
# binary is at ./bin/soulacy
```

---

## Verify the installation

```bash
soulacy version
# Soulacy v0.1.0 (go1.25 darwin/arm64)
```

---

## What's next?

Follow the [Quick Start](quickstart.md) to create your first agent.
