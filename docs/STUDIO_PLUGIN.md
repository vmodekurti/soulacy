# Soulacy Studio — Design & Roadmap

**Studio** is the visual, plain-language workflow builder for Soulacy, shipped as
a **plugin** (intent compiler + builder UI + capability catalog + test harness),
not core. It is a layer on top of primitives Soulacy already has — it emits
`WorkflowSpec` / SOUL.yaml and never introduces a parallel runtime.

## Vision

The user describes intent in plain words; the framework compiles it into a
runnable workflow — choosing the trigger (channel, cron, webhook, manual), the
agent step(s), and the tool/skill/MCP bindings — then lets the user visually
inspect, adjust, and **test it end-to-end** before it goes live. The target user
knows *what they want to happen*, not how Soulacy wires it.

## Settled decisions

- **Surface:** web plugin UI in the gateway portal, built on `@xyflow/svelte`
  (the React-Flow family Langflow uses). The hand-rolled SwiftUI companion canvas
  is retired; the companion, if kept, embeds this web core later.
- **Intent engine:** hybrid — compile a draft graph immediately, then ask
  clarifying questions only where genuinely ambiguous or where a credential /
  permission is needed.
- **First milestone:** a thin vertical slice — intent → single-agent flow with
  one trigger + one tool/MCP, rendered on the canvas, runnable in the harness.

## Architecture (grounded in the current repo)

- **Plugin packaging.** Manifest `plugin.yaml` (schema v2, `pkg/plugin`), loaded
  by `internal/plugins/loader.go` from the dirs in `config.plugin_dirs`
  (`internal/config/config.go`; defaults to `<workspace>/plugins`). UI mounts via
  `gui.static` and is served at `GET /plugins/:id/ui/*`; the portal shows it in a
  sandboxed iframe (`allow-scripts allow-forms`, no same-origin) with a scoped
  plugin token (`splg_…`) passed in the URL fragment (`gui/src/lib/pluginui.js`,
  `gui/src/pages/PluginFrame.svelte`).
- **Flow model (extended in M0).** `sdk/reasoning/flow.go` — `FlowNode`,
  `FlowEdge`, `FlowSpec`; compiled/run by `internal/reasoning/flow.go`. M0 added
  append-only typed ports (`FlowPort`, `FlowNode.Inputs/Outputs`,
  `FlowEdge.FromPort/ToPort`) and typed `FlowNode.Params`, all backward compatible.
- **Capability sources.** Existing endpoints expose what Studio needs to read:
  `GET /api/v1/agents`, `/tool-catalog`, `/providers`, `/skills`, `/mcp`. Registry
  discovery via `internal/pkgregistry` (skills.sh + git).

### Constraint: plugins cannot register `/api` routes

All gateway routes are wired at startup (`internal/gateway/server.go`); plugin
UIs use the existing API surface only. So Studio's M1 needs (intent-compile,
workflow save, flow test/run) will require **new core endpoints**, planned as core
work — not plugin-local code.

### Open decision: plugin access to the capability catalog

The M0 UI cannot yet read `/agents`, `/tool-catalog`, `/providers` with its plugin
token: the cap registry (`internal/caps/caps.go`) defines only `vector.search`,
`channel.send`, `events.subscribe` (each tied to a *scope kind* of
agents/channels/types), and the plugin route gate
(`internal/gateway/plugins.go` `pluginRoutePolicy`) admits plugin principals only
to `/health` and `/knowledge/*/search`. A global "read the catalog" grant does not
map onto the existing scope-kind model, so this is a deliberate design choice to
make with care. Options for M1:

1. **Scoped read caps** — register e.g. `agents.read` / `tools.read` /
   `providers.read`, add `pluginRoutePolicy` entries, grant them in the manifest.
   Simple, but extends the plugin security surface and stretches the scope model.
