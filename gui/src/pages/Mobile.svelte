<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'

  let loading = true
  let error = ''
  let health = null
  let schedule = []
  let agents = []
  let doctor = null
  let learning = null
  let queues = []
  let recentErrors = []
  let refreshingError = ''

  // Companion: approvals, push, pairing.
  let approvals = []
  let approvalBusy = ''
  let pushState = 'unknown'   // unknown | unsupported | denied | off | on | working
  let pairCode = null
  let pairUrl = ''
  let pairQr = ''
  let redeemCode = ''
  let redeemMsg = ''
  let companionMsg = ''

  async function loadApprovals() {
    try {
      const res = await api.approvals.list()
      approvals = res.approvals || []
    } catch { approvals = [] }
  }

  async function resolveApproval(id, approve) {
    approvalBusy = id
    try {
      if (approve) await api.approvals.approve(id)
      else await api.approvals.deny(id)
      approvals = approvals.filter(a => a.call_id !== id)
    } catch (e) { companionMsg = e.message } finally { approvalBusy = '' }
  }

  function urlBase64ToUint8Array(base64String) {
    const padding = '='.repeat((4 - (base64String.length % 4)) % 4)
    const base64 = (base64String + padding).replace(/-/g, '+').replace(/_/g, '/')
    const raw = atob(base64)
    const arr = new Uint8Array(raw.length)
    for (let i = 0; i < raw.length; i++) arr[i] = raw.charCodeAt(i)
    return arr
  }

  async function detectPush() {
    if (!('serviceWorker' in navigator) || !('PushManager' in window)) { pushState = 'unsupported'; return }
    if (Notification.permission === 'denied') { pushState = 'denied'; return }
    try {
      const reg = await navigator.serviceWorker.getRegistration()
      const sub = reg && await reg.pushManager.getSubscription()
      pushState = sub ? 'on' : 'off'
    } catch { pushState = 'off' }
  }

  async function enablePush() {
    pushState = 'working'
    try {
      const perm = await Notification.requestPermission()
      if (perm !== 'granted') { pushState = 'denied'; return }
      const reg = await navigator.serviceWorker.register('/sw.js')
      await navigator.serviceWorker.ready
      const { public_key } = await api.push.publicKey()
      const sub = await reg.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: urlBase64ToUint8Array(public_key),
      })
      await api.push.subscribe(sub.toJSON())
      pushState = 'on'
      companionMsg = 'Notifications enabled on this device.'
    } catch (e) {
      pushState = 'off'
      companionMsg = 'Could not enable notifications: ' + (e.message || e)
    }
  }

  async function makePairCode() {
    try {
      const res = await api.pairing.createToken()
      pairCode = res.code
      pairUrl = res.pair_url || ''
      pairQr = ''
      // Render a scannable QR of the pair URL. qrcode is code-split, so it only
      // loads the first time someone pairs a device.
      try {
        const QR = (await import('qrcode')).default
        pairQr = await QR.toDataURL(pairUrl || pairCode, { margin: 1, width: 200 })
      } catch (_) { /* text code still works as a fallback */ }
    } catch (e) { companionMsg = e.message }
  }

  async function redeemPair() {
    if (!redeemCode.trim()) return
    redeemMsg = ''
    try {
      const res = await api.pairing.redeem(redeemCode.trim())
      redeemMsg = res.paired ? '✓ Paired.' + (res.token ? ' Token issued.' : '') : 'Pairing failed.'
      if (res.token) { try { localStorage.setItem('soulacy-mobile-token', res.token) } catch (_) {} }
      redeemCode = ''
    } catch (e) { redeemMsg = e.message }
  }

  async function load() {
    loading = true
    error = ''
    loading = true
    error = ''
    refreshingError = ''
    try {
      const [h, s, a, d, l, q] = await Promise.allSettled([
        api.health(),
        api.schedule.list(),
        api.agents.list(),
        api.providers.doctor(),
        api.brainMemory.learningSummary(),
        api.queues.names(),
      ])
      if (h.status === 'fulfilled') health = h.value
      if (s.status === 'fulfilled') schedule = s.value?.schedule || []
      if (a.status === 'fulfilled') agents = a.value?.agents || []
      if (d.status === 'fulfilled') doctor = d.value
      if (l.status === 'fulfilled') learning = l.value
      if (q.status === 'fulfilled') queues = q.value?.queues || []
      const failed = [h, s, a, d, l, q].find(x => x.status === 'rejected')
      if (failed) error = failed.reason?.message || 'Some mobile data could not be loaded'
      await loadRecentErrors(a.status === 'fulfilled' ? (a.value?.agents || []) : agents)
    } finally {
      loading = false
    }
  }

  async function loadRecentErrors(agentList) {
    const enabled = (agentList || []).filter(a => a.enabled).slice(0, 8)
    const results = await Promise.allSettled(enabled.map(async (agent) => {
      const res = await api.agents.actions(agent.id, 12, 'error', { durable: true })
      return (res.events || []).map(ev => ({ ...ev, agent_id: agent.id, agent_name: agent.name || agent.id }))
    }))
    recentErrors = results
      .filter(r => r.status === 'fulfilled')
      .flatMap(r => r.value)
      .sort((a, b) => String(b.ts || b.time || '').localeCompare(String(a.ts || a.time || '')))
      .slice(0, 6)
    const failed = results.find(r => r.status === 'rejected')
    if (failed) refreshingError = failed.reason?.message || 'Recent errors could not be loaded'
  }

  function go(page) {
    window.location.hash = '#' + page
  }

  $: providerIssues = (doctor?.providers || []).filter(x => x.status !== 'ok')
  $: channelIssues = (doctor?.channels || []).filter(x => x.status !== 'ok')
  $: enabledAgents = agents.filter(a => a.enabled)
  $: riskCount = providerIssues.length + channelIssues.length + recentErrors.length
  $: pendingLearning = learning?.pending || 0

  function eventText(ev) {
    if (!ev) return ''
    const p = ev.payload || {}
    if (typeof p === 'string') return p
    return p.error || p.message || p.line || ev.message || JSON.stringify(p)
  }

  function eventTime(ev) {
    const raw = ev.ts || ev.time || ev.created_at || ''
    if (!raw) return ''
    const d = new Date(raw)
    if (Number.isNaN(d.getTime())) return raw
    return d.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })
  }

  onMount(() => {
    load()
    loadApprovals()
    detectPush()
    const t = setInterval(loadApprovals, 10000)
    return () => clearInterval(t)
  })
