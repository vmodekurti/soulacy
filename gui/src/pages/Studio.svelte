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
  import { editAgent, studioDebugRun, studioSession } from '../lib/stores.js'
  import { toFlow, kindMeta } from '../lib/studio/graph.js'
  import { validateConnection } from '../lib/studio/portcompat.js'
  import { computeRunState } from '../lib/studio/runstate.js'
  import Palette from '../lib/studio/Palette.svelte'
  import Inspector from '../lib/studio/Inspector.svelte'
  import YamlView from '../lib/studio/YamlView.svelte'
  import BuildInspector from '../lib/studio/BuildInspector.svelte'
  import Collapsible from '../lib/studio/Collapsible.svelte'
  import StudioNode from '../lib/studio/nodes/StudioNode.svelte'
  import TriggerNode from '../lib/studio/nodes/TriggerNode.svelte'
  import OutputNode from '../lib/studio/nodes/OutputNode.svelte'
  import LiveEdge from '../lib/studio/LiveEdge.svelte'
  import { pythonCodeFor, pythonLabelFor } from '../lib/studio/pythonTemplates.js'
  import { autoConnectEdge, wouldCreateCycle } from '../lib/studio/autoconnect.js'
  import { explainPythonError } from '../lib/studio/pyerror.js'
  import { stepResultsByNode } from '../lib/studio/testresults.js'
  import { migrateEndpoints } from '../lib/studio/planlanes.js'
  import PlanView from '../lib/studio/PlanView.svelte'
  import '../lib/studio/studio.css'

  // (Removed) The old iframe build scrubbed the plugin token from the URL
  // fragment on mount. Embedded in the SPA we hold no token and must NOT touch
  // the hash — the core dashboard uses it for routing (#studio).

  const nodeTypes = {
    studio: StudioNode,
    studioTrigger: TriggerNode,
    studioOutput: OutputNode,
  }
  // Custom edge with weight + a heartbeat: glowing particles flow along it while
  // a build/run is in flight (see LiveEdge + the `building`→edge.active effect).
  const edgeTypes = { live: LiveEdge }

  // ── Palette (Wave 1) ──────────────────────────────────────────────────────
  let catalog = null
  let paletteStatus = 'Loading capabilities…'
  let paletteStatusKind = ''
  let paletteError = ''
  let lastGoodCatalog = null

  // Map each connected MCP tool's full name → its published param hint
  // ("title*:string, …"), so the Inspector can show a tool node's allowed
  // parameters and let you add one with a click.
  $: toolParams = (() => {
    const m = {}
    for (const t of (catalog && catalog.mcp_tools) || []) {
      if (t && t.full_name) m[t.full_name] = t.params || ''
    }
    return m
  })()

  async function loadCatalog() {
    paletteStatus = 'Loading capabilities…'
    paletteStatusKind = ''
    try {
      const next = await bridge.catalog()
      const failedParts = Object.entries(next || {})
        .filter(([, v]) => v && v.error)
        .map(([k]) => k)

      const hasAnyCatalogData =
        (next?.agents?.agents || []).length > 0 ||
        (next?.tools?.builtins || []).length > 0 ||
        (next?.tools?.python_tools || []).length > 0 ||
        (next?.tools?.mcp_tools || []).length > 0 ||
        Object.keys(next?.providers?.providers || {}).length > 0 ||
        (next?.channels?.channels || []).length > 0 ||
        (next?.skills?.skills || []).length > 0 ||
        ((next?.mcp?.servers || next?.mcp || [])).length > 0

      if (!hasAnyCatalogData && lastGoodCatalog) {
        catalog = lastGoodCatalog
        paletteStatus = 'Using cached capabilities while Studio refreshes.'
        paletteStatusKind = 'warn'
        paletteError = ''
        return
      }

      catalog = next
      if (hasAnyCatalogData) lastGoodCatalog = next
      paletteError = ''
      if (failedParts.length > 0) {
        paletteStatus = 'Capabilities loaded with limited data: ' + failedParts.join(', ')
        paletteStatusKind = 'warn'
      } else {
        paletteStatus = 'Capabilities loaded.'
        paletteStatusKind = 'ok'
      }
    } catch (e) {
      if (lastGoodCatalog) {
        catalog = lastGoodCatalog
        paletteError = ''
        paletteStatus = 'Using cached capabilities while Studio reconnects.'
        paletteStatusKind = 'warn'
      } else {
        paletteError = 'Unavailable'
        paletteStatus = 'Could not load capabilities: ' + (e.message || 'error')
        paletteStatusKind = 'error'
      }
    }
  }

  // ── Credentials (first-class): set the API keys an agent's tools/MCP need,
  // without leaving Studio. Loaded once; `set` marks which are already provided.
  let secrets = []
  let secretVals = {}     // { [name]: typed value }
  let secretBusy = ''     // name currently being saved
  let secretMsg = ''
  $: unsetSecrets = (secrets || []).filter((s) => s && s.set === false)
  async function loadSecrets() {
    try {
      const r = await bridge.listSecrets()
      secrets = (r && Array.isArray(r.secrets)) ? r.secrets : []
    } catch (_) { secrets = [] }
  }
  async function setSecretVal(name) {
    const v = (secretVals[name] || '').trim()
    if (!v || secretBusy) return
    secretBusy = name
    secretMsg = ''
    try {
      await bridge.setSecret(name, v)
      secretVals = { ...secretVals, [name]: '' }
      secretMsg = `Saved ${name}.`
      await loadSecrets()
    } catch (e) {
      secretMsg = (e && e.message) ? `Could not save ${name}: ${e.message}` : `Could not save ${name}`
    } finally {
      secretBusy = ''
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
  let generationProfile = null // local/cloud builder guardrails used for compile
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
  let agentRoute = null        // { mode, reason } when Studio built an agent instead of a workflow
  let refining = false         // refine request in flight
  let refineAnswers = {}       // { [questionId]: value } for refinement questions
  let rawPrompt = ''           // the user's ORIGINAL prompt (intent holds the refined one)
  let modalRefining = false    // inline refine (from the prompt editor) in flight
  let promptError = ''         // error shown inside the prompt editor

  // ── SOUL.yaml authoring rulebook (auto-injected into generate + fix) ───────
  let rulesOpen = false
  let rulesText = ''
  let rulesDefault = ''
  let rulesIsDefault = true
  let rulesLoading = false
  let rulesSaving = false
  let rulesMsg = ''

  async function openRules() {
    rulesOpen = true
    rulesLoading = true
    rulesMsg = ''
    try {
      const r = await bridge.getRules()
      rulesText = (r && r.rules) || ''
      rulesDefault = (r && r.default) || ''
      rulesIsDefault = !!(r && r.isDefault)
    } catch (e) {
      rulesMsg = '✗ ' + ((e && e.message) || 'Could not load rules')
    }
    rulesLoading = false
  }
  async function saveRules() {
    if (rulesSaving) return
    rulesSaving = true
    rulesMsg = ''
    try {
      const r = await bridge.saveRules(rulesText)
      rulesIsDefault = !!(r && r.isDefault)
      rulesMsg = '✓ Saved'
    } catch (e) {
      rulesMsg = '✗ ' + ((e && e.message) || 'Save failed')
    }
    rulesSaving = false
  }
  function resetRulesToDefault() {
    if (rulesDefault) rulesText = rulesDefault
  }

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

  // Per-node execution status from the last build (id -> 'ok'|'repaired'|
  // 'problem'|'idle'), threaded into the graph so nodes carry a semantic accent.
  let nodeRunState = {}
  // When the Build Inspector's replay scrubber is active it sends a per-frame
  // status map that OVERRIDES the final-build status, so the canvas replays the
  // loop attempt-by-attempt. null = no replay (show the final build outcome).
  let replayOverride = null

  function rebuildGraph() {
    if (!workflow) {
      nodes.set([]); edges.set([]); return
    }
    const flow = toFlow(workflow, validation, nodeRunState)
    nodes.set(flow.nodes)
    edges.set(flow.edges)
  }

  // Recompute node run-state whenever a build finishes (or its preflight
  // updates), or follow the inspector's replay scrubber when active, and re-render
  // the graph so the canvas shows the success/repaired/problem accents.
  $: {
    nodeRunState = replayOverride || computeRunState({ report: buildReport, preflight })
    rebuildGraph()
  }

  // Heartbeat: while an autonomous build is in flight, light every edge so the
  // LiveEdge particles flow — the canvas visibly pulses with the run (principle:
  // "if a workflow is running, the canvas should gently pulse"). Reactive on
  // `building`; rebuildGraph resets edges to rest when the graph itself changes.
  function setEdgesActive(on) {
    edges.update((es) => es.map((e) => (e && e.data ? { ...e, data: { ...e.data, active: on } } : e)))
  }
  $: setEdgesActive(building)

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
  const LLM_EXTRACT_SYSTEM = `Extract the user's intent into a compact JSON object.
Return only JSON. Do not include markdown.
Use null for fields that are not present.`

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

    // Trigger / Exit -> structural endpoint blocks (Phase A). A trigger becomes
    // the flow entry; an exit is a terminal delivery block. Config lives in
    // node.params and is edited in the Inspector; on save the backend projects it
    // onto the workflow trigger/channels (DeriveEndpoints).
    if (drag.kind === 'trigger' || drag.kind === 'exit') {
      const eid = uniqueNodeId(drag.kind)
      const enode = {
        id: eid,
        kind: drag.kind,
        description: drag.kind === 'trigger' ? 'Trigger — starts the flow' : 'Exit — delivers the result',
        input: '', output: '', inputs: [], outputs: [],
        params: drag.kind === 'trigger'
          ? { kind: 'cron', config: { cron: '0 9 * * *' } }
          : { route: 'console', config: {} },
        x: Math.round(at.x),
        y: Math.round(at.y),
      }
      if (!workflow) {
        workflow = { ...emptyWorkflow(), flow: { nodes: [enode], edges: [], entry: drag.kind === 'trigger' ? eid : '' } }
      } else {
        const flow = workflow.flow || { nodes: [], edges: [], entry: '' }
        const baked = bakePositions(flow.nodes)
        const entry = drag.kind === 'trigger' ? eid : (flow.entry || '')
        workflow = { ...workflow, flow: { ...flow, nodes: [...baked, enode], entry } }
      }
      selectedNode = enode
      selectedEdge = null
      rebuildGraph()
      scheduleValidate()
      return
    }

    // Agent / tool / python / llm / skill -> a new flow node at the drop point.
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
      // Python: seed from a named template when the palette supplied one
      // (Guided Studio Builder), else the blank starter. The template's plain-
      // English label becomes the node description so it reads as domain work.
      ...(drag.kind === 'python'
        ? (drag.template
            ? { code: pythonCodeFor(drag.template), description: pythonLabelFor(drag.template) }
            : { code: PYTHON_STARTER })
        : {}),
      ...(drag.kind === 'llm'
        ? {
            description: 'Extract structured intent',
          }
        : {}),
      input: drag.kind === 'llm' ? '{{ .trigger.text }}' : (isSkill ? JSON.stringify({ skill_name: drag.name }) : ''),
      output: drag.kind === 'llm' ? 'extracted' : '',
      inputs: [],
      outputs: [],
      params: drag.kind === 'llm' ? { system: LLM_EXTRACT_SYSTEM, response_format: 'json' } : {},
      x: Math.round(at.x),
      y: Math.round(at.y),
    }

    if (!workflow) {
      workflow = { ...emptyWorkflow(), flow: { nodes: [node], edges: [], entry: id } }
    } else {
      const flow = workflow.flow || { nodes: [], edges: [], entry: '' }
      const baked = bakePositions(flow.nodes)
      // Smart connection (Story 7): auto-wire the new step to the most recent
      // upstream step, unless that would create a cycle. Keeps a freshly-dropped
      // step from stranding under "Needs attention".
      const priorEdges = flow.edges || []
      const auto = autoConnectEdge(baked, node, priorEdges)
      const edges = (auto && !wouldCreateCycle(priorEdges, auto.from, auto.to))
        ? [...priorEdges, auto]
        : priorEdges
      workflow = { ...workflow, flow: { ...flow, nodes: [...baked, node], edges, entry: flow.entry || id } }
    }
    selectedNode = node     // select the new node for immediate editing
    selectedEdge = null
    rebuildGraph()
    scheduleValidate()
  }

  // Phase B: compile a plain-language connector gate into a flow predicate.
  // Passed to the Inspector; returns the predicate string (or throws on error).
  async function compileGate(phrase, vars) {
    const res = await bridge.compileGate(phrase, vars)
    return (res && res.predicate) || ''
  }

  // Phase C: compile a node's plain-language intent into concrete config, then
  // merge the compiled tool/agent/python fields into the node on the canvas.
  async function compileNode(intent, node) {
    if (!node) return
    // Upstream outputs (var names) so the per-node compiler can wire by field.
    // When a recent dry-run captured real output shapes, attach them so the model
    // compiles against actual data instead of guessing the payload format.
    const shapeByName = {}
    for (const s of lastShapes) { if (s && s.name) shapeByName[s.name] = s.shape || '' }
    const upstream = ((workflow && workflow.flow && workflow.flow.nodes) || [])
      .filter((n) => n && n.id !== node.id && n.output)
      .map((n) => {
        const name = String(n.output).trim()
        return shapeByName[name] ? { name, shape: shapeByName[name] } : { name }
      })
    const res = await bridge.compileNode({ intent, nodeId: node.id, kind: node.kind || '', upstream })
    const compiled = res && res.node
    if (!compiled) return
    // Keep id/position; take the compiled kind/config/intent.
    applyNodePatch(node.id, {
      kind: compiled.kind,
      tool: compiled.tool || '',
      agent: compiled.agent || '',
      code: compiled.code || '',
      input: compiled.input || '',
      output: compiled.output || node.output || '',
      requires: compiled.requires || [],
      intent,
    })
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
    // Esc restores the split layout from any maximized frame.
    if (e.key === 'Escape' && maximizedFrame) {
      e.preventDefault()
      maximizedFrame = ''
      return
    }
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
    // Skip flow validation for reasoning agents — they have no graph, so the
    // workflow validator only produces node/edge errors that don't apply and
    // confuse the user. The agent's own validity is checked on save / in YAML.
    if (!workflow || workflow.strategy) { validation = null; return }
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
      // Capture the ORIGINAL prompt: on a full refine the text being refined IS
      // the raw prompt. Don't overwrite an original the user typed in the editor,
      // and don't clobber it on a light re-refine of already-refined text.
      if (!light && !rawPrompt.trim()) rawPrompt = text
      const data = await bridge.refinePrompt(text, compactCatalog(catalog), light)
      refineAnswers = {}
      refinement = {
        original: (data && data.original) || text,
        refined_intent: (data && data.refined_intent) || text,
        summary: (data && data.summary) || '',
        assumptions: (data && Array.isArray(data.assumptions)) ? data.assumptions : [],
        questions: (data && Array.isArray(data.questions)) ? data.questions : [],
        // The framework's architecture call ('workflow' | 'react' | 'plan_execute')
        // and why — so confirmRefinement can build the AGENT directly instead of
        // drawing a fixed flow that a reasoning task can never satisfy.
        recommended_mode: (data && data.recommended_mode) || '',
        recommended_reason: (data && (data.recommended_reason || data.recommendation_rationale)) || '',
      }
    } catch (e) {
      // Refine should never block generation; if it fails, fall back to
      // compiling the original intent directly.
      compileError = (e && e.message) ? `Could not refine prompt (${e.message}); generating from your original text.` : ''
      await runCompile(text, undefined)
      await escalateIfReasoningFit()
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
    const reason = refinement.recommended_reason || ''
    refinement = null
    if (mode === 'auto' || mode === 'react' || mode === 'plan_execute') {
      // A tool/reasoning agent (no fixed flow): build the AGENT directly, take the
      // user to SOUL.yaml, and explain via a modal. "auto" is the recommended
      // default — the engine runs it as a reliable native tool-calling loop.
      await runAgentCompile(text, mode, ans)
      if (workflow && workflow.strategy) routeToAgent(mode, reason)
    } else {
      await runCompile(text, ans)
      // Safety net: the compiler may only realise it's reasoning-fit after trying
      // to build the flow (it returns a workflow carrying a react recommendation).
      await escalateIfReasoningFit()
    }
  }

  // routeToAgent finalises the "this is an agent, not a workflow" handoff: show
  // SOUL.yaml (where the agent actually lives) and raise the explainer modal.
  function routeToAgent(mode, reason) {
    agentRoute = {
      mode,
      reason: reason || (workflow && workflow.recommendation && workflow.recommendation.rationale) || '',
    }
    showCodeView()
  }

  // escalateIfReasoningFit converts a freshly-compiled WORKFLOW into the agent it
  // should have been, when the compiler judged the task reasoning-fit. This is
  // what makes "if it can't be a workflow, don't build a workflow" true even when
  // the verdict only emerges from the compile result. Manual toggles never hit
  // this path (they call runAgentCompile/runCompile directly).
  async function escalateIfReasoningFit() {
    const rec = workflow && !workflow.strategy ? workflow.recommendation : null
    const m = rec && rec.mode
    if (m !== 'auto' && m !== 'react' && m !== 'plan_execute') return
    const text = ((intent || (workflow && workflow.intent) || rawPrompt || '')).trim()
    if (!text) return
    await runAgentCompile(text, m)
    if (workflow && workflow.strategy) routeToAgent(m, rec.rationale || '')
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
      if (workflow) workflow = { ...workflow, intent: text, refined: true, raw_intent: rawPrompt }
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

  // ── Output → delivery auto-wiring ──────────────────────────────────────────
  // Add/remove a delivery channel on the current draft. Surfaced as one-click
  // suggestions when a draft has no channel wired, so the result actually goes
  // somewhere instead of being produced and dropped.
  function addDeliveryChannel(id) {
    if (!workflow || !id) return
    const ch = Array.isArray(workflow.channels) ? workflow.channels.slice() : []
    if (!ch.includes(id)) ch.push(id)
    workflow = { ...workflow, channels: ch }
  }
  function removeDeliveryChannel(id) {
    if (!workflow) return
    const ch = (workflow.channels || []).filter((c) => c !== id)
    workflow = { ...workflow, channels: ch }
  }
  // True when the draft produces output but has nowhere to send it. A
  // channel-triggered agent replies on its trigger channel, so it's exempt.
  $: needsDelivery = !!workflow
    && (!workflow.channels || workflow.channels.length === 0)
    && !(workflow.trigger && workflow.trigger.type === 'channel')
    && channelOptions.length > 0

  // ── Try the agent: run the UNSAVED reasoning agent against one question ─────
  let tryQuestion = ''
  let tryResult = null   // { reply, error } | null
  let trying = false
  async function tryAgent() {
    if (trying || !workflow) return
    const isAgent = !!workflow.strategy
    const runnable = isAgent || (workflow.flow && (workflow.flow.nodes || []).length)
    if (!runnable) return
    // Agents take a free-form question; workflows take the sample input.
    const q = (isAgent ? tryQuestion : sampleInput).trim()
    if (!q) { compileError = 'Enter a sample input to run.'; return }
    trying = true
    tryResult = null
    try {
      const res = await bridge.tryAgent(workflow, q)
      tryResult = {
        reply: (res && res.reply) || '',
        error: (res && res.error) || '',
        trace: (res && Array.isArray(res.trace)) ? res.trace : [],
        nodeTrace: (res && Array.isArray(res.node_trace)) ? res.node_trace : [],
      }
    } catch (e) {
      tryResult = { reply: '', error: (e && e.message) || 'run failed' }
    } finally {
      trying = false
    }
    // A fresh run invalidates any prior repair proposals.
    repairProposals = []
    repairError = ''
    repairDone = ''
  }

  // Copy any value (full node input/output) to the clipboard for offline testing.
  let copiedKey = ''
  async function copyValue(text, key) {
    try {
      await navigator.clipboard.writeText(text || '')
      copiedKey = key
      setTimeout(() => { if (copiedKey === key) { copiedKey = ''; } }, 1500)
    } catch (_) { /* clipboard blocked; ignore */ }
  }
  // Soft failure: a node ran (no error) but its OUTPUT reports an error field.
  function nodeSoftError(n) {
    const raw = (n && (n.output_full || n.output)) || ''
    if (!raw) return ''
    try {
      const o = JSON.parse(raw)
      if (o && typeof o === 'object') {
        for (const k of ['error', 'errors', 'err']) {
          if (typeof o[k] === 'string' && o[k].trim()) return o[k]
        }
      }
    } catch (_) { /* not JSON */ }
    return ''
  }
  function nodeLooksWrong(n) { return !!(n && (n.error || nodeSoftError(n))) }

  // ── Observe-and-adjust: learn from the last Run Live and fix a node ─────────
  // After a run where a node broke because a real API returned an unexpected
  // shape, ask Studio to observe the actual node outputs and propose adjustments.
  let repairing = false
  let repairProposals = []   // [{ node_id, field, class, old, new, rationale, auto, observed_keys }]
  let repairError = ''
  let repairDone = ''
  // True when the last run has at least one node worth adjusting — either a hard
  // error OR a soft failure (ran green but its output reports an error).
  $: liveHasFailure = !!(tryResult && (tryResult.error || (tryResult.nodeTrace || []).some(nodeLooksWrong)))

  async function adjustToLiveOutput() {
    if (repairing || !workflow || !tryResult) return
    repairing = true
    repairError = ''
    repairDone = ''
    repairProposals = []
    try {
      const res = await bridge.repairLive(workflow, tryResult.nodeTrace || [])
      const props = (res && Array.isArray(res.proposals)) ? res.proposals : []
      if (!props.length) {
        repairDone = 'No automatic adjustment found. The failing node may be a real API/auth error rather than a format mismatch — check the node output below.'
      }
      repairProposals = props
    } catch (e) {
      repairError = (e && e.message) || 'could not analyze the run'
    } finally {
      repairing = false
    }
  }

  // Diff preview (Epic 4): compute what a repair proposal would change in the
  // SOUL.yaml WITHOUT saving, so the user can review before applying.
  let repairDiff = null   // { p, candidate, lines, stats }
  let repairDiffBusy = false
  async function previewProposal(p) {
    if (!workflow || repairDiffBusy) return
    repairDiffBusy = true
    repairError = ''
    try {
      const res = await bridge.applyRepair(workflow, p)   // returns candidate; not persisted
      const candidate = res && res.workflow
      if (!candidate) { repairError = 'Could not compute the change.'; return }
      const d = await api.studio.diff(workflow, candidate)
      repairDiff = { p, candidate, lines: (d && d.lines) || [], stats: (d && d.stats) || { added: 0, removed: 0 } }
    } catch (e) {
      repairError = (e && e.message) || 'could not preview the change'
    } finally {
      repairDiffBusy = false
    }
  }
  function applyDiff() {
    if (!repairDiff) return
    workflow = repairDiff.candidate
    rebuildGraph()
    scheduleValidate()
    repairProposals = repairProposals.filter(x => x !== repairDiff.p)
    repairDone = 'Applied. Run Live again to confirm.'
    repairDiff = null
  }
  function cancelDiff() { repairDiff = null }

  async function applyProposal(p) {
    if (!workflow || p._applying) return
    p._applying = true
    repairProposals = repairProposals
    try {
      const res = await bridge.applyRepair(workflow, p)
      if (res && res.workflow) {
        workflow = res.workflow          // patch the draft in place (triggers reactivity)
        if (res.valid === false) {
          repairError = 'Applied, but the node still needs attention: ' + ((res.errors || []).join('; '))
        } else {
          repairDone = 'Adjusted node “' + p.node_id + '”. Run Live again to confirm.'
        }
        // Drop the applied proposal from the list.
        repairProposals = repairProposals.filter(x => x !== p)
      } else {
        repairError = 'Could not apply the adjustment.'
      }
    } catch (e) {
      repairError = (e && e.message) || 'apply failed'
    } finally {
      p._applying = false
      repairProposals = repairProposals
    }
  }

  function rejectProposal(p) {
    repairProposals = repairProposals.filter(x => x !== p)
  }

  // ── Execution-mode override (Workflow ⇄ ReAct ⇄ Plan-Execute) ──────────────
  // The current draft's mode: a fixed workflow (no strategy) or a reasoning agent.
  $: currentMode = workflow && workflow.strategy ? String(workflow.strategy).toLowerCase() : 'workflow'

  // switchMode lets the developer override how Studio classified the task —
  // converting a result that's better as a reasoning agent (e.g. an autonomous
  // skill router built as a brittle fixed flow) into a ReAct/Plan-Execute agent,
  // or back to a workflow. It re-compiles the original intent in the chosen mode,
  // reusing the same agent/flow compilers Generate uses.
  async function switchMode(mode) {
    if (compiling || mode === currentMode) return
    const text = ((intent || (workflow && workflow.intent) || rawPrompt || '')).trim()
    if (!text) {
      compileError = 'Add a prompt (Generate from a description) before switching mode.'
      return
    }
    // Switching modes RE-COMPILES from the prompt and discards any manual edits to
    // the current draft (system prompt, tools, skills, or canvas wiring). Confirm
    // before throwing that work away.
    if (workflow) {
      let ok = true
      try {
        ok = window.confirm(`Switch to ${recoLabel(mode)}? This regenerates from your prompt and discards any manual edits to the current ${currentMode === 'workflow' ? 'workflow' : 'agent'}.`)
      } catch (_) { ok = true }
      if (!ok) return
    }
    if (mode === 'workflow') await runCompile(text)
    else await runAgentCompile(text, mode)
  }

  // runCompile performs the actual compile + canvas rebuild from a finalized
  // (refined) intent. Shared by the confirm path and the refine-failure
  // fallback.
  async function runCompile(text, ans) {
    if (!text || compiling) return
    compiling = true
    compileError = ''
    try {
      const data = await bridge.compile(text, ans, compactCatalog(catalog), rawPrompt)
      applyCompile(data)
      // Remember the prompt on the draft so it persists through save/load and
      // the box stays populated for further edits. Mark it refined so a later
      // edit + Generate uses the fast LIGHT touch-up pass.
      if (workflow) workflow = { ...workflow, intent: text, refined: true, raw_intent: rawPrompt }
      // Surface the refined intent in the editor so further edits start from the
      // clarified version, not the original rough text.
      intent = text
    } catch (e) {
      compileError = e.message || 'compile failed'
    } finally {
      compiling = false
    }
  }

  // Prompt editor — "Refine": take the user's ORIGINAL prompt (rawPrompt) and
  // run the refine pass, dropping the result into the refined box (intent).
  async function refineFromModal() {
    const raw = rawPrompt.trim()
    if (!raw || modalRefining) return
    modalRefining = true
    promptError = ''
    try {
      const data = await bridge.refinePrompt(raw, compactCatalog(catalog), false)
      intent = (data && data.refined_intent) || raw
      if (workflow) workflow = { ...workflow, raw_intent: raw }
    } catch (e) {
      promptError = (e && e.message) ? `Could not refine (${e.message})` : 'Could not refine the prompt'
    } finally {
      modalRefining = false
    }
  }

  // Prompt editor — "Generate": build the workflow straight from the REFINED
  // prompt (intent), skipping a re-refine. Falls back to the original if the
  // refined box is empty (e.g. the user only filled the original and hit go).
  async function generateFromModal() {
    const text = (intent.trim() || rawPrompt.trim())
    if (!text || compiling || refining) return
    if (workflow && workflow.flow && (workflow.flow.nodes || []).length) {
      let ok = true
      try { ok = window.confirm('Generate from this prompt? It replaces the current workflow on the canvas.') } catch (_) { ok = true }
      if (!ok) return
    }
    promptViewer = false
    if (workflow && workflow.strategy) await runAgentCompile(text, workflow.strategy, undefined)
    else await runCompile(text, undefined)
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
    if (mode === 'auto') return 'Auto tool agent'
    if (mode === 'react') return 'ReAct (reasoning loop)'
    if (mode === 'plan_execute') return 'Plan-Execute'
    if (mode === 'workflow') return 'Workflow (fixed flow)'
    return mode || 'Workflow'
  }

  // resetTransientDraftState clears everything tied to a SPECIFIC prior draft so
  // it can't bleed into a freshly generated/loaded one. Without this, a new draft
  // inherited the previous agent's run history, build report, a leftover preflight
  // (which silently disabled Save), or a stale agent-route modal. Called by both
  // applyCompile (post-generate) and setWorkflow (template/load/import).
  function resetTransientDraftState() {
    loadedAgentId = null
    runTrace = null
    runDiagnosis = null
    runHistory = []
    selectedRunId = null
    runTraceErr = ''
    buildReport = null
    buildLog = []
    buildGlue = []
    buildTraceId = null
    agentRoute = null
    preflight = null
    consent = null
    plan = null
    validation = null
    tryQuestion = ''
    tryResult = null
    generationProfile = null
    // Swapping in a different draft invalidates the SOUL.yaml view's serialization
    // (which is only re-generated on entry). Drop back to canvas so a stale YAML
    // can't be shown — or, worse, saved over the new draft. routeToAgent re-opens
    // SOUL.yaml afterwards when it wants the code view.
    if (viewMode === 'code') { viewMode = 'canvas'; codeYaml = ''; codeOrig = '' }
  }

  function applyCompile(data) {
    // Remember which saved agent we were editing BEFORE reset clears it.
    const prevAgentId = loadedAgentId
    resetTransientDraftState()
    workflow = (data && data.workflow) || null
    // If we were editing an existing saved agent, keep the freshly generated
    // draft bound to that agent so re-generating from a tweaked prompt UPDATES
    // it instead of silently saving a brand-new duplicate (the "Flight Finder →
    // Flight Deal Finder" bug). Use "New agent" to intentionally start a
    // separate one — that path clears loadedAgentId first.
    if (prevAgentId && workflow) {
      loadedAgentId = prevAgentId
      workflow.id = prevAgentId
    }
    questions = (data && Array.isArray(data.questions)) ? data.questions : []
    notes = (data && Array.isArray(data.notes)) ? data.notes : []
    explanation = (data && data.explanation) || null
    generationProfile = (data && data.generation) || null
    // M4: surface missing-capability suggestions (non-blocking). Keep only the
    // ones not yet installed; a fresh compile resets any in-flight discovery.
    suggestions = (data && Array.isArray(data.suggestions))
      ? data.suggestions.filter((s) => s && s.installed !== true)
      : []
    discoverState = {}
    selectedNode = null
    selectedEdge = null
    rebuildGraph()
    scheduleValidate()        // validate the fresh draft (debounced)
    autosaveDraft()           // persist the just-generated work so it's recoverable from Drafts
    // Guided default (Guided Studio Builder): land a freshly generated
    // deterministic workflow in the simple Plan view so the user reviews the
    // lanes + plain-English plan first. Agents keep their spec view.
    if (workflow && !workflow.strategy) viewMode = 'plan'
  }

  // ── M6: set the current draft directly (templates / draft-load / import) ───
  // Shared by every path that swaps in a complete workflow WITHOUT a compile
  // round-trip. Mirrors the post-compile reset so the canvas, inspector,
  // plan/tier and validation all refresh consistently.
  function setWorkflow(wf, { name } = {}) {
    // Clear prior-draft state first; callers that re-establish identity (e.g.
    // openAgentOnCanvas setting loadedAgentId) do so AFTER this returns.
    resetTransientDraftState()
    // Migrate legacy draggable entry/exit NODES into trigger/delivery settings
    // so old flows adopt the lane model (Guided Studio Builder, Story 2).
    workflow = wf ? migrateEndpoints(wf) : null
    if (workflow && name && !workflow.name) workflow = { ...workflow, name }
    // Restore the generating prompt into the intent box so the user can see and
    // edit the original instruction, then Generate to re-create the workflow.
    if (workflow && typeof workflow.intent === 'string') intent = workflow.intent
    // Restore the original (pre-refine) prompt for the dual-pane editor.
    rawPrompt = (workflow && typeof workflow.raw_intent === 'string') ? workflow.raw_intent : ''
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
    if (edgeExists(source, target, sourceHandle, targetHandle)) return false
    // Design-time type safety: a wire whose producer port type does not
    // structurally satisfy the consumer port type is rejected DURING the drag,
    // so the wrong connection is physically impossible to draw (principle #1).
    const rawNodes = (workflow && workflow.flow && workflow.flow.nodes) || []
    return validateConnection({ nodes: rawNodes, source, target, sourceHandle, targetHandle }).ok
  }

  function edgeExists(from, to, fromPort, toPort) {
    const edges = (workflow && workflow.flow && workflow.flow.edges) || []
    return edges.some((e) =>
      e.from === from && e.to === to &&
      (e.fromPort || '') === (fromPort || '') &&
      (e.toPort || '') === (toPort || ''))
  }

  // A connection was drawn on the canvas — persist it as a real flow edge.
  //
  // IMPORTANT: @xyflow/svelte delivers connections via the `onconnect` CALLBACK
  // PROP (it dispatches no Svelte `connect` event), and it passes the Connection
  // object DIRECTLY — not wrapped in `event.detail`. The library also optimistically
  // adds a default edge to its own store; persisting here + rebuildGraph() replaces
  // that optimistic edge with our real, typed flow edge so it survives the next
  // re-render (previously, drawing then dropping a block wiped the connection).
  function onConnect(conn) {
    const c = (conn && (conn.connection || conn.detail || conn)) || null
    if (!c || !c.source || !c.target) return
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
    // Same design-time type gate as the canvas drag, so the Inspector's manual
    // "Add connection" can't introduce a type-mismatched wire either.
    const rawNodes = (workflow.flow && workflow.flow.nodes) || []
    const compat = validateConnection({ nodes: rawNodes, source: from, target: to, sourceHandle: fromPort, targetHandle: toPort })
    if (!compat.ok) { toast(compat.reason || 'Incompatible connection'); return }
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
  let testResult = null       // { trace, result, assertions, passed, mode, warnings, shapes }
  // Phase D: the last run's captured output shapes (var -> sample), fed into the
  // per-node compiler so it wires downstream steps against REAL data.
  let lastShapes = []
  $: if (testResult && Array.isArray(testResult.shapes) && testResult.shapes.length) lastShapes = testResult.shapes
  let sampleInput = 'hello'
  let testMode = 'dry'        // 'dry' (default) | 'live' (rendered DISABLED)

  // The saved/loaded agent this draft corresponds to (set on save or when a
  // workflow is loaded onto the canvas). Enables fetching the agent's live
  // per-block run trace — what really happened on its last real run.
  let loadedAgentId = ''
  let runTrace = null         // { agentId, runId, startedAt, entries:[...] }
  let runDiagnosis = null     // { status, summary, rootCause, nextAction, suggestions, evidence }
  let runTraceErr = ''
  let runTraceLoading = false
  let runHistory = []         // [{runId, trigger, startedAt, ok, error, steps}] — every run
  let selectedRunId = ''      // which run's trace is shown ('' = latest)

  // Per-node mock editor state, keyed by node id: { text } (raw JSON the user
  // typed). Parsed lazily at run time; parse errors surface per-node via
  // mockErrors and the invalid mock is NOT sent.
  let mockText = {}           // { [nodeId]: string }
  let mockErrors = {}         // { [nodeId]: string }
  let showMocks = false       // collapsed by default to keep the panel tidy
  let showAssertions = true   // each bench sub-section folds independently

  // ── Workspace layout: collapse/resize the test panels + inspector ─────────
  // Lets the user reclaim space for the canvas (hide the bottom test/self-heal
  // section, hide the inspector) or widen the inspector to read code clearly.
  // Persisted across sessions so the chosen layout sticks.
  let showTests     = true
  let showInspector = true
  let inspectorWidth = 280     // px; clamped on resize
  let benchHeight   = 280      // px; height of the bottom workbench frame (drag-resizable)
  let maximizedFrame = ''      // '' | 'canvas' | 'bench' | 'inspector' — one frame fills the editor
  try {
    if (typeof localStorage !== 'undefined') {
      const p = JSON.parse(localStorage.getItem('studio.layout') || '{}')
      if (typeof p.showTests === 'boolean') showTests = p.showTests
      if (typeof p.showInspector === 'boolean') showInspector = p.showInspector
      if (typeof p.inspectorWidth === 'number') inspectorWidth = p.inspectorWidth
      if (typeof p.benchHeight === 'number') benchHeight = p.benchHeight
    }
  } catch (_) { /* ignore malformed/blocked storage */ }
  function persistLayout() {
    try {
      if (typeof localStorage !== 'undefined') {
        localStorage.setItem('studio.layout', JSON.stringify({ showTests, showInspector, inspectorWidth, benchHeight }))
      }
    } catch (_) { /* ignore */ }
  }
  // Maximize one frame to fill the editor (toggle off to restore the split view).
  function toggleMax(frame) { maximizedFrame = maximizedFrame === frame ? '' : frame }
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
  // Tracks the agent id whose RAW on-disk SOUL.yaml we've loaded into the code
  // editor, so we read the real file once per loaded reasoning agent instead of
  // re-deriving (lossily) from the draft. Reset implicitly when loadedAgentId
  // changes (the guard compares the two).
  let codeRawForId = ''
  let codeWarnings = []
  let codeError = ''
  let codeLoading = false
  let codeValidation = null    // { ok, errors, warnings, items[], fixes[] }
  let codeValidating = false
  let codeFixing = false       // AI fix in progress
  let reviewing = false        // rules-grounded AI review in progress

  async function showCodeView() {
    if (viewMode === 'code' || !workflow) return
    codeError = ''
    codeLoading = true
    viewMode = 'code'
    try {
      const isAgent = ['react', 'plan_execute'].includes(String(workflow.strategy || '').toLowerCase())
      const dirty = codeYaml !== codeOrig
      let yamlText = ''
      // Source of truth for a SAVED reasoning agent is its on-disk SOUL.yaml, not
      // the draft (which can't model every field — run_timeout, memory scopes,
      // confirm_tools, …). Read the real file once per loaded agent, as long as
      // there are no unsaved code edits to preserve.
      if (loadedAgentId && isAgent && !dirty && codeRawForId !== loadedAgentId) {
        try {
          const raw = await bridge.agentYaml(loadedAgentId)
          yamlText = (raw && raw.yaml) || ''
          if (yamlText) codeRawForId = loadedAgentId
        } catch (_) { /* fall through to the draft-derived YAML below */ }
      }
      if (!yamlText) {
        const r = await bridge.toYaml(workflow)
        yamlText = (r && r.yaml) || ''
      }
      codeYaml = yamlText
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

  // Simple "Plan" view (Guided Studio Builder): lanes + readiness cards. Reads
  // the same `workflow` model, so switching preserves edits. Clicking a card
  // jumps to the canvas + Inspector for that block.
  function showPlanView() {
    if (!workflow) return
    viewMode = 'plan'
  }
  function planSelectNode(node) {
    selectedNode = node
    selectedEdge = null
    viewMode = 'canvas'
  }

  // Patch a single node's config from the Plan view's Configuration Card
  // (Story 9). Replaces the node in flow.nodes and re-validates.
  function updateNodeConfig(updated) {
    if (!workflow || !updated || !updated.id) return
    const flow = workflow.flow || { nodes: [], edges: [], entry: '' }
    const nodes = (flow.nodes || []).map((n) => (n.id === updated.id ? updated : n))
    workflow = { ...workflow, flow: { ...flow, nodes } }
    if (selectedNode && selectedNode.id === updated.id) selectedNode = updated
    rebuildGraph()
    scheduleValidate()
  }

  // Insert a suggested Python step (Stories 3 & 4) right after the node it was
  // suggested for, pre-seeded from the matching template and auto-connected.
  // addStepFromText: describe a step in plain English; the backend compiles a
  // single block (recommending tool/python/agent) and appends it to the flow.
  let addStepBusy = false
  let addStepMsg = ''
  async function addStepFromText(instruction) {
    if (!workflow || !instruction || !instruction.trim() || addStepBusy) return
    addStepBusy = true
    addStepMsg = ''
    try {
      const res = await api.studio.addStep(workflow, instruction.trim())
      if (res && res.workflow) {
        workflow = res.workflow
        rebuildGraph()
        scheduleValidate()
        addStepMsg = `Added a ${res.recommended || 'step'}${res.step_summary ? `: ${res.step_summary}` : ''}.`
      }
    } catch (e) {
      addStepMsg = (e && e.message) || 'Could not add that step.'
    } finally {
      addStepBusy = false
    }
  }

  function addSuggestedPython(s) {
    if (!workflow || !s) return
    const flow = workflow.flow || { nodes: [], edges: [], entry: '' }
    const baked = bakePositions(flow.nodes)
    const anchor = baked.find((n) => n.id === s.nodeId)
    const id = uniqueNodeId('python')
    const node = {
      id, kind: 'python',
      code: pythonCodeFor(s.template), description: s.label,
      input: '', output: '', inputs: [], outputs: [], params: {},
      x: Math.round((anchor?.x || 0) + 220), y: Math.round(anchor?.y || 0),
    }
    // rewire anchor → python → (whatever anchor pointed at)
    let edges = flow.edges || []
    const downstream = edges.filter((e) => e && e.from === s.nodeId)
    edges = edges.filter((e) => !(e && e.from === s.nodeId))
    edges = [...edges, { from: s.nodeId, to: id }, ...downstream.map((e) => ({ ...e, from: id }))]
    workflow = { ...workflow, flow: { ...flow, nodes: [...baked, node], edges } }
    selectedNode = node
    rebuildGraph()
    scheduleValidate()
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

  // Rules-grounded AI review: ask the model to check the YAML against the
  // rulebook for judgment-call problems the deterministic validator can't catch,
  // and merge its findings into the validation panel.
  async function reviewWithAI() {
    if (reviewing) return
    reviewing = true
    codeError = ''
    try {
      const r = await bridge.reviewYaml(codeYaml)
      const aiItems = (r && Array.isArray(r.items)) ? r.items : []
      const base = codeValidation || { items: [], fixes: [] }
      const items = [...((base.items) || []).filter((i) => i.source !== 'ai'), ...aiItems]
      const errors = items.filter((i) => i.severity === 'error').length
      const warnings = items.filter((i) => i.severity === 'warning').length
      codeValidation = { ok: errors === 0, errors, warnings, items, fixes: (base.fixes) || [] }
    } catch (e) {
      codeError = (e && e.message) || 'AI review failed'
    }
    reviewing = false
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

  // Fix with AI: ask the framework model to rewrite the whole SOUL.yaml so every
  // issue is resolved (handles cases the deterministic quick-fix can't — picking
  // the right field, restructuring), then re-validate.
  async function fixWithAI() {
    if (codeFixing) return
    codeFixing = true
    codeError = ''
    try {
      const r = await bridge.fixYaml(codeYaml)
      if (r && r.yaml) codeYaml = r.yaml
      await validateCode()
    } catch (e) {
      codeError = (e && e.message) || 'AI fix failed — try again or fix manually'
    }
    codeFixing = false
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
      saveMsg = id ? `Saved ${id} — manage it from Deployed.` : 'Saved'
      // Mirror the canvas Save: hand off to Deployed to review/enable.
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

  // Drag the splitter above the bottom workbench to resize its height. Height is
  // taken from the editor's bottom edge upward (the workbench is the bottom frame).
  function startBenchResize(e) {
    const main = e.currentTarget.closest('main')
    const bottom = main ? main.getBoundingClientRect().bottom : window.innerHeight
    const onMove = (ev) => {
      benchHeight = Math.max(120, Math.min(bottom - 180, bottom - ev.clientY))
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
  function liveRunEvidence(result = tryResult) {
    const rows = []
    for (const t of (result && result.trace) || []) {
      const bits = [`step/tool: ${t.name || t.nodeId || '(unknown)'}`]
      if (t.detail) bits.push(`detail: ${t.detail}`)
      if (t.args) bits.push(`args: ${t.args}`)
      if (t.result) bits.push(`result: ${t.result}`)
      if (t.error) bits.push('status: error')
      rows.push('- ' + bits.join(' | '))
    }
    return rows.join('\n')
  }

  async function troubleshoot(errText, opts = {}) {
    const err = (errText || testError || '').trim()
    if (!workflow || !err || troubleshooting) return
    troubleshooting = true
    try {
      const res = await bridge.troubleshoot(workflow, err, opts)
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

  function troubleshootLiveRun() {
    if (!tryResult || !tryResult.error) return
    troubleshoot(tryResult.error, {
      input: sampleInput,
      evidence: liveRunEvidence(tryResult),
    })
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

  // F-GUI-3 — Cohort F S6 security review. Re-runs on debounced draft change
  // so the panel stays live while the operator edits the workflow, and its
  // blockers merge into the same pre-save preflight gate that Contract already
  // uses (so blockers block Save the same way).
  //
  //   securityReview  — last successful /studio/security_review response
  //   securityLoading — in-flight indicator for the panel
  //   securityError   — surface API failures in the panel without blocking Save
  //   securityRunToken — increments each debounce so stale replies don't win
  let securityReview   = null
  let securityLoading  = false
  let securityError    = ''
  let securityDebounce = null
  let securityRunToken = 0
  let securityPanelOpen = true

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
        try { report = await runStudioContract(workflow) } catch (_) { report = null }
        // Open the dialog when there's anything to show. Blockers stop the save;
        // warnings let the user proceed with "Save anyway". A failed/empty
        // contract check (host error) is non-blocking — fall through to plan/save.
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
    try { preflight = await runStudioContract(workflow) } catch (_) { /* keep current */ }
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
      preflight = await runStudioContract((res && res.workflow) || workflow)
    } catch (e) {
      saveError = e.message || 'auto-fix failed'
    } finally {
      fixing = false
    }
  }

  async function runStudioContract(draft) {
    // F-GUI-3 — pair the contract report with the security review so their
    // blockers/warnings land in the same preflight modal. Both requests fire
    // in parallel; a security failure never blocks the contract path.
    let contract = null
    let secReview = null
    await Promise.all([
      (async () => { try { contract = await bridge.contract(draft) } catch (_) { contract = null } })(),
      (async () => {
        try { secReview = await bridge.securityReview(draft) } catch (_) { secReview = null }
      })(),
    ])
    // Also refresh the always-visible panel state so operators aren't
    // surprised when Save opens the modal.
    if (secReview) securityReview = secReview

    const contractIssues = (() => {
      if (!contract) return { blockers: [], warnings: [] }
      const checks = Array.isArray(contract.checks) ? contract.checks : []
      const toIssue = (c) => ({
        severity: c.status === 'block' ? 'blocker' : 'warning',
        kind: c.id || 'studio.contract',
        nodeId: c.nodeId || '',
        message: `${c.title || 'Studio contract'}: ${c.message || ''}`.trim(),
        fix: c.fix || '',
      })
      return {
        blockers: checks.filter((c) => c && c.status === 'block').map(toIssue),
        warnings: checks.filter((c) => c && c.status === 'warn').map(toIssue),
      }
    })()

    const secIssues = (() => {
      if (!secReview) return { blockers: [], warnings: [] }
      const toIssue = (severity) => (f) => ({
        severity,
        kind: 'studio.security.' + (f.category || 'finding'),
        nodeId: '',
        message: `Security ${f.category || 'finding'}: ${f.message || ''}`.trim(),
        fix: f.fix || '',
      })
      return {
        blockers: (secReview.blockers || []).map(toIssue('blocker')),
        warnings: (secReview.warnings || []).map(toIssue('warning')),
      }
    })()

    if (!contract && !secReview) {
      // Fall back to legacy preflight for downstream compatibility.
      try { return await bridge.preflight(draft) } catch (_) { return null }
    }
    return {
      ok: (!contract || !!contract.ok) && (!secReview || !!secReview.ok),
      blockers: [...contractIssues.blockers, ...secIssues.blockers],
      warnings: [...contractIssues.warnings, ...secIssues.warnings],
      contract,
      security: secReview,
    }
  }

  // F-GUI-3 — debounced always-on security review so the panel stays fresh
  // while the operator edits the graph. Keeps the pre-save gate honest without
  // exploding the request count. Only fires when we have a draft.
  function scheduleSecurityReview() {
    if (typeof window === 'undefined') return
    if (securityDebounce) clearTimeout(securityDebounce)
    securityDebounce = setTimeout(refreshSecurityReview, 700)
  }
  async function refreshSecurityReview() {
    if (!workflow) return
    const token = ++securityRunToken
    securityLoading = true
    securityError = ''
    try {
      const res = await bridge.securityReview(workflow)
      if (token !== securityRunToken) return  // stale
      securityReview = res || null
    } catch (e) {
      if (token !== securityRunToken) return
      securityError = e.message || 'security review failed'
    } finally {
      if (token === securityRunToken) securityLoading = false
    }
  }

  // F-GUI-3 — unambiguous rewrites for the top three risky tools. Any tool
  // node in the workflow whose `tool` (or nested tool ref) matches `from`
  // gets rewritten to `suggest`. When there's no exact match (custom tools,
  // MCP calls, etc.), we quietly skip.
  function applySecurityRecommendation(rec) {
    if (!rec || !rec.from || !rec.suggest || !workflow) return
    const from = String(rec.from)
    // The suggested replacement text for shell_exec / http_request in
    // security_preflight.go is a sentence, not a tool name; only auto-apply
    // when it looks like a bare tool identifier.
    const suggestId = /^[a-zA-Z0-9_.-]+$/.test(rec.suggest) ? rec.suggest : ''
    if (!suggestId) {
      toast(`Recommendation applied to notes — replace ${from} with ${rec.suggest} manually.`)
      return
    }
    const flow = workflow.flow || {}
    const nodes = Array.isArray(flow.nodes) ? flow.nodes : []
    let hits = 0
    const nextNodes = nodes.map((n) => {
      if (!n) return n
      const cfg = n.config || {}
      if (n.tool === from || cfg.tool === from) {
        hits++
        return {
          ...n,
          tool: n.tool === from ? suggestId : n.tool,
          config: cfg.tool === from ? { ...cfg, tool: suggestId } : cfg,
        }
      }
      return n
    })
    if (!hits) {
      toast(`No ${from} usage found to rewrite.`)
      return
    }
    setWorkflow({ ...workflow, flow: { ...flow, nodes: nextNodes } }, { name: workflow.name })
    toast(`Rewrote ${hits} ${from} → ${suggestId}.`)
    scheduleSecurityReview()
  }

  // React to workflow updates via a reactive statement. `workflow` is
  // reassigned by many code paths (edits, autowire, build) so this covers all
  // of them without threading calls into each mutator.
  $: if (workflow) scheduleSecurityReview()

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
  let buildTraceId = null // id of the last build's durable trace (build inspector)
  let showBuildInspector = false
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
        buildTraceId = res.traceId || null
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
  // Story 3 AC3 — heal produces a concrete patch with before/after diff, not a
  // silent canvas replace. `healDiff` holds the candidate + rendered diff and
  // waits for operator confirmation via applyHealDiff() before touching the
  // live workflow. Reuses .repair-diff CSS so the pane looks identical to the
  // per-node repair-diff renderer (both surfaces are "before/after this fix").
  let healDiff = null // { candidate, lines, stats, source, name }
  let healDiffBusy = false
  async function prepareHealDiff(res, source) {
    if (!res || !res.workflow) return
    healDiffBusy = true
    try {
      const before = workflow || { name: (res.workflow && res.workflow.name) || 'workflow', flow: { nodes: [], edges: [] } }
      const d = await api.studio.diff(before, res.workflow)
      healDiff = {
        candidate: res.workflow,
        lines: (d && d.lines) || [],
        stats: (d && d.stats) || { added: 0, removed: 0 },
        source,
        name: (res.workflow && res.workflow.name) || (workflow && workflow.name) || 'workflow',
      }
    } catch (_) {
      // Diff service failed — fall back to no-diff panel but still let operator
      // apply the healed workflow. Preserves the pre-AC3 behaviour minus the
      // silent replace.
      healDiff = {
        candidate: res.workflow,
        lines: [],
        stats: { added: 0, removed: 0 },
        source,
        name: (res.workflow && res.workflow.name) || (workflow && workflow.name) || 'workflow',
      }
    } finally {
      healDiffBusy = false
    }
  }
  function applyHealDiff() {
    if (!healDiff) return
    setWorkflow(healDiff.candidate, { name: healDiff.name })
    const kind = healDiff.source === 'session' ? 'Debugged real run applied' : 'Heal applied'
    toast(kind + ' — review and Save to persist.')
    healDiff = null
  }
  function cancelHealDiff() { healDiff = null }
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
    healDiff = null
    saveError = ''
    try {
      const res = await bridge.diagnoseRun(id)
      healResult = res
      if (res && res.workflow) {
        await prepareHealDiff(res, 'run')
        toast(res.changed ? 'Healed — preview the change below and click Apply this fix to load it.' : 'No fix was suggested for this error.')
      }
    } catch (e) {
      saveError = e.message || 'diagnose failed'
    } finally {
      healing = ''
    }
  }

  async function healActivityRun(agentId, sessionId) {
    if (healing || !agentId || !sessionId) return
    healing = `session:${agentId}:${sessionId}`
    healResult = null
    healDiff = null
    saveError = ''
    try {
      const res = await bridge.diagnoseSession(agentId, sessionId)
      healResult = res
      loadedAgentId = agentId
      // Story 3 AC2b — prefill the Test bench sample input with the original
      // failing user input so operators re-test the exact case that broke,
      // not the literal 'hello' default. Empty string skips (keeps whatever
      // the operator already had typed).
      if (res && typeof res.failing_input === 'string' && res.failing_input.trim() !== '') {
        sampleInput = res.failing_input
      }
      if (res && res.workflow) {
        await prepareHealDiff(res, 'session')
        toast(res.changed ? 'Debugged real run — preview the change below and click Apply this fix to load it.' : 'No fix was suggested for this run.')
      }
    } catch (e) {
      saveError = e.message || 'debug failed'
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
      loadedAgentId = id || loadedAgentId
      saveMsg = `Saved as disabled agent ${id} — enable it from Deployed.`
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

  // Fetch the saved agent's most recent live run trace — the per-block record
  // of what actually ran (input, output, duration, error, whether the input came
  // from typed port wires). Surfaced so a non-technical user can see WHERE a real
  // run went wrong, not just that it failed.
  async function loadRunTrace(runId = '') {
    if (!loadedAgentId || runTraceLoading) return
    runTraceLoading = true
    runTraceErr = ''
    selectedRunId = runId || ''
    try {
      // Fetch the chosen run's trace AND refresh the full run history (every run,
      // scheduled or on-demand) so the picker stays current.
      const [res, hist, diagnosis] = await Promise.all([
        bridge.runTrace(loadedAgentId, runId || undefined),
        bridge.runHistory(loadedAgentId).catch(() => ({ runs: [] })),
        bridge.runDiagnosis(loadedAgentId, runId || undefined).catch(() => null),
      ])
      runTrace = res || null
      runDiagnosis = diagnosis || null
      runHistory = (hist && hist.runs) || []
      if (!selectedRunId && runTrace && runTrace.runId) selectedRunId = runTrace.runId
      if (!res || !res.entries || !res.entries.length) {
        runTraceErr = runHistory.length
          ? 'Select a run above to view its trace.'
          : 'No runs yet — enable the agent and let it run once (on schedule or on demand).'
      }
    } catch (e) {
      runDiagnosis = null
      runTraceErr = e.message || 'could not load run trace'
    } finally {
      runTraceLoading = false
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

  // prettyJSON renders a value as indented JSON — and, crucially, parses a STRING
  // that holds JSON (the rendered node input arrives as a string) so it reads as
  // formatted JSON instead of one long escaped line.
  function prettyJSON(v) {
    if (v == null) return ''
    if (typeof v === 'string') {
      const s = v.trim()
      if (s && (s[0] === '{' || s[0] === '[')) {
        try { return JSON.stringify(JSON.parse(s), null, 2) } catch (_) { /* not JSON */ }
      }
      return v
    }
    try { return JSON.stringify(v, null, 2) } catch (_) { return String(v) }
  }

  // asObject coerces a step's output (object or JSON string) to an object, or null.
  function asObject(v) {
    if (v && typeof v === 'object') return v
    if (typeof v === 'string') { try { return JSON.parse(v) } catch (_) { return null } }
    return null
  }

  // stepToolError surfaces a TOOL-LEVEL error that the transport hid: a node can
  // return `error:""` (the MCP call succeeded) while its OUTPUT carries
  // {"status":"error","error":"…"}. This returns that human error message (or the
  // node's own runtime error), else "" — so a failed step reads as failed.
  function stepToolError(step) {
    if (step && step.error) return step.error
    const o = asObject(step && step.output)
    if (o && (o.status === 'error' || o.status === 'failed')) return o.error || o.message || 'tool returned an error'
    if (o && o.error) return String(o.error)
    return ''
  }

  // stepSummary gives a one-line, human read of a successful step: its status plus
  // the most useful identifying field and any message — so you rarely need to open
  // the raw JSON at all.
  function stepSummary(step) {
    const o = asObject(step && step.output)
    if (!o) return ''
    const bits = []
    if (o.status) bits.push(o.status)
    for (const k of ['notebook_id', 'task_id', 'artifact_id', 'id', 'imported_count', 'sources_found', 'count']) {
      if (o[k] != null) { bits.push(`${k}: ${String(o[k]).slice(0, 48)}`); break }
    }
    if (o.message) bits.push(String(o.message).slice(0, 120))
    return bits.join(' · ')
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

  // Read-only SOUL.yaml browser: view the raw on-disk SOUL.yaml of ANY registered
  // agent (not only workflow-bearing ones), straight from the file — no lossy
  // draft round-trip. Purely for inspection; editing still goes through the
  // canvas/code views.
  let yamlBrowser = {
    open: false, loading: false, error: '',
    agents: [], selectedId: '', yaml: '', path: '', yamlLoading: false, yamlError: '',
  }

  // Open the browser and load the full agent list (every agent, not just
  // Studio-authored workflows).
  async function openYamlBrowser() {
    yamlBrowser = { ...yamlBrowser, open: true, loading: true, error: '', agents: [], selectedId: '', yaml: '', path: '', yamlError: '' }
    try {
      const res = await bridge.allAgents()
      const agents = Array.isArray(res?.agents) ? res.agents : (Array.isArray(res) ? res : [])
      yamlBrowser = { ...yamlBrowser, loading: false, agents }
      // Auto-select the first agent for immediate context.
      if (agents.length) viewAgentYaml(agents[0].id)
    } catch (e) {
      yamlBrowser = { ...yamlBrowser, loading: false, error: e?.message || 'Failed to load agents' }
    }
  }

  function closeYamlBrowser() {
    yamlBrowser = { ...yamlBrowser, open: false }
  }

  // Fetch and show the raw SOUL.yaml for one agent.
  async function viewAgentYaml(id) {
    if (!id) return
    yamlBrowser = { ...yamlBrowser, selectedId: id, yamlLoading: true, yamlError: '', yaml: '', path: '' }
    try {
      const res = await bridge.agentYaml(id)
      yamlBrowser = { ...yamlBrowser, yamlLoading: false, yaml: res?.yaml || '', path: res?.path || '' }
    } catch (e) {
      yamlBrowser = { ...yamlBrowser, yamlLoading: false, yamlError: e?.message || 'Failed to load SOUL.yaml' }
    }
  }

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
      loadedAgentId = id
      runTrace = null
      runDiagnosis = null
      toast('Loaded workflow — edit and Save to update the agent.')
    } catch (e) {
      toast(e.message || 'Could not load agent workflow')
    }
  }

  // When the agent currently ON the canvas is deleted, the canvas must go with
  // it. Leaving the workflow up is worse than untidy: the editor still thinks
  // it is editing agent X, so hitting Save would silently RECREATE the agent the
  // user just deleted. Clearing loadedAgentId is what actually prevents that;
  // wiping the canvas is what makes the deletion believable.
  function clearCanvasIfShowing(id) {
    if (!id || loadedAgentId !== id) return false
    loadedAgentId = ''
    runTrace = null
    runDiagnosis = null
    codeYaml = ''
    codeOrig = ''
    codeValidation = null
    codeWarnings = []
    compileError = ''
    setWorkflow(null)
    return true
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
      const wasOpen = clearCanvasIfShowing(id)
      toast(wasOpen
        ? `Deleted agent ${name || id}. Canvas cleared.`
        : `Deleted agent ${name || id}.`)
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
      loadedAgentId = a.id
      runTrace = null
      runDiagnosis = null
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
      if (clearCanvasIfShowing(a.id)) toast(`Deleted agent ${a.name || a.id}. Canvas cleared.`)
      await loadCatalog()
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

  // Story 9 (Cohort B) — intent-named runtime presets. `studioPresets` holds
  // the catalog fetched from GET /studio/presets; `studioPresetCurrent` is the
  // operator's current choice (empty = model-derived defaults, no override).
  let studioPresets = []
  let studioPresetCurrent = ''
  let studioPresetSaving = false
  let studioPresetError = ''

  // Story 9 M (Cohort C) — build UX preference. `streamed` (default) surfaces
  // a live-transcript panel while the pipeline runs; `wizard` opens the
  // classic stepped modal (refine → confirm → compile) with the strategy
  // review step made explicit. Persisted in llm.studio.build_ux; a
  // per-generation override lives on `buildUXOverride` (nullable).
  let buildUX = 'streamed'
  let buildUXOverride = null
  let buildUXSaving = false
  // Live-transcript state for a streamed generate.
  let pipelineLog = [] // [{phase, status, message, payload}]
  let pipelineRunning = false
  // Progress modal: shown while any generation is in flight so the user can see
  // what's happening instead of staring at a blank/"Compiling…" canvas. It works
  // for BOTH the streamed pipeline (pipelineRunning, with real per-phase events)
  // and the classic refine→compile path (refining/compiling, no sub-events).
  // `pipelineModalHidden` lets them dismiss the overlay and let it run on.
  let pipelineModalHidden = false
  $: genBusy = pipelineRunning || compiling || refining
  $: pipelineLatest = pipelineLog.length ? pipelineLog[pipelineLog.length - 1] : null
  // Real backend phase names (from internal/studio/generatepipeline.go) — not
  // invented UI copy. Used to label the streamed events the server actually
  // emits, and to derive the current-operation line.
  function phaseLabel(phase) {
    switch (phase) {
      case 'clarify_intent':  return 'Clarifying intent'
      case 'choose_strategy': return 'Choosing strategy'
      case 'build_graph':     return 'Building the graph'
      case 'validate':        return 'Validating'
      case 'repair':          return 'Repairing wiring'
      default:                return phase || 'Working'
    }
  }
  // The one-line "what's happening right now", reflecting the real operation in
  // flight rather than a scripted step. The refine and compile calls ARE real
  // builder-model calls; the streamed path names its actual current phase.
  $: genStatusText = refining
      ? 'Refining your prompt into a build-ready spec…'
      : pipelineRunning
        ? (pipelineLatest ? phaseLabel(pipelineLatest.phase) + '…' : 'Starting the build pipeline…')
        : compiling
          ? 'Asking the builder model to write the workflow…'
          : 'Working…'

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
      studioPresetCurrent = st.preset || ''
      if (st.build_ux === 'wizard' || st.build_ux === 'streamed') buildUX = st.build_ux
    }).catch(() => {})
    bridge.presets().then((res) => {
      studioPresets = (res && res.presets) || []
      if (res && typeof res.current === 'string') studioPresetCurrent = res.current
    }).catch(() => { studioPresets = [] })
    refreshModelAdvice()
  })

  async function saveStudioPreset(name) {
    if (studioPresetSaving) return
    studioPresetSaving = true
    studioPresetError = ''
    try {
      await bridge.setStudioPreset(name || '')
      studioPresetCurrent = name || ''
    } catch (e) {
      studioPresetError = (e && e.message) || 'could not save preset'
    } finally {
      studioPresetSaving = false
    }
  }

  async function saveBuildUX(mode) {
    if (buildUXSaving) return
    buildUXSaving = true
    try {
      await bridge.setBuildUX(mode)
      buildUX = mode
    } catch (_) { /* soft-fail — UX preference is non-critical */ }
    finally { buildUXSaving = false }
  }

  // effectiveBuildUX resolves the per-generation override on top of the
  // persisted preference. Toggling the button next to Generate flips this
  // for the next run without touching the saved setting.
  $: effectiveBuildUX = buildUXOverride || buildUX

  // Story 9 M (Cohort C) — streamed generate. Runs the whole pipeline in one
  // SSE call, updating `pipelineLog` per phase. When the stream finishes
  // successfully the compiled draft is applied to the canvas via the same
  // applyCompile path the classic flow uses.
  async function generateStreamed() {
    const text = intent.trim()
    if (!text || compiling || refining || pipelineRunning || cloudGate) return
    if (modelAdvice && modelAdvice.cloud_escalation && !cloudAck) {
      cloudGate = { provider: modelAdvice.provider, model: modelAdvice.model }
      return
    }
    if (workflow && workflow.flow && (workflow.flow.nodes || []).length) {
      let ok = true
      try { ok = window.confirm('Regenerate from this prompt? It replaces the current workflow on the canvas.') } catch (_) { ok = true }
      if (!ok) return
    }
    pipelineRunning = true
    pipelineModalHidden = false
    pipelineLog = []
    compileError = ''
    try {
      const light = !!(workflow && workflow.refined)
      if (!light && !rawPrompt.trim()) rawPrompt = text
      const done = await bridge.generateStream(
        text,
        { light, auto_repair: true },
        (ev) => {
          if (!ev || !ev.phase) return
          pipelineLog = [...pipelineLog, ev]
        },
      )
      const result = done && done.result
      if (done && done.error) {
        compileError = done.error
      } else if (result && result.compile) {
        applyCompile(result.compile)
        if (result.refinement) rawPrompt = result.refinement.original || rawPrompt
      }
    } catch (e) {
      compileError = (e && e.message) || 'streamed generate failed'
    } finally {
      pipelineRunning = false
      buildUXOverride = null // consume the override
    }
  }

  // Router — invoked from the Generate button. Delegates to the streamed or
  // wizard path based on the effective UX preference.
  function generateOrStream() {
    if (effectiveBuildUX === 'streamed') return generateStreamed()
    return generate()
  }

  // Fetch builder-model strength advice so we can warn before generation.
  async function refreshModelAdvice() {
    try { modelAdvice = await bridge.modelAdvice() } catch (_) { modelAdvice = null }
  }

  async function openModelPicker() {
    modelPicker = { open: true, provider: '', model: '', models: [], saving: false, error: '' }
    resetModelChooser()
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
    resetModelChooser()
    loadModelsFor(p)
  }

  // ── model chooser ────────────────────────────────────────────────────────
  // Two earlier attempts got this wrong, both for the same underlying reason:
  // they tried to show the model list in a layer floating ABOVE the field.
  //
  //   1. <input list> + <datalist> — the browser filters a datalist against the
  //      input's current value, so once a model was chosen the list could only
  //      offer that same model back. You had to clear the field to see anything.
  //   2. A hand-rolled absolute dropdown — the modal is `overflow-y: auto`, so
  //      the dropdown was clipped by it, and it covered the Save button.
  //
  // A modal is not a page: there is no room to float things over. So the list
  // is now part of the modal's flow — it takes space instead of stealing it.
  // Nothing can clip it and nothing can hide the buttons.
  let modelFilter = ''   // filters the list; NOT the chosen value
  let manualModel = false

  // Reset per-open so the list never opens pre-filtered from a previous visit.
  function resetModelChooser() {
    modelFilter = ''
    manualModel = false
  }

  $: modelChoices = (() => {
    const all = modelPicker.models || []
    const q = modelFilter.trim().toLowerCase()
    if (!q) return all
    return all.filter((m) => m.toLowerCase().includes(q))
  })()

  function chooseModel(m) {
    modelPicker = { ...modelPicker, model: m }
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
  onMount(loadSecrets)
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

  function hydrateRouteIntent() {
    const h = location.hash || ''
    const idx = h.indexOf('?')
    if (idx < 0) return
    const params = new URLSearchParams(h.slice(idx + 1))
    const routeIntent = (params.get('intent') || '').trim()
    if (!routeIntent) return
    intent = routeIntent
    rawPrompt = routeIntent
    refinement = null
    questions = []
    answers = {}
    notes = []
    explanation = null
    compileError = ''
    promptViewer = true
    viewMode = 'canvas'
    codeYaml = ''
    codeOrig = ''
    codeValidation = null
    codeWarnings = []
    setWorkflow(null)
    history.replaceState({}, '', '#studio')
  }
  onMount(hydrateRouteIntent)

  onMount(() => {
    const pending = get(studioDebugRun)
    if (!pending || !pending.agentId || !pending.sessionId) return
    studioDebugRun.set(null)
    loadedAgentId = pending.agentId
    showTests = true
    showFailedRuns = true
    healResult = {
      agentId: pending.agentId,
      sessionId: pending.sessionId,
      error: pending.error || '',
      changed: false,
      pending: true,
    }
    toast(`Debugging ${pending.agentId} from Runs…`)
    // Story 3 AC2c — surface the structured runTrace panel (per-block input/
    // output/duration/error, plus run history picker) alongside the debug
    // flow. Without this the operator only saw the compacted evidence blob
    // inside healResult; loadRunTrace hydrates the same runTrace panel Studio
    // shows for any saved agent so debugging a failed run reuses the exact
    // "where did this run actually go wrong" UI operators already know.
    loadRunTrace(pending.sessionId).catch(() => {})
    healActivityRun(pending.agentId, pending.sessionId)
  })

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

<svelte:window on:keydown={(e) => { if (e.key === 'Escape' && promptViewer) promptViewer = false }} />

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
        class="intent-trigger"
        value={intent}
        readonly
        placeholder="Describe what you want…  (click to open the editor)"
        aria-label="Open prompt editor"
        data-tooltip="Click to view and edit the full prompt"
        on:focus={() => (promptViewer = true)}
        on:click={() => (promptViewer = true)}
        on:keydown={(e) => { if (e.key === 'Enter') promptViewer = true }}
      />
      <button class="intent-expand" type="button" data-tooltip="Open the full prompt editor" on:click={() => (promptViewer = true)} aria-label="Open prompt editor">⤢ Editor</button>
    </div>
    <div class="generate-group" style="display:inline-flex;gap:.35rem;align-items:center;">
      <button class="btn primary" on:click={generateOrStream} disabled={compiling || refining || pipelineRunning || !!refinement || !intent.trim()}>
        {refining ? 'Refining…' : compiling ? 'Generating…' : pipelineRunning ? 'Running pipeline…' : 'Generate'}
      </button>
      <!--
        Story 9 M — per-generation UX toggle. Clicking "Wizard" for this
        generation flips the effective mode without saving it; the split
        button label reflects the current mode so operators know what will
        happen when they click Generate.
      -->
      <button
        class="btn"
        type="button"
        data-tooltip={effectiveBuildUX === 'streamed' ? 'Streamed: live-transcript panel while the pipeline runs. Click to switch to Wizard for this generation only.' : 'Wizard: stepped modal that pauses between phases. Click to switch to Streamed for this generation only.'}
        on:click={() => (buildUXOverride = effectiveBuildUX === 'streamed' ? 'wizard' : 'streamed')}
      >
        {effectiveBuildUX === 'streamed' ? '⚡ Streamed' : '🪄 Wizard'}
        {#if buildUXOverride}<span style="opacity:.6;">(once)</span>{/if}
      </button>
    </div>

    <!-- M6: draft management toolbar -->
    <div class="toolbar" role="group" aria-label="Draft management">
      <button class="btn primary-ghost" type="button" on:click={startNewAgent} data-tooltip="Start a new agent from scratch">+ New agent</button>
      <button class="btn" type="button" on:click={openRules} data-tooltip="Edit the SOUL.yaml authoring rules used when generating, validating, and fixing">📋 Rules</button>
      <button class="btn" type="button" on:click={openModelPicker} data-tooltip="Choose which in-framework provider/model Studio uses">⚙ {studioModelLabel}</button>
      <button class="btn" type="button" on:click={openTemplates} data-tooltip="Start from a template">Templates</button>
      <button class="btn" type="button" on:click={openLibrary} data-tooltip="Reopen a saved draft or an existing workflow agent">My Workflows</button>
      <button class="btn" type="button" on:click={openYamlBrowser} data-tooltip="View the raw SOUL.yaml of any agent (read-only)">Browse SOUL.yaml</button>
      <button class="btn" type="button" on:click={saveDraft} disabled={!workflow || savingDraft} data-tooltip="Save the current draft to the library">
        {savingDraft ? 'Saving…' : 'Save draft'}
      </button>
      <button class="btn" type="button" on:click={exportDraft} disabled={!workflow} data-tooltip="Download the current draft as a .studio.json file">Export</button>
      <button class="btn" type="button" on:click={triggerImport} data-tooltip="Load a .studio.json file from disk">Import</button>
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

  <main class="body" class:max-canvas={maximizedFrame === 'canvas'} class:max-bench={maximizedFrame === 'bench'} class:max-inspector={maximizedFrame === 'inspector'}>
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
        <div class="toolbar-row">
          <div class="view-switch" role="tablist" aria-label="Editor view">
            {#if currentMode === 'workflow'}
              <button class="vs-btn" class:active={viewMode === 'plan'} type="button"
                      role="tab" aria-selected={viewMode === 'plan'} on:click={showPlanView}
                      data-tooltip="Simple plan view — Trigger, Work Plan, Delivery lanes">📋 Plan</button>
            {/if}
            <button class="vs-btn" class:active={viewMode === 'canvas'} type="button"
                    role="tab" aria-selected={viewMode === 'canvas'} on:click={showCanvasView}
                    data-tooltip="Advanced view — the full graph you can edit node by node">{currentMode === 'workflow' ? '⬚ Canvas' : '🧠 Agent'}</button>
            <button class="vs-btn" class:active={viewMode === 'code'} type="button"
                    role="tab" aria-selected={viewMode === 'code'} on:click={showCodeView}>{'</> SOUL.yaml'}</button>
            {#if viewMode === 'code' && codeYaml !== codeOrig}<span class="vs-dirty" data-tooltip="Unsaved YAML edits">●</span>{/if}
          </div>
          <!-- Execution-mode override: a fixed workflow, or a reasoning agent. -->
          <div class="mode-switch" role="group" aria-label="Execution mode"
            data-tooltip="How this agent runs. Workflow = a fixed graph. ReAct / Plan-Execute = a reasoning agent that chooses its tools/skills at runtime. Switching re-generates from your prompt.">
            <span class="ms-label">mode</span>
            <button class="ms-btn" class:active={currentMode === 'workflow'} type="button"
                    disabled={compiling} on:click={() => switchMode('workflow')}>Workflow</button>
            <button class="ms-btn" class:active={currentMode === 'react'} type="button"
                    disabled={compiling} on:click={() => switchMode('react')}>ReAct</button>
            <button class="ms-btn" class:active={currentMode === 'plan_execute'} type="button"
                    disabled={compiling} on:click={() => switchMode('plan_execute')}>Plan-Execute</button>
          </div>
        </div>
      {/if}

      {#if needsDelivery && viewMode === 'canvas'}
        <div class="strip strip-delivery" data-tooltip="Where this agent's result is sent">
          <span class="strip-label">Deliver to</span>
          <span class="dlv-hint">This {currentMode === 'workflow' ? 'workflow' : 'agent'} produces a result but has no delivery channel. Send it to:</span>
          {#each channelOptions as ch}
            <button class="dlv-btn" type="button" on:click={() => addDeliveryChannel(ch.id)}>+ {ch.name}</button>
          {/each}
        </div>
      {/if}

      {#if workflow && unsetSecrets.length && viewMode === 'canvas'}
        <Collapsible id="credentials" data-tooltip="Credentials" sub={`${unsetSecrets.length} key${unsetSecrets.length === 1 ? '' : 's'} not set — your tools/MCP may need them`} open={false}>
          <div class="creds">
            <p class="creds-hint">Set the API keys your tools and MCP servers need, without leaving Studio. Values are stored in the gateway's secret store, never in the agent file.</p>
            {#each unsetSecrets as sec (sec.name)}
              <div class="cred-row">
                <div class="cred-meta">
                  <span class="cred-name">{sec.name}</span>
                  {#if sec.env_var}<code class="cred-env">{sec.env_var}</code>{/if}
                  {#if sec.description}<span class="cred-desc">{sec.description}</span>{/if}
                </div>
                <div class="cred-set">
                  <input class="cred-input" type="password" placeholder="paste value…"
                    bind:value={secretVals[sec.name]}
                    on:keydown={(e) => { if (e.key === 'Enter') setSecretVal(sec.name) }} />
                  <button class="btn btn-sm" type="button" disabled={secretBusy === sec.name || !(secretVals[sec.name] || '').trim()}
                    on:click={() => setSecretVal(sec.name)}>{secretBusy === sec.name ? 'Saving…' : 'Set'}</button>
                </div>
              </div>
            {/each}
            {#if secretMsg}<div class="creds-msg">{secretMsg}</div>{/if}
          </div>
        </Collapsible>
      {/if}

      {#if codeWarnings.length}
        <div class="strip strip-notes" data-tooltip="Things the canvas can't show — they stay in the YAML">
          <span class="strip-label">Kept in YAML</span>
          {#each codeWarnings as w}<span class="note">{w}</span>{/each}
        </div>
      {/if}
      {#if modelAdvice && (modelAdvice.severity === 'block' || modelAdvice.local_complexity_note || modelAdvice.cloud_escalation)}
        <div class="strip strip-modeladvice" class:strip-local={modelAdvice.local} data-tooltip="Builder model">
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
      {#if generationProfile}
        <div class="strip strip-modeladvice" class:strip-local={generationProfile.local} data-tooltip="Studio generation guardrails">
          <span class="strip-label">
            {#if generationProfile.local}Local guardrails{:else}Builder guardrails{/if}
          </span>
          <span>
            {generationProfile.provider}/{generationProfile.model}
            {#if generationProfile.compact} · compact local mode{/if}
            {#if generationProfile.plan_matched} · deterministic plan{/if}
            {#if generationProfile.pattern_matched} · proven pattern{/if}
            {#if generationProfile.lessons_applied} · {generationProfile.lessons_applied} lesson{generationProfile.lessons_applied === 1 ? '' : 's'}{/if}
            · confidence {generationProfile.confidence || 'medium'}
          </span>
          {#if generationProfile.next_action === 'build_verify'}
            <span class="ma-rec">Recommended: run Build until it works before saving.</span>
          {:else if generationProfile.next_action === 'ask_clarify'}
            <span class="ma-rec">Recommended: refine the spec or run Build until it works.</span>
          {/if}
        </div>
      {/if}

      {#if compileError}
        <!-- Generation failures carry a multi-line, actionable fix (which builder
             model failed and how to fix it) — render it readably, not squashed
             onto one line. -->
        <div class="strip strip-error strip-multiline">
          <span class="strip-icon" aria-hidden="true">⚠</span>
          <span class="strip-body">{compileError}</span>
          <button class="strip-x" data-tooltip="Dismiss" on:click={() => (compileError = '')}>×</button>
        </div>
      {/if}

      {#if notes.length}
        <div class="strip strip-notes" data-tooltip="What the compiler inferred">
          <span class="strip-label">Inferred</span>
          {#each notes as n}<span class="note">{n}</span>{/each}
        </div>
      {/if}

      {#if workflow && workflow.recommendation && workflow.recommendation.mode}
        <div class="strip strip-reco" data-tooltip="Suggested execution model for this agent">
          <span class="strip-label">Recommended: {recoLabel(workflow.recommendation.mode)}</span>
          <span>{workflow.recommendation.rationale}</span>
          {#if workflow.recommendation.mode !== 'workflow' && currentMode === 'workflow'}
            <button class="btn ma-btn" type="button" disabled={compiling}
              on:click={() => switchMode(workflow.recommendation.mode)}>
              Rebuild as {recoLabel(workflow.recommendation.mode)}
            </button>
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
          <div class="strip strip-ok" data-tooltip="Workflow validates">
            <span class="strip-label">Valid</span>
            <span>No issues found.</span>
          </div>
        {:else}
          <div
            class="strip {validation.errors.length ? 'strip-error' : 'strip-warn'}"
            data-tooltip="Validation issues"
          >
            <span class="strip-label">Validation</span>
            {#if validation.errors.length}
              <span class="v-count v-err">{validation.errors.length} error{validation.errors.length === 1 ? '' : 's'}</span>
            {/if}
            {#if validation.warnings.length}
              <span class="v-count v-warn">{validation.warnings.length} warning{validation.warnings.length === 1 ? '' : 's'}</span>
            {/if}
            {#each validation.errors as err}
              <span class="v-msg v-err" data-tooltip={err.nodeId || (err.edgeIndex != null ? 'edge ' + err.edgeIndex : '')}>{err.message}</span>
            {/each}
            {#each validation.warnings as w}
              <span class="v-msg v-warn" data-tooltip={w.nodeId || ''}>{w.message}</span>
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
                    <button class="btn btn-sm cv-fixbtn" on:click={applyTemplateFixes} disabled={codeValidating || codeFixing}
                            data-tooltip="Instantly rewrite the flagged references to the suggested field">
                      ⚡ Quick-fix {codeValidation.fixes.length}
                    </button>
                  {/if}
                  {#if codeValidation.errors || codeValidation.warnings}
                    <button class="btn btn-sm cv-fixbtn cv-aibtn" on:click={fixWithAI} disabled={codeFixing || codeValidating}
                            data-tooltip="Let the model rewrite the whole SOUL.yaml to fix every issue">
                      {codeFixing ? '✨ Fixing…' : '✨ Fix with AI'}
                    </button>
                  {/if}
                  <button class="icon-btn" data-tooltip="Dismiss" on:click={() => (codeValidation = null)}>✕</button>
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
      {:else if viewMode === 'plan'}
        <div class="intent-guide">
          <span class="intent-pill">Intent first</span>
          <strong>Review the plan before editing nodes.</strong>
          <span>Confirm when it runs, what work it performs, and where output goes; use Canvas only for advanced rewiring.</span>
        </div>
        <PlanView {workflow} onSelectNode={planSelectNode} onSave={() => save()} onAddPython={addSuggestedPython} onAddStep={addStepFromText} addStepBusy={addStepBusy} addStepMsg={addStepMsg} onUpdateNode={updateNodeConfig} testByNode={stepResultsByNode(testResult)} {saving} />
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
              <button class="agent-yaml-link" type="button" on:click={showCodeView}
                data-tooltip="Open the full SOUL.yaml — validate, AI-fix, and edit every field">{'</> Edit SOUL.yaml'}</button>
            </div>

            <label class="agent-field-label" for="agent-sys">System prompt (how the agent works)</label>
            <textarea id="agent-sys" class="agent-sys" rows="9" bind:value={workflow.system_prompt}></textarea>

            <label class="agent-field-label" for="agent-tools">Tools the agent may call (one per line — exact builtin or mcp__server__tool names)</label>
            <textarea id="agent-tools" class="agent-tools" rows="5"
              value={(workflow.tools || []).join('\n')}
              on:input={(e) => { workflow = { ...workflow, tools: e.target.value.split('\n') } }}
            ></textarea>

            <label class="agent-field-label" for="agent-skills">Skills the agent may use (one per line — deployed skill names)</label>
            <textarea id="agent-skills" class="agent-tools" rows="4"
              value={(workflow.skills || []).join('\n')}
              on:input={(e) => { workflow = { ...workflow, skills: e.target.value.split('\n') } }}
            ></textarea>

            <div class="agent-spec-meta">
              {#if workflow.knowledge && workflow.knowledge.length}<span><strong>Knowledge:</strong> {workflow.knowledge.join(', ')}</span>{/if}
              {#if workflow.new_agents && workflow.new_agents.length}<span><strong>Peer agents:</strong> {workflow.new_agents.map(a => a.name || a.id).join(', ')}</span>{/if}
              {#if workflow.channels && workflow.channels.length}
                <span><strong>Delivers to:</strong>
                  {#each workflow.channels as c}<span class="dlv-chip">{c}<button class="dlv-x" type="button" data-tooltip="Remove delivery channel" on:click={() => removeDeliveryChannel(c)}>×</button></span>{/each}
                </span>
              {/if}
              {#if workflow.trigger && workflow.trigger.type}<span><strong>Runs:</strong> {workflow.trigger.type}{#if workflow.trigger.config && workflow.trigger.config.cron} ({workflow.trigger.config.cron}){/if}</span>{/if}
            </div>

            <!-- Try it: run the unsaved agent against one sample question -->
            <div class="agent-try">
              <label class="agent-field-label" for="agent-try-q">Try it — ask a sample question (runs the agent for real; the reply is shown here, not delivered to a channel)</label>
              <div class="agent-try-row">
                <input id="agent-try-q" class="agent-try-input" type="text" bind:value={tryQuestion}
                  placeholder="e.g. How has AAPL performed this quarter?"
                  on:keydown={(e) => { if (e.key === 'Enter') tryAgent() }} />
                <button class="btn primary" type="button" disabled={trying || !tryQuestion.trim()} on:click={tryAgent}>
                  {trying ? 'Running…' : 'Run'}
                </button>
              </div>
              {#if trying}
                <div class="agent-try-running"><span class="live-dot"></span> Running the agent — reasoning over its skills…</div>
              {/if}
              {#if tryResult}
                <div class="agent-try-result" class:err={!!tryResult.error}>
                  {#if tryResult.error}<div class="agent-try-err">⚠ {tryResult.error}</div>{/if}
                  {#if tryResult.reply}<div class="agent-try-reply">{tryResult.reply}</div>{/if}
                  {#if !tryResult.reply && !tryResult.error}<div class="agent-try-reply muted">(no text reply)</div>{/if}
                </div>
                {#if tryResult.nodeTrace && tryResult.nodeTrace.length}
                  <div class="agent-try-trace">
                    <div class="att-label">Every node that ran ({tryResult.nodeTrace.length}) — click a node for its input/output</div>
                    <ol class="att-list">
                      {#each tryResult.nodeTrace as n, i}
                        <li class="att-item" class:err={n.error}>
                          <button class="att-row" type="button" on:click={() => { n._open = !n._open; tryResult = tryResult }} title="Show input & output">
                            <span class="att-n">{i + 1}</span>
                            <span class="att-dot">{n.skipped ? '⏭' : n.error ? '✕' : '✓'}</span>
                            <span class="node-kind kind-{n.kind}">{n.kind}</span>
                            <span class="att-name">{n.node_id}</span>
                            {#if n.skipped}<span class="node-skip">skipped · needs consent</span>{/if}
                            <span class="att-caret">{n._open ? '▾' : '▸'}</span>
                          </button>
                          {#if n._open}
                            <div class="att-detail-box">
                              {#if n.input}<div class="att-kv"><span class="att-k">input</span><code>{n.input}</code></div>{/if}
                              {#if n.output}<div class="att-kv"><span class="att-k">output</span><code>{n.output}</code></div>{/if}
                              {#if n.error}<div class="att-kv"><span class="att-k">error</span><code>{n.error}</code></div>{/if}
                              {#if !n.input && !n.output && !n.error}<div class="att-kv muted">(no data captured)</div>{/if}
                            </div>
                          {/if}
                        </li>
                      {/each}
                    </ol>
                    <div class="att-hint">Branch nodes evaluate conditions and aren't listed as steps; the path taken is reflected by which nodes ran.</div>
                  </div>
                {/if}
                {#if tryResult.trace && tryResult.trace.length}
                  <div class="agent-try-trace">
                    <div class="att-label">Skills & tools it called ({tryResult.trace.length}) — click a step for details</div>
                    <ol class="att-list">
                      {#each tryResult.trace as t, i}
                        <li class="att-item" class:err={t.error}>
                          <button class="att-row" type="button" on:click={() => { t._open = !t._open; tryResult = tryResult }} title="Show arguments & result">
                            <span class="att-n">{i + 1}</span>
                            <span class="att-dot">{t.error ? '✕' : '✓'}</span>
                            <span class="att-name">{t.name}{#if t.detail} <span class="att-detail">→ {t.detail}</span>{/if}</span>
                            {#if t.args || t.result}<span class="att-caret">{t._open ? '▾' : '▸'}</span>{/if}
                          </button>
                          {#if t._open}
                            <div class="att-detail-box">
                              {#if t.args}<div class="att-kv"><span class="att-k">args</span><code>{t.args}</code></div>{/if}
                              {#if t.result}<div class="att-kv"><span class="att-k">result</span><code>{t.result}</code></div>{/if}
                            </div>
                          {/if}
                        </li>
                      {/each}
                    </ol>
                  </div>
                {:else if !tryResult.error}
                  <div class="att-label muted">No skills or tools were called (answered directly).</div>
                {/if}
              {/if}
            </div>
          </div>
        {:else}
          <SvelteFlow
            {nodes}
            {edges}
            {nodeTypes}
            {edgeTypes}
            fitView
            {isValidConnection}
            onconnect={onConnect}
            on:nodeclick={onNodeClick}
            on:edgeclick={onEdgeClick}
            on:paneclick={() => { selectedNode = null; selectedEdge = null }}
          >
            <Background />
            <Controls />
            <MiniMap pannable zoomable />
          </SvelteFlow>
          <!-- Maximize / restore the canvas frame -->
          <button class="frame-max canvas-max" type="button"
            data-tooltip={maximizedFrame === 'canvas' ? 'Restore layout (Esc)' : 'Maximize canvas'}
            on:click={() => toggleMax('canvas')}>{maximizedFrame === 'canvas' ? '⤡' : '⤢'}</button>
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
          <!-- Workflow-only bench controls. Reasoning agents are exercised via the
               "Try it" panel in the agent editor, not the flow test bench. -->
          {#if viewMode === 'canvas' && currentMode === 'workflow'}
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
              data-tooltip="Live runs aren’t available for an unsaved draft — save & enable the agent and exercise it via its channel."
              aria-label="Live runs aren’t available for an unsaved draft — save & enable the agent and exercise it via its channel."
              type="button"
            >Live</button>
          </div>
          <button class="btn" on:click={runTest} disabled={testing}>
            {testing ? 'Testing…' : 'Test'}
          </button>
          <button class="btn" on:click={tryAgent} disabled={trying || !(workflow.flow && (workflow.flow.nodes || []).length)}
            data-tooltip="Run the workflow for real with the sample input and show the result + tool trace">
            {trying ? 'Running…' : '▶ Run live'}
          </button>
          <button
            class="btn architect"
            on:click={buildUntilWorks}
            disabled={building || !workflow}
            data-tooltip="Autonomously fill gaps, fix every error, and run it until it works"
          >
            {building ? 'Building…' : '🛠 Build until it works'}
          </button>
          {#if plan && plan.tier}
            <span
              class="tier-chip tier-{plan.tier}"
              data-tooltip={(plan.reasons && plan.reasons.length) ? plan.reasons.join('; ') : 'capability tier'}
            >
              {tierLabel(plan.tier)}
            </span>
          {/if}
          <label class="unattended-toggle" data-tooltip="Let this agent's system/network steps run automatically on scheduled runs, with no approval prompt. Only enable if you trust the steps.">
            <input
              type="checkbox"
              checked={!!(workflow && workflow.unattended)}
              on:change={(e) => { if (workflow) workflow = { ...workflow, unattended: e.target.checked } }}
            />
            Unattended
          </label>
          <button class="btn btn-sm view-toggle" type="button" on:click={toggleTests}
                  data-tooltip="Show or hide the test & self-heal panels below the canvas">
            {showTests ? 'Hide tests' : 'Show tests'}
          </button>
          <button class="btn btn-sm view-toggle" type="button" on:click={toggleInspector}
                  data-tooltip="Show or hide the inspector panel">
            {showInspector ? 'Hide inspector' : 'Show inspector'}
          </button>
          {/if}
          {#if viewMode === 'code'}
            <button class="btn" on:click={validateCode} disabled={codeValidating}>
              {codeValidating ? 'Validating…' : '✓ Validate'}
            </button>
            <button class="btn" on:click={reviewWithAI} disabled={reviewing}
                    data-tooltip="Ask the model to review the YAML against your rules (catches judgment-call issues the linter can't)">
              {reviewing ? 'Reviewing…' : '🔍 AI review'}
            </button>
          {/if}
          <button class="btn primary"
                  on:click={() => (viewMode === 'code' ? saveFromCode() : save())}
                  disabled={saving || (viewMode === 'canvas' && (!!consent || !!preflight || (securityReview && (securityReview.blockers || []).length > 0)))}
                  title={securityReview && (securityReview.blockers || []).length > 0 ? 'Fix security blockers to save' : ''}>
            {saving ? 'Saving…' : (viewMode === 'code' ? 'Save YAML' : 'Save')}
          </button>
        </div>

        {#if saveMsg}<div class="strip strip-ok">✓ {saveMsg}</div>{/if}
        {#if saveError}<div class="strip strip-error">⚠ {saveError}</div>{/if}
        <!-- F-GUI-3 — visible reminder that mirrors the pre-save contract gate,
             so operators aren't surprised when Save is disabled. -->
        {#if viewMode === 'canvas' && securityReview && (securityReview.blockers || []).length > 0}
          <div class="strip strip-error">⚠ Fix security blockers to save — {(securityReview.blockers || []).length} pending.</div>
        {/if}

        <!-- F-GUI-3 — Security review panel. Always visible while editing a
             workflow so the trust / network / file / channel / privileged /
             confirmation summary is one glance away, and the recommendations
             carry the "Apply" quick-actions for the unambiguous replacements
             (write_file → kb_write is auto-appliable; shell_exec / http_request
             recommendations point at a general fix and surface as a toast).
             Structured to match the Contract panel's visual weight. -->
        {#if viewMode === 'canvas' && workflow}
          <!-- a11y note: refresh is a sibling <button>, NOT nested inside the
               header toggle, because nested interactive elements are invalid
               HTML and cause the Svelte a11y linter to flag click-only
               <span>s. Two flex-row buttons keep both keyboard + screen
               reader semantics correct. -->
          <div class="security-panel" class:has-blockers={(securityReview?.blockers || []).length > 0} class:has-warnings={(securityReview?.warnings || []).length > 0}>
            <div class="security-head-row">
              <button class="security-head" type="button" aria-expanded={securityPanelOpen}
                      on:click={() => securityPanelOpen = !securityPanelOpen}>
                <span class="security-caret">{securityPanelOpen ? '▾' : '▸'}</span>
                <strong>Security review</strong>
                {#if securityLoading}
                  <span class="security-count muted">refreshing…</span>
                {:else if !securityReview}
                  <span class="security-count muted">no report yet</span>
                {:else}
                  <span class="security-count {securityReview.ok ? 'ok' : ''}">
                    {(securityReview.blockers || []).length} blocker{(securityReview.blockers || []).length === 1 ? '' : 's'}
                    · {(securityReview.warnings || []).length} warning{(securityReview.warnings || []).length === 1 ? '' : 's'}
                    · {(securityReview.recommendations || []).length} rec{(securityReview.recommendations || []).length === 1 ? '' : 's'}
                  </span>
                {/if}
              </button>
              <button class="security-refresh" type="button"
                      data-tooltip="Re-run security review"
                      aria-label="Re-run security review"
                      on:click={refreshSecurityReview}>↺</button>
            </div>
            {#if securityPanelOpen}
              {#if securityError}
                <div class="strip strip-error" style="margin-top:.35rem;">⚠ {securityError}</div>
              {/if}
              {#if securityReview}
                {@const s = securityReview.summary || {}}
                <div class="security-summary">
                  <div><span>Untrusted sources</span><code>{(s.untrusted_content_sources || []).join(', ') || '—'}</code></div>
                  <div><span>Network</span><code>{(s.network_tools || []).join(', ') || '—'}</code></div>
                  <div><span>File</span><code>{(s.file_tools || []).join(', ') || '—'}</code></div>
                  <div><span>Channel</span><code>{(s.channel_tools || []).join(', ') || '—'}</code></div>
                  <div><span>Privileged</span><code>{(s.privileged_tools || []).join(', ') || '—'}</code></div>
                  <div><span>Confirm gates</span><code>{(s.confirm_tools || []).join(', ') || '—'}</code></div>
                  <div><span>Intent gate</span><code>{s.intent_gate_mode || 'prompt (default)'}</code></div>
                  <div>
                    <span>Privileged channel exposure</span>
                    <code>{s.privileged_channel_exposure ? 'yes — needs ack' : 'no'}</code>
                  </div>
                </div>
                {#if (securityReview.blockers || []).length}
                  <div class="security-block-list">
                    <div class="security-block-hdr">Blockers ({securityReview.blockers.length})</div>
                    {#each securityReview.blockers as b}
                      <div class="security-item block">
                        <div class="security-cat">{b.category}</div>
                        <div class="security-msg">{b.message}</div>
                        {#if b.fix}<div class="security-fix">→ {b.fix}</div>{/if}
                      </div>
                    {/each}
                  </div>
                {/if}
                {#if (securityReview.warnings || []).length}
                  <div class="security-block-list">
                    <div class="security-block-hdr">Warnings ({securityReview.warnings.length})</div>
                    {#each securityReview.warnings as w}
                      <div class="security-item warn">
                        <div class="security-cat">{w.category}</div>
                        <div class="security-msg">{w.message}</div>
                        {#if w.fix}<div class="security-fix">→ {w.fix}</div>{/if}
                      </div>
                    {/each}
                  </div>
                {/if}
                {#if (securityReview.recommendations || []).length}
                  <div class="security-block-list">
                    <div class="security-block-hdr">Recommendations ({securityReview.recommendations.length})</div>
                    {#each securityReview.recommendations as rec}
                      <div class="security-item rec">
                        <div class="security-cat">rewrite</div>
                        <div class="security-msg">Replace <code>{rec.from}</code> with <code>{rec.suggest}</code></div>
                        {#if rec.reason}<div class="security-fix">{rec.reason}</div>{/if}
                        <button class="btn btn-sm security-apply" type="button" on:click={() => applySecurityRecommendation(rec)}>Apply</button>
                      </div>
                    {/each}
                  </div>
                {/if}
              {/if}
            {/if}
          </div>
        {/if}

        {#if viewMode === 'canvas' && currentMode === 'workflow'}
          <!-- Drag this splitter to give the bottom workbench more/less height. -->
          <div class="wb-splitter" role="separator" aria-orientation="horizontal"
            data-tooltip="Drag to resize the workbench" on:pointerdown={startBenchResize}></div>
        {/if}
        <div class="workbench" style={maximizedFrame === 'bench' ? '' : `max-height:${benchHeight}px`}>
          {#if viewMode === 'canvas' && currentMode === 'workflow'}
            <div class="wb-bar">
              <span class="wb-title">Workbench</span>
              <button class="frame-max" type="button"
                data-tooltip={maximizedFrame === 'bench' ? 'Restore layout (Esc)' : 'Maximize workbench'}
                on:click={() => toggleMax('bench')}>{maximizedFrame === 'bench' ? '⤡' : '⤢'}</button>
            </div>
          {/if}

        <!-- ── Live run result (real run with the sample input) ───────────── -->
        {#if currentMode === 'workflow' && (trying || tryResult)}
          <div class="panel build-progress">
            {#if trying}
              <div class="agent-try-running"><span class="live-dot"></span> Running the workflow with your sample input…</div>
            {/if}
            {#if tryResult}
              <div class="agent-try-result" class:err={!!tryResult.error}>
                {#if tryResult.error}
                  <div class="agent-try-err">⚠ {tryResult.error}</div>
                  <!-- Plain-English explanation + suggested fix (Story 6). -->
                  {@const ex = explainPythonError(tryResult.error)}
                  <div class="py-explain">
                    <div class="py-explain-what">{ex.summary}</div>
                    <div class="py-explain-fix">💡 {ex.fix}</div>
                  </div>
                  <div class="try-fix-row">
                    <button
                      class="btn btn-sm cv-fixbtn cv-aibtn"
                      type="button"
                      on:click={troubleshootLiveRun}
                      disabled={troubleshooting || !workflow}
                      data-tooltip="Ask Studio to repair the workflow using this live error and the steps it observed"
                    >
                      {troubleshooting ? 'Fixing…' : '✨ Self-correct workflow'}
                    </button>
                    <span class="try-fix-hint">Uses this run error and trace, then reloads a repaired draft.</span>
                  </div>
                {/if}
                {#if tryResult.reply}<div class="agent-try-reply">{tryResult.reply}</div>{/if}
                {#if !tryResult.reply && !tryResult.error}<div class="agent-try-reply muted">(no text result)</div>{/if}
              </div>
              {#if tryResult.nodeTrace && tryResult.nodeTrace.length}
                <div class="agent-try-trace">
                  <div class="att-label">Every node that ran ({tryResult.nodeTrace.length}) — click a node for its input/output</div>
                  <ol class="att-list">
                    {#each tryResult.nodeTrace as n, i}
                      {@const soft = nodeSoftError(n)}
                      <li class="att-item" class:err={n.error} class:soft={!n.error && soft}>
                        <button class="att-row" type="button" on:click={() => { n._open = !n._open; tryResult = tryResult }} title="Show input & output">
                          <span class="att-n">{i + 1}</span>
                          <span class="att-dot">{n.skipped ? '⏭' : n.error ? '✕' : soft ? '⚠' : '✓'}</span>
                          <span class="node-kind kind-{n.kind}">{n.kind}</span>
                          <span class="att-name">{n.node_id}</span>
                          {#if n.skipped}<span class="node-skip">skipped · needs consent</span>{/if}
                          {#if n.adapted}<span class="node-adapted" data-tooltip="The runtime salvaged this node with the LLM because the input shape was unexpected. The output is a reconstruction — consider fixing the node so it parses the real shape.">✦ auto-adapted</span>{/if}
                          {#if !n.error && soft && !n.adapted}<span class="node-skip soft">ran, but output reports an error</span>{/if}
                          <span class="att-caret">{n._open ? '▾' : '▸'}</span>
                        </button>
                        {#if n._open}
                          <div class="att-detail-box">
                            {#if (n.input_full || n.input)}
                              <div class="att-kv">
                                <span class="att-k">input <button class="copy-mini" type="button" on:click|stopPropagation={() => copyValue(n.input_full || n.input, 'in'+i)}>{copiedKey==='in'+i ? 'copied' : 'copy'}</button></span>
                                <code class="full">{n.input_full || n.input}</code>
                              </div>
                            {/if}
                            {#if (n.output_full || n.output)}
                              <div class="att-kv">
                                <span class="att-k">output <button class="copy-mini" type="button" on:click|stopPropagation={() => copyValue(n.output_full || n.output, 'out'+i)}>{copiedKey==='out'+i ? 'copied' : 'copy'}</button></span>
                                <code class="full">{n.output_full || n.output}</code>
                              </div>
                            {/if}
                            {#if soft && !n.error}<div class="att-kv"><span class="att-k">reported error</span><code>{soft}</code></div>{/if}
                            {#if n.error}<div class="att-kv"><span class="att-k">error</span><code>{n.error}</code></div>{/if}
                            {#if !n.input && !n.output && !n.error}<div class="att-kv muted">(no data captured)</div>{/if}
                          </div>
                        {/if}
                      </li>
                    {/each}
                  </ol>
                  <div class="att-hint">Branch nodes evaluate conditions and aren't listed; the path taken is reflected by which nodes ran.</div>
                </div>
              {/if}

              <!-- ── Observe-and-adjust: fix a node against the REAL output ──── -->
              {#if liveHasFailure}
                <div class="live-adjust">
                  <div class="try-fix-row">
                    <button
                      class="btn btn-sm cv-fixbtn"
                      type="button"
                      on:click={adjustToLiveOutput}
                      disabled={repairing || !workflow}
                      data-tooltip="Have Studio observe what each node actually returned and propose a per-node adjustment"
                    >
                      {repairing ? 'Analyzing the run…' : '🔎 Adjust to real output'}
                    </button>
                    <span class="try-fix-hint">Observes the actual node outputs and proposes targeted fixes for review.</span>
                  </div>

                  {#if repairError}<div class="agent-try-err">⚠ {repairError}</div>{/if}
                  {#if repairDone}<div class="adjust-done">✓ {repairDone}</div>{/if}

                  {#each repairProposals as p (p.node_id + p.field)}
                    <div class="adjust-card" class:auto={p.auto}>
                      <div class="adjust-head">
                        <span class="node-kind">{p.node_id}</span>
                        <span class="adjust-class">{(p.class || '').replace('_',' ')}</span>
                        {#if p.auto}<span class="adjust-badge">safe / deterministic</span>{/if}
                      </div>
                      <div class="adjust-rationale">{p.rationale}</div>
                      {#if p.observed_keys && p.observed_keys.length}
                        <div class="adjust-observed">The API actually returned: <code>{p.observed_keys.join(', ')}</code></div>
                      {/if}
                      {#if p.new}
                        <div class="adjust-diff">
                          {#if p.old}<div class="diff-old"><span class="diff-tag">was</span><code>{p.old}</code></div>{/if}
                          <div class="diff-new"><span class="diff-tag">now</span><code>{p.new}</code></div>
                        </div>
                        <div class="adjust-actions">
                          <button class="btn btn-sm btn-primary" type="button" on:click={() => applyProposal(p)} disabled={p._applying}>
                            {p._applying ? 'Applying…' : 'Approve & apply'}
                          </button>
                          <button class="btn btn-sm" type="button" on:click={() => previewProposal(p)} disabled={p._applying || repairDiffBusy}>
                            {repairDiffBusy && repairDiff?.p === p ? 'Diffing…' : 'Preview diff'}
                          </button>
                          <button class="btn btn-sm" type="button" on:click={() => rejectProposal(p)} disabled={p._applying}>Dismiss</button>
                        </div>
                        {#if repairDiff && repairDiff.p === p}
                          <div class="repair-diff">
                            <div class="repair-diff-head">
                              SOUL.yaml changes <span class="rd-stat add">+{repairDiff.stats.added}</span> <span class="rd-stat del">−{repairDiff.stats.removed}</span>
                            </div>
                            <pre class="repair-diff-body">{#each repairDiff.lines as l}<span class="rd-{l.op === '+' ? 'add' : l.op === '-' ? 'del' : 'ctx'}">{l.op} {l.text}
</span>{/each}</pre>
                            <div class="adjust-actions">
                              <button class="btn btn-sm btn-primary" type="button" on:click={applyDiff}>Apply this change</button>
                              <button class="btn btn-sm" type="button" on:click={cancelDiff}>Cancel</button>
                            </div>
                          </div>
                        {/if}
                      {:else}
                        <div class="adjust-advisory">No automatic rewrite — adjust this node manually using the observed shape above.</div>
                        <div class="adjust-actions">
                          <button class="btn btn-sm" type="button" on:click={() => rejectProposal(p)}>Dismiss</button>
                        </div>
                      {/if}
                    </div>
                  {/each}
                </div>
              {/if}

              {#if tryResult.trace && tryResult.trace.length}
                <div class="agent-try-trace">
                  <div class="att-label">Tool calls ({tryResult.trace.length}) — click for details</div>
                  <ol class="att-list">
                    {#each tryResult.trace as t, i}
                      <li class="att-item" class:err={t.error}>
                        <button class="att-row" type="button" on:click={() => { t._open = !t._open; tryResult = tryResult }}>
                          <span class="att-n">{i + 1}</span>
                          <span class="att-dot">{t.error ? '✕' : '✓'}</span>
                          <span class="att-name">{t.name}{#if t.detail} <span class="att-detail">→ {t.detail}</span>{/if}</span>
                          {#if t.args || t.result}<span class="att-caret">{t._open ? '▾' : '▸'}</span>{/if}
                        </button>
                        {#if t._open}
                          <div class="att-detail-box">
                            {#if t.args}<div class="att-kv"><span class="att-k">args</span><code>{t.args}</code></div>{/if}
                            {#if t.result}<div class="att-kv"><span class="att-k">result</span><code>{t.result}</code></div>{/if}
                          </div>
                        {/if}
                      </li>
                    {/each}
                  </ol>
                </div>
              {/if}
            {/if}
          </div>
        {/if}

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

        <!-- ── Story 9 M (Cohort C): streamed generate pipeline transcript ──
             Hidden while the progress modal is on screen (it shows the same
             phases); reappears as the post-run summary, or as the live
             transcript if the user ran the modal in the background. -->
        {#if (pipelineRunning || pipelineLog.length) && !(pipelineRunning && !pipelineModalHidden)}
          <div class="panel build-progress">
            <div class="build-head">
              {#if pipelineRunning}<span class="spinner" aria-hidden="true"></span>{/if}
              <span class="build-summary">
                {pipelineRunning ? 'Generate pipeline running…' : 'Generate pipeline finished — see the canvas.'}
              </span>
              {#if !pipelineRunning}
                <button class="icon-btn" data-tooltip="Clear transcript" on:click={() => (pipelineLog = [])} style="margin-left:auto;">✕</button>
              {/if}
            </div>
            {#if pipelineLog.length}
              <ul class="build-live">
                {#each pipelineLog as ev}
                  <li class="live-line live-{ev.status}">
                    <span class="live-n">{ev.phase}</span>
                    <span class="live-status">{ev.status}</span>
                    {#if ev.message}<span>{ev.message}</span>{/if}
                  </li>
                {/each}
              </ul>
            {/if}
          </div>
        {/if}

        <!-- ── Architect build report: what was wrong, and how it was fixed ── -->
        {#if buildReport}
          <Collapsible id="build-report" data-tooltip="Build report" sub={buildReport.summary}>
            <svelte:fragment slot="actions">
              <span class="build-badge" class:ok={buildReport.ok}>
                {buildReport.verified ? '✓ Verified by running it' : buildReport.ok ? '✓ Validated' : '⚠ Needs attention'}
              </span>
              <button class="icon-btn" data-tooltip="Inspect every step of this build" on:click={() => (showBuildInspector = true)}>🔍 Inspect</button>
              <button class="icon-btn" data-tooltip="Dismiss" on:click={() => (buildReport = null)}>✕</button>
            </svelte:fragment>
            {#if buildGlue && buildGlue.length}
              <ul class="build-glue">
                {#each buildGlue as g}<li>🧩 {g}</li>{/each}
              </ul>
            {/if}
            {#if buildReport.contract}
              <div class="contract-panel" class:ok={buildReport.contract.ok}>
                <div class="contract-head">
                  <div>
                    <div class="contract-title">Studio contract</div>
                    <div class="contract-summary">{buildReport.contract.summary}</div>
                  </div>
                  <span class="contract-score" class:ok={buildReport.contract.ok}>
                    {buildReport.contract.score}/100
                  </span>
                </div>
                {#if buildReport.contract.checks && buildReport.contract.checks.length}
                  <ul class="contract-checks">
                    {#each buildReport.contract.checks as c}
                      <li class="contract-check" class:pass={c.status === 'pass'} class:warn={c.status === 'warn'} class:block={c.status === 'block'}>
                        <span class="contract-mark">{c.status === 'pass' ? '✓' : c.status === 'warn' ? '!' : '✕'}</span>
                        <div class="contract-copy">
                          <div class="contract-check-title">{c.title}</div>
                          <div class="contract-message">{c.message}</div>
                          {#if c.fix}<div class="contract-fix">{c.fix}</div>{/if}
                        </div>
                        {#if c.nodeId}
                          <button class="contract-open" type="button" on:click={() => revealNode(c.nodeId)}>Open</button>
                        {/if}
                      </li>
                    {/each}
                  </ul>
                {/if}
              </div>
            {/if}
            {#if buildReport.needs_external && buildReport.needs_external.length}
              <div class="build-external">
                <strong>Needs your input:</strong>
                <ul>{#each buildReport.needs_external as n}<li>🔑 {n}</li>{/each}</ul>
              </div>
            {/if}
            {#if buildReport.changes && buildReport.changes.length}
              <div class="build-changes">
                <strong>What Studio changed:</strong>
                <ul>{#each buildReport.changes as ch}<li>✎ {ch}</li>{/each}</ul>
              </div>
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
            {#if buildReport.diagnosis}
              <div class="build-diagnosis">
                <div class="bd-text">💡 {buildReport.diagnosis}</div>
                {#if buildReport.suggest_mode}
                  <button class="bd-action" type="button" disabled={compiling}
                    on:click={() => switchMode(buildReport.suggest_mode)}>
                    Rebuild as {recoLabel(buildReport.suggest_mode)}
                  </button>
                {/if}
              </div>
            {/if}
          </Collapsible>
        {/if}

        {#if showTests && viewMode === 'canvas' && currentMode === 'workflow'}
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
                        data-tooltip={fr.healable ? 'Diagnose and repair the saved agent' : 'The agent for this run no longer exists'}
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
                    <span>
                      ✓ Healed “{healResult.agentName || healResult.agentId}”. Preview the change below and click <em>Apply this fix</em> to load it above; Save to persist.
                      {#if healResult.report && healResult.report.verified} The fix was verified by re-running it.{/if}
                    </span>
                  {:else}
                    <span>No automatic fix found for: {healResult.error}</span>
                  {/if}
                </div>
                {#if healDiffBusy}
                  <div class="repair-diff">
                    <div class="repair-diff-head">Computing before/after diff…</div>
                  </div>
                {:else if healDiff}
                  <div class="repair-diff">
                    <div class="repair-diff-head">
                      SOUL.yaml changes <span class="rd-stat add">+{healDiff.stats.added}</span> <span class="rd-stat del">−{healDiff.stats.removed}</span>
                    </div>
                    {#if healDiff.lines.length}
                      <pre class="repair-diff-body">{#each healDiff.lines as l}<span class="rd-{l.op === '+' ? 'add' : l.op === '-' ? 'del' : 'ctx'}">{l.op} {l.text}
</span>{/each}</pre>
                    {:else}
                      <div class="adjust-advisory">Diff service was unavailable, but the healed workflow is ready to apply.</div>
                    {/if}
                    <div class="adjust-actions">
                      <button class="btn btn-sm btn-primary" type="button" on:click={applyHealDiff}>Apply this fix</button>
                      <button class="btn btn-sm" type="button" on:click={cancelHealDiff}>Cancel</button>
                    </div>
                  </div>
                {/if}
                {#if healResult.sessionId || healResult.evidence}
                  <div class="debug-evidence">
                    <div class="debug-evidence-head">
                      <span class="failed-agent">{healResult.agentName || healResult.agentId}</span>
                      {#if healResult.sessionId}<code>{healResult.sessionId}</code>{/if}
                    </div>
                    {#if healResult.error}<div class="debug-evidence-error">{healResult.error}</div>{/if}
                    {#if healResult.evidence}
                      <details>
                        <summary>Action-log evidence used by Studio</summary>
                        <pre>{healResult.evidence}</pre>
                      </details>
                    {/if}
                  </div>
                {/if}
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
            <div class="bench-head split">
              <button class="bench-head-toggle" type="button" on:click={() => (showAssertions = !showAssertions)}>
                <span class="caret">{showAssertions ? '▾' : '▸'}</span>
                <h3 class="panel-title">Assertions</h3>
                <span class="bench-sub">Check a node’s output or the final result after a run.</span>
              </button>
              <button class="btn btn-sm" type="button" on:click={addAssertion}>+ Add</button>
            </div>
            {#if showAssertions}
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
            {/if}
          </div>
        </div>

        <!-- Clarify panel -->
        {#if questions.length}
          <Collapsible id="clarify" data-tooltip="Clarify">
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
          </Collapsible>
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
          <Collapsible id="test-result" data-tooltip="Test result">
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
                  {@const toolErr = stepToolError(step)}
                  <li class:step-failed={!!toolErr}>
                    <span class="step-n">{i + 1}</span>
                    <div class="step-body">
                      <div class="step-head">
                        {#if toolErr}<span class="status-dot fail" data-tooltip="This step failed">✕</span>{:else}<span class="status-dot ok" data-tooltip="This step succeeded">✓</span>{/if}
                        <strong>{step.nodeId}</strong>
                        {#if step.kind}<span class="step-kind">{step.kind}</span>{/if}
                        {#if step.wiredPorts}<span class="wired-badge" data-tooltip="Input assembled from typed port wires — no template">⮑ wired</span>{/if}
                        {#if step.mocked}<span class="mock-badge" data-tooltip="Output was mocked, node was not run">mocked</span>{/if}
                        {#if step.durationMs != null}<span class="dur-badge" data-tooltip="Wall-clock duration">{step.durationMs}ms</span>{/if}
                      </div>
                      {#if toolErr}
                        <div class="step-line err">{toolErr}</div>
                      {:else if stepSummary(step)}
                        <div class="step-line ok">{stepSummary(step)}</div>
                      {/if}
                      <details class="step-details">
                        <summary>input / output</summary>
                        <div class="step-io">
                          <span class="io-label">in</span><pre>{prettyJSON(step.input)}</pre>
                          <span class="io-label">out</span><pre>{prettyJSON(step.output)}</pre>
                        </div>
                      </details>
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
          </Collapsible>
        {/if}

        <!-- ── Live run trace (Phase 1): the saved agent's last REAL run ──── -->
        {#if loadedAgentId}
          <Collapsible id="run-trace" data-tooltip="Run history" sub="Every run of this agent — scheduled or on-demand. Pick one to view its per-block trace.">
            <svelte:fragment slot="actions">
              <button class="btn btn-sm" type="button" on:click={() => loadRunTrace()} disabled={runTraceLoading}>
                {runTraceLoading ? 'Loading…' : '↻ Refresh'}
              </button>
            </svelte:fragment>
            {#if runHistory.length}
              <ul class="run-list">
                {#each runHistory as r (r.runId)}
                  <li>
                    <button class="run-row" class:active={selectedRunId === r.runId} type="button"
                      on:click={() => loadRunTrace(r.runId)}>
                      <span class="run-badge {r.ok ? 'ok' : 'fail'}">{r.ok ? '✓' : '✕'}</span>
                      <span class="run-when">{r.startedAt ? new Date(r.startedAt).toLocaleString() : '—'}</span>
                      {#if r.trigger}<span class="run-trigger">{r.trigger}</span>{/if}
                      <span class="run-steps">{r.steps} step{r.steps === 1 ? '' : 's'}</span>
                      {#if !r.ok && r.error}<span class="run-err">{r.error}</span>{/if}
                    </button>
                  </li>
                {/each}
              </ul>
            {/if}
            {#if runTraceErr}
              <p class="muted">{runTraceErr}</p>
            {/if}
            {#if runDiagnosis}
              <div class="diagnosis-card {runDiagnosis.status || 'empty'}">
                <div class="diagnosis-head">
                  <span class="diagnosis-badge">{runDiagnosis.status || 'unknown'}</span>
                  <strong>{runDiagnosis.summary}</strong>
                </div>
                {#if runDiagnosis.rootCause}
                  <div class="diagnosis-row">
                    <span>Likely cause</span>
                    <p>{runDiagnosis.rootCause}</p>
                  </div>
                {/if}
                {#if runDiagnosis.nextAction}
                  <div class="diagnosis-row">
                    <span>Next action</span>
                    <p>{runDiagnosis.nextAction}</p>
                  </div>
                {/if}
                {#if runDiagnosis.failedNode || runDiagnosis.failedKind}
                  <div class="diagnosis-meta">
                    {#if runDiagnosis.failedNode}<span>Node <code>{runDiagnosis.failedNode}</code></span>{/if}
                    {#if runDiagnosis.failedKind}<span>Kind <code>{runDiagnosis.failedKind}</code></span>{/if}
                    {#if runDiagnosis.retryable}<span>Retry may help</span>{:else if runDiagnosis.status === 'failed'}<span>Needs a workflow/config fix</span>{/if}
                  </div>
                {/if}
                {#if runDiagnosis.suggestions && runDiagnosis.suggestions.length}
                  <ul class="diagnosis-list">
                    {#each runDiagnosis.suggestions as s}
                      <li>{s}</li>
                    {/each}
                  </ul>
                {/if}
                {#if runDiagnosis.evidence && runDiagnosis.evidence.length}
                  <details class="diagnosis-evidence">
                    <summary>Evidence</summary>
                    <ul>
                      {#each runDiagnosis.evidence as e}
                        <li>{e}</li>
                      {/each}
                    </ul>
                  </details>
                {/if}
              </div>
            {/if}
            {#if runTrace && runTrace.entries && runTrace.entries.length}
              <ol class="trace">
                {#each runTrace.entries as step, i}
                  {@const toolErr = stepToolError(step)}
                  <li class:step-failed={!!toolErr}>
                    <span class="step-n">{i + 1}</span>
                    <div class="step-body">
                      <div class="step-head">
                        {#if toolErr}<span class="status-dot fail" data-tooltip="This step failed">✕</span>{:else}<span class="status-dot ok" data-tooltip="This step succeeded">✓</span>{/if}
                        <strong>{step.nodeId}</strong>
                        {#if step.kind}<span class="step-kind">{step.kind}</span>{/if}
                        {#if step.wiredPorts}<span class="wired-badge" data-tooltip="Input assembled from typed port wires — no template">⮑ wired</span>{/if}
                        {#if step.durationMs != null}<span class="dur-badge" data-tooltip="Wall-clock duration">{step.durationMs}ms</span>{/if}
                      </div>
                      {#if toolErr}
                        <div class="step-line err">{toolErr}</div>
                      {:else if stepSummary(step)}
                        <div class="step-line ok">{stepSummary(step)}</div>
                      {/if}
                      <details class="step-details">
                        <summary>input / output</summary>
                        <div class="step-io">
                          <span class="io-label">in</span><pre>{prettyJSON(step.input)}</pre>
                          <span class="io-label">out</span><pre>{prettyJSON(step.output)}</pre>
                          {#if step.error}<span class="io-label">err</span><pre class="io-err">{step.error}</pre>{/if}
                        </div>
                      </details>
                    </div>
                  </li>
                {/each}
              </ol>
            {/if}
          </Collapsible>
        {/if}

        <!-- ── Run history (S5.4): last ~10 runs, IN MEMORY only ──────────── -->
        {#if history.length}
          <Collapsible id="run-history" data-tooltip="Run history" sub="Session-only — cleared on reload (no storage in the sandbox).">
            <svelte:fragment slot="actions">
              <button class="btn btn-sm" type="button" on:click={clearHistory}>Clear</button>
            </svelte:fragment>
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
          </Collapsible>
        {/if}
        {/if}
        </div><!-- /.workbench -->
      {/if}
    </section>

    {#if showInspector && viewMode === 'canvas' && currentMode === 'workflow'}
      <div
        class="insp-splitter"
        role="separator"
        aria-orientation="vertical"
        data-tooltip="Drag to resize the inspector"
        on:pointerdown={startInspResize}
      ></div>
      <div class="insp-host" style={maximizedFrame === 'inspector' ? '' : `width:${inspectorWidth}px`}>
        <button class="frame-max insp-max" type="button"
          data-tooltip={maximizedFrame === 'inspector' ? 'Restore layout (Esc)' : 'Maximize inspector'}
          on:click={() => toggleMax('inspector')}>{maximizedFrame === 'inspector' ? '⤡' : '⤢'}</button>
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
          {toolParams}
          {scaffolds}
          onGenerateCode={generateNodeCode}
          onCompileGate={compileGate}
          onCompileNode={compileNode}
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

  <!-- ── Browse SOUL.yaml: read-only viewer for every agent ────────────────── -->
  {#if yamlBrowser.open}
    <div class="modal-backdrop" on:click|self={closeYamlBrowser} role="presentation">
      <div class="modal yaml-browser" role="dialog" aria-modal="true" aria-labelledby="yamlb-title">
        <h2 id="yamlb-title" class="modal-title">Browse SOUL.yaml</h2>
        <p class="modal-body">The raw SOUL.yaml of any agent, read straight from disk. Read-only — edit on the canvas or in the code view.</p>
        {#if yamlBrowser.error}<div class="strip strip-error">⚠ {yamlBrowser.error}</div>{/if}
        {#if yamlBrowser.loading}
          <p class="muted">Loading agents…</p>
        {:else if !yamlBrowser.agents.length}
          <p class="muted">No agents registered yet.</p>
        {:else}
          <div class="yamlb-split">
            <ul class="picker-list yamlb-list">
              {#each yamlBrowser.agents as a (a.id)}
                <li class="picker-item">
                  <button class="picker-main" type="button" class:selected={yamlBrowser.selectedId === a.id}
                          on:click={() => viewAgentYaml(a.id)} title="View this agent's SOUL.yaml">
                    <span class="picker-name">
                      {a.name || a.id}
                      <span class="agent-badge {a.enabled ? 'on' : 'off'}">{a.enabled ? 'enabled' : 'disabled'}</span>
                    </span>
                    <span class="picker-desc">{a.id}</span>
                  </button>
                </li>
              {/each}
            </ul>
            <div class="yamlb-view">
              {#if yamlBrowser.yamlError}
                <div class="strip strip-error">⚠ {yamlBrowser.yamlError}</div>
              {:else if yamlBrowser.yamlLoading}
                <p class="muted">Loading SOUL.yaml…</p>
              {:else if yamlBrowser.yaml}
                {#if yamlBrowser.path}<div class="yamlb-path" data-tooltip={yamlBrowser.path}>{yamlBrowser.path}</div>{/if}
                <pre class="yamlb-code">{yamlBrowser.yaml}</pre>
              {:else}
                <p class="muted">Select an agent to view its SOUL.yaml.</p>
              {/if}
            </div>
          </div>
        {/if}
        <div class="modal-actions">
          <button class="btn" type="button" on:click={closeYamlBrowser}>Close</button>
        </div>
      </div>
    </div>
  {/if}

  <!-- Studio model picker: choose the in-framework provider/model for Studio. -->
  {#if modelPicker.open}
    <div class="modal-backdrop" on:click|self={() => modelPicker = { ...modelPicker, open: false }} role="presentation">
      <div class="modal model-modal" role="dialog" aria-modal="true" aria-labelledby="model-title">
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
        <span class="field-label">model</span>

        {#if !modelPicker.provider}
          <p class="mp-hint">Choose a provider above to see its models.</p>
        {:else}
          <!-- Filter only appears once there are enough models to be worth
               filtering; below that it is just a box in the way. -->
          {#if (modelPicker.models || []).length > 6}
            <input
              class="mp-filter"
              type="search"
              autocomplete="off"
              placeholder="Filter {modelPicker.models.length} models…"
              aria-label="Filter models"
              bind:value={modelFilter}
            />
          {/if}

          <div class="mp-list" role="radiogroup" aria-label="Model">
            <!-- Blank model = the provider's own default. It is a real choice,
                 so it belongs in the list rather than hidden in placeholder text. -->
            <button
              type="button"
              class="mp-row"
              class:sel={!modelPicker.model}
              role="radio"
              aria-checked={!modelPicker.model}
              on:click={() => chooseModel('')}
            >
              <span class="mp-tick">{!modelPicker.model ? '✓' : ''}</span>
              <span class="mp-name muted">Provider default</span>
            </button>

            {#each modelChoices as m}
              <button
                type="button"
                class="mp-row"
                class:sel={m === modelPicker.model}
                role="radio"
                aria-checked={m === modelPicker.model}
                on:click={() => chooseModel(m)}
              >
                <span class="mp-tick">{m === modelPicker.model ? '✓' : ''}</span>
                <span class="mp-name">{m}</span>
              </button>
            {/each}

            {#if (modelPicker.models || []).length === 0}
              <p class="mp-empty">This provider reported no models. Enter one by hand below.</p>
            {:else if modelChoices.length === 0}
              <p class="mp-empty">No model matches “{modelFilter.trim()}”.</p>
            {/if}
          </div>

          <!-- Escape hatch: a model the provider does not advertise. Collapsed
               by default so it does not compete with the list. -->
          {#if manualModel}
            <input
              class="mp-manual"
              autocomplete="off"
              placeholder="e.g. llama3.1:70b"
              aria-label="Model name"
              bind:value={modelPicker.model}
            />
          {:else}
            <button type="button" class="mp-link" on:click={() => { manualModel = true }}>
              Model not listed? Enter it manually
            </button>
          {/if}
        {/if}
        <div class="mp-settings-grid">
        {#if studioPresets.length}
          <div class="mp-section">
            <div class="mp-label" style="font-weight:600;margin-bottom:.25rem;">Runtime intent (optional)</div>
            <p class="mp-hint" style="margin:.25rem 0;font-size:.85em;opacity:.75;">
              Overrides the model-derived default timeouts. Leave on "Model default" if you don't need to bias for speed or quality.
            </p>
            <div class="mp-preset-list" style="display:flex;flex-direction:column;gap:.25rem;">
              <label class="mp-preset-row" style="display:flex;gap:.5rem;align-items:flex-start;">
                <input
                  type="radio"
                  name="studio-preset"
                  value=""
                  checked={studioPresetCurrent === ''}
                  on:change={() => saveStudioPreset('')}
                  disabled={studioPresetSaving}
                />
                <div>
                  <div><strong>Model default</strong></div>
                  <div style="font-size:.85em;opacity:.7;">Studio picks patient timeouts based on the model tier (compact vs capable).</div>
                </div>
              </label>
              {#each studioPresets as p}
                <label class="mp-preset-row" style="display:flex;gap:.5rem;align-items:flex-start;">
                  <input
                    type="radio"
                    name="studio-preset"
                    value={p.name}
                    checked={studioPresetCurrent === p.name}
                    on:change={() => saveStudioPreset(p.name)}
                    disabled={studioPresetSaving}
                  />
                  <div>
                    <div>
                      <strong>{p.label}</strong>
                      {#if p.default}<span style="font-size:.75em;opacity:.6;margin-left:.35rem;">(recommended)</span>{/if}
                    </div>
                    <div style="font-size:.85em;opacity:.7;">{p.detail}</div>
                  </div>
                </label>
              {/each}
            </div>
            {#if studioPresetError}
              <div class="mp-error" style="color:var(--err, #c33);font-size:.85em;margin-top:.25rem;">{studioPresetError}</div>
            {/if}
          </div>
        {/if}
        <div class="mp-section">
          <div class="mp-label" style="font-weight:600;margin-bottom:.25rem;">Generate presentation</div>
          <p class="mp-hint" style="margin:.25rem 0;font-size:.85em;opacity:.75;">
            How Studio surfaces the generate pipeline. Streamed shows a live
            transcript beside the canvas; Wizard opens a stepped modal that
            pauses between clarify → strategy → build. Per-generation override
            is available on the toggle next to the Generate button.
          </p>
          <div class="mp-preset-list" style="display:flex;flex-direction:column;gap:.25rem;">
            <label class="mp-preset-row" style="display:flex;gap:.5rem;align-items:flex-start;">
              <input type="radio" name="build-ux" value="streamed" checked={buildUX === 'streamed'} on:change={() => saveBuildUX('streamed')} disabled={buildUXSaving} />
              <div>
                <div><strong>Streamed</strong> <span style="font-size:.75em;opacity:.6;margin-left:.35rem;">(default)</span></div>
                <div style="font-size:.85em;opacity:.7;">One-click generate with a live-transcript panel that shows each pipeline phase as it runs.</div>
              </div>
            </label>
            <label class="mp-preset-row" style="display:flex;gap:.5rem;align-items:flex-start;">
              <input type="radio" name="build-ux" value="wizard" checked={buildUX === 'wizard'} on:change={() => saveBuildUX('wizard')} disabled={buildUXSaving} />
              <div>
                <div><strong>Wizard</strong></div>
                <div style="font-size:.85em;opacity:.7;">Stepped modal that pauses between phases so you can review the clarified intent and chosen strategy before the graph is built.</div>
              </div>
            </label>
          </div>
        </div>
        </div>
        <div class="modal-actions">
          <button class="btn" type="button" on:click={() => modelPicker = { ...modelPicker, open: false }} disabled={modelPicker.saving}>Cancel</button>
          <button class="btn primary" type="button" on:click={saveStudioModel} disabled={modelPicker.saving}>
            {modelPicker.saving ? 'Saving…' : 'Save'}
          </button>
        </div>
      </div>
    </div>
  {/if}

  <!-- Generate progress modal — a centered overlay with a phase stepper so the
       user can see what the (often slow) build pipeline is doing instead of
       staring at an empty canvas. Dismissible to run in the background. -->
  {#if genBusy && !pipelineModalHidden}
    <div class="modal-backdrop" role="presentation">
      <div class="modal gen-progress-modal" role="dialog" aria-modal="true"
           aria-labelledby="gen-progress-title" aria-live="polite">
        <h2 id="gen-progress-title" class="modal-title">Generating your workflow</h2>
        <div class="gen-status">
          <span class="spinner" aria-hidden="true"></span>
          <span>{genStatusText}</span>
        </div>
        {#if pipelineLog.length}
          <!-- Real events streamed by the build pipeline — the actual phase and
               the server's own message for it (refined spec, chosen strategy,
               node/agent counts, validation verdict, repairs). Newest last. -->
          <ul class="gen-events">
            {#each pipelineLog as ev}
              <li class="gen-ev gen-ev-{ev.status}">
                <span class="gen-ev-phase">{phaseLabel(ev.phase)}</span>
                {#if ev.message}<span class="gen-ev-msg">{ev.message}</span>{/if}
              </li>
            {/each}
          </ul>
        {:else}
          <p class="gen-latest">
            This calls the builder model ({studioModelLabel || 'your builder model'}). A
            complex prompt can take a minute — switch to Streamed mode to watch each
            phase as it runs.
          </p>
        {/if}
        <div class="modal-actions">
          <button class="btn" type="button" on:click={() => (pipelineModalHidden = true)}>Run in background</button>
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

        {#if promptError}<div class="strip strip-error">⚠ {promptError}</div>{/if}

        <label class="pe-label" for="pe-raw">Your prompt (original)</label>
        <p class="pe-hint">What you want, in your own words. Edit it and click <strong>Refine</strong> to turn it into a detailed, build-ready spec.</p>
        <textarea id="pe-raw" class="pe-area" bind:value={rawPrompt}
          placeholder="e.g. Every morning, find the top AI news, make a NotebookLM podcast, and post the link to Telegram."></textarea>
        <div class="pe-row">
          <button class="btn" on:click={refineFromModal} disabled={modalRefining || !rawPrompt.trim()}>
            {modalRefining ? 'Refining…' : '✨ Refine →'}
          </button>
        </div>

        <label class="pe-label" for="pe-refined">Refined prompt</label>
        <p class="pe-hint">The detailed specification Studio builds from. Edit it directly if you like, then <strong>Generate</strong>.</p>
        <textarea id="pe-refined" class="pe-area" bind:value={intent}
          placeholder="Appears here after you Refine — or write/paste a detailed prompt directly."></textarea>

        <div class="modal-actions">
          <button class="btn" on:click={() => (promptViewer = false)}>OK</button>
          <button class="btn primary" on:click={generateFromModal}
                  disabled={compiling || refining || modalRefining || !(intent.trim() || rawPrompt.trim())}>
            {compiling ? 'Generating…' : 'Generate from refined'}
          </button>
        </div>
      </div>
    </div>
  {/if}

  <!-- SOUL.yaml authoring rules editor -->
  {#if rulesOpen}
    <div class="modal-backdrop" on:click|self={() => (rulesOpen = false)} role="presentation">
      <div class="modal prompt-modal" role="dialog" aria-modal="true" aria-labelledby="rules-title">
        <h2 id="rules-title" class="modal-title">SOUL.yaml Rules</h2>
        <p class="pe-hint">
          These rules are auto-injected whenever Studio <strong>generates</strong>, and when you run <strong>Fix with AI</strong>.
          Edit or add rules, and list your tools' input/output shapes under “Tier 2”. Saved to your workspace.
          {#if rulesIsDefault}<em>(currently using the built-in default — saving creates your own copy)</em>{/if}
        </p>
        {#if rulesLoading}
          <div class="canvas-state">Loading…</div>
        {:else}
          <textarea class="pe-area rules-area" bind:value={rulesText} spellcheck="false" placeholder="Authoring rules…"></textarea>
        {/if}
        <div class="modal-actions">
          <button class="btn" on:click={resetRulesToDefault} data-tooltip="Replace the editor contents with the built-in default rules">Reset to default</button>
          <button class="btn" on:click={() => (rulesOpen = false)}>Close</button>
          <button class="btn primary" on:click={saveRules} disabled={rulesSaving || rulesLoading}>{rulesSaving ? 'Saving…' : 'Save'}</button>
          {#if rulesMsg}<span class="save-msg" class:ok={rulesMsg.startsWith('✓')}>{rulesMsg}</span>{/if}
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
          Studio checked this agent against your live setup and generation contract
          (tools, MCP servers, channels, schedule, required inputs, graph shape,
          and authoring rules).
        </p>

        {#if preflight.blockers && preflight.blockers.length}
          <div class="refine-section">
            <div class="refine-heading">Blockers ({preflight.blockers.length})</div>
            <ul class="pf-list">
              {#each preflight.blockers as b}
                <li>
                  {#if b.nodeId}
                    <button class="pf-item pf-block pf-clickable"
                        type="button"
                        on:click={() => revealNode(b.nodeId)}
                        title="Click to open this block on the canvas">
                      <div class="pf-msg">{b.message}</div>
                      {#if b.fix}<div class="pf-fix">→ {b.fix}</div>{/if}
                      <div class="pf-node">block: {b.nodeId} — click to open ↗</div>
                    </button>
                  {:else}
                    <div class="pf-item pf-block">
                      <div class="pf-msg">{b.message}</div>
                      {#if b.fix}<div class="pf-fix">→ {b.fix}</div>{/if}
                    </div>
                  {/if}
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
                <li>
                  {#if w.nodeId}
                    <button class="pf-item pf-warn pf-clickable"
                        type="button"
                        on:click={() => revealNode(w.nodeId)}
                        title="Click to open this block on the canvas">
                      <div class="pf-msg">{w.message}</div>
                      {#if w.fix}<div class="pf-fix">→ {w.fix}</div>{/if}
                      <div class="pf-node">block: {w.nodeId} — click to open ↗</div>
                    </button>
                  {:else}
                    <div class="pf-item pf-warn">
                      <div class="pf-msg">{w.message}</div>
                      {#if w.fix}<div class="pf-fix">→ {w.fix}</div>{/if}
                    </div>
                  {/if}
                </li>
              {/each}
            </ul>
          </div>
        {/if}

        <!-- F-GUI-3 — surface security recommendations inline in the preflight
             modal so operators can accept the rewrite without leaving the save
             flow. Only unambiguous tool-name suggestions get an Apply button
             (see applySecurityRecommendation). -->
        {#if preflight.security && (preflight.security.recommendations || []).length}
          <div class="refine-section">
            <div class="refine-heading">Security recommendations ({(preflight.security.recommendations || []).length})</div>
            <ul class="pf-list">
              {#each preflight.security.recommendations as rec}
                <li>
                  <div class="pf-item pf-warn">
                    <div class="pf-msg">Replace <code>{rec.from}</code> with <code>{rec.suggest}</code></div>
                    {#if rec.reason}<div class="pf-fix">{rec.reason}</div>{/if}
                    <button class="btn btn-sm" type="button" style="margin-top:.35rem;"
                            on:click={() => applySecurityRecommendation(rec)}>Apply</button>
                  </div>
                </li>
              {/each}
            </ul>
          </div>
        {/if}

        <div class="modal-actions">
          <button class="btn" on:click={cancelPreflight} disabled={saving || fixing}>Back to editing</button>
          <button class="btn" on:click={fixAutomatically} disabled={saving || fixing} data-tooltip="Auto-wire empty inputs and reconcile mismatched variable names">
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
        <h2 id="refine-title" class="modal-title">
          {effectiveBuildUX === 'wizard' ? 'Wizard — review each phase' : 'Confirm what to build'}
        </h2>
        {#if effectiveBuildUX === 'wizard'}
          <!--
            Story 9 M (Cohort C): wizard mode makes the pipeline phases visible
            in the existing refinement modal. The modal already covers phases
            1 (clarify) and 2 (strategy); phase 3 (build) fires on the Generate
            button. Validate + Repair are shown in the Build Report after
            generation runs. The transcript panel is available for a
            live-view companion in either mode.
          -->
          <div class="wizard-steps" style="display:flex;gap:.5rem;font-size:.85em;opacity:.85;margin-bottom:.5rem;">
            <span><strong>1.</strong> Clarify intent</span>
            <span aria-hidden="true">→</span>
            <span><strong>2.</strong> Choose strategy</span>
            <span aria-hidden="true">→</span>
            <span><strong>3.</strong> Build graph</span>
            <span aria-hidden="true">→</span>
            <span style="opacity:.55;"><strong>4.</strong> Validate</span>
            <span aria-hidden="true">→</span>
            <span style="opacity:.55;"><strong>5.</strong> Repair</span>
          </div>
        {/if}
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
            {#if refinement.recommended_reason} — {refinement.recommended_reason}{/if}
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
            {compiling ? 'Generating…' : (refinement.recommended_mode === 'auto' || refinement.recommended_mode === 'react' || refinement.recommended_mode === 'plan_execute') ? `Generate ${recoLabel(refinement.recommended_mode)} agent` : 'Generate workflow'}
          </button>
        </div>
      </div>
    </div>
  {/if}

  {#if agentRoute}
    <div class="modal-backdrop" on:click|self={() => (agentRoute = null)} role="presentation">
      <div class="modal agent-route-modal" role="dialog" aria-modal="true" aria-labelledby="agentroute-title">
        <h2 id="agentroute-title" class="modal-title">Built as a {recoLabel(agentRoute.mode)} agent</h2>
        <p class="modal-body">
          This task reasons over its tools — it decides which skill or tool to use
          per request — so it can't be expressed as a fixed workflow graph. Studio
          built it as a <strong>{recoLabel(agentRoute.mode)}</strong> agent instead
          and opened its <strong>SOUL.yaml</strong>, where the agent actually lives.
        </p>
        {#if agentRoute.reason}
          <div class="agent-route-reason">{agentRoute.reason}</div>
        {/if}
        <p class="modal-body agent-route-hint">
          Edit the prompt, tools, and skills in the SOUL.yaml below, then Save. To
          build a fixed flow instead, use the <strong>Workflow</strong> toggle.
        </p>
        <div class="modal-actions">
          <button class="btn" on:click={() => { agentRoute = null; switchMode('workflow') }} disabled={compiling}>
            Build as workflow anyway
          </button>
          <button class="btn primary" on:click={() => (agentRoute = null)}>
            Edit the SOUL.yaml
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

  <!-- Build inspector: the durable build transcript, every step made visible. -->
  <BuildInspector
    open={showBuildInspector}
    traceId={buildTraceId}
    on:frame={(e) => (replayOverride = e.detail)}
    on:close={() => { showBuildInspector = false; replayOverride = null }}
  />
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
  .intent-trigger { cursor: pointer; }
  .intent-trigger:hover { border-color: var(--accent); }


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

  /* ── Three resizable / maximizable frames: canvas · workbench · inspector ── */
  .insp-host { position: relative; }
  .insp-host :global(.inspector) { padding-top: 0; }

  /* Bottom workbench: a scrollable, height-resizable frame holding the panels. */
  .workbench {
    flex: 0 0 auto;
    display: flex;
    flex-direction: column;
    gap: 10px;
    overflow-y: auto;
    border-top: 1px solid var(--border);
    background: var(--bg-elev);
    padding: 0 14px 12px;
  }
  .wb-bar {
    position: sticky;
    top: 0;
    z-index: 2;
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 0 4px;
    background: var(--bg-elev);
  }
  .wb-title { font-size: 11px; text-transform: uppercase; letter-spacing: 0.05em; color: var(--text-muted); flex: 1; }

  .wb-splitter {
    flex: 0 0 auto;
    height: 6px;
    cursor: row-resize;
    background: var(--border);
    transition: background 120ms ease;
  }
  .wb-splitter:hover { background: var(--accent); }

  /* Maximize / restore buttons (corner of each frame). */
  .frame-max {
    border: 1px solid var(--border);
    background: var(--bg-elev-2);
    color: var(--text-muted);
    border-radius: 6px;
    width: 26px;
    height: 24px;
    font-size: 13px;
    cursor: pointer;
    flex: none;
  }
  .frame-max:hover { color: var(--text); border-color: var(--accent); }
  .canvas-max { position: absolute; top: 10px; right: 10px; z-index: 5; }
  .insp-max { position: absolute; top: 8px; right: 10px; z-index: 5; }

  /* Maximize states: one frame fills the editor, the others fold away. */
  .body.max-canvas .workbench, .body.max-canvas .wb-splitter,
  .body.max-canvas .insp-host, .body.max-canvas .insp-splitter { display: none; }
  /* Focus the canvas: maximizing hides not just the side/bottom frames but the
     rest of the surrounding chrome too — the palette, the explainer / builder-
     guardrail / validation strips, the "needs setup" cards and the security
     panel — so only the workflow graph (and its compact toolbar) remains.
     Restore everything with the ⤡ button on the canvas or the Esc key. */
  .body.max-canvas .center .strip,
  .body.max-canvas .center .explain-panel,
  .body.max-canvas .center .needs-setup,
  .body.max-canvas .center .security-panel { display: none; }
  /* Palette is a child component, so its root needs :global to match under the
     scoped .body.max-canvas ancestor. */
  .body.max-canvas :global(.palette) { display: none; }
  .body.max-bench .canvas, .body.max-bench .wb-splitter,
  .body.max-bench .insp-host, .body.max-bench .insp-splitter { display: none; }
  .body.max-bench .workbench { flex: 1 1 auto; max-height: none !important; }
  .body.max-inspector .center { display: none; }
  .body.max-inspector .insp-splitter { display: none; }
  .body.max-inspector .insp-host { flex: 1 1 auto; }
  .view-toggle { white-space: nowrap; }

  /* Toolbar row: view switch (left) + execution-mode switch (right) */
  .toolbar-row {
    display: flex; align-items: center; justify-content: space-between;
    gap: 12px; flex-wrap: wrap;
    padding: 8px 14px 0;
    flex-shrink: 0;
  }

  /* Canvas ⇄ Code view switch */
  .view-switch {
    display: flex; align-items: center; gap: 2px;
    flex-shrink: 0;
  }

  /* Workflow ⇄ ReAct ⇄ Plan-Execute execution-mode switch */
  .mode-switch {
    display: flex; align-items: center; gap: 2px;
    flex-shrink: 0;
  }
  .ms-label {
    font-size: 10px; font-weight: 700; letter-spacing: 0.08em;
    text-transform: uppercase; color: var(--text-muted);
    margin-right: 8px;
  }
  .ms-btn {
    font-size: 12px; font-weight: 600;
    padding: 6px 12px;
    color: var(--text-muted);
    background: var(--bg-elev);
    border: 1px solid var(--border);
    border-left: 0;
    cursor: pointer;
  }
  .ms-btn:first-of-type { border-radius: 7px 0 0 7px; border-left: 1px solid var(--border); }
  .ms-btn:last-of-type { border-radius: 0 7px 7px 0; }
  .ms-btn.active { color: #fff; background: var(--accent); border-color: var(--accent); }
  .ms-btn:disabled { opacity: 0.5; cursor: not-allowed; }
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
  .cv-fixbtn { white-space: nowrap; color: var(--accent); border-color: var(--accent); }
  .cv-fixbtn:hover { background: rgba(108,140,255,.12); }
  .cv-fixbtn:first-of-type { margin-left: auto; }   /* push the fix buttons to the right */
  .cv-aibtn { color: #fff; background: var(--accent); border-color: var(--accent); }
  .cv-aibtn:hover { filter: brightness(1.08); background: var(--accent); }
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
    /* Scroll vertically when the chrome above the canvas (strips, explainer,
       needs-setup) plus the security panel below it exceed the viewport — the
       canvas keeps its min-height and the whole column scrolls so the bottom
       (security review, recommendations) is always reachable. */
    overflow-x: hidden;
    overflow-y: auto;
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
  /* Multi-line, actionable generation errors (model name + the fixes). */
  .strip-multiline { align-items: flex-start; flex-wrap: nowrap; padding: 10px 14px; }
  .strip-multiline .strip-icon { flex: 0 0 auto; line-height: 1.5; }
  .strip-multiline .strip-body { flex: 1 1 auto; white-space: pre-line; line-height: 1.5; word-break: break-word; }
  .strip-multiline .strip-x {
    flex: 0 0 auto; background: none; border: none; color: inherit; opacity: .7;
    font-size: 15px; line-height: 1; cursor: pointer; padding: 0 2px;
  }
  .strip-multiline .strip-x:hover { opacity: 1; }
  .strip-ok { background: rgba(54, 211, 153, 0.12); color: var(--ok); }
  .strip-reco { background: rgba(124, 122, 255, 0.12); color: var(--text); }
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
  .contract-panel {
    margin-top: 10px;
    padding: 10px 12px;
    border: 1px solid rgba(245, 167, 66, 0.35);
    border-radius: 8px;
    background: rgba(245, 167, 66, 0.08);
  }
  .contract-panel.ok {
    border-color: rgba(54, 211, 153, 0.35);
    background: rgba(54, 211, 153, 0.07);
  }
  .contract-head { display: flex; justify-content: space-between; align-items: flex-start; gap: 12px; }
  .contract-title { font-size: 12px; font-weight: 700; color: var(--text); }
  .contract-summary { margin-top: 2px; font-size: 12px; color: var(--text-muted); line-height: 1.4; }
  .contract-score {
    flex: 0 0 auto;
    font-size: 12px;
    font-weight: 700;
    color: var(--warn, #f5a742);
    border: 1px solid currentColor;
    border-radius: 999px;
    padding: 2px 8px;
  }
  .contract-score.ok { color: var(--ok); }
  .contract-checks { list-style: none; padding: 0; margin: 9px 0 0; display: flex; flex-direction: column; gap: 6px; }
  .contract-check {
    display: grid;
    grid-template-columns: 18px minmax(0, 1fr) auto;
    gap: 8px;
    align-items: start;
    padding: 7px 8px;
    border: 1px solid var(--border);
    border-radius: 7px;
    background: rgba(0,0,0,0.10);
  }
  .contract-check.pass { opacity: 0.72; }
  .contract-check.warn { border-color: rgba(245, 167, 66, 0.35); }
  .contract-check.block { border-color: rgba(255, 107, 129, 0.38); background: rgba(255, 107, 129, 0.08); }
  .contract-mark { font-weight: 800; text-align: center; color: var(--text-muted); }
  .contract-check.pass .contract-mark { color: var(--ok); }
  .contract-check.warn .contract-mark { color: var(--warn, #f5a742); }
  .contract-check.block .contract-mark { color: var(--error); }
  .contract-copy { min-width: 0; }
  .contract-check-title { font-size: 12px; font-weight: 700; color: var(--text); }
  .contract-message, .contract-fix { font-size: 12px; line-height: 1.4; color: var(--text-muted); }
  .contract-fix { margin-top: 2px; color: var(--text); opacity: 0.86; }
  .contract-open {
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 3px 8px;
    background: var(--bg-elev-2);
    color: var(--text);
    font-size: 11px;
    cursor: pointer;
  }
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

  /* Non-convergence verdict: a flow fighting a reasoning task → offer agent mode */
  .build-diagnosis {
    margin-top: 10px; padding: 10px 12px;
    background: rgba(124, 132, 255, 0.10);
    border: 1px solid var(--accent);
    border-radius: 8px;
    display: flex; flex-direction: column; gap: 8px;
  }
  .build-diagnosis .bd-text { font-size: 12.5px; line-height: 1.5; color: var(--text); }
  .build-diagnosis .bd-action {
    align-self: flex-start;
    font-size: 12px; font-weight: 600;
    padding: 6px 14px; border-radius: 7px;
    color: #fff; background: var(--accent); border: 0; cursor: pointer;
  }
  .build-diagnosis .bd-action:disabled { opacity: 0.5; cursor: not-allowed; }

  /* "Built as an agent, not a workflow" explainer modal */
  .agent-route-modal { max-width: 560px; }
  .agent-route-reason {
    margin: 12px 0; padding: 10px 12px;
    background: rgba(124, 132, 255, 0.10);
    border-left: 3px solid var(--accent);
    border-radius: 6px;
    font-size: 13px; line-height: 1.5; color: var(--text);
  }
  .agent-route-hint { font-size: 12.5px; color: var(--text-muted); }
  .failed-list { list-style: none; margin: 8px 0; padding: 0; display: flex; flex-direction: column; gap: 8px; }
  .debug-evidence {
    margin-top: 8px;
    padding: 10px 12px;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: rgba(255,255,255,0.03);
    color: var(--text);
    font-size: 12px;
  }
  .debug-evidence-head {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    margin-bottom: 6px;
  }
  .debug-evidence-head code {
    color: var(--text-muted);
    font-size: 11px;
    word-break: break-all;
  }
  .debug-evidence-error {
    color: var(--error);
    margin-bottom: 8px;
    line-height: 1.45;
  }
  .debug-evidence summary {
    cursor: pointer;
    color: var(--text-muted);
    margin-bottom: 6px;
  }
  .debug-evidence pre {
    margin: 8px 0 0;
    padding: 10px;
    max-height: 220px;
    overflow: auto;
    border-radius: 6px;
    background: #080a15;
    color: #cbd0ea;
    white-space: pre-wrap;
    word-break: break-word;
  }
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
    word-break: break-word;
    font-family: ui-monospace, monospace;
    font-size: 12px;
    background: var(--bg-elev-2);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 4px 8px;
    /* Long tool outputs (e.g. a NotebookLM report) are fully reachable by scroll
       instead of being clipped or flooding the panel. */
    max-height: 320px;
    overflow: auto;
  }

  /* Status dot + the always-visible one-line read of each step. */
  .status-dot { flex: none; font-size: 12px; font-weight: 700; }
  .status-dot.ok { color: var(--ok, #38c172); }
  .status-dot.fail { color: var(--error, #ff6b81); }
  .step-line {
    margin-top: 3px;
    font-size: 12px;
    line-height: 1.45;
    white-space: pre-wrap;
    word-break: break-word;
  }
  .step-line.ok { color: var(--text-muted, #9aa3b2); }
  .step-line.err {
    color: var(--error, #ff6b81);
    background: rgba(255, 107, 129, 0.08);
    border-radius: 6px;
    padding: 4px 8px;
    font-weight: 500;
  }
  .step-details > summary {
    cursor: pointer;
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--text-muted, #8b93ab);
    margin-top: 4px;
    list-style: revert;
  }
  .step-details[open] > summary { margin-bottom: 4px; }
  /* Run history list: every run, clickable to load its trace. */
  .run-list { list-style: none; margin: 0 0 10px; padding: 0; display: flex; flex-direction: column; gap: 4px; }
  .run-row {
    width: 100%; display: flex; align-items: center; gap: 10px;
    background: var(--bg-elev-2); border: 1px solid var(--border);
    border-radius: 6px; padding: 6px 10px; cursor: pointer; text-align: left;
    color: var(--text); font-size: 12px;
  }
  .run-row:hover { border-color: var(--accent); }
  .run-row.active { border-color: var(--accent); background: color-mix(in srgb, var(--accent) 14%, transparent); }
  .run-badge { flex: none; font-weight: 700; }
  .run-badge.ok { color: var(--ok, #38c172); }
  .run-badge.fail { color: var(--error, #ff6b81); }
  .run-when { flex: none; color: var(--text); font-variant-numeric: tabular-nums; }
  .run-trigger {
    flex: none; font-size: 10px; text-transform: uppercase; letter-spacing: 0.04em;
    color: var(--text-muted); border: 1px solid var(--border); border-radius: 4px; padding: 0 5px;
  }
  .run-steps { flex: none; color: var(--text-muted); }
  .run-err { flex: 1; min-width: 0; color: var(--error, #ff6b81); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

  .diagnosis-card {
    margin: 0 0 12px;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--bg-elev-2);
    padding: 10px 12px;
    font-size: 12px;
  }
  .diagnosis-card.failed {
    border-color: color-mix(in srgb, var(--error, #ff6b81) 55%, var(--border));
    background: color-mix(in srgb, var(--error, #ff6b81) 9%, var(--bg-elev-2));
  }
  .diagnosis-card.success {
    border-color: color-mix(in srgb, var(--ok, #38c172) 45%, var(--border));
    background: color-mix(in srgb, var(--ok, #38c172) 7%, var(--bg-elev-2));
  }
  .diagnosis-card.empty {
    border-color: color-mix(in srgb, var(--warn, #f6c343) 45%, var(--border));
    background: color-mix(in srgb, var(--warn, #f6c343) 7%, var(--bg-elev-2));
  }
  .diagnosis-head {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-bottom: 8px;
  }
  .diagnosis-badge {
    flex: none;
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 1px 7px;
    color: var(--text-muted);
    text-transform: uppercase;
    font-size: 10px;
    letter-spacing: 0.04em;
  }
  .diagnosis-row {
    display: grid;
    grid-template-columns: 94px 1fr;
    gap: 10px;
    margin: 5px 0;
  }
  .diagnosis-row span {
    color: var(--text-muted);
    text-transform: uppercase;
    font-size: 10px;
    letter-spacing: 0.04em;
  }
  .diagnosis-row p { margin: 0; color: var(--text); line-height: 1.45; }
  .diagnosis-meta {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin: 8px 0 0;
  }
  .diagnosis-meta span {
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 2px 7px;
    color: var(--text-muted);
    background: var(--bg);
  }
  .diagnosis-list { margin: 8px 0 0 18px; padding: 0; color: var(--text-muted); }
  .diagnosis-list li { margin: 3px 0; line-height: 1.4; }
  .diagnosis-evidence { margin-top: 8px; color: var(--text-muted); }
  .diagnosis-evidence > summary { cursor: pointer; font-size: 11px; }
  .diagnosis-evidence ul { margin: 5px 0 0 18px; padding: 0; }

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
  /* A bench header that folds its body (toggle on the left, action on the right). */
  .bench-head.split { display: flex; align-items: center; gap: 8px; }
  .bench-head-toggle {
    flex: 1 1 auto;
    min-width: 0;
    display: flex;
    align-items: center;
    gap: 8px;
    background: none;
    border: none;
    padding: 0;
    cursor: pointer;
    text-align: left;
    color: inherit;
  }
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

  /* Per-block trace badges: wired (typed-port handoff), duration, error. */
  .wired-badge,
  .dur-badge,
  .err-badge {
    font-size: 10px;
    letter-spacing: 0.3px;
    font-weight: 700;
    border-radius: 999px;
    padding: 0 6px;
  }
  .wired-badge {
    text-transform: uppercase;
    color: var(--accent, #4f8cff);
    border: 1px solid var(--accent, #4f8cff);
    background: rgba(79, 140, 255, 0.12);
  }
  .dur-badge {
    color: var(--text-muted);
    border: 1px solid var(--border, #3a3a3a);
    background: transparent;
  }
  .err-badge {
    text-transform: uppercase;
    color: var(--danger, #e5534b);
    border: 1px solid var(--danger, #e5534b);
    background: rgba(229, 83, 75, 0.12);
  }
  .step-failed { background: rgba(229, 83, 75, 0.06); border-radius: 6px; }
  .io-err { color: var(--danger, #e5534b); white-space: pre-wrap; }

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
    background: rgba(8, 11, 20, 0.5);
    -webkit-backdrop-filter: blur(6px) saturate(120%);
    backdrop-filter: blur(6px) saturate(120%);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 50;
    animation: backdrop-in 160ms ease-out;
  }
  @keyframes backdrop-in { from { opacity: 0; } to { opacity: 1; } }
  .modal {
    width: min(460px, 92vw);
    max-height: 86vh;
    overflow-y: auto;
    /* Frosted glass: translucent surface over the blurred backdrop. */
    background: color-mix(in srgb, var(--bg-elev) 82%, transparent);
    -webkit-backdrop-filter: blur(18px) saturate(140%);
    backdrop-filter: blur(18px) saturate(140%);
    border: 1px solid color-mix(in srgb, var(--border) 70%, transparent);
    border-radius: 14px;
    padding: 20px;
    box-shadow: 0 16px 48px rgba(0, 0, 0, 0.5), inset 0 1px 0 rgba(255, 255, 255, 0.04);
    /* Spring-ish pop on open. */
    animation: modal-pop 220ms cubic-bezier(0.22, 1.2, 0.36, 1);
  }
  @keyframes modal-pop {
    from { opacity: 0; transform: translateY(8px) scale(0.97); }
    to   { opacity: 1; transform: translateY(0) scale(1); }
  }
  @media (prefers-reduced-motion: reduce) {
    .modal-backdrop, .modal { animation: none; }
  }
  .modal-title { margin: 0 0 10px; font-size: 15px; color: var(--text); }
  .modal-body { margin: 0 0 12px; font-size: 13px; line-height: 1.5; color: var(--text-muted); }
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
  .modal-actions { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 8px; }

  /* Model chooser. Deliberately NO position:absolute — the modal is
     overflow-y:auto, so any floating layer gets clipped by it and covers the
     action buttons. The list lives in the flow and the modal grows around it. */
  .mp-hint { margin: 0; font-size: .8rem; color: var(--text-dim, #6b7294); }
  .mp-filter { width: 100%; box-sizing: border-box; }
  .mp-list {
    display: flex; flex-direction: column;
    max-height: 200px; overflow-y: auto;      /* the LIST scrolls, not the modal */
    border: 1px solid color-mix(in srgb, var(--border) 70%, transparent);
    border-radius: 8px;
    background: color-mix(in srgb, var(--bg) 55%, transparent);
  }
  .mp-row {
    display: flex; align-items: center; gap: .5rem;
    width: 100%; text-align: left; background: none; border: none; border-radius: 0;
    padding: .5rem .6rem; font-size: .84rem; color: var(--text, #c8cadf);
    cursor: pointer;
  }
  .mp-row + .mp-row { border-top: 1px solid color-mix(in srgb, var(--border) 40%, transparent); }
  .mp-row:hover { background: color-mix(in srgb, var(--accent, #6c63ff) 10%, transparent); }
  .mp-row.sel   { background: color-mix(in srgb, var(--accent, #6c63ff) 18%, transparent); }
  .mp-tick  { width: 1em; flex-shrink: 0; color: var(--accent, #6c63ff); font-size: .8rem; }
  .mp-name  { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .mp-name.muted { color: var(--text-dim, #6b7294); }
  .mp-empty { margin: 0; padding: .7rem .6rem; font-size: .78rem; color: var(--text-dim, #6b7294); }
  .mp-manual { width: 100%; box-sizing: border-box; }
  .mp-link {
    align-self: flex-start;
    background: none; border: none; padding: 0;
    font-size: .78rem; color: var(--accent, #6c63ff);
    cursor: pointer; text-decoration: underline;
  }
  .modal-actions .btn { white-space: normal; }

  /* The Studio model modal carries a lot more than the model list (two preset
     groups too), so it gets a wider frame and lays the presets out side by side
     instead of stacking everything into a cramped 460px column. */
  /* Generate progress modal. */
  .gen-progress-modal { width: min(480px, 92vw); }
  .gen-status { display: flex; align-items: center; gap: 10px; margin: 6px 0 12px; font-size: 13px; font-weight: 600; color: var(--text); }
  .gen-events {
    list-style: none; margin: 0; padding: 8px 10px;
    display: flex; flex-direction: column; gap: 6px;
    max-height: 260px; overflow-y: auto;
    border: 1px solid color-mix(in srgb, var(--border) 70%, transparent);
    border-radius: 8px;
    background: color-mix(in srgb, var(--bg) 55%, transparent);
  }
  .gen-ev { font-size: 12px; line-height: 1.45; color: var(--text-muted); display: flex; gap: 8px; align-items: baseline; }
  .gen-ev-phase { flex: none; font-weight: 600; color: var(--text); min-width: 108px; }
  .gen-ev-msg { color: var(--text-muted); }
  .gen-ev.gen-ev-complete .gen-ev-phase { color: #4caf82; }
  .gen-ev.gen-ev-error .gen-ev-phase { color: var(--error, #f06060); }
  .gen-ev.gen-ev-skip { opacity: .6; }
  .gen-latest { margin: 10px 0 0; font-size: 12px; color: var(--text-muted); line-height: 1.5; }

  .modal.model-modal { width: min(760px, 94vw); }
  .mp-preset-row input[type="radio"] {
    margin-top: 3px;
    flex-shrink: 0;
  }
  .mp-settings-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 22px;
    margin-top: 16px;
    padding-top: 14px;
    border-top: 1px solid color-mix(in srgb, var(--border) 70%, transparent);
  }

  /* Grid owns the divider + top spacing; the sections inside sit flush. */
  .mp-settings-grid > .mp-section { margin-top: 0; padding-top: 0; border-top: none; }
  /* Give the model list a little more room now the modal is wider. */
  .model-modal .mp-list { max-height: 240px; }
  @media (max-width: 640px) {
    .mp-settings-grid { grid-template-columns: 1fr; gap: 16px; }
  }

  /* Browse SOUL.yaml: wider modal with an agent list beside a read-only viewer. */
  .modal.yaml-browser { width: min(920px, 94vw); }
  .yamlb-split { display: grid; grid-template-columns: 240px 1fr; gap: 12px; min-height: 320px; }
  .yamlb-list { max-height: 56vh; overflow-y: auto; margin: 0; }
  .picker-main.selected { background: var(--bg-elev); border-color: var(--accent, var(--ok)); }
  .yamlb-view {
    display: flex; flex-direction: column; min-width: 0;
    border: 1px solid var(--border); border-radius: 10px; background: var(--bg);
    overflow: hidden;
  }
  .yamlb-path {
    font-family: ui-monospace, monospace; font-size: 11px; color: var(--text-muted);
    padding: 6px 10px; border-bottom: 1px solid var(--border);
    white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  }
  .yamlb-code {
    margin: 0; padding: 12px; overflow: auto; max-height: 56vh;
    font-family: ui-monospace, monospace; font-size: 12px; line-height: 1.5;
    color: var(--text); white-space: pre; tab-size: 2;
  }
  @media (max-width: 640px) {
    .yamlb-split { grid-template-columns: 1fr; }
    .yamlb-list { max-height: 26vh; }
  }

  /* ── Pre-generation refinement dialog ───────────────────────────────────── */
  .refine-modal { max-width: 640px; width: 92vw; max-height: 84vh; overflow-y: auto; }

  /* Prompt editor modal: roomy editor for writing/editing the (refined) prompt */
  .prompt-modal { max-width: 820px; width: 92vw; max-height: 88vh; overflow-y: auto; }
  .pe-label { display: block; font-size: 11px; font-weight: 700; letter-spacing: .06em; text-transform: uppercase; color: var(--text-muted); margin: 14px 0 2px; }
  .pe-hint { font-size: 12px; color: var(--text-muted); margin: 0 0 6px; line-height: 1.45; }
  .pe-area {
    width: 100%; box-sizing: border-box; resize: vertical; min-height: 150px;
    font: inherit; font-size: 14px; line-height: 1.6; padding: 12px 14px;
    border-radius: 10px; border: 1px solid var(--border);
    background: var(--bg-elev); color: var(--text);
  }
  .pe-area:focus { outline: none; border-color: var(--accent); }
  .pe-row { display: flex; justify-content: flex-start; margin: 8px 0 2px; }
  .rules-area { min-height: 420px; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 13px; line-height: 1.55; white-space: pre; }
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
  .pf-item {
    display: block;
    width: 100%;
    border-radius: 8px;
    padding: 9px 11px;
    border: 0;
    border-left: 3px solid transparent;
    font: inherit;
    font-size: 13px;
    text-align: left;
  }
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
  .agent-yaml-link {
    margin-left: auto; font-size: 12px; font-weight: 600;
    padding: 5px 12px; border-radius: 7px;
    color: var(--accent); background: rgba(124, 132, 255, 0.10);
    border: 1px solid var(--accent); cursor: pointer; white-space: nowrap;
  }
  .agent-yaml-link:hover { background: rgba(124, 132, 255, 0.18); }
  .agent-field-label { display: block; font-size: 12px; font-weight: 600; color: var(--text-muted); margin: 12px 0 4px; }
  .agent-sys, .agent-tools {
    width: 100%; box-sizing: border-box; resize: vertical; font: inherit; font-size: 13px;
    line-height: 1.5; padding: 10px 12px; border-radius: 8px;
    border: 1px solid var(--border, rgba(127,127,127,0.35));
    background: var(--surface, transparent); color: var(--text);
  }
  .agent-tools { font-family: ui-monospace, monospace; font-size: 12px; }
  .agent-spec-meta { display: flex; flex-wrap: wrap; gap: 14px; margin-top: 12px; font-size: 12px; color: var(--text-muted); }

  /* Try-it panel inside the agent editor */
  .agent-try { margin-top: 18px; padding-top: 14px; border-top: 1px solid var(--border); }
  .agent-try-row { display: flex; gap: 8px; align-items: center; }
  .agent-try-input {
    flex: 1 1 auto; font-size: 13px; padding: 8px 10px;
    color: var(--text); background: var(--bg-elev);
    border: 1px solid var(--border); border-radius: 7px;
  }
  .agent-try-result {
    margin-top: 10px; padding: 10px 12px; border-radius: 8px;
    background: var(--bg-elev); border: 1px solid var(--border);
    font-size: 13px; line-height: 1.55; white-space: pre-wrap; word-break: break-word;
  }
  .agent-try-result.err { border-color: var(--error); }
  .agent-try-err { color: var(--error); margin-bottom: 6px; }
  .py-explain { margin: 4px 0 8px; padding: 8px 10px; border-radius: 8px; background: var(--bg-elev-2); border-left: 3px solid var(--warn, #f5a524); }
  .py-explain-what { font-size: 12px; color: var(--text); }
  .py-explain-fix { font-size: 12px; color: var(--text-muted); margin-top: 3px; }
  .try-fix-row {
    display: flex; align-items: center; gap: 8px; flex-wrap: wrap;
    margin-top: 8px;
  }
  .try-fix-row .cv-fixbtn:first-of-type { margin-left: 0; }
  .try-fix-hint { color: var(--text-muted); font-size: 12px; line-height: 1.4; }
  /* Observe-and-adjust panel */
  .live-adjust { margin-top: 10px; padding-top: 8px; border-top: 1px dashed var(--border); }
  .adjust-done { font-size: 12px; color: var(--ok, #2ea043); margin-top: 6px; }
  .adjust-card {
    margin-top: 8px; padding: 10px; border-radius: 8px;
    background: var(--bg-elev-2); border: 1px solid var(--border);
    border-left: 3px solid var(--accent, #6ea8fe);
  }
  .adjust-card.auto { border-left-color: var(--ok, #2ea043); }
  .adjust-head { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .adjust-class { font-size: 11px; color: var(--text-muted); text-transform: capitalize; }
  .adjust-badge {
    font-size: 10px; padding: 1px 6px; border-radius: 999px;
    background: var(--ok-bg, rgba(46,160,67,0.15)); color: var(--ok, #2ea043);
  }
  .adjust-rationale { font-size: 12px; color: var(--text); margin-top: 4px; line-height: 1.4; }
  .adjust-observed { font-size: 12px; color: var(--text-muted); margin-top: 4px; }
  .adjust-observed code, .adjust-diff code { font-family: var(--mono, monospace); font-size: 11px; }
  .adjust-diff { margin-top: 6px; display: flex; flex-direction: column; gap: 3px; }
  .diff-old code { color: var(--text-muted); text-decoration: line-through; }
  .diff-new code { color: var(--text); }
  .repair-diff { margin-top: 8px; border: 1px solid var(--border); border-radius: 8px; overflow: hidden; }
  .repair-diff-head { padding: 6px 10px; font-size: 12px; background: var(--bg-elev-2); border-bottom: 1px solid var(--border); }
  .rd-stat { font-weight: 700; }
  .rd-stat.add { color: #36d399; }
  .rd-stat.del { color: #ff6b81; }
  .repair-diff-body { margin: 0; padding: 8px 10px; max-height: 260px; overflow: auto; font-size: 11px; line-height: 1.45;
    font-family: ui-monospace, Menlo, monospace; white-space: pre; }
  .repair-diff-body .rd-add { color: #36d399; display: block; }
  .repair-diff-body .rd-del { color: #ff6b81; display: block; }
  .repair-diff-body .rd-ctx { color: var(--text-muted); display: block; }
  .diff-tag { display: inline-block; width: 30px; font-size: 10px; color: var(--text-muted); }
  .adjust-advisory { font-size: 12px; color: var(--text-muted); margin-top: 6px; }
  .adjust-actions { display: flex; gap: 6px; margin-top: 8px; }
  /* Full input/output for copy-paste testing outside Studio */
  code.full { white-space: pre-wrap; overflow-wrap: anywhere; max-height: 320px; overflow: auto; display: block; }
  .copy-mini {
    margin-left: 6px; font-size: 10px; padding: 0 6px; border-radius: 4px; cursor: pointer;
    border: 1px solid var(--border); background: var(--bg-elev-1); color: var(--text-muted);
  }
  .copy-mini:hover { color: var(--text); }
  .att-item.soft .att-dot { color: var(--warn, #f5a524); }
  .node-skip.soft { color: var(--warn, #f5a524); }
  .node-adapted {
    margin-left: 6px; font-size: 10px; padding: 1px 6px; border-radius: 999px;
    background: var(--accent-bg, rgba(110,168,254,0.15)); color: var(--accent, #6ea8fe);
  }
  .agent-try-reply.muted { color: var(--text-muted); }
  .agent-try-trace { margin-top: 10px; }
  .att-label { font-size: 11px; font-weight: 700; letter-spacing: 0.04em; text-transform: uppercase; color: var(--text-muted); margin-bottom: 6px; }
  .att-label.muted { text-transform: none; letter-spacing: 0; font-weight: 500; }
  .att-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 3px; }
  .att-item { font-size: 12.5px; border-radius: 6px; background: var(--bg-elev); overflow: hidden; }
  .att-item.err { background: rgba(255, 90, 90, 0.10); }
  .att-row { display: flex; align-items: center; gap: 8px; width: 100%; text-align: left; background: transparent; border: 0; cursor: pointer; padding: 5px 8px; color: inherit; font-size: inherit; }
  .att-n { color: var(--text-muted); font-variant-numeric: tabular-nums; min-width: 14px; }
  .att-dot { color: #36d399; }
  .att-item.err .att-dot { color: var(--error); }
  .att-name { color: var(--text); font-family: ui-monospace, monospace; flex: 1 1 auto; }
  .node-kind { font-size: 10px; font-weight: 700; text-transform: uppercase; letter-spacing: .04em; padding: 1px 6px; border-radius: 999px; }
  .node-kind.kind-python { background: rgba(236,124,196,.16); color: #ec7cc4; }
  .node-kind.kind-tool   { background: rgba(96,204,154,.16); color: #5fce9a; }
  .node-kind.kind-agent  { background: rgba(108,99,255,.18); color: #b3adff; }
  .node-kind.kind-llm    { background: rgba(240,176,112,.16); color: #f0b070; }
  .node-skip { font-size: 11px; color: #f0b070; margin-left: 6px; }
  .att-hint { font-size: 11px; color: var(--text-muted); margin-top: 6px; }
  .att-detail { color: var(--accent); }
  .att-caret { color: var(--text-muted); }
  .att-detail-box { padding: 2px 10px 8px 30px; display: flex; flex-direction: column; gap: 5px; }
  .att-kv { display: flex; gap: 8px; align-items: baseline; }
  .att-k { font-size: 10px; font-weight: 700; text-transform: uppercase; letter-spacing: 0.05em; color: var(--text-muted); min-width: 42px; }
  .att-kv code { font-family: ui-monospace, monospace; font-size: 11.5px; color: var(--text); white-space: pre-wrap; word-break: break-word; background: var(--bg-elev-2, rgba(0,0,0,0.2)); border-radius: 4px; padding: 2px 6px; }

  /* Output → delivery auto-wiring */
  .strip-delivery { background: rgba(124, 132, 255, 0.08); }
  .dlv-hint { color: var(--text-muted); font-size: 12px; }
  .dlv-btn {
    font-size: 12px; font-weight: 600; padding: 3px 10px; border-radius: 6px;
    color: var(--accent); background: var(--bg-elev);
    border: 1px solid var(--accent); cursor: pointer; margin-left: 4px;
  }
  .dlv-btn:hover { background: rgba(124, 132, 255, 0.15); }
  .dlv-chip { display: inline-flex; align-items: center; gap: 3px; margin-left: 5px; padding: 1px 4px 1px 7px; border-radius: 10px; background: var(--bg-elev); border: 1px solid var(--border); }
  .dlv-x { border: 0; background: transparent; color: var(--text-muted); cursor: pointer; font-size: 14px; line-height: 1; padding: 0 2px; }
  .dlv-x:hover { color: var(--error); }

  /* Credentials (first-class secret binding) */
  .creds { padding: 4px 2px; }
  .creds-hint { font-size: 12px; color: var(--text-muted); margin: 0 0 10px; line-height: 1.5; }
  .cred-row { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 8px 0; border-top: 1px solid var(--border); flex-wrap: wrap; }
  .cred-meta { display: flex; flex-direction: column; gap: 2px; min-width: 180px; }
  .cred-name { font-size: 13px; font-weight: 600; color: var(--text); }
  .cred-env { font-size: 11px; color: var(--text-muted); font-family: ui-monospace, monospace; }
  .cred-desc { font-size: 11.5px; color: var(--text-muted); }
  .cred-set { display: flex; gap: 6px; align-items: center; }
  .cred-input { font-size: 13px; padding: 6px 10px; border-radius: 7px; color: var(--text); background: var(--bg-elev); border: 1px solid var(--border); min-width: 220px; }
  .creds-msg { margin-top: 8px; font-size: 12px; color: var(--accent); }

  /* Runtime live-state + motion polish */
  .agent-try-running { display: flex; align-items: center; gap: 8px; margin-top: 10px; font-size: 12.5px; color: var(--text-muted); }
  .live-dot {
    width: 9px; height: 9px; border-radius: 50%; background: var(--accent);
    box-shadow: 0 0 0 0 color-mix(in srgb, var(--accent) 60%, transparent);
    animation: live-pulse 1.3s ease-out infinite;
  }
  @keyframes live-pulse {
    0%   { box-shadow: 0 0 0 0 color-mix(in srgb, var(--accent) 55%, transparent); }
    70%  { box-shadow: 0 0 0 8px color-mix(in srgb, var(--accent) 0%, transparent); }
    100% { box-shadow: 0 0 0 0 color-mix(in srgb, var(--accent) 0%, transparent); }
  }
  /* Spring-y feedback on the mode + view toggles and primary buttons. */
  .ms-btn, .vs-btn, .btn { transition: transform 120ms cubic-bezier(0.22, 1.2, 0.36, 1), background 140ms ease, border-color 140ms ease, color 140ms ease; }
  .ms-btn:active:not(:disabled), .vs-btn:active, .btn:active:not(:disabled) { transform: scale(0.96); }
  .ms-btn.active { transform: translateZ(0); }
  .agent-try-result, .agent-try-trace, .build-diagnosis, .strip-delivery { animation: rise-in 200ms cubic-bezier(0.22, 1.1, 0.36, 1); }
  @keyframes rise-in { from { opacity: 0; transform: translateY(5px); } to { opacity: 1; transform: translateY(0); } }
  @media (prefers-reduced-motion: reduce) {
    .live-dot { animation: none; }
    .agent-try-result, .agent-try-trace, .build-diagnosis, .strip-delivery { animation: none; }
    .ms-btn, .vs-btn, .btn { transition: none; }
  }
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
  .intent-guide {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 10px 12px;
    border-bottom: 1px solid var(--border, #262e44);
    background: rgba(108, 99, 255, .08);
    color: var(--text, #e6e9f2);
    font-size: 12px;
    flex-wrap: wrap;
  }
  .intent-guide span:last-child { color: var(--text-muted, #8b93ab); }
  .intent-pill {
    background: rgba(108, 99, 255, .18);
    border: 1px solid rgba(108, 99, 255, .34);
    border-radius: 999px;
    color: var(--accent, #8b85ff);
    padding: 2px 8px;
    text-transform: uppercase;
    letter-spacing: .06em;
    font-size: 10px;
    font-weight: 700;
  }

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

  /* F-GUI-3 — Studio security review panel. Sits inline in the toolbar strip
     so the operator sees blockers/warnings/recommendations at a glance while
     editing. Colours match the existing Contract panel weight. */
  .security-panel {
    margin-top: .35rem;
    background: rgba(20, 22, 40, .8);
    border: 1px solid #1a1e36;
    border-radius: 8px;
    padding: .35rem .6rem;
    font-size: .78rem;
    /* Don't let a long review (blockers + warnings + recs) grow unbounded and
       push the canvas off-screen — cap it and let its own body scroll. */
    flex: 0 0 auto;
    max-height: 42vh;
    overflow-y: auto;
  }
  .security-panel.has-warnings { border-color: rgba(245,167,66,.4); }
  .security-panel.has-blockers { border-color: rgba(240,96,96,.45); }
  /* G7 — refresh is now a sibling button in the header row, not a nested
     click-only span. Row uses flex so the toggle takes remaining space and
     refresh pins to the right. */
  .security-head-row {
    display: flex; align-items: center; gap: .25rem;
  }
  .security-head {
    flex: 1; background: transparent; border: 0;
    display: flex; align-items: center; gap: .5rem;
    color: #dfe2ff; padding: .2rem 0;
    cursor: pointer; text-align: left;
  }
  .security-caret { color: #7b82a8; font-size: .72rem; }
  .security-panel strong { font-weight: 600; }
  .security-count { color: #ada8ff; font-size: .74rem; }
  .security-count.ok { color: #4caf82; }
  .security-count.muted { color: #7b82a8; }
  .security-refresh {
    background: transparent; border: 0;
    color: #7b82a8; cursor: pointer;
    padding: .1rem .45rem; border-radius: 4px;
    font: inherit; line-height: 1;
  }
  .security-refresh:hover { color: #dfe2ff; background: rgba(255,255,255,.05); }
  .security-refresh:focus-visible { outline: 2px solid rgba(108,99,255,.6); outline-offset: 1px; }
  .security-summary {
    display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
    gap: .3rem .8rem; margin: .45rem 0;
  }
  .security-summary > div {
    display: flex; gap: .35rem; align-items: baseline; min-width: 0;
  }
  .security-summary span {
    color: #7b82a8; font-size: .7rem; text-transform: uppercase;
    letter-spacing: .04em; flex-shrink: 0;
  }
  .security-summary code {
    color: #dfe2ff; font-size: .74rem;
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0;
  }
  .security-block-list { margin-top: .4rem; }
  .security-block-hdr {
    color: #ada8ff; font-size: .72rem; text-transform: uppercase; letter-spacing: .05em;
    margin: .3rem 0;
  }
  .security-item {
    background: #0e1020; border: 1px solid #1a1e36; border-radius: 6px;
    padding: .4rem .55rem; margin-bottom: .3rem;
    display: flex; flex-direction: column; gap: .15rem;
  }
  .security-item.block { border-color: rgba(240,96,96,.4); }
  .security-item.warn  { border-color: rgba(245,167,66,.35); }
  .security-item.rec   { border-color: rgba(139,220,255,.35); }
  .security-cat {
    color: #7b82a8; font-size: .68rem; text-transform: uppercase; letter-spacing: .04em;
  }
  .security-msg { color: #dfe2ff; }
  .security-msg code { color: #ada8ff; }
  .security-fix { color: #7b82a8; font-size: .72rem; }
  .security-apply { align-self: flex-start; margin-top: .3rem; }
</style>
