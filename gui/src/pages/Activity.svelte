<script>
  import { onMount, onDestroy } from 'svelte'
  import { get } from 'svelte/store'
  import { api } from '../lib/api.js'
  import { activityAgent, studioDebugRun } from '../lib/stores.js'
  import RunMetrics from '../lib/RunMetrics.svelte'

  let agents     = []
  let selectedId = ''
  let events     = []
  let path       = ''
  let loading    = false
  let error      = ''
  let watching   = false
  let timer      = null
  let typeFilter = 'all'
  let logEl
  let autoScroll = true
  let replayingSession = ''
  let replayMsg = ''
  let learningSession = ''
  let routeAgentId = ''
  let sessionFilter = ''
  let runHistory = []
  let runFilter = null
  const ALL_AGENTS = '__all__'

  function hashParams() {
    const hash = window.location.hash || ''
    const idx = hash.indexOf('?')
    return new URLSearchParams(idx >= 0 ? hash.slice(idx + 1) : '')
  }

  async function loadAgents() {
    try {
      const res = await api.agents.list()
      agents = res.agents || []
      const preset = get(activityAgent)
      if (routeAgentId && agents.find(a => a.id === routeAgentId)) selectedId = routeAgentId
      else if (preset && agents.find(a => a.id === preset)) selectedId = preset
      else if (!selectedId) selectedId = ALL_AGENTS
      if (selectedId) await poll()
      // Auto-start watching when deep-linked from a "Watch" button.
      if (preset || routeAgentId || sessionFilter) { activityAgent.set(''); if (!watching) toggleWatch() }
    } catch (e) { error = e.message }
  }

  async function poll() {
    if (!selectedId) return
    loading = events.length === 0
    try {
      const [res, hist] = await Promise.all([
        selectedId === ALL_AGENTS
          ? api.runs.events({ limit: 1000, sessionId: sessionFilter })
          : api.agents.actions(selectedId, 500),
        api.runs.ledger({
          agentId: selectedId === ALL_AGENTS ? '' : selectedId,
          limit: 250,
          eventLimit: 50000,
        }).catch(() => ({ runs: [] })),
      ])
      events = res.events || []
      path   = selectedId === ALL_AGENTS ? '' : (res.path || '')
      runHistory = normalizeRunHistory(hist.runs || [])
      if (runFilter && !runHistory.find(r => r.id === runFilter.id)) runFilter = null
      error  = ''
      if (autoScroll) scrollToBottom()
    } catch (e) {
      error = e.message
    } finally {
      loading = false
    }
  }

  function scrollToBottom() {
    setTimeout(() => { if (logEl) logEl.scrollTop = logEl.scrollHeight }, 40)
  }

  function toggleWatch() {
    watching = !watching
    if (watching) {
      poll()
      timer = setInterval(poll, 2000)
    } else {
      clearInterval(timer); timer = null
    }
  }

  async function selectAgent(id) {
    selectedId = id
    sessionFilter = ''
    runFilter = null
    runHistory = []
    events = []
    path = ''
    replayMsg = ''
    await poll()
  }

  async function replayRun(ev) {
    const agentId = eventAgentId(ev)
    if (!agentId || !ev?.session_id || replayingSession) return
    replayingSession = ev.session_id
    replayMsg = ''
    try {
      const res = await api.agents.replay(agentId, ev.session_id)
      replayMsg = `Replayed ${ev.session_id} as ${res.replay_session_id || 'new session'}`
      await poll()
    } catch (e) {
      error = e.message || 'Replay failed'
    } finally {
      replayingSession = ''
    }
  }

  async function learnFromRun(ev) {
    const agentId = eventAgentId(ev)
    if (!agentId || !ev?.session_id || learningSession) return
    learningSession = ev.session_id
    replayMsg = ''
    try {
      const res = await api.brainMemory.proposeFromRun(agentId, ev.session_id, 3)
      const n = res.created || (res.proposals || []).length || 0
      replayMsg = n
        ? `Created ${n} learning proposal${n === 1 ? '' : 's'} from ${ev.session_id}. Review them in Learning.`
        : `No learning proposals created from ${ev.session_id}. The run may be too short to generalize.`
    } catch (e) {
      error = e.message || 'Learning proposal failed'
    } finally {
      learningSession = ''
    }
  }

  function debugInStudio(ev) {
    const agentId = eventAgentId(ev)
    if (!agentId || !ev?.session_id) return
    studioDebugRun.set({
      agentId,
      sessionId: ev.session_id,
      error: eventErrorText(ev),
    })
    window.location.hash = '#studio'
  }

  function isBrowserEvent(ev) {
    if (!ev?.session_id || !ev.type?.startsWith('tool.')) return false
    const p = ev.payload || {}
    const name = String(p.name || '').toLowerCase()
    return name.includes('browser') ||
      name.includes('playwright') ||
      name.includes('puppeteer') ||
      name.includes('computer') ||
      name.includes('navigate') ||
      name.includes('screenshot') ||
      name.includes('page_')
  }

  function openBrowserTrace(ev) {
    const agentId = eventAgentId(ev)
    if (!agentId || !ev?.session_id) return
    const params = new URLSearchParams({ agent_id: agentId, session_id: ev.session_id })
    window.location.hash = `#browser?${params.toString()}`
  }

  function openRunBrowserTrace(run) {
    if (!run?.agentId || !run?.sessionId) return
    const params = new URLSearchParams({ agent_id: run.agentId, session_id: run.sessionId })
    window.location.hash = `#browser?${params.toString()}`
  }

  onMount(() => {
    const params = hashParams()
    routeAgentId = params.get('agent_id') || ''
    sessionFilter = params.get('session_id') || ''
    loadAgents()
  })
  onDestroy(() => { if (timer) clearInterval(timer) })

  // ── rendering helpers ──────────────────────────────────────────────
  const TYPE_META = {
    'message.in':  { label: 'RUN',    color: '#6c63ff', icon: '▶' },
    'llm.call':    { label: 'LLM',    color: '#8b85ff', icon: '↗' },
    'llm.result':  { label: 'LLM',    color: '#8b85ff', icon: '↙' },
    'tool.call':   { label: 'TOOL',   color: '#f0a060', icon: '🔧' },
    'tool.result': { label: 'TOOL',   color: '#f0a060', icon: '↩' },
    'reasoning.start':  { label: 'LOOP', color: '#5bc0de', icon: '▶' },
    'reasoning.step':   { label: 'LOOP', color: '#5bc0de', icon: '∴' },
    'reasoning.result': { label: 'LOOP', color: '#5bc0de', icon: '■' },
    'message.out': { label: 'REPLY',  color: '#4caf82', icon: '✓' },
    'error':       { label: 'ERROR',  color: '#f06060', icon: '✖' },
    'connected':   { label: 'SYS',    color: '#555a7a', icon: '•' },
  }
  function meta(t) { return TYPE_META[t] || { label: (t || 'evt').toUpperCase().slice(0, 5), color: '#6b7294', icon: '•' } }

  function snippet(s, n = 120) { s = String(s ?? ''); return s.length > n ? s.slice(0, n) + '…' : s }

  function normalizeRunHistory(rows) {
    return (rows || []).map(r => ({
      id: r.runId || r.sessionId || '',
      agentId: r.agentId || '',
      sessionId: r.sessionId || r.runId || '',
      trigger: r.trigger || r.channel || '',
      source: r.source || 'run-history',
      startedAt: r.startedAt || '',
      updatedAt: r.updatedAt || r.startedAt || '',
      status: r.status || (r.ok ? 'success' : 'unknown'),
      ok: !!r.ok,
      steps: r.steps || 0,
      output: r.output || r.error || '',
      error: r.error || '',
      deliveryStatus: r.deliveryStatus || '',
      deliveryChannel: r.deliveryChannel || '',
      deliveryTo: r.deliveryTo || '',
      deliveryError: r.deliveryError || '',
      hasBrowserTrace: !!r.hasBrowserTrace,
      browserEvents: r.browserEvents || 0,
    })).filter(r => r.id).sort((a, b) => new Date(b.updatedAt || b.startedAt || 0) - new Date(a.updatedAt || a.startedAt || 0))
  }

  function selectRun(run) {
    runFilter = run
    sessionFilter = ''
    typeFilter = 'all'
  }

  function clearRunFilter() {
    runFilter = null
  }

  function runMatchesEvent(run, ev) {
    if (!run || !ev) return true
    if (run.sessionId && ev.session_id !== run.sessionId) return false
    const t = new Date(ev.timestamp || 0).getTime()
    const start = new Date(run.startedAt || 0).getTime()
    const end = new Date(run.updatedAt || run.startedAt || 0).getTime()
    if (!Number.isFinite(t) || !Number.isFinite(start)) return true
    const pad = 1000
    return t >= start - pad && (!Number.isFinite(end) || end <= 0 || t <= end + pad)
  }

  function runTitle(run) {
    const status = run.status || 'unknown'
    const trigger = run.trigger || run.source || 'run'
    return `${status} · ${trigger} · ${fmtTime(run.startedAt)}`
  }

  function runPreview(run) {
    return snippet(run.error || run.output || run.deliveryError || run.id, 110)
  }

  function partsText(p) {
    if (!p || !p.parts) return ''
    return p.parts.filter(x => x.type === 'text').map(x => x.text).join(' ')
  }

  function summary(ev) {
    const p = ev.payload || {}
    switch (ev.type) {
      case 'message.in': {
        const trig = p.metadata?.trigger || p.channel || 'message'
        return `run started (${trig}) — ${snippet(partsText(p), 100)}`
      }
      case 'llm.call':    return `LLM call → ${p.model || '?'} · turn ${p.turn ?? '?'}`
      case 'llm.result':  return `LLM result — ${p.output_tokens ?? 0} out / ${p.input_tokens ?? 0} in tokens · ${p.duration_ms ?? 0}ms${p.tool_calls ? ` · ${p.tool_calls} tool call(s)` : ''}`
      case 'tool.call':   return `${p.name || 'tool'}(${snippet(JSON.stringify(p.arguments || {}), 80)})`
      case 'tool.result': return `${p.name || 'tool'} → ${snippet(p.content, 100)}`
      case 'reasoning.start':  return `reasoning loop started — ${p.strategy || '?'} · max ${p.max_steps ?? '?'} steps · ${p.tools ?? 0} tools`
      case 'reasoning.step':   return `${p.recovery ? 'recovery ' : ''}step ${p.index ?? '?'}: ${snippet(p.thought, 90)}${p.tool ? ` → ${p.tool}` : ''}`
      case 'reasoning.result': return `loop finished — ${p.steps ?? 0} step(s) · ${p.confident ? 'confident' : 'degraded / not confident'} · ${p.duration_ms ?? 0}ms`
      case 'message.out': return `reply — ${snippet(partsText(p), 120)}`
      case 'error':       return `[${p.stage || 'error'}] ${snippet(p.error, 160)}`
      case 'connected':   return String(ev.payload || 'stream connected')
      default:            return snippet(typeof ev.payload === 'string' ? ev.payload : JSON.stringify(ev.payload))
    }
  }

  function fmtTime(iso) { try { return new Date(iso).toLocaleTimeString() } catch { return '' } }

  function eventErrorText(ev) {
    const p = ev?.payload || {}
    return p.error || p.message || p.content || ev?.type || ''
  }

  function eventAgentId(ev) {
    return ev?.agent_id || (selectedId !== ALL_AGENTS ? selectedId : '')
  }

  function agentName(id) {
    const a = agents.find(x => x.id === id)
    return a?.name || id || ''
  }

  function canDebug(ev) {
    const p = ev?.payload || {}
    return !!ev?.session_id && (ev.type === 'error' || !!p.error || !!p.is_error)
  }

  function canLearn(ev) {
    return !!ev?.session_id && ev.type === 'message.out'
  }

  const FILTERS = {
    all:    () => true,
    run:    (t) => t === 'message.in' || t === 'message.out',
    llm:    (t) => t.startsWith('llm.'),
    tools:  (t) => t.startsWith('tool.'),
    errors: (t) => t === 'error',
  }
  $: filtered = events.filter(ev =>
    (FILTERS[typeFilter] || FILTERS.all)(ev.type || '') &&
    (!sessionFilter || ev.session_id === sessionFilter) &&
    (!runFilter || runMatchesEvent(runFilter, ev))
  )
