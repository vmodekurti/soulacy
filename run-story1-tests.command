#!/bin/bash
# Story 1 — Auth & secret hardening: run verification tests.
# Double-click this file in Finder to run in Terminal.
set -e
cd ~/Documents/Development/agenticai/soulacy

echo ""
echo "╔══════════════════════════════════════════════╗"
echo "║  Story 1 — Auth/Secret Hardening Test Run    ║"
echo "╚══════════════════════════════════════════════╝"
echo ""

echo "→ 1/3 Go: focused redaction + auth tests"
GOCACHE=$PWD/.gocache go test -count=1 -timeout 120s \
  -run "SafeChannelsView|IsSecretChannelKey|GetConfig_ChannelSecretsRedactedEndToEnd|PatchConfig_ResponseChannelSecretsRedacted|GetConfig_Unauthenticated401JSONError" \
  -v ./internal/gateway/ 2>&1 | grep -E "^(=== RUN|--- (PASS|FAIL)|ok|FAIL)"

echo ""
echo "→ 2/3 Go: full suite (regression check)"
GOCACHE=$PWD/.gocache go test -count=1 -timeout 60s ./... 2>&1 | grep -E "^(ok|FAIL)" | sort | uniq -c | sort -rn | head -5
GOCACHE=$PWD/.gocache go test -count=1 -timeout 60s ./... 2>&1 | grep -E "^FAIL" || echo "   No package failures."

echo ""
echo "→ 3/3 GUI: vitest (auth-error display behavior)"
cd gui && npm test

echo ""
echo "✅ Story 1 verification complete."
