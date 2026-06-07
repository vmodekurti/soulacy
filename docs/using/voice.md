# Voice

Realtime voice in Chat lets you talk to an agent out loud — your microphone streams to the provider, the agent answers with audio, and the full transcript lands in your chat session.

## Quick start

1. Add to `~/.soulacy/config.yaml`:

    ```yaml
    voice:
      provider: openai            # only "openai" is supported (v1)
      model: gpt-realtime-mini    # default; optional
      # base_url: ""              # override for Azure/compatible endpoints

    llm:
      providers:
        openai:
          api_key: "sk-..."       # or set the OPENAI_API_KEY env var
    ```

2. Restart the gateway (Config page → **Restart Gateway**, or restart the service).
3. Open **Chat**, click the **🎤** button, and allow microphone access when the browser asks.
4. Talk. Click **⏹ 🎤** to end the session.

## Requirements

- `voice.provider: openai` in config — without it the voice button is disabled.
- An OpenAI API key in `llm.providers.openai.api_key` or the `OPENAI_API_KEY` environment variable.
- A browser context where `getUserMedia` works: **localhost or HTTPS**. The default local deployment (`http://localhost:18789`) qualifies; remote deployments need TLS.

Check availability without the GUI:

```bash
curl http://localhost:18789/api/v1/voice/status \
  -H "Authorization: Bearer $SOULACY_API_KEY"
# → {"available": true, "provider": "openai", "model": "gpt-realtime-mini"}
```

## How a session works

Clicking **🎤** walks through these steps:

1. The gateway mints a short-lived **ephemeral client key** (`POST /api/v1/voice/ephemeral`). Your real API key never reaches the browser.
2. The browser asks for **microphone permission** and opens a WebRTC connection **directly to the provider** — audio never transits the Soulacy gateway, so there is no added latency.
3. The button shows ⏳ while connecting, then pulses red while **live**. A system line ("🎤 voice session started") marks the start in the chat.
4. Speak naturally — the provider's server-side voice activity detection handles turn-taking and barge-in.
5. Click the button again (⏹) to stop; the mic is released and "🎤 voice session ended" is appended.

### Transcripts attach to the session

As you talk, transcripts render live in the chat:

- your completed utterances appear as user messages,
- the assistant's spoken reply streams in as a growing assistant bubble, finalized when the turn ends.

They are part of the same chat session as your typed messages — text only, no audio blobs are stored.

### Usage chip

Once the session has consumed tokens, a usage chip appears in the header:

```
🎤 ↑1234 ↓567 tok
```

That is the running input/output token total for realtime voice in this session (hover it to see the model).

## When voice is not configured

The feature degrades gracefully:

- No `voice.provider` set → the **🎤** button is disabled; its tooltip explains what to configure ("no realtime voice provider configured — set voice.provider and an OpenAI API key in config.yaml").
- Provider set but no API key → `/api/v1/voice/status` reports unavailable with the reason, and the button stays disabled.
- A failed session shows **⚠ 🎤**; click it once to reset to idle and try again.

Everything else in Chat keeps working normally — voice is purely additive.

!!! note
    Voice minutes are billed by the provider per audio token and cost noticeably more than text chat. The usage chip exists so you always see what a session consumed.

!!! tip
    If the browser never asks for the microphone, check the site permission settings — a previously denied mic permission silently fails the session with **⚠ 🎤**.
