<script>
  // Left capability palette. Preserves Wave 1 behavior: agents/tools/providers
  // groups with counts, loaded via the host catalog bridge. Same raw API shapes.
  export let catalog = null   // { agents, tools, providers } | null
  export let status = ''      // human-readable load status
  export let statusKind = ''  // '' | 'ok' | 'warn' | 'error'
  export let error = ''       // hard error message (overrides lists)

  function agentItems(c) {
    const agents = (c && c.agents && c.agents.agents) || []
    return agents.map((a) => ({ label: a.name || a.id || 'agent', sub: a.description || '' }))
  }
  function toolItems(c) {
    const t = (c && c.tools) || {}
    const py = t.python_tools || []
    const mcp = t.mcp_tools || []
    const builtins = t.builtins || []
    const items = []
    builtins.forEach((x) => items.push({ label: x.name, sub: 'builtin' }))
    py.forEach((x) => items.push({ label: x.name, sub: 'python' }))
    mcp.forEach((x) => items.push({ label: x.name || x.full_name, sub: 'mcp · ' + (x.server || '') }))
    return items
  }
  function providerItems(c) {
    const providers = (c && c.providers && c.providers.providers) || {}
    const def = (c && c.providers && c.providers.default_provider) || ''
    return Object.keys(providers).map((name) => {
      const p = providers[name] || {}
      const parts = []
      if (p.model) parts.push(p.model)
      if (name === def) parts.push('default')
      return { label: name, sub: parts.join(' · ') }
    })
  }

  $: agents = error ? [] : agentItems(catalog)
  $: tools = error ? [] : toolItems(catalog)
  $: providers = error ? [] : providerItems(catalog)

  const groups = [
    { key: 'agents', icon: '🤖', title: 'Agents', get: () => agents, empty: 'No agents' },
    { key: 'tools', icon: '🛠️', title: 'Tools', get: () => tools, empty: 'No tools' },
    { key: 'providers', icon: '🧠', title: 'Providers', get: () => providers, empty: 'No providers' },
  ]
</script>

<aside class="palette" aria-label="Capability palette">
  <h2 class="palette-title">Palette</h2>
  <div class="palette-status {statusKind ? 'status-' + statusKind : ''}" role="status">{status}</div>

  {#each groups as g (g.key)}
    <section class="group">
      <h3 class="group-head">
        <span class="group-icon" aria-hidden="true">{g.icon}</span>
        {g.title}
        <span class="group-count">{error ? 0 : g.get().length}</span>
      </h3>
      <ul class="group-list">
        {#if error}
          <li class="item item-error">{error}</li>
        {:else if g.get().length === 0}
          <li class="item item-empty">{g.empty}</li>
        {:else}
          {#each g.get() as it}
            <li class="item" draggable="true">
              <span class="item-label">{it.label}</span>
              {#if it.sub}<span class="item-sub">{it.sub}</span>{/if}
            </li>
          {/each}
        {/if}
      </ul>
    </section>
  {/each}
</aside>

<style>
  .palette {
    flex: 0 0 260px;
    background: var(--bg-elev);
    border-right: 1px solid var(--border);
    padding: 16px;
    overflow-y: auto;
  }
  .palette-title {
    margin: 0 0 4px;
    font-size: 13px;
    text-transform: uppercase;
    letter-spacing: 1px;
    color: var(--text-muted);
  }
  .palette-status {
    margin: 0 0 16px;
    font-size: 12px;
    color: var(--text-muted);
  }
  .palette-status.status-ok { color: var(--ok); }
  .palette-status.status-warn { color: var(--warn); }
  .palette-status.status-error { color: var(--error); }
  .group { margin-bottom: 18px; }
  .group-head {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0 0 8px;
    font-size: 13px;
    font-weight: 600;
    color: var(--text);
  }
  .group-icon { font-size: 14px; }
  .group-count {
    margin-left: auto;
    min-width: 22px;
    padding: 1px 8px;
    text-align: center;
    background: var(--bg-elev-2);
    border: 1px solid var(--border);
    border-radius: 999px;
    font-size: 11px;
    color: var(--text-muted);
  }
  .group-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .item {
    display: flex;
    flex-direction: column;
    padding: 8px 10px;
    background: var(--bg-elev-2);
    border: 1px solid var(--border);
    border-radius: 8px;
    cursor: grab;
    transition: border-color 0.12s ease, transform 0.12s ease;
  }
  .item:hover { border-color: var(--accent); transform: translateX(2px); }
  .item:active { cursor: grabbing; }
  .item-label { font-size: 13px; color: var(--text); word-break: break-word; }
  .item-sub { margin-top: 2px; font-size: 11px; color: var(--text-muted); word-break: break-word; }
  .item-empty, .item-error {
    cursor: default;
    color: var(--text-muted);
    font-size: 12px;
    font-style: italic;
  }
  .item-empty:hover, .item-error:hover { border-color: var(--border); transform: none; }
  .item-error { color: var(--error); font-style: normal; }
</style>
