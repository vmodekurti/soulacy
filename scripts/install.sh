#!/usr/bin/env bash
# Soulacy installer — run with:
#   curl -sSL https://get.soulacy.dev | sh
#
# What this does:
#   1. Detects your OS and architecture
#   2. Downloads the soulacy and sy binaries from GitHub releases
#   3. Installs them to /usr/local/bin
#   4. Creates ~/.soulacy/ with default config, agent dir, and plugin dir
#   5. Optionally installs the Python SDK (pip install soulacy)
#   6. Optionally starts the gateway as a system service
#   7. Opens the GUI in your browser

set -euo pipefail

REPO="soulacy/soulacy"
INSTALL_DIR="/usr/local/bin"
DATA_DIR="${HOME}/.soulacy"
VERSION="${SOULACY_VERSION:-latest}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

banner() {
  echo ""
  echo -e "${BLUE}  ██████╗██╗      █████╗ ██╗    ██╗███████╗████████╗ █████╗  ██████╗██╗  ██╗${NC}"
  echo -e "${BLUE} ██╔════╝██║     ██╔══██╗██║    ██║██╔════╝╚══██╔══╝██╔══██╗██╔════╝██║ ██╔╝${NC}"
  echo -e "${BLUE} ██║     ██║     ███████║██║ █╗ ██║███████╗   ██║   ███████║██║     █████╔╝ ${NC}"
  echo -e "${BLUE} ██║     ██║     ██╔══██║██║███╗██║╚════██║   ██║   ██╔══██║██║     ██╔═██╗ ${NC}"
  echo -e "${BLUE} ╚██████╗███████╗██║  ██║╚███╔███╔╝███████║   ██║   ██║  ██║╚██████╗██║  ██╗${NC}"
  echo -e "${BLUE}  ╚═════╝╚══════╝╚═╝  ╚═╝ ╚══╝╚══╝ ╚══════╝   ╚═╝   ╚═╝  ╚═╝ ╚═════╝╚═╝  ╚═╝${NC}"
  echo ""
  echo -e "  Self-hosted agentic framework — ${GREEN}privacy-first${NC}, ${GREEN}security-first${NC}"
  echo ""
}

log()  { echo -e "${GREEN}▶${NC}  $*"; }
warn() { echo -e "${YELLOW}⚠${NC}  $*"; }
err()  { echo -e "${RED}✗${NC}  $*" >&2; exit 1; }
ok()   { echo -e "${GREEN}✓${NC}  $*"; }

banner

# ── Prerequisites check ───────────────────────────────────────────────────────
log "Checking prerequisites..."

for cmd in curl tar; do
  command -v "$cmd" >/dev/null 2>&1 || err "$cmd is required but not found. Install it and try again."
done

# ── Detect OS and architecture ────────────────────────────────────────────────
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) err "Unsupported architecture: $ARCH" ;;
esac

case "$OS" in
  linux|darwin) ;;
  *) err "Unsupported OS: $OS (Linux and macOS supported)" ;;
esac

log "Detected: ${OS}/${ARCH}"

