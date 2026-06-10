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
      const data = await bridge.compile(text, Object.keys(answers).length ? answers : undefined, compactCatalog(catalog))
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
      const data = await bridge.compile(intent.trim(), answers, compactCatalog(catalog))
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

  // ── M6: set the current draft directly (templates / draft-load / import) ───
  // Shared by every path that swaps in a complete workflow WITHOUT a compile
  // round-trip. Mirrors the post-compile reset so the canvas, inspector,
  // plan/tier and validation all refresh consistently.
  function setWorkflow(wf, { name } = {}) {
    workflow = wf || null
    if (workflow && name && !workflow.name) workflow = { ...workflow, name }
    questions = []
    notes = []
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

  // ── M6: Draft library — Save / Open / Load / Delete (S6.2) ────────────────
  let savingDraft = false
  let library = { open: false, loading: false, error: '', drafts: [], busyId: '' }

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
    } catch (e) {
      saveError = e.message || 'could not save draft'
    } finally {
      savingDraft = false
    }
  }

  async function openLibrary() {
    library = { open: true, loading: true, error: '', drafts: [], busyId: '' }
    try {
      const data = await bridge.draftsList()
      const drafts = (data && Array.isArray(data.drafts)) ? data.drafts : []
      library = { open: true, loading: false, error: '', drafts, busyId: '' }
    } catch (e) {
      library = { open: true, loading: false, error: e.message || 'could not list drafts', drafts: [], busyId: '' }
    }
  }

  function closeLibrary() {
    library = { ...library, open: false }
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

    <!-- M6: draft management toolbar -->
    <div class="toolbar" role="group" aria-label="Draft management">
      <button class="btn" type="button" on:click={openTemplates} title="Start from a template">Templates</button>
      <button class="btn" type="button" on:click={openLibrary} title="Open a saved draft">Open</button>
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

    <div class="badge" title="Scoped plugin principal">
      principal: <strong>plugin:studio</strong>
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
            <p class="empty-or">— or —</p>
            <button class="btn primary" type="button" on:click={openTemplates}>Start from a template</button>
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
          <div class="strip strip-error">⚠ {testError}</div>
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
    </section>

    <Inspector
      node={selectedNode}
      {selectedEdge}
      {workflow}
      channels={channelOptions}
      onChange={applyFraming}
      onEdgeChange={applyEdgePatch}
      onRefine={refineNode}
      {refineState}
    />
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

  <!-- ── M6: Draft library (S6.2) ────────────────────────────────────────── -->
  {#if library.open}
    <div class="modal-backdrop" on:click|self={closeLibrary} role="presentation">
      <div class="modal" role="dialog" aria-modal="true" aria-labelledby="lib-title">
        <h2 id="lib-title" class="modal-title">Open a draft</h2>
        {#if library.error}<div class="strip strip-error">⚠ {library.error}</div>{/if}
        {#if library.loading}
          <p class="muted">Loading drafts…</p>
        {:else if !library.drafts.length}
          <p class="muted">No saved drafts yet. Use “Save draft” to create one.</p>
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
        <div class="modal-actions">
          <button class="btn" type="button" on:click={closeLibrary}>Close</button>
        </div>
      </div>
    </div>
  {/if}

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
