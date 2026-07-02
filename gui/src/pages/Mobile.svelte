<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'

  let loading = true
  let error = ''
  let health = null
  let schedule = []
  let agents = []
  let doctor = null

  async function load() {
    loading = true
    error = ''
    try {
      const [h, s, a, d] = await Promise.allSettled([
        api.health(),
        api.schedule.list(),
        api.agents.list(),
        api.providers.doctor(),
      ])
      if (h.status === 'fulfilled') health = h.value
      if (s.status === 'fulfilled') schedule = s.value?.schedule || []
      if (a.status === 'fulfilled') agents = a.value?.agents || []
      if (d.status === 'fulfilled') doctor = d.value
      const failed = [h, s, a, d].find(x => x.status === 'rejected')
      if (failed) error = failed.reason?.message || 'Some mobile data could not be loaded'
    } finally {
      loading = false
    }
  }

  function go(page) {
    window.location.hash = '#' + page
  }

  $: providerIssues = (doctor?.providers || []).filter(x => x.status !== 'ok')
  $: channelIssues = (doctor?.channels || []).filter(x => x.status === 'fail')
  $: enabledAgents = agents.filter(a => a.enabled)

  onMount(load)
</script>

<div class="mobile-page">
  <header>
    <div>
      <span>Companion</span>
      <h1>Operations</h1>
    </div>
    <button class="btn-secondary" on:click={load} disabled={loading}>↻</button>
  </header>

  {#if error}<div class="banner err">{error}</div>{/if}

  <section class="hero">
    <div>
      <strong>{health ? 'Gateway online' : loading ? 'Checking gateway…' : 'Gateway unknown'}</strong>
      <small>{enabledAgents.length} enabled agents · {schedule.length} scheduled jobs</small>
    </div>
    <span class:ok={health}>{health ? 'Live' : 'Check'}</span>
  </section>

  <div class="quick-grid">
    <button on:click={() => go('chat')}>Chat</button>
    <button on:click={() => go('activity')}>Activity</button>
    <button on:click={() => go('schedule')}>Schedule</button>
    <button on:click={() => go('channels')}>Channels</button>
  </div>

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
  .quick-grid { display: grid; grid-template-columns: repeat(4, 1fr); gap: 8px; }
  .quick-grid button { background: #1c1f35; color: #e8eaf6; border: 1px solid #2a2f4a; border-radius: 8px; padding: 13px 8px; }
  section { border: 1px solid #20243d; background: #10121f; border-radius: 8px; padding: 14px; }
  .list { display: flex; flex-direction: column; gap: 8px; }
  .list button { text-align: left; background: #15182a; border: 1px solid #252a46; color: inherit; border-radius: 8px; padding: 11px; }
  .list strong, .list small { display: block; }
  .list small, .empty { color: #8f96bb; font-size: .8rem; line-height: 1.4; }
  .empty.good { color: #72d9aa; }
  @media (max-width: 540px) {
    .quick-grid { grid-template-columns: repeat(2, 1fr); }
  }
</style>
