<script>
  import { onMount } from 'svelte'
  import { apiKey, connected } from './lib/stores.js'
  import Dashboard  from './pages/Dashboard.svelte'
  import Builder    from './pages/Builder.svelte'
  import Flow       from './pages/Flow.svelte'
  import Agents     from './pages/Agents.svelte'
  import Chat       from './pages/Chat.svelte'
  import Memory     from './pages/Memory.svelte'
  import Knowledge  from './pages/Knowledge.svelte'
  import Channels   from './pages/Channels.svelte'
  import Schedule   from './pages/Schedule.svelte'
  import Skills     from './pages/Skills.svelte'
  import Providers  from './pages/Providers.svelte'
  import MCP        from './pages/MCP.svelte'
  import Activity   from './pages/Activity.svelte'
  import Config     from './pages/Config.svelte'
  import Logs       from './pages/Logs.svelte'

  let page = 'dashboard'
  let showKeyModal = false
  let keyInput = ''

  const pages = [
    { id: 'dashboard', icon: '◈', label: 'Dashboard',  group: 'main'    },
    { id: 'builder',   icon: '✦', label: 'Build',       group: 'main'    },
    { id: 'flow',      icon: '⌘', label: 'Flow',        group: 'main'    },
    { id: 'agents',    icon: '⊕', label: 'Agents',     group: 'main'    },
    { id: 'chat',      icon: '◎', label: 'Chat',        group: 'main'    },
    { id: 'memory',    icon: '⊞', label: 'Memory',      group: 'main'    },
    { id: 'knowledge', icon: '📚', label: 'Knowledge',   group: 'main'    },
    { id: 'channels',  icon: '📡', label: 'Channels',   group: 'ops'     },
    { id: 'schedule',  icon: '⏱', label: 'Schedule',   group: 'ops'     },
    { id: 'skills',    icon: '🧩', label: 'Skills',     group: 'ops'     },
    { id: 'mcp',       icon: '🔌', label: 'MCP',        group: 'ops'     },
    { id: 'providers', icon: '⚙', label: 'Providers',  group: 'ops'     },
    { id: 'activity',  icon: '📈', label: 'Activity',   group: 'system'  },
    { id: 'config',    icon: '≡', label: 'Config',      group: 'system'  },
    { id: 'logs',      icon: '📋', label: 'Logs',       group: 'system'  },
  ]

  function navigate(p) {
    page = p
    history.pushState({}, '', '#' + p)
  }

  onMount(() => {
    const applyHash = () => {
      const h = location.hash.slice(1)
      if (h && pages.find(p => p.id === h)) page = h
    }
    applyHash()
    window.addEventListener('popstate', applyHash)
    window.addEventListener('hashchange', applyHash)
  })

  function saveKey() {
    $apiKey = keyInput.trim()
    showKeyModal = false
    window.location.reload()
  }

  function openKeyModal() {
    keyInput = $apiKey
    showKeyModal = true
  }
</script>

