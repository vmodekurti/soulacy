<script>
  import { onMount, onDestroy } from 'svelte'
  import { api } from '../lib/api.js'
  import { activityAgent } from '../lib/stores.js'

  function watchAgent(id) {
    activityAgent.set(id)
    location.hash = 'activity'
  }

  const IMMINENT_MS = 15 * 60 * 1000  // a scheduled run within 15 min is "about to run"

  let schedule    = []
  let agents      = []
  let loading     = true
  let error       = ''
  let notice      = ''
  let busy        = {}      // agentId → true while a manual run request is in flight

  // Live status (polled)
  let running     = {}      // agentId → ISO start time (currently executing)
  let nextRuns    = {}      // agentId → ISO next scheduled fire
  let now         = Date.now()
  let promptedFires = new Set()  // "id@nextISO" already auto-prompted

  // Modals
  let editing  = null
  let editCron = ''
  let editEnabled = false
  let saving   = false
  let runPrompt = null      // { agent, next, auto }

  let statusTimer, tick

  async function load() {
    loading = true
    error   = ''
    try {
      const [schedRes, agentRes] = await Promise.all([
        api.schedule.list(),
        api.agents.list(),
      ])
      schedule = schedRes.schedule || []
      agents   = agentRes.agents   || []
    } catch (e) {
      error = e.message
    } finally {
      loading = false
    }
    await refreshStatus()
  }

  async function refreshStatus() {
    try {
      const s = await api.schedule.status()
      running  = s.running || {}
      nextRuns = s.next    || {}
    } catch (e) {
      // keep last known status on transient errors
    }
  }

  onMount(() => {
    load()
    statusTimer = setInterval(refreshStatus, 5000)
    tick = setInterval(() => { now = Date.now(); checkAutoPrompt() }, 1000)
  })
  onDestroy(() => { clearInterval(statusTimer); clearInterval(tick) })

  $: cronAgents = agents.filter(a => a.trigger === 'cron')

  // Reactive per-agent status. This $: block references running/nextRuns/now
  // DIRECTLY, so Svelte re-renders the table whenever any of them change.
  // (Calling isRunning(a.id) in markup wouldn't track `running`, because Svelte
  // doesn't look inside function bodies for dependencies.)
  $: statusById = (() => {
    const m = {}
    for (const a of cronAgents) {
      const nx = nextRuns[a.id]
      const ms = nx ? new Date(nx).getTime() - now : null
      m[a.id] = {
        running:  !!running[a.id],
        imminent: ms != null && ms > 0 && ms <= IMMINENT_MS,
        ms,
        hasNext:  !!nx,
      }
    }
    return m
  })()

  // --- status helpers (used by event handlers, where reactivity isn't needed) ---
  const isRunning  = (id) => !!running[id]
  function msUntil(id) {
    const nx = nextRuns[id]
    if (!nx) return null
    return new Date(nx).getTime() - now
  }
  function isImminent(id) {
    const m = msUntil(id)
    return m != null && m > 0 && m <= IMMINENT_MS
  }
  function fmtCountdown(ms) {
    if (ms == null || ms < 0) return ''
    const s = Math.round(ms / 1000)
    const m = Math.floor(s / 60)
    const sec = s % 60
    if (m >= 60) { const h = Math.floor(m / 60); return `${h}h ${m % 60}m` }
    return m > 0 ? `${m}m ${sec}s` : `${sec}s`
  }
  function fmtTime(iso) {
    return iso ? new Date(iso).toLocaleString() : '—'
  }

  // Auto-prompt when an enabled cron job enters the imminence window (once per fire).
  function checkAutoPrompt() {
    if (runPrompt || editing) return  // never stack modals
    for (const a of cronAgents) {
      if (!a.enabled || isRunning(a.id)) continue
      const nx = nextRuns[a.id]
      if (!nx) continue
      const m = new Date(nx).getTime() - now
      if (m > 0 && m <= IMMINENT_MS) {
        const key = a.id + '@' + nx
        if (!promptedFires.has(key)) {
          promptedFires.add(key)
          runPrompt = { agent: a, next: nx, auto: true }
          return
        }
      }
    }
  }

  function setBusy(id, v) { busy = { ...busy, [id]: v } }

  // --- run ---
  function onRunClick(a) {
    if (isRunning(a.id) || busy[a.id]) return
    if (isImminent(a.id)) {
      // mark this fire as handled so the auto-popup won't also fire for it
      promptedFires.add(a.id + '@' + nextRuns[a.id])
      runPrompt = { agent: a, next: nextRuns[a.id], auto: false }
      return
    }
    doRun(a.id)
  }

  async function doRun(id) {
    setBusy(id, true)
    error = ''; notice = ''
    // Optimistically mark running so the indicator shows instantly, even for
    // fast runs that finish before the next status poll.
    running = { ...running, [id]: new Date().toISOString() }
    try {
      const res = await api.agents.trigger(id)
      notice = `▶ ${id} ran. ` + (res?.result ? `Result: ${truncate(res.result, 160)}` : 'Done.')
    } catch (e) {
      error = e.message   // includes "agent is already running" (409)
    } finally {
      setBusy(id, false)
      refreshStatus()     // reconcile with server (clears running when finished)
    }
  }

  function promptRunNow() {
    const id = runPrompt.agent.id
    runPrompt = null
    doRun(id)
  }
  function promptWait() { runPrompt = null }

  // --- clone / delete / edit ---
  async function clone(agentId) {
    setBusy(agentId, true); error = ''; notice = ''
    try {
      const res = await api.agents.clone(agentId)
      notice = `Cloned to "${res.id}" (created disabled).`
      await load()
    } catch (e) { error = e.message } finally { setBusy(agentId, false) }
  }

  async function remove(agentId) {
    if (isRunning(agentId)) { error = `"${agentId}" is currently running — wait for it to finish before deleting.`; return }
    if (!confirm(`Delete agent "${agentId}"? This removes its SOUL.yaml from disk.`)) return
    setBusy(agentId, true); error = ''; notice = ''
    try {
      await api.agents.delete(agentId)
      notice = `Deleted "${agentId}".`
      await load()
    } catch (e) { error = e.message } finally { setBusy(agentId, false) }
  }

  function openEdit(a) {
    editing     = JSON.parse(JSON.stringify(a))
    editCron    = a.schedule?.cron || ''
    editEnabled = !!a.enabled
  }
  function closeEdit() { editing = null }

  async function saveEdit() {
    if (!editing) return
    saving = true; error = ''; notice = ''
    try {
      if (!editing.schedule) editing.schedule = {}
      editing.schedule.cron = editCron.trim()
      editing.enabled = editEnabled
      await api.agents.update(editing.id, editing)
      notice = `Saved "${editing.id}".`
      closeEdit()
      await load()
    } catch (e) { error = e.message } finally { saving = false }
  }

  function agentName(id) {
    const a = agents.find(a => a.id === id)
    return a?.name || id
  }
  function truncate(s, n) { return s.length > n ? s.slice(0, n) + '…' : s }