</script>

<div class="mobile-page">
  <header>
    <div>
      <span>Companion</span>
      <h1>Operations</h1>
    </div>
    <button class="btn-secondary" on:click={() => { load(); loadApprovals() }} disabled={loading}>↻</button>
  </header>

  {#if companionMsg}<button class="companion-note" type="button" on:click={() => companionMsg = ''}>{companionMsg}</button>{/if}

  <!-- Approvals -->
  <section class="m-card approvals-card" class:active={approvals.length}>
    <div class="m-card-hd">
      <span>Approvals</span>
      {#if approvals.length}<span class="m-badge urgent">{approvals.length}</span>{/if}
    </div>
    {#if approvals.length === 0}
      <p class="m-empty">Nothing waiting on you.</p>
    {:else}
      {#each approvals as a}
        <div class="approval">
          <div class="approval-info">
            <div class="approval-tool">{a.tool}</div>
            <div class="approval-reason">{a.reason || 'wants to run'}{a.agent_id ? ' · ' + a.agent_id : ''}</div>
          </div>
          <div class="approval-actions">
            <button class="approve" on:click={() => resolveApproval(a.call_id, true)} disabled={approvalBusy === a.call_id}>Approve</button>
            <button class="deny" on:click={() => resolveApproval(a.call_id, false)} disabled={approvalBusy === a.call_id}>Deny</button>
          </div>
        </div>
      {/each}
    {/if}
  </section>

  <!-- Notifications + pairing -->
  <section class="m-card">
    <div class="m-card-hd"><span>This device</span></div>
    <div class="device-row">
      <div>
        <div class="device-label">Push notifications</div>
        <div class="device-sub">
          {#if pushState === 'on'}Enabled{:else if pushState === 'unsupported'}Not supported in this browser{:else if pushState === 'denied'}Blocked in browser settings{:else}Off{/if}
        </div>
      </div>
      {#if pushState === 'off'}
        <button class="btn-primary small" on:click={enablePush}>Enable</button>
      {:else if pushState === 'working'}
        <button class="btn-secondary small" disabled>…</button>
      {:else if pushState === 'on'}
        <button class="btn-secondary small" on:click={() => api.push.test()}>Test</button>
      {/if}
    </div>

    <div class="device-row">
      <div>
        <div class="device-label">Pair a phone</div>
        <div class="device-sub">Generate a code, then enter it on the other device below.</div>
      </div>
      <button class="btn-secondary small" on:click={makePairCode}>Get code</button>
    </div>
    {#if pairCode}
      {#if pairQr}<img class="pair-qr" src={pairQr} alt="Pairing QR code" />{/if}
      <div class="pair-code">{pairCode}</div>
      {#if pairUrl}<div class="pair-url">{pairUrl}</div>{/if}
    {/if}

    <div class="device-row redeem-row">
      <input class="redeem-input" placeholder="Enter pairing code" bind:value={redeemCode} on:keydown={(e) => e.key === 'Enter' && redeemPair()} />
      <button class="btn-secondary small" on:click={redeemPair} disabled={!redeemCode.trim()}>Pair</button>
    </div>
    {#if redeemMsg}<div class="redeem-msg">{redeemMsg}</div>{/if}
  </section>

  {#if error}<div class="banner err">{error}</div>{/if}

  <section class="hero">
    <div>
      <strong>{riskCount ? `${riskCount} item${riskCount === 1 ? '' : 's'} need attention` : health ? 'Gateway online' : loading ? 'Checking gateway…' : 'Gateway unknown'}</strong>
      <small>{enabledAgents.length} enabled agents · {schedule.length} scheduled jobs · {queues.length} queues</small>
    </div>
    <span class:ok={health && !riskCount} class:warn={health && riskCount}>{health ? (riskCount ? 'Review' : 'Live') : 'Check'}</span>
  </section>

  <div class="quick-grid">
    <button on:click={() => go('chat')}>Chat</button>
    <button on:click={() => go('activity')}>Activity</button>
    <button on:click={() => go('schedule')}>Schedule</button>
    <button on:click={() => go('channels')}>Channels</button>
    <button on:click={() => go('queues')}>Queues</button>
    <button on:click={() => go('memory')}>Learning</button>
    <button on:click={() => go('providers')}>Providers</button>
    <button on:click={() => go('studio')}>Studio</button>
  </div>

  <section>
    <h2>Learning loop</h2>
    <div class="metrics">
      <button on:click={() => go('memory')}>
        <strong>{pendingLearning}</strong>
        <small>pending proposals</small>
      </button>
      <button on:click={() => go('queues')}>
        <strong>{queues.length}</strong>
        <small>active queues</small>
      </button>
      <button on:click={() => go('activity')}>
        <strong>{recentErrors.length}</strong>
        <small>recent errors</small>
      </button>
    </div>
  </section>

  <section>
    <h2>Today’s automations</h2>
    {#if loading}
      <p class="empty">Loading…</p>
    {:else if !schedule.length}
      <p class="empty">No active schedules.</p>
    {:else}
      <div class="list">
        {#each schedule.slice(0, 6) as item}
          <button on:click={() => go('schedule')}>
            <strong>{item.name || item.agent_id || item.id}</strong>
            <small>{item.next_run || item.next || item.status || 'scheduled'}</small>
          </button>
        {/each}
      </div>
    {/if}
  </section>

  <section>
    <h2>Recent failures</h2>
    {#if refreshingError}
      <p class="empty">{refreshingError}</p>
    {:else if loading}
      <p class="empty">Loading…</p>
    {:else if !recentErrors.length}
      <p class="empty good">No recent agent errors found.</p>
    {:else}
      <div class="list">
        {#each recentErrors as ev}
          <button on:click={() => go('activity')}>
            <strong>{ev.agent_name || ev.agent_id}</strong>
            <small>{eventTime(ev)} · {eventText(ev)}</small>
          </button>
        {/each}
      </div>
    {/if}
  </section>

  <section>
    <h2>Needs attention</h2>
    {#if providerIssues.length === 0 && channelIssues.length === 0}
      <p class="empty good">No provider or channel blockers found.</p>
    {:else}
      <div class="list">
        {#each providerIssues as p}
          <button on:click={() => go('providers')}>
            <strong>{p.id}</strong>
            <small>{p.detail}</small>
          </button>
        {/each}
        {#each channelIssues as c}
          <button on:click={() => go('channels')}>
            <strong>{c.id}</strong>
            <small>{c.detail}</small>
          </button>
        {/each}
      </div>
    {/if}
  </section>
</div>

<style>
  .mobile-page { max-width: 760px; margin: 0 auto; display: flex; flex-direction: column; gap: 14px; }
  .companion-note { display: block; width: 100%; text-align: left; background: rgba(108,99,255,.12); border: 1px solid rgba(108,99,255,.35); color: #b9bcf0; border-radius: 9px; padding: .55rem .8rem; font-size: .82rem; cursor: pointer; }
  .m-card { background: #141626; border: 1px solid #1a1e36; border-radius: 12px; padding: .8rem .95rem; }
  .m-card.active { border-color: rgba(240,160,96,.4); }
  .m-card-hd { display: flex; align-items: center; gap: .5rem; font-size: .8rem; font-weight: 700; color: #c5c9e8; margin-bottom: .6rem; }
  .m-badge { font-size: .68rem; padding: .1rem .45rem; border-radius: 999px; background: #1c1f35; color: #8a91b8; }
  .m-badge.urgent { background: rgba(240,160,96,.18); color: #f0b070; }
  .m-empty { color: #6b7294; font-size: .82rem; margin: 0; }
  .approval { display: flex; align-items: center; justify-content: space-between; gap: .6rem; padding: .55rem 0; border-top: 1px solid #1a1e36; }
  .approval:first-of-type { border-top: 0; }
  .approval-tool { font-size: .86rem; font-weight: 650; color: #c5c9e8; }
  .approval-reason { font-size: .74rem; color: #8a91b8; margin-top: .15rem; }
  .approval-actions { display: flex; gap: .4rem; }
  .approval-actions button { border: 0; border-radius: 7px; padding: .38rem .7rem; font-size: .78rem; font-weight: 650; cursor: pointer; }
  .approval-actions .approve { background: #2e7d5b; color: #eafff4; }
  .approval-actions .deny { background: #3a2030; color: #f0a0a0; }
  .device-row { display: flex; align-items: center; justify-content: space-between; gap: .6rem; padding: .5rem 0; border-top: 1px solid #1a1e36; }
  .device-row:first-of-type { border-top: 0; }
  .device-label { font-size: .84rem; color: #c5c9e8; font-weight: 600; }
  .device-sub { font-size: .72rem; color: #6b7294; margin-top: .15rem; }
  .btn-primary.small, .btn-secondary.small { padding: .35rem .7rem; font-size: .78rem; border-radius: 7px; }
  .pair-qr { display: block; margin: .5rem auto .2rem; width: 180px; height: 180px; border-radius: 10px; background: #fff; padding: 8px; }
  .pair-code { font-family: ui-monospace, Menlo, monospace; font-size: 1.3rem; letter-spacing: .12em; color: #c5c9e8; text-align: center; padding: .5rem; background: #0e1020; border-radius: 8px; margin-top: .4rem; }
  .pair-url { font-size: .68rem; color: #6b7294; text-align: center; margin-top: .3rem; word-break: break-all; }
  .redeem-row { gap: .5rem; }
  .redeem-input { flex: 1; background: #0e1020; color: #d7dcf5; border: 1px solid #1a1e36; border-radius: 7px; padding: .4rem .55rem; font-size: .82rem; }
  .redeem-msg { font-size: .76rem; color: #8a91b8; margin-top: .35rem; }
  header { display: flex; align-items: center; justify-content: space-between; }
  header span { color: #8b85ff; text-transform: uppercase; letter-spacing: .08em; font-size: .68rem; }
  h1 { margin: .1rem 0 0; font-size: 1.35rem; }
  h2 { margin: .2rem 0 .5rem; font-size: .92rem; color: #c8cbe8; }
  .banner { border-radius: 8px; padding: .65rem .8rem; }
  .banner.err { color: #ff9a9a; background: rgba(255, 90, 90, .12); }
  .hero { border: 1px solid #252a46; border-radius: 8px; background: #111426; padding: 16px; display: flex; align-items: center; justify-content: space-between; gap: 12px; }
  .hero strong, .hero small { display: block; }
  .hero small { color: #8f96bb; margin-top: 2px; }
  .hero span { border-radius: 999px; padding: .2rem .55rem; background: rgba(240, 176, 112, .12); color: #f0b070; font-size: .75rem; }
  .hero span.ok { background: rgba(76, 175, 130, .12); color: #72d9aa; }
  .hero span.warn { background: rgba(240, 176, 112, .12); color: #f0b070; }
  .quick-grid { display: grid; grid-template-columns: repeat(4, 1fr); gap: 8px; }
  .quick-grid button, .metrics button { background: #1c1f35; color: #e8eaf6; border: 1px solid #2a2f4a; border-radius: 8px; padding: 13px 8px; }
  section { border: 1px solid #20243d; background: #10121f; border-radius: 8px; padding: 14px; }
  .metrics { display: grid; grid-template-columns: repeat(3, 1fr); gap: 8px; }
  .metrics button { text-align: left; }
  .metrics strong { display: block; font-size: 1.35rem; }
  .metrics small { color: #8f96bb; }
  .list { display: flex; flex-direction: column; gap: 8px; }
  .list button { text-align: left; background: #15182a; border: 1px solid #252a46; color: inherit; border-radius: 8px; padding: 11px; }
  .list strong, .list small { display: block; }
  .list small, .empty { color: #8f96bb; font-size: .8rem; line-height: 1.4; }
  .empty.good { color: #72d9aa; }
  @media (max-width: 540px) {
    .quick-grid { grid-template-columns: repeat(2, 1fr); }
    .metrics { grid-template-columns: 1fr; }
  }
</style>
