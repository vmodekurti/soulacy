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
  // Saved drafts, shown in their own palette group with a "Draft" badge.
  // Clicking one loads it onto the canvas (onOpenDraft); 🗑 deletes it.
  export let drafts = []          // [{ id, name }]
  export let onOpenDraft = null   // (draftId) => void
  export let onDeleteDraft = null // (draftId, name) => void
  export let browse = { open: false, loading: false, error: '', results: [], message: '' }

  // Each item carries an optional `drag` payload describing what dropping it on
  // the canvas should create. Agents/tools become flow nodes; channels attach
  // to the workflow output. Providers carry no payload (not droppable).
  function agentItems(c) {
    const agents = (c && c.agents && c.agents.agents) || []
    return agents.map((a) => {
      const strat = a.reasoning && String(a.reasoning.strategy || '').toLowerCase()
      // Openable in Studio when it has SOMETHING Studio can edit: a workflow
      // graph, a reasoning strategy (ReAct/Plan-Execute agent — opens the agent
      // editor + SOUL.yaml), or it was authored in Studio (studio_intent) even if
      // its graph is currently empty (e.g. a 0-step build). Plain library/peer
      // agents have none of these and stay drag-only.
      const openable = !!(a.workflow && Array.isArray(a.workflow.nodes) && a.workflow.nodes.length)
        || strat === 'react' || strat === 'plan_execute'
        || !!(a.studio_intent && String(a.studio_intent).trim())
      return {
        label: a.name || a.id || 'agent',
        sub: a.description || '',
        drag: { kind: 'agent', name: a.name || a.id, id: a.id || a.name },
        agentId: a.id,
        openable,
      }
    })
  }
  function toolItems(c) {
    // Builtins + Python tools. MCP tools live in their own group below.
    const t = (c && c.tools) || {}
    const py = t.python_tools || []
    const builtins = t.builtins || []
    const items = []
    builtins.forEach((x) => items.push({ label: x.name, sub: 'builtin', drag: { kind: 'tool', name: x.name } }))
    py.forEach((x) => items.push({ label: x.name, sub: 'python', drag: { kind: 'tool', name: x.name } }))
    return items
  }

  // Skills — drop one to add a read_skill step pre-pointed at that skill. The
  // Studio canvas turns a {kind:'skill'} payload into a read_skill tool node.
  function skillItems(c) {
    const raw = (c && c.skills && (c.skills.skills || c.skills)) || []
    const list = Array.isArray(raw) ? raw : []
    return list.map((s) => {
      const name = (typeof s === 'string') ? s : (s.name || s.id || 'skill')
      const sub = (typeof s === 'object' && s.description) ? s.description : 'skill'
      return { label: name, sub, drag: { kind: 'skill', name } }
    })
  }

  // MCP servers — return one entry PER SERVER, each carrying its callable tools.
  // The palette renders these as collapsible subsections (collapsed by default)
  // so a server exposing dozens of tools doesn't bloat the section; you expand a
  // server to reveal and drag its individual tools. Configured servers with no
  // tools loaded still appear (count 0) so you know they're wired up.
  function mcpServers(c) {
    const tools = (c && c.tools && c.tools.mcp_tools) || []
    const byServer = new Map()
    tools.forEach((x) => {
      const name = x.name || x.full_name
      const server = x.server || 'mcp'
      if (!byServer.has(server)) byServer.set(server, [])
      byServer.get(server).push({ label: name, sub: '', drag: { kind: 'tool', name } })
    })
    const servers = (c && c.mcp && (c.mcp.servers || c.mcp)) || []
    ;(Array.isArray(servers) ? servers : []).forEach((s) => {
      const sid = (typeof s === 'string') ? s : (s.id || s.name)
      if (sid && !byServer.has(sid)) byServer.set(sid, [])
    })
    return Array.from(byServer.entries()).map(([id, srvTools]) => ({ id, tools: srvTools }))
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

  // Per-section collapse state, keyed by group key. Sections start expanded.
  let collapsed = {}
  function toggleGroup(key) {
    collapsed = { ...collapsed, [key]: !collapsed[key] }
  }

  // Per-MCP-server expand state, keyed by server id. Servers start COLLAPSED so
  // the MCP section stays compact; click a server to reveal its tools.
  let srvOpen = {}
  function toggleServer(id) {
    srvOpen = { ...srvOpen, [id]: !srvOpen[id] }
  }

  // Shorten a long description to a concise, one-line-ish summary for the
  // palette. Prefers the first sentence when short; otherwise hard-truncates.
  // The full text stays available via the element's title (hover) tooltip.
  function concise(text, max = 90) {
    if (!text) return ''
    const t = String(text).trim().replace(/\s+/g, ' ')
    const dot = t.indexOf('. ')
    if (dot > 0 && dot <= max) return t.slice(0, dot + 1)
    return t.length > max ? t.slice(0, max - 1).trimEnd() + '…' : t
  }

  // Begin an HTML5 drag carrying the item's node payload. The Studio canvas
  // listens for `application/studio-node` and creates the node on drop.
  function startDrag(e, drag) {
    if (!drag || !e.dataTransfer) return
    e.dataTransfer.setData('application/studio-node', JSON.stringify(drag))
    e.dataTransfer.effectAllowed = 'copy'
  }

  $: agents = error ? [] : agentItems(catalog)
  // Draft items — clickable to load onto the canvas, tagged with a "Draft" badge.
  $: draftItems = (drafts || []).map((d) => ({
    label: d.name || d.id,
    draftId: d.id,
    badge: 'Draft',
  }))
  $: tools = error ? [] : toolItems(catalog)
  $: skills = error ? [] : skillItems(catalog)
  $: mcp = error ? [] : mcpServers(catalog)
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
    { key: 'drafts', icon: '📝', title: 'Drafts', items: draftItems, empty: 'No saved drafts' },
    { key: 'tools', icon: '🛠️', title: 'Tools', items: tools, empty: 'No tools' },
    { key: 'skills', icon: '📚', title: 'Skills', items: skills, empty: 'No skills installed' },
    { key: 'mcp', icon: '🔌', title: 'MCP servers', items: mcp, empty: 'No MCP servers' },
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
    <button type="button" class="group-head" on:click={() => toggleGroup('blocks')} aria-expanded={!collapsed['blocks']}>
      <span class="group-icon" aria-hidden="true">🧩</span>
      Blocks
      <span class="group-toggle" aria-hidden="true">{collapsed['blocks'] ? '▸' : '▾'}</span>
    </button>
    {#if !collapsed['blocks']}
      <ul class="group-list">
        <li
          class="item draggable"
          draggable="true"
          on:dragstart={(e) => startDrag(e, { kind: 'trigger' })}
          title="Drag onto the canvas — the block that STARTS the flow (cron / HTTP / channel). Configure it in the Inspector."
        >
          <span class="item-label">⚡ Trigger</span>
          <span class="item-sub">starts the flow</span>
        </li>
        <li
          class="item draggable"
          draggable="true"
          on:dragstart={(e) => startDrag(e, { kind: 'python' })}
          title="Drag onto the canvas — a custom Python step you can edit in the Inspector"
        >
          <span class="item-label">🐍 Custom Python</span>
          <span class="item-sub">inline script</span>
        </li>
        <li
          class="item draggable"
          draggable="true"
          on:dragstart={(e) => startDrag(e, { kind: 'exit' })}
          title="Drag onto the canvas — the block that ENDS the flow and delivers the result (HTTP / channel / console)."
        >
          <span class="item-label">🏁 Exit</span>
          <span class="item-sub">ends the flow</span>
        </li>
      </ul>
    {/if}
  </section>

  {#each groups as g (g.key)}
    <section class="group">
      <button type="button" class="group-head" on:click={() => toggleGroup(g.key)} aria-expanded={!collapsed[g.key]}>
        <span class="group-icon" aria-hidden="true">{g.icon}</span>
        {g.title}
        <span class="group-count">{g.items.length}</span>
        <span class="group-toggle" aria-hidden="true">{collapsed[g.key] ? '▸' : '▾'}</span>
      </button>
      {#if !collapsed[g.key]}
      <ul class="group-list">
        {#if error}
          <li class="item item-error">{error}</li>
        {:else if g.items.length === 0}
          <li class="item item-empty">{g.empty}</li>
        {:else if g.key === 'mcp'}
          {#each g.items as srv (srv.id)}
            <li class="mcp-server">
              <button
                type="button"
                class="group-head mcp-server-head"
                on:click={() => toggleServer(srv.id)}
                aria-expanded={!!srvOpen[srv.id]}
              >
                <span class="item-label">🔌 {srv.id}</span>
                <span class="group-count">{srv.tools.length}</span>
                <span class="group-toggle" aria-hidden="true">{srvOpen[srv.id] ? '▾' : '▸'}</span>
              </button>
              {#if srvOpen[srv.id]}
                <ul class="group-list mcp-tool-list">
                  {#if srv.tools.length === 0}
                    <li class="item item-empty">no tools loaded</li>
                  {:else}
                    {#each srv.tools as it}
                      <li
                        class="item draggable"
                        draggable="true"
                        on:dragstart={(e) => startDrag(e, it.drag)}
                        title="Drag onto the canvas"
                      >
                        <span class="item-label">{it.label}</span>
                      </li>
                    {/each}
                  {/if}
                </ul>
              {/if}
            </li>
          {/each}
        {:else}
          {#each g.items as it}
            <!-- An openable (top-level / Studio-authored) agent is CLICK-to-open,
                 NOT draggable: dragging it onto the canvas used to wrap it in a
                 one-step workflow and corrupt a stepless agent. Reusable peer
                 agents (non-openable) stay draggable for composition. -->
            <li
              class="item"
              class:draggable={!!it.drag && !it.openable}
              class:openable={it.openable || !!it.draftId}
              draggable={!!it.drag && !it.openable}
              on:dragstart={(e) => { if (!it.openable) startDrag(e, it.drag) }}
              on:click={() => {
                if (it.draftId && onOpenDraft) onOpenDraft(it.draftId)
                else if (it.openable && onOpenAgent) onOpenAgent(it.agentId)
              }}
              on:keydown={(e) => {
                if ((e.key === 'Enter' || e.key === ' ')) {
                  if (it.draftId && onOpenDraft) { e.preventDefault(); onOpenDraft(it.draftId) }
                  else if (it.openable && onOpenAgent) { e.preventDefault(); onOpenAgent(it.agentId) }
                }
              }}
              role={(it.openable || it.draftId) ? 'button' : undefined}
              tabindex={(it.openable || it.draftId) ? 0 : undefined}
              title={it.draftId ? 'Click to load this draft onto the canvas' : (it.openable ? 'Click to open this agent in the editor' : (it.sub ? it.sub : (it.drag ? 'Drag onto the canvas to add as a step' : '')))}
            >
              <span class="item-label">{it.label}</span>
              {#if it.badge}<span class="item-badge">{it.badge}</span>{/if}
              {#if it.sub}<span class="item-sub" title={it.sub}>{concise(it.sub)}</span>{/if}
              {#if it.openable && onDeleteAgent}
                <button
                  class="item-del"
                  type="button"
                  title="Delete this agent"
                  on:click|stopPropagation={() => onDeleteAgent(it.agentId, it.label)}
                >🗑</button>
              {/if}
              {#if it.draftId && onDeleteDraft}
                <button
                  class="item-del"
                  type="button"
                  title="Delete this draft"
                  on:click|stopPropagation={() => onDeleteDraft(it.draftId, it.label)}
                >🗑</button>
              {/if}
            </li>
          {/each}
        {/if}
      </ul>
      {/if}
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
  .group { margin-bottom: 14px; }
  /* Clickable section header (collapse/expand). Reset native button styling so
     it reads as the old <h3> header. */
  .group-head {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    margin: 0 0 8px;
    padding: 2px 0;
    font-size: 13px;
    font-weight: 600;
    color: var(--text);
    background: none;
    border: none;
    cursor: pointer;
    text-align: left;
  }
  .group-head:hover { color: var(--accent); }
  .group-toggle { margin-left: 6px; font-size: 10px; color: var(--text-muted); }
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
  /* MCP server subsections: a compact collapsible header per server, with its
     tools nested and slightly indented when expanded. */
  .mcp-server { list-style: none; }
  .mcp-server-head {
    width: 100%;
    padding: 4px 2px;
    font-size: 12px;
    font-weight: 500;
  }
  .mcp-tool-list {
    margin: 6px 0 2px 14px;
    padding-left: 8px;
    border-left: 1px solid var(--border);
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
  .item-badge {
    display: inline-block; margin-left: 6px; padding: 0 6px;
    font-size: 10px; line-height: 16px; border-radius: 8px;
    background: rgba(108,140,255,0.18); color: var(--accent, #6c8cff);
    vertical-align: middle;
  }
  .item-sub {
    margin-top: 2px;
    font-size: 11px;
    color: var(--text-muted);
    word-break: break-word;
    /* Safety net: even after concise() truncation, never let a description grow
       past two lines and dominate the palette. Full text shows on hover. */
    display: -webkit-box;
    -webkit-line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
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