2. **Host-provided context (preferred to evaluate first)** — the parent
   `PluginFrame` (which already holds the user session) fetches the catalog and
   hands it to the iframe via `postMessage`. No new plugin permissions; a generic
   "host context" channel reusable by other plugins.

Until decided, the M0 UI degrades gracefully (handles the 403 with per-group
error rows + a status banner).

## Milestone / story map

- **M0 — Foundations.** Plugin scaffold + UI shell (S0.1); typed flow model
  (S0.3). *Status: built, see below.* (S0.2 capability catalog blocked on the
  decision above.)
- **M1 ★ thin slice.** Intent compiler v1 (S1.1), canvas render + inspector
  (S1.2), clarify round-trip (S1.3), test harness v1 (S1.4), save & deploy as a
  disabled workflow (S1.5). Requires the new core endpoints noted above.
- **M2 — Triggers & channels.** Trigger node types; compiler infers trigger from
  intent; capability-tier consent on privileged bindings.
- **M3 — Multi-agent & branching.** Typed multi-handle ports, conditional edges,
  `kind: router` integration, multi-step compilation.
- **M4 — Capability discovery & auto-provision.** Suggest + one-click install
  missing tools/skills/MCP via the registries.
- **M5 — Test depth.** Live vs dry-run, assertions, editable mocks, run history.
- **M6 — Templates, library, polish.** Start-from-template (absorbs the empty
  agent-list templates-picker item), save/clone/version, re-edit a node by
  re-describing it, export/share.

## M0 implementation status

- **Typed flow model (S0.3):** `sdk/reasoning/flow.go` + `internal/reasoning/flow.go`,
  append-only, backward compatible. New tests green: `TestFlowPorts_JSONRoundTrip`,
  `TestFlowSpec_BackwardCompatZeroValues`, `TestCompileFlow_PortValidation`,
  `TestCompileFlow_ParamsPreserved`, `TestRunFlow_PortsAndParamsRegression`,
  `TestFlowPorts_YAMLRoundTrip`.
- **Plugin scaffold + UI shell (S0.1):** `examples/plugins/studio/` — v2 manifest,
  token-aware sandboxed-iframe UI (vanilla JS, no build) that renders a palette /
  canvas placeholder / intent input and fetches the catalog (currently 403,
  handled gracefully). Loads in dev via `plugin_dirs: [examples/plugins]` in the
  gitignored `config.dev.yaml` (same convention as `agent_dirs: examples/agents`);
  for a real install, copy `studio/` into `~/.soulacy/soulspace/plugins/`.

## M1 Wave 1 status (in progress)

- **Host-RPC bridge + live palette (S1.0/S0.2):** `gui/src/pages/PluginFrame.svelte`
  now fetches the catalog with the user session and relays it to the sandboxed
  Studio iframe via a whitelisted `postMessage` bridge (`catalog.request` →
  `catalog.response`, id-correlated, source-checked). `examples/plugins/studio/ui/app.js`
  renders the real palette from it (graceful direct-fetch fallback). No plugin-token
  permission change. gui vite build green.
- **Intent compiler + compile endpoint (S1.1):** new `internal/studio` package
  (BuildPrompt / ParseDraft / Compile behind a narrow `LLM` interface) and
  `POST /api/v1/studio/compile` (`internal/gateway/studio.go`, registered in
  `server.go`, reuses `rbac.ResourceAgents`+`ActionWrite`, adapts `llmRouter`).
  Hybrid: returns a draft workflow AND clarifying questions. 10 unit tests green
  (pinned to the canonical HN-digest example), gofmt/vet clean.
- **Verification gate:** `internal/studio` is fully verified in-sandbox; the gateway
  package can only be compiled where CGO + sqlite headers exist — confirm with
  `make all` on the Mac before pushing.

## Vasu's open questions

Plugin name (keep "Studio"?); intent compiler as agent vs skill; how "live" M1
deploy is (register disabled until flipped on — current assumption); companion's
eventual fate; a concrete first-run example workflow to shape the M1 slice around.
