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
STUDIO_BUILD="${SOULACY_UAT_STUDIO_BUILD:-0}"
STUDIO_LIVE="${SOULACY_UAT_STUDIO_LIVE:-0}"
BROWSER_MCP="${SOULACY_UAT_BROWSER_MCP:-0}"
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
scope = {"doc": doc, "len": len, "any": any, "all": all, "isinstance": isinstance, "list": list, "dict": dict, "str": str}
if not eval(expr, {"__builtins__": {}, **scope}, scope):
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

echo "launch readiness"
api GET /readiness | json_assert "'summary' in doc and 'journey' in doc and 'release' in doc and len(doc['journey']) >= 6 and all(k in doc['summary'] for k in ['status', 'score', 'providers_ready', 'agents', 'enabled_agents', 'updates_ready']) and all(k in [item.get('key') for item in doc['journey']] for k in ['providers', 'studio', 'agents', 'channels', 'monitor', 'learning', 'release'])"

echo "gui shell"
INDEX_HTML="$(curl -fsS "$URL/")"
ASSET_PATH="$(printf '%s' "$INDEX_HTML" | python3 -c '
import re, sys
html = sys.stdin.read()
m = re.search(r"/assets/index-[^\"'"'"']+\.js", html)
print(m.group(0) if m else "")
')"
if [[ -z "$ASSET_PATH" ]]; then
  echo "could not locate GUI asset in index.html" >&2
  exit 1
fi
GUI_JS="$(curl -fsS "$URL$ASSET_PATH")"
GUI_BYTES="$(printf '%s' "$GUI_JS" | wc -c | tr -d ' ')"
if (( GUI_BYTES > 250000 )); then
  echo "GUI entry bundle is too large: ${GUI_BYTES} bytes" >&2
  exit 1
fi
for label in "Deployed" "Learning" "Delivery" "Automations" "Runs" "Browser"; do
  if ! grep -Fq "$label" <<<"$GUI_JS"; then
    echo "GUI entry bundle does not contain expected label: $label" >&2
    exit 1
  fi
done
DASHBOARD_PATH="$(printf '%s' "$GUI_JS" | python3 -c '
import re, sys
js = sys.stdin.read()
m = re.search(r"assets/Dashboard-[^\"'"'"']+\.js", js)
print("/" + m.group(0) if m else "")
')"
if [[ -z "$DASHBOARD_PATH" ]]; then
  echo "could not locate lazy Dashboard chunk in GUI entry bundle" >&2
  exit 1
fi
DASHBOARD_JS="$(curl -fsS "$URL$DASHBOARD_PATH")"
if ! grep -Fq "Launch Readiness" <<<"$DASHBOARD_JS"; then
  echo "Dashboard chunk does not contain Launch Readiness" >&2
  exit 1
fi
MOBILE_PATH="$(printf '%s' "$GUI_JS" | python3 -c '
import re, sys
js = sys.stdin.read()
m = re.search(r"assets/Mobile-[^\"'"'"']+\.js", js)
print("/" + m.group(0) if m else "")
')"
if [[ -z "$MOBILE_PATH" ]]; then
  echo "could not locate lazy Mobile chunk in GUI entry bundle" >&2
  exit 1
fi
MOBILE_JS="$(curl -fsS "$URL$MOBILE_PATH")"
if ! grep -Fq "Launch Readiness" <<<"$MOBILE_JS"; then
  echo "Mobile chunk does not contain Launch Readiness" >&2
  exit 1
fi
for label in "Recent runs" "Delivery health"; do
  if ! grep -Fq "$label" <<<"$MOBILE_JS"; then
    echo "Mobile chunk does not contain expected operations label: $label" >&2
    exit 1
  fi
done
BROWSER_PATH="$(printf '%s' "$GUI_JS" | python3 -c '
import re, sys
js = sys.stdin.read()
m = re.search(r"assets/BrowserTrace-[^\"'"'"']+\.js", js)
print("/" + m.group(0) if m else "")
')"
if [[ -z "$BROWSER_PATH" ]]; then
  echo "could not locate lazy BrowserTrace chunk in GUI entry bundle" >&2
  exit 1
