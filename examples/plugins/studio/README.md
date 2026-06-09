# Studio plugin (Story S0.1 — M0 scaffold)

Studio is a Soulacy plugin that mounts a UI page in the gateway web portal. This
**M0 milestone is a scaffold**: it proves the plugin loads, a nav entry appears,
the sandboxed iframe renders, the scoped plugin token is read from the URL
fragment, and the page attempts to fetch the capability catalog from existing
gateway endpoints. The full visual canvas (xyflow) lands in a later milestone —
M0 is deliberately dependency-light: vanilla HTML/JS/CSS, **no build step**.

## Directory layout

```
examples/plugins/studio/
├── plugin.yaml      # manifest_schema 2; id=studio; gui mount + permissions
├── README.md        # this file
└── ui/              # static assets served at /plugins/studio/ui/
    ├── index.html   # token-aware shell: topbar, palette, canvas placeholder
    ├── app.js       # reads #token, fetches catalog, renders palette
    └── styles.css   # self-contained dark theme (deep navy, accent #6c63ff)
```

## How it loads

1. **Discovery.** The gateway constructs the plugin loader with
   `plugins.New(cfg.PluginDirs, log)` (`internal/app/wire.go`). The loader scans
   each configured dir **one level deep** — each immediate subdirectory holding a
   `plugin.yaml` is one plugin (`internal/plugins/loader.go`).

2. **Where it's installed.** By default `plugin_dirs` resolves to
   `<workspace>/plugins` (`~/.soulacy/soulspace/plugins`). To make Studio
   discoverable from a repo checkout running under `config.dev.yaml`, that file
   now lists this folder:

   ```yaml
   plugin_dirs:
       - examples/plugins
   ```

   (paths are resolved relative to the gateway's working directory, the same way
   `agent_dirs: examples/agents` already works in `config.dev.yaml`). For a real
   installation, copy `examples/plugins/studio/` into `~/.soulacy/soulspace/plugins/studio/`.

3. **Manifest validation.** `manifest_schema: 2` triggers full v2 validation
   (`internal/plugins/manifest2.go`): the `gui.static` directory must exist and
   `gui.nav.label` must be set. Declared `permissions` are compiled by
   `caps.NewSet` — an unknown capability name refuses the plugin outright.

4. **GUI mount.** `lp.GUIMount()` yields the static dir + nav spec; `wire.go`
   passes it to `srv.SetPluginUI(...)`. The gateway then:
   - serves assets at `GET /plugins/studio/ui/*` (no auth — code, not data),
   - lists the nav entry at `GET /api/v1/plugins/ui`,
   - mints a scoped token at `POST /api/v1/plugins/studio/token` → `splg_<hex>`.

5. **Iframe + token.** The Svelte shell embeds the page in a sandboxed iframe
   (`sandbox="allow-scripts allow-forms"`, **no** `allow-same-origin`) and passes
   the token in the URL **fragment** as `#token=...` (`gui/src/lib/pluginui.js`).
   `ui/app.js` reads `location.hash`, scrubs it, keeps the token in a JS variable
   only (no localStorage/cookies — unavailable without same-origin), and sends it
   as `Authorization: Bearer <token>` on API calls.

## API endpoints the UI consumes

All under `/api/v1`, Bearer auth, response shapes verified against the gateway
handlers (`internal/gateway/api.go`):

| Endpoint               | Response fields used                                                                 |
| ---------------------- | ------------------------------------------------------------------------------------ |
| `GET /agents`          | `agents[].name`, `agents[].description`                                               |
| `GET /tool-catalog`    | `python_tools[].name`, `mcp_tools[].name`/`.server`, `builtins[].name`               |
| `GET /providers`       | `providers` (map of name → `{model, …}`), `default_provider`                          |

## Known blocker (read before testing the live fetch)

The plugin **token gate is default-deny**. `internal/gateway/plugins.go`'s
`pluginRoutePolicy` currently admits plugin principals to only two routes:

- `GET /api/v1/health` (no cap), and
- `POST /api/v1/knowledge/*/search` (cap `vector.search`).

The capability registry (`internal/caps/caps.go`) likewise defines only
`vector.search`, `channel.send`, and `events.subscribe` — there is **no**
read capability for agents/tools/providers, and those routes are not in the gate
table. Consequently a `splg_` token currently receives **HTTP 403** on
`/agents`, `/tool-catalog`, and `/providers`.

The UI handles this gracefully (per-group error rows + a status banner). To make
the catalog actually load, the gateway-owning stream must:

1. register read capabilities (e.g. `agents.read`, `tools.read`,
   `providers.read`) in `internal/caps/caps.go`, then
2. add matching `pluginRoute` entries to `pluginRoutePolicy` in
   `internal/gateway/plugins.go`, and
3. add those caps to this plugin's `permissions:` in `plugin.yaml`.

This plugin stream cannot make those edits (they live under `internal/`, owned by
the flow-model stream). Until then the scaffold proves load + mount + token + the
403-handled fetch path.
