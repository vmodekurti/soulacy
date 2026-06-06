# CLI Reference

The `soulacy` binary provides several commands.

## Global flags

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | `./config.yaml` | Path to config file |
| `--log-level` | `info` | Log level: `debug`, `info`, `warn`, `error` |
| `--log-format` | `json` | Log format: `json`, `text` |

---

## `sy doctor`

Run local diagnostics for a Soulacy installation.

```bash
sy doctor
sy doctor --json
```

Checks:

- config discovery
- runtime directories
- `agent_dirs`
- Python interpreter
- Ollama reachability
- knowledge database
- gateway health
- MCP server status

`sy doctor` exits nonzero only when a hard failure is found. Warnings identify suspicious but non-fatal configuration, such as a relative `agent_dirs` path or non-absolute `runtime.python_bin`.

---

## `sy agent validate`

Validate an agent `SOUL.yaml` before deploying it.

```bash
sy agent validate examples/agents/hello-world/SOUL.yaml
sy agent validate --file examples/agents/hello-world/SOUL.yaml --json
```

Checks:

- unknown or malformed YAML fields
- required `id`
- trigger and schedule consistency
- provider configuration and default-provider fallback
- live model availability when the provider can be queried
- model suitability warnings for tool-heavy or structured-output agents
- bad Go duration strings in timeouts
- negative numeric LLM and memory settings
- missing local `python_file` tool paths
- malformed `mcp_servers` and `mcp_tools` entries

When a configured provider exposes a model list, validation suggests better alternatives instead of merely saying "wrong model." Warnings keep exit code 0. Errors return nonzero, making the command useful in CI.

---

## `sy channel`

Manage channel adapters from the terminal.

```bash
sy channel list
sy channel status whatsapp_web
sy channel enable telegram
sy channel disable whatsapp_web
sy channel update whatsapp_web --set trigger_phrase='!soulacy' --set ignore_groups=true
```

Each adapter also has a first-class command namespace:

```bash
sy channel http status

sy channel telegram configure \
  --token "$TELEGRAM_BOT_TOKEN" \
  --agent assistant \
  --trigger '!soulacy' \
  --allowed-users '123456789'

sy channel slack configure \
  --bot-token "$SLACK_BOT_TOKEN" \
  --app-token "$SLACK_APP_TOKEN" \
  --agent assistant \
  --trigger '!soulacy'

sy channel discord configure \
  --token "$DISCORD_BOT_TOKEN" \
  --agent assistant \
  --guild '1234567890' \
  --trigger '!soulacy'

sy channel whatsapp configure \
  --phone-number-id "$WA_PHONE_NUMBER_ID" \
  --access-token "$WA_ACCESS_TOKEN" \
  --verify-token "$WA_VERIFY_TOKEN" \
  --app-secret "$WA_APP_SECRET" \
  --agent assistant \
  --trigger '!soulacy'
```

Every configurable adapter namespace supports:

```bash
sy channel <adapter> status
sy channel <adapter> enable
sy channel <adapter> disable
sy channel <adapter> configure ...
```

### Activation safety

Adapter-specific `configure` commands expose the same safety model:

- `--trigger` sets the wake phrase; messages must start with it.
- `--allow-groups` opts into group/server/channel activation.
- `--allowed-chats` restricts activation to specific platform destinations.
- `--allowed-users` restricts activation to specific senders.

### WhatsApp Web pairing

The CLI supports the same guarded WhatsApp Web setup as the GUI:

```bash
sy channel whatsapp-web pair --agent assistant
```

By default, pairing uses safe activation rules:

- messages must start with `!soulacy`
- group chats are ignored

Customize those rules explicitly:

```bash
sy channel whatsapp-web pair \
  --agent assistant \
  --trigger '!ask' \
  --allowed-chats '12345@s.whatsapp.net'
```

Allow group chats only when that is intentional:

```bash
sy channel whatsapp-web pair --agent assistant --allow-groups
```

Check connection state and the current QR payload:

```bash
sy channel whatsapp-web status
```

The CLI prints the QR pairing payload. Use the Channels GUI for a rendered QR
image, or pipe the payload into a trusted terminal QR renderer.

---

## `soulacy serve`

Start the Soulacy server.

```bash
soulacy serve [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | `./config.yaml` | Config file path |
| `--host` | from config | Override server host |
| `--port` | from config | Override server port |
| `--dev` | `false` | Enable development mode (pretty logs, no rate limits) |

### Examples

```bash
# Start with default config
soulacy serve

# Custom config path
soulacy serve --config /etc/soulacy/config.yaml

# Development mode
soulacy serve --dev --log-format text
```

---

## `soulacy validate`

Validate a config file and all SOUL.yaml files without starting the server.

```bash
soulacy validate --config config.yaml
```

Output:

```
✓ config.yaml — valid
✓ agents/assistant.soul.yaml — valid
✓ agents/researcher.soul.yaml — valid
✗ agents/broken.soul.yaml — unknown field: modell (did you mean model?)
```

---

## `soulacy agent`

Manage agents from the CLI.

### List agents

```bash
soulacy agent list --config config.yaml
```

Output:

```
ID           MODEL          CHANNELS          TOOLS
assistant    gpt-4o-mini    http,telegram     -
researcher   gpt-4o         http              web_search,url_fetch
```

### Chat with an agent (REPL)

```bash
soulacy agent chat assistant --config config.yaml
```

Starts an interactive REPL session in the terminal:

```
Session: sess_abc123
Type your message and press Enter. Ctrl+C to exit.

> Hello!
assistant: Hi! How can I help you today?

> What's 2 + 2?
assistant: 4.
```

---

## `soulacy keys`

Manage API keys.

### Create a key

```bash
soulacy keys create --name ci-bot --role operator --config config.yaml
```

Output:

```
API Key created:
  ID:   ak_abc123
  Name: ci-bot
  Role: operator
  Key:  sk_xxxxxxxxxxxxxxxxxxxxxxxxxx  ← save this, shown once
```

### List keys

```bash
soulacy keys list --config config.yaml
```

### Revoke a key

```bash
soulacy keys revoke ak_abc123 --config config.yaml
```

---

## `soulacy costs`

View cost summaries from the CLI.

```bash
# 30-day summary
soulacy costs summary --config config.yaml

# Per-agent breakdown
soulacy costs breakdown --config config.yaml --period 7d

# Filter by agent
soulacy costs breakdown --agent researcher --period 30d
```

---

## `soulacy version`

Print version and build information.

```bash
soulacy version
# Soulacy v0.1.0 (go1.25 linux/amd64) built 2026-05-28
```
