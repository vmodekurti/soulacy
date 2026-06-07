<script>
  import { onMount, onDestroy } from 'svelte'
  import { get } from 'svelte/store'
  import { api } from '../lib/api.js'
  import { activityAgent } from '../lib/stores.js'
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

  async function loadAgents() {
    try {
      const res = await api.agents.list()
      agents = res.agents || []
      const preset = get(activityAgent)
      if (preset && agents.find(a => a.id === preset)) selectedId = preset
      else if (!selectedId && agents.length) selectedId = agents[0].id
      if (selectedId) await poll()
      // Auto-start watching when deep-linked from a "Watch" button.
      if (preset) { activityAgent.set(''); if (!watching) toggleWatch() }
    } catch (e) { error = e.message }
  }

  async function poll() {
    if (!selectedId) return
    loading = events.length === 0
    try {
      const res = await api.agents.actions(selectedId, 500)
      events = res.events || []
      path   = res.path || ''
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
    events = []
    path = ''
    await poll()
  }

  onMount(loadAgents)
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
      case 'reasoning.step':   return `step ${p.index ?? '?'}: ${snippet(p.thought, 90)}${p.tool ? ` → ${p.tool}` : ''}`
      case 'reasoning.result': return `loop finished — ${p.steps ?? 0} step(s) · ${p.confident ? 'confident' : 'not confident'} · ${p.duration_ms ?? 0}ms`
      case 'message.out': return `reply — ${snippet(partsText(p), 120)}`
      case 'error':       return `[${p.stage || 'error'}] ${snippet(p.error, 160)}`
      case 'connected':   return String(ev.payload || 'stream connected')
      default:            return snippet(typeof ev.payload === 'string' ? ev.payload : JSON.stringify(ev.payload))
    }
  }

  function fmtTime(iso) { try { return new Date(iso).toLocaleTimeString() } catch { return '' } }

  const FILTERS = {
    all:    () => true,
    run:    (t) => t === 'message.in' || t === 'message.out',
    llm:    (t) => t.startsWith('llm.'),
    tools:  (t) => t.startsWith('tool.'),
    errors: (t) => t === 'error',
  }
  $: filtered = events.filter(ev => (FILTERS[typeFilter] || FILTERS.all)(ev.type || ''))
</script>

<div class="page">
  <div class="page-header">
    <h1>Activity</h1>
    <div class="header-actions">
      <button class="btn-secondary" class:active={watching} on:click={toggleWatch}>
        {watching ? '⏹ Stop watching' : '▶ Watch (2s)'}
      </button>
      <button class="btn-secondary" on:click={poll} disabled={!selectedId || loading}>↺ Refresh</button>
    </div>
  </div>

  {#if error}<div class="banner err">{error}</div>{/if}

  <div class="toolbar">
    <select bind:value={selectedId} on:change={() => selectAgent(selectedId)} class="agent-select">
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

  {#if path}
    <div class="source-bar">
      <span class="source-label">Log file</span>
      <code class="source-path">{path}</code>
      <span class="source-count">{filtered.length} / {events.length} events</span>
    </div>
  {/if}

  <div class="log-panel" bind:this={logEl}>
    {#if loading && events.length === 0}
      <div class="empty">Loading…</div>
    {:else if filtered.length === 0}
      <div class="empty">
        {#if events.length === 0}
          No actions logged yet for <strong>{selectedId || 'this agent'}</strong>.
          <br>Trigger the agent (Schedule ▶ Run, or send it a message) and its actions will appear here.
        {:else}
          No <strong>{typeFilter}</strong> events in the log.
        {/if}
      </div>
    {:else}
      {#each filtered as ev, i (i + (ev.timestamp || ''))}
        {@const m = meta(ev.type)}
        <div class="row" class:is-error={ev.type === 'error' || ev.payload?.is_error}>
          <span class="t">{fmtTime(ev.timestamp)}</span>
          <span class="badge" style="color:{m.color}">{m.icon} {m.label}</span>
          <span class="sum">{summary(ev)}</span>
        </div>
        {#if (ev.type === 'message.out' || ev.type === 'error') && ev.session_id}
          <div class="row metrics-row">
            <span class="t"></span>
            <span class="badge run-sum">Σ run</span>
            <RunMetrics sessionId={ev.session_id} agentId={selectedId} />
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

  .log-panel {
    flex: 1; min-height: 0; overflow-y: auto;
    background: #0a0c17; border: 1px solid #1a1e36; border-radius: 10px;
    font-family: 'JetBrains Mono', 'Fira Code', monospace; font-size: .76rem; line-height: 1.5;
  }
  .empty { padding: 3rem 2rem; text-align: center; color: #555a7a; line-height: 1.7; }

  .row {
    display: grid; grid-template-columns: 76px 86px 1fr; gap: .6rem; align-items: start;
    padding: .3rem .85rem; border-bottom: 1px solid rgba(255,255,255,.03);
  }
  .row:hover { background: rgba(255,255,255,.03); }
  .row.is-error { background: rgba(240,96,96,.06); }
  .t     { color: #555a7a; }
  .badge { font-weight: 700; font-size: .68rem; white-space: nowrap; }
  .sum   { color: #c8cadf; white-space: pre-wrap; word-break: break-word; }

  .metrics-row { opacity: .85; }
  .run-sum { color: #6b7294; }
</style>
