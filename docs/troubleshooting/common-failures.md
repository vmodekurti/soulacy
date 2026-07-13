# Common Failures → Fixes

This page maps the errors you're most likely to hit to their cause and the exact
fix. Soulacy tries to surface these as actionable messages in the GUI and in
`sy doctor`; this is the reference behind them.

Run `sy doctor` first — it checks the vault, provider auth, ports, adapters, and
recent error rate, and prints a specific remedy for each failed check.

## Install & startup

| Message | Cause | Fix |
| --- | --- | --- |
| `command not found: sy` / `soulacy` | Binary not on PATH | Re-run the installer, or add the install dir (`~/.local/bin`, `/usr/local/bin`, `/opt/homebrew/bin`) to PATH |
| `address already in use` | Another process holds the gateway port | `sy doctor` reports the port; stop the other process or change `server.addr` |
| Gateway starts then exits | Config invalid or vault locked | Check `sy daemon logs`; run `sy doctor` |
| Service won't start on login | Daemon not installed | `sy daemon install`, then `sy daemon status` |
| `sy doctor` warns `no update manifest configured` | Production upgrade path is not wired | Set `updates.manifest_url` in `config.yaml` or export `SOULACY_UPDATE_MANIFEST` |
| `sy doctor` warns `update manifest could not be checked` | Manifest URL/file is unreachable or invalid | Verify the URL/file path, JSON shape, and artifact links before launch |

## LLM providers

| Message | Cause | Fix |
| --- | --- | --- |
| `provider auth failed` / `401` | Missing or invalid API key | Set the provider key in the vault; re-run the provider test in the first-run wizard |
| `model not found` | Model name typo or not enabled for your key | Pick from the model list in the wizard, which is fetched live from the provider |
| Requests time out | Network egress blocked | Confirm outbound HTTPS to the provider; check any proxy settings |

## Channel delivery (Delivery Doctor)

Each channel mapping has a **Diagnose** button (backed by
`POST /api/v1/channels/:id/diagnose`) that runs these checks and reports the
specific reason in plain language — what happened and how to fix it. Pass
`{"dry": true}` to check readiness (destination set? adapter registered and
connected?) without sending a real message. Common results:

| Symptom | Cause | Fix |
| --- | --- | --- |
| `missing "to"` | No destination on a scheduled/one-off send | Set a destination on the mapping, or let it use the default outbound bot |
| `chat not found` / `invalid chat id` | Wrong Telegram chat ID | Re-copy the chat ID; the bot must have received at least one message in that chat |
| `bot is not a member` / `not_in_channel` | Bot never invited | Invite the bot to the Slack channel / Telegram group / Discord channel |
| `missing scope` | Slack app lacks `chat:write` | Add the scope in the Slack app config and reinstall the app |
| `unauthorized` / `invalid token` | Wrong or rotated bot token | Update the token in the vault |
| `adapter disabled` | Channel adapter turned off | Enable the adapter for that channel |
| `restart required` | Adapter config changed since boot | Restart the gateway (`sy daemon stop && sy daemon start`) |

Rich, GUI-only output (charts, tables, images) is automatically converted to
channel-safe text before delivery, so a message that looks fine in Chat won't
fail silently on Telegram/Slack/Discord.

## Studio & workflows

| Symptom | Cause | Fix |
| --- | --- | --- |
| Save blocked by integrity check | Dangling reference, missing variable, invalid Python | Read the specific check message; fix the flagged node |
| Run fails with `template variable not found` | A `{{var}}` is used before it's set | Use **Debug in Studio** → it identifies the unset variable and proposes a binding |
| Repair won't converge | The failure needs external input (e.g. a missing secret) | Provide the secret/tool, then re-run **Build until it works** |

## Schedules

| Symptom | Cause | Fix |
| --- | --- | --- |
| Scheduled run never fires | Daemon not running | `sy daemon status`; install/start it |
| Scheduled output not delivered | No default outbound bot and no per-schedule destination | Set a default outbound bot, or a destination on the schedule |

## When you're stuck: support bundle

`sy support bundle` produces a redacted support bundle — config with secrets
stripped, recent logs, `doctor` output, release/update metadata, versions, and
recent failures — safe to share when asking for help. The same bundle can be
downloaded from **Dashboard → Launch Readiness** or **Config → Support**; the
live gateway bundle also includes launch-readiness state. It never includes
secret values.
