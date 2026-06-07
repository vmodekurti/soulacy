#!/bin/zsh
# Reinstall Soulacy from scratch (requested 2026-06-07).
#
# ⚠ DESTRUCTIVE — deletes ~/.soulacy entirely (agents, memories, the
# encrypted credential vault, config.yaml). NO BACKUP IS TAKEN, per
# explicit request. Then rebuilds GUI dist + binaries from this checkout
# and boots once so a fresh soulspace workspace is created.
set -e

SCRIPT_DIR="${0:A:h}"
cd "$SCRIPT_DIR"

echo "=== Soulacy reinstall from scratch ==="
echo "This DELETES ~/.soulacy with NO backup."
printf "Type 'wipe' to continue: "
read CONFIRM
if [[ "$CONFIRM" != "wipe" ]]; then
    echo "Aborted — nothing touched."
    read -k 1 -s
    exit 1
fi

echo "=== 1/5 Stopping the gateway ==="
lsof -ti :18789 | xargs kill -9 2>/dev/null || true
# Stop a launchd service if one was ever installed.
launchctl unload ~/Library/LaunchAgents/com.soulacy.gateway.plist 2>/dev/null || true
sleep 1

echo "=== 2/5 Deleting ~/.soulacy (no backup) ==="
rm -rf ~/.soulacy
echo "    gone."

echo "=== 3/5 Rebuilding GUI dist + binaries ==="
(cd gui && npm install && npm run build)
if ! make all 2>&1 | tee /tmp/soulacy-build.log; then
    echo ""
    echo "❌ BUILD FAILED — see /tmp/soulacy-build.log for the full error."
    echo "Press Return to close this window."
    read
    exit 1
fi

echo "=== 4/5 First boot (creates the fresh soulspace workspace) ==="
env -u SOULACY_SERVER_API_KEY \
    nohup ./bin/soulacy serve \
    > /tmp/soulacy.log 2>&1 &
sleep 3
./bin/sy workspace info || true

echo "=== 5/5 Setup wizard ==="
echo "Gateway is up (PID $!, logs at /tmp/soulacy.log)."
echo "Running 'sy setup' for providers/channels — Ctrl-C to skip."
./bin/sy setup || true

echo ""
echo "✓ Reinstall complete. GUI: http://localhost:18789"
echo "Press Return to close this window."
read
