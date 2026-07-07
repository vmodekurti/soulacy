<script>
  import { onMount, onDestroy } from 'svelte'
  import { connected } from '../lib/stores.js'
  import { api, createEventSocket } from '../lib/api.js'

  let status  = null
  let agents  = []
  let events  = []
  let ws      = null
  let error   = null
  let authError = false
  let eventsEl
  let eventFilter = 'all'
  let suggestions = []
  let dismissed = new Set()

  const EVENT_FILTERS = [
    { id: 'all', label: 'All' },
    { id: 'errors', label: 'Errors' },
    { id: 'tools', label: 'Tools' },
    { id: 'llm', label: 'LLM' },
    { id: 'messages', label: 'Messages' },
  ]

  async function load() {
    // /health is unauthenticated — it tells us whether the gateway is up,
    // independent of whether our credentials are valid.
    try {
      status = await api.health()
    } catch {
      status = null
    }
    try {
      const res = await api.agents.list()
      agents    = res.agents || []
      error     = null
      authError = false
    } catch (e) {
      error     = e.message
      authError = e.status === 401 || e.status === 403
    }
    try {
      const res = await api.proactive.suggestions()
      suggestions = (res.suggestions || []).filter(s => !dismissed.has(s.kind + ':' + s.agent_id))
    } catch {
      suggestions = []
    }
  }

  function dismiss(s) {
    dismissed.add(s.kind + ':' + s.agent_id)
    suggestions = suggestions.filter(x => x !== s)
  }

  function connectWS() {
    try { ws = createEventSocket() } catch { return }
    ws.onopen    = () => { $connected = true }
    ws.onmessage = (e) => {
      try {
        const ev = JSON.parse(e.data)
        events = [{ ...ev, _ts: Date.now() }, ...events].slice(0, 300)
      } catch {}
    }
    ws.onclose = () => {
      $connected = false
      setTimeout(connectWS, 3000)
    }
    ws.onerror = () => ws.close()
  }

  onMount(() => {
    load()
    connectWS()
    const t = setInterval(load, 15_000)
    return () => clearInterval(t)
  })

  onDestroy(() => { if (ws) ws.close() })

  function eventColor(type = '') {
    if (type.includes('error'))                        return '#f06060'
    if (type.includes('complete') || type.includes('reply')) return '#4caf82'
    if (type.includes('trigger') || type.includes('start'))  return '#6c63ff'
    if (type.includes('tool'))                         return '#f0a060'
    return '#6b7294'
  }

  function fmtTime(iso) {
    try { return new Date(iso || Date.now()).toLocaleTimeString() } catch { return '' }
  }

  function fmtData(data) {
    if (!data) return ''
    const s = typeof data === 'string' ? data : JSON.stringify(data)
    return s.slice(0, 140)
  }

  function matchesFilter(ev) {
    const type = ev.type || ''
    if (eventFilter === 'all') return true
    if (eventFilter === 'errors') return type.includes('error') || fmtData(ev.payload).toLowerCase().includes('error')
    if (eventFilter === 'tools') return type.includes('tool')
    if (eventFilter === 'llm') return type.includes('llm')
    if (eventFilter === 'messages') return type.includes('message')
    return true
  }

  $: filteredEvents = events.filter(matchesFilter)
</script>

