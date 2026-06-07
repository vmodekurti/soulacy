#!/usr/bin/env bash
# Soulacy installer — run with:
#   curl -fsSL https://vmodekurti.github.io/soulacy/install.sh | bash
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

REPO="vmodekurti/soulacy"
INSTALL_DIR="/usr/local/bin"
DATA_DIR="${HOME}/.soulacy/soulspace"
VERSION="${SOULACY_VERSION:-latest}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

banner() {
  echo ""
  echo -e "${BLUE} ███████╗ ██████╗ ██╗   ██╗██╗      █████╗  ██████╗██╗   ██╗${NC}"
  echo -e "${BLUE} ██╔════╝██╔═══██╗██║   ██║██║     ██╔══██╗██╔════╝╚██╗ ██╔╝${NC}"
  echo -e "${BLUE} ███████╗██║   ██║██║   ██║██║     ███████║██║      ╚████╔╝ ${NC}"
  echo -e "${BLUE} ╚════██║██║   ██║██║   ██║██║     ██╔══██║██║       ╚██╔╝  ${NC}"
  echo -e "${BLUE} ███████║╚██████╔╝╚██████╔╝███████╗██║  ██║╚██████╗   ██║   ${NC}"
  echo -e "${BLUE} ╚══════╝ ╚═════╝  ╚═════╝ ╚══════╝╚═╝  ╚═╝ ╚═════╝   ╚═╝   ${NC}"
  echo ""
  echo -e "  Self-hosted agent runtime — ${GREEN}one binary${NC}, ${GREEN}YAML agents${NC}, ${GREEN}no cloud required${NC}"
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

# ── Self-contained toolchain ──────────────────────────────────────────────────
# When Go or Node are missing we install them PRIVATELY under
# ~/.soulacy/toolchain (official upstream tarballs) — nothing touches the
# system package manager and nothing else on the machine changes.
TOOLCHAIN="${HOME}/.soulacy/toolchain"
GO_VERSION="1.25.0"
NODE_VERSION="22.12.0"

ensure_git() {
  command -v git >/dev/null 2>&1 && return 0
  if [ "$OS" = "darwin" ]; then
    warn "git not found — triggering the Xcode Command Line Tools install."
    xcode-select --install 2>/dev/null || true
    err "Re-run this installer after the Command Line Tools finish installing."
  fi
  err "git is required. Install it (e.g. apt install git / dnf install git) and re-run."
}

ensure_go() {
  if command -v go >/dev/null 2>&1; then return 0; fi
  if [ -x "${TOOLCHAIN}/go/bin/go" ]; then
    export PATH="${TOOLCHAIN}/go/bin:${PATH}"
    return 0
  fi
  log "Go not found — installing Go ${GO_VERSION} to ${TOOLCHAIN} (private, no system changes)..."
  mkdir -p "$TOOLCHAIN"
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.${OS}-${ARCH}.tar.gz" \
    | tar -xz -C "$TOOLCHAIN"
  export PATH="${TOOLCHAIN}/go/bin:${PATH}"
  ok "Go $(go version | awk '{print $3}') ready"
}

ensure_node() {
  if command -v npm >/dev/null 2>&1; then return 0; fi
  # Node names x86_64 "x64", not "amd64".
  local node_arch="$ARCH"
  [ "$node_arch" = "amd64" ] && node_arch="x64"
  local node_dir="${TOOLCHAIN}/node-v${NODE_VERSION}-${OS}-${node_arch}"
  if [ ! -x "${node_dir}/bin/npm" ]; then
    log "Node not found — installing Node ${NODE_VERSION} to ${TOOLCHAIN} (private, no system changes)..."
    mkdir -p "$TOOLCHAIN"
    curl -fsSL "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-${OS}-${node_arch}.tar.gz" \
      | tar -xz -C "$TOOLCHAIN"
  fi
  export PATH="${node_dir}/bin:${PATH}"
  ok "Node $(node --version) ready"
}

# ── Source-build fallback (used when no release tarball exists) ──────────────
build_from_source() {
  log "Building from source — missing dependencies are installed automatically."
  ensure_git
  ensure_go
  ensure_node
  SRCDIR="${TMPDIR}/src"
  git clone --depth 1 "https://github.com/${REPO}.git" "$SRCDIR"
  (cd "$SRCDIR" && make all)   # GUI dist + soulacy + sy
  cp "${SRCDIR}/bin/soulacy" "${SRCDIR}/bin/sy" "$TMPDIR/"
}

# ── Resolve version ───────────────────────────────────────────────────────────
TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

if [ "$VERSION" = "latest" ]; then
  log "Resolving latest release..."
  VERSION=$(curl -sf "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/' || true)
fi

if [ -z "$VERSION" ] || [ "$VERSION" = "latest" ]; then
  warn "No published release found — building from source."
  VERSION="dev"
  build_from_source
else
  # ── Download binaries ───────────────────────────────────────────────────────
  # Release tarballs are named: soulacy_<version>_<os>_<arch>.tar.gz
  # Each tarball contains both the `soulacy` and `sy` binaries.
  log "Installing Soulacy ${VERSION}"
  TARBALL="soulacy_${VERSION}_${OS}_${ARCH}.tar.gz"
  URL="https://github.com/${REPO}/releases/download/${VERSION}/${TARBALL}"
  log "Downloading ${TARBALL}..."
  if curl -fsSL -o "${TMPDIR}/${TARBALL}" "$URL"; then
    tar -xzf "${TMPDIR}/${TARBALL}" -C "$TMPDIR"
  else
    warn "Release binary not found at $URL — building from source instead."
    build_from_source
  fi
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

# ── Create workspace (soulspace layout) ──────────────────────────────────────
# NOTE: paths live under ~/.soulacy/soulspace — creating files directly in
# ~/.soulacy would be detected as a pre-soulspace LEGACY install.
log "Creating workspace: ${DATA_DIR}"
mkdir -p \
  "${DATA_DIR}/agents" \
  "${DATA_DIR}/plugins" \
  "${DATA_DIR}/memory" \
  "${DATA_DIR}/tools" \
  "${DATA_DIR}/skills"

# Write default config if none exists
if [ ! -f "${DATA_DIR}/config.yaml" ]; then
  cat > "${DATA_DIR}/config.yaml" << 'EOF'
# Soulacy configuration — edit this file to customise your setup.
# Full reference: https://vmodekurti.github.io/soulacy/configuration/

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

# ── Ollama (default local LLM) ────────────────────────────────────────────────
echo ""
if command -v ollama >/dev/null 2>&1; then
  ok "Ollama is installed"
else
  warn "Ollama not found. Soulacy defaults to Ollama for local LLM inference."
  install_ollama="n"
  if [ -t 0 ]; then
    read -r -p "Install Ollama now? [Y/n] " install_ollama
    install_ollama="${install_ollama:-Y}"
  fi
  if [[ "$install_ollama" =~ ^[Yy]$ ]]; then
    if [ "$OS" = "darwin" ] && command -v brew >/dev/null 2>&1; then
      brew install ollama && ok "Ollama installed (brew)" || warn "Ollama install failed — get it from https://ollama.com"
    else
      curl -fsSL https://ollama.com/install.sh | sh && ok "Ollama installed" \
        || warn "Ollama install failed — get it from https://ollama.com"
    fi
    command -v ollama >/dev/null 2>&1 && { log "Pulling llama3 (the default model)..."; ollama pull llama3 || warn "Pull failed — run 'ollama pull llama3' later."; }
  else
    echo "     Install later from: https://ollama.com  — then: ollama pull llama3"
    echo "     (Or point Soulacy at OpenAI/Anthropic/Gemini in 'sy setup'.)"
  fi
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
echo "  Docs: https://vmodekurti.github.io/soulacy"
echo "  Config: ${DATA_DIR}/config.yaml"
echo ""
echo -e "  Next: run ${BLUE}sy setup${NC} for the interactive provider/channel wizard."
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
