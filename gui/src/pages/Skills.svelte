<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'

  let skills        = []
  let selected      = null
  let loading       = true
  let detailLoading = false
  let error         = ''

  // AgenticSkills provisioner modal
  let asModal    = false
  let asURL      = ''
  let asFetching = false
  let asError    = ''
  let asSuccess  = ''

  async function load() {
    loading = true
    error   = ''
    try {
      const res = await api.skills.list()
      skills = res.skills || []
    } catch (e) {
      error = e.message
    } finally {
      loading = false
    }
  }

  async function select(sk) {
    if (selected?.name === sk.name) {
      selected = null
      return
    }
    detailLoading = true
    selected = sk
    try {
      const full = await api.skills.get(sk.name)
      selected = full
    } catch (e) {
      error = e.message
    } finally {
      detailLoading = false
    }
  }

  function openASModal() {
    asURL     = ''
    asError   = ''
    asSuccess = ''
    asModal   = true
  }
  function closeASModal() { asModal = false }

  async function installFromAS() {
    if (!asURL.trim()) return
    asFetching = true
    asError    = ''
    asSuccess  = ''
    try {
      const res = await api.skills.provisionAgenticSkills({ url: asURL.trim() })
      if (res.ok) {
        asSuccess = res.message || `Skill installed.`
        setTimeout(() => { closeASModal(); load() }, 1800)
      } else {
        asError = res.error || 'Install failed.'
      }
    } catch (e) {
      asError = e.message
    } finally {
      asFetching = false
    }
  }

  onMount(load)
</script>

