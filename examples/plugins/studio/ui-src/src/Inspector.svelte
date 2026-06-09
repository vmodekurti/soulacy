<script>
  // Right-hand inspector.
  //   - When a flow node is selected: read-only view of its fields (Wave 1).
  //   - Otherwise (no node selected): editable workflow FRAMING — the trigger
  //     and the output channel(s). Edits are written back into the in-memory
  //     draft via onChange so subsequent Test/Save use the edited values, and
  //     the canvas re-renders the START / SINK chips.
  export let node = null        // raw flow node | null
  export let selectedEdge = null // { index, edge } when an edge is selected | null
  export let workflow = null    // the in-memory draft { trigger, channels, ... }
  export let channels = []      // [{ id, name }] from the catalog
  export let onChange = () => {} // (patch) => void; patch merged into workflow
  // (index, patch) => void; patch merged into the draft edge at flow.edges[index].
  export let onEdgeChange = () => {}
  // M6 (S6.3): (nodeId, instruction) => void; asks the backend to refine the
  // selected node and replace the current workflow with the returned one.
  export let onRefine = () => {}
  // { loading, error, message } — drives the refine spinner / error / done UI.
  export let refineState = { loading: false, error: '', message: '' }

  // Per-selected-node refine instruction (cleared when the selection changes).
  let refineInstruction = ''
  let lastNodeId = null
  $: if (node && node.id !== lastNodeId) { lastNodeId = node.id; refineInstruction = '' }

  function submitRefine() {
    const instr = (refineInstruction || '').trim()
    if (!node || !instr || refineState.loading) return
    onRefine(node.id, instr)
  }

  // The raw draft edges (for the "Edges" list shown when nothing is selected).
  $: draftEdges = (workflow && workflow.flow && Array.isArray(workflow.flow.edges))
    ? workflow.flow.edges : []
  // Real (non-terminal) edges, paired with their true index in flow.edges so
  // edits write back to the right slot.
  $: editableEdges = draftEdges
    .map((e, index) => ({ e, index }))
    .filter(({ e }) => e && e.to && e.to !== 'end')

  const TRIGGER_TYPES = ['schedule', 'channel', 'webhook', 'manual']
  const CRON_HINTS = [
    '0 8 * * 1-5  =  weekdays 8am',
    '*/15 * * * *  =  every 15 minutes',
    '0 0 * * 0  =  Sundays at midnight',
  ]

  function entries(params) {
    if (!params || typeof params !== 'object') return []
    return Object.entries(params)
  }
  function fmt(v) {
    if (v == null) return ''
    if (typeof v === 'object') return JSON.stringify(v, null, 2)
    return String(v)
  }

  // ── Trigger editing ───────────────────────────────────────────────────────
  $: trigger = (workflow && workflow.trigger) || { type: '', config: {} }
  $: triggerType = trigger.type || ''
  $: triggerCron = (trigger.config && trigger.config.cron) || ''
  $: triggerChannel = (trigger.config && trigger.config.channel) || ''

  function setTriggerType(t) {
    // Reset config to the shape the new type needs (keep nothing stale).
    onChange({ trigger: { type: t, config: {} } })
  }
  function setTriggerCron(cron) {
    onChange({ trigger: { type: 'schedule', config: { ...(trigger.config || {}), cron } } })
  }
  function setTriggerChannel(ch) {
    onChange({ trigger: { type: 'channel', config: { ...(trigger.config || {}), channel: ch } } })
  }

  // ── Output channel(s) editing (multi) ─────────────────────────────────────
  // workflow.channels is an array of channel ids (strings) or {type/name} objs;
  // we normalise to a Set of selected ids for the multi-picker.
  function channelKey(ch) {
    if (typeof ch === 'string') return ch
    return (ch && (ch.id || ch.type || ch.name)) || ''
  }
  $: selectedChannels = new Set(
    ((workflow && Array.isArray(workflow.channels)) ? workflow.channels : []).map(channelKey),
  )
  function toggleOutputChannel(id, on) {
    const next = new Set(selectedChannels)
    if (on) next.add(id)
    else next.delete(id)
    onChange({ channels: Array.from(next) })
  }
