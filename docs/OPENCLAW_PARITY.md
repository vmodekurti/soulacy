# Soulacy vs OpenClaw — competitive parity analysis

**Date:** 2026-06-08
**Author:** Vasu + Claude
**Posture:** *competitor to differentiate from* — sharpen what Soulacy does that OpenClaw doesn't, only close gaps where parity is load-bearing for our positioning. Do NOT chase OpenClaw's surface area.

---

## TL;DR

OpenClaw is a Node-based, multi-thousand-source-file personal AI assistant with 24 channel adapters, 40+ LLM providers, native macOS / iOS / Android companion apps, voice wake / talk-back, a Canvas (A2UI) mode, an Onboard wizard, and a Sparkle auto-update pipeline. It's heavily funded (OpenAI / NVIDIA / GitHub sponsors visible), with ~18,000 commits in the last 30 days. **We will not match this surface area, and we shouldn't try.** We close 3 specific gaps where parity sharpens our existing differentiation (dumb-install, opinionated, single binary), and explicitly diverge on everything else.

**Recommended implementation shortlist (this session, after your pick):**

1. **`sy onboard` — guided first-run wizard** that beats OpenClaw's at the dumb-install game (no Node, no plugin install, runs against our already-bootstrapped workspace). [HIGH leverage, ~1 day]
2. **`sy doctor` expansion — explainable health checks** with a small fraction of OpenClaw's 140 checks, but covering the 10 things that actually go wrong (config, port, providers, channels, sandbox, db). [MEDIUM leverage, ~half day]
3. **Daemon install / auto-start** — `sy daemon install` for launchd (macOS) / systemd (Linux). Currently we only set up LaunchAgent inside install.sh; this makes it a first-class CLI surface that survives upgrades. [LOW-MEDIUM leverage, ~half day]

Anything else listed below is in the "diverge" or "ignore" column.

---

## What OpenClaw actually is

A personal AI assistant for technically-comfortable single users. Terminal-first by design (`openclaw onboard` is the recommended setup path). It answers you on the channels you already use (WhatsApp, Telegram, Slack, Discord, iMessage, plus 19 more), can talk and listen on macOS / iOS / Android, and can render an agent-controlled "Canvas" surface. Implemented as a Node 22+ gateway with ~140 plugins under `extensions/`, a SQLite-only storage policy, and three native companion apps. Origin myth: Warelay → Clawdbot → Moltbot → OpenClaw.

It is **not what Soulacy is.** Soulacy is a *self-hosted agent runtime* — a single Go binary you run on a laptop or VPS, with YAML agents, capability tiers, and zero Node toolchain. OpenClaw's product is "the assistant"; Soulacy's product is "the runtime your assistant runs on." That framing matters for everything below.

---

## Parity matrix

