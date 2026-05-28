# CLI Reference

The `soulacy` binary provides several commands.

## Global flags

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | `./config.yaml` | Path to config file |
| `--log-level` | `info` | Log level: `debug`, `info`, `warn`, `error` |
| `--log-format` | `json` | Log format: `json`, `text` |

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
