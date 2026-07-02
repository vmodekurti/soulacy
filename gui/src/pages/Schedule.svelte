<script>
  import { onMount, onDestroy } from 'svelte'
  import { api } from '../lib/api.js'
  import { activityAgent } from '../lib/stores.js'
  import RunMetrics from '../lib/RunMetrics.svelte'
  import { catchupLabel, catchupTitle } from '../lib/schedutils.js'

  function watchAgent(id) {
    activityAgent.set(id)
    location.hash = 'activity'
  }

  const IMMINENT_MS = 15 * 60 * 1000  // a scheduled run within 15 min is "about to run"

  let schedule    = []
  let agents      = []
  let channels    = []
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
  let editOutputChannel = ''
  let editOutputTo = ''
  let editOutputBotName = ''
  let editOutputTemplate = ''
  let editRunMissedOnStartup = false
  let editMissedStartupWindow = ''
  let saving   = false
  let runPrompt = null      // { agent, next, auto }

  // History panel
  let historyAgent   = null   // agent whose history is open
  let historyRuns    = []     // [{sessionId, startTime, status, output}]
  let historyLoading = false
  let historyError   = ''
  let expandedRuns   = {}     // sessionId → true (output expanded)

  function partsText(payload) {
    return (payload?.parts || []).filter(x => x.type === 'text').map(x => x.text).join('\n')
  }

  function groupRuns(events) {
    const sessions = {}
    for (const ev of events) {
      const sid = ev.session_id || 'unknown'
      if (!sessions[sid]) sessions[sid] = []
      sessions[sid].push(ev)
    }
    return Object.entries(sessions).map(([sid, evs]) => {
      // Event timestamp field is "timestamp" (not "created_at") per message.Event struct
      const sorted = evs.slice().sort((a, b) => new Date(a.timestamp) - new Date(b.timestamp))
      const outEv    = evs.find(e => e.type === 'message.out')
      const errEv    = evs.find(e => e.type === 'error')
      const inEv     = evs.find(e => e.type === 'message.in')
      // A run is failed if any tool returned is_error:true, even when the LLM
      // still produced a message.out summarising the failure.
      const toolFailed = evs.some(e => e.type === 'tool.result' && e.payload?.is_error === true)

      let status = 'unknown'
      if (outEv && !toolFailed && !errEv) status = 'success'
      else if (toolFailed || errEv)        status = 'failed'
      else if (outEv)                      status = 'success'

      let output = ''
      if (outEv) {
        output = partsText(outEv.payload) || JSON.stringify(outEv.payload)
      }
      if (errEv) {
        const ep = errEv.payload
        const errText = typeof ep === 'string' ? ep : (ep?.message || ep?.error || JSON.stringify(ep))
        output = output ? output + '\n\n⚠ Error: ' + errText : '⚠ Error: ' + errText
        status = 'failed'
      }

      // channel from message.in payload
      const channel = inEv?.payload?.channel || ''
      const startTime = (inEv || sorted[0])?.timestamp

      return { sessionId: sid, startTime, status, output, channel }
    }).sort((a, b) => new Date(b.startTime) - new Date(a.startTime))
  }

  async function openHistory(a) {
    historyAgent   = a
    historyRuns    = []
    historyError   = ''
    expandedRuns   = {}
    historyLoading = true
    try {
      // Request a large tail but filter to only the event types needed for
      // run summaries — this excludes tool.log (every stdout line) which
      // would otherwise eat the limit and hide older completed runs.
      const res = await api.agents.actions(a.id, 2000, 'message.in,message.out,error,tool.result')
      historyRuns = groupRuns(res.events || [])
    } catch (e) {
      historyError = e.message
    } finally {
      historyLoading = false
    }
  }

  function closeHistory() { historyAgent = null }
  function toggleRun(sid) { expandedRuns = { ...expandedRuns, [sid]: !expandedRuns[sid] } }

  let statusTimer, tick

  async function load() {
    loading = true
    error   = ''
    try {
      const [schedRes, agentRes, channelRes] = await Promise.all([
        api.schedule.list(),
        api.agents.list(),
        api.channels.list().catch(() => ({ channels })),
      ])
      schedule = schedRes.schedule || []
      agents   = agentRes.agents   || []
      channels = channelRes.channels || []
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
  $: outputBotOptions = buildOutputBotOptions(channels)

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

  async function testOutput(a) {
    if (!a?.id || busy[a.id]) return
    setBusy(a.id, true)
    error = ''; notice = ''
    try {
      const res = await api.agents.testScheduleOutput(a.id)
      notice = `Sent scheduled-output test for "${a.id}" via ${res.channel}.`
    } catch (e) {
      error = e.message
    } finally {
      setBusy(a.id, false)
    }
  }

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
    editOutputChannel = a.schedule?.output?.channel || ''
    editOutputTo = a.schedule?.output?.to || ''
    editOutputBotName = a.schedule?.output?.bot_name || botNameForChannel(editOutputChannel)
    editOutputTemplate = a.schedule?.output?.template || ''
    editRunMissedOnStartup = !!a.schedule?.run_missed_on_startup
    editMissedStartupWindow = a.schedule?.missed_startup_window || '24h'
  }
  function closeEdit() { editing = null }

  async function saveEdit() {
    if (!editing) return
    saving = true; error = ''; notice = ''
    try {
      if (!editing.schedule) editing.schedule = {}
      editing.schedule.cron = editCron.trim()
      editing.schedule.run_missed_on_startup = editRunMissedOnStartup
      if (editRunMissedOnStartup) {
        editing.schedule.missed_startup_window = editMissedStartupWindow.trim() || '24h'
      } else {
        delete editing.schedule.missed_startup_window
      }
      if (editOutputChannel.trim()) {
        if (!editOutputTo.trim()) {
          throw new Error('Destination ID is required when scheduled output is enabled.')
        }
        editing.schedule.output = {
          channel: editOutputChannel.trim(),
          to: editOutputTo.trim(),
          bot_name: editOutputBotName.trim(),
          template: editOutputTemplate.trim(),
        }
      } else if (editing.schedule.output) {
        delete editing.schedule.output
      }
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
  function buildOutputBotOptions(list) {
    const rows = []
    for (const ch of list || []) {
      if (!ch || ch.id === 'http') continue
      const bots = ch.bots || []
      if (bots.length) {
        for (const bot of bots) {
          const adapterID = bot._adapter_id || ch.id
          rows.push({
            channel: adapterID,
            bot_name: bot.bot_name || adapterID,
            agent_id: bot.agent_id || '',
            connected: !!bot._connected,
            label: `${bot.bot_name || adapterID} (${adapterID}${bot.agent_id ? ' → ' + bot.agent_id : ''})`,
          })
        }
        continue
      }
      if (ch.settings?.agent_id || ch.id === 'whatsapp') {
        rows.push({
          channel: ch.id,
          bot_name: ch.settings?.bot_name || ch.name || ch.id,
          agent_id: ch.settings?.agent_id || '',
          connected: !!ch.status?.connected,
          label: `${ch.settings?.bot_name || ch.name || ch.id} (${ch.id}${ch.settings?.agent_id ? ' → ' + ch.settings.agent_id : ''})`,
        })
      }
    }
    return rows
  }
  function botNameForChannel(channelID) {
    return outputBotOptions.find(o => o.channel === channelID)?.bot_name || ''
  }
  function selectOutputChannel(channelID) {
    editOutputChannel = channelID
    editOutputBotName = botNameForChannel(channelID)
  }
  function outputSummary(a) {
    const out = a.schedule?.output
    if (!out?.channel) return null
    const name = out.bot_name || botNameForChannel(out.channel) || out.channel
    return { name, channel: out.channel, to: out.to || '' }
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
          <tr><th>Agent</th><th>Next run</th><th>Last run</th><th>Missed runs</th></tr>
        </thead>
        <tbody>
          {#each schedule as entry}
            <tr>
              <td class="td-name">{agentName(entry.agent_id)}</td>
              <td class="td-hint">{entry.next ? new Date(entry.next).toLocaleString() : '—'}</td>
              <td class="td-hint">{entry.prev && !entry.prev.startsWith('0001') ? new Date(entry.prev).toLocaleString() : '—'}</td>
              <td class="td-hint">
                <span class="catchup" class:on={entry.catch_up} title={catchupTitle(entry)}>
                  {entry.catch_up ? '⟳' : '—'} {catchupLabel(entry)}
                </span>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
      <p class="field-help missed-help">
        <strong>Missed runs:</strong> if the gateway is down at an agent's scheduled
        time, agents with <code>run_missed_on_startup</code> run the <em>latest</em>
        missed fire once at startup (within their window; default 24h). Older missed
        fires are never replayed, and completed fires are remembered across restarts —
        no duplicates. All other agents simply skip missed fires.
      </p>
    {/if}
  </section>

  <!-- Cron agents (manageable) -->
  <section class="section">
    <div class="section-hdr">
      <span>Cron agents</span>
      <span class="pill">{cronAgents.length}</span>
    </div>
    {#if cronAgents.length === 0}
      <div class="empty">No agents with <code>trigger: cron</code>. Create one in Studio or add a SOUL.yaml.</div>
    {:else}
      <table class="tbl">
        <thead>
          <tr><th>ID</th><th>Name</th><th>Schedule</th><th>Output bot</th><th>Enabled</th><th>Status</th><th class="td-action">Actions</th></tr>
        </thead>
        <tbody>
          {#each cronAgents as a}
            {@const st = statusById[a.id] || {}}
            {@const out = outputSummary(a)}
            <tr class:row-running={st.running}>
              <td class="td-mono">{a.id}</td>
              <td>{a.name}</td>
              <td class="td-mono">{a.schedule?.cron || '—'}</td>
              <td>
                {#if out}
                  <div class="output-cell">
                    <strong>{out.name}</strong>
                    <span>{out.channel}{out.to ? ` → ${out.to}` : ''}</span>
                  </div>
                {:else}
                  <span class="td-hint">—</span>
                {/if}
              </td>
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
                  <button class="btn-secondary xs" on:click={() => openHistory(a)} title="Run history">📋 History</button>
                  <button class="btn-secondary xs" on:click={() => watchAgent(a.id)} title="Watch action log">👁 Watch</button>
                  {#if out}
                    <button class="btn-secondary xs" on:click={() => testOutput(a)} disabled={busy[a.id]} title="Send a test message to the configured output destination">Test output</button>
                  {/if}
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
  cron: "0 8 * * *"   # every day at 8 AM
  output:
    channel: "telegram-financial-agent"
    to: "123456789"
    bot_name: "Finance Bot"</pre>
    <p><strong>▶ Run</strong> triggers a manual run immediately and returns the result here. Cron-fired runs use <code>schedule.output</code> to publish through the selected bot. While a job runs it shows <strong>Running</strong> and can't be started again (a scheduled fire is also skipped).</p>
  </div>
</div>

<!-- Edit modal -->
{#if editing}
  <div
    class="modal-bg"
    role="button"
    tabindex="0"
    aria-label="Close schedule edit modal"
    on:click|self={closeEdit}
    on:keydown={(e) => e.key === 'Escape' && closeEdit()}
  >
    <div class="modal">
      <h2>Edit schedule — {editing.id}</h2>
      <label class="field">
        <span class="field-label">Cron expression</span>
        <input type="text" bind:value={editCron} placeholder="0 8 * * *" />
        <span class="field-help">5-field cron (min hour day month weekday). Example: <code>0 7 * * *</code> = 7 AM daily.</span>
      </label>
      <div class="output-editor">
        <div class="output-editor-head">
          <span>Missed runs</span>
        </div>
        <label class="check">
          <input type="checkbox" bind:checked={editRunMissedOnStartup} />
          <span>Run the latest missed cron after startup</span>
        </label>
        {#if editRunMissedOnStartup}
          <label class="field">
            <span class="field-label">Catch-up window</span>
            <input type="text" bind:value={editMissedStartupWindow} placeholder="24h" />
            <span class="field-help">Examples: <code>6h</code>, <code>24h</code>, <code>72h</code>.</span>
          </label>
        {/if}
      </div>
      <div class="output-editor">
        <div class="output-editor-head">
          <span>Scheduled output</span>
          {#if outputBotOptions.length === 0}<em>No channel bots configured</em>{/if}
        </div>
        <div class="field-help output-help">
          Add or rotate Telegram output bot tokens in <a href="#channels">Channels</a>, then restart the gateway and select the bot here.
        </div>
        <label class="field">
          <span class="field-label">Bot</span>
          <select value={editOutputChannel} on:change={(e) => selectOutputChannel(e.currentTarget.value)} disabled={outputBotOptions.length === 0}>
            <option value="">Do not send output</option>
            {#each outputBotOptions as opt}
              <option value={opt.channel}>{opt.label}{opt.connected ? '' : ' · offline'}</option>
            {/each}
          </select>
          <span class="field-help">The scheduler sends the agent reply through this channel adapter ID.</span>
        </label>
        {#if editOutputChannel}
          <label class="field">
            <span class="field-label">Destination ID</span>
            <input type="text" bind:value={editOutputTo} placeholder="Telegram chat ID, Slack channel ID, Discord channel ID, or WhatsApp number" />
            <span class="field-help">This becomes the outbound message thread/chat/channel/user ID for the selected bot.</span>
          </label>
          <label class="field">
            <span class="field-label">Bot name</span>
            <input type="text" bind:value={editOutputBotName} placeholder="Friendly bot name" />
          </label>
          <label class="field">
            <span class="field-label">Template</span>
            <textarea bind:value={editOutputTemplate} rows="3" placeholder="&#123;reply&#125;"></textarea>
            <span class="field-help">Optional. Tokens: <code>&#123;reply&#125;</code>, <code>&#123;agent_id&#125;</code>, <code>&#123;agent_name&#125;</code>, <code>&#123;trigger&#125;</code>, <code>&#123;timestamp&#125;</code>.</span>
          </label>
        {/if}
      </div>
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

<!-- History slide-out panel -->
{#if historyAgent}
  <div
    class="panel-bg"
    role="button"
    tabindex="0"
    aria-label="Close run history panel"
    on:click|self={closeHistory}
    on:keydown={(e) => e.key === 'Escape' && closeHistory()}
  >
    <div class="history-panel">
      <div class="panel-hdr">
        <div class="panel-title">
          <span class="panel-label">Run history</span>
          <span class="panel-agent">{historyAgent.name || historyAgent.id}</span>
        </div>
        <button class="close-btn" on:click={closeHistory}>✕</button>
      </div>

      <div class="panel-body">
        {#if historyLoading}
          <div class="panel-empty">Loading…</div>
        {:else if historyError}
          <div class="panel-empty err">{historyError}</div>
        {:else if historyRuns.length === 0}
          <div class="panel-empty">No runs recorded yet.</div>
        {:else}
          {#each historyRuns as run}
            <div class="run" class:run-fail={run.status === 'failed'}>
              <button class="run-hdr" on:click={() => toggleRun(run.sessionId)}>
                <span class="run-badge" class:badge-ok={run.status === 'success'} class:badge-fail={run.status === 'failed'} class:badge-unk={run.status === 'unknown'}>
                  {run.status === 'success' ? '✓ success' : run.status === 'failed' ? '✗ failed' : '? unknown'}
                </span>
                <span class="run-time">{run.startTime ? new Date(run.startTime).toLocaleString() : '—'}</span>
                {#if run.channel}<span class="run-channel">{run.channel}</span>{/if}
                <span class="run-chevron">{expandedRuns[run.sessionId] ? '▲' : '▼'}</span>
              </button>
              {#if expandedRuns[run.sessionId]}
                <div class="run-output">
                  <div class="run-metrics-row">
                    <RunMetrics sessionId={run.sessionId} agentId={historyAgent.id} />
                  </div>
                  {#if run.output}
                    <pre>{run.output}</pre>
                  {:else}
                    <span class="no-output">No output captured.</span>
                  {/if}
                </div>
              {/if}
            </div>
          {/each}
        {/if}
      </div>
    </div>
  </div>
{/if}

<!-- Run-now / wait prompt (on click when imminent, or auto when a run approaches) -->
{#if runPrompt}
  <div
    class="modal-bg"
    role="button"
    tabindex="0"
    aria-label="Wait for scheduled run"
    on:click|self={promptWait}
    on:keydown={(e) => e.key === 'Escape' && promptWait()}
  >
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

  @media (max-width: 768px) {
    /* Wide tables scroll horizontally instead of overflowing the viewport */
    .tbl { display: block; overflow-x: auto; }
    .tbl th, .tbl td { padding: .6rem .75rem; }
  }
  .tbl th { padding: .6rem 1.25rem; text-align: left; color: #555a7a; font-weight: 500; font-size: .72rem; text-transform: uppercase; letter-spacing: .04em; border-bottom: 1px solid #1a1e36; }
  .tbl td { padding: .7rem 1.25rem; border-bottom: 1px solid #0e1020; vertical-align: middle; }
  .tbl tr:last-child td { border-bottom: none; }
  .tbl tr:hover td { background: rgba(255,255,255,.02); }
  .row-running td { background: rgba(76,175,130,.06); }

  .td-name   { font-weight: 500; }
  .td-mono   { font-family: monospace; font-size: .8rem; color: #8b85ff; }
  .td-hint   { color: #555a7a; font-size: .78rem; }
  .catchup        { white-space: nowrap; cursor: help; }
  .catchup.on     { color: #6fbf8f; }
  .missed-help    { margin-top: .5rem; max-width: 70ch; }
  .td-action { text-align: right; }
  .actions   { display: flex; gap: .35rem; justify-content: flex-end; flex-wrap: wrap; }
  .xs { padding: .28rem .6rem; font-size: .75rem; border-radius: 6px; }
  .output-cell { display: flex; flex-direction: column; gap: .15rem; min-width: 150px; }
  .output-cell strong { color: #c8cadf; font-size: .78rem; font-weight: 600; }
  .output-cell span { color: #6b7294; font-family: monospace; font-size: .7rem; word-break: break-all; }

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

  /* History panel */
  .panel-bg {
    position: fixed; inset: 0; background: rgba(0,0,0,.45); z-index: 100;
    display: flex; justify-content: flex-end;
  }
  .history-panel {
    width: min(520px, 100vw); max-width: 100vw; height: 100%;
    background: #141626; border-left: 1px solid #2a2f4a;
    display: flex; flex-direction: column; overflow: hidden;
  }
  .panel-hdr {
    display: flex; align-items: center; justify-content: space-between;
    padding: 1rem 1.25rem; border-bottom: 1px solid #1a1e36; flex-shrink: 0;
  }
  .panel-title { display: flex; flex-direction: column; gap: .2rem; }
  .panel-label { font-size: .68rem; text-transform: uppercase; letter-spacing: .07em; color: #555a7a; font-weight: 600; }
  .panel-agent { font-size: .95rem; font-weight: 600; color: #e0e1f0; }
  .close-btn {
    background: none; border: none; color: #555a7a; font-size: 1rem;
    cursor: pointer; padding: .25rem .5rem; border-radius: 6px;
  }
  .close-btn:hover { background: #1c1f35; color: #c8cadf; }
  .panel-body { flex: 1; overflow-y: auto; display: flex; flex-direction: column; }
  .panel-empty { padding: 3rem 1.5rem; color: #555a7a; font-size: .85rem; text-align: center; }
  .panel-empty.err { color: #f06060; }

  .run { border-bottom: 1px solid #1a1e36; }
  .run:last-child { border-bottom: none; }
  .run-hdr {
    width: 100%; background: none; border: none; cursor: pointer;
    display: flex; align-items: center; gap: .6rem; flex-wrap: wrap;
    padding: .75rem 1.25rem; text-align: left;
  }
  .run-hdr:hover { background: rgba(255,255,255,.03); }
  .run-fail .run-hdr { background: rgba(240,96,96,.04); }
  .run-badge {
    font-size: .7rem; font-weight: 600; padding: .15rem .55rem;
    border-radius: 999px; flex-shrink: 0;
  }
  .badge-ok  { background: rgba(76,175,130,.15); color: #4caf82; }
  .badge-fail{ background: rgba(240,96,96,.15);  color: #f06060; }
  .badge-unk { background: rgba(100,100,120,.15); color: #888; }
  .run-time    { font-size: .78rem; color: #7b82a8; flex: 1; }
  .run-channel { font-size: .7rem; color: #555a7a; font-family: monospace; }
  .run-chevron { font-size: .65rem; color: #555a7a; margin-left: auto; }
  .run-output {
    padding: .75rem 1.25rem 1rem; background: #0e1020;
    border-top: 1px solid #1a1e36;
  }
  .run-output pre {
    font-family: monospace; font-size: .78rem; color: #b0b5d8;
    line-height: 1.65; white-space: pre-wrap; word-break: break-word; margin: 0;
  }
  .no-output { font-size: .78rem; color: #555a7a; font-style: italic; }

  /* Modal */
  .modal-bg { position: fixed; inset: 0; background: rgba(0,0,0,.65); display: flex; align-items: center; justify-content: center; z-index: 100; }
  .modal { background: #141626; border: 1px solid #2a2f4a; border-radius: 12px; padding: 1.5rem; width: 460px; max-width: 92vw; max-height: 88vh; overflow-y: auto; display: flex; flex-direction: column; gap: 1rem; }
  .modal h2 { font-size: 1rem; font-weight: 600; }
  .modal p  { font-size: .85rem; color: #b0b5d8; line-height: 1.6; }
  .modal p.sub { color: #7b82a8; }
  .field { display: flex; flex-direction: column; gap: .3rem; }
  .field-label { font-size: .8rem; color: #c8cadf; }
  .field select,
  .field textarea {
    background: #1c1f35; color: #e8eaf6; border: 1px solid #2a2f4a;
    border-radius: 6px; padding: .5rem .65rem; font-size: .85rem;
  }
  .field textarea { resize: vertical; min-height: 4.5rem; font-family: inherit; line-height: 1.45; }
  .field-help { font-size: .72rem; color: #555a7a; }
  .field-help code { background: #1c1f35; padding: .05rem .3rem; border-radius: 4px; color: #8b85ff; }
  .output-editor {
    border: 1px solid #1a1e36; border-radius: 8px; background: #101323;
    padding: .85rem; display: flex; flex-direction: column; gap: .8rem;
  }
  .output-editor-head { display: flex; align-items: center; justify-content: space-between; gap: .75rem; }
  .output-editor-head span { font-size: .82rem; color: #e0e1f0; font-weight: 600; }
  .output-editor-head em { font-size: .72rem; color: #f0a060; font-style: normal; }
  .check { display: flex; align-items: center; gap: .5rem; font-size: .82rem; color: #c8cadf; }
  .check input { width: auto; }
  .modal-row {
    display: flex; gap: .75rem; justify-content: flex-end;
    position: sticky; bottom: 0; z-index: 5;
    background: #141626; padding-top: .6rem;
    box-shadow: 0 -10px 12px -10px rgba(0, 0, 0, 0.6);
  }

  .run-metrics-row { padding: .2rem 0 .45rem; }
</style>
