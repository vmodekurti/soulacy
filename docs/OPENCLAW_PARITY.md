# Soulacy vs OpenClaw — Current Parity Snapshot

**Date:** 2026-07-05
**Posture:** Close the gaps that make Soulacy feel production-ready. Do not copy OpenClaw's entire surface area.

## TL;DR

Soulacy has closed the original load-bearing parity gaps from the June analysis:

- Guided setup exists through `sy onboard` and the GUI First Run page.
- Health checks exist through `sy doctor`, the Provider/Secrets Doctor API, and GUI provider/channel checks.
- Daemon management exists through `sy daemon`.
- Soulacy can act as an MCP server through `sy mcp serve`.
- Browser automation can be mounted through MCP with a headless Playwright quick-start.
- Queues, Knowledge, Workboard, schedules, and agents are exposed through platform tools.
- Studio now has validation, autowiring, runtime diagnosis, and build-until-it-works repair loops.

The remaining parity work is not "add more primitives." It is productization: mobile companion polish, release packaging, launch documentation, regression suites, and broader channel/community polish.

## Updated Parity Matrix

| Area | OpenClaw | Soulacy | Status |
|---|---|---|---|
| Install | Node/npm based install | Go binary with GUI embed; `make install` installs `soulacy` and `sy` | Strong once release binaries are published |
| Onboarding | `openclaw onboard` | `sy onboard` plus GUI First Run | Closed |
| Daemon | CLI-managed daemon | `sy daemon` for service lifecycle | Closed |
| Doctor | Broad doctor checks | `sy doctor`, GUI/API provider and channel doctor, support bundle | Mostly closed; keep expanding from real failures |
| Channels | Very broad channel list | HTTP, Telegram, Slack, Discord, WhatsApp/WhatsApp Web, plus webhook/MCP extension path | Deliberate narrower core |
| MCP client | External MCP tools | External MCP tools | Closed |
| MCP server | Exposes OpenClaw to MCP clients | `sy mcp serve` exposes agents, chat, schedules, Workboard, KB, queues | Closed |
| Browser automation | Native/plugin browser control | Playwright MCP sidecar, headless template, process cleanup | Closed for MVP |
| Studio/canvas | Assistant canvas/workflow surfaces | Studio workflow canvas, ReAct/Plan-Execute authoring, self-heal, run traces | Stronger for auditable workflows |
| Memory/learning | Persistent memory and skill learning | Episodic/semantic/procedural memory, proposals, accepted skill injection | Strong, still needs polished narrative |
| Queues | Plugin/storage primitives | Built-in ephemeral queue tools and GUI | Closed |
| Mobile companion | Native apps | Responsive Mobile operations page | Partial |
| Voice | Voice/wake/talk-back | Voice spike and API foundations, not productized | Intentional deferral |
| Auto-update | npm/Sparkle-style update story | No release updater yet | Open |

## Soulacy Advantages To Preserve

- **Local-first, inspectable runtime:** SOUL.yaml stays on disk and is easy to diff, review, copy, or repair.
- **Single binary posture:** The production runtime should not require Node after build.
- **Studio as one-stop agent development:** Studio owns generation, validation, testing, debugging, and repair.
- **Safer operating model:** Capability tiers, tool confirmations, RBAC, vault-backed secrets, and doctor checks are first-class.
- **MCP as the extension layer:** Instead of copying every adapter, Soulacy can expose itself as MCP and mount outside MCP servers.
- **Unified memory model:** Episodic, semantic, and procedural memory are part of one runtime story.

## Remaining Launch Gaps

1. **Release packaging:** Signed/notarized builds, release archives, install smoke tests, and an update/rollback story.
2. **Regression suite:** Golden UAT agents for weather, stock screening, document ingestion, scheduled channel delivery, queue processing, Slack/Telegram/Discord, Studio build/repair, and MCP sidecars.
3. **Mobile/PWA companion:** Better phone-sized operations, activity, approval, schedule, and chat flows.
4. **Channel polish:** Clear default outbound routing, agent-specific bot mappings, delivery tests, and onboarding copy for Telegram/Slack/Discord.
5. **Public docs:** A hosted docs site, quickstarts, production deployment guide, channel setup guides, and troubleshooting playbooks.
6. **Browser control hardening:** Domain policies, capture artifacts, screenshots/traces in Activity, and per-agent browser permissions.
7. **Voice/product narrative:** Decide whether voice is a launch requirement or a post-launch differentiator.

## Current Recommendation

Do not add new architectural primitives until the UAT suite is in place. The platform now has enough core machinery; the next competitive leap is proving that generated agents run reliably across real workflows, restart cleanly, deliver to channels, and can be debugged by ordinary users from Studio.
