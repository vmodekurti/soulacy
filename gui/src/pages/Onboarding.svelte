<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'

  let loading = true
  let error = ''
  let status = null
  let installing = ''
  let notice = ''

  async function load() {
    loading = true
    error = ''
    try {
      status = await api.onboarding.status()
    } catch (e) {
      error = e.message || 'Could not load onboarding status'
    } finally {
      loading = false
    }
  }

  function go(page) {
    if (page) window.location.hash = '#' + page
  }

  async function install(t) {
    installing = t.name
    notice = ''
    error = ''
    try {
      const def = await api.templates.instantiate(t.name)
      notice = `Created ${def.id}. Open Agents or Chat to try it.`
      await load()
    } catch (e) {
      error = e.message || 'Could not install template'
    } finally {
      installing = ''
    }
  }

  function stepIcon(st) {
    if (st.status === 'ok') return '✓'
    if (st.status === 'warn') return '!'
    return '→'
  }

  onMount(load)
</script>

<div class="page">
  <div class="page-header">
    <div>
      <h1>First Run</h1>
      <p>Set up the minimum pieces needed to run agents reliably.</p>
    </div>
    <button class="btn-secondary" on:click={load} disabled={loading}>↻ Refresh</button>
  </div>

  {#if error}<div class="banner err">{error}</div>{/if}
  {#if notice}<div class="banner ok">{notice}</div>{/if}

  {#if loading}
    <div class="empty">Checking your workspace…</div>
  {:else if status}
    <section class="summary" class:done={status.complete}>
      <div>
        <span class="eyebrow">{status.complete ? 'Ready' : 'Setup needed'}</span>
        <h2>{status.complete ? 'Soulacy is ready to operate.' : 'Finish these setup items first.'}</h2>
      </div>
      <button class="btn-primary" on:click={() => go(status.complete ? 'chat' : 'providers')}>
        {status.complete ? 'Open Chat' : 'Continue setup'}
      </button>
    </section>

    <div class="steps">
      {#each status.steps || [] as st}
        <button class="step {st.status}" on:click={() => go(st.href)}>
          <span class="step-icon">{stepIcon(st)}</span>
          <span>
            <strong>{st.label}</strong>
            <small>{st.detail}</small>
          </span>
        </button>
      {/each}
    </div>

    <section class="starter">
      <div class="section-head">
        <h2>Good starters</h2>
        <button class="btn-secondary" on:click={() => go('templates')}>All templates</button>
      </div>
      <div class="template-row">
        {#each status.suggested_templates || [] as t}
          <article>
            <h3>{t.display_name || t.name}</h3>
            <p>{t.description}</p>
            <button class="btn-primary" on:click={() => install(t)} disabled={installing === t.name}>
              {installing === t.name ? 'Installing…' : 'Install'}
            </button>
          </article>
        {/each}
      </div>
    </section>
  {/if}
</div>

<style>
  .page { display: flex; flex-direction: column; gap: 16px; }
  .page-header { display: flex; justify-content: space-between; gap: 16px; align-items: flex-start; }
  .page-header h1 { margin: 0; font-size: 1.35rem; }
  .page-header p, .empty { color: #8f96bb; font-size: .86rem; }
  .banner { padding: .65rem .8rem; border-radius: 8px; font-size: .86rem; }
  .banner.err { color: #ff9a9a; background: rgba(255, 90, 90, .12); }
  .banner.ok { color: #72d9aa; background: rgba(76, 175, 130, .12); }
  .summary { border: 1px solid #252a46; background: #111426; border-radius: 8px; padding: 18px; display: flex; align-items: center; justify-content: space-between; gap: 16px; }
  .summary.done { border-color: rgba(95, 206, 154, .36); background: rgba(76, 175, 130, .08); }
  .summary h2 { margin: .15rem 0 0; font-size: 1.05rem; }
  .eyebrow { color: #8b85ff; font-size: .7rem; text-transform: uppercase; letter-spacing: .06em; }
  .steps { display: grid; grid-template-columns: repeat(auto-fit, minmax(230px, 1fr)); gap: 10px; }
  .step { display: grid; grid-template-columns: 34px 1fr; gap: 10px; text-align: left; align-items: center; padding: 14px; border-radius: 8px; background: #10121f; border: 1px solid #20243d; color: inherit; }
  .step.ok { border-color: rgba(95, 206, 154, .3); }
  .step.warn { border-color: rgba(240, 176, 112, .3); }
  .step.todo { border-color: rgba(255, 90, 90, .3); }
  .step-icon { width: 30px; height: 30px; border-radius: 50%; display: grid; place-items: center; background: #1c1f35; color: #cfd3ee; }
  .step strong { display: block; font-size: .86rem; }
  .step small { display: block; margin-top: 2px; color: #8f96bb; line-height: 1.35; }
  .section-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 10px; }
  .section-head h2 { font-size: 1rem; margin: 0; }
  .template-row { display: grid; grid-template-columns: repeat(auto-fill, minmax(240px, 1fr)); gap: 10px; }
  article { border: 1px solid #20243d; background: #10121f; border-radius: 8px; padding: 14px; display: flex; flex-direction: column; gap: 10px; }
  article h3 { margin: 0; font-size: .92rem; }
  article p { color: #9aa0c3; font-size: .78rem; line-height: 1.45; flex: 1; }
  article button { align-self: flex-start; }
  @media (max-width: 760px) {
    .summary, .page-header { flex-direction: column; align-items: stretch; }
  }
</style>
