<script>
  import { classifyCode } from './codeclass.js'
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
  // ({from,to,fromPort,toPort}) => void; append a new edge to the draft.
  export let onAddEdge = () => {}
  // (index) => void; remove the draft edge at flow.edges[index].
  export let onEdgeDelete = () => {}
  // (nodeId) => void; make nodeId the flow's entry/start node (trigger target).
  export let onSetEntry = () => {}
  // (nodeId) => void; make nodeId the flow's output node (its result is delivered).
  export let onSetOutput = () => {}
  // M6 (S6.3): (nodeId, instruction) => void; asks the backend to refine the
  // selected node and replace the current workflow with the returned one.
  export let onRefine = () => {}
  // { loading, error, message } — drives the refine spinner / error / done UI.
  export let refineState = { loading: false, error: '', message: '' }
  // (nodeId) => void; remove the selected node (and its edges) from the draft.
  export let onDelete = () => {}
  // (nodeId, patch) => void; merge a patch into the selected node (used by the
  // Custom Python code editor to write inline code back into the draft).
  export let onNodeChange = () => {}
  // Framework-written Python options for a code node:
  //   scaffolds: [{kind,title,requires,code}] deterministic templates.
  //   onGenerateCode: async (nodeId) => code — the framework's model writes it.
  export let scaffolds = []
  export let onGenerateCode = null

  let generating = false

  async function generateCode() {
    if (!node || !onGenerateCode || generating) return
    generating = true
    try {
      const code = await onGenerateCode(node.id)
      if (code) { codeDraft = code; onNodeChange(node.id, { code }) }
    } finally {
      generating = false
    }
  }

  function insertScaffold(kind) {
    if (!kind) return
    const sc = (scaffolds || []).find((s) => s.kind === kind)
    if (sc) { codeDraft = sc.code; onNodeChange(node.id, { code: sc.code }) }
  }

  async function uploadPy(e) {
    const file = e.target.files && e.target.files[0]
    if (!file) return
    const text = await file.text()
    codeDraft = text
    onNodeChange(node.id, { code: text })
    e.target.value = '' // allow re-uploading the same file
  }

  // Local buffers for the editable node fields so typing doesn't rebuild the
  // graph on every keystroke; written back on blur. Re-synced when the
  // selection changes (see the lastNodeId reset below).
  let codeDraft = ''
  let inputDraft = ''
  let outputDraft = ''
  let toolDraft = ''
  let agentDraft = ''
  let paramsDraft = ''
  let paramsError = ''
  let descDraft = ''

  // Live (advisory) capability hint for the Custom Python editor. The server is
  // authoritative at save/consent — this just shows the author what they're
  // about to trip while typing.
  $: codeCaps = (node && node.kind === 'python') ? classifyCode(codeDraft) : { requires: [], dynamic: false }

  // Per-selected-node refine instruction (cleared when the selection changes).
  let refineInstruction = ''
  let lastNodeId = null
  $: if (node && node.id !== lastNodeId) {
    lastNodeId = node.id
    refineInstruction = ''
    codeDraft = node.code || ''
    inputDraft = node.input || ''
    outputDraft = node.output || ''
    toolDraft = node.tool || ''
    agentDraft = node.agent || ''
    paramsDraft = (node.params && Object.keys(node.params).length) ? JSON.stringify(node.params, null, 2) : ''
    paramsError = ''
    descDraft = node.description || ''
  }

  // Parse the params JSON editor and write it back; flag invalid JSON.
  function commitParams() {
    const raw = (paramsDraft || '').trim()
    if (!raw) { paramsError = ''; onNodeChange(node.id, { params: {} }); return }
    try {
      const parsed = JSON.parse(raw)
      paramsError = ''
      onNodeChange(node.id, { params: parsed })
    } catch (e) {
      paramsError = 'Invalid JSON: ' + (e.message || 'parse error')
    }
  }

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

  // ── Edge wiring helpers (rewire / add connections) ──────────────────────────
  $: flowNodes = (workflow && workflow.flow && Array.isArray(workflow.flow.nodes))
    ? workflow.flow.nodes : []
  $: nodeIds = flowNodes.map((n) => n.id)
  // Friendly label for a node id (tool/agent name), so the connection dropdowns
  // read as "read_skill (skill_1)" instead of a bare id.
  function nodeLabel(id) {
    const n = flowNodes.find((x) => x.id === id)
    if (!n) return id
    const name = n.tool || n.agent || ''
    return name && name !== id ? `${name} (${id})` : id
  }
  // Is the currently-selected node the flow's entry/start node?
  $: isEntryNode = !!(node && workflow && workflow.flow && workflow.flow.entry === node.id)
  // Is it the flow's designated output node (its result is delivered to channels)?
  $: isOutputNode = !!(node && workflow && workflow.flow && workflow.flow.output === node.id)

  // Declared ports for a node by direction ('in' | 'out'); empty when the node
  // uses the implicit single handle (then we just offer the default).
  function portsOf(id, dir) {
    const n = flowNodes.find((x) => x.id === id)
    if (!n) return []
    const arr = dir === 'out' ? n.outputs : n.inputs
    return Array.isArray(arr) ? arr.map((p) => p.name).filter(Boolean) : []
  }

  // Retarget one end of the selected edge. Changing the node resets that end's
  // port (ports are node-specific) so we never keep a stale/invalid port.
  function retargetEdge(end, value) {
    if (!selectedEdge) return
    const patch = end === 'from'
      ? { from: value, fromPort: '' }
      : { to: value, toPort: '' }
    onEdgeChange(selectedEdge.index, patch)
  }

  // Special endpoints for the connection form so ANY connector can be wired
  // without dragging on the canvas: Trigger as a source (sets the entry) and
  // Output as a target (sets the output node).
  const TRIGGER_OPT = '__trigger__'
  const OUTPUT_OPT = '__output__'
  // Source options: Trigger + every node. Target options: every node + Output.
  $: sourceOptions = [{ id: TRIGGER_OPT, label: '▶ Trigger (start)' }, ...flowNodes.map((n) => ({ id: n.id, label: nodeLabel(n.id) }))]
  $: targetOptions = [...flowNodes.map((n) => ({ id: n.id, label: nodeLabel(n.id) })), { id: OUTPUT_OPT, label: '◆ Output (channels)' }]

  // New-connection draft for the "Add connection" form (no node selected).
  let newEdge = { from: '', to: '' }
  function submitNewEdge() {
    const { from, to } = newEdge
    if (!from || !to || from === to) return
    // Trigger → node sets the entry; node → Output sets the output node;
    // anything else is a normal stored edge. All three persist with Save.
    if (from === TRIGGER_OPT && to === OUTPUT_OPT) return
    if (from === TRIGGER_OPT) { onSetEntry(to); newEdge = { from: '', to: '' }; return }
    if (to === OUTPUT_OPT) { onSetOutput(from); newEdge = { from: '', to: '' }; return }
    onAddEdge({ from, to })
    newEdge = { from: '', to: '' }
  }
