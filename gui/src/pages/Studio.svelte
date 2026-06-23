<script>
  import { onMount, onDestroy } from 'svelte'
  import {
    SvelteFlow, Background, Controls, MiniMap, Position,
  } from '@xyflow/svelte'
  import '@xyflow/svelte/dist/style.css'
  import { writable, get } from 'svelte/store'

  // ARCH-6: Studio is now a first-class page of the core dashboard, not a
  // sandboxed iframe plugin. The old postMessage RPC bridge is replaced by a
  // thin client that calls the gateway directly through the GUI's
  // authenticated `api` session (studioApi.js keeps the same `bridge` shape).
  import { bridge } from '../lib/studio/studioApi.js'
  import { editAgent, studioSession } from '../lib/stores.js'
  import { toFlow, kindMeta } from '../lib/studio/graph.js'
  import Palette from '../lib/studio/Palette.svelte'
  import Inspector from '../lib/studio/Inspector.svelte'
  import YamlView from '../lib/studio/YamlView.svelte'
  import StudioNode from '../lib/studio/nodes/StudioNode.svelte'
  import TriggerNode from '../lib/studio/nodes/TriggerNode.svelte'
  import OutputNode from '../lib/studio/nodes/OutputNode.svelte'
  import '../lib/studio/studio.css'

  // (Removed) The old iframe build scrubbed the plugin token from the URL
  // fragment on mount. Embedded in the SPA we hold no token and must NOT touch
  // the hash — the core dashboard uses it for routing (#studio).

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

  // Derive a COMPACT catalog of installed capability NAMES from the raw catalog
  // payload and thread it into compile, so the backend can flag missing
  // capabilities (M4). Without it the backend's empty-catalog guard returns no
  // suggestions. Keys MUST match the studio Request.Catalog JSON tags:
  //   { tools:[], agents:[], providers:[] }
  //
  // Raw shapes (see PluginFrame.svelte handleCatalogRequest):
  //   agents:    { agents:[{id,name,...}], count }            (GET /agents)
  //   tools:     { python_tools:[{name}], mcp_tools:[{name,server}],
  //                builtins:[{name}] }                        (GET /tool-catalog)
  //   providers: { providers:{<id>:{...}}, default_provider } (GET /providers)
  function compactCatalog(cat) {
    if (!cat) return undefined

    // Tools: unique names across python_tools + mcp_tools + builtins.
    const t = cat.tools || {}
    const toolNames = []
    const seenTool = new Set()
    const pushTool = (name) => {
      const n = (name == null ? '' : String(name)).trim()
      if (!n || seenTool.has(n)) return
      seenTool.add(n)
      toolNames.push(n)
    }
    for (const arr of [t.python_tools, t.mcp_tools, t.builtins]) {
      if (Array.isArray(arr)) for (const x of arr) pushTool(x && x.name)
    }

    // Agents: both ids AND names (a draft may reference either).
    const agentList = (cat.agents && Array.isArray(cat.agents.agents)) ? cat.agents.agents : []
    const agentNames = []
    const seenAgent = new Set()
    const pushAgent = (name) => {
      const n = (name == null ? '' : String(name)).trim()
      if (!n || seenAgent.has(n)) return
      seenAgent.add(n)
      agentNames.push(n)
    }
    for (const a of agentList) {
      pushAgent(a && a.id)
      pushAgent(a && a.name)
    }

    // Providers: the provider ids (keys of the providers map).
    const provMap = (cat.providers && cat.providers.providers) || {}
    const providerNames = (provMap && typeof provMap === 'object') ? Object.keys(provMap) : []

    return { tools: toolNames, agents: agentNames, providers: providerNames }
  }

  // ── Compile loop ──────────────────────────────────────────────────────────
  let intent = ''
  let compiling = false
  let compileError = ''
  let workflow = null
  let questions = []
  let notes = []
  let answers = {}            // { [questionId]: value }
  let explanation = null      // plain-language "what Studio built" (Story #10)
  let modelAdvice = null      // builder-model advice (local-first)
  let cloudAck = false        // user acknowledged cloud builder this session
  let cloudGate = null        // { provider, model } when the escalation dialog is open

  // ── Pre-generation prompt refinement ──────────────────────────────────────
  // Pressing Generate first runs a refine pass: the framework LLM rewrites the
  // rough intent into a clear spec, lists the assumptions it made, and asks any
  // clarifying questions. The dialog below shows all of it for confirmation;
  // the workflow is only compiled after the user confirms (and may edit the
  // refined intent / answer questions first). `refinement` is the open dialog
  // state; null when closed.
  let promptViewer = false     // full-prompt read/edit modal open
  let refinement = null        // { original, refined_intent, summary, assumptions[], questions[] }
  let refining = false         // refine request in flight
  let refineAnswers = {}       // { [questionId]: value } for refinement questions

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

  // ── Palette drag-and-drop (create nodes by dropping) ──────────────────────
  // Palette items carry an `application/studio-node` payload. Dropping one on
  // the canvas creates a real flow node (agent/tool) at the drop point, or
  // attaches a channel to the workflow output. New feature on top of ARCH-6.
  const DRAG_MIME = 'application/studio-node'

  // Starter script for a freshly-dropped Custom Python block. The runtime
  // contract (Phase 1): upstream node outputs arrive as `inputs` (a dict); the
  // value you return/print becomes this node's output.
  const PYTHON_STARTER = `# Custom Python step.
# Upstream node outputs arrive as 'inputs' (a dict).
# Return (or print) a value — it becomes this node's output.
def run(inputs):
    # TODO: your logic here
    return inputs
`

  function emptyWorkflow() {
    return {
      name: 'Untitled workflow',
      trigger: { type: 'manual' },
      channels: [],
      flow: { nodes: [], edges: [], entry: '' },
    }
  }

  function onCanvasDragOver(e) {
    if (!e.dataTransfer) return
    if (Array.from(e.dataTransfer.types || []).includes(DRAG_MIME)) {
      e.preventDefault()
      e.dataTransfer.dropEffect = 'copy'
    }
  }

  function onCanvasDrop(e) {
    if (!e.dataTransfer) return
    const raw = e.dataTransfer.getData(DRAG_MIME)
    if (!raw) return
    e.preventDefault()
    let drag
    try { drag = JSON.parse(raw) } catch (_) { return }
    addFromPalette(drag, dropFlowPosition(e))
  }

  // Invert the current viewport transform (read off the xyflow viewport element)
  // to turn the drop's screen coords into flow coords. Identity fallback when no
  // canvas is mounted yet (first drop onto the empty state).
  function dropFlowPosition(e) {
    const pane = document.querySelector('#studio-app .svelte-flow')
    if (!pane) return { x: 0, y: 0 }
    const rect = pane.getBoundingClientRect()
    let tx = 0, ty = 0, zoom = 1
    const vp = pane.querySelector('.svelte-flow__viewport')
    if (vp) {
      try {
        const m = new DOMMatrixReadOnly(getComputedStyle(vp).transform)
        tx = m.m41; ty = m.m42; zoom = m.a || 1
      } catch (_) { /* identity fallback */ }
    }
    return {
      x: (e.clientX - rect.left - tx) / zoom,
      y: (e.clientY - rect.top - ty) / zoom,
    }
  }

  function uniqueNodeId(prefix) {
    const used = new Set(((workflow && workflow.flow && workflow.flow.nodes) || []).map((n) => n.id))
    let i = 1, id
    do { id = prefix + '_' + i++ } while (used.has(id))
    return id
  }

  // Make a node the flow's entry/start node. The trigger always flows into the
  // entry node, so this is how the user "connects" the trigger to a given node.
  function setEntry(nodeId) {
    if (!nodeId || !workflow || !workflow.flow) return
    const flow = workflow.flow
    workflow = { ...workflow, flow: { ...flow, nodes: bakePositions(flow.nodes), entry: nodeId } }
    plan = null
    rebuildGraph()
    scheduleValidate()
  }

  // Designate a node as the flow's output: its result becomes what's delivered
  // to the output channels (the runtime returns this node's result). Dragging a
  // node onto the output box, or the Inspector button, both call this.
  function setOutput(nodeId) {
    if (!nodeId || !workflow || !workflow.flow) return
    const flow = workflow.flow
    workflow = { ...workflow, flow: { ...flow, nodes: bakePositions(flow.nodes), output: nodeId } }
    plan = null
    rebuildGraph()
    scheduleValidate()
  }

  // Snapshot current on-screen node positions into x/y on the raw nodes.
  // graph.js auto-lays-out only when EVERY node lacks x/y, so baking existing
  // positions before appending a positioned node keeps the compiled layout from
  // collapsing onto (0,0).
  function bakePositions(rawNodes) {
    const live = get(nodes)
    const pos = new Map(live.map((n) => [n.id, n.position]))
    return (rawNodes || []).map((n) => {
      const pt = pos.get(n.id)
      return pt ? { ...n, x: Math.round(pt.x), y: Math.round(pt.y) } : n
    })
  }

  function addFromPalette(drag, at) {
    if (!drag || !drag.kind) return

    // Channels attach to the workflow output rather than becoming a flow node.
    if (drag.kind === 'channel') {
      const base = workflow || emptyWorkflow()
      const chans = Array.isArray(base.channels) ? base.channels.slice() : []
      const id = drag.id || drag.name
      if (id && !chans.includes(id)) chans.push(id)
      workflow = { ...base, channels: chans }
      rebuildGraph()
      scheduleValidate()
      return
    }

    // Agent / tool / python / skill -> a new flow node at the drop point.
    // A skill becomes a `read_skill` tool node pre-pointed at the skill name
    // (read_skill takes a skill_name argument), so it's runnable immediately.
    const isSkill = drag.kind === 'skill'
    const id = uniqueNodeId(isSkill ? 'skill' : drag.kind)
    const node = {
      id,
      kind: isSkill ? 'tool' : drag.kind,
      ...(drag.kind === 'tool' ? { tool: drag.name } : {}),
      ...(isSkill ? { tool: 'read_skill', description: 'Read skill: ' + drag.name } : {}),
      ...(drag.kind === 'agent' ? { agent: drag.name } : {}),
      ...(drag.kind === 'python' ? { code: PYTHON_STARTER } : {}),
      input: isSkill ? JSON.stringify({ skill_name: drag.name }) : '',
      output: '',
      inputs: [],
      outputs: [],
      params: {},
      x: Math.round(at.x),
      y: Math.round(at.y),
    }

    if (!workflow) {
      workflow = { ...emptyWorkflow(), flow: { nodes: [node], edges: [], entry: id } }
    } else {
      const flow = workflow.flow || { nodes: [], edges: [], entry: '' }
      const baked = bakePositions(flow.nodes)
      workflow = { ...workflow, flow: { ...flow, nodes: [...baked, node], entry: flow.entry || id } }
    }
    selectedNode = node     // select the new node for immediate editing
    selectedEdge = null
    rebuildGraph()
    scheduleValidate()
  }

  // ── Delete a node / edge from the canvas ──────────────────────────────────
  function deleteNode(nodeId) {
    if (!nodeId || !workflow || !workflow.flow) return
    const flow = workflow.flow
    // Bake positions first so the survivors keep their places, then drop the
    // node and every edge that touched it.
    const remaining = bakePositions(flow.nodes).filter((n) => n.id !== nodeId)
    const remEdges = (flow.edges || []).filter((e) => e.from !== nodeId && e.to !== nodeId)
    let entry = flow.entry
    if (entry === nodeId) entry = remaining.length ? remaining[0].id : ''
    workflow = { ...workflow, flow: { ...flow, nodes: remaining, edges: remEdges, entry } }
    if (selectedNode && selectedNode.id === nodeId) selectedNode = null
    selectedEdge = null
    rebuildGraph()
    scheduleValidate()
  }

  function deleteEdge(index) {
    if (index == null || !workflow || !workflow.flow || !Array.isArray(workflow.flow.edges)) return
    const edges = workflow.flow.edges.filter((_, i) => i !== index)
    workflow = { ...workflow, flow: { ...workflow.flow, edges } }
    selectedEdge = null
    rebuildGraph()
    scheduleValidate()
  }

  // Merge a patch into one node of the draft (e.g. the Custom Python editor
  // writing inline code back). Bakes positions so the graph doesn't reflow, and
  // refreshes the selection reference so the Inspector keeps editing the live
  // node after rebuildGraph.
  function applyNodePatch(nodeId, patch) {
    if (!nodeId || !patch || !workflow || !workflow.flow) return
    const flow = workflow.flow
    let updated = null
    const nodes = bakePositions(flow.nodes).map((n) => {
      if (n.id !== nodeId) return n
      updated = { ...n, ...patch }
      return updated
    })
    if (!updated) return
    workflow = { ...workflow, flow: { ...flow, nodes } }
    if (selectedNode && selectedNode.id === nodeId) selectedNode = updated
    rebuildGraph()
    scheduleValidate()
  }

  // Delete/Backspace removes the selected node (or edge) — but never while the
  // user is typing in a field (intent box, inspector inputs, refine textarea).
  function onCanvasKeydown(e) {
    if (e.key !== 'Delete' && e.key !== 'Backspace') return
    const el = document.activeElement
    if (el && (/^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName) || el.isContentEditable)) return
    if (selectedNode) { e.preventDefault(); deleteNode(selectedNode.id) }
    else if (selectedEdge) { e.preventDefault(); deleteEdge(selectedEdge.index) }
  }

  onMount(() => {
    window.addEventListener('keydown', onCanvasKeydown)
    return () => window.removeEventListener('keydown', onCanvasKeydown)
  })

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

  // Generate button entry point. Mandatory pre-generation step: instead of
  // compiling the raw intent straight away, first run a refine pass and open
  // the confirmation dialog. The actual compile happens in runCompile, called
  // once the user confirms the refined spec.
  async function generate() {
    const text = intent.trim()
    if (!text || compiling || refining || cloudGate) return
    // Local-first cloud-escalation gate: if the builder is a cloud model and the
    // user hasn't acknowledged it this session, ask before sending the prompt
    // off-box. They can continue with cloud or switch to a local model.
    if (modelAdvice && modelAdvice.cloud_escalation && !cloudAck) {
      cloudGate = { provider: modelAdvice.provider, model: modelAdvice.model }
      return
    }
    // Regenerating from an edited prompt replaces the whole canvas (including any
    // manual rewiring). Confirm when there's existing work to lose — do this up
    // front, before spending a refine round-trip.
    if (workflow && workflow.flow && (workflow.flow.nodes || []).length) {
      let ok = true
      try { ok = window.confirm('Regenerate from this prompt? It replaces the current workflow on the canvas.') } catch (_) { ok = true }
      if (!ok) return
    }
    refining = true
    compileError = ''
    try {
      // If this prompt has already been refined once (fresh generate this
      // session, or a re-opened workflow that was saved as refined), run a fast
      // LIGHT touch-up that respects the user's edits instead of a full rewrite.
      const light = !!(workflow && workflow.refined)
      const data = await bridge.refinePrompt(text, compactCatalog(catalog), light)
      refineAnswers = {}
      refinement = {
        original: (data && data.original) || text,
        refined_intent: (data && data.refined_intent) || text,
        summary: (data && data.summary) || '',
        assumptions: (data && Array.isArray(data.assumptions)) ? data.assumptions : [],
        questions: (data && Array.isArray(data.questions)) ? data.questions : [],
      }
    } catch (e) {
      // Refine should never block generation; if it fails, fall back to
      // compiling the original intent directly.
      compileError = (e && e.message) ? `Could not refine prompt (${e.message}); generating from your original text.` : ''
      await runCompile(text, undefined)
    } finally {
      refining = false
    }
  }

  // Cloud-escalation gate: user chose to continue with the cloud builder.
  function approveCloud() {
    cloudAck = true
    cloudGate = null
    generate()
  }
  // User chose to keep things local — open the model picker to switch.
  function declineCloud() {
    cloudGate = null
    openModelPicker()
  }

  // Confirm the refinement dialog. The framework's recommended architecture
  // decides what we generate: a fixed workflow (canvas) OR a ReAct/Plan-Execute
  // AGENT (no canvas — it reasons over tools). This is the wizard branch point.
  async function confirmRefinement() {
    if (!refinement || compiling) return
    const text = (refinement.refined_intent || '').trim() || refinement.original
    const ans = Object.keys(refineAnswers).length ? { ...refineAnswers } : undefined
    const mode = (refinement.recommended_mode || 'workflow')
    refinement = null
    if (mode === 'react' || mode === 'plan_execute') {
      await runAgentCompile(text, mode, ans)
    } else {
      await runCompile(text, ans)
    }
  }

  // Generate a ReAct/Plan-Execute agent (no flow). The result draft carries
  // `strategy`, so the UI renders the agent-spec panel instead of the canvas,
  // and Save persists a strategy-based agent.
  async function runAgentCompile(text, mode, ans) {
    if (!text || compiling) return
    compiling = true
    compileError = ''
    try {
      const data = await bridge.compileAgent(text, mode, ans, compactCatalog(catalog))
      applyCompile(data)
      // Mark the prompt as refined so a later edit + Generate uses the fast
      // LIGHT touch-up pass; persists via the workflow's `refined` field.
      if (workflow) workflow = { ...workflow, intent: text, refined: true }
      intent = text
    } catch (e) {
      compileError = e.message || 'agent generation failed'
    } finally {
      compiling = false
    }
  }

  function cancelRefinement() {
    if (compiling) return
    refinement = null
  }

  // runCompile performs the actual compile + canvas rebuild from a finalized
  // (refined) intent. Shared by the confirm path and the refine-failure
  // fallback.
  async function runCompile(text, ans) {
    if (!text || compiling) return
    compiling = true
    compileError = ''
    try {
      const data = await bridge.compile(text, ans, compactCatalog(catalog))
      applyCompile(data)
      // Remember the prompt on the draft so it persists through save/load and
      // the box stays populated for further edits. Mark it refined so a later
      // edit + Generate uses the fast LIGHT touch-up pass.
      if (workflow) workflow = { ...workflow, intent: text, refined: true }
      // Surface the refined intent in the editor so further edits start from the
      // clarified version, not the original rough text.
      intent = text
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
      const data = await bridge.compile(intent.trim(), answers, compactCatalog(catalog))
      applyCompile(data)
    } catch (e) {
      compileError = e.message || 'compile failed'
    } finally {
      compiling = false
    }
  }

  // Friendly label for the compiler's recommended execution mode.
  function recoLabel(mode) {
    if (mode === 'react') return 'ReAct (reasoning loop)'
    if (mode === 'plan_execute') return 'Plan-Execute'
    if (mode === 'workflow') return 'Workflow (fixed flow)'
    return mode || 'Workflow'
  }

  function applyCompile(data) {
    workflow = (data && data.workflow) || null
    questions = (data && Array.isArray(data.questions)) ? data.questions : []
    notes = (data && Array.isArray(data.notes)) ? data.notes : []
    explanation = (data && data.explanation) || null
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
    autosaveDraft()           // persist the just-generated work so it's recoverable from Drafts
  }

  // ── M6: set the current draft directly (templates / draft-load / import) ───
  // Shared by every path that swaps in a complete workflow WITHOUT a compile
  // round-trip. Mirrors the post-compile reset so the canvas, inspector,
  // plan/tier and validation all refresh consistently.
  function setWorkflow(wf, { name } = {}) {
    workflow = wf || null
    if (workflow && name && !workflow.name) workflow = { ...workflow, name }
    // Restore the generating prompt into the intent box so the user can see and
    // edit the original instruction, then Generate to re-create the workflow.
    if (workflow && typeof workflow.intent === 'string') intent = workflow.intent
    questions = []
    notes = []
    explanation = null
    suggestions = []
    discoverState = {}
    selectedNode = null
    selectedEdge = null
    plan = null
    validation = null
    saveMsg = ''
    saveError = ''
    refineState = { loading: false, error: '', message: '' }
    rebuildGraph()
    scheduleValidate()
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

  // ── Visual edge wiring ────────────────────────────────────────────────────
  // The framing START/SINK chips (__trigger__/__output__) are synthetic — they
  // aren't real flow nodes. The OUTPUT sink can't be wired by hand, but the
  // TRIGGER can: dragging from it to a node sets that node as the entry, so the
  // trigger behaves like a normal, re-pointable connector.
  const TRIGGER_ID = '__trigger__'
  const OUTPUT_ID = '__output__'
  const isFramingId = (id) => typeof id === 'string' && id.startsWith('__')

  function realNodeIds() {
    return new Set(((workflow && workflow.flow && workflow.flow.nodes) || []).map((n) => n.id))
  }

  // Predicate xyflow calls DURING a drag to allow/reject a connection (gives the
  // user live valid/invalid feedback). Reject self-loops, framing nodes, unknown
  // nodes, and exact duplicates so invalid edges can't be drawn.
  function isValidConnection(conn) {
    const c = (conn && conn.connection) || conn || {}
    const { source, target, sourceHandle, targetHandle } = c
    if (!source || !target) return false
    if (source === target) return false
    // Trigger → any real node is allowed (re-points the entry).
    if (source === TRIGGER_ID) return realNodeIds().has(target)
    // Any real node → output is allowed (designates the flow's output node).
    if (target === OUTPUT_ID) return realNodeIds().has(source)
    if (isFramingId(source) || isFramingId(target)) return false
    const ids = realNodeIds()
    if (!ids.has(source) || !ids.has(target)) return false
    return !edgeExists(source, target, sourceHandle, targetHandle)
  }

  function edgeExists(from, to, fromPort, toPort) {
    const edges = (workflow && workflow.flow && workflow.flow.edges) || []
    return edges.some((e) =>
      e.from === from && e.to === to &&
      (e.fromPort || '') === (fromPort || '') &&
      (e.toPort || '') === (toPort || ''))
  }

  // A connection was drawn on the canvas — append it as a real flow edge.
  function onConnect(event) {
    const c = (event && event.detail && (event.detail.connection || event.detail)) || null
    if (!c) return
    // Dragging from the trigger re-points the entry rather than adding an edge.
    if (c.source === TRIGGER_ID) { setEntry(c.target); return }
    // Dragging a node onto the output box designates it as the flow's output.
    if (c.target === OUTPUT_ID) { setOutput(c.source); return }
    addEdge({ from: c.source, to: c.target, fromPort: c.sourceHandle, toPort: c.targetHandle })
  }

  // Append a new flow edge (shared by the canvas connect handler and the
  // Inspector "Add connection" button). Validates, dedupes, bakes positions.
  function addEdge(spec) {
    if (!workflow || !workflow.flow || !spec) return
    const { from, to } = spec
    const fromPort = spec.fromPort || ''
    const toPort = spec.toPort || ''
    if (!from || !to || from === to) return
    if (isFramingId(from) || isFramingId(to)) return
    const ids = realNodeIds()
    if (!ids.has(from) || !ids.has(to)) return
    if (edgeExists(from, to, fromPort, toPort)) return
    const flow = workflow.flow
    const edges = Array.isArray(flow.edges) ? flow.edges : []
    const newEdge = { from, to, if: '' }
    if (fromPort) newEdge.fromPort = fromPort
    if (toPort) newEdge.toPort = toPort
    const nextEdges = [...edges, newEdge]
    workflow = { ...workflow, flow: { ...flow, nodes: bakePositions(flow.nodes), edges: nextEdges } }
    selectedNode = null
    selectedEdge = { index: nextEdges.length - 1, edge: nextEdges[nextEdges.length - 1] }
    plan = null
    rebuildGraph()
    scheduleValidate()
  }

  // Snapshot live canvas node positions into the draft before plan/save so
  // drag-repositioning persists with the saved workflow.
  function commitGraphPositions() {
    if (!workflow || !workflow.flow) return
    workflow = { ...workflow, flow: { ...workflow.flow, nodes: bakePositions(workflow.flow.nodes) } }
  }

  // Edge click -> select it for `if`-predicate editing. The xyflow edge carries
  // our data.index (ordinal in flow.edges) so we can read/write the right slot.
  function onEdgeClick(event) {
    const e = event?.detail?.edge
    const idx = e && e.data && Number.isInteger(e.data.index) ? e.data.index : -1
    if (idx < 0 || !workflow || !workflow.flow || !Array.isArray(workflow.flow.edges)) {
      selectedEdge = null
      // Framing edges (trigger→entry, node→output) are derived, not stored, so
      // they can't be deleted — explain how to change them instead of silently
      // ignoring the click.
      const id = e && typeof e.id === 'string' ? e.id : ''
      if (id.startsWith('e-trigger-')) {
        toast('The trigger always feeds the start node — it can’t be deleted. Drag the trigger to another block, or use “Make this the start node”, to re-point it.')
      } else if (id.includes('-output-')) {
        toast('Output links come from your Output Channels, not a stored edge. Click empty canvas, then uncheck Output Channels in the Inspector to remove them.')
      }
      return
    }
    selectedNode = null
    selectedEdge = { index: idx, edge: workflow.flow.edges[idx] }
  }

  // ── Test bench (M5) ─────────────────────────────────────────────────────
  // A real test bench: sample input + mode (dry/live), per-node output MOCKS,
  // ASSERTIONS, a per-node trace with mocked badges, assertion pass/fail, an
  // overall banner, and an IN-MEMORY run history with replay. The iframe is
  // sandboxed without same-origin (no localStorage), so EVERY bit of this state
  // lives in component memory only and is lost on reload — surfaced to the user.
  let testing = false
  let testError = ''
  let testResult = null       // { trace, result, assertions, passed, mode, warnings }
  let sampleInput = 'hello'
  let testMode = 'dry'        // 'dry' (default) | 'live' (rendered DISABLED)

  // Per-node mock editor state, keyed by node id: { text } (raw JSON the user
  // typed). Parsed lazily at run time; parse errors surface per-node via
  // mockErrors and the invalid mock is NOT sent.
  let mockText = {}           // { [nodeId]: string }
  let mockErrors = {}         // { [nodeId]: string }
  let showMocks = false       // collapsed by default to keep the panel tidy

  // ── Workspace layout: collapse/resize the test panels + inspector ─────────
  // Lets the user reclaim space for the canvas (hide the bottom test/self-heal
  // section, hide the inspector) or widen the inspector to read code clearly.
  // Persisted across sessions so the chosen layout sticks.
  let showTests     = true
  let showInspector = true
  let inspectorWidth = 280     // px; clamped on resize
  try {
    if (typeof localStorage !== 'undefined') {
      const p = JSON.parse(localStorage.getItem('studio.layout') || '{}')
      if (typeof p.showTests === 'boolean') showTests = p.showTests
      if (typeof p.showInspector === 'boolean') showInspector = p.showInspector
      if (typeof p.inspectorWidth === 'number') inspectorWidth = p.inspectorWidth
    }
  } catch (_) { /* ignore malformed/blocked storage */ }
  function persistLayout() {
    try {
      if (typeof localStorage !== 'undefined') {
        localStorage.setItem('studio.layout', JSON.stringify({ showTests, showInspector, inspectorWidth }))
      }
    } catch (_) { /* ignore */ }
  }
  function toggleTests() { showTests = !showTests; persistLayout() }
  function toggleInspector() { showInspector = !showInspector; persistLayout() }

  // ── Canvas ⇄ Code (SOUL.yaml) view ────────────────────────────────────────
  // Code view is authoritative: switching to it serializes the current draft to
  // SOUL.yaml; switching back parses the (possibly edited) YAML into a draft and
  // warns about anything the canvas can't show; Save in code view writes the
  // YAML straight to disk.
  let viewMode = 'canvas'     // 'canvas' | 'code'
  let codeYaml = ''
  let codeOrig = ''           // last-synced text (dirty detection)
  let codeWarnings = []
  let codeError = ''
  let codeLoading = false
  let codeValidation = null    // { ok, errors, warnings, items[] }
  let codeValidating = false

  async function showCodeView() {
    if (viewMode === 'code' || !workflow) return
    codeError = ''
    codeLoading = true
    viewMode = 'code'
    try {
      const r = await bridge.toYaml(workflow)
      codeYaml = (r && r.yaml) || ''
      codeOrig = codeYaml
    } catch (e) {
      codeError = (e && e.message) || 'Could not generate YAML'
    }
    codeLoading = false
  }

  // Editing the YAML invalidates any prior validation report.
  $: { codeYaml; codeValidation = null }

  async function showCanvasView() {
    if (viewMode === 'canvas') return
    codeError = ''
    // Parse the authoritative YAML back into a draft for the canvas.
    if (codeYaml.trim()) {
      try {
        const r = await bridge.fromYaml(codeYaml)
        codeWarnings = (r && Array.isArray(r.warnings)) ? r.warnings : []
        if (r && r.workflow) setWorkflow(r.workflow)
      } catch (e) {
        codeError = (e && e.message) || 'YAML could not be parsed'
        return // stay in code view so the user can fix it
      }
    }
    codeOrig = codeYaml
    viewMode = 'canvas'
  }

  // Full validation of the edited YAML: syntax + definition + graph + runtime
  // (missing capabilities, unfilled args, template-reference bugs, …).
  async function validateCode() {
    if (codeValidating) return
    codeValidating = true
    codeError = ''
    try {
      codeValidation = await bridge.validateYaml(codeYaml)
    } catch (e) {
      codeError = (e && e.message) || 'Validation failed'
      codeValidation = null
    }
    codeValidating = false
  }

  // One-click fix: rewrite each flagged template reference to the suggested
  // scalar accessor (e.g. {{ .notebook.notebook }} -> {{ .notebook.notebook.id }})
  // across the whole YAML, then re-validate so the panel refreshes.
  async function applyTemplateFixes() {
    const fixes = (codeValidation && codeValidation.fixes) || []
    if (!fixes.length) return
    let text = codeYaml
    for (const f of fixes) {
      if (f && f.find) text = text.split(f.find).join(f.replace)
    }
    codeYaml = text
    await validateCode()
  }

  // Save directly from the authoritative YAML (bypasses the draft round-trip so
  // fields the canvas can't express are preserved).
  async function saveFromCode() {
    if (saving) return
    saving = true
    saveError = ''
    saveMsg = ''
    try {
      const res = await bridge.saveYaml(codeYaml)
      codeOrig = codeYaml
      const id = (res && res.id) || ''
      saveMsg = id ? `Saved ${id} — manage it from the Agents page.` : 'Saved'
      // Mirror the canvas Save: hand off to the Agents page to review/enable.
      if (id) { $editAgent = id; window.location.hash = '#agents' }
    } catch (e) {
      const v = e && e.body && e.body.validation
      if (v && Array.isArray(v.findings) && v.findings.length) {
        const f = v.findings.find((x) => x.severity === 'error') || v.findings[0]
        saveError = (f.field ? f.field + ': ' : '') + f.message
      } else {
        saveError = (e && e.message) || 'Save failed'
      }
    }
    saving = false
  }
  // Drag the splitter on the inspector's left edge to resize it. Width is taken
  // from the viewport's right edge (the inspector is the rightmost column).
  function startInspResize(e) {
    const onMove = (ev) => {
      inspectorWidth = Math.max(240, Math.min(760, window.innerWidth - ev.clientX))
    }
    const onUp = () => {
      persistLayout()
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
    }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
    e.preventDefault()
  }

  // Assertions editor: rows of { target, op, value }. target is a node id or
  // the literal "result"; op ∈ contains|equals|exists.
  let assertions = []         // [{ target, op, value }]

  // In-memory run history (last ~10). Each entry keeps the full request +
  // response so it can be re-viewed or replayed exactly. Session-only.
  const HISTORY_MAX = 10
  let history = []            // [{ ts, inputSummary, passed, assertionCount, request, response }]
  let activeHistoryId = null  // ts of the entry currently shown (null = latest live run)

  // Draft nodes available as mock / assertion targets. Derived from the
  // in-memory workflow so edits keep them in sync.
  $: draftNodes = (workflow && workflow.flow && Array.isArray(workflow.flow.nodes))
    ? workflow.flow.nodes
    : []
  // Targets for an assertion dropdown: every node id plus the literal "result".
  $: assertionTargets = [...draftNodes.map((n) => n.id), 'result']

  // ── Mocks ─────────────────────────────────────────────────────────────────
  function setMockText(nodeId, value) {
    mockText = { ...mockText, [nodeId]: value }
    // Re-validate this one as the user types; clear stale errors when emptied.
    const trimmed = (value || '').trim()
    if (!trimmed) {
      const { [nodeId]: _drop, ...rest } = mockErrors
      mockErrors = rest
      return
    }
    try {
      JSON.parse(trimmed)
      const { [nodeId]: _drop, ...rest } = mockErrors
      mockErrors = rest
    } catch (e) {
      mockErrors = { ...mockErrors, [nodeId]: 'Invalid JSON: ' + (e.message || 'parse error') }
    }
  }

  // Collect non-empty, VALID mocks into { <nodeId>: <parsedOutput> }. Invalid
  // JSON is skipped (and flagged) so we never send a malformed override.
  function collectMocks() {
    const out = {}
    for (const n of draftNodes) {
      const raw = (mockText[n.id] || '').trim()
      if (!raw) continue
      try {
        out[n.id] = JSON.parse(raw)
      } catch (_) { /* skipped — flagged in mockErrors */ }
    }
    return out
  }

  function hasMockErrors() {
    return Object.keys(mockErrors).length > 0
  }

  // ── Assertions ──────────────────────────────────────────────────────────
  function addAssertion() {
    const target = assertionTargets[assertionTargets.length - 1] || 'result'
    assertions = [...assertions, { target, op: 'contains', value: '' }]
  }

  function removeAssertion(i) {
    assertions = assertions.filter((_, idx) => idx !== i)
  }

  function updateAssertion(i, patch) {
    assertions = assertions.map((a, idx) => (idx === i ? { ...a, ...patch } : a))
  }

  // Build the assertion payload — drop empty rows; "exists" needs no value.
  function collectAssertions() {
    return assertions
      .filter((a) => a && a.target && a.op)
      .map((a) => ({
        target: a.target,
        op: a.op,
        value: a.op === 'exists' ? undefined : a.value,
      }))
  }

  // ── Run ───────────────────────────────────────────────────────────────────
  function inputSummary(input) {
    const s = (input == null ? '' : String(input))
    return s.length > 48 ? s.slice(0, 48) + '…' : s
  }

  function pushHistory(request, response) {
    const ts = Date.now()
    const entry = {
      ts,
      inputSummary: inputSummary(request.input),
      passed: response && response.passed === true,
      assertionCount: (request.assertions && request.assertions.length) || 0,
      request,
      response,
    }
    history = [entry, ...history].slice(0, HISTORY_MAX)
    activeHistoryId = ts
    return entry
  }

  async function runTest() {
    if (!workflow || testing) return
    testing = true
    testError = ''
    testResult = null
    if (hasMockErrors()) {
      // Don't send invalid JSON — collectMocks already skips them, but warn so
      // the user knows a mock was ignored rather than silently dropped.
      testError = 'Some mocks have invalid JSON and were skipped. Fix them or clear the field.'
    }
    // Snapshot the exact request so history/replay re-send it verbatim.
    const mocks = collectMocks()
    const asserts = collectAssertions()
    const request = {
      workflow,
      input: sampleInput,
      mode: testMode,
      ...(Object.keys(mocks).length ? { mocks } : {}),
      ...(asserts.length ? { assertions: asserts } : {}),
    }
    try {
      const res = await bridge.test(request.workflow, request.input, {
        mocks: request.mocks,
        assertions: request.assertions,
        mode: request.mode,
      })
      testResult = res || null
      pushHistory(request, testResult)
    } catch (e) {
      testError = e.message || 'test failed'
    } finally {
      testing = false
    }
  }

  // Preview what a single run will do (Story #13): a one-click dry run through
  // the test bench, framed for the user as "here's what each run does." For a
  // scheduled agent there's no inbound message, so we send an empty input. No
  // mocks/assertions — just the per-node trace, which renders in the bench below.
  let previewing = false
  async function previewRun() {
    if (!workflow || previewing || testing) return
    previewing = true
    testError = ''
    testResult = null
    try {
      const input = (workflow.trigger && /^(schedule|cron|webhook)$/i.test(workflow.trigger.type)) ? '' : sampleInput
      testResult = await bridge.test(workflow, input, { mode: 'dry' }) || null
    } catch (e) {
      testError = e.message || 'preview failed'
    } finally {
      previewing = false
    }
  }

  // "Fix with AI": feed a runtime/test error to the LLM, which rewrites the
  // draft so it won't recur, then applies + re-validates. Works for both the
  // test-bench error and a pasted runtime error.
  let troubleshooting = false
  async function troubleshoot(errText) {
    const err = (errText || testError || '').trim()
    if (!workflow || !err || troubleshooting) return
    troubleshooting = true
    try {
      const res = await bridge.troubleshoot(workflow, err)
      if (res && res.workflow) {
        setWorkflow(res.workflow, { name: workflow.name })
        toast(res.changed ? 'Applied an AI fix for that error — re-test to confirm.' : 'AI could not find an automatic fix for that error.')
        if (res.preflight && ((res.preflight.blockers || []).length || (res.preflight.warnings || []).length)) {
          preflight = res.preflight
        }
      }
    } catch (e) {
      testError = e.message || 'troubleshoot failed'
    } finally {
      troubleshooting = false
    }
  }

  // Re-send a history entry's EXACT request (workflow snapshot + input + mocks
  // + assertions + mode) and prepend the fresh result. Non-blocking on error.
  async function replayHistory(entry) {
    if (!entry || !entry.request || testing) return
    testing = true
    testError = ''
    const request = entry.request
    try {
      const res = await bridge.test(request.workflow, request.input, {
        mocks: request.mocks,
        assertions: request.assertions,
        mode: request.mode,
      })
      testResult = res || null
      pushHistory(request, testResult)
    } catch (e) {
      testError = e.message || 'replay failed'
    } finally {
      testing = false
    }
  }

  // View a stored run again without re-sending it.
  function viewHistory(entry) {
    if (!entry) return
    testResult = entry.response
    testError = ''
    activeHistoryId = entry.ts
  }

  function clearHistory() {
    history = []
    activeHistoryId = null
  }

  // ── Plan + Save (M2) ──────────────────────────────────────────────────────
  let saving = false
  let saveError = ''
  let saveMsg = ''
  let plan = null              // last plan result { tier, reasons, requiresConsent, consentItems }
  let consent = null          // { items:[{kind,name,reason}] } when the dialog is open
  // Pre-save validation report when the dialog is open: { ok, blockers[], warnings[] }.
  // Blockers must be fixed before saving; warnings can be acknowledged and saved over.
  let preflight = null

  // Save click: VALIDATE first (consolidated preflight), then PLAN, then either
  // save directly or raise the consent dialog. Every bridge op degrades
  // gracefully — a bridge/host error just surfaces as saveError. The optional
  // `skipPreflight` flag is set when the user clicked "Save anyway" past a
  // warnings-only report, so we don't re-gate in a loop.
  async function save(skipPreflight = false) {
    if (!workflow || saving || consent || (preflight && !skipPreflight)) return
    saving = true
    saveError = ''
    saveMsg = ''
    commitGraphPositions()   // keep drag-repositioned layout in the saved draft
    try {
      if (!skipPreflight) {
        let report = null
        try { report = await bridge.preflight(workflow) } catch (_) { report = null }
        // Open the dialog when there's anything to show. Blockers stop the save;
        // warnings let the user proceed with "Save anyway". A failed/empty
        // preflight (host error) is non-blocking — fall through to plan/save.
        if (report && ((report.blockers && report.blockers.length) || (report.warnings && report.warnings.length))) {
          preflight = report
          return    // wait for the user to fix blockers or acknowledge warnings
        }
      }
      const p = await bridge.plan(workflow)
      plan = p || null
      if (p && p.requiresConsent) {
        openConsent(p.consentItems)
        return        // wait for the operator's acknowledgement
      }
      await doSave(false, [])
    } catch (e) {
      saveError = e.message || 'plan failed'
    } finally {
      saving = false
    }
  }

  // From the preflight dialog: proceed to save despite warnings (only reachable
  // when there are no blockers).
  function proceedAfterPreflight() {
    if (!preflight || (preflight.blockers && preflight.blockers.length)) return
    preflight = null
    save(true)
  }

  function cancelPreflight() {
    if (saving) return
    preflight = null
  }

  // Jump from a validation blocker/warning to the offending block: close the
  // dialog and select that node so it opens in the Inspector for editing.
  function revealNode(nodeId) {
    const wf = workflow && workflow.flow && Array.isArray(workflow.flow.nodes) ? workflow.flow.nodes : []
    const node = wf.find((n) => n && n.id === nodeId)
    preflight = null
    if (node) {
      selectedNode = node
      selectedEdge = null
      // Highlight the node on the canvas (xyflow reads `selected` per node).
      try { nodes.update((arr) => arr.map((gn) => ({ ...gn, selected: gn.id === nodeId }))) } catch (_) {}
    }
  }

  // Re-run validation in place (e.g. after the user fixed something) without
  // closing the dialog.
  async function rerunPreflight() {
    if (!workflow) return
    try { preflight = await bridge.preflight(workflow) } catch (_) { /* keep current */ }
  }

  // "Fix automatically": run the deterministic data-flow repair (auto-wire empty
  // required args + reconcile dangling {{ .var }} references to the right
  // upstream output) on the current draft, apply the result to the canvas, then
  // re-check. Resolves the common wiring/var-name mismatches without regenerating.
  let fixing = false
  async function fixAutomatically() {
    if (!workflow || fixing) return
    fixing = true
    try {
      const res = await bridge.autowire(workflow)
      if (res && res.workflow) {
        setWorkflow(res.workflow, { name: workflow.name })
        toast(res.fixed ? `Auto-fixed ${res.fixed} wiring issue${res.fixed === 1 ? '' : 's'}.` : 'No auto-fixable wiring issues found.')
      }
      preflight = await bridge.preflight(workflow)
    } catch (e) {
      saveError = e.message || 'auto-fix failed'
    } finally {
      fixing = false
    }
  }

  // ── Architect: autonomous build-verify-repair ("Build until it works") ────
  // One click drives the whole loop server-side: fill capability holes with
  // generated glue code, synthesize self-tests, then repair every blocker AND
  // every runtime error — actually RUNNING the agent — until it works or the
  // budget is hit. The healed draft replaces the canvas and the transcript is
  // shown so the user sees exactly what was wrong and how each problem was fixed.
  let building = false
  let buildReport = null // { workflow, ok, verified, attempts[], summary, residual[] }
  let buildGlue = []
  let buildLog = [] // live progress lines while building: [{kind, message}]
  async function buildUntilWorks() {
    if (!workflow || building) return
    building = true
    buildReport = null
    buildGlue = []
    buildLog = []
    saveError = ''
    try {
      const res = await bridge.buildStream(workflow, intent, true, (ev) => {
        if (!ev || !ev.message) return
        // Append a live line; glue notes also feed the report's glue list.
        buildLog = [...buildLog, ev]
        if (ev.kind === 'glue') buildGlue = [...buildGlue, ev.message.replace(/^🧩\s*/, '')]
      })
      if (res && res.report) {
        buildReport = res.report
        if (res.report.workflow) setWorkflow(res.report.workflow, { name: workflow.name })
        preflight = res.preflight && !res.preflight.ok ? res.preflight : null
        toast(res.report.summary || 'Build complete.')
      }
    } catch (e) {
      saveError = e.message || 'build failed'
    } finally {
      building = false
    }
  }

  // ── Runtime self-heal: failed (incl. scheduled) runs ──────────────────────
  // The DLQ feed of runs that failed at run time. Diagnosing one loads the saved
  // agent, repairs it against the REAL error, re-verifies, and drops the healed
  // draft onto the canvas to review + Save. No copy-paste of errors anywhere.
  let showFailedRuns = false
  let failedRuns = []
  let loadingFailed = false
  let healing = '' // id currently being healed
  let healResult = null
  async function loadFailedRuns() {
    loadingFailed = true
    try {
      const r = await bridge.failedRuns()
      failedRuns = (r && r.runs) || []
    } catch (_) {
      failedRuns = []
    } finally {
      loadingFailed = false
    }
  }
  async function toggleFailedRuns() {
    showFailedRuns = !showFailedRuns
    if (showFailedRuns) await loadFailedRuns()
  }
  async function healFailedRun(id) {
    if (healing) return
    healing = id
    healResult = null
    saveError = ''
    try {
      const res = await bridge.diagnoseRun(id)
      healResult = res
      if (res && res.workflow) {
        setWorkflow(res.workflow, { name: (res.workflow && res.workflow.name) || (workflow && workflow.name) })
        toast(res.changed ? 'Healed — review the change and Save to apply it.' : 'No fix was suggested for this error.')
      }
    } catch (e) {
      saveError = e.message || 'diagnose failed'
    } finally {
      healing = ''
    }
  }

  // Persist the draft. acceptPrivilegedExposure threads the operator's
  // channel-consent; grants threads the per-node code consent (§13). Handles the
  // 409 consent fallback (error carrying requiresConsent + consentItems) by
  // opening the same dialog.
  async function doSave(acceptPrivilegedExposure, grants) {
    saveError = ''
    try {
      const res = await bridge.save(workflow, acceptPrivilegedExposure, grants)
      const id = res.agentId
      saveMsg = `Saved as disabled agent ${id} — enable it from the Agents page.`
      $editAgent = res.agentId
      window.location.hash = '#agents'
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
    const list = Array.isArray(items) ? items : []
    // Default every code item's scope to "this workflow until the code changes".
    const scopes = {}
    for (const it of list) {
      if (it.kind === 'code') scopes[it.name] = 'workflow'
    }
    consent = { items: list, scopes }
  }

  function setConsentScope(nodeId, scope) {
    if (!consent) return
    consent = { ...consent, scopes: { ...consent.scopes, [nodeId]: scope } }
  }

  // The inline code for a consent item's node, shown in the dialog so the user
  // sees exactly what they're approving.
  function codeForNode(nodeId) {
    const n = draftNodes.find((x) => x.id === nodeId)
    return (n && n.code) || ''
  }

  // Build the per-node grant array from the code consent items + chosen scopes.
  function collectGrants() {
    if (!consent) return []
    return (consent.items || [])
      .filter((it) => it.kind === 'code')
      .map((it) => ({
        nodeId: it.name,
        hash: it.hash,
        capabilities: it.capabilities || [],
        scope: (consent.scopes && consent.scopes[it.name]) || 'workflow',
      }))
  }

  async function acknowledgeConsent() {
    if (saving) return
    saving = true
    try {
      await doSave(true, collectGrants())
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

  // ── M6: Templates + empty-state picker (S6.1) ─────────────────────────────
  // No localStorage in the sandbox, so "is there a current draft?" is simply
  // `workflow != null`. On a fresh open (no draft) we show a template picker;
  // a top-bar "Templates" button reopens it any time. Choosing one loads its
  // workflow as the current draft.
  let templatePicker = { open: false, loading: false, error: '', items: [] }

  async function openTemplates() {
    templatePicker = { open: true, loading: true, error: '', items: [] }
    try {
      const data = await bridge.templates()
      const items = (data && Array.isArray(data.templates)) ? data.templates : []
      templatePicker = { open: true, loading: false, error: '', items }
    } catch (e) {
      templatePicker = { open: true, loading: false, error: e.message || 'could not load templates', items: [] }
    }
  }

  function closeTemplates() {
    templatePicker = { ...templatePicker, open: false }
  }

  function chooseTemplate(t) {
    if (!t || !t.workflow) return
    setWorkflow(t.workflow, { name: t.name })
    closeTemplates()
  }

  // ── Draft library + "My Workflows" (published agent workflows) ────────────
  let savingDraft = false
  let library = { open: false, loading: false, error: '', drafts: [], agents: [], busyId: '' }

  // Autosave: after every successful generate, the current workflow is saved to
  // a single recoverable draft slot ("⟳ Last session") so completed work can be
  // brought back from My Workflows → Drafts even after a reload or restart — not
  // just within the in-memory session. We track the slot's id so each autosave
  // overwrites the previous one instead of piling up duplicates.
  const AUTOSAVE_NAME = '⟳ Last session'
  let autosaveId = ''

  // Drafts shown in the left palette (Drafts group, "Draft" badge). Refreshed
  // on mount and whenever a draft is saved/autosaved/deleted.
  let paletteDrafts = []
  async function refreshPaletteDrafts() {
    try {
      const res = await bridge.draftsList()
      paletteDrafts = (res && Array.isArray(res.drafts)) ? res.drafts : []
    } catch (_) { /* leave as-is on error */ }
  }
  // Load a draft onto the canvas by id (palette click).
  function openDraftById(id) { if (id) loadDraft({ id }) }
  // Delete a draft from the palette, then refresh the list.
  async function deleteDraftFromPalette(id, name) {
    let ok = true
    try { ok = window.confirm(`Delete draft “${name || id}”?`) } catch (_) { ok = true }
    if (!ok) return
    try { await bridge.draftDelete(id) } catch (_) { /* best-effort */ }
    await refreshPaletteDrafts()
  }
  async function autosaveDraft() {
    if (!workflow || !(workflow.flow && (workflow.flow.nodes || []).length)) return
    try {
      const prev = autosaveId
      const res = await bridge.draftSave(AUTOSAVE_NAME, { ...workflow, intent })
      autosaveId = (res && res.id) || autosaveId
      // Remove the previous slot if its id changed (workflow content hash moved).
      if (prev && prev !== autosaveId) {
        try { await bridge.draftDelete(prev) } catch (_) { /* best-effort */ }
      }
      refreshPaletteDrafts()
    } catch (_) { /* autosave is best-effort; never block the user */ }
  }

  async function saveDraft() {
    if (!workflow || savingDraft) return
    const suggested = (workflow.name || '').trim() || 'My workflow'
    let name
    try {
      name = window.prompt('Save draft as:', suggested)
    } catch (_) {
      name = suggested        // sandbox could block prompt — fall back silently
    }
    if (name == null) return   // cancelled
    name = (name || '').trim() || suggested
    savingDraft = true
    saveError = ''
    saveMsg = ''
    try {
      const res = await bridge.draftSave(name, workflow)
      const id = (res && res.id) || ''
      // Keep the draft's name in sync so subsequent saves/export reuse it.
      if (name && workflow.name !== name) {
        workflow = { ...workflow, name }
        rebuildGraph()
      }
      toast(`Saved draft “${name}”${id ? ' (' + id + ')' : ''}.`)
      refreshPaletteDrafts()
    } catch (e) {
      saveError = e.message || 'could not save draft'
    } finally {
      savingDraft = false
    }
  }

  // Opens the unified "My Workflows" panel: published agent workflows + drafts,
  // fetched together. Each list degrades independently so one failure doesn't
  // hide the other.
  async function openLibrary() {
    library = { open: true, loading: true, error: '', drafts: [], agents: [], busyId: '' }
    const [agentsRes, draftsRes] = await Promise.allSettled([
      bridge.agentWorkflows(),
      bridge.draftsList(),
    ])
    const agents = agentsRes.status === 'fulfilled' && Array.isArray(agentsRes.value?.agents)
      ? agentsRes.value.agents : []
    const drafts = draftsRes.status === 'fulfilled' && Array.isArray(draftsRes.value?.drafts)
      ? draftsRes.value.drafts : []
    const errs = []
    if (agentsRes.status === 'rejected') errs.push('agents: ' + (agentsRes.reason?.message || 'failed'))
    if (draftsRes.status === 'rejected') errs.push('drafts: ' + (draftsRes.reason?.message || 'failed'))
    library = { open: true, loading: false, error: errs.join(' · '), drafts, agents, busyId: '' }
  }

  function closeLibrary() {
    library = { ...library, open: false }
  }

  // Start a brand-new, empty workflow (clears the canvas).
  function newWorkflow() {
    setWorkflow(null)
    closeLibrary()
  }

  // "New agent": a clean slate — clear the prompt, the canvas, and any code-view
  // state, so the user can describe and build a fresh agent from scratch.
  function startNewAgent() {
    if (workflow && workflow.flow && (workflow.flow.nodes || []).length) {
      let ok = true
      try { ok = window.confirm('Start a new agent? This clears the current prompt and canvas.') } catch (_) { ok = true }
      if (!ok) return
    }
    intent = ''
    refinement = null
    viewMode = 'canvas'
    codeYaml = ''
    codeOrig = ''
    codeValidation = null
    codeWarnings = []
    compileError = ''
    setWorkflow(null)
    closeLibrary()
  }

  // Click an agent in the left palette → load its workflow onto the canvas for
  // editing. Re-Saving (same name → same id) upserts it.
  async function openAgentOnCanvas(id) {
    if (!id) return
    try {
      const data = await bridge.loadAgentWorkflow(id)
      const wf = (data && data.workflow) || null
      if (!wf) { toast('This agent has no editable workflow.'); return }
      setWorkflow(wf, { name: wf.name })
      toast('Loaded workflow — edit and Save to update the agent.')
    } catch (e) {
      toast(e.message || 'Could not load agent workflow')
    }
  }

  // Delete an agent straight from the palette (with confirm), then refresh the
  // palette so it disappears.
  async function deleteAgentFromPalette(id, name) {
    if (!id) return
    let ok = true
    try { ok = window.confirm(`Delete agent “${name || id}”? This cannot be undone.`) } catch (_) { ok = true }
    if (!ok) return
    try {
      await bridge.deleteAgent(id)
      toast(`Deleted agent ${name || id}.`)
      await loadCatalog()
    } catch (e) {
      toast(e.message || 'Could not delete agent')
    }
  }

  // Load a saved agent's workflow back onto the canvas for editing. Re-saving
  // (with the same name → same id) upserts the agent.
  async function loadAgentForEdit(a) {
    if (!a || !a.id || library.busyId) return
    library = { ...library, busyId: a.id, error: '' }
    try {
      const data = await bridge.loadAgentWorkflow(a.id)
      const wf = (data && data.workflow) || null
      if (!wf) throw new Error('agent has no editable workflow')
      setWorkflow(wf, { name: wf.name || a.name })
      closeLibrary()
    } catch (e) {
      library = { ...library, busyId: '', error: e.message || 'could not load agent' }
    }
  }

  async function deleteAgentWorkflow(a) {
    if (!a || !a.id || library.busyId) return
    let ok = true
    try { ok = window.confirm(`Delete agent “${a.name || a.id}”? This cannot be undone.`) } catch (_) { ok = true }
    if (!ok) return
    library = { ...library, busyId: a.id, error: '' }
    try {
      await bridge.deleteAgent(a.id)
      library = { ...library, busyId: '', agents: library.agents.filter((x) => x.id !== a.id) }
    } catch (e) {
      library = { ...library, busyId: '', error: e.message || 'could not delete agent' }
    }
  }

  async function loadDraft(d) {
    if (!d || !d.id || library.busyId) return
    library = { ...library, busyId: d.id, error: '' }
    try {
      const data = await bridge.draftLoad(d.id)
      const wf = (data && data.workflow) || null
      if (!wf) throw new Error('draft has no workflow')
      setWorkflow(wf, { name: (data && data.name) || d.name })
      closeLibrary()
    } catch (e) {
      library = { ...library, busyId: '', error: e.message || 'could not load draft' }
    }
  }

  async function deleteDraft(d) {
    if (!d || !d.id || library.busyId) return
    library = { ...library, busyId: d.id, error: '' }
    try {
      await bridge.draftDelete(d.id)
      const drafts = library.drafts.filter((x) => x.id !== d.id)
      library = { ...library, busyId: '', drafts }
    } catch (e) {
      library = { ...library, busyId: '', error: e.message || 'could not delete draft' }
    }
  }

  function draftWhen(d) {
    const u = d && d.updated
    if (!u) return ''
    const t = typeof u === 'number' ? u : Date.parse(u)
    if (Number.isNaN(t)) return String(u)
    return new Date(t).toLocaleString()
  }

  // ── M6: Export / Import (S6.4) — client-side, no backend ──────────────────
  let importError = ''
  let fileInputEl                    // bound hidden <input type=file>

  function safeFileName(base) {
    const s = (base || 'workflow').trim().toLowerCase().replace(/[^a-z0-9._-]+/g, '-').replace(/^-+|-+$/g, '')
    return (s || 'workflow') + '.studio.json'
  }

  function exportDraft() {
    if (!workflow) return
    try {
      const json = JSON.stringify(workflow, null, 2)
      const blob = new Blob([json], { type: 'application/json' })
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = safeFileName(workflow.name)
      document.body.appendChild(a)
      a.click()
      a.remove()
      // Revoke after the click has a chance to start the download.
      setTimeout(() => URL.revokeObjectURL(url), 1000)
      toast('Exported draft.')
    } catch (e) {
      saveError = e.message || 'export failed'
    }
  }

  function triggerImport() {
    importError = ''
    if (fileInputEl) fileInputEl.click()
  }

  function onImportFile(event) {
    const file = event && event.target && event.target.files && event.target.files[0]
    // Reset the input so picking the same file again re-fires change.
    if (event && event.target) event.target.value = ''
    if (!file) return
    importError = ''
    const reader = new FileReader()
    reader.onerror = () => { importError = 'Could not read the file.' }
    reader.onload = () => {
      let parsed
      try {
        parsed = JSON.parse(String(reader.result || ''))
      } catch (e) {
        importError = 'Not valid JSON: ' + (e.message || 'parse error')
        return
      }
      // Accept either a bare workflow or an export envelope.
      const wf = (parsed && parsed.flow) ? parsed : (parsed && parsed.workflow) ? parsed.workflow : null
      if (!wf || typeof wf !== 'object' || !wf.flow) {
        importError = 'That file does not look like a Studio workflow.'
        return
      }
      setWorkflow(wf, { name: wf.name })
      toast('Imported draft.')
    }
    reader.readAsText(file)
  }

  // ── M6: Per-node refine (S6.3) ────────────────────────────────────────────
  // Lives in the Inspector when a node is selected. Sends { workflow, nodeId,
  // instruction } to the host's refine op and REPLACES the current workflow
  // with the returned one, then re-validates. Spinner + graceful errors.
  let refineState = { loading: false, error: '', message: '' }

  async function refineNode(nodeId, instruction) {
    const instr = (instruction || '').trim()
    if (!workflow || !nodeId || !instr || refineState.loading) return
    refineState = { loading: true, error: '', message: '' }
    try {
      const data = await bridge.refine(workflow, nodeId, instr)
      const wf = (data && data.workflow) || null
      if (!wf || typeof wf !== 'object') throw new Error('refine returned no workflow')
      // Keep the node selected (by id) across the swap if it still exists.
      const keepId = nodeId
      const prevName = workflow.name
      workflow = (wf.name || !prevName) ? wf : { ...wf, name: prevName }
      plan = null
      validation = null
      // Re-resolve the selected node from the new workflow if it survived.
      const nextNodes = (workflow.flow && Array.isArray(workflow.flow.nodes)) ? workflow.flow.nodes : []
      const stillThere = nextNodes.find((n) => n.id === keepId)
      selectedNode = stillThere || null
      selectedEdge = null
      rebuildGraph()
      scheduleValidate()
      refineState = { loading: false, error: '', message: 'Applied.' }
    } catch (e) {
      refineState = { loading: false, error: e.message || 'refine failed', message: '' }
    }
  }

  // ── M6: lightweight toast (session-only; the sandbox has no storage) ──────
  let toastMsg = ''
  let toastTimer = null
  function toast(msg) {
    toastMsg = msg
    if (toastTimer) clearTimeout(toastTimer)
    toastTimer = setTimeout(() => { toastMsg = '' }, 3500)
  }

  // Framework-written Python: deterministic scaffolds (fetched once) + the
  // framework's own model writing a node's code on demand.
  let scaffolds = []
  onMount(() => {
    bridge.scaffolds().then((d) => { scaffolds = (d && d.scaffolds) || [] }).catch(() => { scaffolds = [] })
  })

  async function generateNodeCode(nodeId) {
    const n = draftNodes.find((x) => x.id === nodeId)
    if (!n) return ''
    try {
      const r = await bridge.generateCode(nodeId, n.description || '', workflow)
      return (r && r.code) || ''
    } catch (e) {
      toast(e.message || 'code generation failed')
      return ''
    }
  }

  // ── Studio model picker (llm.studio) ─────────────────────────────────────
  // Lets the developer choose which IN-FRAMEWORK provider/model Studio uses for
  // its reasoning + code generation, without editing config.yaml by hand.
  let modelPicker = { open: false, provider: '', model: '', models: [], saving: false, error: '' }
  let studioModelLabel = 'default'

  $: providerOptions = (catalog && catalog.providers && catalog.providers.providers)
    ? Object.keys(catalog.providers.providers) : []

  function fmtModelLabel(provider, model) {
    if (!provider) return 'default'
    return model ? `${provider} / ${model}` : provider
  }

  onMount(() => {
    bridge.getConfig().then((cfg) => {
      const st = (cfg && cfg.llm && cfg.llm.studio) || {}
      studioModelLabel = fmtModelLabel(st.provider, st.model)
    }).catch(() => {})
    refreshModelAdvice()
  })

  // Fetch builder-model strength advice so we can warn before generation.
  async function refreshModelAdvice() {
    try { modelAdvice = await bridge.modelAdvice() } catch (_) { modelAdvice = null }
  }

  async function openModelPicker() {
    modelPicker = { open: true, provider: '', model: '', models: [], saving: false, error: '' }
    try {
      const cfg = await bridge.getConfig()
      const st = (cfg && cfg.llm && cfg.llm.studio) || {}
      modelPicker = { ...modelPicker, provider: st.provider || '', model: st.model || '' }
      if (st.provider) loadModelsFor(st.provider)
    } catch (e) {
      modelPicker = { ...modelPicker, error: e.message || 'could not load config' }
    }
  }

  async function loadModelsFor(provider) {
    if (!provider) { modelPicker = { ...modelPicker, models: [] }; return }
    try {
      const r = await bridge.providerModels(provider)
      const raw = (r && (r.models || r)) || []
      const models = Array.isArray(raw)
        ? raw.map((m) => (typeof m === 'string' ? m : (m.id || m.name || ''))).filter(Boolean)
        : []
      modelPicker = { ...modelPicker, models }
    } catch (_) {
      modelPicker = { ...modelPicker, models: [] }
    }
  }

  function pickProvider(p) {
    modelPicker = { ...modelPicker, provider: p, model: '' }
    loadModelsFor(p)
  }

  async function saveStudioModel() {
    modelPicker = { ...modelPicker, saving: true, error: '' }
    try {
      await bridge.setStudioModel(modelPicker.provider, modelPicker.model)
      studioModelLabel = fmtModelLabel(modelPicker.provider, modelPicker.model)
      modelPicker = { ...modelPicker, open: false, saving: false }
      toast('Studio model updated.')
      refreshModelAdvice()
    } catch (e) {
      modelPicker = { ...modelPicker, saving: false, error: e.message || 'could not save' }
    }
  }

  onMount(loadCatalog)
  onMount(refreshPaletteDrafts)

  // ── Persist the working session across screen switches ─────────────────────
  // App.svelte mounts/destroys page components on navigation, so without this
  // the intent, the generated/refined workflow, and the transparency panels are
  // lost when you leave Studio and come back. We snapshot into a module-level
  // store on unmount and restore on mount.
  function hydrateSession() {
    const s = get(studioSession)
    if (!s) return
    if (s.workflow) setWorkflow(s.workflow)   // rebuilds the canvas (also sets intent from workflow.intent)
    if (typeof s.intent === 'string') intent = s.intent
    if (Array.isArray(s.notes)) notes = s.notes
    if (Array.isArray(s.questions)) questions = s.questions
    if (Array.isArray(s.suggestions)) suggestions = s.suggestions
    if (s.explanation) explanation = s.explanation
    if (s.refinement) { refinement = s.refinement; refineAnswers = s.refineAnswers || {} }
    if (typeof s.autosaveId === 'string') autosaveId = s.autosaveId
  }
  onMount(hydrateSession)

  onDestroy(() => {
    studioSession.set({
      intent,
      workflow,
      notes,
      questions,
      suggestions,
      explanation,
      refinement,
      refineAnswers,
      autosaveId,
    })
  })
</script>

<div id="studio-app">
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
      <button class="intent-expand" type="button" title="Open the full prompt editor" on:click={() => (promptViewer = true)} aria-label="Open prompt editor">⤢ Editor</button>
    </div>
    <button class="btn primary" on:click={generate} disabled={compiling || refining || !!refinement || !intent.trim()}>
      {refining ? 'Refining…' : compiling ? 'Generating…' : 'Generate'}
    </button>

    <!-- M6: draft management toolbar -->
    <div class="toolbar" role="group" aria-label="Draft management">
      <button class="btn primary-ghost" type="button" on:click={startNewAgent} title="Start a new agent from scratch">+ New agent</button>
      <button class="btn" type="button" on:click={openModelPicker} title="Choose which in-framework provider/model Studio uses">⚙ {studioModelLabel}</button>
      <button class="btn" type="button" on:click={openTemplates} title="Start from a template">Templates</button>
      <button class="btn" type="button" on:click={openLibrary} title="Reopen a saved draft or an existing workflow agent">My Workflows</button>
      <button class="btn" type="button" on:click={saveDraft} disabled={!workflow || savingDraft} title="Save the current draft to the library">
        {savingDraft ? 'Saving…' : 'Save draft'}
      </button>
      <button class="btn" type="button" on:click={exportDraft} disabled={!workflow} title="Download the current draft as a .studio.json file">Export</button>
      <button class="btn" type="button" on:click={triggerImport} title="Load a .studio.json file from disk">Import</button>
      <input
        bind:this={fileInputEl}
        class="hidden-file"
        type="file"
        accept=".json,application/json"
        on:change={onImportFile}
        aria-hidden="true"
        tabindex="-1"
      />
    </div>

  </header>

  <!-- M6: import error + toast strips -->
  {#if importError}
    <div class="strip strip-error">⚠ {importError}</div>
  {/if}
  {#if toastMsg}
    <div class="strip strip-ok toast-strip">✓ {toastMsg}</div>
  {/if}

  <main class="body">
    <Palette
      {catalog}
      status={paletteStatus}
      statusKind={paletteStatusKind}
      error={paletteError}
      onBrowse={browseRegistry}
      {browse}
      onInstall={installBrowse}
      onOpenAgent={openAgentOnCanvas}
      onDeleteAgent={deleteAgentFromPalette}
      drafts={paletteDrafts}
      onOpenDraft={openDraftById}
      onDeleteDraft={deleteDraftFromPalette}
    />

    <!-- Center: canvas + transparency strips + panels -->
    <section class="center">
      {#if workflow}
        <div class="view-switch" role="tablist" aria-label="Editor view">
          <button class="vs-btn" class:active={viewMode === 'canvas'} type="button"
                  role="tab" aria-selected={viewMode === 'canvas'} on:click={showCanvasView}>⬚ Canvas</button>
          <button class="vs-btn" class:active={viewMode === 'code'} type="button"
                  role="tab" aria-selected={viewMode === 'code'} on:click={showCodeView}>{'</> SOUL.yaml'}</button>
          {#if viewMode === 'code' && codeYaml !== codeOrig}<span class="vs-dirty" title="Unsaved YAML edits">●</span>{/if}
        </div>
      {/if}

      {#if codeWarnings.length}
        <div class="strip strip-notes" title="Things the canvas can't show — they stay in the YAML">
          <span class="strip-label">Kept in YAML</span>
          {#each codeWarnings as w}<span class="note">{w}</span>{/each}
        </div>
      {/if}
      {#if modelAdvice && (modelAdvice.severity === 'block' || modelAdvice.local_complexity_note || modelAdvice.cloud_escalation)}
        <div class="strip strip-modeladvice" class:strip-local={modelAdvice.local} title="Builder model">
          <span class="strip-label">
            {#if modelAdvice.severity === 'block'}Pick a builder model
            {:else if modelAdvice.local}🔒 Local-first
            {:else}☁ Cloud builder{/if}
          </span>
          <span>{modelAdvice.local_complexity_note || modelAdvice.message}</span>
          {#if modelAdvice.cloud_escalation}
            <button class="btn ma-btn" type="button" on:click={openModelPicker}>Switch to local</button>
          {:else if modelAdvice.severity === 'block'}
            <button class="btn ma-btn" type="button" on:click={openModelPicker}>Choose model</button>
          {:else if modelAdvice.frontier_available}
            <span class="ma-rec">Optional: use {modelAdvice.frontier_provider} for complex builds —</span>
            <button class="btn ma-btn" type="button" on:click={openModelPicker}>Use {modelAdvice.frontier_provider}</button>
          {/if}
        </div>
      {/if}

      {#if compileError}
        <div class="strip strip-error">⚠ {compileError}</div>
      {/if}

      {#if notes.length}
        <div class="strip strip-notes" title="What the compiler inferred">
          <span class="strip-label">Inferred</span>
          {#each notes as n}<span class="note">{n}</span>{/each}
        </div>
      {/if}

      {#if workflow && workflow.recommendation && workflow.recommendation.mode}
        <div class="strip strip-reco" title="Suggested execution model for this agent">
          <span class="strip-label">Recommended: {recoLabel(workflow.recommendation.mode)}</span>
          <span>{workflow.recommendation.rationale}</span>
          {#if workflow.recommendation.mode !== 'workflow'}
            <span class="reco-how">
              Studio still draws a fixed flow, and the agent runs it as-is until you switch modes.
              To use <strong>{recoLabel(workflow.recommendation.mode)}</strong>, edit the agent's SOUL.yaml:
              remove the <code>workflow:</code> block and add
              <code>reasoning:&nbsp;strategy:&nbsp;{workflow.recommendation.mode}</code>.
              The agent then drives its tools dynamically instead of following the frozen graph.
            </span>
          {/if}
        </div>
      {/if}

      {#if explanation}
        <details class="explain-panel" open>
          <summary class="explain-summary">What Studio built — in plain language</summary>
          <div class="explain-body">
            {#if explanation.purpose}<p class="explain-purpose">{explanation.purpose}</p>{/if}
            <div class="explain-meta">
              {#if explanation.trigger}<span><strong>Runs:</strong> {explanation.trigger}</span>{/if}
              {#if explanation.architecture}<span><strong>Type:</strong> {recoLabel(explanation.architecture)}{#if explanation.arch_reason} — {explanation.arch_reason}{/if}</span>{/if}
            </div>
            {#if explanation.steps && explanation.steps.length}
              <div class="explain-heading">Steps</div>
              <ol class="explain-steps">
                {#each explanation.steps as st}<li>{st}</li>{/each}
              </ol>
            {/if}
            <div class="explain-meta">
              {#if explanation.tools && explanation.tools.length}<span><strong>Tools:</strong> {explanation.tools.join(', ')}</span>{/if}
              {#if explanation.agents && explanation.agents.length}<span><strong>Agents:</strong> {explanation.agents.join(', ')}</span>{/if}
              {#if explanation.skills && explanation.skills.length}<span><strong>Skills:</strong> {explanation.skills.join(', ')}</span>{/if}
              {#if explanation.knowledge_bases && explanation.knowledge_bases.length}<span><strong>Knowledge:</strong> {explanation.knowledge_bases.join(', ')}</span>{/if}
              {#if explanation.channels && explanation.channels.length}<span><strong>Delivers to:</strong> {explanation.channels.join(', ')}</span>{/if}
            </div>
            {#if explanation.needs_config && explanation.needs_config.length}
              <div class="explain-heading">Still needs configuration</div>
              <ul class="explain-needs">
                {#each explanation.needs_config as nc}<li>{nc}</li>{/each}
              </ul>
            {/if}
            <div class="explain-actions">
              <button class="btn" type="button" on:click={previewRun} disabled={previewing || testing}>
                {previewing ? 'Previewing…' : 'Preview a run'}
              </button>
              <span class="explain-hint">Dry-runs the steps so you can see what each run does — no tools actually fire.</span>
            </div>
          </div>
        </details>
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

      {#if viewMode === 'code'}
        <div class="code-view">
          {#if codeError}<div class="strip strip-error">⚠ {codeError}</div>{/if}
          {#if codeLoading}
            <div class="canvas-state">Generating SOUL.yaml…</div>
          {:else}
            <div class="code-editor-wrap">
              <YamlView bind:value={codeYaml} />
            </div>
            {#if codeValidation}
              <div class="code-validation" class:ok={codeValidation.ok}>
                <div class="cv-summary">
                  {#if codeValidation.ok && !codeValidation.warnings}
                    <span class="cv-badge ok">✓ All checks passed</span>
                  {:else}
                    {#if codeValidation.errors}<span class="cv-badge err">{codeValidation.errors} error{codeValidation.errors === 1 ? '' : 's'}</span>{/if}
                    {#if codeValidation.warnings}<span class="cv-badge warn">{codeValidation.warnings} warning{codeValidation.warnings === 1 ? '' : 's'}</span>{/if}
                  {/if}
                  {#if codeValidation.fixes && codeValidation.fixes.length}
                    <button class="btn btn-sm cv-fixbtn" on:click={applyTemplateFixes} disabled={codeValidating}
                            title="Rewrite the flagged template references to the suggested scalar field">
                      ⚡ Auto-fix {codeValidation.fixes.length} reference{codeValidation.fixes.length === 1 ? '' : 's'}
                    </button>
                  {/if}
                  <button class="icon-btn" title="Dismiss" on:click={() => (codeValidation = null)}>✕</button>
                </div>
                {#if codeValidation.items && codeValidation.items.length}
                  <ul class="cv-list">
                    {#each codeValidation.items as it}
                      <li class="cv-item cv-{it.severity}">
                        <span class="cv-dot">{it.severity === 'error' ? '✕' : '!'}</span>
                        <div class="cv-body">
                          <div class="cv-msg">
                            <span class="cv-source">{it.source}</span>
                            {#if it.nodeId}<span class="cv-node">{it.nodeId}</span>{/if}
                            {it.message}
                          </div>
                          {#if it.fix}<div class="cv-fix">→ {it.fix}</div>{/if}
                        </div>
                      </li>
                    {/each}
                  </ul>
                {/if}
              </div>
            {/if}
          {/if}
        </div>
      {:else}
      <div
        class="canvas"
        role="application"
        aria-label="Workflow canvas — drop palette items here"
        on:dragover={onCanvasDragOver}
        on:drop={onCanvasDrop}
      >
        {#if compiling}
          <div class="canvas-state">Compiling…</div>
        {:else if !workflow}
          <div class="canvas-state empty">
            <div class="glyph" aria-hidden="true">⬚</div>
            <p>Describe what you want above, then press Generate.</p>
            <p class="empty-or">— or —</p>
            <button class="btn primary" type="button" on:click={openTemplates}>Start from a template</button>
          </div>
        {:else if workflow.strategy}
          <!-- ReAct / Plan-Execute AGENT spec (no fixed flow). Editable. -->
          <div class="agent-spec">
            <div class="agent-spec-head">
              <span class="agent-badge on">{recoLabel(workflow.strategy)} agent</span>
              <span class="agent-spec-name">{workflow.name || 'Untitled agent'}</span>
              <span class="agent-spec-note">Reasons over its tools — no fixed graph. Loops & polls as needed.</span>
            </div>

            <label class="agent-field-label" for="agent-sys">System prompt (how the agent works)</label>
            <textarea id="agent-sys" class="agent-sys" rows="9" bind:value={workflow.system_prompt}></textarea>

            <label class="agent-field-label" for="agent-tools">Tools the agent may call (one per line — exact builtin or mcp__server__tool names)</label>
            <textarea id="agent-tools" class="agent-tools" rows="5"
              value={(workflow.tools || []).join('\n')}
              on:change={(e) => { workflow = { ...workflow, tools: e.target.value.split('\n').map(s => s.trim()).filter(Boolean) } }}
            ></textarea>

            <div class="agent-spec-meta">
              {#if workflow.skills && workflow.skills.length}<span><strong>Skills:</strong> {workflow.skills.join(', ')}</span>{/if}
              {#if workflow.knowledge && workflow.knowledge.length}<span><strong>Knowledge:</strong> {workflow.knowledge.join(', ')}</span>{/if}
              {#if workflow.new_agents && workflow.new_agents.length}<span><strong>Peer agents:</strong> {workflow.new_agents.map(a => a.name || a.id).join(', ')}</span>{/if}
              {#if workflow.channels && workflow.channels.length}<span><strong>Delivers to:</strong> {workflow.channels.join(', ')}</span>{/if}
              {#if workflow.trigger && workflow.trigger.type}<span><strong>Runs:</strong> {workflow.trigger.type}{#if workflow.trigger.config && workflow.trigger.config.cron} ({workflow.trigger.config.cron}){/if}</span>{/if}
            </div>
          </div>
        {:else}
          <SvelteFlow
            {nodes}
            {edges}
            {nodeTypes}
            fitView
            {isValidConnection}
            on:connect={onConnect}
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
      {/if}

      {#if workflow}
        <!-- Action bar -->
        <div class="actions">
          {#if viewMode === 'canvas'}
          <input
            class="sample"
            type="text"
            bind:value={sampleInput}
            placeholder="sample input"
            aria-label="Sample test input"
          />
          <!-- Mode toggle: Dry run (default) + Live (disabled for unsaved draft) -->
          <div class="mode-toggle" role="group" aria-label="Run mode">
            <button
              class="mode-btn"
              class:active={testMode === 'dry'}
              on:click={() => (testMode = 'dry')}
              type="button"
            >Dry run</button>
            <button
              class="mode-btn"
              disabled
              title="Live runs aren’t available for an unsaved draft — save & enable the agent and exercise it via its channel."
              aria-label="Live runs aren’t available for an unsaved draft — save & enable the agent and exercise it via its channel."
              type="button"
            >Live</button>
          </div>
          <button class="btn" on:click={runTest} disabled={testing}>
            {testing ? 'Testing…' : 'Test'}
          </button>
          <button
            class="btn architect"
            on:click={buildUntilWorks}
            disabled={building || !workflow}
            title="Autonomously fill gaps, fix every error, and run it until it works"
          >
            {building ? 'Building…' : '🛠 Build until it works'}
          </button>
          {#if plan && plan.tier}
            <span
              class="tier-chip tier-{plan.tier}"
              title={(plan.reasons && plan.reasons.length) ? plan.reasons.join('; ') : 'capability tier'}
            >
              {tierLabel(plan.tier)}
            </span>
          {/if}
          <label class="unattended-toggle" title="Let this agent's system/network steps run automatically on scheduled runs, with no approval prompt. Only enable if you trust the steps.">
            <input
              type="checkbox"
              checked={!!(workflow && workflow.unattended)}
              on:change={(e) => { if (workflow) workflow = { ...workflow, unattended: e.target.checked } }}
            />
            Unattended
          </label>
          <button class="btn btn-sm view-toggle" type="button" on:click={toggleTests}
                  title="Show or hide the test & self-heal panels below the canvas">
            {showTests ? 'Hide tests' : 'Show tests'}
          </button>
          <button class="btn btn-sm view-toggle" type="button" on:click={toggleInspector}
                  title="Show or hide the inspector panel">
            {showInspector ? 'Hide inspector' : 'Show inspector'}
          </button>
          {/if}
          {#if viewMode === 'code'}
            <button class="btn" on:click={validateCode} disabled={codeValidating}>
              {codeValidating ? 'Validating…' : '✓ Validate'}
            </button>
          {/if}
          <button class="btn primary"
                  on:click={() => (viewMode === 'code' ? saveFromCode() : save())}
                  disabled={saving || (viewMode === 'canvas' && (!!consent || !!preflight))}>
            {saving ? 'Saving…' : (viewMode === 'code' ? 'Save YAML' : 'Save')}
          </button>
        </div>

        {#if saveMsg}<div class="strip strip-ok">✓ {saveMsg}</div>{/if}
        {#if saveError}<div class="strip strip-error">⚠ {saveError}</div>{/if}

        <!-- ── Architect live progress: streamed while the loop runs ──────── -->
        {#if building || (buildLog.length && !buildReport)}
          <div class="panel build-progress">
            <div class="build-head">
              <span class="spinner" aria-hidden="true"></span>
              <span class="build-summary">Building & verifying — fixing problems as they’re found…</span>
            </div>
            {#if buildLog.length}
              <ul class="build-live">
                {#each buildLog as ev}
                  <li class="live-line live-{ev.kind}">
                    {#if ev.attempt}<span class="live-n">#{ev.attempt}</span>{/if}
                    {ev.message}
                  </li>
                {/each}
              </ul>
            {/if}
          </div>
        {/if}

        <!-- ── Architect build report: what was wrong, and how it was fixed ── -->
        {#if buildReport}
          <div class="panel build-report" class:ok={buildReport.ok}>
            <div class="build-head">
              <span class="build-badge" class:ok={buildReport.ok}>
                {buildReport.verified ? '✓ Verified by running it' : buildReport.ok ? '✓ Validated' : '⚠ Needs attention'}
              </span>
              <span class="build-summary">{buildReport.summary}</span>
              <button class="icon-btn" title="Dismiss" on:click={() => (buildReport = null)}>✕</button>
            </div>
            {#if buildGlue && buildGlue.length}
              <ul class="build-glue">
                {#each buildGlue as g}<li>🧩 {g}</li>{/each}
              </ul>
            {/if}
            {#if buildReport.attempts && buildReport.attempts.length}
              <ol class="build-attempts">
                {#each buildReport.attempts as a}
                  <li class="attempt" class:ok={a.ok}>
                    <span class="attempt-phase">{a.phase}</span>
                    {#if a.problems && a.problems.length}
                      <ul class="attempt-problems">
                        {#each a.problems as p}<li>{p}</li>{/each}
                      </ul>
                    {/if}
                    <span class="attempt-action">{a.changed ? '→ ' : ''}{a.action}</span>
                  </li>
                {/each}
              </ol>
            {/if}
            {#if buildReport.residual && buildReport.residual.length}
              <div class="build-residual">
                <strong>Still unresolved:</strong>
                <ul>{#each buildReport.residual as r}<li>{r}</li>{/each}</ul>
              </div>
            {/if}
          </div>
        {/if}

        {#if showTests && viewMode === 'canvas'}
        <!-- ── Runtime self-heal: failed (incl. scheduled) runs ──────────── -->
        <div class="panel bench">
          <div class="bench-section">
            <button class="bench-head" type="button" on:click={toggleFailedRuns}>
              <span class="caret">{showFailedRuns ? '▾' : '▸'}</span>
              <h3 class="panel-title">Failed runs — self-heal</h3>
              <span class="bench-sub">Pick up what failed at run time (including scheduled runs) and fix it automatically.</span>
            </button>
            {#if showFailedRuns}
              {#if loadingFailed}
                <p class="muted">Loading failed runs…</p>
              {:else if !failedRuns.length}
                <p class="muted">No failed runs recorded. 🎉</p>
              {:else}
                <ul class="failed-list">
                  {#each failedRuns as fr (fr.id)}
                    <li class="failed-item">
                      <div class="failed-meta">
                        <span class="failed-agent">{fr.agentName || fr.agentId}</span>
                        <span class="failed-when">{fr.failedAt}</span>
                      </div>
                      <div class="failed-error">{fr.error}</div>
                      <button
                        class="btn small"
                        disabled={!fr.healable || !!healing}
                        title={fr.healable ? 'Diagnose and repair the saved agent' : 'The agent for this run no longer exists'}
                        on:click={() => healFailedRun(fr.id)}
                      >
                        {healing === fr.id ? 'Healing…' : '✨ Fix with AI'}
                      </button>
                    </li>
                  {/each}
                </ul>
                <button class="btn small" on:click={loadFailedRuns} disabled={loadingFailed}>Refresh</button>
              {/if}
              {#if healResult}
                <div class="strip {healResult.changed ? 'strip-ok' : 'strip-warn'}">
                  {#if healResult.changed}
                    ✓ Healed “{healResult.agentName}”. The repaired draft is loaded above — review it and Save to apply.
                    {#if healResult.report && healResult.report.verified} The fix was verified by re-running it.{/if}
                  {:else}
                    No automatic fix found for: {healResult.error}
                  {/if}
                </div>
              {/if}
            {/if}
          </div>
        </div>

        <!-- ── Test bench editors: Mocks + Assertions (M5) ──────────────── -->
        <div class="panel bench">
          <!-- Mocks (S5.3): per-node output overrides -->
          <div class="bench-section">
            <button class="bench-head" type="button" on:click={() => (showMocks = !showMocks)}>
              <span class="caret">{showMocks ? '▾' : '▸'}</span>
              <h3 class="panel-title">Mocks</h3>
              <span class="bench-sub">Override a node’s output with JSON to test downstream logic.</span>
            </button>
            {#if showMocks}
              {#if draftNodes.length}
                <ul class="mock-list">
                  {#each draftNodes as n (n.id)}
                    <li class="mock-item">
                      <div class="mock-head">
                        <span class="mock-id">{n.id}</span>
                        {#if n.kind}<span class="step-kind">{n.kind}</span>{/if}
                        {#if n.tool || n.agent}<span class="mock-sub">{n.tool || n.agent}</span>{/if}
                      </div>
                      <textarea
                        class="mock-input"
                        class:invalid={mockErrors[n.id]}
                        rows="2"
                        placeholder={'JSON output to mock for ' + n.id + ' (leave empty to run for real)'}
                        value={mockText[n.id] || ''}
                        on:input={(e) => setMockText(n.id, e.target.value)}
                        aria-label={'Mock output for ' + n.id}
                      ></textarea>
                      {#if mockErrors[n.id]}
                        <div class="mock-err">⚠ {mockErrors[n.id]}</div>
                      {/if}
                    </li>
                  {/each}
                </ul>
              {:else}
                <p class="muted">No nodes to mock yet.</p>
              {/if}
            {/if}
          </div>

          <!-- Assertions (S5.2) -->
          <div class="bench-section">
            <div class="bench-head static">
              <h3 class="panel-title">Assertions</h3>
              <span class="bench-sub">Check a node’s output or the final result after a run.</span>
              <button class="btn btn-sm" type="button" on:click={addAssertion}>+ Add</button>
            </div>
            {#if assertions.length}
              <ul class="assert-list">
                {#each assertions as a, i}
                  <li class="assert-row">
                    <select
                      class="assert-target"
                      value={a.target}
                      on:change={(e) => updateAssertion(i, { target: e.target.value })}
                      aria-label="Assertion target"
                    >
                      {#each assertionTargets as t}
                        <option value={t}>{t}</option>
                      {/each}
                    </select>
                    <select
                      class="assert-op"
                      value={a.op}
                      on:change={(e) => updateAssertion(i, { op: e.target.value })}
                      aria-label="Assertion operator"
                    >
                      <option value="contains">contains</option>
                      <option value="equals">equals</option>
                      <option value="exists">exists</option>
                    </select>
                    {#if a.op !== 'exists'}
                      <input
                        class="assert-value"
                        type="text"
                        value={a.value}
                        on:input={(e) => updateAssertion(i, { value: e.target.value })}
                        placeholder="expected value"
                        aria-label="Assertion value"
                      />
                    {:else}
                      <span class="assert-value muted">(no value)</span>
                    {/if}
                    <button class="btn btn-sm" type="button" on:click={() => removeAssertion(i)} aria-label="Remove assertion">✕</button>
                  </li>
                {/each}
              </ul>
            {:else}
              <p class="muted">No assertions — add one to verify outputs.</p>
            {/if}
          </div>
        </div>

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
          <div class="strip strip-error">
            ⚠ {testError}
            <button class="btn btn-sm" type="button" on:click={() => troubleshoot(testError)} disabled={troubleshooting || !workflow} title="Let the builder model rewrite the agent to fix this error">
              {troubleshooting ? 'Fixing…' : '✨ Fix with AI'}
            </button>
          </div>
        {/if}
        {#if testResult}
          <div class="panel test">
            <!-- Overall pass/fail banner (only when assertions ran) -->
            {#if testResult.assertions && testResult.assertions.length}
              <div class="overall {testResult.passed ? 'overall-pass' : 'overall-fail'}">
                <span class="overall-badge">{testResult.passed ? '✓ PASSED' : '✕ FAILED'}</span>
                <span class="overall-sub">
                  {testResult.assertions.filter((a) => a.pass).length}/{testResult.assertions.length} assertions passed
                </span>
                {#if testResult.mode}<span class="mode-chip">{testResult.mode}</span>{/if}
              </div>
            {/if}

            <!-- Backend warnings (e.g. a live run on an unsaved draft) -->
            {#if testResult.warnings && testResult.warnings.length}
              {#each testResult.warnings as w}
                <div class="strip strip-warn bench-warn">⚠ {typeof w === 'string' ? w : (w.message || fmt(w))}</div>
              {/each}
            {/if}

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
                        {#if step.mocked}<span class="mock-badge" title="Output was mocked, node was not run">mocked</span>{/if}
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

            <!-- Assertion results (S5.2) -->
            {#if testResult.assertions && testResult.assertions.length}
              <div class="assert-results">
                <h3 class="panel-title">Assertions</h3>
                <ul class="ar-list">
                  {#each testResult.assertions as a}
                    <li class="ar-row {a.pass ? 'ar-pass' : 'ar-fail'}">
                      <span class="ar-badge">{a.pass ? '✓' : '✕'}</span>
                      <span class="ar-expr">
                        <code>{a.target}</code> <em>{a.op}</em>
                        {#if a.op !== 'exists'}<code>{fmt(a.value)}</code>{/if}
                      </span>
                      {#if a.detail}<span class="ar-detail">{a.detail}</span>{/if}
                    </li>
                  {/each}
                </ul>
              </div>
            {/if}
          </div>
        {/if}

        <!-- ── Run history (S5.4): last ~10 runs, IN MEMORY only ──────────── -->
        {#if history.length}
          <div class="panel history">
            <div class="hist-head">
              <h3 class="panel-title">Run history</h3>
              <span class="hist-note">Session-only — cleared on reload (no storage in the sandbox).</span>
              <button class="btn btn-sm" type="button" on:click={clearHistory}>Clear</button>
            </div>
            <ul class="hist-list">
              {#each history as h (h.ts)}
                <li class="hist-item" class:active={activeHistoryId === h.ts}>
                  <button class="hist-main" type="button" on:click={() => viewHistory(h)} title="View this run again">
                    <span class="hist-badge {h.passed ? 'hist-pass' : (h.assertionCount ? 'hist-fail' : 'hist-neutral')}">
                      {h.assertionCount ? (h.passed ? 'pass' : 'fail') : 'run'}
                    </span>
                    <span class="hist-time">{new Date(h.ts).toLocaleTimeString()}</span>
                    <span class="hist-input">“{h.inputSummary}”</span>
                    {#if h.assertionCount}<span class="hist-meta">{h.assertionCount} assertion{h.assertionCount === 1 ? '' : 's'}</span>{/if}
                    {#if h.request.mocks}<span class="hist-meta">mocked</span>{/if}
                  </button>
                  <button class="btn btn-sm" type="button" on:click={() => replayHistory(h)} disabled={testing} title="Re-send this exact request">
                    Replay
                  </button>
                </li>
              {/each}
            </ul>
          </div>
        {/if}
        {/if}
      {/if}
    </section>

    {#if showInspector && viewMode === 'canvas'}
      <div
        class="insp-splitter"
        role="separator"
        aria-orientation="vertical"
        title="Drag to resize the inspector"
        on:pointerdown={startInspResize}
      ></div>
      <div class="insp-host" style="width:{inspectorWidth}px">
        <Inspector
          node={selectedNode}
          {selectedEdge}
          {workflow}
          channels={channelOptions}
          onChange={applyFraming}
          onEdgeChange={applyEdgePatch}
          onAddEdge={addEdge}
          onEdgeDelete={deleteEdge}
          onSetEntry={setEntry}
          onSetOutput={setOutput}
          onRefine={refineNode}
          {refineState}
          onDelete={deleteNode}
          onNodeChange={applyNodePatch}
          {scaffolds}
          onGenerateCode={generateNodeCode}
        />
      </div>
    {/if}
  </main>

  <!-- ── M6: Template picker (S6.1) ──────────────────────────────────────── -->
  {#if templatePicker.open}
    <div class="modal-backdrop" on:click|self={closeTemplates} role="presentation">
      <div class="modal" role="dialog" aria-modal="true" aria-labelledby="tmpl-title">
        <h2 id="tmpl-title" class="modal-title">Start from a template</h2>
        <p class="modal-body">Pick a starting point — it loads as your current draft, ready to edit, test and save.</p>
        {#if templatePicker.loading}
          <p class="muted">Loading templates…</p>
        {:else if templatePicker.error}
          <div class="strip strip-error">⚠ {templatePicker.error}</div>
        {:else if !templatePicker.items.length}
          <p class="muted">No templates available.</p>
        {:else}
          <ul class="picker-list">
            {#each templatePicker.items as t (t.id)}
              <li class="picker-item">
                <button class="picker-main" type="button" on:click={() => chooseTemplate(t)} title="Load this template">
                  <span class="picker-name">{t.name || t.id}</span>
                  {#if t.description}<span class="picker-desc">{t.description}</span>{/if}
                </button>
              </li>
            {/each}
          </ul>
        {/if}
        <div class="modal-actions">
          <button class="btn" type="button" on:click={closeTemplates}>Close</button>
        </div>
      </div>
    </div>
  {/if}

  <!-- ── My Workflows: published agent workflows + drafts ──────────────────── -->
  {#if library.open}
    <div class="modal-backdrop" on:click|self={closeLibrary} role="presentation">
      <div class="modal" role="dialog" aria-modal="true" aria-labelledby="lib-title">
        <div class="lib-head">
          <h2 id="lib-title" class="modal-title">My Workflows</h2>
          <button class="btn primary btn-sm" type="button" on:click={newWorkflow}>+ New workflow</button>
        </div>
        {#if library.error}<div class="strip strip-error">⚠ {library.error}</div>{/if}
        {#if library.loading}
          <p class="muted">Loading…</p>
        {:else}
          <!-- Published agent workflows -->
          <h3 class="lib-section">Saved agents</h3>
          {#if !library.agents.length}
            <p class="muted">No saved agent workflows yet. Build one and click Save.</p>
          {:else}
            <ul class="picker-list">
              {#each library.agents as a (a.id)}
                <li class="picker-item lib-item">
                  <button class="picker-main" type="button" on:click={() => loadAgentForEdit(a)} disabled={!!library.busyId} title="Edit this workflow">
                    <span class="picker-name">
                      {a.name || a.id}
                      <span class="agent-badge {a.enabled ? 'on' : 'off'}">{a.enabled ? 'enabled' : 'disabled'}</span>
                    </span>
                    <span class="picker-desc">{a.description || (a.trigger + ' · ' + a.nodes + ' step' + (a.nodes === 1 ? '' : 's'))}</span>
                  </button>
                  <div class="lib-actions">
                    <button class="btn btn-sm" type="button" on:click={() => loadAgentForEdit(a)} disabled={!!library.busyId}>
                      {library.busyId === a.id ? '…' : 'Edit'}
                    </button>
                    <button class="btn btn-sm" type="button" on:click={() => deleteAgentWorkflow(a)} disabled={!!library.busyId} title="Delete this agent">
                      Delete
                    </button>
                  </div>
                </li>
              {/each}
            </ul>
          {/if}

          <!-- Work-in-progress drafts -->
          <h3 class="lib-section">Drafts</h3>
          {#if !library.drafts.length}
            <p class="muted">No saved drafts. Use “Save draft” to keep a work in progress.</p>
          {:else}
            <ul class="picker-list">
              {#each library.drafts as d (d.id)}
                <li class="picker-item lib-item">
                  <button class="picker-main" type="button" on:click={() => loadDraft(d)} disabled={!!library.busyId} title="Load this draft">
                    <span class="picker-name">{d.name || d.id}</span>
                    {#if draftWhen(d)}<span class="picker-desc">updated {draftWhen(d)}</span>{/if}
                  </button>
                  <div class="lib-actions">
                    <button class="btn btn-sm" type="button" on:click={() => loadDraft(d)} disabled={!!library.busyId}>
                      {library.busyId === d.id ? '…' : 'Load'}
                    </button>
                    <button class="btn btn-sm" type="button" on:click={() => deleteDraft(d)} disabled={!!library.busyId} title="Delete this draft">
                      Delete
                    </button>
                  </div>
                </li>
              {/each}
            </ul>
          {/if}
        {/if}
        <div class="modal-actions">
          <button class="btn" type="button" on:click={closeLibrary}>Close</button>
        </div>
      </div>
    </div>
  {/if}

  <!-- Studio model picker: choose the in-framework provider/model for Studio. -->
  {#if modelPicker.open}
    <div class="modal-backdrop" on:click|self={() => modelPicker = { ...modelPicker, open: false }} role="presentation">
      <div class="modal" role="dialog" aria-modal="true" aria-labelledby="model-title">
        <h2 id="model-title" class="modal-title">Studio model</h2>
        <p class="modal-body">
          Which provider/model should Studio use for its reasoning and code
          generation? Uses your configured providers — leave provider blank to
          use the global default.
        </p>
        {#if modelPicker.error}<div class="strip strip-error">⚠ {modelPicker.error}</div>{/if}
        <label class="field-label" for="mp-provider">provider</label>
        <select id="mp-provider" value={modelPicker.provider} on:change={(e) => pickProvider(e.target.value)}>
          <option value="">(default provider)</option>
          {#each providerOptions as p}<option value={p}>{p}</option>{/each}
        </select>
        <label class="field-label" for="mp-model">model</label>
        <input id="mp-model" list="mp-models" placeholder="leave blank for the provider's default model"
          bind:value={modelPicker.model} />
        <datalist id="mp-models">
          {#each modelPicker.models as m}<option value={m}></option>{/each}
        </datalist>
        <div class="modal-actions">
          <button class="btn" type="button" on:click={() => modelPicker = { ...modelPicker, open: false }} disabled={modelPicker.saving}>Cancel</button>
          <button class="btn primary" type="button" on:click={saveStudioModel} disabled={modelPicker.saving}>
            {modelPicker.saving ? 'Saving…' : 'Save'}
          </button>
        </div>
      </div>
    </div>
  {/if}

  <!-- Consent dialog (M2): shown before saving a privileged, channel-bound
       workflow, or on the server's 409 consent fallback. -->
  <!-- Full prompt viewer/editor — the top bar truncates long prompts. -->
  {#if promptViewer}
    <div class="modal-backdrop" on:click|self={() => (promptViewer = false)} role="presentation">
      <div class="modal prompt-modal" role="dialog" aria-modal="true" aria-labelledby="prompt-title">
        <h2 id="prompt-title" class="modal-title">Prompt editor</h2>
        <p class="modal-body">
          {#if workflow && workflow.refined}
            This is the refined prompt your agent was built from. Edit it here, then re-generate.
          {:else}
            The full instruction Studio refines and builds from. Write or paste a longer prompt here comfortably.
          {/if}
        </p>
        <textarea class="prompt-editor-area" bind:value={intent} placeholder="Describe what you want your agent to do…"></textarea>
        {#if workflow && workflow.intent && workflow.intent !== intent}
          <div class="refine-section">
            <div class="refine-heading">Prompt this draft was generated from</div>
            <div class="prompt-readonly">{workflow.intent}</div>
          </div>
        {/if}
        <div class="modal-actions">
          <button class="btn" on:click={() => (promptViewer = false)}>Close</button>
          <button class="btn primary" on:click={() => { promptViewer = false; generate() }} disabled={!intent.trim() || compiling || refining}>
            {refining ? 'Refining…' : compiling ? 'Generating…' : 'Generate from this'}
          </button>
        </div>
      </div>
    </div>
  {/if}

  <!-- Cloud-escalation gate: ask before a prompt leaves the machine. -->
  {#if cloudGate}
    <div class="modal-backdrop" on:click|self={() => (cloudGate = null)} role="presentation">
      <div class="modal" role="dialog" aria-modal="true" aria-labelledby="cloud-title">
        <h2 id="cloud-title" class="modal-title">Use a cloud model for this build?</h2>
        <p class="modal-body">
          Your Studio builder is <strong>{cloudGate.provider} / {cloudGate.model}</strong>, a cloud
          model. To generate this agent, your prompt and your Soulacy setup context
          (skills, tools, channels, etc.) will be sent to <strong>{cloudGate.provider}</strong>.
          Soulacy is local-first — using the cloud is always your choice.
        </p>
        <div class="modal-actions">
          <button class="btn" on:click={declineCloud}>Keep it local (choose a local model)</button>
          <button class="btn primary" on:click={approveCloud}>Continue with {cloudGate.provider}</button>
        </div>
      </div>
    </div>
  {/if}

  <!-- Pre-save validation: blockers must be fixed; warnings can be saved over. -->
  {#if preflight}
    <div class="modal-backdrop" on:click|self={cancelPreflight} role="presentation">
      <div class="modal refine-modal" role="dialog" aria-modal="true" aria-labelledby="preflight-title">
        <h2 id="preflight-title" class="modal-title">
          {#if preflight.blockers && preflight.blockers.length}Fix these before saving{:else}Ready to save — a few warnings{/if}
        </h2>
        <p class="modal-body">
          Studio checked this agent against your live setup (tools, MCP servers,
          channels, schedule, and required inputs).
        </p>

        {#if preflight.blockers && preflight.blockers.length}
          <div class="refine-section">
            <div class="refine-heading">Blockers ({preflight.blockers.length})</div>
            <ul class="pf-list">
              {#each preflight.blockers as b}
                <li class="pf-item pf-block" class:pf-clickable={!!b.nodeId}
                    role={b.nodeId ? 'button' : undefined} tabindex={b.nodeId ? 0 : undefined}
                    on:click={() => b.nodeId && revealNode(b.nodeId)}
                    on:keydown={(e) => b.nodeId && (e.key === 'Enter' || e.key === ' ') && (e.preventDefault(), revealNode(b.nodeId))}
                    title={b.nodeId ? 'Click to open this block on the canvas' : ''}>
                  <div class="pf-msg">{b.message}</div>
                  {#if b.fix}<div class="pf-fix">→ {b.fix}</div>{/if}
                  {#if b.nodeId}<div class="pf-node">block: {b.nodeId} — click to open ↗</div>{/if}
                </li>
              {/each}
            </ul>
          </div>
        {/if}

        {#if preflight.warnings && preflight.warnings.length}
          <div class="refine-section">
            <div class="refine-heading">Warnings ({preflight.warnings.length})</div>
            <ul class="pf-list">
              {#each preflight.warnings as w}
                <li class="pf-item pf-warn" class:pf-clickable={!!w.nodeId}
                    role={w.nodeId ? 'button' : undefined} tabindex={w.nodeId ? 0 : undefined}
                    on:click={() => w.nodeId && revealNode(w.nodeId)}
                    on:keydown={(e) => w.nodeId && (e.key === 'Enter' || e.key === ' ') && (e.preventDefault(), revealNode(w.nodeId))}
                    title={w.nodeId ? 'Click to open this block on the canvas' : ''}>
                  <div class="pf-msg">{w.message}</div>
                  {#if w.fix}<div class="pf-fix">→ {w.fix}</div>{/if}
                  {#if w.nodeId}<div class="pf-node">block: {w.nodeId} — click to open ↗</div>{/if}
                </li>
              {/each}
            </ul>
          </div>
        {/if}

        <div class="modal-actions">
          <button class="btn" on:click={cancelPreflight} disabled={saving || fixing}>Back to editing</button>
          <button class="btn" on:click={fixAutomatically} disabled={saving || fixing} title="Auto-wire empty inputs and reconcile mismatched variable names">
            {fixing ? 'Fixing…' : 'Fix automatically'}
          </button>
          <button class="btn" on:click={rerunPreflight} disabled={saving || fixing}>Re-check</button>
          {#if !(preflight.blockers && preflight.blockers.length)}
            <button class="btn primary" on:click={proceedAfterPreflight} disabled={saving || fixing}>Save anyway</button>
          {/if}
        </div>
      </div>
    </div>
  {/if}

  <!-- Pre-generation prompt refinement: confirm what will be built before a
       workflow is generated. Shows the clarified spec (editable), the
       assumptions the analyst made, and any clarifying questions. -->
  {#if refinement}
    <div class="modal-backdrop" on:click|self={cancelRefinement} role="presentation">
      <div class="modal refine-modal" role="dialog" aria-modal="true" aria-labelledby="refine-title">
        <h2 id="refine-title" class="modal-title">Confirm what to build</h2>
        <p class="modal-body">
          Here's how your request was understood. Review it, fix anything that's
          off, then generate. A clearer spec means a better workflow with fewer errors.
        </p>

        {#if modelAdvice && modelAdvice.severity && modelAdvice.severity !== 'ok'}
          <div class="refine-modelwarn">
            ⚠ {modelAdvice.message}
            {#if modelAdvice.recommendation}<div class="refine-modelrec">{modelAdvice.recommendation}</div>{/if}
          </div>
        {/if}

        {#if refinement.summary}
          <div class="refine-summary">{refinement.summary}</div>
        {/if}

        {#if refinement.recommended_mode && refinement.recommended_mode !== 'workflow'}
          <div class="refine-mode">
            <strong>Recommended build: {recoLabel(refinement.recommended_mode)} agent</strong>
            {#if refinement.mode_reason} — {refinement.mode_reason}{/if}
            <div class="refine-mode-sub">This task reasons over its tools (looping/polling), so Studio will build a reasoning agent instead of a fixed flow canvas.</div>
          </div>
        {/if}

        <label class="refine-label" for="refine-intent">Refined prompt — edit it in the editor below before generating</label>
        <textarea
          id="refine-intent"
          class="refine-textarea refine-editor"
          rows="14"
          bind:value={refinement.refined_intent}
        ></textarea>

        {#if refinement.assumptions && refinement.assumptions.length}
          <div class="refine-section">
            <div class="refine-heading">Assumptions made (edit the spec above to change)</div>
            <ul class="refine-assumptions">
              {#each refinement.assumptions as a}
                <li>{a}</li>
              {/each}
            </ul>
          </div>
        {/if}

        {#if refinement.questions && refinement.questions.length}
          <div class="refine-section">
            <div class="refine-heading">A few questions to get this right</div>
            {#each refinement.questions as q}
              <div class="refine-q">
                <label class="refine-qtext" for={`refine-q-${q.id}`}>{q.text}</label>
                {#if q.options && q.options.length}
                  <select id={`refine-q-${q.id}`} bind:value={refineAnswers[q.id]}>
                    <option value="" disabled selected={!refineAnswers[q.id]}>Choose…</option>
                    {#each q.options as opt}
                      <option value={opt}>{opt}</option>
                    {/each}
                  </select>
                {:else}
                  <input id={`refine-q-${q.id}`} type="text" bind:value={refineAnswers[q.id]} placeholder="Your answer…" />
                {/if}
              </div>
            {/each}
          </div>
        {/if}

        <div class="modal-actions">
          <button class="btn" on:click={cancelRefinement} disabled={compiling}>Cancel</button>
          <button class="btn primary" on:click={confirmRefinement} disabled={compiling || !((refinement.refined_intent || '').trim())}>
            {compiling ? 'Generating…' : 'Generate workflow'}
          </button>
        </div>
      </div>
    </div>
  {/if}

  {#if consent}
    <div class="modal-backdrop" on:click|self={cancelConsent} role="presentation">
      <div class="modal" role="dialog" aria-modal="true" aria-labelledby="consent-title">
        <h2 id="consent-title" class="modal-title">Review &amp; consent</h2>
        <p class="modal-body">
          This workflow needs your explicit consent before it can be saved. Review
          each item below. It is saved as a <strong>DISABLED</strong> agent;
          consent is bound to the exact code shown — editing it later voids the grant.
        </p>
        {#if consent.items.length}
          <ul class="consent-items">
            {#each consent.items as it}
              {#if it.kind === 'code'}
                <li class="consent-code">
                  <div class="consent-row">
                    <span class="consent-name">🐍 {it.name}</span>
                    {#each (it.capabilities || []) as cap}
                      <span class="cap cap-{cap}">{cap}</span>
                    {/each}
                    {#if it.dynamic}<span class="cap cap-dynamic">dynamic</span>{/if}
                  </div>
                  {#if it.reason}<div class="consent-reason">{it.reason}</div>{/if}
                  <pre class="consent-codeblock">{codeForNode(it.name)}</pre>
                  <label class="consent-scope">
                    Grant scope:
                    <select
                      value={consent.scopes[it.name] || 'workflow'}
                      on:change={(e) => setConsentScope(it.name, e.target.value)}
                    >
                      <option value="run">this run only</option>
                      <option value="workflow">this workflow (until the code changes)</option>
                      <option value="until_revoked">until I revoke it</option>
                    </select>
                  </label>
                </li>
              {:else}
                <li>
                  <span class="consent-name">{it.name}</span>
                  {#if it.reason}<span class="consent-reason">{it.reason}</span>{/if}
                  <div class="consent-note">
                    Privileged channel exposure — an operator must still set
                    <code>accept_privileged_exposure</code> in config at deploy time.
                  </div>
                </li>
              {/if}
            {/each}
          </ul>
        {/if}
        <div class="modal-actions">
          <button class="btn" on:click={cancelConsent} disabled={saving}>Cancel</button>
          <button class="btn primary" on:click={acknowledgeConsent} disabled={saving}>
            {saving ? 'Saving…' : 'Consent & save'}
          </button>
        </div>
      </div>
    </div>
  {/if}
</div>

<style>
  #studio-app {
    display: flex;
    flex-direction: column;
    /* ARCH-6: fill the dashboard content area (.content is flex:1 of a 100vh
     * layout) rather than the viewport, so the embedded page sits correctly
     * below the mobile top bar and beside the sidebar. */
    height: 100%;
    min-height: 0;
    flex: 1 1 auto;
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

  /* M6: draft-management toolbar */
  .toolbar { display: flex; align-items: center; gap: 6px; flex: 0 0 auto; }
  .hidden-file {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    margin: -1px;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    border: 0;
  }
  .toast-strip { animation: fadein 0.15s ease; }
  @keyframes fadein { from { opacity: 0; } to { opacity: 1; } }

  /* M6: empty-state template affordance */
  .empty-or { font-size: 12px; color: var(--text-muted); margin: 4px 0; }

  /* M6: picker / library lists in modals */
  .lib-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 4px; }
  .lib-section { margin: 14px 0 6px; font-size: 12px; font-weight: 700; text-transform: uppercase; letter-spacing: 0.06em; color: var(--text-muted); }
  .agent-badge { margin-left: 8px; font-size: 10px; font-weight: 600; padding: 1px 6px; border-radius: 999px; border: 1px solid var(--border); vertical-align: middle; }
  .agent-badge.on { color: var(--ok); border-color: var(--ok); background: rgba(54,211,153,.12); }
  .agent-badge.off { color: var(--text-muted); }
  .picker-list { list-style: none; margin: 0 0 14px; padding: 0; display: flex; flex-direction: column; gap: 8px; }
  .picker-item {
    background: var(--bg-elev-2);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 0;
    overflow: hidden;
  }
  .picker-main {
    width: 100%;
    text-align: left;
    background: none;
    border: none;
    color: inherit;
    cursor: pointer;
    padding: 10px 12px;
    display: flex;
    flex-direction: column;
    gap: 3px;
  }
  .picker-main:hover:not(:disabled) { background: var(--bg-elev); }
  .picker-main:disabled { cursor: default; opacity: 0.6; }
  .picker-name { font-size: 13px; font-weight: 600; color: var(--text); }
  .picker-desc { font-size: 12px; color: var(--text-muted); }
  .lib-item { display: flex; align-items: center; }
  .lib-item .picker-main { flex: 1 1 auto; }
  .lib-actions { display: flex; gap: 6px; padding: 0 10px; flex: 0 0 auto; }

  /* Body layout */
  .body { display: flex; flex: 1 1 auto; min-height: 0; }

  /* Resizable inspector column: a host whose width the user controls via the
     splitter, with the Inspector component filling it (overriding its fixed
     280px). */
  .insp-host { flex: 0 0 auto; display: flex; min-width: 0; overflow: hidden; }
  .insp-host :global(.inspector) { flex: 1 1 auto; width: 100%; }
  .insp-splitter {
    flex: 0 0 6px;
    cursor: col-resize;
    background: var(--border);
    transition: background .12s;
  }
  .insp-splitter:hover { background: var(--accent); }
  .view-toggle { white-space: nowrap; }

  /* Canvas ⇄ Code view switch */
  .view-switch {
    display: flex; align-items: center; gap: 2px;
    padding: 8px 14px 0;
    flex-shrink: 0;
  }
  .vs-btn {
    font-size: 12px; font-weight: 600;
    padding: 6px 14px;
    color: var(--text-muted);
    background: var(--bg-elev);
    border: 1px solid var(--border);
    cursor: pointer;
  }
  .vs-btn:first-of-type { border-radius: 7px 0 0 7px; }
  .vs-btn:nth-of-type(2) { border-radius: 0 7px 7px 0; border-left: 0; }
  .vs-btn.active { color: #fff; background: var(--accent); border-color: var(--accent); }
  .vs-dirty { color: #ffd479; font-size: 13px; margin-left: 6px; }

  /* Code view fills the canvas area */
  .code-view {
    flex: 1 1 auto;
    min-height: 240px;
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 12px 14px;
    background: var(--bg);
    overflow: hidden;
  }
  .code-editor-wrap { flex: 1 1 auto; min-height: 360px; display: flex; }

  /* Validation results */
  .code-validation {
    flex: 0 0 auto;
    max-height: 34vh;
    overflow-y: auto;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--bg-elev);
  }
  .cv-summary {
    display: flex; align-items: center; gap: 8px;
    padding: 8px 10px; border-bottom: 1px solid var(--border);
    position: sticky; top: 0; background: var(--bg-elev);
  }
  .cv-badge { font-size: 12px; font-weight: 700; padding: 2px 8px; border-radius: 20px; }
  .cv-badge.ok   { color: #4caf82; background: rgba(76,175,130,.14); }
  .cv-badge.err  { color: #f06060; background: rgba(240,96,96,.14); }
  .cv-badge.warn { color: #e7b765; background: rgba(231,183,101,.14); }
  .cv-fixbtn { margin-left: auto; white-space: nowrap; color: var(--accent); border-color: var(--accent); }
  .cv-fixbtn:hover { background: rgba(108,140,255,.12); }
  .cv-summary .icon-btn { margin-left: 4px; }
  .cv-list { list-style: none; margin: 0; padding: 6px; display: flex; flex-direction: column; gap: 4px; }
  .cv-item { display: flex; gap: 8px; padding: 7px 8px; border-radius: 6px; background: var(--bg); }
  .cv-dot { flex: 0 0 auto; width: 18px; text-align: center; font-weight: 700; }
  .cv-error .cv-dot   { color: #f06060; }
  .cv-warning .cv-dot { color: #e7b765; }
  .cv-body { min-width: 0; }
  .cv-msg { font-size: 13px; line-height: 1.45; color: var(--text); }
  .cv-source {
    font-size: 10px; text-transform: uppercase; letter-spacing: .04em;
    color: var(--text-muted); border: 1px solid var(--border);
    border-radius: 4px; padding: 0 4px; margin-right: 6px;
  }
  .cv-node {
    font-family: ui-monospace, Menlo, monospace; font-size: 11px;
    color: var(--accent); margin-right: 6px;
  }
  .cv-fix { font-size: 12px; color: var(--text-muted); margin-top: 3px; }

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
  .strip-reco { background: rgba(124, 122, 255, 0.12); color: var(--text); }
  .strip-reco .reco-how { flex-basis: 100%; color: var(--text-muted); line-height: 1.5; }
  .strip-reco code {
    background: var(--bg-elev-2);
    border: 1px solid var(--border);
    border-radius: 4px;
    padding: 0 4px;
    font-size: 11px;
  }
  .strip-warn { background: rgba(245, 167, 66, 0.12); color: var(--warn, #f5a742); }

  /* ── Architect: build button + report + self-heal ─────────────────────── */
  .btn.architect { background: var(--accent, #7c7aff); color: #fff; border-color: transparent; font-weight: 600; }
  .btn.architect:disabled { opacity: 0.6; }
  .btn.small { padding: 3px 10px; font-size: 12px; }
  .build-report {
    margin-top: 10px; padding: 10px 12px; border-radius: 10px;
    border: 1px solid var(--border); background: var(--bg-elev);
  }
  .build-report.ok { border-color: rgba(54, 211, 153, 0.4); }
  .build-head { display: flex; align-items: center; gap: 10px; }
  .build-badge {
    font-weight: 700; font-size: 12px; padding: 2px 8px; border-radius: 999px;
    background: rgba(245, 167, 66, 0.16); color: var(--warn, #f5a742); white-space: nowrap;
  }
  .build-badge.ok { background: rgba(54, 211, 153, 0.16); color: var(--ok); }
  .build-summary { flex: 1; font-size: 13px; line-height: 1.4; }
  .build-glue { margin: 8px 0 0; padding-left: 18px; font-size: 12px; color: var(--text-muted); }
  .build-attempts { margin: 8px 0 0; padding-left: 18px; display: flex; flex-direction: column; gap: 6px; }
  .attempt { font-size: 12px; line-height: 1.4; }
  .attempt-phase {
    display: inline-block; text-transform: uppercase; font-size: 10px; letter-spacing: 0.05em;
    color: var(--text-muted); border: 1px solid var(--border); border-radius: 6px; padding: 0 5px; margin-right: 6px;
  }
  .attempt.ok .attempt-phase { color: var(--ok); border-color: rgba(54, 211, 153, 0.4); }
  .attempt-problems { margin: 4px 0; padding-left: 16px; color: var(--text-muted); }
  .attempt-action { color: var(--text); }
  .build-residual { margin-top: 8px; font-size: 12px; color: var(--error); }
  .build-residual ul { margin: 4px 0 0; padding-left: 18px; }
  .failed-list { list-style: none; margin: 8px 0; padding: 0; display: flex; flex-direction: column; gap: 8px; }
  .failed-item {
    border: 1px solid var(--border); border-radius: 8px; padding: 8px 10px;
    display: flex; flex-direction: column; gap: 4px; background: var(--bg);
  }
  .failed-meta { display: flex; justify-content: space-between; gap: 8px; font-size: 12px; }
  .failed-agent { font-weight: 600; }
  .failed-when { color: var(--text-muted); }
  .failed-error { font-size: 12px; color: var(--error); word-break: break-word; }
  .failed-item .btn { align-self: flex-start; }
  .build-progress {
    margin-top: 10px; padding: 10px 12px; border-radius: 10px;
    border: 1px solid var(--accent, #7c7aff); background: var(--bg-elev);
  }
  .spinner {
    width: 14px; height: 14px; border-radius: 50%; flex: 0 0 auto;
    border: 2px solid var(--border); border-top-color: var(--accent, #7c7aff);
    animation: studio-spin 0.7s linear infinite;
  }
  @keyframes studio-spin { to { transform: rotate(360deg); } }
  .build-live { margin: 8px 0 0; padding-left: 4px; list-style: none; display: flex; flex-direction: column; gap: 4px; max-height: 220px; overflow-y: auto; }
  .live-line { font-size: 12px; line-height: 1.4; color: var(--text-muted); }
  .live-line.live-result { color: var(--text); font-weight: 600; }
  .live-line.live-verify { color: var(--accent, #7c7aff); }
  .live-line.live-glue { color: var(--ok); }
  .live-n {
    display: inline-block; font-size: 10px; color: var(--text-muted);
    border: 1px solid var(--border); border-radius: 5px; padding: 0 4px; margin-right: 6px;
  }
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

  /* ── Mode toggle (dry / live) ─────────────────────────────────────────── */
  .mode-toggle {
    flex: 0 0 auto;
    display: inline-flex;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    overflow: hidden;
  }
  .mode-btn {
    padding: 8px 12px;
    background: var(--bg-elev-2);
    border: none;
    color: var(--text-muted);
    font-size: 12px;
    font-weight: 600;
    cursor: pointer;
  }
  .mode-btn + .mode-btn { border-left: 1px solid var(--border); }
  .mode-btn.active { background: var(--accent); color: #fff; }
  .mode-btn:disabled { opacity: 0.45; cursor: not-allowed; }

  /* ── Test bench editors (mocks + assertions) ──────────────────────────── */
  .bench { display: flex; flex-direction: column; gap: 14px; }
  .bench-section { display: flex; flex-direction: column; gap: 8px; }
  .bench-head {
    display: flex;
    align-items: baseline;
    gap: 8px;
    background: none;
    border: none;
    padding: 0;
    color: inherit;
    cursor: pointer;
    text-align: left;
    width: 100%;
  }
  .bench-head.static { cursor: default; }
  .bench-head .panel-title { margin: 0; }
  .caret { color: var(--accent); font-size: 11px; }
  .bench-sub { font-size: 11px; color: var(--text-muted); flex: 1 1 auto; }

  .mock-list, .assert-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 8px; }
  .mock-item {
    background: var(--bg-elev-2);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 8px;
  }
  .mock-head { display: flex; align-items: center; gap: 8px; margin-bottom: 6px; }
  .mock-id { font-size: 12px; font-weight: 600; font-family: ui-monospace, monospace; color: var(--text); }
  .mock-sub { font-size: 11px; color: var(--text-muted); }
  .mock-input {
    width: 100%;
    box-sizing: border-box;
    padding: 6px 8px;
    background: var(--bg-elev);
    border: 1px solid var(--border);
    border-radius: 6px;
    color: var(--text);
    font-family: ui-monospace, monospace;
    font-size: 12px;
    resize: vertical;
    outline: none;
  }
  .mock-input:focus { border-color: var(--accent); }
  .mock-input.invalid { border-color: var(--error, #ff6b81); }
  .mock-err { margin-top: 4px; font-size: 11px; color: var(--error, #ff6b81); }

  .assert-row {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
  }
  .assert-target, .assert-op, .assert-value {
    padding: 6px 8px;
    background: var(--bg-elev-2);
    border: 1px solid var(--border);
    border-radius: 6px;
    color: var(--text);
    font-size: 12px;
    outline: none;
  }
  .assert-target { flex: 1 1 120px; min-width: 100px; }
  .assert-op { flex: 0 0 auto; }
  .assert-value { flex: 2 1 140px; min-width: 100px; }
  .assert-value.muted { border-style: dashed; align-self: center; }
  .assert-target:focus, .assert-op:focus, .assert-value:focus { border-color: var(--accent); }

  /* Mocked trace badge */
  .mock-badge {
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.4px;
    font-weight: 700;
    border-radius: 999px;
    padding: 0 6px;
    color: var(--warn, #f5a742);
    border: 1px solid var(--warn, #f5a742);
    background: rgba(245, 167, 66, 0.12);
  }

  /* Overall pass/fail banner */
  .overall {
    display: flex;
    align-items: center;
    gap: 10px;
    border-radius: 8px;
    padding: 8px 12px;
    margin-bottom: 12px;
  }
  .overall-pass { background: rgba(54, 211, 153, 0.12); border: 1px solid var(--ok, #36d399); }
  .overall-fail { background: rgba(255, 107, 129, 0.12); border: 1px solid var(--error, #ff6b81); }
  .overall-badge { font-weight: 700; font-size: 13px; }
  .overall-pass .overall-badge { color: var(--ok, #36d399); }
  .overall-fail .overall-badge { color: var(--error, #ff6b81); }
  .overall-sub { font-size: 12px; color: var(--text-muted); }
  .mode-chip {
    margin-left: auto;
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.4px;
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 1px 8px;
    color: var(--text-muted);
  }
  .bench-warn { border-radius: 6px; margin-bottom: 8px; border-bottom: none; }

  /* Assertion results */
  .assert-results { margin-top: 12px; }
  .ar-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
  .ar-row {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    background: var(--bg-elev-2);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 6px 8px;
  }
  .ar-row.ar-pass { border-color: var(--ok, #36d399); }
  .ar-row.ar-fail { border-color: var(--error, #ff6b81); }
  .ar-badge { font-weight: 700; font-size: 13px; }
  .ar-pass .ar-badge { color: var(--ok, #36d399); }
  .ar-fail .ar-badge { color: var(--error, #ff6b81); }
  .ar-expr { font-size: 12px; }
  .ar-expr code {
    font-family: ui-monospace, monospace;
    background: var(--bg-elev);
    border: 1px solid var(--border);
    border-radius: 4px;
    padding: 0 5px;
  }
  .ar-expr em { color: var(--text-muted); font-style: normal; }
  .ar-detail { font-size: 11px; color: var(--text-muted); flex: 1 1 100%; }

  /* ── Run history ──────────────────────────────────────────────────────── */
  .hist-head { display: flex; align-items: baseline; gap: 8px; margin-bottom: 8px; flex-wrap: wrap; }
  .hist-head .panel-title { margin: 0; }
  .hist-note { font-size: 11px; color: var(--text-muted); flex: 1 1 auto; }
  .hist-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
  .hist-item {
    display: flex;
    align-items: center;
    gap: 8px;
    background: var(--bg-elev-2);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 4px 8px;
  }
  .hist-item.active { border-color: var(--accent); }
  .hist-main {
    flex: 1 1 auto;
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    background: none;
    border: none;
    padding: 4px 0;
    color: inherit;
    cursor: pointer;
    text-align: left;
    min-width: 0;
  }
  .hist-badge {
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.4px;
    font-weight: 700;
    border-radius: 999px;
    padding: 1px 8px;
    border: 1px solid var(--border);
  }
  .hist-pass { color: var(--ok, #36d399); border-color: var(--ok, #36d399); }
  .hist-fail { color: var(--error, #ff6b81); border-color: var(--error, #ff6b81); }
  .hist-neutral { color: var(--text-muted); }
  .hist-time { font-size: 11px; color: var(--text-muted); font-family: ui-monospace, monospace; }
  .hist-input { font-size: 12px; color: var(--text); }
  .hist-meta { font-size: 10px; color: var(--text-muted); }

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
  .consent-name { display: inline-block; font-size: 13px; font-weight: 600; color: var(--text); }
  .consent-reason { display: block; margin-top: 3px; font-size: 12px; color: var(--text-muted); }
  .consent-note { margin-top: 4px; font-size: 11px; color: var(--text-muted); }
  .consent-row { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
  .cap { font-size: 11px; font-weight: 600; padding: 1px 7px; border-radius: 999px; border: 1px solid var(--border); }
  .cap-system  { background: rgba(255,107,129,.14); border-color: var(--error); color: var(--error); }
  .cap-network { background: rgba(245,197,66,.14);  border-color: var(--warn);  color: var(--warn); }
  .cap-dynamic { background: rgba(245,197,66,.14);  border-color: var(--warn);  color: var(--warn); }
  .consent-codeblock {
    margin: 8px 0 6px; max-height: 180px; overflow: auto;
    background: var(--bg); border: 1px solid var(--border); border-radius: 8px;
    padding: 8px 10px; font-family: ui-monospace, Menlo, monospace; font-size: 11px;
    line-height: 1.5; white-space: pre; color: var(--text);
  }
  .consent-scope { display: flex; align-items: center; gap: 8px; font-size: 12px; color: var(--text-muted); }
  .consent-scope select { width: auto; flex: 1; }
  .modal-actions { display: flex; justify-content: flex-end; gap: 8px; }

  /* ── Pre-generation refinement dialog ───────────────────────────────────── */
  .refine-modal { max-width: 640px; width: 92vw; max-height: 84vh; overflow-y: auto; }

  /* Prompt editor modal: roomy editor for writing/editing the (refined) prompt */
  .prompt-modal { max-width: 820px; width: 92vw; max-height: 88vh; overflow-y: auto; }
  .prompt-editor-area {
    width: 100%; box-sizing: border-box; resize: vertical;
    min-height: 320px;
    font: inherit; font-size: 14px; line-height: 1.6;
    padding: 14px 16px; border-radius: 10px;
    border: 1px solid var(--border, rgba(127,127,127,0.35));
    background: var(--bg-elev, var(--surface, transparent)); color: var(--text);
  }
  .prompt-editor-area:focus { outline: none; border-color: var(--accent); }

  /* "New agent" emphasized-but-secondary button */
  .primary-ghost {
    color: var(--accent); border-color: var(--accent);
    font-weight: 600;
  }
  .primary-ghost:hover { background: rgba(108,140,255,.12); }
  .refine-summary {
    font-size: 13px; line-height: 1.5; color: var(--text);
    background: var(--surface-2, rgba(127,127,127,0.08));
    border-left: 3px solid var(--accent, #6c8cff);
    border-radius: 6px; padding: 10px 12px; margin: 0 0 14px;
  }
  .refine-label { display: block; font-size: 12px; font-weight: 600; color: var(--text-muted); margin: 0 0 6px; }
  .refine-textarea {
    width: 100%; box-sizing: border-box; resize: vertical;
    font: inherit; font-size: 13px; line-height: 1.5;
    padding: 10px 12px; border-radius: 8px;
    border: 1px solid var(--border, rgba(127,127,127,0.35));
    background: var(--surface, transparent); color: var(--text);
  }
  .refine-editor { min-height: 240px; font-size: 14px; line-height: 1.6; }
  .refine-section { margin-top: 16px; }
  .refine-heading { font-size: 12px; font-weight: 600; color: var(--text-muted); margin: 0 0 8px; }
  .refine-assumptions { margin: 0; padding-left: 18px; font-size: 13px; line-height: 1.6; color: var(--text); }
  .refine-q { margin-bottom: 12px; }
  .refine-qtext { display: block; font-size: 13px; color: var(--text); margin: 0 0 6px; }
  .refine-q input, .refine-q select {
    width: 100%; box-sizing: border-box; font: inherit; font-size: 13px;
    padding: 7px 10px; border-radius: 8px;
    border: 1px solid var(--border, rgba(127,127,127,0.35));
    background: var(--surface, transparent); color: var(--text);
  }

  /* ── Pre-save validation (preflight) list ───────────────────────────────── */
  .pf-list { margin: 0; padding: 0; list-style: none; display: flex; flex-direction: column; gap: 8px; }
  .pf-item { border-radius: 8px; padding: 9px 11px; border-left: 3px solid transparent; font-size: 13px; }
  .pf-clickable { cursor: pointer; }
  .pf-clickable:hover { filter: brightness(1.15); outline: 1px solid var(--accent, #6c8cff); }
  .pf-block { background: rgba(220,60,60,0.10); border-left-color: #d83c3c; }
  .pf-warn  { background: rgba(220,160,40,0.10); border-left-color: #e0a028; }
  .pf-msg { color: var(--text); line-height: 1.45; }
  .pf-fix { color: var(--text-muted); font-size: 12px; margin-top: 3px; }
  .pf-node { color: var(--text-muted); font-size: 11px; margin-top: 2px; opacity: 0.8; }

  /* ── Model-advice warning + refinement model banner ─────────────────────── */
  .strip-modeladvice { background: rgba(220,160,40,0.12); color: var(--text); align-items: center; gap: 10px; flex-wrap: wrap; }
  .strip-modeladvice.strip-local { background: rgba(60,160,90,0.12); }
  .strip-modeladvice .ma-rec { color: var(--text-muted); font-size: 12px; }
  .ma-btn { margin-left: auto; }
  .refine-modelwarn {
    background: rgba(220,160,40,0.12); border-left: 3px solid #e0a028;
    border-radius: 6px; padding: 9px 11px; margin: 0 0 12px;
    font-size: 13px; line-height: 1.45; color: var(--text);
  }
  .refine-modelrec { color: var(--text-muted); font-size: 12px; margin-top: 4px; }

  /* ── "What Studio built" explanation panel ──────────────────────────────── */
  .explain-panel {
    background: var(--bg-elev); border: 1px solid var(--border, rgba(127,127,127,0.25));
    border-radius: 8px; margin: 6px 0; padding: 8px 12px;
  }
  .explain-summary { cursor: pointer; font-size: 13px; font-weight: 600; color: var(--text); }
  .explain-body { margin-top: 8px; font-size: 13px; line-height: 1.5; color: var(--text); }
  .explain-purpose { margin: 0 0 8px; }
  .explain-meta { display: flex; flex-wrap: wrap; gap: 14px; margin: 6px 0; color: var(--text-muted); font-size: 12px; }
  .explain-heading { font-weight: 600; font-size: 12px; color: var(--text-muted); margin: 8px 0 4px; }
  .explain-steps { margin: 0; padding-left: 18px; }
  .explain-steps li { margin: 2px 0; }
  .explain-needs { margin: 0; padding-left: 18px; color: var(--text); }
  .explain-needs li { margin: 2px 0; color: #c98a1a; }

  /* ── ReAct / Plan-Execute agent spec panel (no canvas) ──────────────────── */
  .agent-spec { padding: 16px 18px; overflow-y: auto; height: 100%; box-sizing: border-box; }
  .agent-spec-head { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; margin-bottom: 12px; }
  .agent-spec-name { font-size: 15px; font-weight: 600; color: var(--text); }
  .agent-spec-note { font-size: 12px; color: var(--text-muted); }
  .agent-field-label { display: block; font-size: 12px; font-weight: 600; color: var(--text-muted); margin: 12px 0 4px; }
  .agent-sys, .agent-tools {
    width: 100%; box-sizing: border-box; resize: vertical; font: inherit; font-size: 13px;
    line-height: 1.5; padding: 10px 12px; border-radius: 8px;
    border: 1px solid var(--border, rgba(127,127,127,0.35));
    background: var(--surface, transparent); color: var(--text);
  }
  .agent-tools { font-family: ui-monospace, monospace; font-size: 12px; }
  .agent-spec-meta { display: flex; flex-wrap: wrap; gap: 14px; margin-top: 12px; font-size: 12px; color: var(--text-muted); }
  .intent { position: relative; display: flex; align-items: center; }
  .intent-expand {
    position: absolute; right: 6px; top: 50%; transform: translateY(-50%);
    background: none; border: none; color: var(--text-muted); cursor: pointer;
    font-size: 14px; padding: 2px 4px; line-height: 1;
  }
  .intent-expand:hover { color: var(--accent, #6c8cff); }
  .prompt-readonly { font-size: 12px; color: var(--text-muted); white-space: pre-wrap; line-height: 1.5; background: var(--bg-elev); border-radius: 6px; padding: 8px 10px; }
  .refine-mode { background: rgba(108,140,255,0.12); border-left: 3px solid var(--accent, #6c8cff); border-radius: 6px; padding: 9px 11px; margin: 0 0 12px; font-size: 13px; color: var(--text); }
  .refine-mode-sub { font-size: 12px; color: var(--text-muted); margin-top: 4px; }
  .unattended-toggle { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; color: var(--text-muted); cursor: pointer; user-select: none; }
  .unattended-toggle input { margin: 0; }
  .explain-actions { margin-top: 10px; display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
  .explain-hint { font-size: 12px; color: var(--text-muted); }

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
