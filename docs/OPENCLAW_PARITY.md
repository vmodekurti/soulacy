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
- Verified update checks and installs exist through `sy update check/install`, and launch readiness reports whether a production update manifest is configured.

The remaining parity work is not "add more primitives." It is productization: mobile companion polish, release packaging, launch documentation, regression suites, and broader channel/community polish.

## Updated Parity Matrix

| Area | OpenClaw | Soulacy | Status |
|---|---|---|---|
| Install | Node/npm based install | Go binary with GUI embed; `make install` installs `soulacy` and `sy`; release packaging emits combined tarballs, checksums, manifest, `sy update check`, and verified `sy update install` | Strong once release binaries are published |
| Onboarding | `openclaw onboard` | `sy onboard` plus GUI First Run | Closed |
| Daemon | CLI-managed daemon | `sy daemon` for service lifecycle | Closed |
| Doctor | Broad doctor checks | `sy doctor`, GUI/API provider and channel doctor, support bundle, mobile delivery checks | Mostly closed; keep expanding from real failures |
| Channels | Very broad channel list | HTTP, Telegram, Slack, Discord, WhatsApp/WhatsApp Web, plus webhook/MCP extension path | Deliberate narrower core |
| MCP client | External MCP tools | External MCP tools | Closed |
| MCP server | Exposes OpenClaw to MCP clients | `sy mcp serve` exposes agents, chat, schedules, Workboard, KB, queues | Closed |
| Browser automation | Native/plugin browser control | Playwright MCP sidecar, headless template, process cleanup, per-agent domain policy docs, Browser trace page/API pinned in UAT | Closed for MVP |
| Multi-agent orchestration | Agent handoffs / teams | Peer agents, router agents, auto-delegate, transitive safety tiers, and opt-in parallel peer fan-out with deterministic result ordering | Strong for MVP |
| Studio/canvas | Assistant canvas/workflow surfaces | Studio workflow canvas, ReAct/Plan-Execute authoring, self-heal, run traces | Stronger for auditable workflows |
| Memory/learning | Persistent memory and skill learning | Episodic/semantic/procedural memory, proposals, accepted skill injection | Strong, still needs polished narrative |
| Queues | Plugin/storage primitives | Built-in ephemeral queue tools and GUI | Closed |
| Mobile companion | Native apps | Responsive Mobile operations page with approvals, active runs, retained run history, schedule actions, and delivery checks | MVP closed; native app remains deferred |
| Voice | Voice/wake/talk-back | Voice spike and API foundations, not productized | Intentional deferral |
| Auto-update | npm/Sparkle-style update story | Manifest-backed `sy update check/install`, checksum verification, dry-run, backups, rollback docs, and readiness/support-bundle visibility | MVP closed; signed/notarized distribution remains |

## Soulacy Advantages To Preserve

- **Local-first, inspectable runtime:** SOUL.yaml stays on disk and is easy to diff, review, copy, or repair.
- **Single binary posture:** The production runtime should not require Node after build.
- **Studio as one-stop agent development:** Studio owns generation, validation, testing, debugging, and repair.
- **Safer operating model:** Capability tiers, tool confirmations, RBAC, vault-backed secrets, and doctor checks are first-class.
- **MCP as the extension layer:** Instead of copying every adapter, Soulacy can expose itself as MCP and mount outside MCP servers.
- **Unified memory model:** Episodic, semantic, and procedural memory are part of one runtime story.

## Remaining Launch Gaps

1. **Release packaging:** Signed/notarized builds remain open; release archives, install smoke tests, checksums, manifest, update check, verified `sy update install`, launch-readiness update-manifest checks, support-bundle release metadata, upgrade docs, and rollback now exist.
2. **Regression suite:** Clean-runtime UAT now covers launch readiness, GUI/PWA, golden template presence/instantiation, queues, schedule, support bundles, update checks, Browser trace wiring, and opt-in live-model Studio build/repair traces. `sy eval` now supports reusable golden suites, tag filtering, repeat-based benchmark runs, fail-fast, secret-aware skips, latency p50/p95, token summaries, tool assertions, and channel-delivery assertions. Opt-in golden smokes now prove real Slack/Telegram/Discord delivery and Playwright MCP browser sidecar startup/tool discovery when credentials or local sidecar dependencies are available.
3. **Mobile/PWA companion:** Core phone-sized operations are now usable: approvals, active run links, recent run history, schedule run/test actions, and delivery health checks. Remaining work is richer mobile chat polish and native push packaging.
4. **Channel polish:** Default outbound routing, agent-specific bot mappings, dry diagnosis, live delivery tests, companion-surface checks, and opt-in credential-backed golden delivery smoke exist. Remaining work is sharper setup docs and collecting more real-world failure signatures into the doctor.
5. **Public docs:** A hosted docs site, quickstarts, production deployment guide, channel setup guides, and troubleshooting playbooks.
6. **Browser control hardening:** Domain policies, Browser trace wiring, action filters, trace export, screenshot gallery, and deep links exist. Remaining work is richer artifact capture from sidecars and more Activity-to-Browser affordances.
7. **Voice/product narrative:** Decide whether voice is a launch requirement or a post-launch differentiator.

## Current Recommendation

Do not add new architectural primitives unless they directly improve reliability or launch polish. The platform now has enough core machinery; the next competitive leap is polishing mobile chat/activity, proving live model repair loops, and making every failure explainable and recoverable from Studio or Activity.
