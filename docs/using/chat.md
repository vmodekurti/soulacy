# Chat

The Chat page lets you converse with any enabled agent, watch its reasoning live, fork the conversation from any message, and see exactly what each reply cost.

## Quick start

1. Open **Chat** in the GUI (`http://localhost:18789` → ◎ Chat).
2. Pick an agent in the dropdown (the `system` agent is pre-selected when available).
3. Type a message and press **Enter** (Shift+Enter for a newline).

The same conversation via CLI or API:

```bash
sy chat --agent system "Hello!"

curl -X POST http://localhost:18789/api/v1/chat \
  -H "Authorization: Bearer $SOULACY_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"agent_id": "system", "user_id": "api-user", "text": "Hello!"}'
```

## Sessions

Each Chat tab conversation is one session. The metrics strip in the header shows the running totals for the current session, and **Clear** starts a fresh session (new session ID, branches and baselines reset). Passing your own `session_id` in the API request keeps a conversation going across calls.

## Watching the agent think

While a reply is being generated, a **Thinking** panel appears under the message and fills with live runtime events for that run:

| Event | What you see |
|---|---|
| `llm.call` / `llm.result` | Which model was called, turn number, tokens in/out, duration |
| `tool.call` / `tool.result` / `tool.log` | Tool name, arguments, returned content |
| `reasoning.start` / `reasoning.step` / `reasoning.result` | Loop strategy, each step's thought and tool, final step count and confidence |
| `error` | The failing stage and error text |

The panel stays attached to the reply afterwards — collapse or expand it any time. The summary line counts events ("6 events · 2 LLM · 3 tools").

!!! note
    Thinking events only appear for runs started from this page while the event stream is connected (the sidebar shows **● Live**).

## Per-reply token and cost deltas

Each assistant reply carries a small delta label next to its timestamp, e.g.:

```
+350 tok · $0.0035 · gpt-4o
```

It is computed by diffing the session's cumulative metrics before and after the turn. Hover it for the full breakdown: prompt/completion tokens this turn, number of LLM calls, session totals, and the provider.

## Checkpoints & branching

You can fork the conversation from **any** user or assistant message:

1. Hover a message bubble — a **⑂** fork button appears in its corner.
2. Click it. Soulacy copies the session history up to that message into a new session.
3. Branch chips appear above the conversation; the original becomes **main** and each fork gets its own chip. Click a chip to switch branches — every branch keeps its own messages and metrics.

This is ideal for "what if I had asked it differently?" exploration: branch, rephrase, compare, and switch back without losing anything.

API equivalent:

```bash
curl -X POST http://localhost:18789/api/v1/history/<session_id>/fork \
  -H "Authorization: Bearer $SOULACY_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"agent_id": "system", "upto_entry_id": "<entry-id>"}'
# → {"session_id": "<new-branch-session>"}
```

Entry IDs come from `GET /api/v1/history/<session_id>`.

!!! tip
    A message can only be forked once its turn is fully saved. If you see "This message has no saved history yet", let the turn finish first.

## Confirmation gates for dangerous tools

Agents can require explicit approval before running sensitive built-in tools. In SOUL.yaml:

```yaml
confirm_tools:
  - shell_exec
  - write_file
# or require confirmation for every built-in:
# confirm_tools: ["*"]
```

The built-in **system** agent ships with confirmation required for `shell_exec`, `run_script`, `write_file`, `http_request`, `download_file`, and `install_library`.

How it works: on the streaming chat endpoint (`POST /api/v1/chat/stream`), when the model requests a gated tool the run pauses and the server emits a `tool_confirm` SSE event carrying a `call_id`, the tool name, and its arguments. The client approves or denies with:

```bash
curl -X POST http://localhost:18789/api/v1/chat/confirm \
  -H "Authorization: Bearer $SOULACY_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"call_id": "<id-from-the-event>", "approved": true}'
```

A denial fails that tool call (the agent is told it was denied); an approval lets the run continue.

!!! warning
    Confirmation gates only pause runs that have a streaming connection to deliver the prompt. On non-streaming calls (plain `POST /api/v1/chat`) the engine logs a warning and proceeds, so pair `confirm_tools` with streaming clients for tools you truly want gated.

## Voice

The **🎤** button in the Chat header starts a realtime voice conversation whose transcripts land in the same session — see [Voice](voice.md).
