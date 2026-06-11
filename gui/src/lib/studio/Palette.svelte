<script>
  // Left capability palette. Preserves Wave 1 behavior: agents/tools/providers
  // groups with counts, loaded via the host catalog bridge. M2 adds a Channels
  // group (counts + names) from the same catalog payload.
  export let catalog = null   // { agents, tools, providers, channels } | null
  export let status = ''      // human-readable load status
  export let statusKind = ''  // '' | 'ok' | 'warn' | 'error'
  export let error = ''       // hard error message (overrides lists)
  // S4.3 (light): "Browse registry" affordance. The parent owns the discover /
  // install state + handlers (reusing the same bridge ops as Needs-setup).
  export let onBrowse = null  // () => void — runs discover with the intent
  export let onInstall = null // (pkg) => void — stages an install
  // Click a workflow-bearing agent to open it on the canvas; delete removes it.
  export let onOpenAgent = null   // (agentId) => void
  export let onDeleteAgent = null // (agentId, name) => void
  export let browse = { open: false, loading: false, error: '', results: [], message: '' }

  // Each item carries an optional `drag` payload describing what dropping it on
  // the canvas should create. Agents/tools become flow nodes; channels attach
  // to the workflow output. Providers carry no payload (not droppable).
  function agentItems(c) {
    const agents = (c && c.agents && c.agents.agents) || []
    return agents.map((a) => ({
      label: a.name || a.id || 'agent',
      sub: a.description || '',
      drag: { kind: 'agent', name: a.name || a.id, id: a.id || a.name },
      agentId: a.id,
      // A workflow-bearing agent can be opened on the canvas to edit/delete.
      hasWorkflow: !!(a.workflow && Array.isArray(a.workflow.nodes) && a.workflow.nodes.length),
    }))
  }
  function toolItems(c) {
    const t = (c && c.tools) || {}
    const py = t.python_tools || []
    const mcp = t.mcp_tools || []
    const builtins = t.builtins || []
    const items = []
    builtins.forEach((x) => items.push({ label: x.name, sub: 'builtin', drag: { kind: 'tool', name: x.name } }))
    py.forEach((x) => items.push({ label: x.name, sub: 'python', drag: { kind: 'tool', name: x.name } }))
    mcp.forEach((x) => {
      const name = x.name || x.full_name
      items.push({ label: name, sub: 'mcp · ' + (x.server || ''), drag: { kind: 'tool', name } })
    })
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

  function channelItems(c) {
    // catalog.channels is the raw GET /channels payload: { channels: [...] }.
    const list = (c && c.channels && c.channels.channels) || []
    return list.map((ch) => {
      const parts = []
      if (ch.enabled) parts.push('enabled')
      else parts.push('disabled')
      if (ch.configured) parts.push('configured')
      return {
        label: ch.name || ch.id || 'channel',
        sub: parts.join(' · '),
        drag: { kind: 'channel', id: ch.id || ch.name, name: ch.name || ch.id },
      }
    })
  }

  // Begin an HTML5 drag carrying the item's node payload. The Studio canvas
  // listens for `application/studio-node` and creates the node on drop.
  function startDrag(e, drag) {
    if (!drag || !e.dataTransfer) return
    e.dataTransfer.setData('application/studio-node', JSON.stringify(drag))
    e.dataTransfer.effectAllowed = 'copy'
  }

  $: agents = error ? [] : agentItems(catalog)
  $: tools = error ? [] : toolItems(catalog)
  $: providers = error ? [] : providerItems(catalog)
  $: channels = error ? [] : channelItems(catalog)

  // `groups` MUST be reactive (`$:`) and carry the resolved arrays inline.
  // The catalog loads asynchronously after mount, so `agents`/`tools`/… start
  // empty and fill in later. If the template read them through an opaque call
  // (e.g. `g.get()`), Svelte couldn't see that the {#each} body depends on
  // those arrays and would never re-render once the data arrived — the palette
  // would stay stuck at its initial empty/zero state. Referencing them here
  // makes `groups` a tracked dependency of agents/tools/providers/channels.
  $: groups = [
    { key: 'agents', icon: '🤖', title: 'Agents', items: agents, empty: 'No agents' },
    { key: 'tools', icon: '🛠️', title: 'Tools', items: tools, empty: 'No tools' },
    { key: 'providers', icon: '🧠', title: 'Providers', items: providers, empty: 'No providers' },
    { key: 'channels', icon: '📡', title: 'Channels', items: channels, empty: 'No channels' },
  ]
</script>

<aside class="palette" aria-label="Capability palette">
  <h2 class="palette-title">Palette</h2>
  <div class="palette-status {statusKind ? 'status-' + statusKind : ''}" role="status">{status}</div>

  <!-- Blocks: synthetic palette items not backed by the catalog. A Custom
       Python block lets you drop an inline script step onto the canvas. -->
  <section class="group">
    <h3 class="group-head">
      <span class="group-icon" aria-hidden="true">🧩</span>
      Blocks
    </h3>
    <ul class="group-list">
      <li
        class="item draggable"
        draggable="true"
        on:dragstart={(e) => startDrag(e, { kind: 'python' })}
        title="Drag onto the canvas — a custom Python step you can edit in the Inspector"
      >
        <span class="item-label">🐍 Custom Python</span>
        <span class="item-sub">inline script</span>
      </li>
    </ul>
  </section>

  {#each groups as g (g.key)}
    <section class="group">
      <h3 class="group-head">
        <span class="group-icon" aria-hidden="true">{g.icon}</span>
        {g.title}
        <span class="group-count">{g.items.length}</span>
      </h3>
      <ul class="group-list">
        {#if error}
          <li class="item item-error">{error}</li>
        {:else if g.items.length === 0}
          <li class="item item-empty">{g.empty}</li>
        {:else}
          {#each g.items as it}
            <li
              class="item"
              class:draggable={!!it.drag}
              class:openable={it.hasWorkflow}
              draggable={!!it.drag}
              on:dragstart={(e) => startDrag(e, it.drag)}
              on:click={() => it.hasWorkflow && onOpenAgent && onOpenAgent(it.agentId)}
              on:keydown={(e) => { if (it.hasWorkflow && onOpenAgent && (e.key === 'Enter' || e.key === ' ')) { e.preventDefault(); onOpenAgent(it.agentId) } }}
              role={it.hasWorkflow ? 'button' : undefined}
              tabindex={it.hasWorkflow ? 0 : undefined}
              title={it.hasWorkflow ? 'Click to edit this workflow · drag to add a handoff step' : (it.drag ? 'Drag onto the canvas' : '')}
            >
              <span class="item-label">{it.label}</span>
              {#if it.sub}<span class="item-sub">{it.sub}</span>{/if}
              {#if it.hasWorkflow && onDeleteAgent}
                <button
                  class="item-del"
                  type="button"
                  title="Delete this agent"
                  on:click|stopPropagation={() => onDeleteAgent(it.agentId, it.label)}
                >🗑</button>
              {/if}
            </li>
          {/each}
        {/if}
      </ul>
    </section>
  {/each}

  {#if onBrowse}
    <section class="group browse-group">
      <button class="browse-btn" on:click={() => onBrowse()} disabled={browse && browse.loading}>
        <span aria-hidden="true">🔎</span>
        {browse && browse.loading ? 'Searching…' : 'Browse registry'}
      </button>
      {#if browse && browse.open}
        {#if browse.error}
          <div class="browse-msg browse-err">⚠ {browse.error}</div>
        {/if}
        {#if browse.message}
          <div class="browse-msg browse-ok">{browse.message}</div>
        {/if}
        {#if browse.results && browse.results.length}
          <ul class="browse-list">
            {#each browse.results as pkg}
              <li class="browse-item">
                <div class="browse-main">
                  <span class="browse-name">{pkg.slug || pkg.name || '(package)'}</span>
                  {#if pkg.provider}<span class="browse-src">{pkg.provider}</span>{/if}
                </div>
                {#if pkg.description}<div class="browse-desc">{pkg.description}</div>{/if}
                {#if onInstall}
                  <button
                    class="browse-install"
                    on:click={() => onInstall(pkg)}
                    disabled={browse.loading}
                    title="Stage this package for install (review & approve in the Plugins page)"
                  >
                    Install
                  </button>
                {/if}
              </li>
            {/each}
          </ul>
        {/if}
      {/if}
    </section>
  {/if}
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
    cursor: default;
    transition: border-color 0.12s ease, transform 0.12s ease;
  }
  .item.draggable { cursor: grab; }
  .item.draggable:active { cursor: grabbing; }
  .item.openable { cursor: pointer; position: relative; }
  .item.openable:hover { border-color: var(--accent); }
  .item-del {
    position: absolute; top: 6px; right: 6px;
    background: none; border: none; color: var(--text-muted);
    font-size: 12px; line-height: 1; padding: 2px; cursor: pointer; opacity: 0;
    transition: opacity 0.12s ease;
  }
  .item.openable:hover .item-del { opacity: 1; }
  .item-del:hover { color: var(--error); }
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

  /* Browse-registry affordance (S4.3) */
  .browse-group { margin-top: 4px; }
  .browse-btn {
    width: 100%;
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 6px;
    padding: 8px 10px;
    background: var(--bg-elev-2);
    border: 1px dashed var(--border);
    border-radius: 8px;
    color: var(--text);
    font-size: 12px;
    font-weight: 600;
    cursor: pointer;
  }
  .browse-btn:hover:not(:disabled) { border-color: var(--accent); }
  .browse-btn:disabled { opacity: 0.5; cursor: not-allowed; }
  .browse-msg { margin-top: 8px; font-size: 11px; }
  .browse-msg.browse-err { color: var(--error, #ff6b81); }
  .browse-msg.browse-ok { color: var(--ok, #36d399); }
  .browse-list { list-style: none; margin: 8px 0 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
  .browse-item {
    background: var(--bg-elev-2);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 8px 10px;
  }
  .browse-main { display: flex; align-items: baseline; gap: 6px; }
  .browse-name { font-size: 12px; font-weight: 600; color: var(--text); word-break: break-word; }
  .browse-src {
    font-size: 10px;
    color: var(--text-muted);
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 6px;
  }
  .browse-desc { margin-top: 3px; font-size: 11px; color: var(--text-muted); word-break: break-word; }
  .browse-install {
    margin-top: 6px;
    padding: 4px 10px;
    background: var(--accent);
    border: 1px solid var(--accent);
    border-radius: 6px;
    color: #fff;
    font-size: 11px;
    font-weight: 600;
    cursor: pointer;
  }
  .browse-install:hover:not(:disabled) { filter: brightness(1.08); }
  .browse-install:disabled { opacity: 0.5; cursor: not-allowed; }
</style>