</script>

<aside class="inspector" aria-label="Node inspector">
  <h2 class="insp-title">Inspector</h2>

  {#if selectedEdge}
    <!-- Selected edge: editable `if` predicate + read-only endpoints/ports. -->
    <p class="insp-hint">Editing a connection. The predicate decides when this branch is taken; leave it empty for the fallback (“else”) leg.</p>
    <dl class="fields">
      <dt>from</dt><dd>{selectedEdge.edge.from || '—'}{#if selectedEdge.edge.fromPort} <span class="port">·{selectedEdge.edge.fromPort}</span>{/if}</dd>
      <dt>to</dt><dd>{selectedEdge.edge.to || '—'}{#if selectedEdge.edge.toPort} <span class="port">·{selectedEdge.edge.toPort}</span>{/if}</dd>
    </dl>
    <label class="field-label" for="edge-if">if (predicate)</label>
    <input
      id="edge-if"
      type="text"
      placeholder="e.g. score > 0.5"
      value={selectedEdge.edge.if || ''}
      on:input={(e) => onEdgeChange(selectedEdge.index, { if: e.target.value })}
    />
    <p class="insp-hint">Writes back to the draft so Test / Save / Validate use this condition.</p>
  {:else if node}
    <!-- Selected node: read-only fields (unchanged Wave 1 behaviour). -->
    <dl class="fields">
      <dt>id</dt><dd>{node.id}</dd>
      <dt>kind</dt><dd>{node.kind || '—'}</dd>
      {#if node.tool}<dt>tool</dt><dd>{node.tool}</dd>{/if}
      {#if node.agent}<dt>agent</dt><dd>{node.agent}</dd>{/if}
      <dt>input</dt><dd>{fmt(node.input) || '—'}</dd>
      <dt>output</dt><dd>{fmt(node.output) || '—'}</dd>
    </dl>

    {#if (node.inputs && node.inputs.length) || (node.outputs && node.outputs.length)}
      <h3 class="sub">ports</h3>
      <dl class="fields">
        {#each (node.inputs || []) as p}
          <dt>in</dt><dd>{p.label || p.name}{#if p.type} <span class="port">:{p.type}</span>{/if}</dd>
        {/each}
        {#each (node.outputs || []) as p}
          <dt>out</dt><dd>{p.label || p.name}{#if p.type} <span class="port">:{p.type}</span>{/if}</dd>
        {/each}
      </dl>
    {/if}

    <h3 class="sub">params</h3>
    {#if entries(node.params).length === 0}
      <p class="insp-empty">No params.</p>
    {:else}
      <dl class="fields">
        {#each entries(node.params) as [k, v]}
          <dt>{k}</dt><dd><pre>{fmt(v)}</pre></dd>
        {/each}
      </dl>
    {/if}

    <!-- M6 (S6.3): refine this node from a natural-language instruction. The
         backend returns a NEW workflow that replaces the current one. -->
    <section class="frame refine">
      <h3 class="sub">Refine</h3>
      <label class="field-label" for="refine-instr">describe a change</label>
      <textarea
        id="refine-instr"
        class="refine-input"
        rows="2"
        placeholder="e.g. use the summarize tool instead, or add error handling"
        bind:value={refineInstruction}
        on:keydown={(e) => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); submitRefine() } }}
        disabled={refineState.loading}
      ></textarea>
      <button
        class="btn-refine"
        type="button"
        on:click={submitRefine}
        disabled={refineState.loading || !refineInstruction.trim()}
      >
        {#if refineState.loading}<span class="spinner" aria-hidden="true"></span> Refining…{:else}Apply{/if}
      </button>
      {#if refineState.error}<p class="refine-msg refine-err">⚠ {refineState.error}</p>{/if}
      {#if refineState.message && !refineState.loading}<p class="refine-msg refine-ok">✓ {refineState.message}</p>{/if}
      <p class="insp-hint">Replaces the workflow with the refined version, then re-validates.</p>
    </section>
  {:else if workflow}
    <!-- No node selected: edit the workflow framing (trigger + output). -->
    <p class="insp-hint">Editing how this workflow starts and where it sends output. Select a node to inspect it.</p>

    <section class="frame">
      <h3 class="sub">Trigger</h3>
      <label class="field-label" for="trigger-type">type</label>
      <select
        id="trigger-type"
        value={triggerType}
        on:change={(e) => setTriggerType(e.target.value)}
      >
        <option value="" disabled selected={!triggerType}>Choose…</option>
        {#each TRIGGER_TYPES as t}
          <option value={t} selected={t === triggerType}>{t}</option>
        {/each}
      </select>

      {#if triggerType === 'schedule'}
        <label class="field-label" for="trigger-cron">cron</label>
        <input
          id="trigger-cron"
          type="text"
          placeholder="0 8 * * 1-5"
          value={triggerCron}
          on:input={(e) => setTriggerCron(e.target.value)}
        />
        <ul class="hints">
          {#each CRON_HINTS as h}<li>{h}</li>{/each}
        </ul>
      {:else if triggerType === 'channel'}
        <label class="field-label" for="trigger-channel">channel</label>
        <select
          id="trigger-channel"
          value={triggerChannel}
          on:change={(e) => setTriggerChannel(e.target.value)}
        >
          <option value="" disabled selected={!triggerChannel}>Choose channel…</option>
          {#each channels as ch}
            <option value={ch.id} selected={ch.id === triggerChannel}>{ch.name || ch.id}</option>
          {/each}
        </select>
        {#if !channels.length}<p class="insp-empty">No channels in catalog.</p>{/if}
      {:else if triggerType === 'webhook'}
        <p class="insp-hint">Fires when an inbound webhook is received.</p>
      {:else if triggerType === 'manual'}
        <p class="insp-hint">Runs only when triggered by hand.</p>
      {/if}
    </section>

    <section class="frame">
      <h3 class="sub">Output channels</h3>
      {#if !channels.length}
        <p class="insp-empty">No channels in catalog.</p>
      {:else}
        <ul class="checklist">
          {#each channels as ch}
            <li>
              <label>
                <input
                  type="checkbox"
                  checked={selectedChannels.has(ch.id)}
                  on:change={(e) => toggleOutputChannel(ch.id, e.target.checked)}
                />
                <span>{ch.name || ch.id}</span>
              </label>
            </li>
          {/each}
        </ul>
      {/if}
    </section>

    <!-- Edges list: edit each branch/flow edge's `if` predicate without having
         to click the edge on the canvas (also the fallback if edge-selection is
         awkward in this xyflow build). -->
    <section class="frame">
      <h3 class="sub">Edges &amp; conditions</h3>
      {#if !editableEdges.length}
        <p class="insp-empty">No edges yet.</p>
      {:else}
        <ul class="edge-list">
          {#each editableEdges as { e, index } (index)}
            <li class="edge-row">
              <div class="edge-ends">
                <span class="edge-from">{e.from}{#if e.fromPort}<span class="port">·{e.fromPort}</span>{/if}</span>
                <span class="edge-arrow">→</span>
                <span class="edge-to">{e.to}{#if e.toPort}<span class="port">·{e.toPort}</span>{/if}</span>
              </div>
              <input
                class="edge-if"
                type="text"
                placeholder="if predicate (empty = else)"
                value={e.if || ''}
                on:input={(ev) => onEdgeChange(index, { if: ev.target.value })}
              />
            </li>
          {/each}
        </ul>
      {/if}
    </section>
  {:else}
    <p class="insp-empty">Generate a workflow to edit its trigger and output.</p>
  {/if}
</aside>

<style>
  .inspector {
    flex: 0 0 280px;
    background: var(--bg-elev);
    border-left: 1px solid var(--border);
    padding: 16px;
    overflow-y: auto;
  }
  .insp-title {
    margin: 0 0 12px;
    font-size: 13px;
    text-transform: uppercase;
    letter-spacing: 1px;
    color: var(--text-muted);
  }
  .insp-empty { font-size: 12px; color: var(--text-muted); }
  .insp-hint { font-size: 12px; color: var(--text-muted); margin: 0 0 12px; }
  .sub {
    margin: 18px 0 8px;
    font-size: 12px;
    text-transform: uppercase;
    letter-spacing: 0.5px;
    color: var(--text-muted);
  }
  .frame { border-top: 1px solid var(--border); padding-top: 4px; margin-top: 6px; }
  .field-label {
    display: block;
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.5px;
    color: var(--text-muted);
    margin: 10px 0 4px;
  }
  select, input[type='text'] {
    width: 100%;
    padding: 7px 10px;
    background: var(--bg-elev-2);
    border: 1px solid var(--border);
    border-radius: 8px;
    color: var(--text);
    font-size: 13px;
    outline: none;
  }
  select:focus, input[type='text']:focus { border-color: var(--accent); }
  .hints {
    list-style: none;
    margin: 6px 0 0;
    padding: 0;
    font-family: ui-monospace, monospace;
    font-size: 11px;
    color: var(--text-muted);
  }
  .hints li { margin: 2px 0; }
  .checklist { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
  .checklist label { display: flex; align-items: center; gap: 8px; font-size: 13px; color: var(--text); cursor: pointer; }
  .checklist input[type='checkbox'] { accent-color: var(--accent); }
  .fields { margin: 0; }
  .fields dt {
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.5px;
    color: var(--text-muted);
    margin-top: 10px;
  }
  .fields dd {
    margin: 2px 0 0;
    font-size: 13px;
    color: var(--text);
    word-break: break-word;
  }
  .fields pre {
    margin: 0;
    white-space: pre-wrap;
    font-family: ui-monospace, monospace;
    font-size: 12px;
    background: var(--bg-elev-2);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 6px 8px;
  }
  .port {
    font-family: ui-monospace, monospace;
    font-size: 11px;
    color: var(--text-muted);
  }

  /* M6: refine block */
  .refine textarea.refine-input {
    width: 100%;
    box-sizing: border-box;
    padding: 7px 10px;
    background: var(--bg-elev-2);
    border: 1px solid var(--border);
    border-radius: 8px;
    color: var(--text);
    font-size: 13px;
    resize: vertical;
    outline: none;
    font-family: inherit;
  }
  .refine textarea.refine-input:focus { border-color: var(--accent); }
  .btn-refine {
    margin-top: 8px;
    width: 100%;
    padding: 8px 12px;
    background: var(--accent);
    border: 1px solid var(--accent);
    border-radius: 8px;
    color: #fff;
    font-size: 13px;
    font-weight: 600;
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 8px;
  }
  .btn-refine:hover:not(:disabled) { filter: brightness(1.08); }
  .btn-refine:disabled { opacity: 0.5; cursor: not-allowed; }
  .spinner {
    width: 12px;
    height: 12px;
    border: 2px solid rgba(255, 255, 255, 0.4);
    border-top-color: #fff;
    border-radius: 50%;
    display: inline-block;
    animation: insp-spin 0.7s linear infinite;
  }
  @keyframes insp-spin { to { transform: rotate(360deg); } }
  .refine-msg { font-size: 12px; margin: 8px 0 0; }
  .refine-err { color: var(--error, #ff6b81); }
  .refine-ok { color: var(--ok, #36d399); }

  /* Edges list */
  .edge-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 10px; }
  .edge-row {
    background: var(--bg-elev-2);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 8px;
  }
  .edge-ends {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
    color: var(--text);
    margin-bottom: 6px;
    flex-wrap: wrap;
  }
  .edge-arrow { color: var(--text-muted); }
  .edge-if { width: 100%; }
</style>
