<script>
  import { onMount } from 'svelte'
  import { apiKey, connected, authRequired } from './lib/stores.js'
  import Dashboard  from './pages/Dashboard.svelte'
  import Onboarding from './pages/Onboarding.svelte'
  import Studio     from './pages/Studio.svelte'
  import Agents     from './pages/Agents.svelte'
  import Chat       from './pages/Chat.svelte'
  import Memory     from './pages/Memory.svelte'
  import Knowledge  from './pages/Knowledge.svelte'
  import Channels   from './pages/Channels.svelte'
  import Workboard  from './pages/Workboard.svelte'
  import Templates  from './pages/Templates.svelte'
  import Schedule   from './pages/Schedule.svelte'
  import Skills     from './pages/Skills.svelte'
  import Providers  from './pages/Providers.svelte'
  import Secrets    from './pages/Secrets.svelte'
  import MCP        from './pages/MCP.svelte'
  import Activity   from './pages/Activity.svelte'
  import Config     from './pages/Config.svelte'
  import Logs       from './pages/Logs.svelte'
  import Mobile     from './pages/Mobile.svelte'
  import PluginFrame from './pages/PluginFrame.svelte'
  import PluginManager from './pages/PluginManager.svelte'
  import { pageTitle } from './lib/pagetitle.js'
  import { api } from './lib/api.js'
  import { pluginNavEntries, isPluginPage, pluginIdFromPage } from './lib/pluginui.js'

  let page = 'dashboard'
  let pluginPages = []   // nav entries for mounted plugin UIs (E8)
  let showKeyModal = false
  let keyInput = ''
  let sidebarOpen = false   // mobile drawer state (≤768px)
  let navCollapsed = false   // desktop: collapse the left nav to an icon rail

  function toggleNav() {
    navCollapsed = !navCollapsed
    try { localStorage.setItem('soulacy-nav-collapsed', navCollapsed ? '1' : '0') } catch (_) {}
  }

  // Gateway restart (main-menu action): confirm modal + a blocking overlay
  // that polls /health until the replacement process answers, then reloads.
  let showRestartModal = false
  let restarting = false
  let restartError = ''

  const pages = [
    { id: 'dashboard', icon: '◈', label: 'Dashboard',  group: 'main'         },
    { id: 'onboarding', icon: '✓', label: 'First Run',  group: 'main'         },
    { id: 'studio',    icon: '🎬', label: 'Studio',      group: 'main'         },
    { id: 'agents',    icon: '⊕', label: 'Agents',     group: 'main'         },
    { id: 'templates', icon: '📋', label: 'Templates',  group: 'main'         },
    { id: 'chat',      icon: '◎', label: 'Chat',        group: 'main'         },
    { id: 'memory',    icon: '🧠', label: 'Brain Mem',   group: 'capabilities' },
    { id: 'knowledge', icon: '📚', label: 'Knowledge',   group: 'capabilities' },
    { id: 'workboard', icon: '▦', label: 'Workboard',  group: 'capabilities' },
    { id: 'channels',  icon: '📡', label: 'Channels',   group: 'integrations' },
    { id: 'schedule',  icon: '⏱', label: 'Schedule',   group: 'integrations' },
    { id: 'skills',    icon: '🧩', label: 'Skills',     group: 'integrations' },
    { id: 'mcp',       icon: '🔌', label: 'MCP',        group: 'integrations' },
    { id: 'pluginmgr', icon: '🧱', label: 'Plugins',    group: 'integrations' },
    { id: 'providers', icon: '⚙', label: 'Providers',  group: 'integrations' },
    { id: 'secrets',   icon: '🔑', label: 'Secrets',    group: 'integrations' },
    { id: 'activity',  icon: '📈', label: 'Activity',   group: 'system'       },
    { id: 'config',    icon: '≡', label: 'Config',      group: 'system'       },
    { id: 'mobile',    icon: '▣', label: 'Mobile',      group: 'system'       },
    { id: 'logs',      icon: '📋', label: 'Logs',       group: 'system'       },
  ]

  const retiredPages = {
    builder: 'studio',
    build: 'studio',
  }

  // Ordered nav sections with their (optional) uppercase headers, per wireframe.
  const navGroups = [
    { key: 'main',         label: ''             },
    { key: 'capabilities', label: 'Capabilities' },
    { key: 'integrations', label: 'Integrations' },
    { key: 'system',       label: 'System'       },
  ]

  // Keep the browser tab title in sync with the active page (Story 15).
  $: if (typeof document !== 'undefined') document.title = pageTitle(page, pages, pluginPages)

  function navigate(p) {
    p = retiredPages[p] || p
    page = p
    sidebarOpen = false
    history.pushState({}, '', '#' + p)
  }

  function openRestartModal() {
    restartError = ''
    showRestartModal = true
    sidebarOpen = false
  }

  async function restartGateway() {
    if (restarting) return
    restarting = true
    restartError = ''
    try {
      await api.admin.restart()
    } catch (e) {
      // The server exits ~250ms after responding, so the fetch itself may
      // fail with a network error even though the restart was accepted.
      // Only a real auth/permission error should stop us.
      if (e?.status === 401 || e?.status === 403) {
        restarting = false
        showRestartModal = false
        restartError = 'You are not authorized to restart the gateway.'
        return
      }
    }
    showRestartModal = false
    waitForGatewayBack()
  }

  // Poll /health until the re-exec'd gateway answers, then hard-reload the
  // SPA so every store/stream reconnects to the fresh process.
  async function waitForGatewayBack() {
    for (let i = 0; i < 60; i++) {
      await new Promise((r) => setTimeout(r, 1000))
      try {
        await api.health()
        location.reload()
        return
      } catch (_) { /* not back yet — keep polling */ }
    }
    restarting = false
    restartError = 'The gateway did not come back within 60s — check the server logs.'
  }

  onMount(() => {
    const applyHash = () => {
      const h = location.hash.slice(1)
      if (retiredPages[h]) {
        navigate(retiredPages[h])
        return
      }
      if (h && (pages.find(p => p.id === h) || isPluginPage(h))) { page = h; return }
      // Path-based entry (ARCH-6): the SPA fallback serves index.html for any
      // unmatched path, so a deep link / refresh on e.g. /studio lands here
      // with an empty hash. Map the last path segment to a page id when it
      // matches a known route so /studio opens the Studio editor directly.
      if (!h) {
        const seg = location.pathname.replace(/\/+$/, '').split('/').pop()
        if (retiredPages[seg]) {
          navigate(retiredPages[seg])
          return
        }
        if (seg && pages.find(p => p.id === seg)) page = seg
      }
    }
    applyHash()
    try { navCollapsed = localStorage.getItem('soulacy-nav-collapsed') === '1' } catch (_) {}
    window.addEventListener('popstate', applyHash)
    window.addEventListener('hashchange', applyHash)

    // Auth probe: hit an authenticated endpoint. apiFetch flips $authRequired
    // true on 401/403 (→ login screen) and false on success (→ dashboard).
    api.agents.list().then(() => { $authRequired = false }).catch(() => {})

    // Plugin GUI mounts (E8): populate the Plugins nav group.
    api.plugins.ui()
      .then((res) => { pluginPages = pluginNavEntries(res?.mounts) })
      .catch(() => { pluginPages = [] }) // older gateways: no route, no nav group
  })

  // ── Login screen (shown full-screen while $authRequired) ──────────────────
  let loginKey = ''
  let loginError = ''
  let loginChecking = false

  async function submitLogin() {
    const key = loginKey.trim()
    if (!key || loginChecking) return
    loginChecking = true
    loginError = ''
    const prev = $apiKey
    $apiKey = key // apiFetch reads the key from this store
    try {
      await api.agents.list() // validate the key
      $authRequired = false   // success → reveal the app
      loginKey = ''
    } catch (e) {
      $apiKey = prev          // never persist a rejected key
      loginError = (e && (e.status === 401 || e.status === 403))
        ? 'That key was rejected. Double-check it and try again.'
        : (e?.message || 'Could not reach the gateway. Is it running?')
    } finally {
      loginChecking = false
    }
  }

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