# ── Resolve version ───────────────────────────────────────────────────────────
if [ "$VERSION" = "latest" ]; then
  log "Resolving latest release..."
  VERSION=$(curl -sf "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/') || VERSION="v0.1.0"
fi
log "Installing Soulacy ${VERSION}"

# ── Download binaries ─────────────────────────────────────────────────────────
# Release tarballs are named: soulacy_<version>_<os>_<arch>.tar.gz
# Each tarball contains both the `soulacy` and `sy` binaries.
BASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"
TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

TARBALL="soulacy_${VERSION}_${OS}_${ARCH}.tar.gz"
URL="${BASE_URL}/${TARBALL}"

log "Downloading ${TARBALL}..."
if curl -fsSL -o "${TMPDIR}/${TARBALL}" "$URL"; then
  tar -xzf "${TMPDIR}/${TARBALL}" -C "$TMPDIR"
else
  warn "Release binary not found at $URL — building from source instead."
  build_from_source
fi

# ── Install binaries ──────────────────────────────────────────────────────────
log "Installing binaries to ${INSTALL_DIR}..."
SUDO=""
if [ ! -w "$INSTALL_DIR" ]; then
  SUDO="sudo"
  warn "Needs sudo to write to ${INSTALL_DIR}"
fi

for binary in soulacy sy; do
  if [ -f "${TMPDIR}/${binary}" ]; then
    $SUDO install -m 755 "${TMPDIR}/${binary}" "${INSTALL_DIR}/${binary}"
    ok "${binary} installed to ${INSTALL_DIR}/${binary}"
  fi
done

# ── Create data directory ─────────────────────────────────────────────────────
log "Creating data directory: ${DATA_DIR}"
mkdir -p \
  "${DATA_DIR}/agents" \
  "${DATA_DIR}/plugins" \
  "${DATA_DIR}/memory" \
  "${DATA_DIR}/tools" \
  "${DATA_DIR}/gui"

# Write default config if none exists
if [ ! -f "${DATA_DIR}/config.yaml" ]; then
  cat > "${DATA_DIR}/config.yaml" << 'EOF'
# Soulacy configuration — edit this file to customise your setup.
# Full reference: https://docs.soulacy.dev/configuration

server:
  host: "127.0.0.1"    # Localhost only by default — change for remote access
  port: 18789
  gui_enabled: true
  api_key: ""          # ⚠ Set this before exposing to a network

llm:
  default_provider: ollama
  providers:
    ollama:
      base_url: "http://localhost:11434"
      model: "llama3"

log:
  level: info
  format: console
EOF
  ok "Default config written to ${DATA_DIR}/config.yaml"
else
  ok "Existing config preserved at ${DATA_DIR}/config.yaml"
fi

# ── Python SDK ────────────────────────────────────────────────────────────────
if command -v pip3 >/dev/null 2>&1; then
  log "Installing Python SDK..."
  pip3 install "soulacy==${VERSION#v}" --quiet 2>/dev/null \
    || warn "Python SDK install failed — install manually with: pip install soulacy"
  ok "Python SDK installed"
else
  warn "pip3 not found — skipping Python SDK. Install with: pip install soulacy"
fi

# ── Ollama check ──────────────────────────────────────────────────────────────
echo ""
if command -v ollama >/dev/null 2>&1; then
  ok "Ollama is installed"
else
  warn "Ollama not found. Soulacy defaults to Ollama for local LLM inference."
  echo "     Install it from: https://ollama.com"
  echo "     Then run: ollama pull llama3"
fi

# ── Done ─────────────────────────────────────────────────────────────────────
echo ""
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${GREEN}  Soulacy ${VERSION} installed successfully!${NC}"
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
echo "  Quick start:"
echo ""
echo -e "  ${BLUE}soulacy${NC}              Start the gateway"
echo -e "  ${BLUE}sy agent list${NC}        List loaded agents"
echo -e "  ${BLUE}sy chat --agent hello-world \"Hi\"${NC}"
echo ""
echo "  GUI: http://localhost:18789  (after starting the gateway)"
echo "  Docs: https://docs.soulacy.dev"
echo "  Config: ${DATA_DIR}/config.yaml"
echo ""
echo "  To start automatically on login, run:"
echo "    soulacy --install-service"
echo ""

# ── Auto-start option ─────────────────────────────────────────────────────────
if [ -t 0 ]; then  # only if running interactively
  read -r -p "Start Soulacy now? [Y/n] " answer
  answer="${answer:-Y}"
  if [[ "$answer" =~ ^[Yy]$ ]]; then
    log "Starting Soulacy gateway..."
    nohup soulacy > "${DATA_DIR}/gateway.log" 2>&1 &
    sleep 2
    if kill -0 $! 2>/dev/null; then
      ok "Gateway started (PID $!)"
      echo "  Log: ${DATA_DIR}/gateway.log"
      # Open browser
      URL="http://localhost:18789"
      if command -v open >/dev/null 2>&1; then
        open "$URL"
      elif command -v xdg-open >/dev/null 2>&1; then
        xdg-open "$URL"
      fi
      echo "  Opening GUI: $URL"
    else
      warn "Gateway may not have started — check ${DATA_DIR}/gateway.log"
    fi
  fi
fi

build_from_source() {
  log "Building from source (requires Go 1.22+)..."
  command -v go >/dev/null 2>&1 || err "Go is required to build from source. Install from https://go.dev/dl/"
  SRCDIR="${TMPDIR}/src"
  git clone --depth 1 "https://github.com/${REPO}.git" "$SRCDIR"
  (cd "$SRCDIR" && make build)
  cp "${SRCDIR}/bin/"* "$TMPDIR/"
}
