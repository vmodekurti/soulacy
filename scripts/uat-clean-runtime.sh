#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${SOULACY_UAT_BIN:-$ROOT/bin/soulacy}"
CLI="${SOULACY_UAT_CLI:-$ROOT/bin/sy}"
WORKSPACE="${SOULACY_UAT_WORKSPACE:-$(mktemp -d "${TMPDIR:-/tmp}/soulacy-uat-XXXXXXXX")}"
HOST="${SOULACY_UAT_HOST:-127.0.0.1}"
PORT="${SOULACY_UAT_PORT:-18891}"
API_KEY="${SOULACY_UAT_API_KEY:-sy_uat_clean_runtime}"
MODEL="${SOULACY_UAT_MODEL:-}"
CHAT="${SOULACY_UAT_CHAT:-0}"
URL="http://${HOST}:${PORT}"

if [[ ! -x "$BIN" ]]; then
  echo "soulacy binary not found at $BIN; run make build first or set SOULACY_UAT_BIN" >&2
  exit 2
fi

mkdir -p "$WORKSPACE"
if [[ -z "$MODEL" ]]; then
  MODEL="$(curl -fsS http://localhost:11434/api/tags 2>/dev/null \
    | python3 -c 'import json,sys; d=json.load(sys.stdin); ms=d.get("models") or []; print((ms[0].get("name") if ms else "") or "llama3.2")' 2>/dev/null \
    || printf 'llama3.2')"
fi
cat > "$WORKSPACE/config.yaml" <<YAML
server:
  host: "${HOST}"
  port: ${PORT}
  api_key: "${API_KEY}"
  gui_enabled: true
llm:
  default_provider: ollama
  providers:
    ollama:
      base_url: "http://localhost:11434"
      model: "${MODEL}"
agent_dirs:
  - "${WORKSPACE}/agents"
log:
  file: "${WORKSPACE}/logs/soulacy.log"
YAML

cleanup() {
  if [[ -n "${PID:-}" ]]; then
    kill "$PID" >/dev/null 2>&1 || true
    wait "$PID" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

SOULACY_WORKSPACE="$WORKSPACE" SOULACY_CONFIG_PATH="$WORKSPACE/config.yaml" "$BIN" serve >"$WORKSPACE/server.out" 2>"$WORKSPACE/server.err" &
PID=$!

api() {
  local method="$1"; shift
  local path="$1"; shift
  local data="${1:-}"
  if [[ -n "$data" ]]; then
    curl -fsS -X "$method" "$URL/api/v1$path" \
      -H "Authorization: Bearer $API_KEY" \
      -H "Content-Type: application/json" \
      --data "$data"
  else
    curl -fsS -X "$method" "$URL/api/v1$path" \
      -H "Authorization: Bearer $API_KEY"
  fi
}

wait_for_gateway() {
  for _ in $(seq 1 60); do
    if curl -fsS -H "Authorization: Bearer $API_KEY" "$URL/api/v1/health" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.5
  done
  echo "gateway did not start; stdout/stderr follow" >&2
  sed -n '1,120p' "$WORKSPACE/server.out" >&2 || true
  sed -n '1,160p' "$WORKSPACE/server.err" >&2 || true
  return 1
}

json_assert() {
  python3 -c '
import json, sys
expr = sys.argv[1]
doc = json.load(sys.stdin)
if not eval(expr, {"__builtins__": {}}, {"doc": doc, "len": len, "any": any, "all": all}):
    raise SystemExit(f"assertion failed: {expr}\n{json.dumps(doc, indent=2)[:2000]}")
' "$@"
}

echo "== clean runtime UAT =="
echo "workspace: $WORKSPACE"
echo "gateway:   $URL"

wait_for_gateway

echo "health"
api GET /health | json_assert "doc.get('status') == 'ok'"

echo "onboarding status"
api GET /onboarding/status | json_assert "'steps' in doc and len(doc['steps']) >= 3"

echo "template catalog"
TEMPLATE_JSON="$(api GET /templates)"
printf '%s' "$TEMPLATE_JSON" | json_assert "doc.get('count', 0) > 0"
TEMPLATE="$(printf '%s' "$TEMPLATE_JSON" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d["templates"][0]["name"])')"

echo "instantiate template: $TEMPLATE"
api POST "/templates/${TEMPLATE}/instantiate" '{"id":"uat-template-agent"}' >/dev/null
api GET /agents | json_assert "any(a.get('id') == 'uat-template-agent' for a in doc.get('agents', []))"

echo "queues"
api POST /queues '{"queue":"uat_resources"}' >/dev/null
api POST /queues/items '{"queue":"uat_resources","item":{"kind":"url","url":"https://example.com/uat"}}' >/dev/null
api GET '/queues/items?queue=uat_resources' | json_assert "doc.get('count') == 1"
api POST '/queues/take?queue=uat_resources' | json_assert "doc.get('ok') is True and doc.get('item') is not None"

echo "schedule"
api GET /schedule | json_assert "'schedule' in doc"

echo "doctor"
if [[ -x "$CLI" ]]; then
  SOULACY_WORKSPACE="$WORKSPACE" "$CLI" --gateway "$URL" --api-key "$API_KEY" --json doctor check >/dev/null
else
  api GET /doctor | json_assert "'providers' in doc and 'channels' in doc"
fi

if [[ "$CHAT" == "1" ]]; then
  echo "optional chat"
  SOULACY_WORKSPACE="$WORKSPACE" "$CLI" --gateway "$URL" --api-key "$API_KEY" chat --agent uat-template-agent "Reply with exactly: clean runtime ok" \
    | tee "$WORKSPACE/chat.out"
  grep -qi "clean runtime ok" "$WORKSPACE/chat.out"
fi

echo "PASS clean runtime UAT"