<!-- Full-screen login: intercepts the whole UI while authentication is required. -->
{#if $authRequired}
  <div class="login-screen">
    <div class="login-aurora" aria-hidden="true"></div>
    <form class="login-card" on:submit|preventDefault={submitLogin}>
      <div class="login-brand">
        <span class="login-glyph" aria-hidden="true">⬡</span>
      </div>
      <h1 class="login-title">Soulacy</h1>
      <p class="login-sub">Enter your API key to continue.</p>

      <input
        class="login-input"
        type="password"
        autocomplete="current-password"
        placeholder="sy_…"
        bind:value={loginKey}
        disabled={loginChecking}
      />

      {#if loginError}
        <p class="login-error" role="alert">{loginError}</p>
      {/if}

      <button class="login-submit" type="submit" disabled={loginChecking || !loginKey.trim()}>
        {loginChecking ? 'Verifying…' : 'Unlock'}
      </button>

      <p class="login-hint">
        Find your key in <code>~/.soulacy/soulspace/config.yaml</code> (under
        <code>server.api_key</code>) or the <code>SOULACY_API_KEY</code> env var.
      </p>
    </form>
  </div>
{/if}

<!-- API Key modal -->
{#if showRestartModal}
  <div
    class="modal-bg"
    role="button"
    tabindex="0"
    aria-label="Close restart dialog"
    on:click|self={() => showRestartModal = false}
    on:keydown={(e) => e.key === 'Escape' && (showRestartModal = false)}
  >
    <div class="modal">
      <h2>Restart Gateway</h2>
      <p>This stops the running gateway and starts a fresh process. In-flight
         requests are dropped and the UI reconnects automatically once it's back
         (usually a few seconds).</p>
      <div class="modal-row">
        <button class="btn-secondary" on:click={() => showRestartModal = false}>Cancel</button>
        <button class="btn-danger" on:click={restartGateway}>Restart</button>
      </div>
    </div>
  </div>
{/if}

{#if restarting}
  <div class="restart-overlay" aria-live="polite">
    <div class="restart-card">
      <span class="restart-spinner" aria-hidden="true">⟳</span>
      <p>Restarting gateway…</p>
      <small>Reconnecting as soon as the new process answers.</small>
    </div>
  </div>
{/if}

{#if restartError}
  <div class="modal-bg" role="button" tabindex="0" aria-label="Dismiss error"
       on:click|self={() => restartError = ''}
       on:keydown={(e) => e.key === 'Escape' && (restartError = '')}>
    <div class="modal">
      <h2>Restart</h2>
      <p>{restartError}</p>
      <div class="modal-row">
        <button class="btn-primary" on:click={() => restartError = ''}>OK</button>
      </div>
    </div>
  </div>
{/if}

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
  <!-- Mobile top bar (hidden on desktop) -->
  <header class="topbar">
    <button class="hamburger" on:click={() => sidebarOpen = !sidebarOpen}
            aria-label="Toggle navigation" aria-expanded={sidebarOpen}>☰</button>
    <span class="brand-icon">⬡</span>
    <span class="brand-name">Soulacy</span>
  </header>

  <!-- Backdrop behind the mobile drawer -->
  {#if sidebarOpen}
    <div class="backdrop" role="button" tabindex="-1" aria-label="Close navigation"
         on:click={() => sidebarOpen = false}
         on:keydown={(e) => e.key === 'Escape' && (sidebarOpen = false)}></div>
  {/if}

  <!-- Sidebar -->
  <aside class="sidebar" class:open={sidebarOpen} class:collapsed={navCollapsed}>
    <div class="brand">
      <span class="brand-logo" aria-hidden="true"></span>
      <span class="brand-name">Soulacy</span>
      <button class="nav-toggle" on:click={toggleNav}
              title={navCollapsed ? 'Expand menu' : 'Collapse menu'}
              aria-label={navCollapsed ? 'Expand menu' : 'Collapse menu'}>
        {navCollapsed ? '»' : '«'}
      </button>
    </div>

    <nav>
      {#each navGroups as grp}
        {@const groupPages = pages.filter(p => p.group === grp.key)}
        {#if groupPages.length}
          {#if grp.label}<div class="nav-section" aria-hidden="true">{grp.label}</div>{/if}
          {#each groupPages as p}
            <button class="nav-item" class:active={page === p.id} on:click={() => navigate(p.id)} title={p.label}>
              <span class="nav-icon">{p.icon}</span>
              <span class="nav-label">{p.label}</span>
            </button>
          {/each}
        {/if}
      {/each}
      {#if pluginPages.length > 0}
        <div class="nav-section" aria-hidden="true">Plugins</div>
        {#each pluginPages as p}
          <button class="nav-item" class:active={page === p.id} on:click={() => navigate(p.id)} title={p.label}>
            <span class="nav-icon">{p.icon}</span>
            <span class="nav-label">{p.label}</span>
          </button>
        {/each}
      {/if}
    </nav>

    <button class="nav-item nav-action" on:click={openRestartModal}
            title="Restart the gateway server">
      <span class="nav-icon restart-dot">●</span>
      <span class="nav-label">Restart Gateway</span>
    </button>

    <div class="sidebar-footer">
      {#if $authRequired}
        <button class="conn-dot auth-required" on:click={openKeyModal}
                title="The gateway rejected your API key — click to set it">
          🔒 Authentication required
        </button>
      {:else}
        <span class="conn-dot" class:live={$connected} title={$connected ? 'Event stream live' : 'Disconnected from event stream'}>
          {$connected ? '● Live' : '○ Offline'}
        </span>
      {/if}
      <button class="icon-btn" on:click={openKeyModal} title="Set API key">🔑</button>
    </div>
  </aside>

  <!-- Main content -->
  <main class="content">
    {#if page === 'dashboard'}
      <Dashboard />
    {:else if page === 'onboarding'}
      <Onboarding />
    {:else if page === 'studio'}
      <Studio />
    {:else if page === 'agents'}
      <Agents />
    {:else if page === 'templates'}
      <Templates />
    {:else if page === 'chat'}
      <Chat />
    {:else if page === 'memory'}
      <Memory />
    {:else if page === 'knowledge'}
      <Knowledge />
    {:else if page === 'workboard'}
      <Workboard />
    {:else if page === 'channels'}
      <Channels />
    {:else if page === 'schedule'}
      <Schedule />
    {:else if page === 'skills'}
      <Skills />
    {:else if page === 'providers'}
      <Providers />
    {:else if page === 'secrets'}
      <Secrets />
    {:else if page === 'mcp'}
      <MCP />
    {:else if page === 'pluginmgr'}
      <PluginManager />
    {:else if page === 'activity'}
      <Activity />
    {:else if page === 'config'}
      <Config />
    {:else if page === 'mobile'}
      <Mobile />
    {:else if page === 'logs'}
      <Logs />
    {:else if isPluginPage(page)}
      {@const mount = pluginPages.find(p => p.id === page)}
      <PluginFrame
        pluginId={pluginIdFromPage(page)}
        label={mount?.label || pluginIdFromPage(page)}
        url={mount?.url || ''}
      />
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

  /* ── Mobile top bar + drawer (≤768px) ───────────────────────────── */
  .topbar { display: none; }
  .backdrop { display: none; }

  /* ── Sidebar ─────────────────────────────────────────────────────── */
  .sidebar {
    width: 210px; flex-shrink: 0;
    background: #0e1020;
    border-right: 1px solid #1a1e36;
    display: flex; flex-direction: column;
    transition: width 0.16s ease;
  }

  /* Collapsed icon rail (desktop): labels hidden, icons centered. */
  .sidebar.collapsed { width: 56px; }
  .sidebar.collapsed .brand-name,
  .sidebar.collapsed .nav-label,
  .sidebar.collapsed .conn-dot,
  .sidebar.collapsed .nav-section { display: none; }
  .sidebar.collapsed .brand { justify-content: center; padding: 1.1rem 0; gap: 0; position: relative; }
  .sidebar.collapsed .nav-item { justify-content: center; padding: 0.6rem 0; gap: 0; }
  .sidebar.collapsed .nav-icon { width: auto; }
  .sidebar.collapsed .sidebar-footer { justify-content: center; padding: 0.65rem 0; }
  /* When collapsed, the toggle sits under the logo as a small centered pill. */
  .sidebar.collapsed .nav-toggle { position: absolute; bottom: -6px; right: 6px; }

  .nav-toggle {
    margin-left: auto; background: none; border: none;
    color: #6b7294; font-size: 0.9rem; line-height: 1;
    padding: 0.2rem 0.35rem; border-radius: 6px; cursor: pointer;
  }
  .nav-toggle:hover { background: #181b30; color: #c8cadf; }

  @media (max-width: 768px) {
    .layout { flex-direction: column; }

    .topbar {
      display: flex; align-items: center; gap: 0.6rem;
      padding: 0.55rem 0.9rem;
      background: #0e1020;
      border-bottom: 1px solid #1a1e36;
      flex-shrink: 0;
    }
    .hamburger {
      background: none; color: #c8cadf;
      font-size: 1.25rem; line-height: 1;
      padding: 0.25rem 0.5rem; border-radius: 6px;
    }
    .hamburger:hover { background: #181b30; }

    /* Sidebar becomes an off-canvas drawer */
    .sidebar {
      position: fixed; top: 0; bottom: 0; left: 0;
      width: min(260px, 80vw);
      transform: translateX(-105%);
      transition: transform 0.2s ease;
      z-index: 90;
      box-shadow: 4px 0 24px rgba(0, 0, 0, 0.5);
    }
    .sidebar.open { transform: translateX(0); }

    .backdrop {
      display: block;
      position: fixed; inset: 0;
      background: rgba(0, 0, 0, 0.55);
      z-index: 80;
      border: none;
    }

    /* Slightly larger touch targets in the drawer */
    .nav-item { padding: 0.75rem 1rem; }
  }

  /* App-wide responsive defaults for page content */
  @media (max-width: 768px) {
    :global(.page) { padding: 1rem !important; }
    :global(.page-header) { flex-wrap: wrap; gap: 0.6rem; row-gap: 0.6rem; }
    :global(.page-header h1) { font-size: 1.15rem; }
  }

  .brand {
    display: flex; align-items: center; gap: 0.7rem;
    padding: 1.15rem 1.1rem;
  }
  /* Rounded circular logo mark (wireframe): dark disc with an inner accent dot. */
  .brand-logo {
    width: 30px; height: 30px; border-radius: 50%; flex-shrink: 0;
    background: radial-gradient(circle at 50% 50%, #8b85ff 0 5px, transparent 6px),
                linear-gradient(145deg, #2a2f52, #14162a);
    box-shadow: inset 0 0 0 1px #3a3f68;
  }
  .brand-name { font-weight: 700; font-size: 1.02rem; letter-spacing: 0.01em; color: #f2f3fb; }

  nav { flex: 1; padding: 0.5rem 0.5rem; overflow-y: auto; }
  /* Uppercase section header (CAPABILITIES / INTEGRATIONS / …). */
  .nav-section {
    padding: 0.9rem 0.65rem 0.4rem;
    font-size: 0.66rem; font-weight: 600; letter-spacing: 0.09em;
    text-transform: uppercase; color: #565c82;
  }
  .nav-item {
    display: flex; align-items: center; gap: 0.75rem;
    width: 100%; padding: 0.55rem 0.65rem; margin: 0.05rem 0;
    background: none; color: #8188ad;
    font-size: 0.9rem; font-weight: 500;
    text-align: left; border-radius: 9px;
    transition: background 0.1s, color 0.1s;
  }
  .nav-item:hover  { background: #181b30; color: #d4d7ea; }
  .nav-item.active { background: rgba(108, 99, 255, 0.16); color: #b3adff; }
  .nav-icon { font-size: 1rem; width: 1.2rem; text-align: center; }

  /* Action item (not a page): restart the gateway — pinned at the bottom. */
  .nav-action { margin: 0.35rem 0.5rem 0.6rem; width: auto; color: #d98a8a; border-radius: 9px; }
  .nav-action:hover { background: rgba(127, 32, 32, 0.18); color: #ff9d9d; }
  .restart-dot { color: #e06666; font-size: 0.7rem; }

  /* Blocking overlay shown while the gateway re-execs. */
  .restart-overlay {
    position: fixed; inset: 0; z-index: 1000;
    display: flex; align-items: center; justify-content: center;
    background: rgba(8, 10, 20, 0.82);
    backdrop-filter: blur(3px);
  }
  .restart-card {
    text-align: center; color: #e8eaf6;
    background: #14172a; border: 1px solid #2a2f4a;
    border-radius: 12px; padding: 1.75rem 2.25rem;
    box-shadow: 0 12px 40px rgba(0, 0, 0, 0.5);
  }
  .restart-card p { margin: 0.6rem 0 0.25rem; font-weight: 500; }
  .restart-card small { color: #8b8fa8; }
  .restart-spinner {
    display: inline-block; font-size: 1.8rem; color: #8b85ff;
    animation: restart-spin 1s linear infinite;
  }
  @keyframes restart-spin { to { transform: rotate(360deg); } }

  .sidebar-footer {
    display: flex; align-items: center; justify-content: space-between;
    padding: 0.65rem 1rem;
    border-top: 1px solid #1a1e36;
  }
  .conn-dot { font-size: 0.72rem; font-family: monospace; color: #5a3030; }
  .conn-dot.live { color: #4caf82; }
  .conn-dot.auth-required {
    background: none; color: #f0a060; padding: 0;
    font-size: 0.72rem; font-family: monospace; text-align: left;
  }
  .conn-dot.auth-required:hover { color: #ffc08a; text-decoration: underline; }
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
    padding: 1.5rem; width: 420px; max-width: 92vw; max-height: 88vh; overflow-y: auto;
    display: flex; flex-direction: column; gap: 1rem;
  }
  .modal h2 { font-size: 1rem; font-weight: 600; }
  .modal p  { color: #7b82a8; font-size: 0.85rem; line-height: 1.6; }
  .modal p code { background: #1c1f35; padding: 0.1rem 0.35rem; border-radius: 4px; font-size: 0.8rem; }
  .modal-row { display: flex; gap: 0.75rem; justify-content: flex-end; }

  /* ── Login screen (glassmorphic) ──────────────────────────────────────── */
  .login-screen {
    position: fixed; inset: 0; z-index: 1000;
    display: flex; align-items: center; justify-content: center;
    background: radial-gradient(1200px 800px at 50% -10%, hsl(248 60% 16%), hsl(240 40% 6%) 60%);
    overflow: hidden;
  }
  .login-aurora {
    position: absolute; inset: -20%;
    background:
      radial-gradient(40% 40% at 20% 30%, hsla(258, 90%, 60%, 0.35), transparent 70%),
      radial-gradient(35% 35% at 80% 25%, hsla(190, 90%, 55%, 0.25), transparent 70%),
      radial-gradient(45% 45% at 60% 90%, hsla(280, 90%, 60%, 0.22), transparent 70%);
    filter: blur(40px);
    animation: login-drift 18s ease-in-out infinite alternate;
  }
  @keyframes login-drift {
    from { transform: translate3d(-3%, -2%, 0) scale(1); }
    to   { transform: translate3d(3%, 2%, 0) scale(1.08); }
  }
  .login-card {
    position: relative; z-index: 1;
    width: min(380px, 92vw);
    padding: 2.4rem 2rem 1.8rem;
    display: flex; flex-direction: column; align-items: center; gap: 0.5rem;
    background: hsla(240, 30%, 16%, 0.55);
    border: 1px solid hsla(255, 40%, 70%, 0.18);
    border-radius: 20px;
    backdrop-filter: blur(22px) saturate(140%);
    -webkit-backdrop-filter: blur(22px) saturate(140%);
    box-shadow: 0 24px 80px hsla(248, 60%, 4%, 0.6), inset 0 1px 0 hsla(0,0%,100%,0.06);
  }
  .login-glyph {
    font-size: 2.6rem;
    color: hsl(252, 90%, 72%);
    filter: drop-shadow(0 0 16px hsla(252, 90%, 65%, 0.7));
    animation: login-pulse 3.2s ease-in-out infinite;
  }
  @keyframes login-pulse {
    0%,100% { filter: drop-shadow(0 0 12px hsla(252,90%,65%,0.5)); }
    50%     { filter: drop-shadow(0 0 26px hsla(252,90%,70%,0.95)); }
  }
  .login-title { font-size: 1.5rem; font-weight: 700; letter-spacing: 0.06em; color: hsl(0,0%,98%); margin-top: 0.2rem; }
  .login-sub { font-size: 0.85rem; color: hsl(240, 15%, 72%); margin-bottom: 0.6rem; }
  .login-input {
    width: 100%; text-align: center; letter-spacing: 0.04em;
    padding: 0.7rem 0.9rem; font-size: 0.95rem;
    background: hsla(240, 30%, 10%, 0.6);
    border: 1px solid hsla(255, 40%, 70%, 0.2);
    border-radius: 10px; color: hsl(0,0%,96%);
  }
  .login-input:focus { outline: none; border-color: hsl(252, 90%, 68%); box-shadow: 0 0 0 3px hsla(252,90%,65%,0.25); }
  .login-error { font-size: 0.8rem; color: hsl(352, 90%, 72%); margin: 0.1rem 0; text-align: center; }
  .login-submit {
    width: 100%; margin-top: 0.5rem; padding: 0.7rem 1rem;
    font-size: 0.95rem; font-weight: 600; color: #fff; border: none; border-radius: 10px; cursor: pointer;
    background: linear-gradient(135deg, hsl(252, 85%, 62%), hsl(280, 80%, 60%));
    box-shadow: 0 8px 24px hsla(258, 80%, 50%, 0.4);
  }
  .login-submit:hover:not(:disabled) { filter: brightness(1.08); }
  .login-submit:disabled { opacity: 0.55; cursor: not-allowed; }
  .login-hint { margin-top: 0.8rem; font-size: 0.72rem; line-height: 1.5; color: hsl(240, 12%, 60%); text-align: center; }
  .login-hint code { background: hsla(240, 30%, 12%, 0.7); padding: 0.05rem 0.3rem; border-radius: 4px; font-size: 0.7rem; }
</style>
