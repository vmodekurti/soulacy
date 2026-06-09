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

  // xyflow stores (the component expects writable stores for nodes/edges).
  const nodes = writable([])
  const edges = writable([])
  let selectedNode = null     // raw flow node for the inspector

  function rebuildGraph() {
    if (!workflow) {
      nodes.set([]); edges.set([]); return
    }
    const flow = toFlow(workflow)
    nodes.set(flow.nodes)
    edges.set(flow.edges)
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
    selectedNode = null
    rebuildGraph()
  }

  function onNodeClick(event) {
    // SvelteFlow dispatches { node } in event.detail.
    const n = event?.detail?.node
    if (!n || !n.data || !n.data.node) { selectedNode = null; return }
    selectedNode = n.data.node
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

  // ── Save ──────────────────────────────────────────────────────────────────
  let saving = false
  let saveError = ''
  let saveMsg = ''

  async function save() {
    if (!workflow || saving) return
    saving = true
    saveError = ''
    saveMsg = ''
    try {
      const res = await bridge.save(workflow)
      const id = (res && res.agentId) || '(unknown)'
      saveMsg = `Saved as disabled agent ${id} — enable it from the Agents page.`
    } catch (e) {
      saveError = e.message || 'save failed'
    } finally {
      saving = false
    }
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
            on:paneclick={() => (selectedNode = null)}
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
          <button class="btn primary" on:click={save} disabled={saving}>
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

    <Inspector node={selectedNode} />
  </main>
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
</style>
