<script>
  import { onMount, tick } from 'svelte'
  import { api } from '../lib/api.js'
  import { apiKey, editAgent } from '../lib/stores.js'
  import ChipPicker from '../lib/ChipPicker.svelte'
  import FilePicker from '../lib/FilePicker.svelte'

  let agents   = []
  let selected = null   // the agent currently shown in the editor
  let editing  = null   // deep-copy being modified
  let error    = null
  let saveMsg  = ''
  let saving   = false
  let deleting = false
  let validating = false
  let validationReport = null

  // Tool catalog — fetched once, used by the python_file dropdown
  let catalog = { python_tools: [], mcp_tools: [], builtins: [] }

  // Providers + per-provider model lists (lazy-fetched on demand)
  let providers       = []   // [{ id, registered }]
  let modelsByProv    = {}   // provider id → [model names]
  let modelsLoading   = {}   // provider id → bool
  let modelsError     = {}   // provider id → string

  // Lookup sources for the picker fields. Loaded once on mount so
  // typing errors are impossible — every selection is from a real list.
  let availableChannels  = []  // [{id, name, kind}]
  let availableSkills    = []  // [{name, description}]
  let availableKBs       = []  // [{id, name, description, document_count, chunk_count}]

  async function loadLookups() {
    const [chs, sks, kbs] = await Promise.allSettled([
      api.channels.list(),
      api.skills.list(),
      api.knowledge.list(),
    ])
    if (chs.status === 'fulfilled') availableChannels = chs.value?.channels || []
    if (sks.status === 'fulfilled') availableSkills   = sks.value?.skills   || []
    if (kbs.status === 'fulfilled') availableKBs      = kbs.value?.knowledge_bases || []
  }

  // ---- Picker option transforms ---------------------------------------------
  // Each lookup is mapped to ChipPicker's { value, label, description, group }
  // shape so the picker can be generic. Recomputed reactively so freshly-added
  // channels/skills/KBs show up without a reload.
  $: channelOptions = availableChannels.map(c => ({
    value: c.id, label: c.id, description: c.kind || c.name || '',
  }))
  $: skillOptions = availableSkills.map(s => ({
    value: s.name, label: s.name, description: s.description || '',
  }))
  $: kbOptions = availableKBs.map(k => ({
    value: k.name, label: k.name,
    description: `${k.document_count || 0} docs · ${k.chunk_count || 0} chunks${k.description ? ' — ' + k.description : ''}`,
  }))
  $: peerAgentOptions = agents
    .filter(a => !editing || a.id !== editing.id)
    .map(a => ({
      value: a.id, label: a.id,
      description: a.name || a.description || '',
    }))
  $: builtinOptions = (catalog.builtins || []).map(b => ({
    value: b.name, label: b.name, description: b.description || '',
  }))
  $: pythonFileOptions = (catalog.python_tools || []).map(pt => ({
    value: pt.path, label: pt.name, description: pt.description || '',
  }))
  // The tool_choice dropdown is a UNION of meta-options + peers + builtins.
  // Computed reactively from editing.agents and editing.builtins so the list
  // updates as the user toggles those fields.
  $: toolChoiceOptions = (() => {
    const opts = [
      { value: '',         label: '(default — auto)', description: 'Model decides freely', group: 'Mode' },
      { value: 'auto',     label: 'auto',             description: 'Same as default',      group: 'Mode' },
      { value: 'none',     label: 'none',             description: 'Disallow tool calls on turn 1', group: 'Mode' },
      { value: 'required', label: 'required',         description: 'Must call some tool on turn 1', group: 'Mode' },
    ]
    if (editing) {
      for (const peerId of (editing.agents || [])) {
        const peer = agents.find(a => a.id === peerId)
        opts.push({
          value: `agent__${peerId}`, label: `agent__${peerId}`,
          description: peer?.name || peer?.description || '(peer agent)',
          group: 'Peer agents',
        })
      }
      // Allow naming specific built-ins too (rare but supported)
      for (const b of (catalog.builtins || [])) {
        opts.push({ value: b.name, label: b.name, description: b.description || '', group: 'Built-ins' })
      }
    }
    return opts
  })()
  // Memory scopes are a fixed enum.
  const memoryScopeOptions = [
    { value: 'session', label: 'session', description: 'Per-conversation history' },
    { value: 'global',  label: 'global',  description: 'Persistent across conversations for this agent' },
    { value: 'agent',   label: 'agent',   description: 'Shared across all sessions of this agent' },
  ]

  const BLANK = () => ({
    id: '', name: '', description: '', version: '1.0',
    trigger: 'channel', channels: ['http'], schedule: { cron: '' },
    system_prompt: '',
    llm: { provider: 'ollama', model: '', temperature: 0.7, max_tokens: 512 },
    memory: { read_scopes: ['session'], write_scopes: ['session'], max_tokens: 20 },
    tools: [], skills: [], knowledge: [], agents: [], max_turns: 5, stream_reply: false, enabled: true,
  })

  function isSystemAgent(agent) {
    return agent?.id === 'system'
  }

  $: editingProtected = isSystemAgent(selected)

  async function load() {
    try {
      const res = await api.agents.list()
      agents = res.agents || []
      error  = null
      if ($editAgent) {
        const found = agents.find(a => a.id === $editAgent)
        if (found) {
          select(found)
        }
        $editAgent = ''
      }
    } catch (e) { error = e.message }
  }

  async function loadCatalog() {
    try {
      const res = await api.tools.catalog()
      catalog = {
        python_tools: res?.python_tools || [],
        mcp_tools:    res?.mcp_tools    || [],
        builtins:     res?.builtins     || [],
      }
    } catch (e) {
      catalog = { python_tools: [], mcp_tools: [], builtins: [] }
    }
  }

  // Fetch the list of registered providers (drives the LLM Provider dropdown)
  async function loadProviders() {
    try {
      const res  = await api.providers.list()
      const regs = res.registered || []      // currently registered with the live router
      const ids  = new Set([...regs, ...(res.known || []), ...Object.keys(res.providers || {})])
      providers  = [...ids].map(id => ({
        id,
        registered: regs.includes(id),
        configured: (res.providers || {})[id] != null,
        defaultModel: (res.providers || {})[id]?.model || '',
      }))
    } catch (e) {
      providers = []
    }
  }

  $: enabledProviders = providers.filter(p => p.registered)

  function onProviderChange() {
    if (!editing?.llm) return
    const prov = providers.find(p => p.id === editing.llm.provider)
    if (prov && prov.defaultModel) {
      editing.llm.model = prov.defaultModel
    } else {
      editing.llm.model = ''
    }
  }

  // Lazy-fetch the model list for one provider (cached). Called whenever the
  // user selects a provider in the LLM section.
  async function loadModels(providerId) {
    if (!providerId) return
    if (modelsByProv[providerId] || modelsLoading[providerId]) return
    modelsLoading = { ...modelsLoading, [providerId]: true }
    try {
      const res = await api.providers.models(providerId)
      modelsByProv = { ...modelsByProv, [providerId]: res.models || [] }
      modelsError  = { ...modelsError,  [providerId]: '' }
    } catch (e) {
      modelsByProv = { ...modelsByProv, [providerId]: [] }
      modelsError  = { ...modelsError,  [providerId]: e.message }
    } finally {
      modelsLoading = { ...modelsLoading, [providerId]: false }
    }
  }

  // Reactive: any time the editor's provider changes, pull its model list.
  $: if (editing?.llm?.provider) loadModels(editing.llm.provider)

  // Reactive: if the agent model is unset, default to the provider's defaultModel once providers are loaded.
  $: if (editing && providers.length > 0) {
    if (editing.llm && !editing.llm.model && editing.llm.provider) {
      const prov = providers.find(p => p.id === editing.llm.provider)
      if (prov && prov.defaultModel) {
        editing.llm.model = prov.defaultModel
      }
    }
  }

  // Computed: options for the model dropdown.
  // Rules:
  //   1. The current model is ALWAYS at position 0 — it never moves or disappears
  //      while the provider model list is loading, so bind:value never loses its
  //      match and the browser never snaps to option[0].
  //   2. Remaining known models follow, deduplicated.
  // The {#each} below uses a keyed loop (m) so Svelte moves <option> DOM nodes
  // instead of mutating them in-place — avoids the browser reset-to-first bug.
  $: modelOptions = (() => {
    const provId = editing?.llm?.provider
    const list   = (modelsByProv[provId] || [])
    const cur    = editing?.llm?.model
    const others = list.filter(m => m !== cur && m !== '__custom__')
    return cur ? [cur, ...others] : others
  })()

  // ── Tools editing ─────────────────────────────────────────────────────────
  function addTool() {
    if (!editing) return
    editing.tools = [...(editing.tools || []), {
      name: '', description: '', python_file: '',
      timeout: '',
      parameters: { type: 'object', properties: {} },
    }]
  }
  function removeTool(i) {
    editing.tools = editing.tools.filter((_, idx) => idx !== i)
  }
  function moveTool(i, dir) {
    const arr = [...editing.tools]
    const j = i + dir
    if (j < 0 || j >= arr.length) return
    ;[arr[i], arr[j]] = [arr[j], arr[i]]
    editing.tools = arr
  }
  function onPythonFilePicked(i, path) {
    const meta = catalog.python_tools.find(t => t.path === path)
    editing.tools[i].python_file = path
    if (meta) {
      if (!editing.tools[i].name)        editing.tools[i].name = meta.name
      if (!editing.tools[i].description) editing.tools[i].description = meta.description || ''
    }
    editing = editing
  }
  function paramsJson(t) {
    try { return JSON.stringify(t.parameters || {}, null, 2) } catch { return '{}' }
  }
  function updateParams(i, json) {
    try { editing.tools[i].parameters = JSON.parse(json) } catch { /* mid-edit */ }
  }
  // ── Channels & skills (CSV inputs) ────────────────────────────────────────
  function csvToArr(s) { return (s || '').split(',').map(x => x.trim()).filter(Boolean) }
  function linesToArr(s) { return (s || '').split('\n').map(x => x.trim()).filter(Boolean) }
  function syncChannels(v)  { if (editing) editing.channels  = csvToArr(v) }
  function syncSkills(v)    { if (editing) editing.skills    = csvToArr(v) }
  function syncKnowledge(v) { if (editing) editing.knowledge = csvToArr(v) }
  function syncAgents(v)    { if (editing) editing.agents    = csvToArr(v) }

  // ensurePersona writes a single field into one of the three persona
  // blocks (identity / personality / non_negotiables) while keeping any
  // other fields the operator already typed. We lazily allocate the
  // block so a user who types one Identity field doesn't have an empty
  // Personality block round-trip through YAML.
  //
  // Wiped-clean detection: if every field in the resulting block is
  // empty / falsy, we delete the block so YAML stays minimal. This is
  // what makes the GUI round-trip-safe — a user who clears every
  // Identity field gets `identity:` removed from the saved YAML, not
  // `identity: {role: "", expertise: [], ...}`.
  function ensurePersona(block, patch) {
    if (!editing) return
    const current = editing[block] || {}
    const next = { ...current, ...patch }
    if (isBlockEmpty(block, next)) {
      delete editing[block]
    } else {
      editing[block] = next
    }
    editing = editing  // Svelte reactivity nudge
  }
  function isBlockEmpty(block, obj) {
    if (!obj) return true
    if (block === 'identity') {
      return !obj.role && !obj.audience && !obj.backstory &&
             (!obj.expertise || obj.expertise.length === 0)
    }
    if (block === 'personality') {
      return !obj.tone && !obj.voice &&
             (!obj.prefer || obj.prefer.length === 0) &&
             (!obj.avoid || obj.avoid.length === 0)
    }
    if (block === 'non_negotiables') {
      const oc = obj.output_constraints || {}
      const ocEmpty = !oc.format && !oc.max_length && !oc.min_length
      return (!obj.must || obj.must.length === 0) &&
             (!obj.must_not || obj.must_not.length === 0) &&
             ocEmpty
    }
    return false
  }
  // Built-ins field is tri-state:
  //   "default"  → undefined (no `builtins:` key in YAML, server applies default gating)
  //   "none"     → []        (peer-only orchestrator: no built-ins at all)
  //   "csv,…"    → [csv,…]   (allowlist)
  function syncBuiltins(v) {
    if (!editing) return
    const t = String(v || '').trim().toLowerCase()
    if (t === '' || t === 'default') { delete editing.builtins; return }
    if (t === 'none' || t === '[]')  { editing.builtins = [];   return }
    editing.builtins = csvToArr(v)
  }
  function builtinsDisplay() {
    if (!editing) return 'default'
    if (editing.builtins === undefined || editing.builtins === null) return 'default'
    if (editing.builtins.length === 0) return 'none'
    return editing.builtins.join(', ')
  }
  // Tri-state for the builtins radio: 'default' (field absent), 'none' (empty
  // array), 'restricted' (non-empty array).
  function builtinsMode() {
    if (!editing) return 'default'
    if (editing.builtins === undefined || editing.builtins === null) return 'default'
    if (editing.builtins.length === 0) return 'none'
    return 'restricted'
  }

  function select(agent) {
    selected = agent
    editing  = JSON.parse(JSON.stringify(agent))
    // Ensure nested objects always exist in the editor copy
    editing.llm      = editing.llm      || { provider: 'ollama', model: '', temperature: 0.7, max_tokens: 512 }
    editing.memory   = editing.memory   || { read_scopes: ['session'], write_scopes: ['session'], max_tokens: 20 }
    editing.schedule = editing.schedule || { cron: '' }
    editing.tools    = editing.tools    || []
    editing.skills    = editing.skills    || []
    editing.knowledge = editing.knowledge || []
    editing.agents    = editing.agents    || []
    editing.channels  = editing.channels  || []
    saveMsg = ''
    validationReport = null
  }

  function newAgent() {
    selected = null
    editing  = BLANK()
    saveMsg  = ''
    validationReport = null
  }

  async function validateEditing() {
    if (!editing || validating) return
    validating = true
    saveMsg = ''
    try {
      validationReport = await api.agents.validate(editing)
    } catch (e) {
      validationReport = {
        valid: false,
        errors: 1,
        warnings: 0,
        findings: [{ severity: 'error', field: 'validator', message: e.message }],
      }
    }
    validating = false
  }
  async function save() {
    if (!editing) return
    saving  = true
    saveMsg = ''
    try {
      if (selected) {
        await api.agents.update(selected.id, editing)
      } else {
        await api.agents.create(editing)
      }
      await load()
      saveMsg  = '✓ Saved'
      const found = agents.find(a => a.id === editing.id)
      if (found) select(found)
    } catch (e) {
      saveMsg = '✗ ' + e.message
    }
    saving = false
  }

  async function toggleEnabled(agent, e) {
    e.stopPropagation()
    if (isSystemAgent(agent)) return
    try {
      agent.enabled ? await api.agents.disable(agent.id)
                    : await api.agents.enable(agent.id)
      await load()
      if (selected?.id === agent.id) {
        const found = agents.find(a => a.id === agent.id)
        if (found) select(found)
      }
    } catch (e) { error = e.message }
  }

  async function deleteAgent() {
    if (!selected) return
    if (editingProtected) {
      error = 'System agent is protected'
      return
    }
    if (!confirm(`Delete agent "${selected.id}"? This cannot be undone.`)) return
    deleting = true
    try {
      await api.agents.delete(selected.id)
      editing = null; selected = null
      await load()
    } catch (e) { error = e.message }
    deleting = false
  }

  // ── Inline playground ─────────────────────────────────────────────────────
  // Right-side panel that chats with the currently-selected agent without
  // forcing the user to navigate to the standalone Chat page. Mirrors
  // Langflow's "Playground" sidebar that opens over the canvas — the win is
  // edit-prompt → save → chat in one screen without losing scroll/context.
  //
  // Conversation state is keyed by agent.id so switching between agents
  // preserves a separate transcript per agent (matches the standalone Chat
  // page's mental model). State is intentionally local (not in a global
  // store) so the inline playground and the full Chat page don't fight over
  // the same buffer.
  let showPlay        = false
  let playByAgent     = {}     // agentId → [{role, text, ts}]
  let playInput       = ''
  let playSending     = false
  let playError       = ''
  let playMsgListEl
  let playOverridesOpen = false
  let playUseOverrides = false
  let playProvider = ''
  let playModel = ''
  let playTemperature = ''
  let playMaxTokens = ''
  let playMaxTurns = ''
  let playToolChoice = ''

  // Derived: the current agent's transcript. We assign through the map so
  // Svelte sees the reactive change.
  $: playMessages = (selected ? (playByAgent[selected.id] || []) : [])

  function appendPlayMsg(agentId, msg) {
    const prev = playByAgent[agentId] || []
    playByAgent = { ...playByAgent, [agentId]: [...prev, msg] }
  }

  async function playSend() {
    if (!selected) return
    const text = playInput.trim()
    if (!text || playSending) return
    playInput   = ''
    playError   = ''
    const agentId = selected.id
    appendPlayMsg(agentId, { role: 'user', text, ts: new Date() })
    playSending = true
    await scrollPlayBottom()
    try {
      const res = await api.chat(agentId, text, 'gui-playground', playgroundOverrides())
      appendPlayMsg(agentId, { role: 'assistant', text: res.reply, ts: new Date() })
    } catch (e) {
      playError = e.message
      appendPlayMsg(agentId, { role: 'system', text: '⚠ ' + e.message, ts: new Date() })
    }
    playSending = false
    await scrollPlayBottom()
  }

  async function scrollPlayBottom() {
    // Tick is imported below
    await tick()
    if (playMsgListEl) playMsgListEl.scrollTop = playMsgListEl.scrollHeight
  }

  function playKeydown(e) {
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); playSend() }
  }

  function playgroundOverrides() {
    if (!playUseOverrides) return null
    const overrides = {}
    if (playProvider.trim()) overrides.provider = playProvider.trim()
    if (playModel.trim()) overrides.model = playModel.trim()
    if (playTemperature !== '' && !Number.isNaN(Number(playTemperature))) overrides.temperature = Number(playTemperature)
    if (playMaxTokens !== '' && Number(playMaxTokens) > 0) overrides.max_tokens = Number(playMaxTokens)
    if (playMaxTurns !== '' && Number(playMaxTurns) > 0) overrides.max_turns = Number(playMaxTurns)
    if (playToolChoice.trim()) overrides.tool_choice = playToolChoice.trim()
    return Object.keys(overrides).length ? overrides : null
  }

  function useSelectedLLMForPlayground() {
    if (!selected) return
    playProvider = selected.llm?.provider || ''
    playModel = selected.llm?.model || ''
    playTemperature = selected.llm?.temperature ?? ''
    playMaxTokens = selected.llm?.max_tokens || ''
    playMaxTurns = selected.max_turns || ''
    playToolChoice = selected.llm?.tool_choice || ''
  }

  function clearPlayChat() {
    if (!selected) return
    playByAgent = { ...playByAgent, [selected.id]: [] }
    playError   = ''
  }

  function fmtTime(d) {
    try { return d.toLocaleTimeString() } catch { return '' }
  }

  // ── API export modal ─────────────────────────────────────────────────────
  // Shows ready-to-paste curl / Python / JS snippets for the selected agent's
  // /api/v1/chat endpoint with the active API key pre-filled. Inspired by
  // Langflow's "API" button — the demo always ends with "here's how to call
  // this from your code." Lowers the activation energy for users who came in
  // through the GUI but want to script against the agent later.
  let showExport   = false
  let exportTab    = 'curl'      // 'curl' | 'python' | 'js'

  $: gatewayOrigin = (typeof window !== 'undefined') ? window.location.origin : 'http://127.0.0.1:18789'
  $: apiKeyValue   = $apiKey || '<your-api-key>'

  $: curlSnippet = selected ? `curl -X POST ${gatewayOrigin}/api/v1/chat \\
  -H 'Content-Type: application/json' \\
  -H 'Authorization: Bearer ${apiKeyValue}' \\
  -d '{
    "agent_id": "${selected.id}",
    "user_id":  "api-user",
    "text":     "hello"
  }'` : ''

  $: pythonSnippet = selected ? `import requests

resp = requests.post(
    "${gatewayOrigin}/api/v1/chat",
    headers={"Authorization": "Bearer ${apiKeyValue}"},
    json={
        "agent_id": "${selected.id}",
        "user_id":  "api-user",
        "text":     "hello",
    },
    timeout=120,
)
resp.raise_for_status()
print(resp.json()["reply"])` : ''

  $: jsSnippet = selected ? `const res = await fetch("${gatewayOrigin}/api/v1/chat", {
  method: "POST",
  headers: {
    "Content-Type":  "application/json",
    "Authorization": "Bearer ${apiKeyValue}",
  },
  body: JSON.stringify({
    agent_id: "${selected.id}",
    user_id:  "api-user",
    text:     "hello",
  }),
});
const { reply } = await res.json();
console.log(reply);` : ''

  $: activeSnippet = exportTab === 'python' ? pythonSnippet
                   : exportTab === 'js'     ? jsSnippet
                   : curlSnippet

  function copySnippet() {
    if (!activeSnippet) return
    navigator.clipboard?.writeText(activeSnippet)
  }

  // ── Templates ("New from template" modal) ────────────────────────────────
  // Loaded on demand when the user opens the picker. Each entry has the shape
  // { name, display_name, description, tags, source, definition } from the
  // /api/v1/templates endpoint. The `source` is "embedded" or "user" — we
  // show a small badge so users can tell built-ins from their own.
  let showTemplates    = false
  let templates        = []
  let templatesLoading = false
  let templatesError   = ''
  let instantiating    = ''   // name of the template currently being instantiated

  async function openTemplates() {
    showTemplates    = true
    templatesError   = ''
    if (templates.length === 0) {
      templatesLoading = true
      try {
        const res = await api.templates.list()
        templates = res.templates || []
      } catch (e) {
        templatesError = e.message
      }
      templatesLoading = false
    }
  }

  async function useTemplate(t) {
    instantiating = t.name
    try {
      const def = await api.templates.instantiate(t.name)
      await load()                              // refresh agent list
      const fresh = agents.find(a => a.id === def.id)
      if (fresh) select(fresh)                  // open it in the editor
      showTemplates = false
    } catch (e) {
      templatesError = e.message
    }
    instantiating = ''
  }

  onMount(() => { load(); loadCatalog(); loadProviders(); loadLookups() })
