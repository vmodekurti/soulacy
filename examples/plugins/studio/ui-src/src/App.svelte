<script>
  import { onMount } from 'svelte'
  import {
    SvelteFlow, Background, Controls, MiniMap, Position,
  } from '@xyflow/svelte'
  import '@xyflow/svelte/dist/style.css'
  import { writable } from 'svelte/store'

  import { bridge } from './bridge.js'
  import { toFlow, kindMeta } from './graph.js'
  import Palette from './Palette.svelte'
  import Inspector from './Inspector.svelte'
  import StudioNode from './nodes/StudioNode.svelte'
  import TriggerNode from './nodes/TriggerNode.svelte'
  import OutputNode from './nodes/OutputNode.svelte'

  // Scrub the token from the URL fragment (kept by the host; the bridge needs
  // no credential — the host holds the session).
  try {
    if (window.location.hash) {
      window.history.replaceState(null, '', window.location.pathname + window.location.search)
    }
  } catch (_) { /* sandbox may block history */ }

  const nodeTypes = {
    studio: StudioNode,
    studioTrigger: TriggerNode,
    studioOutput: OutputNode,
  }

  // ── Palette (Wave 1) ──────────────────────────────────────────────────────
  let catalog = null
  let paletteStatus = 'Loading capabilities…'
  let paletteStatusKind = ''
  let paletteError = ''

  async function loadCatalog() {
    paletteStatus = 'Loading capabilities…'
    try {
      catalog = await bridge.catalog()
      paletteStatus = 'Capabilities loaded.'
      paletteStatusKind = 'ok'
    } catch (e) {
      paletteError = 'Unavailable'
      paletteStatus = 'Could not load capabilities: ' + (e.message || 'error')
      paletteStatusKind = 'error'
    }
  }

  // ── Compile loop ──────────────────────────────────────────────────────────
  let intent = ''
  let compiling = false
  let compileError = ''
  let workflow = null
  let questions = []
  let notes = []
  let answers = {}            // { [questionId]: value }

  // ── Missing-capability suggestions (M4) ───────────────────────────────────
  // The compile response may carry `suggestions:[{kind,name,reason,installed}]`
  // — capabilities the draft references that are NOT installed. They surface in
  // a non-blocking "Needs setup" strip; each can be discovered + staged for
  // install through the EXISTING registry-search / plugin-install endpoints.
  let suggestions = []
  // Per-suggestion UI state keyed by name:
  //   { loading, error, results:[pkg], message, staged }
  let discoverState = {}

  function suggestionKey(s) {
    return (s && s.name) || ''
  }

  // Find installable packages for one missing capability via the discover
  // bridge op (relays GET /registries/search). Degrades gracefully on error.
  async function findCapability(s) {
    const key = suggestionKey(s)
    if (!key) return
    discoverState = { ...discoverState, [key]: { loading: true, error: '', results: [], message: '', staged: '' } }
    try {
      const data = await bridge.discover(key, s.kind)
      const results = (data && Array.isArray(data.results)) ? data.results : []
      discoverState = { ...discoverState, [key]: { loading: false, error: '', results, message: results.length ? '' : 'No matches found.', staged: '' } }
    } catch (e) {
      discoverState = { ...discoverState, [key]: { loading: false, error: e.message || 'discovery failed', results: [], message: '', staged: '' } }
    }
  }

  // Stage an install for a chosen registry result via the install bridge op
  // (relays POST /plugins/install). Staging is real + consent-bearing and does
  // NOT activate the package — the operator must Approve it in the Plugins
  // page. We surface that honestly, then re-fetch the catalog so the palette
  // (and any now-installed suggestions) refresh.
  async function installResult(s, pkg) {
    const key = suggestionKey(s)
    if (!key || !pkg) return
    const source = pkg.source || pkg.slug || ''
    const prev = discoverState[key] || {}
    discoverState = { ...discoverState, [key]: { ...prev, loading: true, error: '', message: '' } }
    try {
      const data = await bridge.install({ source, checksum: pkg.checksum, name: pkg.slug || key })
      const note = (data && data.note) || ''
      const msg = data && data.multiStep
        ? (data.staged
            ? `Staged "${pkg.slug || key}" — approve it in the Plugins page to activate. ${note}`.trim()
            : `Staged for review. ${note}`.trim())
        : `Installed "${pkg.slug || key}".`
      discoverState = { ...discoverState, [key]: { ...discoverState[key], loading: false, message: msg, staged: (data && data.staged) || '' } }
      // Refresh the catalog so the palette + suggestions reflect the new state.
      await loadCatalog()
    } catch (e) {
      discoverState = { ...discoverState, [key]: { ...discoverState[key], loading: false, error: e.message || 'install failed' } }
    }
  }

  function pkgDesc(pkg) {
    return (pkg && (pkg.description || (pkg.provider ? '' : ''))) || ''
  }

  // ── Browse registry (S4.3, light) ─────────────────────────────────────────
  // A small Palette affordance that runs `discover` with the user's intent so
  // they can explore + stage installable packages without a suggestion. Reuses
  // the same install path (and catalog refresh) as the Needs-setup panel.
  let browse = { open: false, loading: false, error: '', results: [], message: '' }

  async function browseRegistry() {
    const q = (intent || '').trim()
    browse = { open: true, loading: true, error: '', results: [], message: '' }
    try {
      const data = await bridge.discover(q || 'skill')
      const results = (data && Array.isArray(data.results)) ? data.results : []
      browse = { open: true, loading: false, error: '', results, message: results.length ? '' : 'No matches found.' }
    } catch (e) {
      browse = { open: true, loading: false, error: e.message || 'discovery failed', results: [], message: '' }
    }
  }

  async function installBrowse(pkg) {
    if (!pkg) return
    const source = pkg.source || pkg.slug || ''
    browse = { ...browse, loading: true, error: '', message: '' }
    try {
      const data = await bridge.install({ source, checksum: pkg.checksum, name: pkg.slug })
      const note = (data && data.note) || ''
      browse = {
        ...browse,
        loading: false,
        message: data && data.multiStep
          ? `Staged "${pkg.slug}" — approve it in the Plugins page to activate. ${note}`.trim()
          : `Installed "${pkg.slug}".`,
      }
      await loadCatalog()
    } catch (e) {
      browse = { ...browse, loading: false, error: e.message || 'install failed' }
    }
  }

  // xyflow stores (the component expects writable stores for nodes/edges).
  const nodes = writable([])
  const edges = writable([])
  let selectedNode = null     // raw flow node for the inspector
  let selectedEdge = null     // { index, edge } for the inspector

  // ── Validation (M3) ───────────────────────────────────────────────────────
  // Non-blocking: a debounced /studio/validate after compile and after any
  // draft edit. Highlights offending nodes/edges and shows a status strip.
  let validation = null       // { ok, errors[], warnings[] } | null
  let validateTimer = null

  function rebuildGraph() {
    if (!workflow) {
      nodes.set([]); edges.set([]); return
    }
    const flow = toFlow(workflow, validation)
    nodes.set(flow.nodes)
    edges.set(flow.edges)
  }

  // Debounced, best-effort validation. On any bridge error we degrade
  // gracefully: clear the strip rather than block editing.
  function scheduleValidate() {
    if (validateTimer) clearTimeout(validateTimer)
    if (!workflow) { validation = null; return }
    validateTimer = setTimeout(runValidate, 350)
  }

  async function runValidate() {
    if (!workflow) { validation = null; return }
    const snapshot = workflow
    try {
      const res = await bridge.validate(snapshot)
      // Ignore a stale response if the draft moved on under us.
      if (snapshot !== workflow) return
      validation = {
        ok: res && res.ok !== false && !(res && res.errors && res.errors.length),
        errors: (res && Array.isArray(res.errors)) ? res.errors : [],
        warnings: (res && Array.isArray(res.warnings)) ? res.warnings : [],
      }
    } catch (_) {
      // Bridge/host unavailable — stay silent and non-blocking.
      validation = null
    }
    rebuildGraph()   // re-render with (or without) highlights
  }

  async function generate() {
    const text = intent.trim()
    if (!text || compiling) return
    compiling = true
    compileError = ''
    try {
      const data = await bridge.compile(text, Object.keys(answers).length ? answers : undefined)
      applyCompile(data)
    } catch (e) {
      compileError = e.message || 'compile failed'
    } finally {
      compiling = false
    }
  }

  async function applyAnswers() {
    // Re-send compile with the current answers map -> re-render.
    if (compiling) return
    compiling = true
    compileError = ''
    try {
      const data = await bridge.compile(intent.trim(), answers)
      applyCompile(data)
    } catch (e) {
      compileError = e.message || 'compile failed'
    } finally {
      compiling = false
    }
  }

  function applyCompile(data) {
    workflow = (data && data.workflow) || null
    questions = (data && Array.isArray(data.questions)) ? data.questions : []
    notes = (data && Array.isArray(data.notes)) ? data.notes : []
    // M4: surface missing-capability suggestions (non-blocking). Keep only the
    // ones not yet installed; a fresh compile resets any in-flight discovery.
    suggestions = (data && Array.isArray(data.suggestions))
      ? data.suggestions.filter((s) => s && s.installed !== true)
      : []
    discoverState = {}
    selectedNode = null
    selectedEdge = null
    plan = null               // a fresh compile invalidates any prior plan/tier
    validation = null         // clear stale highlights until re-validated
    rebuildGraph()
    scheduleValidate()        // validate the fresh draft (debounced)
  }

  // ── Framing edits (trigger / output channels) ─────────────────────────────
  // Channels available to pick from (catalog payload is { channels: [...] }).
  $: channelOptions = (catalog && catalog.channels && catalog.channels.channels)
    ? catalog.channels.channels.map((ch) => ({ id: ch.id, name: ch.name || ch.id }))
    : []

  // Merge an Inspector patch (e.g. { trigger } or { channels }) into the
  // in-memory draft so subsequent Test/Save use the edited values, then
  // re-render the START/SINK framing on the canvas. A new object reference
  // keeps Svelte reactivity firing.
  function applyFraming(patch) {
    if (!workflow) return
    workflow = { ...workflow, ...patch }
    plan = null               // edits can change the tier; re-plan on next save
    rebuildGraph()
    scheduleValidate()        // re-validate after a framing edit
  }

  // Merge a patch into a single draft edge (flow.edges[index]) and re-render.
  // Used by the Inspector's `if` predicate field (selected edge or Edges list)
  // so Test/Save/Validate pick up the edited condition.
  function applyEdgePatch(index, patch) {
    if (!workflow || !workflow.flow || !Array.isArray(workflow.flow.edges)) return
    const edgesArr = workflow.flow.edges
    if (index < 0 || index >= edgesArr.length) return
    const nextEdges = edgesArr.map((e, i) => (i === index ? { ...e, ...patch } : e))
    workflow = { ...workflow, flow: { ...workflow.flow, edges: nextEdges } }
    // Keep the selected-edge mirror in sync so the field stays controlled.
    if (selectedEdge && selectedEdge.index === index) {
      selectedEdge = { index, edge: nextEdges[index] }
    }
    plan = null
    rebuildGraph()
    scheduleValidate()
  }

  function onNodeClick(event) {
    // SvelteFlow dispatches { node } in event.detail.
    const n = event?.detail?.node
    selectedEdge = null
    if (!n || !n.data || !n.data.node) { selectedNode = null; return }
    selectedNode = n.data.node
  }

  // Edge click -> select it for `if`-predicate editing. The xyflow edge carries
  // our data.index (ordinal in flow.edges) so we can read/write the right slot.
  function onEdgeClick(event) {
    const e = event?.detail?.edge
    const idx = e && e.data && Number.isInteger(e.data.index) ? e.data.index : -1
    if (idx < 0 || !workflow || !workflow.flow || !Array.isArray(workflow.flow.edges)) {
      selectedEdge = null
      return
    }
    selectedNode = null
    selectedEdge = { index: idx, edge: workflow.flow.edges[idx] }
  }

  // ── Test ──────────────────────────────────────────────────────────────────
  let testing = false
  let testError = ''
  let testResult = null       // { trace:[{nodeId,kind,input,output}], result }
  let sampleInput = 'hello'

  async function runTest() {
    if (!workflow || testing) return
    testing = true
    testError = ''
    testResult = null
    try {
      testResult = await bridge.test(workflow, sampleInput)
    } catch (e) {
      testError = e.message || 'test failed'
    } finally {
      testing = false
    }
  }

  // ── Plan + Save (M2) ──────────────────────────────────────────────────────
  let saving = false
  let saveError = ''
  let saveMsg = ''
  let plan = null              // last plan result { tier, reasons, requiresConsent, consentItems }
  let consent = null          // { items:[{kind,name,reason}] } when the dialog is open

  // Save click: PLAN first, then either save directly or raise the consent
  // dialog. Every bridge op degrades gracefully — a bridge/host error just
  // surfaces as saveError and never throws past here.
  async function save() {
    if (!workflow || saving || consent) return
    saving = true
    saveError = ''
    saveMsg = ''
    try {
      const p = await bridge.plan(workflow)
      plan = p || null
      if (p && p.requiresConsent) {
        openConsent(p.consentItems)
        return        // wait for the operator's acknowledgement
      }
      await doSave(false)
    } catch (e) {
      saveError = e.message || 'plan failed'
    } finally {
      saving = false
    }
  }

  // Persist the draft. acceptPrivilegedExposure threads the operator's consent.
  // Handles the 409 consent fallback (error carrying requiresConsent +
  // consentItems) by opening the same dialog.
  async function doSave(acceptPrivilegedExposure) {
    saveError = ''
    try {
      const res = await bridge.save(workflow, acceptPrivilegedExposure)
      const id = (res && res.agentId) || '(unknown)'
      saveMsg = `Saved as disabled agent ${id} — enable it from the Agents page.`
      consent = null
    } catch (e) {
      // 409 fallback: server demands consent even though plan didn't (or the
      // draft changed). Show the dialog rather than a raw error.
      if (e && e.requiresConsent && !acceptPrivilegedExposure) {
        openConsent(e.consentItems)
        return
      }
      saveError = e.message || 'save failed'
      consent = null
    }
  }

  function openConsent(items) {
    consent = { items: Array.isArray(items) ? items : [] }
  }

  async function acknowledgeConsent() {
    if (saving) return
    saving = true
    try {
      await doSave(true)
    } finally {
      saving = false
    }
  }

  function cancelConsent() {
    consent = null
    saving = false
  }

  function tierLabel(t) {
    if (t === 'privileged') return 'privileged'
    if (t === 'active') return 'active'
    if (t === 'readonly') return 'read-only'
    return t || ''
  }

  function fmt(v) {
    if (v == null) return ''
    if (typeof v === 'object') return JSON.stringify(v, null, 2)
    return String(v)
  }

  const kinds = ['tool', 'agent', 'branch']

  onMount(loadCatalog)
