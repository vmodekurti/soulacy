#!/bin/bash
# deploy.sh — build Soulacy and sync runtime files to ~/.soulacy/
# Run from the dev repo after making changes: ./deploy.sh
set -e

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUNTIME_DIR="$HOME/.soulacy"
LAUNCHAGENT_LABEL="com.soulacy.soulacy"
LAUNCHAGENT_PLIST="$HOME/Library/LaunchAgents/${LAUNCHAGENT_LABEL}.plist"

GREEN='\033[0;32m'; NC='\033[0m'; BOLD='\033[1m'
ok() { echo -e "${GREEN}✓${NC} $*"; }

echo -e "\n${BOLD}Soulacy deploy${NC}: $REPO_DIR → $RUNTIME_DIR\n"

# ── 1. Build ──────────────────────────────────────────────────────────────────
echo "→ Building..."
cd "$REPO_DIR"
make all 2>&1 | tee /tmp/soulacy-build.log || {
    echo "❌ Build failed — see /tmp/soulacy-build.log"
    exit 1
}
ok "Build complete (GUI + Go)"

# ── 2. Install binary ─────────────────────────────────────────────────────────
echo "→ Installing binary..."
# Detect where soulacy is installed
INSTALLED_BIN="$(command -v soulacy 2>/dev/null || echo /usr/local/bin/soulacy)"
INSTALLED_SY="$(dirname "$INSTALLED_BIN")/sy"

if [[ "$INSTALLED_BIN" == /usr/local/bin/* || "$INSTALLED_BIN" == /opt/homebrew/bin/* ]]; then
    sudo cp "$REPO_DIR/bin/soulacy" "$INSTALLED_BIN"
    sudo cp "$REPO_DIR/bin/sy"      "$INSTALLED_SY"
else
    cp "$REPO_DIR/bin/soulacy" "$INSTALLED_BIN"
    cp "$REPO_DIR/bin/sy"      "$INSTALLED_SY"
fi
chmod +x "$INSTALLED_BIN" "$INSTALLED_SY" 2>/dev/null || sudo chmod +x "$INSTALLED_BIN" "$INSTALLED_SY"
ok "Binary updated: $INSTALLED_BIN"

# ── 3. Sync runtime files (never touch config.yaml) ───────────────────────────
echo "→ Syncing mcp-servers, agents, and skills..."
mkdir -p "$RUNTIME_DIR"/{agents,mcp-servers,skills,logs}
rsync -a --ignore-times "$REPO_DIR/mcp-servers/" "$RUNTIME_DIR/mcp-servers/"
rsync -a --ignore-times \
    --filter="protect ai-article-podcast-agent/SOUL.yaml" \
    "$REPO_DIR/examples/agents/" "$RUNTIME_DIR/agents/"
rsync -a --ignore-times "$REPO_DIR/examples/skills/" "$RUNTIME_DIR/skills/"
ok "Runtime files synced"

# ── 4. Reload via LaunchAgent (preferred) or manual restart ───────────────────
echo "→ Restarting Soulacy..."
if [ -f "$LAUNCHAGENT_PLIST" ]; then
    # LaunchAgent is installed — use it
    launchctl stop  "$LAUNCHAGENT_LABEL" 2>/dev/null || true
    sleep 1
    launchctl start "$LAUNCHAGENT_LABEL" 2>/dev/null || true
    sleep 2
    ok "Reloaded via LaunchAgent"
else
    # No LaunchAgent — fall back to manual.
    # Start from REPO_DIR so the relative agent_dirs path in config.yaml resolves correctly.
    lsof -ti :18789 | xargs kill -9 2>/dev/null || true
    sleep 1
    cd "$REPO_DIR"
    nohup soulacy serve > "$RUNTIME_DIR/logs/soulacy.log" 2>&1 &
    sleep 2
    ok "Restarted manually (PID recorded in logs)"
fi

# ── 5. Health check ───────────────────────────────────────────────────────────
if lsof -ti :18789 >/dev/null 2>&1; then
    ok "Soulacy running at http://localhost:18789"
else
    echo "⚠  Soulacy may still be starting — check: tail -f $RUNTIME_DIR/logs/soulacy.log"
fi

echo ""
echo -e "${BOLD}Deploy complete.${NC}"
