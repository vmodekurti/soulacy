<script>
  // Sandboxed host page for one plugin UI (Story E8). Fetches the plugin's
  // SCOPED token (bound to the plugin:<id> principal and its manifest
  // capabilities — never the user's API key) and embeds the static mount in
  // a sandboxed iframe. The token travels in the URL fragment, readable by
  // the plugin via location.hash but never sent to the server.
  import { onDestroy } from 'svelte'
  import { api } from '../lib/api.js'
  import { iframeSrc, IFRAME_SANDBOX } from '../lib/pluginui.js'

  export let pluginId = ''
  export let label = ''
  export let url = ''

  let src = ''
  let error = ''
  let loading = true
  let iframeEl              // bound <iframe> element; identifies the trusted source

  $: pluginId, load()

  async function load() {
    if (!pluginId || !url) return
    loading = true
    error = ''
    src = ''
    try {
      const res = await api.plugins.token(pluginId)
      src = iframeSrc(url, res?.token || '')
    } catch (e) {
      error = e.message || 'failed to fetch plugin token'
    } finally {
      loading = false
    }
  }

  // ---------------------------------------------------------------------------
  // Host-mediated RPC bridge (Studio M1-A).
  //
  // The plugin UI runs in a sandbox WITHOUT allow-same-origin, so its own
  // scoped plugin token is DEFAULT-DENIED by the capability system on reads
  // like /agents, /tool-catalog and /providers. Rather than broaden plugin
  // permissions, the HOST frame — which already holds the user's authenticated
  // `api` session — performs those reads on the iframe's behalf and relays the
  // result back over postMessage. The host stays in full control: it only ever
  // answers a fixed whitelist of request types and never forwards arbitrary
  // calls.
  //
  // postMessage contract
  // --------------------
  //   iframe -> host (request):
  //     { source: 'studio', type: <whitelisted>, id: <string> }
  //   host -> iframe (response):
  //     { source: 'studio-host', type: '<reqType>.response', id, ok: true,  data: {...} }
  //     { source: 'studio-host', type: '<reqType>.response', id, ok: false, error: '<msg>' }
  //
  // Implemented types:
  //   'catalog.request' -> 'catalog.response'
  //       data = { agents: [...], tools: {...}, providers: {...},
  //                channels: {...} }                          (M1-A + M2)
  //   'compile.request' -> 'compile.response'  (M1 Wave 2)
  //       req  { intent, answers? }
  //       data = POST /studio/compile body { workflow, questions, notes }
  //   'test.request'    -> 'test.response'      (M1 Wave 2 + M5)
  //       req  { workflow, input, mocks?:{<nodeId>:<output>},
  //              assertions?:[{target,op,value}], mode?:"dry" }
  //       data = POST /studio/test body
  //              { trace:[{nodeId,kind,input,output,mocked?}], result,
  //                assertions:[{target,op,value,pass,detail}], passed,
  //                mode, warnings? }
  //   'plan.request'    -> 'plan.response'      (M2)
  //       req  { workflow }
  //       data = POST /studio/plan body
  //              { tier, reasons[], requiresConsent,
  //                consentItems:[{ kind, name, reason }] }
  //   'validate.request' -> 'validate.response'  (M3)
  //       req  { workflow }
  //       data = POST /studio/validate body
  //              { ok, errors:[{ nodeId?, edgeIndex?, message }],
  //                warnings:[{ nodeId?, message }] }
  //   'save.request'    -> 'save.response'      (M1 Wave 2 + M2)
  //       req  { workflow, acceptPrivilegedExposure? }
  //       data = POST /studio/save body { agentId, enabled }
  //       on 409 consent fallback the error reply also carries
  //       { requiresConsent, consentItems } alongside `error`.
  //   'discover.request' -> 'discover.response'  (M4)
  //       req  { query, kind? }
  //       data = { results:[...] } — relays GET /registries/search?q=<query>
  //              (the existing skill-source search; its packages are passed
  //              through verbatim under `results`).
  //   'install.request' -> 'install.response'    (M4)
  //       req  { source, checksum?, name? }
  //       data = the existing /plugins/install (stage) response
  //              { staged, multiStep:true, preview, security?, note } —
  //              staging carries its own review/consent; activation still
  //              requires a separate Approve in the Plugins page. The reply is
  //              honest about that ("staged" not "installed").
  //   'templates.request' -> 'templates.response'  (M6)
  //       data = GET /studio/templates -> { templates:[{id,name,description,workflow}] }
  //   'draftSave.request' -> 'draftSave.response'   (M6)
  //       req  { name, workflow }
  //       data = POST /studio/drafts -> { id }
  //   'draftsList.request' -> 'draftsList.response' (M6)
  //       data = GET /studio/drafts -> { drafts:[{id,name,updated}] }
  //   'draftLoad.request' -> 'draftLoad.response'   (M6)
  //       req  { id }
  //       data = GET /studio/drafts/:id -> { id,name,workflow }
  //   'draftDelete.request' -> 'draftDelete.response'  (M6)
  //       req  { id }
  //       data = DELETE /studio/drafts/:id -> { ok:true }
  //   'refine.request' -> 'refine.response'         (M6)
  //       req  { workflow, nodeId, instruction }
  //       data = POST /studio/refine -> { workflow }
  // Nothing else is served until explicitly added to the whitelist below.
  //
  // Security:
  //   - Source check: we only act on messages whose event.source is THIS
  //     iframe's contentWindow. The sandbox has an opaque ("null") origin, so
  //     event.origin is not a usable allowlist key — identity of the source
  //     window is the trustworthy signal and we verify it strictly.
  //   - Type whitelist: a switch over known request types; unknown types are
  //     ignored (no default passthrough).
  //   - Reply targetOrigin: because the iframe's origin is opaque ("null"),
  //     a specific origin string can never match it, so postMessage would drop
  //     the reply. We therefore reply with targetOrigin '*'. This is safe here:
  //     the message carries only the read-only public catalog the user can
  //     already see, and the destination window is fixed (iframeEl's
  //     contentWindow), not a navigable/cross-origin target.
  const REPLY_TARGET_ORIGIN = '*'

  async function handleBridgeMessage(event) {
    // Only trust messages originating from THIS plugin iframe's window.
    if (!iframeEl || event.source !== iframeEl.contentWindow) return

    const msg = event.data
    if (!msg || typeof msg !== 'object' || msg.source !== 'studio') return
    const { type, id } = msg
    if (typeof type !== 'string') return

    switch (type) {
      case 'catalog.request':
        await handleCatalogRequest(id)
        break
      case 'compile.request':
        await handleStudioRequest(id, 'compile.response', () =>
          api.studio.compile({ intent: msg.intent, answers: msg.answers, catalog: msg.catalog }))
        break
      case 'test.request':
        // M5: the test bench passes richer input — optional per-node `mocks`,
        // `assertions`, and a `mode` ("dry"/"live"). We forward them verbatim;
        // unset fields are simply omitted and the backend defaults them.
        await handleStudioRequest(id, 'test.response', () =>
          api.studio.test({
            workflow: msg.workflow,
            input: msg.input,
            mocks: msg.mocks,
            assertions: msg.assertions,
            mode: msg.mode,
          }))
        break
      case 'plan.request':
        await handleStudioRequest(id, 'plan.response', () =>
          api.studio.plan({ workflow: msg.workflow }))
        break
      case 'validate.request':
        await handleStudioRequest(id, 'validate.response', () =>
          api.studio.validate({ workflow: msg.workflow }))
        break
      case 'save.request':
        await handleStudioRequest(id, 'save.response', () =>
          api.studio.save({
            workflow: msg.workflow,
            acceptPrivilegedExposure: msg.acceptPrivilegedExposure,
          }))
        break
      case 'discover.request':
        // Relay to the EXISTING skill-source search (GET /registries/search).
        // Its packages are passed through verbatim under `results` so the
        // Studio panel can render whatever the registry indexed.
        await handleStudioRequest(id, 'discover.response', async () => {
          const res = await api.registries.search(msg.query || '')
          const results = (res && Array.isArray(res.packages)) ? res.packages : []
          return { results, count: (res && res.count) || results.length }
        })
        break
      case 'install.request':
        // Relay to the EXISTING plugin install endpoint (POST /plugins/install).
        // This STAGES the package — a real, review-bearing operation that does
        // NOT activate anything on its own; the operator must still Approve it
        // from the Plugins page. We report that honestly (multiStep:true) rather
        // than faking completion, and never invent our own install logic.
        await handleStudioRequest(id, 'install.response', async () => {
          const res = await api.plugins.stage(msg.source, msg.checksum || '')
          const preview = (res && res.preview) || null
          return {
            staged: preview && (preview.StagedID || preview.stagedID || preview.staged_id) || '',
            multiStep: true,
            preview,
            security: preview && (preview.Security || preview.security) || null,
            note: (res && res.note) || 'Staged for review — approve it in the Plugins page to activate.',
          }
        })
        break
      case 'templates.request':
        // M6: starter templates for the empty-state / "Templates" picker.
        await handleStudioRequest(id, 'templates.response', () =>
          api.studio.templates())
        break
      case 'draftSave.request':
        // M6: persist the current draft into the server-side draft library.
        await handleStudioRequest(id, 'draftSave.response', () =>
          api.studio.draftSave({ name: msg.name, workflow: msg.workflow }))
        break
      case 'draftsList.request':
        // M6: list saved drafts for the Open panel.
        await handleStudioRequest(id, 'draftsList.response', () =>
          api.studio.draftsList())
        break
      case 'draftLoad.request':
        // M6: load one saved draft by id.
        await handleStudioRequest(id, 'draftLoad.response', () =>
          api.studio.draftLoad(msg.id))
        break
      case 'draftDelete.request':
        // M6: delete one saved draft by id.
        await handleStudioRequest(id, 'draftDelete.response', () =>
          api.studio.draftDelete(msg.id))
        break
      case 'refine.request':
        // M6: per-node natural-language refine -> a new workflow.
        await handleStudioRequest(id, 'refine.response', () =>
          api.studio.refine({
            workflow: msg.workflow,
            nodeId: msg.nodeId,
            instruction: msg.instruction,
          }))
        break
      default:
        // Unknown/unsupported type: ignore (never forward arbitrary requests).
        return
    }
  }

  // Generic relay for the Studio compile/test/save ops: runs `call()` (which
  // POSTs through the user's authed api client) and posts an id-correlated,
  // ok/error-shaped reply back to THIS iframe — identical pattern to the
  // catalog handler. The host stays the only thing that can reach the gateway;
  // the iframe never holds a usable credential.
  async function handleStudioRequest(id, responseType, call) {
    const win = iframeEl && iframeEl.contentWindow
    if (!win) return
    try {
      const data = await call()
      win.postMessage(
        { source: 'studio-host', type: responseType, id, ok: true, data },
        REPLY_TARGET_ORIGIN,
      )
    } catch (e) {
      // Forward any structured error fields the api layer preserved (e.g.
      // /studio/save's 409 consent fallback carries requiresConsent +
      // consentItems on e.body). The iframe can then show the same consent
      // dialog from the error path without a separate plan round-trip.
      const reply = {
        source: 'studio-host', type: responseType, id, ok: false,
        error: e?.message || 'request failed',
      }
      const b = e && e.body
      if (b && typeof b === 'object') {
        if (b.requiresConsent != null) reply.requiresConsent = b.requiresConsent
        if (b.consentItems != null) reply.consentItems = b.consentItems
      }
      win.postMessage(reply, REPLY_TARGET_ORIGIN)
    }
  }

  // Fetch the read-only catalog (agents + tools + providers + channels) using
  // the user's authenticated api client and relay it to the iframe.
  async function handleCatalogRequest(id) {
    const win = iframeEl && iframeEl.contentWindow
    if (!win) return
    try {
      const [agents, tools, providers, channels] = await Promise.all([
        api.agents.list(),
        api.tools.catalog(),
        api.providers.list(),
        api.channels.list(),
      ])
      win.postMessage(
        { source: 'studio-host', type: 'catalog.response', id, ok: true, data: { agents, tools, providers, channels } },
        REPLY_TARGET_ORIGIN,
      )
    } catch (e) {
      win.postMessage(
        { source: 'studio-host', type: 'catalog.response', id, ok: false, error: e?.message || 'catalog fetch failed' },
        REPLY_TARGET_ORIGIN,
      )
    }
  }

  window.addEventListener('message', handleBridgeMessage)
  onDestroy(() => window.removeEventListener('message', handleBridgeMessage))