</script>

<div class="page">
  <div class="page-header">
    <h1>Schedule</h1>
    <button class="btn-secondary" on:click={load} disabled={loading}>↺ Refresh</button>
  </div>

  {#if error}<div class="banner err">{error}</div>{/if}
  {#if notice}<div class="banner ok">{notice}</div>{/if}

  <!-- Active scheduled jobs from API -->
  <section class="section">
    <div class="section-hdr">
      <span>Active schedules</span>
      <span class="pill">{schedule.length} entr{schedule.length === 1 ? 'y' : 'ies'}</span>
    </div>

    {#if loading}
      <div class="empty">Loading…</div>
    {:else if schedule.length === 0}
      <div class="empty">No active schedules. Enable a cron agent below to register it with the scheduler.</div>
    {:else}
      <table class="tbl">
        <thead>
          <tr><th>Agent</th><th>Next run</th><th>Last run</th></tr>
        </thead>
        <tbody>
          {#each schedule as entry}
            <tr>
              <td class="td-name">{agentName(entry.agent_id)}</td>
              <td class="td-hint">{entry.next ? new Date(entry.next).toLocaleString() : '—'}</td>
              <td class="td-hint">{entry.prev && !entry.prev.startsWith('0001') ? new Date(entry.prev).toLocaleString() : '—'}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  </section>

  <!-- Cron agents (manageable) -->
  <section class="section">
    <div class="section-hdr">
      <span>Cron agents</span>
      <span class="pill">{cronAgents.length}</span>
    </div>
    {#if cronAgents.length === 0}
      <div class="empty">No agents with <code>trigger: cron</code>. Create one in the Build tab or add a SOUL.yaml.</div>
    {:else}
      <table class="tbl">
        <thead>
          <tr><th>ID</th><th>Name</th><th>Schedule</th><th>Enabled</th><th>Status</th><th class="td-action">Actions</th></tr>
        </thead>
        <tbody>
          {#each cronAgents as a}
            {@const st = statusById[a.id] || {}}
            <tr class:row-running={st.running}>
              <td class="td-mono">{a.id}</td>
              <td>{a.name}</td>
              <td class="td-mono">{a.schedule?.cron || '—'}</td>
              <td><span class="pill" class:pill-ok={a.enabled}>{a.enabled ? 'Yes' : 'No'}</span></td>
              <td>
                {#if st.running}
                  <span class="status running"><span class="dot"></span> Running…</span>
                {:else if st.imminent}
                  <span class="status soon">⏰ runs in {fmtCountdown(st.ms)}</span>
                {:else if st.hasNext}
                  <span class="status idle">next {fmtCountdown(st.ms)}</span>
                {:else}
                  <span class="status idle">—</span>
                {/if}
              </td>
              <td class="td-action">
                <div class="actions">
                  {#if st.running}
                    <button class="btn-secondary xs is-running" disabled><span class="dot"></span> Running</button>
                  {:else}
                    <button class="btn-primary xs" on:click={() => onRunClick(a)} disabled={busy[a.id]} title="Run now">
                      {busy[a.id] ? '…' : '▶ Run'}
                    </button>
                  {/if}
                  <button class="btn-secondary xs" on:click={() => watchAgent(a.id)} title="Watch action log">👁 Watch</button>
                  <button class="btn-secondary xs" on:click={() => openEdit(a)} disabled={busy[a.id]}>Edit</button>
                  <button class="btn-secondary xs" on:click={() => clone(a.id)} disabled={busy[a.id]}>Clone</button>
                  <button class="btn-danger xs" on:click={() => remove(a.id)} disabled={busy[a.id] || st.running}>Delete</button>
                </div>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  </section>

  <div class="info-card">
    <h3>Setting up a scheduled agent</h3>
    <p>Set <code>trigger: cron</code> and a schedule block in the agent's SOUL.yaml:</p>
    <pre class="code-block">trigger: cron
schedule:
  cron: "0 8 * * *"   # every day at 8 AM</pre>
    <p><strong>▶ Run</strong> triggers a manual run immediately. While a job runs it shows <strong>Running</strong> and can't be started again (a scheduled fire is also skipped). If a scheduled run is within 15 minutes, Soulacy asks whether to <strong>Run now</strong> or <strong>wait</strong> for the scheduled time.</p>
  </div>
</div>

<!-- Edit modal -->
{#if editing}
  <div class="modal-bg" on:click|self={closeEdit}>
    <div class="modal">
      <h2>Edit schedule — {editing.id}</h2>
      <label class="field">
        <span class="field-label">Cron expression</span>
        <input type="text" bind:value={editCron} placeholder="0 8 * * *" />
        <span class="field-help">5-field cron (min hour day month weekday). Example: <code>0 7 * * *</code> = 7 AM daily.</span>
      </label>
      <label class="check">
        <input type="checkbox" bind:checked={editEnabled} />
        <span>Enabled (registers the schedule with the scheduler)</span>
      </label>
      <div class="modal-row">
        <button class="btn-secondary" on:click={closeEdit} disabled={saving}>Cancel</button>
        <button class="btn-primary" on:click={saveEdit} disabled={saving}>{saving ? 'Saving…' : 'Save'}</button>
      </div>
    </div>
  </div>
{/if}

<!-- Run-now / wait prompt (on click when imminent, or auto when a run approaches) -->
{#if runPrompt}
  <div class="modal-bg" on:click|self={promptWait}>
    <div class="modal">
      <h2>⏰ {runPrompt.agent.name || runPrompt.agent.id} is about to run</h2>
      <p>
        This job is scheduled to run at <strong>{fmtTime(runPrompt.next)}</strong>
        — <strong>{fmtCountdown(new Date(runPrompt.next).getTime() - now)}</strong> from now.
        {#if runPrompt.auto}Heads up: it will start on its own at that time.{/if}
      </p>
      <p class="sub">Do you want to run it now, or wait for the scheduled time?</p>
      <div class="modal-row">
        <button class="btn-secondary" on:click={promptWait}>Wait for schedule</button>
        <button class="btn-primary" on:click={promptRunNow}>Run now</button>
      </div>
    </div>
  </div>
{/if}

<style>
  .page        { padding: 1.5rem; display: flex; flex-direction: column; gap: 1.5rem; }
  .page-header { display: flex; align-items: center; justify-content: space-between; }
  .page-header h1 { font-size: 1.2rem; font-weight: 600; }
  .banner { padding: .7rem 1rem; border-radius: 8px; font-size: .85rem; }
  .err    { background: rgba(240,96,96,.1); border: 1px solid rgba(240,96,96,.3); color: #f06060; }
  .ok     { background: rgba(76,175,130,.1); border: 1px solid rgba(76,175,130,.3); color: #4caf82; }
  .empty  { padding: 2rem 1.25rem; color: #6b7294; font-size: .85rem; }
  .empty code { background: #1c1f35; padding: .1rem .35rem; border-radius: 4px; font-size: .78rem; color: #8b85ff; }

  .section     { background: #141626; border: 1px solid #1a1e36; border-radius: 10px; overflow: hidden; }
  .section-hdr {
    display: flex; align-items: center; gap: .7rem;
    padding: .8rem 1.25rem; border-bottom: 1px solid #1a1e36;
    font-size: .875rem; font-weight: 600;
  }
  .pill    { font-size: .7rem; padding: .15rem .5rem; border-radius: 999px; background: #1c1f35; color: #6b7294; }
  .pill-ok { background: rgba(76,175,130,.15); color: #4caf82; }

  .tbl { width: 100%; border-collapse: collapse; font-size: .85rem; }
  .tbl th { padding: .6rem 1.25rem; text-align: left; color: #555a7a; font-weight: 500; font-size: .72rem; text-transform: uppercase; letter-spacing: .04em; border-bottom: 1px solid #1a1e36; }
  .tbl td { padding: .7rem 1.25rem; border-bottom: 1px solid #0e1020; vertical-align: middle; }
  .tbl tr:last-child td { border-bottom: none; }
  .tbl tr:hover td { background: rgba(255,255,255,.02); }
  .row-running td { background: rgba(76,175,130,.06); }

  .td-name   { font-weight: 500; }
  .td-mono   { font-family: monospace; font-size: .8rem; color: #8b85ff; }
  .td-hint   { color: #555a7a; font-size: .78rem; }
  .td-action { text-align: right; }
  .actions   { display: flex; gap: .35rem; justify-content: flex-end; flex-wrap: wrap; }
  .xs { padding: .28rem .6rem; font-size: .75rem; border-radius: 6px; }

  /* status cell */
  .status { font-size: .78rem; display: inline-flex; align-items: center; gap: .35rem; }
  .status.idle    { color: #555a7a; }
  .status.soon    { color: #f0a060; font-weight: 600; }
  .status.running { color: #4caf82; font-weight: 600; }
  .dot {
    width: .55rem; height: .55rem; border-radius: 50%;
    background: #4caf82; display: inline-block;
    box-shadow: 0 0 0 0 rgba(76,175,130,.6); animation: pulse 1.3s infinite;
  }
  .is-running { color: #4caf82 !important; }
  .is-running .dot { width: .5rem; height: .5rem; }
  @keyframes pulse {
    0%   { box-shadow: 0 0 0 0 rgba(76,175,130,.5); }
    70%  { box-shadow: 0 0 0 .35rem rgba(76,175,130,0); }
    100% { box-shadow: 0 0 0 0 rgba(76,175,130,0); }
  }

  .info-card {
    background: #141626; border: 1px solid #1a1e36; border-radius: 10px;
    padding: 1.1rem 1.25rem; display: flex; flex-direction: column; gap: .6rem;
  }
  .info-card h3 { font-size: .875rem; font-weight: 600; }
  .info-card p  { font-size: .82rem; color: #7b82a8; line-height: 1.6; }
  .info-card code { background: #1c1f35; padding: .1rem .35rem; border-radius: 4px; font-size: .78rem; color: #8b85ff; }
  .code-block {
    background: #0e1020; border: 1px solid #1a1e36; border-radius: 6px;
    padding: .85rem 1rem; font-family: monospace; font-size: .8rem;
    color: #b0b5d8; line-height: 1.65; white-space: pre;
  }

  /* Modal */
  .modal-bg { position: fixed; inset: 0; background: rgba(0,0,0,.65); display: flex; align-items: center; justify-content: center; z-index: 100; }
  .modal { background: #141626; border: 1px solid #2a2f4a; border-radius: 12px; padding: 1.5rem; width: 460px; max-width: 92vw; display: flex; flex-direction: column; gap: 1rem; }
  .modal h2 { font-size: 1rem; font-weight: 600; }
  .modal p  { font-size: .85rem; color: #b0b5d8; line-height: 1.6; }
  .modal p.sub { color: #7b82a8; }
  .field { display: flex; flex-direction: column; gap: .3rem; }
  .field-label { font-size: .8rem; color: #c8cadf; }
  .field-help { font-size: .72rem; color: #555a7a; }
  .field-help code { background: #1c1f35; padding: .05rem .3rem; border-radius: 4px; color: #8b85ff; }
  .check { display: flex; align-items: center; gap: .5rem; font-size: .82rem; color: #c8cadf; }
  .check input { width: auto; }
  .modal-row { display: flex; gap: .75rem; justify-content: flex-end; }
</style>