</script>

<div class="page">
  <div class="page-header">
    <h1>Agents</h1>
    <div class="hdr-actions">
      <button class="btn-secondary" on:click={openTemplates}>📋 From template…</button>
      <button class="btn-primary"   on:click={newAgent}>+ New Agent</button>
    </div>
  </div>

  {#if error}
    <div class="banner err">⚠ {error}</div>
  {/if}

  <div class="split">
    <!-- ── Agent list ── -->
    <div class="list-col">
      {#if agents.length === 0}
        <div class="empty">No agents loaded.<br>Click "New Agent" to create one.</div>
      {/if}
      {#each agents as agent}
        <div class="agent-card" class:active={selected?.id === agent.id}
             role="button"
             tabindex="0"
             on:click={() => select(agent)}
             on:keydown={(e) => (e.key === 'Enter' || e.key === ' ') && (e.preventDefault(), select(agent))}>
          <div>
            <div class="agent-name">{agent.name || agent.id}</div>
            <div class="agent-meta">
              {agent.trigger} · {agent.llm?.provider || 'ollama'}/{agent.llm?.model || '?'}
            </div>
          </div>
          <button class="toggle" class:on={agent.enabled}
                  title={agent.enabled ? 'Disable' : 'Enable'}
                  disabled={isSystemAgent(agent)}
                  on:click={(e) => toggleEnabled(agent, e)}>
            {agent.enabled ? '●' : '○'}
          </button>
        </div>
      {/each}
    </div>

    <!-- ── Editor ── -->
    <div class="editor-col">
      {#if !editing}
        <div class="empty-panel">Select an agent or create a new one.</div>
      {:else}
        <div class="editor">
          <div class="editor-hdr">
            <span>{selected ? 'Editing: ' + selected.id : 'New Agent'}</span>
            <div class="hdr-actions">
              {#if selected}
                <button class="btn-secondary" on:click={() => showExport = true}
                        title="Show API snippets for this agent">&lt;/&gt; API</button>
                <button class="btn-secondary" on:click={() => showPlay = !showPlay}
                        class:on={showPlay}
                        title="Toggle inline chat panel">
                  {showPlay ? '× Close' : '💬 Test'}
                </button>
                <button class="btn-danger" on:click={deleteAgent} disabled={deleting || editingProtected}>
                  {deleting ? '…' : 'Delete'}
                </button>
              {/if}
              <button class="btn-secondary" on:click={validateEditing} disabled={validating}>
                {validating ? 'Checking…' : 'Validate'}
              </button>
              <button class="btn-primary" on:click={save} disabled={saving}>
                {saving ? 'Saving…' : 'Save'}
              </button>
              {#if saveMsg}
                <span class="save-msg" class:ok={saveMsg.startsWith('✓')}>{saveMsg}</span>
              {/if}
            </div>
          </div>

          <div class="fields">
            {#if validationReport}
              <div class="validation-panel" class:ok={validationReport.valid && validationReport.warnings === 0}
                   class:warn={validationReport.valid && validationReport.warnings > 0}
                   class:fail={!validationReport.valid}>
                <div class="validation-head">
                  <span>
                    {validationReport.valid ? (validationReport.warnings ? 'Validation warnings' : 'Validation passed') : 'Validation failed'}
                  </span>
                  <span>{validationReport.errors || 0} errors · {validationReport.warnings || 0} warnings</span>
                </div>
                {#if (validationReport.findings || []).length === 0}
                  <div class="validation-empty">No findings.</div>
                {:else}
                  <div class="validation-list">
                    {#each validationReport.findings as finding}
                      <div class="validation-item" class:error={finding.severity === 'error'}>
                        <div class="validation-line">
                          <span class="severity">{finding.severity}</span>
                          <code>{finding.field}</code>
                          <span>{finding.message}</span>
                        </div>
                        {#if finding.suggestion}
                          <div class="validation-suggestion">{finding.suggestion}</div>
                        {/if}
                        {#if finding.alternatives?.length}
                          <div class="validation-alts">
                            {#each finding.alternatives as alt}<span>{alt}</span>{/each}
                          </div>
                        {/if}
                      </div>
                    {/each}
                  </div>
                {/if}
              </div>
            {/if}

            <div class="row-2">
              <div class="field">
                <span class="field-label">ID <span class="req">*</span></span>
                <input bind:value={editing.id} placeholder="my-agent"
                       disabled={!!selected} />
              </div>
              <div class="field">
                <span class="field-label">Name</span>
                <input bind:value={editing.name} placeholder="My Agent" />
              </div>
            </div>

            <div class="field">
              <span class="field-label">Description</span>
              <input bind:value={editing.description} placeholder="What this agent does" />
            </div>

            <div class="row-2">
              <div class="field">
                <span class="field-label">Trigger</span>
                <select bind:value={editing.trigger}>
                  <option value="channel">channel (HTTP / Slack / Telegram…)</option>
                  <option value="cron">cron (scheduled)</option>
                  <option value="oneshot">oneshot (run once at startup)</option>
                  <option value="webhook">webhook</option>
                </select>
              </div>
              <div class="field">
                <span class="field-label">Enabled</span>
                <select bind:value={editing.enabled} disabled={editingProtected}>
                  <option value={true}>Yes</option>
                  <option value={false}>No</option>
                </select>
              </div>
            </div>

            {#if editing.trigger === 'cron'}
              <div class="field">
                <span class="field-label">Cron expression</span>
                <input bind:value={editing.schedule.cron}
                       placeholder="0 9 * * *  (9 AM every day)" />
              </div>
            {/if}

            <div class="field">
              <span class="field-label">System Prompt</span>
              <textarea bind:value={editing.system_prompt} rows="7"
                        placeholder="You are a helpful Soulacy agent…"></textarea>
            </div>

            <!-- ── Persona blocks (identity / personality / non-negotiables) ───────
                 Optional structured sections that are wrapped into the
                 system prompt with consistent framing across every agent.
                 Operators can leave them empty for a "classic" agent.    -->
            <div class="sep">Persona <span class="sep-hint">(optional — leave empty for a classic agent)</span></div>

            <details class="persona-block" open={!!editing.identity}>
              <summary>
                <span class="persona-title">Identity</span>
                <span class="persona-sub">Who is the agent? Used at the top of the prompt so the model has a clear self-concept.</span>
              </summary>
              <div class="persona-body">
                <div class="field">
                  <span class="field-label">
                    Role
                    <small class="field-hint">Their job title or function. Example: "senior research analyst"</small>
                  </span>
                  <input type="text"
                         value={editing.identity?.role || ''}
                         on:input={(e) => ensurePersona('identity', { role: e.target.value })}
                         placeholder="senior research analyst" />
                </div>
                <div class="field">
                  <span class="field-label">
                    Audience
                    <small class="field-hint">Who they're talking to. Helps the model pitch vocabulary and depth.</small>
                  </span>
                  <input type="text"
                         value={editing.identity?.audience || ''}
                         on:input={(e) => ensurePersona('identity', { audience: e.target.value })}
                         placeholder="institutional investors" />
                </div>
                <div class="field">
                  <span class="field-label">
                    Expertise
                    <small class="field-hint">Comma-separated topics. Example: "macroeconomics, monetary policy"</small>
                  </span>
                  <input type="text"
                         value={(editing.identity?.expertise || []).join(', ')}
                         on:input={(e) => ensurePersona('identity', { expertise: csvToArr(e.target.value) })}
                         placeholder="macroeconomics, monetary policy" />
                </div>
                <div class="field">
                  <span class="field-label">
                    Backstory <small class="field-optional">(rarely needed)</small>
                    <small class="field-hint">Only fill this if a backstory materially changes behavior.</small>
                  </span>
                  <textarea rows="2"
                            value={editing.identity?.backstory || ''}
                            on:input={(e) => ensurePersona('identity', { backstory: e.target.value })}
                            placeholder=""></textarea>
                </div>
              </div>
            </details>

            <details class="persona-block" open={!!editing.personality}>
              <summary>
                <span class="persona-title">Personality</span>
                <span class="persona-sub">How they speak. Tone, voice, and soft style preferences. Not enforced — for hard rules use Non-Negotiables.</span>
              </summary>
              <div class="persona-body">
                <div class="field">
                  <span class="field-label">
                    Tone
                    <small class="field-hint">Emotional/professional register. Example: "concise, slightly dry"</small>
                  </span>
                  <input type="text"
                         value={editing.personality?.tone || ''}
                         on:input={(e) => ensurePersona('personality', { tone: e.target.value })}
                         placeholder="concise, slightly dry" />
                </div>
                <div class="field">
                  <span class="field-label">
                    Voice
                    <small class="field-hint">Structural style. Example: "third-person observations, never 'I think'"</small>
                  </span>
                  <input type="text"
                         value={editing.personality?.voice || ''}
                         on:input={(e) => ensurePersona('personality', { voice: e.target.value })}
                         placeholder="third-person observations, never 'I think'" />
                </div>
                <div class="field">
                  <span class="field-label">
                    Prefer
                    <small class="field-hint">Comma-separated. Things to lean into. Example: "active voice, named sources"</small>
                  </span>
                  <input type="text"
                         value={(editing.personality?.prefer || []).join(', ')}
                         on:input={(e) => ensurePersona('personality', { prefer: csvToArr(e.target.value) })}
                         placeholder="active voice, named sources" />
                </div>
                <div class="field">
                  <span class="field-label">
                    Avoid
                    <small class="field-hint">Comma-separated. Things to skip. Example: "exclamation marks, hedging, emojis"</small>
                  </span>
                  <input type="text"
                         value={(editing.personality?.avoid || []).join(', ')}
                         on:input={(e) => ensurePersona('personality', { avoid: csvToArr(e.target.value) })}
                         placeholder="exclamation marks, hedging, emojis" />
                </div>
              </div>
            </details>

            <details class="persona-block persona-rules" open={!!editing.non_negotiables}>
              <summary>
                <span class="persona-title">Non-negotiables <span class="persona-tag">HARD RULES</span></span>
                <span class="persona-sub">Rules the agent must follow even if the user asks it to do otherwise. Rendered with extra weight in the prompt.</span>
              </summary>
              <div class="persona-body">
                <div class="field">
                  <span class="field-label persona-must-label">
                    MUST
                    <small class="field-hint">Things the agent must ALWAYS do. One per line.</small>
                  </span>
                  <textarea rows="3"
                            value={(editing.non_negotiables?.must || []).join('\n')}
                            on:input={(e) => ensurePersona('non_negotiables', { must: linesToArr(e.target.value) })}
                            placeholder={`cite every numeric claim with [n]\nrespond in the same language as the most recent user message`}></textarea>
                </div>
                <div class="field">
                  <span class="field-label persona-mustnot-label">
                    MUST NOT
                    <small class="field-hint">Things the agent must NEVER do. One per line.</small>
                  </span>
                  <textarea rows="3"
                            value={(editing.non_negotiables?.must_not || []).join('\n')}
                            on:input={(e) => ensurePersona('non_negotiables', { must_not: linesToArr(e.target.value) })}
                            placeholder={`reveal any environment variable\ngive legal or medical advice\nclaim to be human if asked directly`}></textarea>
                </div>
                <div class="field-row3">
                  <div class="field">
                    <span class="field-label">
                      Format
                      <small class="field-hint">Mechanical output format.</small>
                    </span>
                    <select value={editing.non_negotiables?.output_constraints?.format || ''}
                            on:change={(e) => ensurePersona('non_negotiables', { output_constraints: { ...(editing.non_negotiables?.output_constraints || {}), format: e.target.value } })}>
                      <option value="">(any)</option>
                      <option value="markdown">markdown</option>
                      <option value="plain">plain</option>
                      <option value="json">json</option>
                      <option value="code">code</option>
                    </select>
                  </div>
                  <div class="field">
                    <span class="field-label">
                      Max words
                      <small class="field-hint">0 = no limit.</small>
                    </span>
                    <input type="number" min="0" max="10000"
                           value={editing.non_negotiables?.output_constraints?.max_length || 0}
                           on:input={(e) => ensurePersona('non_negotiables', { output_constraints: { ...(editing.non_negotiables?.output_constraints || {}), max_length: parseInt(e.target.value || 0) } })} />
                  </div>
                  <div class="field">
                    <span class="field-label">
                      Min words
                      <small class="field-hint">0 = no minimum.</small>
                    </span>
                    <input type="number" min="0" max="10000"
                           value={editing.non_negotiables?.output_constraints?.min_length || 0}
                           on:input={(e) => ensurePersona('non_negotiables', { output_constraints: { ...(editing.non_negotiables?.output_constraints || {}), min_length: parseInt(e.target.value || 0) } })} />
                  </div>
                </div>
                <div class="persona-note">
                  <strong>Note:</strong> in this release these rules become extra-weighted text in the system prompt. Post-LLM validation (auto-rewrite on violation) is on the roadmap.
                </div>
              </div>
            </details>

            <div class="sep">LLM</div>

            <div class="row-3">
              <div class="field">
                <span class="field-label">Provider</span>
                <select bind:value={editing.llm.provider} on:change={onProviderChange}>
                  {#if enabledProviders.length === 0}
                    <option value={editing.llm.provider}>{editing.llm.provider || 'ollama'}</option>
                  {:else}
                    {#if editing.llm.provider && !enabledProviders.some(p => p.id === editing.llm.provider)}
                      <option value={editing.llm.provider}>{editing.llm.provider} (disabled/unregistered)</option>
                    {/if}
                    {#each enabledProviders as p}
                      <option value={p.id}>
                        {p.id}
                      </option>
                    {/each}
                  {/if}
                </select>
              </div>
              <div class="field">
                <span class="field-label">
                  Model
                  {#if modelsLoading[editing.llm.provider]}
                    <span class="mloading">loading…</span>
                  {:else if modelsError[editing.llm.provider]}
                    <span class="merror" title={modelsError[editing.llm.provider]}>(can't reach provider)</span>
                  {:else if modelOptions.length > 0}
                    <span class="mhint">{modelOptions.length} options</span>
                  {/if}
                </span>
                <!-- Keyed {#each (m)} prevents Svelte from mutating <option> values
                     in-place during list updates. Current model is always at [0]
                     (from modelOptions), so bind:value always has a match and the
                     browser never resets to the first option. -->
                <select bind:value={editing.llm.model}>
                  {#if !editing.llm.model}
                    <option value="">— pick a model —</option>
                  {/if}
                  {#each modelOptions as m (m)}
                    <option value={m}>{m}</option>
                  {/each}
                  <option value="__custom__">Custom (type below)…</option>
                </select>
                {#if editing.llm.model === '__custom__'}
                  <input bind:value={editing.llm.model}
                         placeholder="Enter model name"
                         on:focus={() => editing.llm.model = ''} />
                {/if}
              </div>
              <div class="field">
                <span class="field-label">Max tokens</span>
                <input type="number" bind:value={editing.llm.max_tokens} min="64" max="8192" />
              </div>
            </div>

            <div class="row-2">
              <div class="field">
                <span class="field-label">Temperature</span>
                <input type="number" bind:value={editing.llm.temperature}
                       min="0" max="2" step="0.05" />
              </div>
              <div class="field">
                <span class="field-label">Max turns</span>
                <input type="number" bind:value={editing.max_turns} min="1" max="50" />
              </div>
            </div>

            <div class="field">
              <span class="field-label">Tool choice <span class="optional">(controls turn-1 tool selection — agent__&lt;peer&gt; triggers the engine's auto-delegate path)</span></span>
              <ChipPicker
                value={editing.llm.tool_choice ? [editing.llm.tool_choice] : []}
                options={toolChoiceOptions}
                placeholder="(default — auto)"
                single={true}
                allowFreeform={true}
                on:change={(e) => editing.llm.tool_choice = e.detail[0] || ''}
              />
            </div>

            <div class="sep">Memory</div>
            <div class="row-2">
              <div class="field">
                <span class="field-label">Read scopes <span class="optional">(which memory tiers the agent loads as context)</span></span>
                <ChipPicker
                  value={editing.memory?.read_scopes || []}
                  options={memoryScopeOptions}
                  placeholder="Pick scopes (typically: session)"
                  on:change={(e) => { editing.memory = editing.memory || {}; editing.memory.read_scopes = e.detail }}
                />
              </div>
              <div class="field">
                <span class="field-label">Write scopes <span class="optional">(which memory tiers the agent persists into)</span></span>
                <ChipPicker
                  value={editing.memory?.write_scopes || []}
                  options={memoryScopeOptions}
                  placeholder="Pick scopes (typically: session)"
                  on:change={(e) => { editing.memory = editing.memory || {}; editing.memory.write_scopes = e.detail }}
                />
              </div>
            </div>
            <div class="field">
              <span class="field-label">Memory max tokens <span class="optional">(how many recent entries to inject into context)</span></span>
              <input type="number" bind:value={editing.memory.max_tokens} min="0" max="100000" />
            </div>

            <!-- ── Reasoning loop ── -->
            <div class="sep">Reasoning loop <span class="optional">(multi-step ReAct / Plan-Execute)</span></div>
            <div class="field">
              <span class="field-label">Strategy</span>
              <div class="strategy-cards">
                {#each [
                  { val: '',             icon: '⚡', title: 'None',          desc: 'Classic single-call (default)' },
                  { val: 'react',        icon: '🔄', title: 'ReAct',         desc: 'Iterative think → act → observe' },
                  { val: 'plan_execute', icon: '📋', title: 'Plan-Execute',  desc: 'Decompose → execute plan steps' },
                ] as s}
                  <button
                    class="strategy-card {(editing.reasoning?.strategy||'') === s.val ? 'active' : ''}"
                    on:click={() => { editing.reasoning = editing.reasoning || {}; editing.reasoning.strategy = s.val; editing = editing }}
                  >
                    <span class="sc-icon">{s.icon}</span>
                    <span class="sc-title">{s.title}</span>
                    <span class="sc-desc">{s.desc}</span>
                  </button>
                {/each}
              </div>
            </div>

            {#if editing.reasoning?.strategy}
              <div class="field-row">
                <div class="field">
                  <span class="field-label">Max steps <span class="optional">(default 8)</span></span>
                  <input type="number" min="1" max="50"
                    value={editing.reasoning?.max_steps || ''}
                    on:input={e => { editing.reasoning = editing.reasoning||{}; editing.reasoning.max_steps = Number(e.target.value)||0 }} />
                </div>
                {#if editing.reasoning?.strategy === 'plan_execute'}
                  <div class="field">
                    <span class="field-label">Max plan steps <span class="optional">(default 6)</span></span>
                    <input type="number" min="1" max="20"
                      value={editing.reasoning?.max_plan_steps || ''}
                      on:input={e => { editing.reasoning = editing.reasoning||{}; editing.reasoning.max_plan_steps = Number(e.target.value)||0 }} />
                  </div>
                {/if}
                <div class="field">
                  <span class="field-label">Step timeout <span class="optional">(e.g. 30s)</span></span>
                  <input type="text" placeholder="30s"
                    value={editing.reasoning?.step_timeout || ''}
                    on:input={e => { editing.reasoning = editing.reasoning||{}; editing.reasoning.step_timeout = e.target.value }} />
                </div>
                <div class="field">
                  <span class="field-label">Total timeout <span class="optional">(e.g. 180s)</span></span>
                  <input type="text" placeholder="180s"
                    value={editing.reasoning?.total_timeout || ''}
                    on:input={e => { editing.reasoning = editing.reasoning||{}; editing.reasoning.total_timeout = e.target.value }} />
                </div>
              </div>
            {/if}

            <!-- ── Brain memory ── -->
            <div class="sep">Brain memory <span class="optional">(long-term episodic / procedural / semantic)</span></div>
            <div class="brain-mem-grid">
              {#each [
                { key: 'episodic',   icon: '🕐', label: 'Episodic',   desc: 'Task history. Injected as "Recent task history".' },
                { key: 'semantic',   icon: '🔍', label: 'Semantic',   desc: 'Knowledge chunks (vector search).' },
                { key: 'procedural', icon: '📋', label: 'Procedural', desc: 'Operating rules the agent learns over time.' },
              ] as layer}
                {@const cfg = editing.brain_memory?.[layer.key] || {}}
                <div class="bm-card {cfg.enabled ? 'enabled' : ''}">
                  <div class="bm-header">
                    <span class="bm-icon">{layer.icon}</span>
                    <span class="bm-label">{layer.label}</span>
                    <label class="toggle-sm">
                      <input type="checkbox" checked={!!cfg.enabled}
                        on:change={e => {
                          editing.brain_memory = editing.brain_memory || {}
                          editing.brain_memory[layer.key] = editing.brain_memory[layer.key] || {}
                          editing.brain_memory[layer.key].enabled = e.target.checked
                          editing = editing
                        }} />
                      <span class="toggle-track-sm"></span>
                    </label>
                  </div>
                  <div class="bm-desc">{layer.desc}</div>
                  {#if cfg.enabled}
                    <div class="bm-fields">
                      <div class="bm-field">
                        <span class="bm-field-lbl">Max inject</span>
                        <input class="bm-num" type="number" min="1" max="50"
                          value={cfg.max_inject || ''}
                          on:input={e => {
                            editing.brain_memory[layer.key].max_inject = Number(e.target.value)||0
                            editing = editing
                          }} />
                      </div>
                      {#if layer.key === 'procedural'}
                        <label class="bm-check">
                          <input type="checkbox" checked={!!cfg.auto_update}
                            on:change={e => {
                              editing.brain_memory[layer.key].auto_update = e.target.checked
                              editing = editing
                            }} />
                          <span>Auto-update after each task</span>
                        </label>
                      {/if}
                    </div>
                  {/if}
                </div>
              {/each}
            </div>

            {#if editing.trigger === 'channel'}
              <div class="sep">Channels</div>
              <div class="field">
                <span class="field-label">Bound channels — pick from registered channel adapters</span>
                <ChipPicker
                  value={editing.channels || []}
                  options={channelOptions}
                  placeholder="Type to search (http, telegram, slack…)"
                  allowFreeform={true}
                  on:change={(e) => editing.channels = e.detail}
                />
              </div>
            {/if}

            <div class="sep">
              Tools
              <button class="add-btn" type="button" on:click={addTool}>+ Add tool</button>
            </div>

            {#if (editing.tools || []).length === 0}
              <div class="tools-empty">
                No tools wired. Click <strong>+ Add tool</strong> to give this agent a Python script, MCP tool, or built-in.
                {#if catalog.python_tools.length > 0}
                  &nbsp;Available Python scripts: <em>{catalog.python_tools.map(t => t.name).join(', ')}</em>
                {/if}
              </div>
            {:else}
              {#each editing.tools as tool, i (i)}
                <div class="tool-card">
                  <div class="tool-hdr">
                    <span class="tool-idx">#{i + 1}</span>
                    <input class="tool-name-input"
                           bind:value={tool.name}
                           placeholder="tool_name (snake_case)" />
                    <div class="tool-ops">
                      <button title="Move up"   on:click={() => moveTool(i, -1)} disabled={i === 0}>↑</button>
                      <button title="Move down" on:click={() => moveTool(i, +1)} disabled={i === editing.tools.length - 1}>↓</button>
                      <button title="Remove" class="rm" on:click={() => removeTool(i)}>✕</button>
                    </div>
                  </div>

                  <div class="field">
                    <span class="field-label">Python file — type a path or 📂 Browse the tool catalog</span>
                    <FilePicker
                      value={tool.python_file || ''}
                      options={pythonFileOptions}
                      placeholder="~/.soulacy/tools/your_tool.py"
                      on:change={(e) => { editing.tools[i].python_file = e.detail; editing = editing }}
                      on:pick={(e) => onPythonFilePicked(i, e.detail.value)}
                    />
                  </div>

                  <div class="field">
                    <span class="field-label">Description (shown to the LLM)</span>
                    <input bind:value={tool.description}
                           placeholder="What this tool does. The LLM picks tools by description." />
                  </div>

                  <div class="field">
                    <span class="field-label">Timeout <span class="optional">(optional — overrides global; e.g. 30m, 1h)</span></span>
                    <input bind:value={tool.timeout}
                           placeholder="30s (defaults to runtime.tool_timeout)" />
                  </div>

                  <div class="field">
                    <span class="field-label">Parameters schema (JSON)</span>
                    <textarea rows="5"
                              value={paramsJson(tool)}
                              on:input={(e) => updateParams(i, e.target.value)}
                              spellcheck="false"></textarea>
                  </div>
                </div>
              {/each}
            {/if}

            {#if (catalog.mcp_tools || []).length > 0}
              <div class="catalog-hint">
                <strong>MCP tools auto-injected:</strong>
                {#each catalog.mcp_tools.slice(0, 8) as t}
                  <span class="chip">{t.full_name}</span>
                {/each}
                {#if catalog.mcp_tools.length > 8}<span class="more">+{catalog.mcp_tools.length - 8} more</span>{/if}
              </div>
            {/if}

            <div class="sep">Skills</div>
            <div class="field">
              <span class="field-label">Skills available to this agent — pick from installed skills</span>
              <ChipPicker
                value={editing.skills || []}
                options={skillOptions}
                placeholder={availableSkills.length === 0 ? 'No skills installed' : 'Type to search…'}
                allowFreeform={true}
                on:change={(e) => editing.skills = e.detail}
              />
            </div>

            <div class="sep">Knowledge bases</div>
            <div class="field">
              <span class="field-label">KBs the agent may search via <code>kb_search</code> — pick from created KBs</span>
              <ChipPicker
                value={editing.knowledge || []}
                options={kbOptions}
                placeholder={availableKBs.length === 0 ? 'No knowledge bases yet — create one on the Knowledge page' : 'Type to search…'}
                allowFreeform={true}
                on:change={(e) => editing.knowledge = e.detail}
              />
            </div>

            <div class="sep">Peer agents</div>
            <div class="field">
              <span class="field-label">Other agents this agent may invoke as <code>agent__&lt;id&gt;</code> tools — pick from loaded agents</span>
              <ChipPicker
                value={editing.agents || []}
                options={peerAgentOptions}
                placeholder={peerAgentOptions.length === 0 ? 'No other agents loaded' : 'Type to search…'}
                allowFreeform={true}
                on:change={(e) => editing.agents = e.detail}
              />
            </div>

            <div class="sep">Built-in tools <span class="optional">(advanced)</span></div>
            <div class="field">
              <div class="radio-row">
                <label class="radio-opt">
                  <input type="radio" name="builtins-mode" value="default"
                         checked={builtinsMode() === 'default'}
                         on:change={() => { delete editing.builtins; editing = editing }} />
                  Default <span class="optional">(all gated built-ins auto-injected)</span>
                </label>
                <label class="radio-opt">
                  <input type="radio" name="builtins-mode" value="none"
                         checked={builtinsMode() === 'none'}
                         on:change={() => { editing.builtins = []; editing = editing }} />
                  None <span class="optional">(peer-only orchestrator)</span>
                </label>
                <label class="radio-opt">
                  <input type="radio" name="builtins-mode" value="restricted"
                         checked={builtinsMode() === 'restricted'}
                         on:change={() => { editing.builtins = editing.builtins?.length ? editing.builtins : []; editing = editing }} />
                  Restricted to:
                </label>
              </div>
              {#if builtinsMode() === 'restricted'}
                <ChipPicker
                  value={editing.builtins || []}
                  options={builtinOptions}
                  placeholder="Pick which built-ins this agent can use"
                  on:change={(e) => editing.builtins = e.detail}
                />
              {/if}
            </div>
          </div>
        </div>
      {/if}
    </div>

    <!-- ── Inline playground (right column) ── -->
    {#if showPlay && selected}
      <div class="play-col">
        <div class="play-hdr">
          <span>Playground · <code>{selected.id}</code></span>
          <div class="play-hdr-actions">
            <button class="btn-secondary small" class:on={playOverridesOpen} on:click={() => playOverridesOpen = !playOverridesOpen}>Params</button>
            <button class="btn-secondary small" on:click={clearPlayChat}
                    disabled={playSending || playMessages.length === 0}>Clear</button>
          </div>
        </div>

        {#if playOverridesOpen}
          <div class="play-params">
            <label class="check-row">
              <input type="checkbox" bind:checked={playUseOverrides} />
              Override this run
            </label>
            <div class="param-actions">
              <button class="btn-secondary small" type="button" on:click={useSelectedLLMForPlayground}>Use agent defaults</button>
            </div>
            <div class="param-grid">
              <label>
                <span>Provider</span>
                <select bind:value={playProvider} disabled={!playUseOverrides}>
                  <option value="">unchanged</option>
                  {#if playProvider && !enabledProviders.some(p => p.id === playProvider)}
                    <option value={playProvider}>{playProvider} (disabled/unregistered)</option>
                  {/if}
                  {#each enabledProviders as p}
                    <option value={p.id}>{p.id}</option>
                  {/each}
                </select>
              </label>
              <label>
                <span>Model</span>
                <input bind:value={playModel} placeholder="unchanged" disabled={!playUseOverrides} />
              </label>
              <label>
                <span>Temperature</span>
                <input type="number" step="0.1" min="0" max="2" bind:value={playTemperature} placeholder="unchanged" disabled={!playUseOverrides} />
              </label>
              <label>
                <span>Max tokens</span>
                <input type="number" min="1" bind:value={playMaxTokens} placeholder="unchanged" disabled={!playUseOverrides} />
              </label>
              <label>
                <span>Max turns</span>
                <input type="number" min="1" max="100" bind:value={playMaxTurns} placeholder="unchanged" disabled={!playUseOverrides} />
              </label>
              <label>
                <span>Tool choice</span>
                <input bind:value={playToolChoice} placeholder="auto, none, required, tool name" disabled={!playUseOverrides} />
              </label>
            </div>
          </div>
        {/if}

        <div class="play-messages" bind:this={playMsgListEl}>
          {#if playMessages.length === 0}
            <div class="play-empty">
              Saved changes? Send a message to test this agent.<br>
              <span class="hint">Edits aren't picked up until you click Save above.</span>
            </div>
          {:else}
            {#each playMessages as msg}
              <div class="msg-row" class:user={msg.role==='user'} class:sys={msg.role==='system'}>
                <div class="bubble">
                  <div class="btext">{msg.text}</div>
                  <div class="btime">{fmtTime(msg.ts)}</div>
                </div>
              </div>
            {/each}
            {#if playSending}
              <div class="msg-row">
                <div class="bubble">
                  <div class="typing"><span/><span/><span/></div>
                </div>
              </div>
            {/if}
          {/if}
        </div>

        <div class="play-input">
          <textarea
            bind:value={playInput}
            on:keydown={playKeydown}
            placeholder="Message {selected.id}… (Enter to send)"
            rows="2"
            disabled={playSending}
          ></textarea>
          <button class="send-btn btn-primary"
                  on:click={playSend}
                  disabled={playSending || !playInput.trim()}>
            {playSending ? '…' : '↑'}
          </button>
        </div>
      </div>
    {/if}
  </div>
</div>

{#if showExport && selected}
  <div
    class="modal-bg"
    role="button"
    tabindex="0"
    aria-label="Close API snippets modal"
    on:click|self={() => showExport = false}
    on:keydown={(e) => e.key === 'Escape' && (showExport = false)}
  >
    <div class="modal wide">
      <h2>API · {selected.id}</h2>
      <div class="modal-sub">
        Call this agent from your own code. The Authorization header uses the
        API key you're logged in with — anyone holding it gets the same access.
      </div>

      <div class="tab-row">
        <button class="tab" class:active={exportTab==='curl'}   on:click={() => exportTab = 'curl'}>cURL</button>
        <button class="tab" class:active={exportTab==='python'} on:click={() => exportTab = 'python'}>Python</button>
        <button class="tab" class:active={exportTab==='js'}     on:click={() => exportTab = 'js'}>JavaScript</button>
        <div style="flex:1"></div>
        <button class="btn-secondary small" on:click={copySnippet}>Copy</button>
      </div>

      <pre class="snippet">{activeSnippet}</pre>

      <div class="modal-row" style="display:flex;justify-content:flex-end;">
        <button class="btn-secondary" on:click={() => showExport = false}>Close</button>
      </div>
    </div>
  </div>
{/if}

{#if showTemplates}
  <div
    class="modal-bg"
    role="button"
    tabindex="0"
    aria-label="Close template modal"
    on:click|self={() => { if (!instantiating) showTemplates = false }}
    on:keydown={(e) => e.key === 'Escape' && !instantiating && (showTemplates = false)}
  >
    <div class="modal">
      <h2>Start from a template</h2>
      <div class="modal-sub">
        Each template creates a working agent you can chat with immediately,
        then tweak. Drop your own templates into <code>~/.soulacy/templates/</code>
        to extend this list.
      </div>

      {#if templatesError}
        <div class="banner err">⚠ {templatesError}</div>
      {/if}

      {#if templatesLoading}
        <div class="tpl-empty">Loading templates…</div>
      {:else if templates.length === 0}
        <div class="tpl-empty">No templates available.</div>
      {:else}
        <div class="tpl-list">
          {#each templates as t (t.name)}
            <div class="tpl-card">
              <div class="tpl-body">
                <div class="tpl-title">
                  {t.display_name || t.name}
                  <span class="tpl-source-badge {t.source}">{t.source}</span>
                </div>
                {#if t.description}
                  <div class="tpl-desc">{t.description}</div>
                {/if}
                {#if t.tags?.length}
                  <div class="tpl-tags">
                    {#each t.tags as tag}<span class="tpl-tag">{tag}</span>{/each}
                  </div>
                {/if}
              </div>
              <button class="tpl-use"
                      disabled={!!instantiating}
                      on:click={() => useTemplate(t)}>
                {instantiating === t.name ? 'Creating…' : 'Use this'}
              </button>
            </div>
          {/each}
        </div>
      {/if}

      <div class="modal-row" style="display:flex;justify-content:flex-end;gap:.5rem;">
        <button class="btn-secondary" on:click={() => showTemplates = false}
                disabled={!!instantiating}>Cancel</button>
      </div>
    </div>
  </div>
{/if}

<style>
  .page        { padding: 1.5rem; display: flex; flex-direction: column; gap: 1.25rem; height: 100%; }
  .page-header { display: flex; align-items: center; justify-content: space-between; flex-shrink: 0; }
  .page-header h1 { font-size: 1.2rem; font-weight: 600; }

  .banner { padding: .7rem 1rem; border-radius: 8px; font-size: .85rem; flex-shrink: 0; }
  .err    { background: rgba(240,96,96,.1); border: 1px solid rgba(240,96,96,.3); color: #f06060; }

  .split    { display: flex; gap: 1rem; flex: 1; min-height: 0; }

  @media (max-width: 900px) {
    /* Stack list / editor / playground vertically on tablet & mobile */
    .split    { flex-direction: column; overflow-y: auto; }
    .list-col { width: 100%; max-height: 220px; flex-shrink: 0; }
    .play-col { width: 100%; flex-shrink: 0; }
  }
  @media (max-width: 640px) {
    .row-2, .row-3 { grid-template-columns: 1fr; }
    .param-grid    { grid-template-columns: 1fr; }
  }

  /* List */
  .list-col { width: 250px; flex-shrink: 0; overflow-y: auto; display: flex; flex-direction: column; gap: .45rem; }
  .agent-card {
    background: #141626; border: 1px solid #1a1e36; border-radius: 8px;
    padding: .7rem .85rem; cursor: pointer;
    display: flex; align-items: center; justify-content: space-between;
    transition: border-color .12s;
  }
  .agent-card:hover, .agent-card.active { border-color: #6c63ff; }
  .agent-name  { font-weight: 500; font-size: .875rem; }
  .agent-meta  { color: #6b7294; font-size: .72rem; margin-top: .15rem; }
  .toggle      { background: none; font-size: 1rem; color: #6b7294; padding: .15rem; }
  .toggle.on   { color: #4caf82; }
  .empty       { color: #6b7294; text-align: center; padding: 2rem .5rem; font-size: .875rem; line-height: 1.6; }

  /* Editor */
  .editor-col  { flex: 1; overflow-y: auto; min-width: 0; }
  .empty-panel { display: flex; align-items: center; justify-content: center; height: 200px; color: #6b7294; }

  .editor      { background: #141626; border: 1px solid #1a1e36; border-radius: 10px; overflow: hidden; }
  .editor-hdr  {
    display: flex; align-items: center; justify-content: space-between;
    padding: .8rem 1rem; border-bottom: 1px solid #1a1e36;
    font-weight: 600; font-size: .875rem; flex-shrink: 0;
  }
  .hdr-actions { display: flex; align-items: center; gap: .65rem; }
  .save-msg    { font-size: .8rem; color: #f06060; }
  .save-msg.ok { color: #4caf82; }

  .fields  { padding: 1rem; display: flex; flex-direction: column; gap: .8rem; }
  .field   { display: flex; flex-direction: column; gap: .3rem; }
  .field-label {
    font-size: .72rem; color: #6b7294; text-transform: uppercase;
    letter-spacing: .06em; font-weight: 600;
  }
  .validation-panel {
    border-radius: 8px; padding: .8rem; display: flex; flex-direction: column; gap: .6rem;
    background: #0e1020; border: 1px solid #2a2f4a;
  }
  .validation-panel.ok   { border-color: rgba(76,175,130,.35); background: rgba(76,175,130,.07); }
  .validation-panel.warn { border-color: rgba(240,192,96,.35); background: rgba(240,192,96,.07); }
  .validation-panel.fail { border-color: rgba(240,96,96,.35); background: rgba(240,96,96,.07); }
  .validation-head { display: flex; align-items: center; justify-content: space-between; font-size: .8rem; color: #c8cadf; font-weight: 600; }
  .validation-head span:last-child { color: #6b7294; font-weight: 500; }
  .validation-empty { color: #4caf82; font-size: .8rem; }
  .validation-list { display: flex; flex-direction: column; gap: .55rem; }
  .validation-item { border-top: 1px solid rgba(255,255,255,.06); padding-top: .55rem; }
  .validation-item:first-child { border-top: none; padding-top: 0; }
  .validation-line { display: flex; gap: .45rem; align-items: baseline; color: #c8cadf; font-size: .8rem; line-height: 1.45; }
  .validation-line code { font-size: .75rem; color: #8b85ff; background: #1a1e36; padding: .08rem .28rem; border-radius: 4px; }
  .severity { color: #f0c060; text-transform: uppercase; font-size: .66rem; font-weight: 700; min-width: 42px; }
  .validation-item.error .severity { color: #f06060; }
  .validation-suggestion { margin-left: 48px; margin-top: .25rem; color: #8a90b8; font-size: .76rem; line-height: 1.45; }
  .validation-alts { margin-left: 48px; margin-top: .35rem; display: flex; flex-wrap: wrap; gap: .3rem; }
  .validation-alts span { font-family: monospace; font-size: .68rem; color: #b0b5d8; background: #1a1e36; padding: .12rem .4rem; border-radius: 4px; }
  label    { font-size: .72rem; color: #6b7294; text-transform: uppercase; letter-spacing: .05em; }
  .mloading { color: #8b85ff; text-transform: none; font-weight: 500; margin-left: .35rem; }
  .merror   { color: #f0c060; text-transform: none; font-weight: 500; margin-left: .35rem; cursor: help; }
  .mhint    { color: #4caf82; text-transform: none; font-weight: 500; margin-left: .35rem; }
  .optional { color: #555a7a; text-transform: none; font-weight: 400; font-size: .68rem; letter-spacing: 0; margin-left: .25rem; }
  .req     { color: #f06060; }
  .row-2   { display: grid; grid-template-columns: 1fr 1fr; gap: .75rem; }
  .row-3   { display: grid; grid-template-columns: 1fr 1fr 1fr; gap: .75rem; }
  .sep {
    font-size: .7rem; color: #6b7294; text-transform: uppercase; letter-spacing: .08em;
    border-bottom: 1px solid #1a1e36; padding-bottom: .3rem; margin-top: .25rem;
    display: flex; align-items: center; justify-content: space-between;
  }
  .add-btn {
    background: rgba(108,99,255,.12); color: #8b85ff; border: 1px solid rgba(108,99,255,.35);
    padding: .25rem .6rem; border-radius: 6px; font-size: .72rem; font-weight: 600;
    text-transform: none; letter-spacing: 0;
  }
  .add-btn:hover { background: rgba(108,99,255,.2); }

  /* Radio rows for tri-state fields (e.g. builtins: default/none/restricted) */
  .radio-row { display: flex; flex-wrap: wrap; gap: .9rem; padding: .15rem 0 .3rem; }
  .radio-opt {
    display: inline-flex; align-items: center; gap: .35rem;
    font-size: .82rem; color: #c8cadf; cursor: pointer; user-select: none;
  }
  .radio-opt input[type="radio"] { width: auto; margin: 0; cursor: pointer; }
  .tools-empty {
    background: #0e1020; border: 1px dashed #2a2f4a; border-radius: 8px;
    padding: .8rem 1rem; color: #6b7294; font-size: .8rem; line-height: 1.55;
  }
  .tools-empty em { color: #8b85ff; font-style: normal; }
  .tool-card {
    background: #0e1020; border: 1px solid #1a1e36; border-radius: 8px;
    padding: .8rem; display: flex; flex-direction: column; gap: .55rem;
  }
  .tool-hdr { display: flex; align-items: center; gap: .5rem; }
  .tool-idx { color: #555a7a; font-size: .72rem; font-weight: 600; }
  .tool-name-input { flex: 1; font-family: monospace; font-size: .85rem; }
  .tool-ops { display: flex; gap: .25rem; }
  .tool-ops button {
    background: #1c1f35; color: #c8cadf; border: 1px solid #2a2f4a;
    width: 26px; height: 26px; border-radius: 5px; font-size: .8rem;
  }
  .tool-ops button:hover:not(:disabled) { background: #252840; }
  .tool-ops .rm { color: #f06060; border-color: rgba(240,96,96,.3); }
  .tool-ops .rm:hover { background: rgba(240,96,96,.12); }

  .catalog-hint {
    font-size: .72rem; color: #6b7294; padding: .5rem .6rem;
    background: rgba(76,175,130,.06); border: 1px solid rgba(76,175,130,.18);
    border-radius: 6px; display: flex; flex-wrap: wrap; align-items: center; gap: .4rem;
  }
  .catalog-hint strong { color: #4caf82; font-weight: 600; }
  .chip {
    font-family: monospace; font-size: .68rem; color: #b0b5d8;
    background: #1a1e36; padding: .1rem .4rem; border-radius: 4px;
  }
  .more { color: #555a7a; font-size: .72rem; }

  /* ── Inline playground ─────────────────────────────────────────────── */
  .play-col {
    width: 360px; flex-shrink: 0;
    background: #141626; border: 1px solid #1a1e36; border-radius: 10px;
    display: flex; flex-direction: column; min-height: 0; overflow: hidden;
  }
  .play-hdr {
    display: flex; align-items: center; justify-content: space-between;
    padding: .6rem .85rem; border-bottom: 1px solid #1a1e36; flex-shrink: 0;
    font-size: .82rem; color: #c8cadf;
  }
  .play-hdr-actions { display: flex; gap: .35rem; align-items: center; }
  .play-hdr code { font-family: monospace; color: #4caf82; }
  .play-params {
    border-bottom: 1px solid #1a1e36; padding: .65rem .75rem;
    display: flex; flex-direction: column; gap: .55rem; background: #101323;
  }
  .check-row { display: flex; align-items: center; gap: .45rem; font-size: .75rem; color: #c8cadf; }
  .param-actions { display: flex; justify-content: flex-end; }
  .param-grid { display: grid; grid-template-columns: 1fr 1fr; gap: .5rem; }
  .param-grid label { display: flex; flex-direction: column; gap: .25rem; min-width: 0; }
  .param-grid span { font-size: .68rem; color: #7b82a8; }
  .param-grid input, .param-grid select {
    width: 100%; min-width: 0; background: #1c1f35; color: #e8eaf6;
    border: 1px solid #2a2f4a; border-radius: 5px; padding: .38rem .45rem; font-size: .75rem;
  }
  .param-grid input:disabled, .param-grid select:disabled { opacity: .55; }
  .play-messages {
    flex: 1; overflow-y: auto; padding: .85rem;
    display: flex; flex-direction: column; gap: .65rem;
  }
  .play-empty {
    flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center;
    color: #6b7294; text-align: center; line-height: 1.6; font-size: .82rem;
  }
  .play-empty .hint { color: #555a7a; font-size: .72rem; margin-top: .35rem; }
  .play-input {
    display: flex; gap: .55rem; align-items: flex-end;
    padding: .55rem; border-top: 1px solid #1a1e36; flex-shrink: 0;
  }
  .play-input textarea {
    flex: 1; resize: none; font-size: .85rem;
    background: #1c1f35; color: #e8eaf6; border: 1px solid #2a2f4a;
    border-radius: 5px; padding: .45rem .55rem;
  }
  .send-btn {
    height: 38px; min-width: 44px; padding: 0; font-size: 1rem;
    align-self: flex-end; flex-shrink: 0; border-radius: 6px;
  }

  .msg-row       { display: flex; justify-content: flex-start; }
  .msg-row.user  { justify-content: flex-end; }
  .msg-row.sys   { justify-content: center; }
  .bubble {
    max-width: 80%; padding: .55rem .8rem; border-radius: 10px;
    display: flex; flex-direction: column; gap: .25rem;
    background: #1c1f35; border: 1px solid #2a2f4a;
    border-bottom-left-radius: 3px;
  }
  .msg-row.user .bubble {
    background: #5b52ef; border-color: transparent; color: #fff;
    border-bottom-left-radius: 10px; border-bottom-right-radius: 3px;
  }
  .msg-row.sys .bubble {
    background: rgba(240,96,96,.1); border-color: rgba(240,96,96,.3); color: #f06060;
  }
  .btext { font-size: .85rem; white-space: pre-wrap; word-break: break-word; line-height: 1.45; }
  .btime { font-size: .65rem; opacity: .55; align-self: flex-end; }

  .typing { display: flex; gap: 4px; align-items: center; height: 1.1rem; }
  .typing span {
    width: 5px; height: 5px; border-radius: 50%;
    background: #6b7294; animation: bounce 1.1s infinite;
  }
  .typing span:nth-child(2) { animation-delay: .18s; }
  .typing span:nth-child(3) { animation-delay: .36s; }
  @keyframes bounce {
    0%, 80%, 100% { transform: scale(.65); opacity: .4; }
    40%           { transform: scale(1);   opacity: 1;   }
  }

  /* Small variant for header buttons */
  :global(.btn-secondary.small) { padding: .25rem .55rem; font-size: .72rem; }
  /* "On" state for the toggle so users can tell at a glance that the
     playground is open. Picks up the global btn-secondary style and tints it. */
  :global(.btn-secondary.on) { background: #4caf82; color: #0a0d1a; border-color: transparent; }
  :global(.btn-secondary.on:hover:not(:disabled)) { background: #5ec092; }

  /* ── API export modal ──────────────────────────────────────────────── */
  .modal.wide { width: 720px; }
  .tab-row {
    display: flex; gap: .35rem; align-items: center;
    border-bottom: 1px solid #2a2f4a; padding-bottom: .4rem;
  }
  .tab {
    background: transparent; color: #8a90b8; border: none;
    padding: .35rem .7rem; font-size: .78rem; cursor: pointer;
    border-radius: 5px 5px 0 0; border-bottom: 2px solid transparent;
  }
  .tab:hover  { color: #c8cadf; }
  .tab.active { color: #4caf82; border-bottom-color: #4caf82; }
  .snippet {
    background: #0a0d1a; border: 1px solid #2a2f4a; border-radius: 6px;
    padding: .85rem; font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: .76rem; color: #c8cadf; white-space: pre-wrap; word-break: break-all;
    max-height: 360px; overflow-y: auto; line-height: 1.5;
  }

  /* ── Templates modal ───────────────────────────────────────────────── */
  .modal-bg {
    position: fixed; inset: 0; background: rgba(5,7,18,.6);
    display: flex; align-items: center; justify-content: center; z-index: 100;
  }
  .modal {
    background: #141626; border: 1px solid #2a2f4a; border-radius: 12px;
    padding: 1.5rem; width: 680px; max-width: 92vw; max-height: 86vh;
    display: flex; flex-direction: column; gap: .9rem; overflow: hidden;
  }
  .modal h2 { font-size: 1.05rem; font-weight: 600; margin-bottom: .15rem; }
  .modal-sub { font-size: .78rem; color: #8a90b8; margin-top: -.2rem; }
  .tpl-list { display: flex; flex-direction: column; gap: .55rem; overflow-y: auto; padding-right: .25rem; }
  .tpl-card {
    background: #181b30; border: 1px solid #2a2f4a; border-radius: 8px;
    padding: .85rem .95rem; display: flex; gap: .9rem; align-items: flex-start;
    transition: border-color .12s, background .12s;
  }
  .tpl-card:hover { border-color: #4caf82; background: #1c2138; }
  .tpl-body { flex: 1; min-width: 0; }
  .tpl-title {
    font-weight: 600; color: #e8ebf8; font-size: .92rem;
    display: flex; align-items: center; gap: .5rem;
  }
  .tpl-source-badge {
    font-size: .62rem; padding: .08rem .35rem; border-radius: 3px;
    text-transform: uppercase; letter-spacing: .04em; font-weight: 600;
  }
  .tpl-source-badge.embedded { background: rgba(76,175,130,.18); color: #4caf82; }
  .tpl-source-badge.user     { background: rgba(110,150,255,.18); color: #8aa6ff; }
  .tpl-desc {
    font-size: .78rem; color: #a0a6cc; margin-top: .25rem;
    line-height: 1.4; white-space: pre-wrap;
  }
  .tpl-tags { margin-top: .35rem; display: flex; gap: .3rem; flex-wrap: wrap; }
  .tpl-tag {
    font-family: monospace; font-size: .65rem; color: #b0b5d8;
    background: #1a1e36; padding: .05rem .35rem; border-radius: 3px;
  }
  .tpl-use {
    align-self: center; padding: .45rem .9rem; font-size: .8rem;
    background: #4caf82; color: #0a0d1a; border: none; border-radius: 5px;
    font-weight: 600; cursor: pointer; white-space: nowrap;
  }
  .tpl-use:disabled { opacity: .55; cursor: wait; }
  .tpl-empty { color: #6b7294; font-size: .8rem; text-align: center; padding: 2rem 0; }

  /* ── Reasoning strategy picker ── */
  .strategy-cards { display: flex; gap: .55rem; flex-wrap: wrap; }
  .strategy-card {
    flex: 1; min-width: 130px; max-width: 200px;
    background: #0e1020; border: 1px solid #1a1e36; border-radius: 9px;
    padding: .75rem .9rem; cursor: pointer; display: flex; flex-direction: column;
    gap: .2rem; text-align: left; transition: border-color .15s, background .15s;
  }
  .strategy-card:hover { border-color: #6c63ff44; background: #141626; }
  .strategy-card.active { border-color: #6c63ff; background: rgba(108,99,255,.1); }
  .sc-icon  { font-size: 1.2rem; }
  .sc-title { font-size: .83rem; font-weight: 600; color: #c5c9e8; }
  .sc-desc  { font-size: .72rem; color: #6b7294; line-height: 1.4; }
  .field-row { display: flex; gap: .6rem; flex-wrap: wrap; }
  .field-row .field { flex: 1; min-width: 120px; }

  /* ── Brain memory cards ── */
  .brain-mem-grid { display: flex; gap: .6rem; flex-wrap: wrap; }
  .bm-card {
    flex: 1; min-width: 160px; background: #0e1020; border: 1px solid #1a1e36;
    border-radius: 9px; padding: .75rem .9rem; display: flex; flex-direction: column; gap: .4rem;
    transition: border-color .15s;
  }
  .bm-card.enabled { border-color: #6c63ff44; background: rgba(108,99,255,.04); }
  .bm-header { display: flex; align-items: center; gap: .45rem; }
  .bm-icon  { font-size: 1rem; }
  .bm-label { font-size: .82rem; font-weight: 600; color: #c5c9e8; flex: 1; }
  .bm-desc  { font-size: .73rem; color: #6b7294; line-height: 1.4; }
  .bm-fields { display: flex; flex-direction: column; gap: .4rem; margin-top: .25rem; }
  .bm-field { display: flex; align-items: center; gap: .5rem; }
  .bm-field-lbl { font-size: .7rem; color: #6b7294; white-space: nowrap; }
  .bm-num { width: 64px; padding: .25rem .4rem; font-size: .8rem; }
  .bm-check { display: flex; align-items: center; gap: .4rem; font-size: .73rem; color: #9da3c0; cursor: pointer; }
  .bm-check input { cursor: pointer; }

  /* ── Small toggle ── */
  .toggle-sm { position: relative; display: inline-flex; align-items: center; cursor: pointer; }
  .toggle-sm input { opacity: 0; width: 0; height: 0; position: absolute; }
  .toggle-track-sm {
    width: 28px; height: 15px; background: #1a1e36; border-radius: 999px;
    transition: background .2s; border: 1px solid #2a2f4a;
  }
  .toggle-sm input:checked ~ .toggle-track-sm { background: #6c63ff; border-color: #6c63ff; }
  .toggle-track-sm::after {
    content: ''; position: absolute; top: 2px; left: 3px;
    width: 11px; height: 11px; border-radius: 50%; background: #fff;
    transition: transform .2s; transform: translateX(0);
  }
  .toggle-sm input:checked ~ .toggle-track-sm::after { transform: translateX(13px); }

  /* ── Persona blocks (Identity / Personality / Non-Negotiables) ───────── */
  .sep-hint {
    font-weight: 400;
    color: #8b90a8;
    font-size: .78rem;
    margin-left: .4rem;
    letter-spacing: 0;
    text-transform: none;
  }
  .persona-block {
    margin: .55rem 0;
    background: rgba(108, 99, 255, 0.04);
    border: 1px solid rgba(108, 99, 255, 0.18);
    border-radius: 8px;
    overflow: hidden;
  }
  .persona-block > summary {
    cursor: pointer;
    list-style: none;
    padding: .55rem .85rem;
    display: flex;
    flex-direction: column;
    gap: .15rem;
    user-select: none;
  }
  .persona-block > summary::-webkit-details-marker { display: none; }
  .persona-block > summary::before {
    content: '▸';
    display: inline-block;
    margin-right: .4rem;
    color: #8b90a8;
    transition: transform .2s ease;
  }
  .persona-block[open] > summary::before { transform: rotate(90deg); }
  .persona-title {
    font-weight: 600;
    color: #e8eaf2;
    font-size: .88rem;
  }
  .persona-sub {
    color: #8b90a8;
    font-size: .76rem;
    line-height: 1.45;
    padding-left: 1rem;
  }
  .persona-tag {
    display: inline-block;
    background: rgba(255, 90, 90, 0.18);
    color: #ff8a8a;
    font-size: .65rem;
    padding: .08rem .42rem;
    border-radius: 4px;
    margin-left: .5rem;
    letter-spacing: 0.05em;
    font-weight: 600;
  }
  .persona-rules {
    background: rgba(255, 90, 90, 0.04);
    border-color: rgba(255, 90, 90, 0.22);
  }
  .persona-body {
    padding: .35rem .85rem .85rem .85rem;
    border-top: 1px solid rgba(108, 99, 255, 0.12);
  }
  .persona-rules .persona-body { border-top-color: rgba(255, 90, 90, 0.16); }
  .field-hint {
    display: block;
    color: #8b90a8;
    font-size: .72rem;
    font-weight: 400;
    margin-top: .12rem;
    line-height: 1.4;
  }
  .field-optional {
    color: #6c728c;
    font-weight: 400;
    font-style: italic;
    font-size: .72rem;
  }
  .persona-must-label { color: #6dd09a; }
  .persona-mustnot-label { color: #ff8a8a; }
  .field-row3 {
    display: flex;
    gap: .65rem;
    margin-top: .35rem;
  }
  .field-row3 .field { flex: 1; min-width: 0; }
  .persona-note {
    margin-top: .65rem;
    padding: .5rem .75rem;
    background: rgba(255, 200, 100, 0.06);
    border-left: 3px solid rgba(255, 200, 100, 0.4);
    border-radius: 4px;
    color: #d8d4ad;
    font-size: .76rem;
    line-height: 1.5;
  }
</style>