fi
BROWSER_JS="$(curl -fsS "$URL$BROWSER_PATH")"
if ! grep -Fq "Browser Trace" <<<"$BROWSER_JS"; then
  echo "BrowserTrace chunk does not contain Browser Trace" >&2
  exit 1
fi
for label in "Screenshot Gallery" "Export JSON" "Copy link"; do
  if ! grep -Fq "$label" <<<"$BROWSER_JS"; then
    echo "BrowserTrace chunk does not contain expected trace tool: $label" >&2
    exit 1
  fi
done

echo "pwa manifest"
MANIFEST_JSON="$(curl -fsS "$URL/manifest.webmanifest")"
printf '%s' "$MANIFEST_JSON" | json_assert "doc.get('start_url') == '/#mobile' and doc.get('display') == 'standalone' and doc.get('id') == '/#mobile' and all(url in [s.get('url') for s in doc.get('shortcuts', [])] for url in ['/#chat', '/#activity', '/#studio', '/#channels'])"
SW_JS="$(curl -fsS "$URL/sw.js")"
for needle in "notificationclick" "/#mobile" "/icon.svg"; do
  if ! grep -Fq "$needle" <<<"$SW_JS"; then
    echo "service worker does not contain expected PWA behavior: $needle" >&2
    exit 1
  fi
done

echo "template catalog"
TEMPLATE_JSON="$(api GET /templates)"
printf '%s' "$TEMPLATE_JSON" | json_assert "doc.get('count', 0) > 0"
printf '%s' "$TEMPLATE_JSON" | json_assert "all(name in [t.get('name') for t in doc.get('templates', [])] for name in ['stock-screener', 'research-brief', 'rag-over-docs', 'scheduled-briefing'])"
printf '%s' "$TEMPLATE_JSON" | json_assert "all(t.get('display_name') or t.get('definition', {}).get('name') for t in doc.get('templates', []))"
printf '%s' "$TEMPLATE_JSON" | json_assert "any(t.get('name') == 'stock-screener' and any(item.get('key') == 'search' for item in t.get('setup', [])) for t in doc.get('templates', []))"
TEMPLATE="$(printf '%s' "$TEMPLATE_JSON" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d["templates"][0]["name"])')"

echo "instantiate template: $TEMPLATE"
api POST "/templates/${TEMPLATE}/instantiate" '{"id":"uat-template-agent"}' >/dev/null
api GET /agents | json_assert "any(a.get('id') == 'uat-template-agent' for a in doc.get('agents', []))"

echo "studio run history"
api GET '/studio/run-history?agentId=uat-template-agent' | json_assert "doc.get('agentId') == 'uat-template-agent' and isinstance(doc.get('runs'), list)"

echo "browser trace"
api GET '/browser/trace?agent_id=uat-template-agent' | json_assert "doc.get('enabled') is True and doc.get('trace', {}).get('agent_id') == 'uat-template-agent' and isinstance(doc.get('trace', {}).get('steps'), list)"

if [[ "$BROWSER_MCP" == "1" ]]; then
  echo "optional browser MCP sidecar"
  SOULACY_BROWSER_MCP_SMOKE=1 python3 "$ROOT/scripts/browser-mcp-smoke.py"
fi

if [[ "$STUDIO_BUILD" == "1" || "$STUDIO_LIVE" == "1" ]]; then
  echo "optional studio build loop"
  VERIFY=false
  if [[ "$STUDIO_LIVE" == "1" ]]; then
    VERIFY=true
  fi
  STUDIO_BUILD_BODY="$(python3 - "$VERIFY" <<'PY'