<!-- API Key modal -->
{#if showKeyModal}
  <div
    class="modal-bg"
    role="button"
    tabindex="0"
    aria-label="Close API key modal"
    on:click|self={() => showKeyModal = false}
    on:keydown={(e) => e.key === 'Escape' && (showKeyModal = false)}
  >
    <div class="modal">
      <h2>API Key</h2>
      <p>Enter your Soulacy API key. Find it in <code>~/.soulacy/config.yaml</code> or the <code>SOULACY_API_KEY</code> env var.</p>
      <input type="password" bind:value={keyInput}
             placeholder="claw_..."
             on:keydown={(e) => e.key === 'Enter' && saveKey()} />
      <div class="modal-row">
        <button class="btn-secondary" on:click={() => showKeyModal = false}>Cancel</button>
        <button class="btn-primary"   on:click={saveKey}>Save &amp; Reload</button>
      </div>
    </div>
  </div>
{/if}

<div class="layout">
  <!-- Sidebar -->
  <aside class="sidebar">
    <div class="brand">
      <span class="brand-icon">⬡</span>
      <span class="brand-name">Soulacy</span>
    </div>

    <nav>
      {#each ['main', 'ops', 'system'] as group}
        {@const groupPages = pages.filter(p => p.group === group)}
        {#if group !== 'main'}
          <div class="nav-divider"></div>
        {/if}
        {#each groupPages as p}
          <button class="nav-item" class:active={page === p.id} on:click={() => navigate(p.id)}>
            <span class="nav-icon">{p.icon}</span>
            <span class="nav-label">{p.label}</span>
          </button>
        {/each}
      {/each}
    </nav>

    <div class="sidebar-footer">
      <span class="conn-dot" class:live={$connected} title={$connected ? 'Event stream live' : 'Disconnected from event stream'}>
        {$connected ? '● Live' : '○ Offline'}
      </span>
      <button class="icon-btn" on:click={openKeyModal} title="Set API key">🔑</button>
    </div>
  </aside>

  <!-- Main content -->
  <main class="content">
    {#if page === 'dashboard'}
      <Dashboard />
    {:else if page === 'builder'}
      <Builder />
    {:else if page === 'flow'}
      <Flow />
    {:else if page === 'agents'}
      <Agents />
    {:else if page === 'chat'}
      <Chat />
    {:else if page === 'memory'}
      <Memory />
    {:else if page === 'knowledge'}
      <Knowledge />
    {:else if page === 'channels'}
      <Channels />
    {:else if page === 'schedule'}
      <Schedule />
    {:else if page === 'skills'}
      <Skills />
    {:else if page === 'providers'}
      <Providers />
    {:else if page === 'mcp'}
      <MCP />
    {:else if page === 'activity'}
      <Activity />
    {:else if page === 'config'}
      <Config />
    {:else if page === 'logs'}
      <Logs />
    {/if}
  </main>
</div>

<style>
  /* ── Reset & globals ────────────────────────────────────────────── */
  :global(*, *::before, *::after) { box-sizing: border-box; margin: 0; padding: 0; }
  :global(html, body) { height: 100%; }
  :global(body) {
    background: #0c0e1a;
    color: #e8eaf6;
    font-family: 'Inter', system-ui, -apple-system, sans-serif;
    font-size: 14px;
    line-height: 1.5;
  }

  /* ── Form elements ──────────────────────────────────────────────── */
  :global(input), :global(textarea), :global(select) {
    background: #1c1f35;
    border: 1px solid #2a2f4a;
    border-radius: 6px;
    color: #e8eaf6;
    font-size: 14px;
    padding: 0.45rem 0.75rem;
    outline: none;
    width: 100%;
    transition: border-color 0.15s;
  }
  :global(input:focus), :global(textarea:focus), :global(select:focus) {
    border-color: #6c63ff;
    box-shadow: 0 0 0 2px rgba(108, 99, 255, 0.15);
  }
  :global(input:disabled), :global(textarea:disabled), :global(select:disabled) {
    opacity: 0.5; cursor: not-allowed;
  }
  :global(textarea) { resize: vertical; }

  /* ── Buttons ────────────────────────────────────────────────────── */
  :global(button) { cursor: pointer; border: none; font-size: 14px; transition: background 0.15s, opacity 0.15s; }
  :global(button:disabled) { opacity: 0.5; cursor: not-allowed; }

  :global(.btn-primary) {
    background: #6c63ff; color: #fff;
    padding: 0.45rem 1.1rem; border-radius: 6px; font-weight: 500;
  }
  :global(.btn-primary:hover:not(:disabled)) { background: #5b52ef; }

  :global(.btn-secondary) {
    background: #1c1f35; color: #e8eaf6;
    border: 1px solid #2a2f4a;
    padding: 0.45rem 1.1rem; border-radius: 6px;
  }
  :global(.btn-secondary:hover:not(:disabled)) { background: #252840; }

  :global(.btn-danger) {
    background: #7f2020; color: #fff;
    padding: 0.45rem 1.1rem; border-radius: 6px;
  }
  :global(.btn-danger:hover:not(:disabled)) { background: #9f2828; }

  /* ── Layout ─────────────────────────────────────────────────────── */
  .layout { display: flex; height: 100vh; overflow: hidden; }

  /* ── Sidebar ─────────────────────────────────────────────────────── */
  .sidebar {
    width: 210px; flex-shrink: 0;
    background: #0e1020;
    border-right: 1px solid #1a1e36;
    display: flex; flex-direction: column;
  }

  .brand {
    display: flex; align-items: center; gap: 0.6rem;
    padding: 1.1rem 1rem;
    border-bottom: 1px solid #1a1e36;
  }
  .brand-icon { font-size: 1.35rem; color: #6c63ff; }
  .brand-name { font-weight: 700; font-size: 0.95rem; letter-spacing: 0.04em; }

  nav { flex: 1; padding: 0.6rem 0; overflow-y: auto; }
  .nav-divider { height: 1px; background: #1a1e36; margin: .4rem .75rem; }
  .nav-item {
    display: flex; align-items: center; gap: 0.7rem;
    width: 100%; padding: 0.6rem 1rem;
    background: none; color: #6b7294;
    font-size: 0.875rem; font-weight: 500;
    text-align: left; border-radius: 0;
    transition: background 0.1s, color 0.1s;
  }
  .nav-item:hover  { background: #181b30; color: #c8cadf; }
  .nav-item.active { background: rgba(108, 99, 255, 0.12); color: #8b85ff; }
  .nav-icon { font-size: 0.95rem; width: 1.1rem; text-align: center; }

  .sidebar-footer {
    display: flex; align-items: center; justify-content: space-between;
    padding: 0.65rem 1rem;
    border-top: 1px solid #1a1e36;
  }
  .conn-dot { font-size: 0.72rem; font-family: monospace; color: #5a3030; }
  .conn-dot.live { color: #4caf82; }
  .icon-btn { background: none; color: #6b7294; font-size: 0.85rem; padding: 0.15rem; }
  .icon-btn:hover { color: #e8eaf6; }

  /* ── Main content ────────────────────────────────────────────────── */
  .content { flex: 1; overflow-y: auto; display: flex; flex-direction: column; }

  /* ── Modal ───────────────────────────────────────────────────────── */
  .modal-bg {
    position: fixed; inset: 0;
    background: rgba(0, 0, 0, 0.65);
    display: flex; align-items: center; justify-content: center;
    z-index: 100;
  }
  .modal {
    background: #141626; border: 1px solid #2a2f4a; border-radius: 12px;
    padding: 1.5rem; width: 420px;
    display: flex; flex-direction: column; gap: 1rem;
  }
  .modal h2 { font-size: 1rem; font-weight: 600; }
  .modal p  { color: #7b82a8; font-size: 0.85rem; line-height: 1.6; }
  .modal p code { background: #1c1f35; padding: 0.1rem 0.35rem; border-radius: 4px; font-size: 0.8rem; }
  .modal-row { display: flex; gap: 0.75rem; justify-content: flex-end; }
</style>