</script>

<div class="page">
  <div class="page-header">
    <h1>Runs</h1>
    <div class="header-actions">
      <button class="btn-secondary" class:active={watching} on:click={toggleWatch}>
        {watching ? '⏹ Stop watching' : '▶ Watch (2s)'}
      </button>
      <button class="btn-secondary" on:click={poll} disabled={!selectedId || loading}>↺ Refresh</button>
    </div>
  </div>

  {#if error}<div class="banner err">{error}</div>{/if}
  {#if replayMsg}<div class="banner ok">{replayMsg}</div>{/if}

  <div class="toolbar">
    <select bind:value={selectedId} on:change={() => selectAgent(selectedId)} class="agent-select">
      <option value={ALL_AGENTS}>All agents</option>
      {#if agents.length === 0}
        <option value="">No agents</option>
      {/if}
      {#each agents as a}
        <option value={a.id}>{a.name || a.id}</option>
      {/each}
    </select>

    <div class="chips">
      {#each Object.keys(FILTERS) as f}
        <button class="chip" class:on={typeFilter === f} on:click={() => typeFilter = f}>{f}</button>
      {/each}
    </div>

    <label class="autoscroll"><input type="checkbox" bind:checked={autoScroll} /> auto-scroll</label>
  </div>

  {#if sessionFilter}
    <div class="source-bar session-bar">
      <span class="source-label">Session</span>
      <code class="source-path">{sessionFilter}</code>
      <button class="row-action" on:click={() => sessionFilter = ''}>Show all runs</button>
    </div>
  {/if}

  {#if runHistory.length > 0}
    <div class="run-strip" aria-label="Canonical run history">
      <div class="run-strip-head">
        <span class="source-label">Unified runs</span>
        <span class="source-count">{runHistory.length} from durable ledger</span>
        {#if runFilter}
          <button class="row-action" on:click={clearRunFilter}>Show all events</button>
        {/if}
      </div>
      <div class="run-cards">
        {#each runHistory.slice(0, 18) as run (run.id)}
          <button
            class="run-card"
            class:on={runFilter?.id === run.id}
            class:failed={run.status === 'failed'}
            class:success={run.status === 'success'}
            title={run.id}
            on:click={() => selectRun(run)}
          >
            <span class="run-status">{run.status || 'unknown'}</span>
            <span class="run-time">{fmtTime(run.startedAt)}</span>
            <span class="run-trigger">{run.trigger || run.source || 'run'}</span>
            <span class="run-preview">{runPreview(run)}</span>
            {#if run.deliveryChannel || run.deliveryStatus}
              <span class="run-delivery">{run.deliveryChannel || 'delivery'} · {run.deliveryStatus || 'unknown'}</span>
            {/if}
            {#if run.hasBrowserTrace}
              <span class="run-browser">Browser trace · {run.browserEvents || 1} event{(run.browserEvents || 1) === 1 ? '' : 's'}</span>
            {/if}
          </button>
        {/each}
      </div>
    </div>
  {/if}

  {#if runFilter}
    <div class="source-bar session-bar">
      <span class="source-label">Run</span>
      <code class="source-path">{runTitle(runFilter)}</code>
      <span class="source-count">session {runFilter.sessionId || '—'}</span>
      {#if runFilter.hasBrowserTrace}
        <button class="row-action browser" on:click={() => openRunBrowserTrace(runFilter)}>Browser trace</button>
      {/if}
    </div>
  {/if}

  {#if path}
    <div class="source-bar">
      <span class="source-label">Log file</span>
      <code class="source-path">{path}</code>
      <span class="source-count">{filtered.length} / {events.length} events</span>
    </div>
  {:else if selectedId === ALL_AGENTS}
    <div class="source-bar">
      <span class="source-label">Durable history</span>
      <code class="source-path">All agents from the SQLite action log</code>
      <span class="source-count">{filtered.length} / {events.length} events</span>
    </div>
  {/if}

  <div class="log-panel" bind:this={logEl}>
    {#if loading && events.length === 0}
      <div class="empty">Loading…</div>
    {:else if filtered.length === 0}
      <div class="empty">
        {#if events.length === 0}
          No actions logged yet for <strong>{selectedId === ALL_AGENTS ? 'any agent' : (selectedId || 'this agent')}</strong>.
          <br>Trigger the agent from Automations, Studio, or Chat and its actions will appear here.
        {:else}
          No <strong>{typeFilter}</strong> events in the log.
        {/if}
      </div>
    {:else}
      {#each filtered as ev, i (i + (ev.timestamp || ''))}
        {@const m = meta(ev.type)}
        <div class="row" class:is-error={ev.type === 'error' || ev.payload?.is_error} class:is-recovery={ev.payload?.recovery} class:is-degraded={ev.type === 'reasoning.result' && ev.payload?.confident === false}>
          <span class="t">{fmtTime(ev.timestamp)}</span>
          <span class="badge" style="color:{m.color}">{m.icon} {m.label}</span>
          <span class="sum">{summary(ev)}</span>
          {#if selectedId === ALL_AGENTS && ev.agent_id}
            <span class="agent-pill" title={ev.agent_id}>{agentName(ev.agent_id)}</span>
          {/if}
          {#if ev.type === 'message.in' && ev.session_id}
            <button class="row-action" on:click={() => replayRun(ev)} disabled={!!replayingSession}>
              {replayingSession === ev.session_id ? 'Replaying…' : 'Replay'}
            </button>
          {/if}
          {#if canDebug(ev)}
            <button class="row-action debug" on:click={() => debugInStudio(ev)}>
              Debug in Studio
            </button>
          {/if}
          {#if isBrowserEvent(ev)}
            <button class="row-action browser" on:click={() => openBrowserTrace(ev)}>
              Browser trace
            </button>
          {/if}
          {#if canLearn(ev)}
            <button class="row-action learn" on:click={() => learnFromRun(ev)} disabled={!!learningSession}>
              {learningSession === ev.session_id ? 'Learning…' : 'Learn'}
            </button>
          {/if}
        </div>
        {#if (ev.type === 'message.out' || ev.type === 'error') && ev.session_id}
          <div class="row metrics-row">
            <span class="t"></span>
            <span class="badge run-sum">Σ run</span>
            <RunMetrics sessionId={ev.session_id} agentId={eventAgentId(ev)} />
          </div>
        {/if}
      {/each}
    {/if}
  </div>
</div>

<style>
  .page        { padding: 1.5rem; display: flex; flex-direction: column; gap: 1rem; height: 100%; min-height: 0; }
  .page-header { display: flex; align-items: center; justify-content: space-between; flex-shrink: 0; }
  .page-header h1 { font-size: 1.2rem; font-weight: 600; }
  .header-actions { display: flex; gap: .5rem; }
  .active { background: rgba(108,99,255,.15) !important; color: #8b85ff !important; border-color: rgba(108,99,255,.4) !important; }
  .banner { padding: .65rem 1rem; border-radius: 8px; font-size: .82rem; flex-shrink: 0; }
  .err  { background: rgba(240,96,96,.1); border: 1px solid rgba(240,96,96,.3); color: #f06060; }
  .ok   { background: rgba(76,175,130,.12); border: 1px solid rgba(76,175,130,.35); color: #4caf82; }

  .toolbar { display: flex; gap: .75rem; align-items: center; flex-wrap: wrap; flex-shrink: 0; }
  .agent-select { width: 240px; flex-shrink: 0; }
  .chips { display: flex; gap: .35rem; flex-wrap: wrap; }
  .chip {
    background: #1c1f35; border: 1px solid #2a2f4a; color: #7b82a8;
    padding: .3rem .7rem; border-radius: 999px; font-size: .75rem; text-transform: capitalize;
  }
  .chip.on { background: rgba(108,99,255,.15); color: #8b85ff; border-color: rgba(108,99,255,.4); }
  .autoscroll { display: flex; align-items: center; gap: .35rem; font-size: .78rem; color: #7b82a8; margin-left: auto; }
  .autoscroll input { width: auto; }

  .source-bar {
    display: flex; align-items: center; gap: .75rem;
    padding: .5rem .85rem; background: #0e1020; border: 1px solid #1a1e36;
    border-radius: 8px; flex-shrink: 0;
  }
  .source-label { font-size: .7rem; color: #555a7a; text-transform: uppercase; letter-spacing: .06em; font-weight: 600; }
  .source-path  { font-size: .78rem; color: #7b82a8; flex: 1; word-break: break-all; }
  .source-count { font-size: .72rem; color: #555a7a; flex-shrink: 0; }

  .run-strip {
    background: #0e1020; border: 1px solid #1a1e36; border-radius: 10px;
    padding: .65rem; display: flex; flex-direction: column; gap: .55rem; flex-shrink: 0;
  }
  .run-strip-head { display: flex; align-items: center; gap: .7rem; }
  .run-cards {
    display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
    gap: .45rem; max-height: 190px; overflow: auto;
  }
  .run-card {
    text-align: left; border: 1px solid #242a47; background: #12162a; color: #c8cadf;
    border-radius: 8px; padding: .55rem .65rem; display: grid;
    grid-template-columns: 1fr auto; gap: .18rem .5rem; min-height: 74px;
  }
  .run-card:hover, .run-card.on { border-color: rgba(108,99,255,.65); background: rgba(108,99,255,.12); }
  .run-card.failed { border-left: 3px solid #f06060; }
  .run-card.success { border-left: 3px solid #4caf82; }
  .run-status { font-size: .68rem; text-transform: uppercase; letter-spacing: .05em; color: #8b85ff; font-weight: 700; }
  .run-time { font-size: .66rem; color: #6b7294; justify-self: end; }
  .run-trigger, .run-preview, .run-delivery, .run-browser { grid-column: 1 / -1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .run-trigger { color: #e7e8f5; font-size: .76rem; }
  .run-preview, .run-delivery { color: #7b82a8; font-size: .68rem; }
  .run-browser { color: #8bdcff; font-size: .68rem; }

  .log-panel {
    flex: 1; min-height: 0; overflow-y: auto;
    background: #0a0c17; border: 1px solid #1a1e36; border-radius: 10px;
    font-family: 'JetBrains Mono', 'Fira Code', monospace; font-size: .76rem; line-height: 1.5;
  }
  .empty { padding: 3rem 2rem; text-align: center; color: #555a7a; line-height: 1.7; }

  .row {
    display: grid; grid-template-columns: 76px 86px minmax(0, 1fr) repeat(5, auto); gap: .6rem; align-items: start;
    padding: .3rem .85rem; border-bottom: 1px solid rgba(255,255,255,.03);
  }
  .row:hover { background: rgba(255,255,255,.03); }
  .row.is-error { background: rgba(240,96,96,.06); }
  .row.is-recovery { background: rgba(245,167,66,.055); }
  .row.is-degraded { background: rgba(245,167,66,.075); }
  .t     { color: #555a7a; }
  .badge { font-weight: 700; font-size: .68rem; white-space: nowrap; }
  .sum   { color: #c8cadf; white-space: pre-wrap; word-break: break-word; }
  .row-action {
    justify-self: end; align-self: center;
    background: rgba(108,99,255,.12); border: 1px solid rgba(108,99,255,.35);
    color: #ada8ff; border-radius: 6px; padding: .18rem .48rem;
    font-family: inherit; font-size: .68rem;
  }
  .row-action.debug { background: rgba(76,175,130,.10); border-color: rgba(76,175,130,.35); color: #76d6a0; }
  .row-action.browser { background: rgba(64,196,255,.10); border-color: rgba(64,196,255,.35); color: #8bdcff; }
  .row-action.learn { background: rgba(245,167,66,.10); border-color: rgba(245,167,66,.35); color: #f5bd67; }
  .row-action:disabled { opacity: .55; cursor: wait; }
  .agent-pill {
    justify-self: end; align-self: center; max-width: 180px; overflow: hidden; text-overflow: ellipsis;
    white-space: nowrap; border: 1px solid rgba(108,99,255,.28); background: rgba(108,99,255,.10);
    color: #ada8ff; border-radius: 999px; padding: .14rem .45rem; font-size: .66rem;
  }

  .metrics-row { opacity: .85; }
  .run-sum { color: #6b7294; }
</style>
