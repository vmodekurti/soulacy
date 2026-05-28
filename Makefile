BINARY_GATEWAY := soulacy
BINARY_CLI     := sy
VERSION        ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS        := -ldflags "-X github.com/soulacy/soulacy/internal/config.Version=$(VERSION)"

.PHONY: all build build-gateway build-cli gui install clean test lint dev sdk-install tidy \
        docker-up docker-down docker-up-lite docker-build docker-push \
        release release-linux release-linux-amd64 release-linux-arm64 \
        release-darwin release-darwin-arm64 release-darwin-amd64 \
        service-install service-uninstall help

## Full build: GUI then Go binaries
all: gui build

## Build only the Go binaries (embeds whatever is in internal/webui/dist/).
## `go mod tidy` runs first so go.sum picks up any newly-added deps.
build: deps build-gateway build-cli

## Ensure module deps are fetched and go.sum is consistent.
deps:
	@echo "→ Ensuring go.mod/go.sum are in sync..."
	go mod tidy

## Build the Svelte GUI → outputs to internal/webui/dist/ (embedded into the binary)
gui:
	@echo "→ Building GUI (Svelte)..."
	@command -v npm >/dev/null 2>&1 || { echo "npm not found — install Node.js from https://nodejs.org"; exit 1; }
	cd gui && npm install --silent && npm run build
	@echo "→ GUI built."

build-gateway:
	@echo "→ Building gateway server..."
	CGO_ENABLED=1 go build $(LDFLAGS) -o bin/$(BINARY_GATEWAY) ./cmd/soulacy

build-cli:
	@echo "→ Building CLI..."
	CGO_ENABLED=1 go build $(LDFLAGS) -o bin/$(BINARY_CLI) ./cmd/sy

## Install both binaries to /usr/local/bin
install: build
	@echo "→ Installing to /usr/local/bin..."
	cp bin/$(BINARY_GATEWAY) /usr/local/bin/$(BINARY_GATEWAY)
	cp bin/$(BINARY_CLI) /usr/local/bin/$(BINARY_CLI)
	@echo "✓ soulacy and sy installed"

## Install Python SDK in editable mode (development)
sdk-install:
	pip3 install -e sdk/python

## Install Python SDK from PyPI
sdk-install-release:
	pip3 install soulacy

## Run gateway in dev mode (auto-restart on changes with Air)
dev:
	@which air > /dev/null 2>&1 || go install github.com/cosmtrek/air@latest
	air -c .air.toml

## Run tests
test:
	go test ./... -v -timeout 30s

## Lint
lint:
	@which golangci-lint > /dev/null 2>&1 || curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(go env GOPATH)/bin
	golangci-lint run

## Tidy modules
tidy:
	go mod tidy

clean:
	rm -rf bin/

# ──────────────────────────────────────────────────────────────────────────────
# Docker Compose — one-command full-stack deployment
# ──────────────────────────────────────────────────────────────────────────────

DOCKER         ?= docker
COMPOSE        ?= docker compose

## Start full stack (gateway + Postgres + Qdrant) — first run builds the image
docker-up:
	@echo "→ Starting full stack (gateway + Postgres + Qdrant)..."
	$(COMPOSE) up --build -d
	@echo "✓ Soulacy running at http://localhost:18789"

## Start lightweight stack (gateway + SQLite only, no external dependencies)
docker-up-lite:
	@echo "→ Starting lite stack (gateway + SQLite only)..."
	$(COMPOSE) -f docker-compose.lite.yml up --build -d
	@echo "✓ Soulacy running at http://localhost:18789"

## Stop all compose services
docker-down:
	$(COMPOSE) down

## Build the Docker image without starting
docker-build:
	$(COMPOSE) build

## Push image to registry (set REGISTRY env var)
docker-push:
	$(COMPOSE) push

# ──────────────────────────────────────────────────────────────────────────────
# System service — register soulacy as an OS service (Mac LaunchAgent / Linux systemd)
# ──────────────────────────────────────────────────────────────────────────────

OS := $(shell uname -s)

## Install soulacy as a system service (starts on login/boot)
service-install: install
ifeq ($(OS),Darwin)
	@echo "→ Installing Mac LaunchAgent..."
	@mkdir -p ~/Library/LaunchAgents
	@sed "s|__INSTALL_DIR__|/usr/local/bin|g" scripts/com.soulacy.gateway.plist \
	    > ~/Library/LaunchAgents/com.soulacy.gateway.plist
	launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.soulacy.gateway.plist 2>/dev/null || \
	launchctl load -w ~/Library/LaunchAgents/com.soulacy.gateway.plist
	@echo "✓ Soulacy LaunchAgent installed — starts automatically on login"
else
	@echo "→ Installing systemd service..."
	@sed "s|__INSTALL_DIR__|/usr/local/bin|g" scripts/soulacy.service \
	    | sudo tee /etc/systemd/system/soulacy.service > /dev/null
	sudo systemctl daemon-reload
	sudo systemctl enable --now soulacy
	@echo "✓ Soulacy systemd service installed and started"
endif