<div class="page">
  <div class="page-header">
    <h1>Dashboard</h1>
    <button class="btn-secondary" on:click={load}>↺ Refresh</button>
  </div>

  {#if error}
    <div class="banner err">
      {#if authError}
        🔒 Authentication required — click 🔑 in the sidebar to set your API key
      {:else}
        ⚠ {error}
      {/if}
    </div>
  {/if}

  <!-- Status cards -->
  <div class="cards">
    <div class="card" class:card-ok={!!status}>
      <div class="card-label">Gateway</div>
      <div class="card-value">{status ? '● Online' : authError ? '🔒 Authentication required' : '○ Offline'}</div>
      {#if status}<div class="card-sub">v{status.version}</div>{/if}
    </div>

    <div class="card">
      <div class="card-label">Agents</div>
      <div class="card-value">{agents.length}</div>
      <div class="card-sub">{agents.filter(a => a.enabled).length} enabled</div>
    </div>

    <div class="card">
      <div class="card-label">Events (session)</div>
      <div class="card-value">{events.length}</div>
      <div class="card-sub">{filteredEvents.length} shown · {$connected ? 'streaming live' : 'reconnecting…'}</div>
    </div>
  </div>

  <!-- Proactive suggestions -->
  {#if suggestions.length}
    <div class="section suggest-section">
      <div class="section-hdr">
        <span>Suggested for you</span>
        <span class="pill">{suggestions.length}</span>
      </div>
      <div class="suggest-list">
        {#each suggestions as s}
          <div class="suggest-card" class:review={s.kind === 'review'}>
            <div class="suggest-body">
              <div class="suggest-title">{s.title}</div>
              <div class="suggest-detail">{s.detail}</div>
              <div class="suggest-action">→ {s.action}</div>
            </div>
            <button class="suggest-dismiss" title="Dismiss" on:click={() => dismiss(s)}>×</button>
          </div>
        {/each}
      </div>
    </div>
  {/if}

  <!-- Live event log -->
  <div class="section">
    <div class="section-hdr">
      <span>Live Event Log</span>
      <span class="pill" class:pill-live={$connected}>{$connected ? '● Live' : '○ Reconnecting'}</span>
      <div class="filter-tabs" aria-label="Event filters">
        {#each EVENT_FILTERS as filter}
          <button
            class:active={eventFilter === filter.id}
            on:click={() => eventFilter = filter.id}
            type="button"
          >{filter.label}</button>
        {/each}
      </div>
      {#if events.length}
        <button class="btn-secondary" style="padding:0.2rem 0.6rem;font-size:0.72rem"
                on:click={() => events = []}>Clear</button>
      {/if}
    </div>

    <div class="log" bind:this={eventsEl}>
      {#if events.length === 0}
        <div class="empty">No events yet — send a chat or trigger an agent to see activity.</div>
      {:else if filteredEvents.length === 0}
        <div class="empty">No events match this filter.</div>
      {:else}
        {#each filteredEvents as ev (ev._ts + (ev.id || ''))}
          <div class="log-row">
            <span class="log-time">{fmtTime(ev.timestamp)}</span>
            <span class="log-type" style="color:{eventColor(ev.type)}">{ev.type || 'event'}</span>
            <span class="log-agent">{ev.agent_id || ''}</span>
            <span class="log-data">{fmtData(ev.payload)}</span>
          </div>
        {/each}
      {/if}
    </div>
  </div>
</div>

<style>
  .page        { padding: 1.5rem; display: flex; flex-direction: column; gap: 1.5rem; }
  .page-header { display: flex; align-items: center; justify-content: space-between; }
  .page-header h1 { font-size: 1.2rem; font-weight: 600; }

  .banner { padding: 0.7rem 1rem; border-radius: 8px; font-size: 0.85rem; }
  .err    { background: rgba(240,96,96,.1); border: 1px solid rgba(240,96,96,.3); color: #f06060; }

  .cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(170px, 1fr)); gap: 1rem; }
  .card  {
    background: #141626; border: 1px solid #1a1e36; border-radius: 10px; padding: 1.1rem 1.25rem;
    transition: border-color 0.2s;
  }
  .card-ok    { border-color: rgba(76,175,130,.35); }
  .card-label { color: #6b7294; font-size: 0.72rem; text-transform: uppercase; letter-spacing: .06em; margin-bottom: .4rem; }
  .card-value { font-size: 1.45rem; font-weight: 600; }
  .card-sub   { color: #6b7294; font-size: 0.75rem; margin-top: .2rem; }

  .section     { background: #141626; border: 1px solid #1a1e36; border-radius: 10px; overflow: hidden; flex: 1; min-height: 0; display: flex; flex-direction: column; }
  .section-hdr {
    display: flex; align-items: center; gap: .7rem;
    padding: .8rem 1rem; border-bottom: 1px solid #1a1e36;
    font-size: .875rem; font-weight: 600; flex-shrink: 0;
  }
  .pill      { font-size: .7rem; padding: .15rem .5rem; border-radius: 999px; background: #1c1f35; color: #6b7294; }
  /* Suggestions sit above the live log and must size to their content — reset
     the .section flex-grow/clip so cards aren't cut off. */
  .suggest-section { margin-bottom: 1rem; flex: 0 0 auto; overflow: visible; }
  .suggest-list { display: grid; grid-template-columns: repeat(auto-fit, minmax(280px, 1fr)); gap: .6rem; padding: .8rem 1rem; }
  .suggest-card { position: relative; display: flex; gap: .5rem; background: #141626; border: 1px solid #232847; border-left: 3px solid #6c63ff; border-radius: 9px; padding: .7rem .85rem; }
  .suggest-card.review { border-left-color: #f0a060; }
  .suggest-title { font-size: .82rem; font-weight: 650; color: #c5c9e8; }
  .suggest-detail { font-size: .72rem; color: #8a91b8; margin-top: .28rem; line-height: 1.35; }
  .suggest-action { font-size: .72rem; color: #7d84c9; margin-top: .4rem; font-weight: 600; }
  .suggest-dismiss { position: absolute; top: .35rem; right: .45rem; background: transparent; border: 0; color: #4a4f70; font-size: 1rem; line-height: 1; cursor: pointer; }
  .suggest-dismiss:hover { color: #c5c9e8; }
  .pill-live { background: rgba(76,175,130,.15); color: #4caf82; }
  .filter-tabs { margin-left: auto; display: inline-flex; gap: .25rem; padding: .15rem; background: #0e1020; border: 1px solid #1a1e36; border-radius: 8px; }
  .filter-tabs button {
    background: transparent; color: #7b82a8; border: 0; border-radius: 6px;
    padding: .22rem .5rem; font-size: .72rem; cursor: pointer;
  }
  .filter-tabs button.active { background: #262b4c; color: #f0f2ff; }
  .filter-tabs button:hover { color: #f0f2ff; }

  .log { flex: 1; overflow-y: auto; font-family: monospace; font-size: .78rem; max-height: 480px; }
  .log-row {
    display: grid; grid-template-columns: 72px 180px 130px 1fr;
    gap: .6rem; padding: .35rem 1rem; border-bottom: 1px solid #0e1020;
  }
  .log-row:hover { background: #1a1e36; }
  .log-time  { color: #6b7294; }
  .log-type  { font-weight: 500; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .log-agent { color: #6c63ff; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .log-data  { color: #6b7294; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .empty     { padding: 2.5rem 1rem; text-align: center; color: #6b7294; }

  @media (max-width: 640px) {
    /* Event log: time+type on row 1, agent+data on row 2 */
    .log-row { grid-template-columns: 72px minmax(0, 1fr); row-gap: 0.1rem; }
    .log-data { grid-column: 1 / -1; white-space: normal; overflow-wrap: anywhere; }
    .filter-tabs { margin-left: 0; flex-wrap: wrap; }
    .section-hdr { flex-wrap: wrap; }
  }
</style>