</script>

<div class="page plugin-page">
  <div class="page-header">
    <h1>{label || pluginId}</h1>
    <span class="principal" title="This UI runs as a scoped plugin principal, not as you">
      plugin:{pluginId}
    </span>
  </div>

  {#if loading}
    <div class="state">Loading plugin UI…</div>
  {:else if error}
    <div class="state error">⚠ {error}</div>
  {:else}
    <iframe
      bind:this={iframeEl}
      title={label || pluginId}
      {src}
      sandbox={IFRAME_SANDBOX}
      referrerpolicy="no-referrer"
    ></iframe>
  {/if}
</div>

<style>
  .plugin-page { display: flex; flex-direction: column; height: 100%; }
  .page-header { display: flex; align-items: baseline; gap: 0.75rem; }
  .principal {
    font-family: ui-monospace, monospace;
    font-size: 0.75rem;
    color: #8a91b4;
    border: 1px solid #2a2f4a;
    border-radius: 4px;
    padding: 0.1rem 0.4rem;
  }
  iframe {
    flex: 1;
    width: 100%;
    min-height: 70vh;
    border: 1px solid #2a2f4a;
    border-radius: 8px;
    background: #fff;
  }
  .state { padding: 2rem; color: #8a91b4; }
  .state.error { color: #ff7676; }
</style>