import json, sys
verify = sys.argv[1].lower() == "true"
workflow = {
    "name": "UAT Echo Workflow",
    "intent": "When manually triggered, echo the user's message in a short friendly sentence.",
    "trigger": {"type": "manual"},
    "channels": ["http"],
    "flow": {
        "entry": "normalize",
        "output": "normalize",
        "nodes": [
            {
                "id": "normalize",
                "kind": "python",
                "description": "Normalize the incoming manual input and produce a short reply.",
                "input": "{\"message\":\"{{ .trigger.text }}\"}",
                "code": "def run(inputs):\n    msg = inputs.get('message') or ''\n    return {'reply': 'UAT echo: ' + str(msg).strip()}\n",
                "output": "result",
                "x": 0,
                "y": 0,
            }
        ],
        "edges": [{"from": "normalize", "to": "end"}],
    },
}
print(json.dumps({
    "workflow": workflow,
    "intent": workflow["intent"],
    "verify": verify,
}))
PY
)"
  STUDIO_BUILD_JSON="$(api POST /studio/build "$STUDIO_BUILD_BODY")"
  printf '%s' "$STUDIO_BUILD_JSON" | json_assert "doc.get('report', {}).get('ok') is True and isinstance(doc.get('report', {}).get('attempts'), list) and isinstance(doc.get('traceId'), str) and len(doc.get('traceId')) > 0"
  if [[ "$STUDIO_LIVE" == "1" ]]; then
    printf '%s' "$STUDIO_BUILD_JSON" | json_assert "doc.get('report', {}).get('verified') is True"
  fi
  TRACE_ID="$(printf '%s' "$STUDIO_BUILD_JSON" | python3 -c 'import json,sys; print(json.load(sys.stdin)["traceId"])')"
  api GET "/studio/build-trace?id=${TRACE_ID}" | json_assert "doc.get('id') == '${TRACE_ID}' and isinstance(doc.get('events'), list) and len(doc.get('events')) > 0"
fi

echo "golden template instantiation"
api POST "/templates/stock-screener/instantiate" '{"id":"uat-stock-screener","cron":"0 7 * * 1-5","output":{"channel":"http","to":"uat-outbox","template":"{reply}"}}' >/dev/null
api POST "/templates/research-brief/instantiate" '{"id":"uat-research-brief"}' >/dev/null
api POST "/templates/rag-over-docs/instantiate" '{"id":"uat-rag-over-docs"}' >/dev/null
api GET /agents | json_assert "all(agent_id in [a.get('id') for a in doc.get('agents', [])] for agent_id in ['uat-stock-screener', 'uat-research-brief', 'uat-rag-over-docs'])"
api POST /agents/validate "$(api GET /agents | python3 -c 'import json,sys; agents=json.load(sys.stdin)["agents"]; print(json.dumps(next(a for a in agents if a.get("id")=="uat-stock-screener")))')" \
  | json_assert "doc.get('valid') is True and doc.get('errors') == 0"

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
  SOULACY_WORKSPACE="$WORKSPACE" "$CLI" --gateway "$URL" --api-key "$API_KEY" launch check >/dev/null
  cat > "$WORKSPACE/release-manifest.json" <<JSON
{"product":"soulacy","version":"99.0.0","artifacts":[{"name":"soulacy-test.tar.gz","os":"uat","arch":"uat","sha256":"abc123","bytes":123}]}
JSON
  SOULACY_WORKSPACE="$WORKSPACE" "$CLI" --gateway "$URL" --api-key "$API_KEY" --json update check --manifest "$WORKSPACE/release-manifest.json" --current 1.0.0 \
    | json_assert "doc.get('update_available') is True and doc.get('latest_version') == '99.0.0'"
  RELDIR="$WORKSPACE/update-dry-run"
  mkdir -p "$RELDIR/stage"
  printf '#!/usr/bin/env sh\necho soulacy uat update\n' > "$RELDIR/stage/soulacy"
  printf '#!/usr/bin/env sh\necho sy uat update\n' > "$RELDIR/stage/sy"
  chmod 0755 "$RELDIR/stage/soulacy" "$RELDIR/stage/sy"
  (cd "$RELDIR/stage" && tar -czf "$RELDIR/soulacy-uat.tar.gz" soulacy sy)
  if command -v shasum >/dev/null 2>&1; then
    UAT_SHA="$(shasum -a 256 "$RELDIR/soulacy-uat.tar.gz" | awk '{print $1}')"
  else
    UAT_SHA="$(sha256sum "$RELDIR/soulacy-uat.tar.gz" | awk '{print $1}')"
  fi
  UAT_BYTES="$(wc -c < "$RELDIR/soulacy-uat.tar.gz" | tr -d ' ')"
  UAT_GOOS="$(go env GOOS 2>/dev/null || uname | tr '[:upper:]' '[:lower:]')"
  UAT_GOARCH="$(go env GOARCH 2>/dev/null || uname -m)"
  cat > "$WORKSPACE/release-manifest-real.json" <<JSON
{"product":"soulacy","version":"99.0.0","artifacts":[{"name":"$(basename "$RELDIR/soulacy-uat.tar.gz")","os":"$UAT_GOOS","arch":"$UAT_GOARCH","sha256":"$UAT_SHA","bytes":$UAT_BYTES,"url":"$RELDIR/soulacy-uat.tar.gz"}]}
JSON
  SOULACY_WORKSPACE="$WORKSPACE" "$CLI" --gateway "$URL" --api-key "$API_KEY" --json update install --manifest "$WORKSPACE/release-manifest-real.json" --current 1.0.0 --install-dir "$RELDIR/install" --dry-run \
    | json_assert "doc.get('dry_run') is True and doc.get('installed') is False and doc.get('artifact', {}).get('name') == 'soulacy-uat.tar.gz'"
