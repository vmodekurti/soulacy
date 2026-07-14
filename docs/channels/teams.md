# Microsoft Teams Channel

The Teams channel sends outbound Soulacy messages to a Microsoft Teams Incoming
Webhook or Teams Workflow URL. It is meant for scheduled reports, alerts, and
non-interactive agent output.

## Configure

```yaml title="config.yaml"
channels:
  teams:
    enabled: true
    webhook_url: "https://..."
    title: "Soulacy"
```

Fields:

- `webhook_url`: required. The Teams webhook or workflow endpoint.
- `title`: optional heading prepended to each message.
- `default_output_to`: optional alternate webhook URL for scheduled output.
- `timeout_seconds`: request timeout; defaults to 10.

## Send From An Agent

```json
{
  "channel": "teams",
  "text": "Daily run completed."
}
```

If `to` is supplied and is an absolute HTTP URL, Soulacy uses it as a one-off
webhook override. Otherwise it uses `webhook_url`.

## Notes

This adapter is outbound-only. Interactive Microsoft Teams bot support requires
a separate bot framework adapter because Teams inbound events are not delivered
through incoming webhooks.
