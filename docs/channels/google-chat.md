# Google Chat Channel

The Google Chat channel sends outbound Soulacy messages to a Google Chat
incoming webhook URL. It is meant for scheduled reports, alerts, and
non-interactive agent output.

## Configure

```yaml title="config.yaml"
channels:
  google_chat:
    enabled: true
    webhook_url: "https://chat.googleapis.com/v1/spaces/..."
    prefix: "[Soulacy]"
```

Fields:

- `webhook_url`: required. The Google Chat incoming webhook endpoint.
- `prefix`: optional text prepended to each message.
- `default_output_to`: optional alternate webhook URL for scheduled output.
- `timeout_seconds`: request timeout; defaults to 10.

## Send From An Agent

```json
{
  "channel": "google_chat",
  "text": "Daily run completed."
}
```

If `to` is supplied and is an absolute HTTP URL, Soulacy uses it as a one-off
webhook override. Otherwise it uses `webhook_url`.

## Notes

This adapter is outbound-only. Interactive Google Chat app support requires a
separate inbound app integration because incoming webhooks do not receive user
messages.
