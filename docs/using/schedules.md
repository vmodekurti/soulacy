# Schedules

Cron agents run on a schedule you define — daily briefings, weekly monitors, periodic syncs — and the Schedule page is where you watch, trigger, and tune them.

## Quick start

Give an agent a cron trigger in its SOUL.yaml (or set Trigger to `cron` in the Agents editor):

```yaml
trigger: cron
schedule:
  cron: "0 8 * * *"        # every day at 8 AM
  output:                   # optional: where the result goes
    channel: "telegram-financial-agent"
    to: "123456789"
    bot_name: "Finance Bot"
enabled: true
```

Then open **⏱ Schedule** in the GUI — the agent appears under **Cron agents** with a countdown to its next fire. Click **▶ Run** to trigger it immediately.

CLI / API:

```bash
sy schedule list                       # all scheduled entries
sy schedule trigger <agent-id>         # run now

curl -X POST http://localhost:18789/api/v1/agents/<agent-id>/trigger \
  -H "Authorization: Bearer $SOULACY_API_KEY"
```

## The Schedule page

### Active schedules table

One row per schedule registered with the scheduler:

| Column | Meaning |
|---|---|
| **Agent** | The agent's display name |
| **Next run** | When the scheduler will fire it next |
| **Last run** | The previous fire time ("—" if it has never run) |
| **Missed runs** | The catch-up policy: ⟳ `catch up · 24h window` or `skip` (hover for the full explanation) |

### Cron agents table

Every agent with `trigger: cron`, manageable in place: ID, name, cron expression, output bot, enabled state, and a live **Status** cell — **Running…** with a pulse while executing, **⏰ runs in 4m 12s** when a fire is imminent (within 15 minutes), or a plain countdown.

Per-row actions:

- **▶ Run** — manual trigger. If the next scheduled fire is imminent, a prompt asks whether to run now or wait (so you don't double-run). A running job can't be started again, and a scheduled fire is skipped while one is in progress.
- **📋 History** — slide-out panel of past runs, each with a ✓ success / ✗ failed badge, timestamp, channel, expandable full output, and token/cost metrics. A run counts as failed if any tool returned an error, even when the agent still produced a reply.
- **👁 Watch** — jumps to the Activity page with live polling already on.
- **Edit / Clone / Delete** — clone creates a disabled copy; delete removes the SOUL.yaml from disk.

## Missed-run catch-up

If the gateway is down (or asleep) at an agent's scheduled time, the default is to **skip** the missed fire. Agents that should make up for downtime opt in:

```yaml
schedule:
  cron: "0 7 * * *"
  run_missed_on_startup: true     # catch up after a restart
  missed_startup_window: "24h"    # how far back to look (default 24h)
```

In the GUI: **Edit** on the cron agent row → **Missed runs** → tick *Run the latest missed cron after startup* and set the window (`6h`, `24h`, `72h`, …).

The semantics are **latest-only**:

- Only the **most recent** missed fire inside the window runs, once, at startup.
- Older missed fires are never replayed.
- Completed fires are remembered across restarts, so nothing runs twice.

The schedule API reports the policy per entry as `catch_up` and `catch_up_window` — that is what the **Missed runs** column displays.

!!! tip
    A daily-briefing agent is the classic candidate: with `run_missed_on_startup: true` and a `24h` window, booting your machine at 9:30 still gets you the 7:00 briefing — exactly once.

## Scheduled output

By default a cron run's result stays internal (visible in History/Activity). To deliver it somewhere, configure `schedule.output` — in the Edit modal pick a **Bot** (any configured Telegram/Slack/Discord/WhatsApp bot), set the **Destination ID** (chat/channel/user ID), and optionally a **Template**:

```
{reply}            the agent's reply (default template)
{agent_id} {agent_name} {trigger} {timestamp}
```

!!! note
    Manual **▶ Run** returns the result to the page banner; only cron-fired runs are delivered through `schedule.output`.