Columns: **OpenClaw** = what they have today. **Soulacy** = what we have today. **Recommendation** = one of `CLOSE` (worth matching), `DIVERGE` (deliberately do the opposite or skip), `IGNORE` (out of scope for our positioning, don't worry about it).

### Install & first-run

| Aspect | OpenClaw | Soulacy | Recommendation |
|---|---|---|---|
| Install command | `npm install -g openclaw` (needs Node 22+) | `curl … \| bash` (needs Go + npm to build) | **DIVERGE** — Ours requires no runtime once built; theirs requires Node at runtime. We win this dimension *once we ship prebuilt binaries*. Tier 3 of our INSTALL_UX_GAPS.md. |
| Onboarding wizard | `openclaw onboard` — Clack TUI, ~13 steps, writes config, installs daemon, migrates from Claude Code/Hermes, atomic config rewrites | None. `soulacy serve` does silent first-run bootstrap; we never *ask* the user anything | **CLOSE** — `sy onboard` is the single highest-leverage thing on this list. We can do it cleaner than them because we don't have plugin installs to manage. |
| Daemon / auto-start | `openclaw daemon install` registers launchd/systemd user service via CLI; survives upgrades | Only as an interactive prompt inside `install.sh` (macOS LaunchAgent). Not a CLI surface. | **CLOSE** — promote to `sy daemon {install,uninstall,status}`, mirror across launchd + systemd. |
| Doctor / health | ~140 `doctor-*.ts` checks (config, auth, sandbox, gateway-health, post-upgrade migrations) | `sy doctor` — basic workspace + agent_dirs + python_bin + KB db checks | **CLOSE (small)** — we don't need 140; we need the 10 that catch real failures. Add: provider reachability, port availability, GUI dist embed sanity, sandbox enabled, recent error rate from action log. |
| Auto-update | Sparkle on macOS app (`appcast.xml`); `openclaw update` for npm | None | **DIVERGE** — auto-update of a single binary is solvable later via `soulacy update` that pulls the latest GitHub release. Don't build now; punt until we have releases at all. |

### Channels (the OpenClaw headline number)

| Aspect | OpenClaw | Soulacy | Recommendation |
|---|---|---|---|
| Adapters | 24 channel plugins + WebChat surface (Slack, Discord, Telegram, WhatsApp, iMessage, Signal, Matrix, Teams, Google Chat, Feishu, LINE, Mattermost, Nextcloud Talk, Nostr, IRC, Twitch, SMS, QQ, Zalo, Zalo personal, Synology Chat, Tlon, WeChat, ClickClack) | Built-in: HTTP, Telegram, Slack, Discord, WhatsApp Web. 5 adapters. | **DIVERGE** — adding 19 channel adapters would be a quarter of work. Our positioning lets us bet on `kind: router` + MCP + webhook as the *generic* way to add any channel. Document that explicitly in README instead. |
| Channel breadth claim | "supports 23+ channels" | "Telegram, Slack, Discord, WhatsApp out of the box; any HTTP-receiving service via webhook adapter" | **CLOSE (cheap)** — the *honest* version of our story is already strong. Just say it well in README. Adding a generic webhook→router adapter (~50 LoC) makes "any channel" provably true. |
| DM safety / pairing | First-class pairing flow with `openclaw pairing approve <channel> <code>` — DMs locked down by default | We have capability tiers (ReadOnly/Active/Privileged) + `accept_privileged_exposure` on binding | **DIVERGE (we're already ahead)** — our capability-tier model is more principled than their pairing flow. Make sure the GUI surfaces it well. |

### LLM providers

| Aspect | OpenClaw | Soulacy | Recommendation |
|---|---|---|---|
| Provider extensions | ~40 (OpenAI, Anthropic, Bedrock, Google, Cerebras, Cloudflare, DeepSeek, Fireworks, Groq, HuggingFace, Kimi, LMStudio, Microsoft Foundry, Mistral, Moonshot, Novita, NVIDIA, Ollama, OpenRouter, Perplexity, Qianfan, Qwen, SGLang, StepFun, Together, Venice, Vercel AI, vLLM, Volcengine, Voyage, xAI, Zhipu, …) | OpenAI, Anthropic, Ollama, Groq, Mistral, OpenRouter — plus arbitrary OpenAI-compatible via `customId` (this just shipped in Providers.svelte) | **IGNORE** — `customId` + OpenAI-compatible endpoint covers ~90% of OpenClaw's provider list (DeepSeek, Together, Fireworks, etc. are all OpenAI-compatible). The differentiating ones (Bedrock IAM auth, Vertex SA auth, native xAI) can be added one-at-a-time when a user actually asks. |
| Provider routing | Internal `provider-runtime` with retry, model picker, fallback | Per-agent `default_provider`, `allowed_providers` allowlist | **DIVERGE** — we have `allowed_providers` which they don't (or it's buried). Make sure it's surfaced in the GUI. |

### Voice

| Aspect | OpenClaw | Soulacy | Recommendation |
|---|---|---|---|
| Voice runtime | `src/talk/` (84+ files) — turn-taking, agent-talkback, fast context, forced-consult coordinator | None | **IGNORE** — voice is a quarter of work and a different product. Soulacy is text-first; that's fine. |
| TTS / STT providers | ElevenLabs, Deepgram, Azure, Inworld, Minimax, Senseaudio, OpenAI, local MLX TTS, local sherpa-onnx | None | **IGNORE** |
| Wake word daemon | `swabble` (separate Swift package, "clawd" wake word) | None | **IGNORE** |
| Voice on iOS / Android | Native apps with realtime Talk playback, continuous voice tab | None | **IGNORE** |

### Native apps

| Aspect | OpenClaw | Soulacy | Recommendation |
|---|---|---|---|
| macOS app | SwiftUI menu bar, 238 Swift files, 117 tests, Sparkle auto-update, Canvas client, Peekaboo automation | ClawStackMac (SwiftUI executable, REST client, sidebar nav, visual canvas builder) | **DIVERGE** — we already have a Mac app at ~/Documents/Development/agenticai/clawstack-mac/. Rename + ship it as the official Soulacy Mac companion. Don't try to match Sparkle / menu bar / Peekaboo *now*; first ship what we have under the right brand. |
| iOS app | "Super Alpha, internal-use only" — TestFlight only | None | **IGNORE** — even OpenClaw is alpha here. Punt indefinitely. |
| Android app | "extremely alpha, actively being rebuilt" | None | **IGNORE** — same as iOS. |
| Windows Hub | Native WinUI app, signed installers via GitHub releases — **not in this repo** | None | **IGNORE** — they market it loudly but it lives elsewhere. Building a WinUI app to compete is absurd for us. |

### Skills / agents / memory

| Aspect | OpenClaw | Soulacy | Recommendation |
|---|---|---|---|
| Agent definition format | Runtime in `openclaw.json` + per-agent SQLite | YAML (SOUL.yaml) on disk in `~/.soulacy/soulspace/agents/<id>/SOUL.yaml` | **DIVERGE (we're better)** — file-on-disk YAML beats runtime JSON for git-versioning, code-review, and "I can read what my agent does." Lean into this. |
| Skills | `SKILL.md` files with YAML frontmatter — 58 bundled, plus workspace + plugin dirs. New "Skill Workshop" lets agents *propose* new skills | We have skills as a concept but not a Workshop equivalent | **CLOSE (lightweight)** — already have a skills pipeline; document the SKILL.md convention so our format is interchangeable with theirs. Free interop win. |
| Memory model | `active-memory` + `memory-core` ("Dreaming" — light/REM/deep sweeps via cron) + `memory-lancedb` + `memory-wiki`. Their VISION admits this is unresolved — they ship 4 competing memory plugins. | Episodic / semantic / procedural tiers with provenance (per memory) — one model, not four | **DIVERGE (we're better)** — one unified model is the right answer. They will eventually consolidate to one; we already did. Mention this in CRITIQUE_RESPONSE. |
| Multi-agent | Workboard (new, 2026.6.x) — orchestration primitives, task-backed board runs | `kind: router` agent + agent-as-tool dispatcher + `Agents []string` field (researcher+writer example). Capability tiers cycle-detected. | **DIVERGE (we ship cleaner)** — router-as-agent is more elegant than Workboard's orchestration objects. |
| Sandboxing | Docker default for non-main sessions; SSH, OpenShell backends | Self-reexec subprocess sandbox (rlimits, no Docker required) | **DIVERGE (we're better for single-machine)** — no Docker dependency = dumb install actually works on a fresh laptop. |

### Storage, distribution, MCP

| Aspect | OpenClaw | Soulacy | Recommendation |
|---|---|---|---|
| Storage policy | SQLite-only, enforced by lint (`pnpm lint:kysely`). 4 DBs (state, per-agent, plugin KV, sessions) | SQLite for actions.db / archive.db / knowledge db / credentials.db. Files for SOUL.yaml | **DIVERGE** — file-based SOUL.yaml is a strength. Don't move agents into SQLite just because they did. |
| MCP support | Server (expose OpenClaw tools as MCP) + client (mount external MCP servers) | Client only (mount external MCP servers via `mcp-servers/`) | **CLOSE (high leverage, separate session)** — expose Soulacy as an MCP server so Claude Desktop / Cursor / etc. can use Soulacy agents as tools. Major distribution lever. Don't do this session; flag as next priority. |
| ClawHub registry / plugin marketplace | Yes — npm-packaged plugins discoverable via clawhub.ai | None | **IGNORE** — they need a marketplace because they have 140 plugins. We don't. Plugins as MCP servers is the marketplace. |
| Auto-update for binary | npm `update` command for the gateway + Sparkle for the Mac app | None | **PUNT** — needed eventually (`soulacy update` via GitHub releases), but no point until we have releases. |
| Observability | OpenTelemetry + Prometheus extensions | Prometheus `/metrics` endpoint shipped (Bundle J) | **DIVERGE (we're there)** — already have Prometheus. Add OpenTelemetry only if a user asks. |

### UX / polish

| Aspect | OpenClaw | Soulacy | Recommendation |
|---|---|---|---|
| README | 86KB, multi-language, sponsor logos, badge wall, docs deep-links | ~10KB, focused. Just rewrote install section. | **DIVERGE** — short README is a feature. Don't bloat. |
| Documentation site | `docs.openclaw.ai` (hosted) — getting started, install, platforms, FAQ, troubleshooting | Inline in `docs/` directory; no hosted site | **CLOSE (low priority)** — publish `docs/` as a static site via GitHub Pages. Tier 3 polish, not this session. |
| Discord community | Discord server (`discord.gg/clawd`) | None | **IGNORE for now** — community follows users; ship something users want first. |
| Sponsor logos | OpenAI, NVIDIA, GitHub, Vercel, Blacksmith, Convex | None | **IGNORE** — irrelevant signal to a self-hoster. |

---

## What Soulacy has that OpenClaw doesn't (lean into these)

Worth highlighting in README, marketing, and CRITIQUE_RESPONSE because these are real:

1. **Single Go binary, no Node toolchain at runtime.** Their install requires Node 22+ permanently on the host. Ours requires Node *once at build time* (for the embedded GUI) and then never again. Once we ship prebuilt binaries, even that goes away.
2. **YAML-on-disk agent definitions** (SOUL.yaml). Git-versionable, code-reviewable, human-editable. Theirs are runtime config + SQLite — opaque.
3. **Capability tiers** (ReadOnly / Active / Privileged) with cycle-detected peer-graph walk + `accept_privileged_exposure` on the binding. We didn't have this last week — now we do. OpenClaw has *pairing*, which is a different and weaker primitive: pairing is "is this contact allowed to message", tiers are "is this agent allowed to be exposed on this channel".
4. **Unified memory model** (episodic / semantic / procedural with provenance) versus their four-competing-plugins problem.
5. **`kind: router` agent** — more elegant than Workboard's orchestration objects. Routing is just an agent kind, dispatch reuses peer-call machinery.
6. **First-run config + key auto-generation** (`internal/config.EnsureBootstrap`). OpenClaw makes you walk through 13 wizard steps. We make zero steps — *and we can still add a wizard for users who want one* (gap 1 below).
7. **No Docker required.** Their default sandbox is Docker-based; non-main sessions need a container runtime. Ours is a self-reexec subprocess with rlimits. Works on a stock Mac with no setup.
8. **`soulacy build` flavored binaries** — operators can compile a custom distribution including their own drivers. They have plugin install via npm, which is heavier.

---

## Recommended implementation shortlist (pick one or two)

In priority order. Each estimates how much it'd take and what we get out of it.

### Pick 1: `sy onboard` — first-run wizard *(~1 day, HIGHEST leverage)*

Cobra subcommand that wraps the soulacy serve bootstrap with a 5-step Clack-style TUI:

1. Where should the workspace live? *(default `~/.soulacy/soulspace`, pre-validated against `ResolveWorkspace`)*
2. Bind to a loopback (recommended) or expose? *(loopback default; non-loopback prompts for auth choice)*
3. Pick a default LLM provider — Ollama (auto-detect localhost:11434), OpenAI (paste key), Anthropic (paste key), or "skip for now".
4. Want a starter agent? *(`basic-chat` default; can also pick from templates)*
5. Install a daemon so `soulacy serve` runs on login? *(launchd / systemd)*

State persists into `config.yaml` via the same EnsureBootstrap surface — wizard just *fills* fields the bootstrap would have left blank. No new file format.

**Why this and not the others:** ours can be cleaner than OpenClaw's because we already have the silent bootstrap working. The wizard becomes a thin layer that *replaces* default values, never a precondition for things working.

### Pick 2: `sy doctor` v2 — explainable health checks *(~half day, MEDIUM leverage)*

Don't build 140 checks. Build 10 that catch the actual failure modes:

1. Workspace path exists and writable
2. Config file parseable
3. Port 18789 free (or configured port)
4. At least one provider has a key set
5. Provider endpoint reachable (HEAD request, 2s timeout)
6. Python binary resolvable (already checked)
7. KB db exists and openable (already partial)
8. Sandbox enabled when `runtime.sandbox.enabled: true`
9. Agent count > 0 (warn if 0 — "run `sy onboard` or copy a SOUL.yaml")
10. Recent error rate from action log < threshold

Each check returns `(status, summary, fix-hint)`. Print in a table. Exit non-zero on any failure. JSON output mode for scripts (`--json`).

### Pick 3: `sy daemon install` — first-class auto-start *(~half day, LOWER leverage)*

Promote the LaunchAgent setup from inside install.sh to a CLI surface:

```
sy daemon install    # writes launchd plist (mac) / systemd user unit (linux)
sy daemon uninstall  # removes + unloads
sy daemon status     # is it loaded? recent failures?
sy daemon logs       # tail
```

Cleanly survives `soulacy` upgrades — the plist points at a stable `~/.local/bin/soulacy`. OpenClaw has this; we should too.

---

## What to explicitly NOT do (deferred or rejected)

- ❌ Native iOS / Android apps. Even OpenClaw's are alpha; we don't have the platform expertise to ship this.
- ❌ Voice. Different product. Punt indefinitely.
- ❌ Canvas / A2UI. Cool but niche; not in our positioning.
- ❌ Adding 19 channel adapters. Wrong move. We have a generic router and HTTP webhook surface — document those as the "any channel" answer.
- ❌ A plugin marketplace. We don't have enough plugins to need one. MCP is the marketplace.
- ❌ Bloated README. Our 10KB README beats their 86KB one for first-impression clarity.
- ❌ Sparkle / auto-update. Punt until we ship binary releases (Tier 3 of INSTALL_UX_GAPS.md).
- ❌ Match-the-doctor-check-count. Their 140 is signaling, not value. 10 well-chosen checks is better than 100 noisy ones.
- ❌ Multi-user / RBAC. They explicitly don't have it either; both projects are personal-assistant model. Don't accidentally rebuild org auth.

---

## Decision request

Pick **one** (or two if they don't overlap) of these to implement now:

- **A.** `sy onboard` wizard
- **B.** `sy doctor` v2
- **C.** `sy daemon install`

Once you've picked, I'll go straight to implementation in this session — no further discussion needed.
