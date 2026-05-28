<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'

  let agents   = []
  let agentId  = ''
  let entries  = []
  let filtered = []
  let search   = ''
  let loading  = false
  let error    = null
  let notice   = ''

  async function loadAgents() {
    try {
      const res = await api.agents.list()
      agents = res.agents || []
      if (agents.length && !agentId) agentId = agents[0].id
    } catch (e) { error = e.message }
  }

  async function loadMemory() {
    if (!agentId) return
    loading = true; error = null; notice = ''
    try {
      const res = await api.memory.list(agentId)
      if (res.message) {
        // API returns a placeholder message until memory browser is complete
        notice  = res.message
        entries = []
      } else {
        entries = res.entries || res.memories || []
      }
      applyFilter()
    } catch (e) { error = e.message }
    loading = false
  }

  function applyFilter() {
    const q = search.toLowerCase().trim()
    filtered = q
      ? entries.filter(e =>
          e.content?.toLowerCase().includes(q) ||
          e.key?.toLowerCase().includes(q) ||
          e.scope?.toLowerCase().includes(q))
      : [...entries]
  }

  function fmtDate(iso) {
    try { return new Date(iso).toLocaleString() } catch { return iso }
  }

  // Reactive: reload when agent changes, re-filter when search changes
  $: if (agentId) loadMemory()
  $: { search; applyFilter() }

  onMount(loadAgents)
</script>

<div class="page">
  <div class="page-header">
    <h1>Memory Inspector</h1>
    <div class="controls">
      <select bind:value={agentId} style="width:180px">
        {#if !agents.length}
          <option value="">No agents</option>
        {:else}
          {#each agents as a}
            <option value={a.id}>{a.name || a.id}</option>
          {/each}
        {/if}
      </select>
      <input bind:value={search} placeholder="Search…" style="width:180px" />
      <button class="btn-secondary" on:click={loadMemory} disabled={loading}>
        {loading ? '…' : '↺'}
      </button>
    </div>
  </div>

  {#if error}
    <div class="banner err">⚠ {error}</div>
  {/if}
  {#if notice}
    <div class="banner info">ℹ {notice}</div>
  {/if}

  <div class="table-wrap">
    {#if loading}
      <div class="empty">Loading…</div>
    {:else if filtered.length === 0 && !notice}
      <div class="empty">
        {search ? 'No entries match your search.' : 'No memory entries for this agent yet.'}
      </div>
    {:else if filtered.length > 0}
      <table>
        <thead>
          <tr>
            <th>Scope</th>
            <th>Provenance</th>
            <th>Key</th>
            <th>Content</th>
            <th>Created</th>
          </tr>
        </thead>
        <tbody>
          {#each filtered as entry}
            <tr>
              <td><span class="badge scope-{entry.scope}">{entry.scope || '—'}</span></td>
              <td><span class="badge prov-{entry.provenance}">{entry.provenance || '—'}</span></td>
              <td class="mono">{entry.key || '—'}</td>
              <td class="content-cell" title={entry.content}>
                {entry.content?.slice(0, 130)}{(entry.content?.length ?? 0) > 130 ? '…' : ''}
              </td>
              <td class="mono dim">{fmtDate(entry.created_at)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
      <div class="table-footer">{filtered.length} entr{filtered.length === 1 ? 'y' : 'ies'}</div>
    {/if}
  </div>
</div>

<style>
  .page        { padding: 1.5rem; display: flex; flex-direction: column; gap: 1.25rem; height: 100%; }
  .page-header { display: flex; align-items: center; justify-content: space-between; flex-shrink: 0; flex-wrap: wrap; gap: .75rem; }
  .page-header h1 { font-size: 1.2rem; font-weight: 600; }
  .controls    { display: flex; gap: .65rem; align-items: center; flex-wrap: wrap; }

  .banner { padding: .7rem 1rem; border-radius: 8px; font-size: .85rem; flex-shrink: 0; }
  .err    { background: rgba(240,96,96,.1); border: 1px solid rgba(240,96,96,.3); color: #f06060; }
  .info   { background: rgba(108,99,255,.1); border: 1px solid rgba(108,99,255,.3); color: #9b95ff; }

  .table-wrap {
    flex: 1; background: #141626; border: 1px solid #1a1e36; border-radius: 10px;
    overflow: auto; display: flex; flex-direction: column;
  }
  .empty { flex: 1; display: flex; align-items: center; justify-content: center; color: #6b7294; }

  table   { width: 100%; border-collapse: collapse; }
  thead   { position: sticky; top: 0; z-index: 1; }
  th {
    background: #0e1020; color: #6b7294;
    font-size: .7rem; text-transform: uppercase; letter-spacing: .06em;
    padding: .65rem 1rem; text-align: left; border-bottom: 1px solid #1a1e36;
  }
  td { padding: .6rem 1rem; border-bottom: 1px solid #0e1020; font-size: .82rem; vertical-align: top; }
  tr:hover td { background: #1a1e36; }

  .badge  { display: inline-block; padding: .15rem .5rem; border-radius: 999px; font-size: .68rem; font-weight: 600; }
  .scope-session { background: rgba(108,99,255,.15); color: #9b95ff; }
  .scope-agent   { background: rgba(76,175,130,.15);  color: #4caf82; }
  .scope-global  { background: rgba(240,160,96,.15);  color: #f0a060; }
  .prov-confirmed { background: rgba(76,175,130,.15); color: #4caf82; }
  .prov-inferred  { background: rgba(240,160,96,.15); color: #f0a060; }
  .prov-ephemeral { background: rgba(107,114,148,.15);color: #6b7294; }
  .prov-system    { background: rgba(108,99,255,.15); color: #9b95ff; }

  .mono          { font-family: monospace; font-size: .78rem; }
  .dim           { color: #6b7294; }
  .content-cell  { max-width: 380px; word-break: break-word; }
  .table-footer  { padding: .5rem 1rem; font-size: .75rem; color: #6b7294; border-top: 1px solid #1a1e36; flex-shrink: 0; }
</style>
