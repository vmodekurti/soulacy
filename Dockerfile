# syntax=docker/dockerfile:1.6
#
# Dockerfile — production image for running Soulacy via docker compose.
#
# Stages:
#   gui      → builds the Svelte dashboard with Node 20
#   gobuild  → compiles soulacy + sy with cgo (sqlite-vec, mattn/go-sqlite3)
#   runtime  → slim Debian image with Python 3 + the SDK + both binaries
#
# Usage (standalone):
#   docker build -t soulacy .
#   docker run -p 18789:18789 -v ~/.soulacy:/home/soulacy/.soulacy soulacy
#
# Usage (full stack):
#   docker compose up   ← starts gateway + Postgres + Qdrant

# ── Stage 1: GUI ─────────────────────────────────────────────────────────────
FROM node:20-bookworm-slim AS gui
WORKDIR /src/gui
COPY gui/package.json gui/package-lock.json* ./
RUN --mount=type=cache,target=/root/.npm \
    npm install --no-audit --no-fund --silent
COPY gui ./
RUN npm run build
# Output: /src/gui/dist  (copied to /src/internal/webui/dist in gobuild)

# ── Stage 2: Go binary ───────────────────────────────────────────────────────
FROM golang:1.24-bookworm AS gobuild
ARG VERSION=dev
WORKDIR /src

RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential pkg-config libsqlite3-dev ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
# Inject the Svelte build so the gateway binary embeds the GUI.
COPY --from=gui /src/gui/dist /src/internal/webui/dist

ENV CGO_ENABLED=1
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go build \
        -ldflags "-X github.com/soulacy/soulacy/internal/config.Version=${VERSION}" \
        -o /out/soulacy ./cmd/soulacy \
    && go build \
        -ldflags "-X github.com/soulacy/soulacy/internal/config.Version=${VERSION}" \
        -o /out/sy ./cmd/sy

# ── Stage 3: Runtime ─────────────────────────────────────────────────────────
FROM debian:bookworm-slim AS runtime

RUN apt-get update && apt-get install -y --no-install-recommends \
        libsqlite3-0 ca-certificates python3 python3-pip curl \
    && rm -rf /var/lib/apt/lists/*

# Python SDK — agents written in Python work without any extra setup.
# Failure is non-fatal: the gateway works fine without it.
RUN pip3 install --break-system-packages soulacy 2>/dev/null || true

RUN useradd --create-home --shell /usr/sbin/nologin soulacy

COPY --from=gobuild --chown=soulacy /out/soulacy /usr/local/bin/soulacy
COPY --from=gobuild --chown=soulacy /out/sy      /usr/local/bin/sy

# Data directory — mount a volume here to persist agents, memory, and logs.
RUN mkdir -p /home/soulacy/.soulacy && chown soulacy:soulacy /home/soulacy/.soulacy
VOLUME ["/home/soulacy/.soulacy"]

USER soulacy
WORKDIR /home/soulacy

ENV SOULACY_CONFIG_PATH=/home/soulacy/.soulacy/config.yaml

EXPOSE 18789

HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -fs http://localhost:18789/api/v1/health || exit 1

ENTRYPOINT ["soulacy"]
CMD ["serve"]
