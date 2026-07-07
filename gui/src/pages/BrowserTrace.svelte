<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'

  let agents = []
  let agentId = ''
  let sessionId = ''
  let trace = null
  let loading = false
  let error = ''

  const ACTION_ICON = {
    navigate: '🌐', click: '🖱', type: '⌨', extract: '📄', screenshot: '📸', other: '•',
  }

  async function loadAgents() {
    try {
      const res = await api.agents.list()
      agents = res.agents || []
      if (!agentId && agents.length) agentId = agents[0].id
    } catch (e) { error = e.message }
  }

  async function load() {
    if (!agentId) return
    loading = true
    error = ''
    trace = null
    try {
      const res = await api.browserTrace(agentId, sessionId.trim())
      trace = res.trace || null
      if (res.enabled === false) error = 'Action logging is disabled, so no trace is available.'
    } catch (e) {
      error = e.message
    }
    loading = false
  }

  onMount(loadAgents)
</script>

<div class="page">
  <div class="page-header">
    <h1>Browser Trace</h1>
    <button class="btn-secondary" on:click={load} disabled={loading || !agentId}>{loading ? 'Loading…' : '↺ Refresh'}</button>
  </div>

  <p class="intro">
    Replay an agent's browser automation — every navigate, click, type, extract and
    screenshot, reconstructed from the action log. Per-domain navigation is enforced
    by each agent's tool policy.
  </p>

  <div class="controls">
    <label>
      Agent
      <select bind:value={agentId} on:change={load}>
        {#each agents as a}<option value={a.id}>{a.name || a.id}</option>{/each}
      </select>
    </label>
    <label>
      Session (optional)
      <input placeholder="filter by session id" bind:value={sessionId} on:keydown={(e) => e.key === 'Enter' && load()} />
    </label>
  </div>

  {#if error}<div class="banner err">⚠ {error}</div>{/if}

  {#if trace}
    <div class="summary">
      <div class="stat"><div class="val">{trace.steps?.length || 0}</div><div class="lbl">Steps</div></div>
      <div class="stat"><div class="val">{trace.navigations || 0}</div><div class="lbl">Navigations</div></div>
      {#if trace.last_url}
        <div class="stat wide"><div class="val url">{trace.last_url}</div><div class="lbl">Last URL</div></div>
      {/if}
    </div>

    {#if trace.steps && trace.steps.length}
      <div class="timeline">
        {#each trace.steps as s}
          <div class="step" class:err={s.is_error}>
            <div class="step-seq">{s.seq}</div>
            <div class="step-icon">{ACTION_ICON[s.action] || '•'}</div>
            <div class="step-body">
              <div class="step-head">
                <span class="step-action">{s.action}</span>
                <span class="step-tool">{s.tool}</span>
                {#if s.is_error}<span class="step-badge">failed</span>{/if}
              </div>
              {#if s.url}<div class="step-detail">{s.url}</div>{/if}
              {#if s.target}<div class="step-detail dim">{s.target}</div>{/if}
            </div>
            {#if s.at}<div class="step-time">{new Date(s.at).toLocaleTimeString()}</div>{/if}
          </div>
        {/each}
      </div>
    {:else}
      <div class="empty">No browser steps found for this selection. Run a browser-automation agent, then refresh.</div>
    {/if}
  {/if}
</div>

<style>
  .intro { color: #8a91b8; font-size: .85rem; max-width: 720px; margin: 0 0 1rem; line-height: 1.5; }
  .controls { display: flex; gap: 1rem; flex-wrap: wrap; margin-bottom: 1rem; }
  .controls label { display: flex; flex-direction: column; gap: .3rem; font-size: .72rem; color: #6b7294; }
  .controls select, .controls input { background: #0e1020; color: #d7dcf5; border: 1px solid #1a1e36; border-radius: 7px; padding: .4rem .55rem; font-size: .82rem; min-width: 220px; }
  .banner.err { background: rgba(240,96,96,.08); border: 1px solid rgba(240,96,96,.4); color: #f0a0a0; border-radius: 8px; padding: .6rem .8rem; margin-bottom: 1rem; }
  .summary { display: flex; gap: .6rem; flex-wrap: wrap; margin-bottom: 1rem; }
  .stat { background: #141626; border: 1px solid #1a1e36; border-radius: 9px; padding: .6rem .85rem; min-width: 110px; }
  .stat.wide { flex: 1; min-width: 240px; }
  .val { font-size: 1.2rem; font-weight: 750; color: #c5c9e8; }
  .val.url { font-size: .82rem; font-weight: 600; word-break: break-all; }
  .lbl { font-size: .64rem; text-transform: uppercase; letter-spacing: .05em; color: #6b7294; margin-top: .3rem; }
  .timeline { display: flex; flex-direction: column; gap: .4rem; }
  .step { display: flex; align-items: center; gap: .7rem; background: #141626; border: 1px solid #1a1e36; border-left: 3px solid #6c63ff; border-radius: 9px; padding: .55rem .8rem; }
  .step.err { border-left-color: #f06060; }
  .step-seq { font-size: .72rem; color: #6b7294; width: 1.6rem; text-align: right; }
  .step-icon { font-size: 1rem; width: 1.4rem; text-align: center; }
  .step-body { flex: 1; min-width: 0; }
  .step-head { display: flex; align-items: center; gap: .5rem; }
  .step-action { font-size: .82rem; font-weight: 650; color: #c5c9e8; text-transform: capitalize; }
  .step-tool { font-size: .7rem; color: #6b7294; }
  .step-badge { font-size: .62rem; color: #f0a0a0; background: rgba(240,96,96,.12); border-radius: 999px; padding: .05rem .4rem; }
  .step-detail { font-size: .74rem; color: #9aa0c8; margin-top: .2rem; word-break: break-all; }
  .step-detail.dim { color: #6b7294; }
  .step-time { font-size: .68rem; color: #6b7294; white-space: nowrap; }
  .empty { color: #6b7294; font-size: .85rem; padding: 2rem; text-align: center; }
</style>
