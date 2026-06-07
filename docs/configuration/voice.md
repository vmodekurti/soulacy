# Voice

Talk to your agents. The `voice:` block enables a push-to-talk voice panel
in the Chat view: the browser connects to the provider **directly over
WebRTC** using a short-lived key minted by the gateway — your real API key
never reaches the browser, and audio never transits the gateway.

```yaml
voice:
  provider: openai            # only "openai" is supported (v1)
  model: gpt-realtime-mini    # default; gpt-realtime for higher quality
  # base_url: ""              # override for Azure/compatible endpoints
```

## Reference

| Key | Default | Description |
|-----|---------|-------------|
| `provider` | `""` (disabled) | Realtime voice provider. Only `openai` is supported in v1. Empty = voice panel hidden |
| `model` | `gpt-realtime-mini` | Realtime model. `gpt-realtime` for higher quality at higher cost |
| `base_url` | `https://api.openai.com` | Override for Azure or OpenAI-compatible endpoints |

## API key sourcing

The OpenAI API key is **not** part of the `voice:` block. It comes from,
in order:

1. `llm.providers.openai.api_key` in `config.yaml`
2. The `OPENAI_API_KEY` environment variable

```yaml
llm:
  providers:
    openai:
      api_key: "sk-..."
```

If neither is set, `GET /api/v1/voice/status` reports
`"no OpenAI API key configured (set llm.providers.openai.api_key or
OPENAI_API_KEY)"` and the Chat panel hides the voice button.

## How it works

1. The Chat panel calls `GET /api/v1/voice/status` to check availability.
2. On push-to-talk, the browser requests `POST /api/v1/voice/ephemeral`;
   the gateway mints a short-lived client key from your real API key.
3. The browser opens a WebRTC connection straight to the provider with
   the ephemeral key. Mic audio and synthesized speech flow
   browser ↔ provider — never through Soulacy.

Both routes sit behind the same auth and RBAC surface as chat.

## Costs (ballpark)

Realtime voice is billed by the provider per audio token, which works out
to a per-minute rate well above text chat (list prices, June 2026):

| Model | Approximate cost |
|-------|------------------|
| `gpt-realtime-mini` (default) | ≈ $0.06–0.10 per minute of conversation |
| `gpt-realtime` | ≈ $0.18–0.24 per minute of conversation |

Underlying list rates for `gpt-realtime`: $32/$64 per 1M audio tokens
in/out plus $4/$16 per 1M text tokens. Treat these as ballpark figures —
check current provider pricing before enabling voice for a team.

## Troubleshooting

**Voice button missing or greyed out**

- `voice.provider` is unset — add the `voice:` block and restart.
- No OpenAI API key — check `GET /api/v1/voice/status` for the exact
  reason in the `detail` field.

**Microphone permission denied**

The browser must grant mic access to the GUI origin. If you previously
denied it, reset the permission in the browser's site settings (the lock
icon in the address bar) and reload.

**"getUserMedia is not available" / mic never prompts**

Browsers only expose microphone capture in a **secure context**:
`https://` or `http://localhost`. If you access the GUI over plain HTTP
on a LAN address (e.g. `http://192.168.1.10:18789`), voice cannot work.
Options:

- Access the GUI via `http://localhost:18789` (SSH port-forward from
  remote hosts: `ssh -L 18789:localhost:18789 user@host`).
- Terminate TLS — set `server.tls_cert` / `server.tls_key` (see
  [Server](server.md)) or put a reverse proxy with HTTPS in front.

**Ephemeral key request fails (503 / 502)**

- `503 no realtime voice provider configured` — `voice:` block missing.
- `503 voice provider not ready: …` — usually the missing API key.
- `502` — the provider rejected the mint request; check the key's
  validity and that your account has Realtime API access.

## See also

- [LLM providers](llm.md) — where the OpenAI key lives
- [API overview](../api/index.md) — `/voice/status`, `/voice/ephemeral`
- In-repo design notes: [`docs/VOICE_SPIKE.md`](../VOICE_SPIKE.md)