else
  api GET /doctor | json_assert "'providers' in doc and 'channels' in doc"
fi


# ── Knowledge base + ASYNC ingestion ────────────────────────────────────────
# Ingestion runs OUT of the request: POST returns 202 + a durable job, and a
# background worker does extract → chunk → embed → store. We always assert the
# async CONTRACT (202 + a queued job + a retrievable job row), which needs no
# embedder. The full round-trip (job reaches `done`, the doc is searchable) is
# additionally asserted when an embedder is actually reachable.
# KB creation itself probes the embedding model, so the whole block needs a
# reachable embedder. Without one this is an ENVIRONMENT gap, not a regression —
# skip loudly instead of failing the run.
KB_CODE="$(curl -sS -o "$WORKSPACE/kb.json" -w '%{http_code}' \
  -X POST "$URL/api/v1/knowledge" -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" --data '{"name":"uat_kb","description":"clean runtime UAT"}' || true)"
if [[ "$KB_CODE" != "200" && "$KB_CODE" != "201" ]]; then
  echo "SKIP knowledge: no reachable embedder — KB creation returned HTTP $KB_CODE"
  echo "     (set up Ollama with an embedding model, e.g. \`ollama pull nomic-embed-text\`, to cover this)"
  KB_AVAILABLE=0
else
  KB_AVAILABLE=1
fi

if [[ "$KB_AVAILABLE" == "1" ]]; then
echo "knowledge: kb created"
api GET /knowledge | json_assert "any(k.get('name') == 'uat_kb' for k in doc.get('knowledge_bases', doc.get('kbs', [])))"

echo "knowledge: ingest returns 202 + job (async, non-blocking)"
INGEST_CODE="$(curl -sS -o "$WORKSPACE/ingest.json" -w '%{http_code}' \
  -X POST "$URL/api/v1/knowledge/uat_kb/documents" \
  -H "Authorization: Bearer $API_KEY" -H "Content-Type: application/json" \
  --data '{"title":"UAT Doc","mime_type":"text/plain","content":"Soulacy clean runtime UAT document. The refund window is 30 days."}')"
if [[ "$INGEST_CODE" != "202" ]]; then
  echo "FAIL: ingest must be async (202 Accepted), got HTTP $INGEST_CODE" >&2
  cat "$WORKSPACE/ingest.json" >&2 || true
  exit 1
fi
json_assert "doc.get('id') and doc.get('status') in ('queued','running') and doc.get('kb_name') == 'uat_kb'" < "$WORKSPACE/ingest.json"
JOB_ID="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "$WORKSPACE/ingest.json")"

echo "knowledge: job is durable + listable ($JOB_ID)"
api GET "/ingest-jobs/${JOB_ID}" | json_assert "doc.get('id') == '${JOB_ID}'"
api GET '/knowledge/uat_kb/jobs' | json_assert "any(j.get('id') == '${JOB_ID}' for j in doc.get('jobs', []))"

# Full ingestion needs a live embedder. Poll to a terminal state, then decide.
echo "knowledge: awaiting worker"
KB_STATUS="queued"
for _ in $(seq 1 60); do
  KB_STATUS="$(api GET "/ingest-jobs/${JOB_ID}" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("status",""))')"
  [[ "$KB_STATUS" == "done" || "$KB_STATUS" == "failed" ]] && break
  sleep 1
done