<div class="page">
  <div class="page-header">
    <h1>Agent Skills</h1>
    <div class="header-actions">
      <button class="btn-as" on:click={openASModal}>⚡ From AgenticSkills</button>
      <button class="btn-secondary" on:click={load} disabled={loading}>↺ Refresh</button>
    </div>
  </div>

  <!-- AgenticSkills provisioner modal -->
  {#if asModal}
    <div
      class="modal-backdrop"
      role="button"
      tabindex="0"
      aria-label="Close AgenticSkills modal"
      on:click|self={closeASModal}
      on:keydown={(e) => e.key === 'Escape' && closeASModal()}
    >
      <div class="modal">
        <div class="modal-header">
          <h2>Install from AgenticSkills</h2>
          <button class="modal-close" on:click={closeASModal}>✕</button>
        </div>

        <p class="as-hint">
          Paste an <strong>agenticskills.io</strong> skill URL and click Install.
          The SKILL.md will be downloaded and hot-loaded — no restart needed.
        </p>

        <input
          class="as-input"
          type="url"
          aria-label="AgenticSkills skill URL"
          placeholder="https://agenticskills.io/skills/frontend-design"
          bind:value={asURL}
          disabled={asFetching}
          on:keydown={(e) => e.key === 'Enter' && installFromAS()}
        />

        {#if asError}
          <div class="as-err">{asError}</div>
        {/if}
        {#if asSuccess}
          <div class="as-ok">✓ {asSuccess}</div>
        {/if}

        <div class="modal-footer">
          <button class="btn-secondary" on:click={closeASModal} disabled={asFetching}>Cancel</button>
          <button class="btn-as" on:click={installFromAS} disabled={asFetching || !asURL.trim()}>
            {asFetching ? 'Installing…' : '⚡ Install'}
          </button>
        </div>
      </div>
    </div>
  {/if}

  {#if error}
    <div class="banner err">{error}</div>
  {/if}

  <div class="layout">
    <!-- Skill list -->
    <section class="list-panel">
      {#if loading}
        <div class="empty">Loading skills…</div>
      {:else if skills.length === 0}
        <div class="empty-state">
          <div class="empty-icon">🧩</div>
          <p>No skills loaded.</p>
          <p class="hint">Install skills into <code>~/.soulacy/skills/</code> or use <code>sy skill install &lt;path&gt;</code>.</p>
        </div>
      {:else}
        {#each skills as sk}
          <button class="skill-row" class:active={selected?.name === sk.name} on:click={() => select(sk)}>
            <div class="skill-row-top">
              <span class="skill-name">{sk.name}</span>
              {#if sk.license}
                <span class="skill-license">{sk.license}</span>
              {/if}
            </div>
            <p class="skill-desc">{sk.description}</p>
            {#if sk.resources?.length}
              <span class="skill-res">{sk.resources.length} resource{sk.resources.length !== 1 ? 's' : ''}</span>
            {/if}
          </button>
        {/each}
      {/if}
    </section>

    <!-- Skill detail -->
    <section class="detail-panel">
      {#if !selected}
        <div class="detail-empty">
          <div class="detail-empty-icon">🧩</div>
          <p>Select a skill to view its instructions and resources.</p>
        </div>
      {:else if detailLoading}
        <div class="detail-empty"><p>Loading…</p></div>
      {:else}
        <div class="detail-header">
          <div>
            <h2 class="detail-name">{selected.name}</h2>
            {#if selected.compatibility}
              <p class="detail-compat">{selected.compatibility}</p>
            {/if}
          </div>
          <div class="detail-meta">
            {#if selected.license}
              <span class="meta-chip">{selected.license}</span>
            {/if}
            {#if selected.metadata?.version}
              <span class="meta-chip">v{selected.metadata.version}</span>
            {/if}
            {#if selected.metadata?.author}
              <span class="meta-chip">by {selected.metadata.author}</span>
            {/if}
          </div>
        </div>

        <p class="detail-description">{selected.description}</p>

        {#if selected.dir}
          <div class="detail-path">
            <span class="path-label">Location</span>
            <code class="path-val">{selected.dir}</code>
          </div>
        {/if}

        {#if selected.resources?.length}
          <div class="resources-section">
            <h3>Resources</h3>
            <ul class="resource-list">
              {#each selected.resources as r}
                <li><code>{r}</code></li>
              {/each}
            </ul>
          </div>
        {/if}

        {#if selected.body}
          <div class="body-section">
            <h3>Instructions</h3>
            <pre class="skill-body">{selected.body}</pre>
          </div>
        {/if}
      {/if}
    </section>
  </div>

  <div class="info-card">
    <h3>Installing skills</h3>
    <p>Copy a skill directory containing a <code>SKILL.md</code> into <code>~/.soulacy/skills/</code> or run:</p>
    <pre class="code-block">sy skill install ./my-skill-dir</pre>
    <p>Skills are automatically injected into the system prompt of every agent as an available_skills catalog. Agents call <code>read_skill</code> to load full instructions when needed.</p>
  </div>
</div>

<style>
  .page        { padding: 1.5rem; display: flex; flex-direction: column; gap: 1.5rem; height: 100%; min-height: 0; }
  .page-header { display: flex; align-items: center; justify-content: space-between; flex-shrink: 0; }
  .page-header h1 { font-size: 1.2rem; font-weight: 600; }
  .header-actions { display: flex; gap: .5rem; align-items: center; }

  /* AgenticSkills button */
  .btn-as {
    padding: .45rem .85rem; border-radius: 7px; font-size: .8rem; font-weight: 600; cursor: pointer;
    background: linear-gradient(135deg, #f59e0b, #d97706); color: #fff; border: none;
    transition: opacity .15s;
  }
  .btn-as:hover    { opacity: .85; }
  .btn-as:disabled { opacity: .5; cursor: not-allowed; }

  /* Modal */
  .modal-backdrop {
    position: fixed; inset: 0; background: rgba(0,0,0,.65); z-index: 200;
    display: flex; align-items: center; justify-content: center;
  }
  .modal {
    background: #141626; border: 1px solid #2a2f4a; border-radius: 12px;
    padding: 1.5rem; width: 520px; max-width: 95vw; max-height: 88vh; overflow-y: auto;
    display: flex; flex-direction: column; gap: 1rem;
  }
  .modal-header { display: flex; align-items: center; justify-content: space-between; }
  .modal-header h2 { font-size: 1rem; font-weight: 700; }
  .modal-close { background: none; border: none; color: #6b7294; font-size: 1rem; cursor: pointer; padding: .2rem; }
  .modal-footer { display: flex; justify-content: flex-end; gap: .5rem; margin-top: .25rem; }
  .as-hint { font-size: .82rem; color: #7b82a8; line-height: 1.5; }
  .as-hint strong { color: #f59e0b; }
  .as-input {
    width: 100%; padding: .6rem .8rem; border-radius: 7px; font-size: .85rem;
    background: #0e1020; border: 1px solid #2a2f4a; color: #c8cadf;
    outline: none; box-sizing: border-box;
  }
  .as-input:focus { border-color: #f59e0b; }
  .as-err { background: rgba(240,96,96,.1); border: 1px solid rgba(240,96,96,.3); color: #f06060; padding: .6rem .8rem; border-radius: 7px; font-size: .82rem; }
  .as-ok  { background: rgba(80,200,120,.1); border: 1px solid rgba(80,200,120,.3); color: #50c878; padding: .6rem .8rem; border-radius: 7px; font-size: .82rem; }
  .banner { padding: .7rem 1rem; border-radius: 8px; font-size: .85rem; flex-shrink: 0; }
  .err    { background: rgba(240,96,96,.1); border: 1px solid rgba(240,96,96,.3); color: #f06060; }

  .layout { display: flex; gap: 1rem; flex: 1; min-height: 0; overflow: hidden; }

  /* List panel */
  .list-panel {
    width: 260px; flex-shrink: 0;
    background: #0e1020; border: 1px solid #1a1e36; border-radius: 10px;
    overflow-y: auto; display: flex; flex-direction: column;
  }
  .empty { padding: 2rem 1rem; text-align: center; color: #6b7294; font-size: .85rem; }
  .empty-state {
    padding: 3rem 1.5rem; text-align: center; display: flex; flex-direction: column;
    align-items: center; gap: .5rem; color: #6b7294;
  }
  .empty-icon { font-size: 2rem; }
  .hint { font-size: .78rem; }
  .hint code { background: #1c1f35; padding: .1rem .3rem; border-radius: 4px; color: #8b85ff; }

  .skill-row {
    width: 100%; text-align: left; background: none; padding: .85rem 1rem;
    border-bottom: 1px solid #1a1e36; border-radius: 0;
    color: #c8cadf; cursor: pointer; transition: background .1s;
    display: flex; flex-direction: column; gap: .3rem;
  }
  .skill-row:hover  { background: #141626; }
  .skill-row.active { background: rgba(108,99,255,.12); }
  .skill-row-top    { display: flex; align-items: center; justify-content: space-between; }
  .skill-name    { font-weight: 600; font-size: .87rem; font-family: monospace; color: #8b85ff; }
  .skill-license { font-size: .68rem; color: #555a7a; }
  .skill-desc    { font-size: .78rem; color: #7b82a8; line-height: 1.4; overflow: hidden; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; }
  .skill-res     { font-size: .7rem; color: #6c63ff; }

  /* Detail panel */
  .detail-panel {
    flex: 1; min-width: 0;
    background: #141626; border: 1px solid #1a1e36; border-radius: 10px;
    overflow-y: auto; padding: 1.25rem; display: flex; flex-direction: column; gap: 1rem;
  }
  .detail-empty {
    flex: 1; display: flex; flex-direction: column; align-items: center;
    justify-content: center; gap: .75rem; color: #6b7294; text-align: center;
  }
  .detail-empty-icon { font-size: 2.5rem; }

  .detail-header { display: flex; align-items: flex-start; justify-content: space-between; gap: 1rem; }
  .detail-name   { font-size: 1.1rem; font-weight: 700; font-family: monospace; color: #8b85ff; }
  .detail-compat { font-size: .78rem; color: #555a7a; margin-top: .2rem; }
  .detail-meta   { display: flex; flex-wrap: wrap; gap: .35rem; }
  .meta-chip {
    font-size: .7rem; padding: .15rem .5rem; border-radius: 999px;
    background: #1a1e36; border: 1px solid #2a2f4a; color: #b0b5d8;
  }
  .detail-description { font-size: .875rem; color: #c8cadf; line-height: 1.6; }

  .detail-path { display: flex; align-items: center; gap: .75rem; }
  .path-label  { font-size: .72rem; color: #555a7a; text-transform: uppercase; letter-spacing: .06em; font-weight: 600; flex-shrink: 0; }
  .path-val    { font-size: .78rem; color: #7b82a8; word-break: break-all; }

  .resources-section h3,
  .body-section h3 {
    font-size: .8rem; text-transform: uppercase; letter-spacing: .06em;
    color: #555a7a; font-weight: 600; margin-bottom: .5rem;
  }
  .resource-list { list-style: none; display: flex; flex-direction: column; gap: .3rem; }
  .resource-list li code {
    font-size: .8rem; color: #f0a060; background: #1a1e36;
    padding: .2rem .5rem; border-radius: 4px;
  }
  .skill-body {
    background: #0e1020; border: 1px solid #1a1e36; border-radius: 8px;
    padding: 1rem 1.1rem; font-family: monospace; font-size: .78rem;
    color: #b0b5d8; line-height: 1.65; white-space: pre-wrap; word-break: break-word;
    max-height: 480px; overflow-y: auto;
  }

  .info-card {
    background: #141626; border: 1px solid #1a1e36; border-radius: 10px;
    padding: 1.1rem 1.25rem; display: flex; flex-direction: column; gap: .5rem; flex-shrink: 0;
  }
  .info-card h3 { font-size: .875rem; font-weight: 600; }
  .info-card p  { font-size: .82rem; color: #7b82a8; line-height: 1.6; }
  .info-card code { background: #1c1f35; padding: .1rem .35rem; border-radius: 4px; font-size: .78rem; color: #8b85ff; }
  .code-block {
    background: #0e1020; border: 1px solid #1a1e36; border-radius: 6px;
    padding: .75rem 1rem; font-family: monospace; font-size: .8rem;
    color: #b0b5d8; white-space: pre;
  }
</style>
