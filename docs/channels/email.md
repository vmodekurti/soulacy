# Email Channel

Email is an outbound delivery channel for scheduled reports, alerts, and
`channel.send`. It does not read an inbox; use a sidecar or external integration
for inbound mail automation.

## Configuration

```yaml title="config.yaml"
channels:
  email:
    enabled: true
    host: smtp.gmail.com
    port: 587
    username: "you@example.com"
    password: "app-password-from-your-mail-provider"
    from: "Soulacy <you@example.com>"
    default_output_to: "operator@example.com"
    subject: "Soulacy update"
    tls: starttls
```

Use an app password or SMTP relay credential. Do not use your normal account
password.

## Scheduled Output

```yaml title="agents/daily-brief/SOUL.yaml"
trigger: cron
schedule:
  cron: "0 7 * * *"
  output:
    channel: email
    to: "team@example.com"
```

If `schedule.output.to` is omitted, Soulacy uses `channels.email.default_output_to`.

## Agent Send

Give an agent the `channel.send` builtin:

```yaml title="agents/notifier/SOUL.yaml"
builtins:
  - channel.send
```

Then call:

```json
{
  "channel": "email",
  "to": "team@example.com",
  "text": "The report is ready.",
  "metadata": {
    "subject": "Daily report"
  }
}
```

## Troubleshooting

- `host is required`: fill SMTP host before enabling the channel.
- `authentication failed`: rotate the app password or SMTP credential, then restart.
- `refusing to send credentials over an unencrypted connection`: use `tls: starttls`
  or `tls: implicit` when username/password are configured.
- `no recipient`: set `to` in `schedule.output`, pass `to` to `channel.send`, or
  configure `default_output_to`.