</script>

<aside class="inspector" aria-label="Node inspector">
  <h2 class="insp-title">Inspector</h2>

  {#if selectedEdge}
    <!-- Selected edge: editable `if` predicate + read-only endpoints/ports. -->
    <p class="insp-hint">Editing a connection. Re-point either end to reroute the flow; the predicate decides when this branch is taken (leave it empty for the fallback “else” leg).</p>

    <label class="field-label" for="edge-from">from (source node)</label>
    <select id="edge-from" value={selectedEdge.edge.from || ''}
      on:change={(e) => retargetEdge('from', e.target.value)}>
      {#each nodeIds as id}
        <option value={id} disabled={id === selectedEdge.edge.to}>{nodeLabel(id)}</option>
      {/each}
    </select>
    {#if portsOf(selectedEdge.edge.from, 'out').length}
      <label class="field-label" for="edge-fromport">from port</label>
      <select id="edge-fromport" value={selectedEdge.edge.fromPort || ''}
        on:change={(e) => onEdgeChange(selectedEdge.index, { fromPort: e.target.value })}>
        <option value="">(default)</option>
        {#each portsOf(selectedEdge.edge.from, 'out') as p}<option value={p}>{p}</option>{/each}
      </select>
    {/if}

    <label class="field-label" for="edge-to">to (target node)</label>
    <select id="edge-to" value={selectedEdge.edge.to || ''}
      on:change={(e) => retargetEdge('to', e.target.value)}>
      {#each nodeIds as id}
        <option value={id} disabled={id === selectedEdge.edge.from}>{nodeLabel(id)}</option>
      {/each}
    </select>
    {#if portsOf(selectedEdge.edge.to, 'in').length}
      <label class="field-label" for="edge-toport">to port</label>
      <select id="edge-toport" value={selectedEdge.edge.toPort || ''}
        on:change={(e) => onEdgeChange(selectedEdge.index, { toPort: e.target.value })}>
        <option value="">(default)</option>
        {#each portsOf(selectedEdge.edge.to, 'in') as p}<option value={p}>{p}</option>{/each}
      </select>
    {/if}

    <label class="field-label" for="edge-if">if (predicate)</label>
    <input
      id="edge-if"
      type="text"
      placeholder="e.g. score > 0.5"
      value={selectedEdge.edge.if || ''}
      on:input={(e) => onEdgeChange(selectedEdge.index, { if: e.target.value })}
    />
    <p class="insp-hint">Writes back to the draft so Test / Save / Validate use this condition.</p>
    <button type="button" class="btn danger sm" on:click={() => onEdgeDelete(selectedEdge.index)}>Delete connection</button>
  {:else if node}
    <!-- Selected node: editable configuration. Each field writes back to the
         draft on blur/change so Test / Save / Validate use the edited values. -->
    <dl class="fields">
      <dt>id</dt><dd>{node.id}</dd>
      <dt>kind</dt><dd>{node.kind || '—'}</dd>
    </dl>

    <!-- Entry/start node: the trigger always flows into the entry node, so this
         is how you "connect" the trigger to a given node. -->
    {#if isEntryNode}
      <p class="insp-entry">★ Start node — the trigger enters here.</p>
    {:else}
      <button type="button" class="btn sm" on:click={() => onSetEntry(node.id)}>
        Make this the start node
      </button>
      <p class="insp-hint">The trigger feeds the start node. Set this to route the trigger here.</p>
    {/if}

    {#if isOutputNode}
      <p class="insp-entry">◆ Output node — this node's result is delivered to the channels.</p>
    {:else}
      <button type="button" class="btn sm" on:click={() => onSetOutput(node.id)}>
        Make this the output node
      </button>
      <p class="insp-hint">The output node's result is what gets sent to the output channels (or drag this node onto the output box).</p>
    {/if}

    <label class="field-label" for="node-desc">what this node does</label>
    <input id="node-desc" type="text" placeholder="e.g. Search the web for today's AI news"
      bind:value={descDraft} on:blur={() => onNodeChange(node.id, { description: descDraft })} />

    {#if node.kind === 'tool' || node.tool}
      <label class="field-label" for="node-tool">tool</label>
      <input id="node-tool" type="text" placeholder="tool name (e.g. web_search)"
        bind:value={toolDraft} on:blur={() => onNodeChange(node.id, { tool: toolDraft })} />
    {/if}
    {#if node.kind === 'agent' || node.agent}
      <label class="field-label" for="node-agent">agent</label>
      <input id="node-agent" type="text" placeholder="peer agent id"
        bind:value={agentDraft} on:blur={() => onNodeChange(node.id, { agent: agentDraft })} />
    {/if}

    <label class="field-label" for="node-input">
      {node.kind === 'agent' ? 'task prompt' : node.kind === 'tool' ? 'arguments (JSON)' : 'input'}
    </label>
    <textarea id="node-input" class="node-input" rows="4" spellcheck="false"
      placeholder={node.kind === 'tool' ? '{"query": "…"}' : node.kind === 'agent' ? 'What should this agent do, using which data?' : 'input template'}
      bind:value={inputDraft} on:blur={() => onNodeChange(node.id, { input: inputDraft })}></textarea>
    <p class="insp-hint">
      Reference an upstream output with <code>{'{{ toJson .var }}'}</code> (JSON) or
      <code>{'{{ .var }}'}</code> (text). Saved when you click away.
    </p>

    <label class="field-label" for="node-output">output variable</label>
    <input id="node-output" type="text" placeholder="e.g. articles"
      bind:value={outputDraft} on:blur={() => onNodeChange(node.id, { output: outputDraft })} />

    <label class="field-label" for="node-onerror">on error</label>
    <select id="node-onerror" value={node.on_error || 'abort'}
      on:change={(e) => onNodeChange(node.id, { on_error: e.target.value })}>
      <option value="abort">abort — stop the workflow</option>
      <option value="skip">skip — continue without this step</option>
      <option value="retry">retry — try once more, then abort</option>
    </select>

    {#if node.kind === 'python'}
      <!-- Custom Python block: edit the inline script. Written back on blur. -->
      <section class="frame">
        <h3 class="sub">Python</h3>
        <!-- Three in-framework ways to get this node's code; no external service. -->
        <div class="codegen-bar">
          {#if onGenerateCode}
            <button class="btn-codegen" type="button" on:click={generateCode} disabled={generating}
              title="The framework's configured model writes this node's code">
              {#if generating}<span class="spinner" aria-hidden="true"></span> Writing…{:else}✨ Generate{/if}
            </button>
          {/if}
          {#if scaffolds && scaffolds.length}
            <select class="scaffold-select" on:change={(e) => { insertScaffold(e.target.value); e.target.value = '' }} title="Insert a deterministic framework scaffold">
              <option value="">Scaffold…</option>
              {#each scaffolds as s}<option value={s.kind}>{s.title}</option>{/each}
            </select>
          {/if}
          <label class="btn-upload" title="Upload your own .py for this node">
            ⤓ Upload .py
            <input type="file" accept=".py,text/x-python,text/plain" on:change={uploadPy} hidden />
          </label>
        </div>
        <label class="field-label" for="py-code">code</label>
        <textarea
          id="py-code"
          class="code-input"
          rows="12"
          spellcheck="false"
          bind:value={codeDraft}
          on:blur={() => onNodeChange(node.id, { code: codeDraft })}
        ></textarea>
        <p class="insp-hint">
          Inputs arrive as <code>inputs</code>; the value you return becomes this
          node's output. Saved when you click away.
        </p>

        {#if codeCaps.requires.length || codeCaps.dynamic}
          <div class="caps">
            <span class="caps-label">needs consent:</span>
            {#each codeCaps.requires as cap}
              <span class="cap cap-{cap}">{cap}</span>
            {/each}
            {#if codeCaps.dynamic}<span class="cap cap-dynamic">dynamic</span>{/if}
          </div>
          <p class="insp-hint caps-hint">
            This step runs beyond the default guardrails — saving it will require
            your explicit, per-step consent.
          </p>
        {:else}
          <div class="caps caps-ok"><span class="cap cap-ok">read-only · no consent needed</span></div>
        {/if}
      </section>
    {/if}

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

    <label class="field-label" for="node-params">params (JSON)</label>
    <textarea id="node-params" class="node-input" rows="3" spellcheck="false"
      placeholder={'{ "timeout_s": 600 }'}
      bind:value={paramsDraft} on:blur={commitParams}></textarea>
    {#if paramsError}<p class="insp-hint caps-hint">⚠ {paramsError}</p>{/if}

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

    <!-- Remove the selected node (and any edges touching it) from the draft. -->
    <button
      class="btn-delete-node"
      type="button"
      on:click={() => onDelete(node.id)}
      title="Delete this node (or press Delete)"
    >
      🗑 Delete node
    </button>
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
              <button type="button" class="edge-del" title="Delete connection"
                on:click={() => onEdgeDelete(index)} aria-label="Delete connection">×</button>
            </li>
          {/each}
        </ul>
      {/if}

      <!-- Add ANY connection by picking source + target. Includes the Trigger
           (as a source) and Output (as a target) so every connector is wireable
           here without dragging — and every choice persists with Save. -->
      {#if nodeIds.length >= 1}
        <div class="edge-add">
          <select aria-label="New connection source" bind:value={newEdge.from}>
            <option value="" disabled>from…</option>
            {#each sourceOptions as o}<option value={o.id} disabled={o.id === newEdge.to}>{o.label}</option>{/each}
          </select>
          <span class="edge-arrow">→</span>
          <select aria-label="New connection target" bind:value={newEdge.to}>
            <option value="" disabled>to…</option>
            {#each targetOptions as o}<option value={o.id} disabled={o.id === newEdge.from}>{o.label}</option>{/each}
          </select>
          <button type="button" class="btn sm" on:click={submitNewEdge}
            disabled={!newEdge.from || !newEdge.to || newEdge.from === newEdge.to}>Connect</button>
        </div>
        <p class="insp-hint">Pick any source → any target (Trigger and Output included). Saved with the workflow. You can also drag on the canvas.</p>
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
  .insp-entry { font-size: 12px; color: var(--accent); font-weight: 600; margin: 0 0 12px; }
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
  .btn-delete-node {
    margin-top: 14px;
    width: 100%;
    padding: 8px 12px;
    background: transparent;
    border: 1px solid var(--error);
    border-radius: 8px;
    color: var(--error);
    font-size: 13px;
    font-weight: 600;
    cursor: pointer;
  }
  .btn-delete-node:hover { background: var(--error); color: #fff; }
  .code-input {
    width: 100%;
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    font-size: 12px;
    line-height: 1.5;
    tab-size: 4;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 8px;
    color: var(--text);
    padding: 8px 10px;
    resize: vertical;
    white-space: pre;
    overflow-wrap: normal;
    overflow-x: auto;
  }
  .code-input:focus { outline: none; border-color: var(--accent); }
  .node-input {
    width: 100%; font-family: ui-monospace, Menlo, monospace; font-size: 12px;
    line-height: 1.5; background: var(--bg); border: 1px solid var(--border);
    border-radius: 8px; color: var(--text); padding: 7px 9px; resize: vertical;
  }
  .node-input:focus { outline: none; border-color: var(--accent); }
  .codegen-bar { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; margin: 6px 0; }
  .btn-codegen, .btn-upload {
    font-size: 12px; font-weight: 600; padding: 5px 10px; border-radius: 7px;
    border: 1px solid var(--accent); color: var(--accent); background: var(--accent-dim);
    cursor: pointer; display: inline-flex; align-items: center; gap: 6px;
  }
  .btn-codegen:hover:not(:disabled), .btn-upload:hover { filter: brightness(1.1); }
  .btn-codegen:disabled { opacity: 0.6; cursor: not-allowed; }
  .scaffold-select { width: auto; flex: 0 0 auto; font-size: 12px; padding: 4px 8px; }
  .caps { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; margin-top: 8px; }
  .caps-label { font-size: 11px; color: var(--text-muted); }
  .cap {
    font-size: 11px; font-weight: 600; padding: 2px 8px; border-radius: 999px;
    border: 1px solid var(--border); text-transform: lowercase;
  }
  .cap-system  { background: rgba(255,107,129,.14); border-color: var(--error); color: var(--error); }
  .cap-network { background: rgba(245,197,66,.14);  border-color: var(--warn);  color: var(--warn); }
  .cap-dynamic { background: rgba(245,197,66,.14);  border-color: var(--warn);  color: var(--warn); }
  .cap-ok      { background: rgba(54,211,153,.12);  border-color: var(--ok);    color: var(--ok); }
  .caps-hint { color: var(--warn); }
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

  /* Inline delete on each edge row + the add-connection form. */
  .edge-row { position: relative; }
  .edge-del {
    position: absolute;
    top: 6px;
    right: 6px;
    border: none;
    background: transparent;
    color: var(--text-muted);
    font-size: 15px;
    line-height: 1;
    cursor: pointer;
    padding: 0 4px;
    border-radius: 4px;
  }
  .edge-del:hover { background: var(--error); color: #fff; }
  .edge-add {
    display: flex;
    align-items: center;
    gap: 6px;
    margin-top: 10px;
    flex-wrap: wrap;
  }
  .edge-add select { flex: 1 1 auto; min-width: 64px; }

  /* Lightweight buttons used by the edge tools (Inspector has no global .btn). */
  .btn {
    border: 1px solid var(--border);
    background: var(--bg-elev-2);
    color: var(--text);
    border-radius: 6px;
    padding: 5px 10px;
    cursor: pointer;
    font-size: 12px;
  }
  .btn:hover:not(:disabled) { filter: brightness(1.1); }
  .btn:disabled { opacity: 0.5; cursor: not-allowed; }
  .btn.sm { padding: 4px 8px; font-size: 11px; }
  .btn.danger { border-color: var(--error); color: var(--error); }
  .btn.danger:hover:not(:disabled) { background: var(--error); color: #fff; }
</style>
