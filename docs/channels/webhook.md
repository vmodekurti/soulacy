# Generic Webhooks

Use generic webhooks when another product can send JSON but Soulacy does not
need a dedicated native adapter yet.

## Agent configuration

```yaml title="agents/github-triage/SOUL.yaml"
id: github-triage
name: GitHub Triage
trigger: webhook

webhook:
  text_path: issue.title
  user_id_path: sender.id
  username_path: sender.login
  thread_id_path: issue.number
  include_raw: true

system_prompt: >
  Triage inbound GitHub issues. Summarize the problem, identify missing
  reproduction details, and recommend labels.
```

The endpoint is:

```bash
curl -X POST http://localhost:18789/api/v1/webhooks/github-triage \
  -H "Authorization: Bearer $SOULACY_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"issue":{"number":42,"title":"Crash on launch"},"sender":{"id":7,"login":"vasu"}}'
```

Soulacy returns the agent reply synchronously:

```json
{
  "reply": "The issue is a launch crash report...",
  "session_id": "webhook-github-triage-42",
  "thread_id": "42",
  "user_id": "7"
}
```

## Mapping fields

Paths use dot notation. A leading `$` or `$.` is allowed.

| Field | Purpose |
|-------|---------|
| `text_path` | Message text sent to the agent. |
| `user_id_path` | Stable sender identifier. |
| `username_path` | Human-readable sender name. |
| `thread_id_path` | Conversation, issue, alert, or event thread. |
| `session_id_path` | Explicit Soulacy session id. |
| `include_raw` | Attach the compact original JSON payload as run metadata. |

If `text_path` is empty, Soulacy tries common fields such as `text`,
`message`, `body`, `content`, `issue.title`, and `alert.title`. If none are
present, it sends the compact JSON payload as the prompt.

## GUI

Open **Agents**, set **Trigger** to `webhook`, then fill the **Webhook request
mapping** fields. The editor shows the endpoint path for the selected agent.