</script>

<div id="app">
  <!-- Top bar -->
  <header class="topbar">
    <div class="brand">
      <span class="brand-mark" aria-hidden="true">🎬</span>
      <span class="brand-name">Studio</span>
    </div>
    <div class="intent">
      <input
        type="text"
        bind:value={intent}
        placeholder="Describe what you want…"
        aria-label="Describe what you want"
        on:keydown={(e) => e.key === 'Enter' && generate()}
      />
    </div>
    <button class="btn primary" on:click={generate} disabled={compiling || !intent.trim()}>
      {compiling ? 'Generating…' : 'Generate'}
    </button>
    <div class="badge" title="Scoped plugin principal">
      principal: <strong>plugin:studio</strong>
    </div>
  </header>

  <main class="body">
    <Palette
      {catalog}
      status={paletteStatus}
      statusKind={paletteStatusKind}
      error={paletteError}
      onBrowse={browseRegistry}
      {browse}
      onInstall={installBrowse}
    />

    <!-- Center: canvas + transparency strips + panels -->
    <section class="center">
      {#if compileError}
        <div class="strip strip-error">⚠ {compileError}</div>
      {/if}

      {#if notes.length}
        <div class="strip strip-notes" title="What the compiler inferred">
          <span class="strip-label">Inferred</span>
          {#each notes as n}<span class="note">{n}</span>{/each}
        </div>
      {/if}

      <!-- Validation strip (M3): non-blocking ok / N errors / N warnings. -->
      {#if workflow && validation}
        {#if validation.ok && !validation.warnings.length}
          <div class="strip strip-ok" title="Workflow validates">
            <span class="strip-label">Valid</span>
            <span>No issues found.</span>
          </div>
        {:else}
          <div
            class="strip {validation.errors.length ? 'strip-error' : 'strip-warn'}"
            title="Validation issues"
          >
            <span class="strip-label">Validation</span>
            {#if validation.errors.length}
              <span class="v-count v-err">{validation.errors.length} error{validation.errors.length === 1 ? '' : 's'}</span>
            {/if}
            {#if validation.warnings.length}
              <span class="v-count v-warn">{validation.warnings.length} warning{validation.warnings.length === 1 ? '' : 's'}</span>
            {/if}
            {#each validation.errors as err}
              <span class="v-msg v-err" title={err.nodeId || (err.edgeIndex != null ? 'edge ' + err.edgeIndex : '')}>{err.message}</span>
            {/each}
            {#each validation.warnings as w}
              <span class="v-msg v-warn" title={w.nodeId || ''}>{w.message}</span>
            {/each}
          </div>
        {/if}
      {/if}

      <!-- Needs-setup panel (M4): missing capabilities the draft references but
           that are NOT installed. Non-blocking — the draft still renders/tests;
           each item can be discovered + staged via the existing endpoints. -->
      {#if suggestions.length}
        <div class="needs-setup" aria-label="Missing capabilities">
          <div class="ns-head">
            <span class="strip-label">Needs setup</span>
            <span class="ns-sub">These capabilities aren’t installed yet — the draft still works, but won’t run them until you add them.</span>
          </div>
          <ul class="ns-list">
            {#each suggestions as s (s.name)}
              <li class="ns-item">
                <div class="ns-row">
                  <span class="kind-chip kind-{s.kind || 'tool'}">{s.kind || 'tool'}</span>
                  <span class="ns-name">{s.name}</span>
                  {#if s.reason}<span class="ns-reason">{s.reason}</span>{/if}
                  <button
                    class="btn btn-sm"
                    on:click={() => findCapability(s)}
                    disabled={discoverState[s.name] && discoverState[s.name].loading}
                  >
                    {discoverState[s.name] && discoverState[s.name].loading ? 'Finding…' : 'Find'}
                  </button>
                </div>

                {#if discoverState[s.name]}
                  {#if discoverState[s.name].error}
                    <div class="ns-msg ns-err">⚠ {discoverState[s.name].error}</div>
                  {/if}
                  {#if discoverState[s.name].message}
                    <div class="ns-msg ns-ok">{discoverState[s.name].message}</div>
                  {/if}
                  {#if discoverState[s.name].results && discoverState[s.name].results.length}
                    <ul class="ns-results">
                      {#each discoverState[s.name].results as pkg}
                        <li class="ns-result">
                          <div class="nsr-main">
                            <span class="nsr-name">{pkg.slug || pkg.name || '(package)'}</span>
                            {#if pkg.provider}<span class="nsr-src">{pkg.provider}</span>{/if}
                            {#if pkg.version}<span class="nsr-ver">{pkg.version}</span>{/if}
                          </div>
                          {#if pkgDesc(pkg)}<div class="nsr-desc">{pkgDesc(pkg)}</div>{/if}
                          <button
                            class="btn btn-sm primary"
                            on:click={() => installResult(s, pkg)}
                            disabled={discoverState[s.name] && discoverState[s.name].loading}
                            title="Stage this package for install (review & approve in the Plugins page)"
                          >
                            Install
                          </button>
                        </li>
                      {/each}
                    </ul>
                  {/if}
                {/if}
              </li>
            {/each}
          </ul>
        </div>
      {/if}

      <div class="canvas">
        {#if compiling}
          <div class="canvas-state">Compiling…</div>
        {:else if !workflow}
          <div class="canvas-state empty">
            <div class="glyph" aria-hidden="true">⬚</div>
            <p>Describe what you want above, then press Generate.</p>
          </div>
        {:else}
          <SvelteFlow
            {nodes}
            {edges}
            {nodeTypes}
            fitView
            on:nodeclick={onNodeClick}
            on:edgeclick={onEdgeClick}
            on:paneclick={() => { selectedNode = null; selectedEdge = null }}
          >
            <Background />
            <Controls />
            <MiniMap pannable zoomable />
          </SvelteFlow>
          <!-- Kind legend -->
          <div class="legend">
            {#each kinds as k}
              <span class="legend-item">
                <span class="swatch" style="background: {kindMeta(k).color}"></span>
                {kindMeta(k).label}
              </span>
            {/each}
          </div>
        {/if}
      </div>

      {#if workflow}
        <!-- Action bar -->
        <div class="actions">
          <input
            class="sample"
            type="text"
            bind:value={sampleInput}
            placeholder="sample input"
            aria-label="Sample test input"
          />
          <button class="btn" on:click={runTest} disabled={testing}>
            {testing ? 'Testing…' : 'Test'}
          </button>
          {#if plan && plan.tier}
            <span
              class="tier-chip tier-{plan.tier}"
              title={(plan.reasons && plan.reasons.length) ? plan.reasons.join('; ') : 'capability tier'}
            >
              {tierLabel(plan.tier)}
            </span>
          {/if}
          <button class="btn primary" on:click={save} disabled={saving || !!consent}>
            {saving ? 'Saving…' : 'Save'}
          </button>
        </div>

        {#if saveMsg}<div class="strip strip-ok">✓ {saveMsg}</div>{/if}
        {#if saveError}<div class="strip strip-error">⚠ {saveError}</div>{/if}

        <!-- Clarify panel -->
        {#if questions.length}
          <div class="panel clarify">
            <h3 class="panel-title">Clarify</h3>
            {#each questions as q (q.id)}
              <div class="q">
                <label for={'q-' + q.id}>{q.text}</label>
                {#if q.options && q.options.length}
                  <select id={'q-' + q.id} bind:value={answers[q.id]}>
                    <option value="" disabled selected={!answers[q.id]}>Choose…</option>
                    {#each q.options as opt}
                      <option value={opt}>{opt}</option>
                    {/each}
                  </select>
                {:else}
                  <input id={'q-' + q.id} type="text" bind:value={answers[q.id]} />
                {/if}
              </div>
            {/each}
            <button class="btn primary" on:click={applyAnswers} disabled={compiling}>
              Apply answers
            </button>
          </div>
        {/if}

        <!-- Test results -->
        {#if testError}
          <div class="strip strip-error">⚠ {testError}</div>
        {/if}
        {#if testResult}
          <div class="panel test">
            <h3 class="panel-title">Test trace</h3>
            {#if testResult.trace && testResult.trace.length}
              <ol class="trace">
                {#each testResult.trace as step, i}
                  <li>
                    <span class="step-n">{i + 1}</span>
                    <div class="step-body">
                      <div class="step-head">
                        <strong>{step.nodeId}</strong>
                        {#if step.kind}<span class="step-kind">{step.kind}</span>{/if}
                      </div>
                      <div class="step-io">
                        <span class="io-label">in</span><pre>{fmt(step.input)}</pre>
                        <span class="io-label">out</span><pre>{fmt(step.output)}</pre>
                      </div>
                    </div>
                  </li>
                {/each}
              </ol>
            {:else}
              <p class="muted">No trace returned.</p>
            {/if}
            <div class="result">
              <span class="io-label">result</span>
              <pre>{fmt(testResult.result)}</pre>
            </div>
          </div>
        {/if}
      {/if}
    </section>

    <Inspector
      node={selectedNode}
      {selectedEdge}
      {workflow}
      channels={channelOptions}
      onChange={applyFraming}
      onEdgeChange={applyEdgePatch}
    />
  </main>

  <!-- Consent dialog (M2): shown before saving a privileged, channel-bound
       workflow, or on the server's 409 consent fallback. -->
  {#if consent}
    <div class="modal-backdrop" on:click|self={cancelConsent} role="presentation">
      <div class="modal" role="dialog" aria-modal="true" aria-labelledby="consent-title">
        <h2 id="consent-title" class="modal-title">Privileged channel exposure</h2>
        <p class="modal-body">
          This workflow uses privileged tools (shell/file/install-class) and is bound to a
          channel. Acknowledge to save it as a <strong>DISABLED</strong> agent.
          Note: an operator must still grant channel exposure at deploy time
          (<code>accept_privileged_exposure</code> in config) before it can run.
        </p>
        {#if consent.items.length}
          <ul class="consent-items">
            {#each consent.items as it}
              <li>
                <span class="consent-name">{it.name}</span>
                {#if it.reason}<span class="consent-reason">{it.reason}</span>{/if}
              </li>
            {/each}
          </ul>
        {/if}
        <div class="modal-actions">
          <button class="btn" on:click={cancelConsent} disabled={saving}>Cancel</button>
          <button class="btn primary" on:click={acknowledgeConsent} disabled={saving}>
            {saving ? 'Saving…' : 'Acknowledge & save'}
          </button>
        </div>
      </div>
    </div>
  {/if}
</div>

<style>
  #app {
    display: flex;
    flex-direction: column;
    height: 100vh;
    overflow: hidden;
  }

  /* Top bar */
  .topbar {
    display: flex;
    align-items: center;
    gap: var(--gap);
    padding: 12px 18px;
    background: var(--bg-elev);
    border-bottom: 1px solid var(--border);
    flex: 0 0 auto;
  }
  .brand { display: flex; align-items: center; gap: 8px; font-weight: 600; }
  .brand-mark { font-size: 18px; }
  .intent { flex: 1 1 auto; }
  .intent input {
    width: 100%;
    padding: 10px 14px;
    background: var(--bg-elev-2);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    color: var(--text);
    font-size: 14px;
    outline: none;
  }
  .intent input::placeholder { color: var(--text-muted); }
  .intent input:focus { border-color: var(--accent); }

  .badge {
    flex: 0 0 auto;
    padding: 6px 12px;
    background: var(--accent-dim);
    border: 1px solid var(--accent);
    border-radius: 999px;
    font-size: 12px;
    white-space: nowrap;
  }
  .badge strong { color: var(--accent); font-weight: 600; }

  .btn {
    flex: 0 0 auto;
    padding: 9px 16px;
    background: var(--bg-elev-2);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    color: var(--text);
    font-size: 13px;
    font-weight: 600;
    cursor: pointer;
    transition: border-color 0.12s ease, background 0.12s ease;
  }
  .btn:hover:not(:disabled) { border-color: var(--accent); }
  .btn.primary {
    background: var(--accent);
    border-color: var(--accent);
    color: #fff;
  }
  .btn.primary:hover:not(:disabled) { filter: brightness(1.08); }
  .btn:disabled { opacity: 0.5; cursor: not-allowed; }

  /* Body layout */
  .body { display: flex; flex: 1 1 auto; min-height: 0; }
  .center {
    flex: 1 1 auto;
    min-width: 0;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }

  /* Transparency strips */
  .strip {
    padding: 6px 14px;
    font-size: 12px;
    border-bottom: 1px solid var(--border);
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
  }
  .strip-notes { background: var(--bg-elev); color: var(--text-muted); }
  .strip-label {
    text-transform: uppercase;
    letter-spacing: 0.5px;
    font-size: 10px;
    color: var(--accent);
  }
  .note {
    background: var(--bg-elev-2);
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 1px 8px;
  }
  .strip-error { background: rgba(255, 107, 129, 0.12); color: var(--error); }
  .strip-ok { background: rgba(54, 211, 153, 0.12); color: var(--ok); }
  .strip-warn { background: rgba(245, 167, 66, 0.12); color: var(--warn, #f5a742); }
  .v-count {
    font-weight: 700;
    border-radius: 999px;
    padding: 0 8px;
    font-size: 11px;
  }
  .v-count.v-err { background: rgba(255, 107, 129, 0.18); color: var(--error, #ff6b81); }
  .v-count.v-warn { background: rgba(245, 167, 66, 0.18); color: var(--warn, #f5a742); }
  .v-msg {
    font-size: 11px;
    background: var(--bg-elev-2);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 1px 8px;
    max-width: 320px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .v-msg.v-err { border-color: var(--error, #ff6b81); }
  .v-msg.v-warn { border-color: var(--warn, #f5a742); }

  /* ── Needs-setup panel (M4: missing-capability suggestions) ───────────── */
  .needs-setup {
    flex: 0 0 auto;
    padding: 10px 14px;
    background: rgba(245, 167, 66, 0.08);
    border-bottom: 1px solid var(--border);
    max-height: 38vh;
    overflow-y: auto;
  }
  .ns-head { display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; margin-bottom: 8px; }
  .ns-sub { font-size: 11px; color: var(--text-muted); }
  .ns-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 8px; }
  .ns-item {
    background: var(--bg-elev);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 8px 10px;
  }
  .ns-row { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .ns-name { font-size: 13px; font-weight: 600; color: var(--text); }
  .ns-reason { font-size: 11px; color: var(--text-muted); flex: 1 1 auto; min-width: 0; }
  .kind-chip {
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.4px;
    border-radius: 999px;
    padding: 1px 8px;
    font-weight: 700;
    border: 1px solid var(--border);
    color: var(--text-muted);
    background: var(--bg-elev-2);
  }
  .kind-chip.kind-tool { color: var(--accent); border-color: var(--accent); }
  .kind-chip.kind-agent { color: var(--ok, #36d399); border-color: var(--ok, #36d399); }
  .btn-sm { padding: 4px 10px; font-size: 12px; }
  .ns-msg { margin-top: 6px; font-size: 11px; }
  .ns-msg.ns-err { color: var(--error, #ff6b81); }
  .ns-msg.ns-ok { color: var(--ok, #36d399); }
  .ns-results { list-style: none; margin: 8px 0 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
  .ns-result {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 6px 10px;
    background: var(--bg-elev-2);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 6px 8px;
  }
  .nsr-main { display: flex; align-items: baseline; gap: 8px; flex: 1 1 auto; min-width: 0; }
  .nsr-name { font-size: 12px; font-weight: 600; color: var(--text); }
  .nsr-src {
    font-size: 10px;
    color: var(--text-muted);
    background: var(--bg-elev);
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 6px;
  }
  .nsr-ver { font-size: 10px; color: var(--text-muted); font-family: ui-monospace, monospace; }
  .nsr-desc { flex: 1 1 100%; font-size: 11px; color: var(--text-muted); }

  /* Canvas */
  .canvas {
    position: relative;
    flex: 1 1 auto;
    min-height: 240px;
    background: var(--bg);
  }
  .canvas-state {
    height: 100%;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    color: var(--text-muted);
    gap: 8px;
  }
  .canvas-state .glyph { font-size: 40px; color: var(--accent); opacity: 0.7; }

  .legend {
    position: absolute;
    top: 10px;
    left: 10px;
    display: flex;
    gap: 12px;
    background: rgba(20, 25, 39, 0.85);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 6px 10px;
    font-size: 11px;
    z-index: 5;
  }
  .legend-item { display: flex; align-items: center; gap: 5px; }
  .swatch { width: 10px; height: 10px; border-radius: 3px; display: inline-block; }

  /* Action bar */
  .actions {
    display: flex;
    gap: 8px;
    padding: 10px 14px;
    border-top: 1px solid var(--border);
    background: var(--bg-elev);
    flex: 0 0 auto;
  }
  .sample {
    flex: 1 1 auto;
    padding: 8px 12px;
    background: var(--bg-elev-2);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    color: var(--text);
    font-size: 13px;
    outline: none;
  }
  .sample:focus { border-color: var(--accent); }

  /* Panels (clarify + test) */
  .panel {
    padding: 12px 14px;
    border-top: 1px solid var(--border);
    background: var(--bg-elev);
    max-height: 40vh;
    overflow-y: auto;
    flex: 0 0 auto;
  }
  .panel-title {
    margin: 0 0 10px;
    font-size: 12px;
    text-transform: uppercase;
    letter-spacing: 0.5px;
    color: var(--text-muted);
  }
  .q { margin-bottom: 10px; }
  .q label { display: block; font-size: 12px; margin-bottom: 4px; color: var(--text); }
  .q input, .q select {
    width: 100%;
    padding: 7px 10px;
    background: var(--bg-elev-2);
    border: 1px solid var(--border);
    border-radius: 8px;
    color: var(--text);
    font-size: 13px;
    outline: none;
  }
  .q input:focus, .q select:focus { border-color: var(--accent); }

  .trace { margin: 0; padding: 0; list-style: none; }
  .trace li { display: flex; gap: 10px; margin-bottom: 10px; }
  .step-n {
    flex: 0 0 auto;
    width: 22px;
    height: 22px;
    border-radius: 999px;
    background: var(--accent-dim);
    border: 1px solid var(--accent);
    color: var(--accent);
    font-size: 11px;
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .step-body { flex: 1 1 auto; min-width: 0; }
  .step-head { display: flex; align-items: center; gap: 8px; }
  .step-kind {
    font-size: 10px;
    text-transform: uppercase;
    background: var(--bg-elev-2);
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 6px;
    color: var(--text-muted);
  }
  .step-io { display: grid; grid-template-columns: auto 1fr; gap: 4px 8px; margin-top: 4px; }
  .io-label { font-size: 10px; text-transform: uppercase; color: var(--text-muted); }
  pre {
    margin: 0;
    white-space: pre-wrap;
    font-family: ui-monospace, monospace;
    font-size: 12px;
    background: var(--bg-elev-2);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 4px 8px;
  }
  .result { margin-top: 8px; display: grid; grid-template-columns: auto 1fr; gap: 4px 8px; }
  .muted { color: var(--text-muted); font-size: 12px; }

  /* Tier chip (subtle, readonly/active/privileged) near Save */
  .tier-chip {
    flex: 0 0 auto;
    align-self: center;
    padding: 3px 10px;
    border-radius: 999px;
    font-size: 11px;
    font-weight: 600;
    text-transform: capitalize;
    border: 1px solid var(--border);
    color: var(--text-muted);
    background: var(--bg-elev-2);
    cursor: default;
  }
  .tier-chip.tier-readonly { color: var(--ok, #36d399); border-color: var(--ok, #36d399); }
  .tier-chip.tier-active { color: var(--accent); border-color: var(--accent); }
  .tier-chip.tier-privileged {
    color: var(--warn, #f5a742);
    border-color: var(--warn, #f5a742);
    background: rgba(245, 167, 66, 0.12);
  }

  /* Consent modal */
  .modal-backdrop {
    position: fixed;
    inset: 0;
    background: rgba(8, 11, 20, 0.6);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 50;
  }
  .modal {
    width: min(460px, 92vw);
    max-height: 86vh;
    overflow-y: auto;
    background: var(--bg-elev);
    border: 1px solid var(--border);
    border-radius: 12px;
    padding: 20px;
    box-shadow: 0 12px 40px rgba(0, 0, 0, 0.45);
  }
  .modal-title { margin: 0 0 10px; font-size: 15px; color: var(--text); }
  .modal-body { margin: 0 0 12px; font-size: 13px; line-height: 1.5; color: var(--text-muted); }
  .modal-body code {
    font-family: ui-monospace, monospace;
    font-size: 12px;
    background: var(--bg-elev-2);
    border: 1px solid var(--border);
    border-radius: 4px;
    padding: 0 4px;
  }
  .consent-items { list-style: none; margin: 0 0 14px; padding: 0; display: flex; flex-direction: column; gap: 8px; }
  .consent-items li {
    background: var(--bg-elev-2);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 8px 10px;
  }
  .consent-name { display: block; font-size: 13px; font-weight: 600; color: var(--text); }
  .consent-reason { display: block; margin-top: 3px; font-size: 12px; color: var(--text-muted); }
  .modal-actions { display: flex; justify-content: flex-end; gap: 8px; }

  /* ── Edge styling (M3) ──────────────────────────────────────────────────
     xyflow renders edges in its own DOM, so we reach them with :global().
     - .cond  : conditional branch (has an `if`) — accent stroke + readable label
     - .else  : fallback leg out of a branch — dashed, muted
     - .studio-invalid : an edge flagged by validation — red stroke */
  :global(.svelte-flow__edge.studio-edge .svelte-flow__edge-path) {
    stroke: var(--text-muted, #8b93ab);
    stroke-width: 1.5;
  }
  :global(.svelte-flow__edge.studio-edge.cond .svelte-flow__edge-path) {
    stroke: var(--accent, #6c63ff);
    stroke-width: 2;
  }
  :global(.svelte-flow__edge.studio-edge.else .svelte-flow__edge-path) {
    stroke: var(--text-muted, #8b93ab);
    stroke-dasharray: 5 4;
    opacity: 0.85;
  }
  :global(.svelte-flow__edge.studio-edge.studio-invalid .svelte-flow__edge-path) {
    stroke: var(--error, #ff6b81) !important;
    stroke-width: 2.5;
  }
  :global(.svelte-flow__edge.studio-edge.selected .svelte-flow__edge-path) {
    stroke: var(--accent, #6c63ff);
    stroke-width: 2.5;
  }
  /* Edge condition label pill (rendered as an HTML div by xyflow and portaled
     into .svelte-flow__edgelabel-renderer, so it is NOT a descendant of the
     edge element — we style it uniformly here; the predicate text vs the
     literal "else" word, plus the stroke style above, carry the distinction). */
  :global(.svelte-flow__edge-label) {
    background: var(--bg-elev-2, #1b2235);
    border: 1px solid var(--border, #262e44);
    border-radius: 6px;
    padding: 1px 6px;
    font-size: 10px;
    color: var(--text, #e6e9f2);
    font-family: ui-monospace, monospace;
  }

  /* ── Node validation rings (driven by the node-wrapper class) ─────────── */
  :global(.svelte-flow__node.studio-invalid) {
    border-radius: 12px;
    box-shadow: 0 0 0 2px var(--error, #ff6b81);
  }
  :global(.svelte-flow__node.studio-warn) {
    border-radius: 12px;
    box-shadow: 0 0 0 2px var(--warn, #f5a742);
  }
</style>
