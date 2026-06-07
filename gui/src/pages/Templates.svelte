<script>
  // Templates tab (Story E21): the template catalog as a first-class page —
  // four default agentic workflows ship embedded (Meeting Minutes, Inbox
  // Triage, Market Monitor, Compliance Auditor) alongside the starter
  // templates; user-dir templates appear with a "user" badge. One click
  // creates a ready-to-run agent.
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'

  let templates = []
  let loading   = true
  let error     = ''
  let notice    = ''
  let instantiating = ''

  const WORKFLOW_TAG = 'workflow'

  async function load() {
    loading = true
    error   = ''
    try {
      const res = await api.templates.list()
      templates = res.templates || []
    } catch (e) {
      error = e.message
    } finally {
      loading = false
    }
  }

  async function useTemplate(t) {
    instantiating = t.name
    error  = ''
    notice = ''
    try {
      const def = await api.templates.instantiate(t.name)
      notice = `Agent "${def.id}" created from "${t.display_name || t.name}" — find it on the Agents page.`
    } catch (e) {
      error = e.message
    } finally {
      instantiating = ''
    }
  }

  $: workflows = templates.filter(t => (t.tags || []).includes(WORKFLOW_TAG))
  $: starters  = templates.filter(t => !(t.tags || []).includes(WORKFLOW_TAG))

  onMount(load)
</script>

<div class="page">
  <div class="page-header">
    <h1>Templates</h1>
    <button class="btn-secondary" on:click={load} disabled={loading}>↺ Refresh</button>
  </div>

  {#if error}<div class="banner err">⚠ {error}</div>{/if}
  {#if notice}<div class="banner ok">✓ {notice}</div>{/if}

  {#if loading}
    <p class="hint">Loading templates…</p>
  {:else if templates.length === 0}
    <p class="hint">
      No templates available. Defaults ship with the gateway; drop extra
      <code>*.yaml</code> agent definitions in <code>~/.soulacy/templates</code>
      to add your own.
    </p>
  {:else}
    {#if workflows.length > 0}
      <h2 class="section-title">Agentic workflows</h2>
      <p class="hint">Ready-made multi-step workflows — create the agent, follow the setup note in its description, go.</p>
      <div class="grid">
        {#each workflows as t (t.name)}
          <div class="card tpl">
            <div class="tpl-head">
              <h3>{t.display_name || t.name}</h3>
              <span class="badge {t.source}">{t.source}</span>
            </div>
            <p class="desc">{t.description}</p>
            {#if t.tags?.length}
              <div class="tags">{#each t.tags.filter(x => x !== 'template') as tag}<span class="tag">{tag}</span>{/each}</div>
            {/if}
            <button class="btn-primary" on:click={() => useTemplate(t)} disabled={instantiating === t.name}>
              {instantiating === t.name ? 'Creating…' : '⊕ Create agent'}
            </button>
          </div>
        {/each}
      </div>
    {/if}

    {#if starters.length > 0}
      <h2 class="section-title">Starters</h2>
      <div class="grid">
        {#each starters as t (t.name)}
          <div class="card tpl">
            <div class="tpl-head">
              <h3>{t.display_name || t.name}</h3>
              <span class="badge {t.source}">{t.source}</span>
            </div>
            <p class="desc">{t.description}</p>
            {#if t.tags?.length}
              <div class="tags">{#each t.tags.filter(x => x !== 'template') as tag}<span class="tag">{tag}</span>{/each}</div>
            {/if}
            <button class="btn-primary" on:click={() => useTemplate(t)} disabled={instantiating === t.name}>
              {instantiating === t.name ? 'Creating…' : '⊕ Create agent'}
            </button>
          </div>
        {/each}
      </div>
    {/if}
  {/if}
</div>

<style>
  .page { display: flex; flex-direction: column; gap: 14px; }
  .page-header { display: flex; align-items: center; justify-content: space-between; }
  .section-title { font-size: 1rem; margin: .4rem 0 0; color: #c8cbe8; }
  .hint { font-size: .8rem; color: #6b7294; }
  .hint code { background: #1c1f35; padding: .08rem .35rem; border-radius: 4px; }
  .banner { padding: .55rem .8rem; border-radius: 8px; font-size: .85rem; }
  .banner.err { background: rgba(240, 96, 96, .12); color: #f08080; }
  .banner.ok { background: rgba(76, 175, 130, .12); color: #5fce9a; }
  .grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 12px; }
  .card.tpl { display: flex; flex-direction: column; gap: .5rem; padding: .9rem 1rem;
              background: #10121f; border: 1px solid #1a1e36; border-radius: 10px; }
  .tpl-head { display: flex; align-items: baseline; justify-content: space-between; gap: .5rem; }
  .tpl-head h3 { margin: 0; font-size: .95rem; }
  .badge { font-size: .65rem; padding: .1rem .45rem; border-radius: 999px; text-transform: uppercase; }
  .badge.embedded { background: rgba(139, 133, 255, .15); color: #8b85ff; }
  .badge.user { background: rgba(240, 160, 96, .15); color: #f0a060; }
  .desc { font-size: .78rem; color: #9aa0c3; line-height: 1.5; white-space: pre-line; flex: 1; }
  .tags { display: flex; flex-wrap: wrap; gap: .3rem; }
  .tag { font-size: .65rem; background: #1c1f35; color: #6b7294; padding: .1rem .45rem; border-radius: 999px; }
  .btn-primary { align-self: flex-start; }
</style>