if [[ "$KB_STATUS" == "done" ]]; then
  echo "knowledge: ingested — asserting document + search"
  api GET "/ingest-jobs/${JOB_ID}" | json_assert "doc.get('progress') == 100 and doc.get('doc_id')"
  api GET /knowledge/uat_kb/documents | json_assert "any(d.get('title') == 'UAT Doc' for d in doc.get('documents', []))"
  api POST /knowledge/uat_kb/search '{"query":"refund window","top_k":3}' \
    | json_assert "isinstance(doc.get('hits', doc.get('results', [])), list)"
else
  # A failed job here means no reachable embedder — that's an environment gap,
  # not a product regression. The async contract above already passed, and the
  # failure must be RECORDED with a reason (that itself is the guarantee).
  api GET "/ingest-jobs/${JOB_ID}" | json_assert "doc.get('status') == 'failed' and doc.get('error')"
  echo "SKIP knowledge round-trip: no reachable embedder (job recorded a failure reason, as designed)"
fi

fi   # end KB_AVAILABLE

# ── Channel delivery doctor (no secrets required) ────────────────────────────
# Diagnose must return a STRUCTURED, plain-language verdict rather than a raw
# error, even for a channel that was never configured.
echo "channels: diagnose an unconfigured channel"
DIAG="$(curl -sS -X POST "$URL/api/v1/channels/telegram/diagnose" \
  -H "Authorization: Bearer $API_KEY" -H "Content-Type: application/json" --data '{"dry":true}' || true)"
printf '%s' "$DIAG" | json_assert "'diagnosis' in doc and doc['diagnosis'].get('category') and doc['diagnosis'].get('reason')"

# ── Live channel delivery (secret-gated; skips cleanly) ─────────────────────
if [[ -n "${TELEGRAM_BOT_TOKEN:-}" && -n "${TELEGRAM_TEST_CHAT_ID:-}" ]]; then
  echo "channels: live telegram delivery"
  api PATCH /channels/telegram "$(python3 -c '
import json, os
print(json.dumps({"settings": {
  "token": os.environ["TELEGRAM_BOT_TOKEN"],
  "default_output_to": os.environ["TELEGRAM_TEST_CHAT_ID"],
  "outbound_only": True,
}}))')" >/dev/null
  api POST /channels/telegram/enable >/dev/null || true
  api POST /channels/telegram/test '{"text":"clean runtime UAT"}' \
    | json_assert "doc.get('ok') is True"
  api POST /channels/telegram/diagnose '{}' \
    | json_assert "doc['diagnosis'].get('ok') is True"
else
  echo "SKIP live telegram delivery: set TELEGRAM_BOT_TOKEN + TELEGRAM_TEST_CHAT_ID to run it"
fi

if [[ -n "${SLACK_BOT_TOKEN:-}" && -n "${SLACK_TEST_CHANNEL_ID:-}" ]]; then
  echo "channels: live slack delivery"
  api POST /channels/slack/test "$(python3 -c '
import json, os
print(json.dumps({"to": os.environ["SLACK_TEST_CHANNEL_ID"], "text": "clean runtime UAT"}))')" \
    | json_assert "doc.get('ok') is True"
else
  echo "SKIP live slack delivery: set SLACK_BOT_TOKEN + SLACK_TEST_CHANNEL_ID to run it"
fi

if [[ -n "${DISCORD_BOT_TOKEN:-}" && -n "${DISCORD_TEST_CHANNEL_ID:-}" ]]; then
  echo "channels: live discord delivery"
  api POST /channels/discord/test "$(python3 -c '
import json, os
print(json.dumps({"to": os.environ["DISCORD_TEST_CHANNEL_ID"], "text": "clean runtime UAT"}))')" \
    | json_assert "doc.get('ok') is True"
else
  echo "SKIP live discord delivery: set DISCORD_BOT_TOKEN + DISCORD_TEST_CHANNEL_ID to run it"
fi

echo "support bundle"
BUNDLE="$WORKSPACE/support-bundle.zip"
curl -fsS "$URL/api/v1/support/bundle" -H "Authorization: Bearer $API_KEY" -o "$BUNDLE"
python3 - "$BUNDLE" <<'PY'
import sys, zipfile
path = sys.argv[1]
with zipfile.ZipFile(path) as zf:
    names = set(zf.namelist())
    required = {"manifest.json", "doctor.json", "readiness.json"}
    missing = sorted(required - names)
    if missing:
        raise SystemExit(f"support bundle missing {missing}; got {sorted(names)[:40]}")
    for name in required:
        if not zf.read(name).strip():
            raise SystemExit(f"support bundle entry {name} is empty")
