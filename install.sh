#!/bin/bash
# Soulacy installer — interactive, one-line install:
#   curl -fsSL https://raw.githubusercontent.com/your-org/soulacy/main/install.sh | bash
# Or run locally from the repo:
#   ./install.sh
set -e

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUNTIME_DIR="$HOME/.soulacy"
LAUNCHAGENT_LABEL="com.soulacy.soulacy"
LAUNCHAGENT_PLIST="$HOME/Library/LaunchAgents/${LAUNCHAGENT_LABEL}.plist"

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; NC='\033[0m'; BOLD='\033[1m'
ok()   { echo -e "${GREEN}✓${NC} $*"; }
warn() { echo -e "${YELLOW}⚠${NC}  $*"; }
err()  { echo -e "${RED}✗${NC} $*"; exit 1; }
hdr()  { echo -e "\n${BOLD}$*${NC}"; }

echo ""
echo -e "${BOLD}  Soulacy Installer${NC}"
echo "  ────────────────────────────────────────"
echo ""

# ── 1. Choose binary install location ─────────────────────────────────────────
hdr "Step 1: Binary install location"
echo "  [1] /usr/local/bin    (system-wide, requires sudo)  ← recommended"
echo "  [2] /opt/homebrew/bin (Homebrew prefix, requires sudo)"
echo "  [3] ~/.local/bin      (user-only, no sudo)"
echo "  [4] Custom path"
echo ""
read -r -p "  Choose [1-4, default 1]: " BIN_CHOICE
BIN_CHOICE="${BIN_CHOICE:-1}"

case "$BIN_CHOICE" in
    1) BIN_DIR="/usr/local/bin" ;;
    2) BIN_DIR="/opt/homebrew/bin" ;;
    3) BIN_DIR="$HOME/.local/bin"; mkdir -p "$BIN_DIR" ;;
    4) read -r -p "  Enter path: " BIN_DIR; mkdir -p "$BIN_DIR" ;;
    *) err "Invalid choice" ;;
esac

SOULACY_BIN="$BIN_DIR/soulacy"
SY_BIN="$BIN_DIR/sy"
ok "Will install to: $BIN_DIR"

# ── 2. Check PATH ──────────────────────────────────────────────────────────────
if ! echo "$PATH" | tr ':' '\n' | grep -qx "$BIN_DIR"; then
    warn "$BIN_DIR is not in your PATH."
    echo "  Add this to your ~/.zshrc or ~/.bashrc:"
    echo "    export PATH=\"$BIN_DIR:\$PATH\""
fi

# ── 3. Build or use existing binary ───────────────────────────────────────────
hdr "Step 2: Binary"
if [ -f "$REPO_DIR/bin/soulacy" ]; then
    echo "  Pre-built binary found at $REPO_DIR/bin/soulacy."
    read -r -p "  Rebuild from source? [y/N]: " REBUILD
    if [[ "$REBUILD" =~ ^[Yy]$ ]]; then
        echo "  Building..."
        cd "$REPO_DIR" && make build
    fi
    BUILT_BIN="$REPO_DIR/bin/soulacy"
    BUILT_SY="$REPO_DIR/bin/sy"
else
    echo "  No pre-built binary found — building from source..."
    command -v go >/dev/null 2>&1 || err "Go is not installed. Install from https://go.dev/dl/"
    cd "$REPO_DIR" && make build
    BUILT_BIN="$REPO_DIR/bin/soulacy"
    BUILT_SY="$REPO_DIR/bin/sy"
fi

# Install binary
if [[ "$BIN_DIR" == /usr/local/bin || "$BIN_DIR" == /opt/homebrew/bin ]]; then
    sudo cp "$BUILT_BIN" "$SOULACY_BIN"
    sudo cp "$BUILT_SY"  "$SY_BIN"
    sudo chmod +x "$SOULACY_BIN" "$SY_BIN"
else
    cp "$BUILT_BIN" "$SOULACY_BIN"
    cp "$BUILT_SY"  "$SY_BIN"
    chmod +x "$SOULACY_BIN" "$SY_BIN"
fi
ok "Binary installed: $SOULACY_BIN"

# ── 4. Set up ~/.soulacy/ runtime home ────────────────────────────────────────
hdr "Step 3: Runtime home (~/.soulacy/)"
mkdir -p "$RUNTIME_DIR"/{agents,mcp-servers,logs,plugins,skills}
ok "Runtime dirs created"

# Sync MCP servers and example agents (never overwrite config.yaml)
if [ -d "$REPO_DIR/mcp-servers" ]; then
    rsync -a "$REPO_DIR/mcp-servers/" "$RUNTIME_DIR/mcp-servers/"
    ok "MCP servers synced"
fi
if [ -d "$REPO_DIR/examples/agents" ]; then
    rsync -a "$REPO_DIR/examples/agents/" "$RUNTIME_DIR/agents/"
    ok "Example agents synced"
fi

# Bootstrap config if missing
if [ ! -f "$RUNTIME_DIR/config.yaml" ]; then
    sed \
        -e "s|/path/to/soulacy/mcp-servers|$RUNTIME_DIR/mcp-servers|g" \
        -e "s|/Users/yourname|$HOME|g" \
        "$REPO_DIR/config.yaml.example" > "$RUNTIME_DIR/config.yaml"
    warn "Created $RUNTIME_DIR/config.yaml from template — add your API keys before starting."
else
    ok "Existing config.yaml preserved"
fi

# ── 5. Install LaunchAgent ─────────────────────────────────────────────────────
hdr "Step 4: LaunchAgent (auto-start on login)"
mkdir -p "$HOME/Library/LaunchAgents"

sed \
    -e "s|__SOULACY_BIN__|$SOULACY_BIN|g" \
    -e "s|__HOME__|$HOME|g" \
    "$REPO_DIR/scripts/soulacy-launchagent.plist" > "$LAUNCHAGENT_PLIST"

# Unload existing if present, load fresh
launchctl unload "$LAUNCHAGENT_PLIST" 2>/dev/null || true
launchctl load -w "$LAUNCHAGENT_PLIST"
ok "LaunchAgent installed and loaded"
ok "Soulacy will start automatically at login"

# ── 6. Start now ──────────────────────────────────────────────────────────────
hdr "Step 5: Starting Soulacy"
lsof -ti :18789 | xargs kill -9 2>/dev/null || true
sleep 1
launchctl start "$LAUNCHAGENT_LABEL" 2>/dev/null || true
sleep 2

if lsof -ti :18789 >/dev/null 2>&1; then
    ok "Soulacy is running on http://localhost:18789"
else
    warn "Soulacy may still be starting — check logs:"
    warn "  tail -f $RUNTIME_DIR/logs/soulacy.log"
fi

# ── Summary ───────────────────────────────────────────────────────────────────
echo ""
echo -e "${BOLD}  Installation complete!${NC}"
echo "  ────────────────────────────────────────"
echo "  Binary:    $SOULACY_BIN"
echo "  Config:    $RUNTIME_DIR/config.yaml"
echo "  Agents:    $RUNTIME_DIR/agents/"
echo "  Logs:      $RUNTIME_DIR/logs/soulacy.log"
echo "  GUI:       http://localhost:18789"
echo ""
echo "  Commands:"
echo "    soulacy serve          start manually"
echo "    soulacy version        show version"
echo "    sy chat --agent X      chat with agent X"
echo ""
echo "  To update after code changes:"
echo "    ./deploy.sh"
echo ""