## Remove soulacy system service
service-uninstall:
ifeq ($(OS),Darwin)
	launchctl unload ~/Library/LaunchAgents/com.soulacy.gateway.plist 2>/dev/null || true
	rm -f ~/Library/LaunchAgents/com.soulacy.gateway.plist
	@echo "✓ LaunchAgent removed"
else
	sudo systemctl disable --now soulacy 2>/dev/null || true
	sudo rm -f /etc/systemd/system/soulacy.service
	sudo systemctl daemon-reload
	@echo "✓ systemd service removed"
endif

# ──────────────────────────────────────────────────────────────────────────────
# Release pipeline — produces platform-tagged binaries under bin/release/.
# Linux targets build inside a Docker container so cgo+sqlite-vec doesn't
# need a cross-toolchain on the host. Darwin targets build natively.
# ──────────────────────────────────────────────────────────────────────────────

RELEASE_DIR    := bin/release
RELEASE_IMAGE  := soulacy-release
DOCKER_BUILDKIT_FLAGS := --load

## Build every supported target (Linux amd64+arm64). Skip Darwin here;
## it requires a macOS host — use `make release-darwin` on macOS.
release: release-linux

## All Linux targets.
release-linux: release-linux-amd64 release-linux-arm64

release-linux-amd64:
	@echo "→ Release: linux/amd64 (cgo, sqlite-vec) via Dockerfile.release"
	@mkdir -p $(RELEASE_DIR)
	DOCKER_BUILDKIT=1 $(DOCKER) buildx build \
		--platform linux/amd64 \
		--build-arg VERSION=$(VERSION) \
		-f Dockerfile.release \
		-t $(RELEASE_IMAGE):linux-amd64 \
		$(DOCKER_BUILDKIT_FLAGS) .
	@$(DOCKER) rm -f soulacy-extract-amd64 >/dev/null 2>&1 || true
	$(DOCKER) create --name soulacy-extract-amd64 $(RELEASE_IMAGE):linux-amd64 >/dev/null
	$(DOCKER) cp soulacy-extract-amd64:/out/soulacy $(RELEASE_DIR)/soulacy-linux-amd64
	$(DOCKER) cp soulacy-extract-amd64:/out/sy      $(RELEASE_DIR)/sy-linux-amd64
	$(DOCKER) rm soulacy-extract-amd64 >/dev/null
	@echo "✓ $(RELEASE_DIR)/soulacy-linux-amd64"

release-linux-arm64:
	@echo "→ Release: linux/arm64 (cgo, sqlite-vec) via Dockerfile.release"
	@mkdir -p $(RELEASE_DIR)
	DOCKER_BUILDKIT=1 $(DOCKER) buildx build \
		--platform linux/arm64 \
		--build-arg VERSION=$(VERSION) \
		-f Dockerfile.release \
		-t $(RELEASE_IMAGE):linux-arm64 \
		$(DOCKER_BUILDKIT_FLAGS) .
	@$(DOCKER) rm -f soulacy-extract-arm64 >/dev/null 2>&1 || true
	$(DOCKER) create --name soulacy-extract-arm64 $(RELEASE_IMAGE):linux-arm64 >/dev/null
	$(DOCKER) cp soulacy-extract-arm64:/out/soulacy $(RELEASE_DIR)/soulacy-linux-arm64
	$(DOCKER) cp soulacy-extract-arm64:/out/sy      $(RELEASE_DIR)/sy-linux-arm64
	$(DOCKER) rm soulacy-extract-arm64 >/dev/null
	@echo "✓ $(RELEASE_DIR)/soulacy-linux-arm64"

## Darwin targets — built natively; macOS + Xcode required.
release-darwin: release-darwin-arm64

release-darwin-arm64:
	@echo "→ Release: darwin/arm64 (native cgo build)"
	@mkdir -p $(RELEASE_DIR)
	CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
		go build $(LDFLAGS) -o $(RELEASE_DIR)/soulacy-darwin-arm64 ./cmd/soulacy
	CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
		go build $(LDFLAGS) -o $(RELEASE_DIR)/sy-darwin-arm64 ./cmd/sy
	@echo "✓ $(RELEASE_DIR)/soulacy-darwin-arm64"

release-darwin-amd64:
	@echo "→ Release: darwin/amd64 (native cgo build)"
	@mkdir -p $(RELEASE_DIR)
	CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 \
		go build $(LDFLAGS) -o $(RELEASE_DIR)/soulacy-darwin-amd64 ./cmd/soulacy
	CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 \
		go build $(LDFLAGS) -o $(RELEASE_DIR)/sy-darwin-amd64 ./cmd/sy
	@echo "✓ $(RELEASE_DIR)/soulacy-darwin-amd64"

## Show help
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //' | column -t -s '—'
	@echo ""
	@echo "Quick start:"
	@echo "  make all              Build GUI + binaries locally"
	@echo "  make install          Install to /usr/local/bin"
	@echo "  make docker-up        Start full stack in Docker"
	@echo "  make docker-up-lite   Start SQLite-only stack in Docker"
	@echo "  make service-install  Register as OS service (auto-start)"