PY

if [[ "$CHAT" == "1" ]]; then
  echo "optional chat"
  SOULACY_WORKSPACE="$WORKSPACE" "$CLI" --gateway "$URL" --api-key "$API_KEY" chat --agent uat-template-agent "Reply with exactly: clean runtime ok" \
    | tee "$WORKSPACE/chat.out"
  grep -qi "clean runtime ok" "$WORKSPACE/chat.out"
fi


# ── First-run bootstrap (virgin install) ────────────────────────────────────
# The main run above pre-writes config.yaml, so it never exercises the "dumb
# install" path. Here we start the gateway against a COMPLETELY EMPTY workspace
# and assert EnsureBootstrap does its job: writes a default config.yaml, mints an
# API key, and comes up authenticated on that key. This is the very first thing a
# new user hits, so it must never regress.
echo "first-run bootstrap (virgin workspace)"
FR_WS="$(mktemp -d "${TMPDIR:-/tmp}/soulacy-uat-firstrun-XXXXXXXX")"
FR_PORT=$((PORT + 1))
FR_URL="http://${HOST}:${FR_PORT}"

SOULACY_WORKSPACE="$FR_WS" SOULACY_SERVER_HOST="$HOST" SOULACY_SERVER_PORT="$FR_PORT" \
  "$BIN" serve >"$FR_WS/server.out" 2>"$FR_WS/server.err" &
FR_PID=$!

fr_cleanup() {
  if [[ -n "${FR_PID:-}" ]]; then
    kill "$FR_PID" >/dev/null 2>&1 || true
    wait "$FR_PID" >/dev/null 2>&1 || true
  fi
}

# The config it generates is the source of the key we must authenticate with.
FR_CONFIG=""
for _ in $(seq 1 60); do
  FR_CONFIG="$(find "$FR_WS" -name config.yaml -maxdepth 3 2>/dev/null | head -1)"
  [[ -n "$FR_CONFIG" ]] && break
  sleep 0.5
done
if [[ -z "$FR_CONFIG" ]]; then
  echo "FAIL first-run: no config.yaml was bootstrapped under $FR_WS" >&2
  sed -n '1,60p' "$FR_WS/server.err" >&2 || true
  fr_cleanup; exit 1
fi
echo "  bootstrapped config: $FR_CONFIG"

# A key must have been generated and persisted (it gates every API call).
FR_KEY="$(python3 -c '
import re, sys
text = open(sys.argv[1]).read()
m = re.search(r"^\s*api_key:\s*\"?([^\"\n]+)\"?", text, re.M)
print((m.group(1).strip() if m else ""))' "$FR_CONFIG")"
if [[ -z "$FR_KEY" ]]; then
  echo "FAIL first-run: bootstrapped config has no generated api_key" >&2
  fr_cleanup; exit 1
fi

# And the gateway must actually be up and accept exactly that key.
FR_OK=0
for _ in $(seq 1 60); do
  if curl -fsS -H "Authorization: Bearer $FR_KEY" "$FR_URL/api/v1/health" >/dev/null 2>&1; then
    FR_OK=1; break
  fi
  sleep 0.5
done
if [[ "$FR_OK" != "1" ]]; then
  echo "FAIL first-run: gateway did not come up authenticated on the generated key" >&2
  sed -n '1,60p' "$FR_WS/server.err" >&2 || true
  fr_cleanup; exit 1
fi

# An unauthenticated call must still be rejected (the key isn't decorative).
FR_CODE="$(curl -sS -o /dev/null -w '%{http_code}' "$FR_URL/api/v1/agents" || true)"
if [[ "$FR_CODE" != "401" && "$FR_CODE" != "403" ]]; then
  echo "FAIL first-run: unauthenticated request returned $FR_CODE (expected 401/403)" >&2
  fr_cleanup; exit 1
fi

fr_cleanup
rm -rf "$FR_WS"
echo "  first-run OK: config + generated key + authenticated gateway"

echo "PASS clean runtime UAT"
