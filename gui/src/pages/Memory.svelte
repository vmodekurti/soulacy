<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'

  // ── State ──────────────────────────────────────────────────────────────────
  let agentStats   = []
  let selectedID   = ''
  let activeTab    = 'episodic'

  // Episodic
  let episodic     = []
  let epSearch     = ''
  let epLoading    = false
  let expandedIDs  = new Set()

  // Procedural
  let procRules    = ''
  let procDraft    = ''
  let procSaving   = false
  let procPreview  = false

  // Context preview
  let previewQuery  = ''
  let previewResult = null
  let previewing    = false

  // Write episodic modal
  let showWrite    = false
  let writeContent = ''
  let writeTags    = ''
  let writing      = false

  // Confirm clear modal
  let showClearConfirm = false
  let clearing     = false

  let error        = null
  let notice       = null
  let brainEnabled = true

  // ── Derived ───────────────────────────────────────────────────────────────
  $: filteredEp = epSearch.trim()
    ? episodic.filter(r =>
        r.content?.toLowerCase().includes(epSearch.toLowerCase()) ||
        (r.tags || []).some(t => t.toLowerCase().includes(epSearch.toLowerCase()))
      )
    : episodic

  $: selectedAgent = agentStats.find(a => a.agent_id === selectedID)
  $: procDirty = procDraft !== procRules

  // ── API helpers ───────────────────────────────────────────────────────────
  async function loadOverview() {
    try {
      const res = await api.brainMemory.stats()
      brainEnabled = res.enabled !== false
      agentStats   = res.agents || []
      if (agentStats.length && !selectedID) {
        selectedID = agentStats[0].agent_id
      }
    } catch (e) { error = e.message }
  }

  async function loadTab() {
    error = null; notice = null
    if (!selectedID) return
    if (activeTab === 'episodic')   await loadEpisodic()
    if (activeTab === 'procedural') await loadProcedural()
    if (activeTab === 'preview')    previewResult = null
  }

  async function loadEpisodic() {
    epLoading = true
    try {
      const res = await api.brainMemory.episodic(selectedID)
      episodic = res.records || []
    } catch (e) { error = e.message }
    epLoading = false
  }

  async function loadProcedural() {
    try {
      const res = await api.brainMemory.procedural(selectedID)
      procRules = res.rules || ''
      procDraft = procRules
    } catch (e) { error = e.message }
  }

  async function saveProcedural() {
    procSaving = true; error = null
    try {
      await api.brainMemory.updateProcedural(selectedID, procDraft)
      procRules = procDraft
      notice = 'Procedural rules saved.'
      setTimeout(() => notice = null, 2500)
      await loadOverview()
    } catch (e) { error = e.message }
    procSaving = false
  }

  async function clearProcedural() {
    if (!confirm('Clear procedural rules for ' + selectedID + '?')) return
    await api.brainMemory.clearProcedural(selectedID).catch(e => error = e.message)
    procRules = ''; procDraft = ''
    await loadOverview()
  }

  async function clearAllEpisodic() {
    clearing = true; error = null
    try {
      await api.brainMemory.clearEpisodic(selectedID)
      episodic = []; showClearConfirm = false
      notice = 'Episodic records cleared.'
      setTimeout(() => notice = null, 2500)
      await loadOverview()
    } catch (e) { error = e.message }
    clearing = false
  }

  async function writeEpisodic() {
    if (!writeContent.trim()) return
    writing = true; error = null
    try {
      const tags = writeTags.split(',').map(t => t.trim()).filter(Boolean)
      await api.brainMemory.writeEpisodic(selectedID, writeContent.trim(), tags)
      writeContent = ''; writeTags = ''; showWrite = false
      await loadEpisodic()
      await loadOverview()
    } catch (e) { error = e.message }
    writing = false
  }

  async function runContextPreview() {
    if (!previewQuery.trim()) return
    previewing = true; error = null; previewResult = null
    try {
      previewResult = await api.brainMemory.contextPreview(selectedID, previewQuery)
    } catch (e) { error = e.message }
    previewing = false
  }

  function toggleExpand(id) {
    if (expandedIDs.has(id)) expandedIDs.delete(id)
    else expandedIDs.add(id)
    expandedIDs = new Set(expandedIDs)
  }

  function fmtDate(iso) {
    if (!iso) return '—'
    try {
      const d = new Date(iso)
      return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
        + ' ' + d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
    } catch { return iso }
  }

  function relTime(iso) {
    if (!iso) return ''
    try {
      const secs = Math.floor((Date.now() - new Date(iso)) / 1000)
      if (secs < 60) return 'just now'
      if (secs < 3600) return Math.floor(secs/60) + 'm ago'
      if (secs < 86400) return Math.floor(secs/3600) + 'h ago'
      return Math.floor(secs/86400) + 'd ago'
    } catch { return '' }
  }

  function markdownToHtml(md) {
    return md
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/^### (.+)$/gm, '<h3>$1</h3>')
      .replace(/^## (.+)$/gm, '<h2>$1</h2>')
      .replace(/^# (.+)$/gm, '<h1>$1</h1>')
      .replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>')
      .replace(/\*(.+?)\*/g, '<em>$1</em>')
      .replace(/`([^`]+)`/g, '<code>$1</code>')
      .replace(/^- (.+)$/gm, '<li>$1</li>')
      .replace(/\n\n/g, '</p><p>')
  }

  $: if (selectedID || activeTab) loadTab()
  onMount(loadOverview)
</script>

<div class="page">
  <!-- Header -->
  <div class="page-header">
    <div class="title-row">
      <span class="page-icon">🧠</span>
      <h1>Brain Memory</h1>
      <span class="subtitle">Episodic · Procedural · Semantic</span>
    </div>
    <div class="hdr-actions">
      <select bind:value={selectedID} class="agent-select">
        {#each agentStats as a}
          <option value={a.agent_id}>{a.agent_name || a.agent_id}</option>
        {/each}
        {#if !agentStats.length}<option value="">No agents</option>{/if}
      </select>
      <button class="btn-icon" title="Refresh" on:click={loadOverview}>↺</button>
    </div>
  </div>

  {#if !brainEnabled}
    <div class="banner warn">
      ⚠ Brain memory is not enabled. Set <code>SOULACY_MEMORY_DIR</code> and restart Soulacy.
    </div>
  {/if}
  {#if error}  <div class="banner err">⚠ {error}</div>{/if}
  {#if notice} <div class="banner ok">✓ {notice}</div>{/if}

  <!-- Stats row -->
  {#if selectedAgent}
    <div class="stats-row">
      <div class="stat-card">
        <div class="stat-val">{selectedAgent.episodic_count}</div>
        <div class="stat-lbl">Episodic records</div>
      </div>
      <div class="stat-card">
        <div class="stat-val {selectedAgent.has_procedural ? 'green' : 'dim'}">{selectedAgent.has_procedural ? '✓ Active' : '—'}</div>
        <div class="stat-lbl">Procedural rules</div>
      </div>
      <div class="stat-card">
        <div class="stat-val dim small">{selectedAgent.last_activity ? relTime(selectedAgent.last_activity) : 'never'}</div>
        <div class="stat-lbl">Last activity</div>
      </div>
      <div class="stat-card mono-val">
        <div class="stat-val dim small">{selectedAgent.agent_id}</div>
        <div class="stat-lbl">Agent ID</div>
      </div>
    </div>
  {/if}

  <!-- Agent chips -->
  {#if agentStats.length > 1}
    <div class="agent-grid">
      {#each agentStats as a}
        <button class="agent-chip {a.agent_id === selectedID ? 'active' : ''}"
          on:click={() => { selectedID = a.agent_id }}>
          <span class="chip-name">{a.agent_name || a.agent_id}</span>
          <div class="chip-badges">
            {#if a.episodic_count > 0}<span class="cbadge ep">{a.episodic_count}</span>{/if}
            {#if a.has_procedural}<span class="cbadge proc">proc</span>{/if}
          </div>
        </button>
      {/each}
    </div>
  {/if}

  <!-- Tabs -->
  <div class="tabs">
    <button class="tab {activeTab==='episodic'?'active':''}" on:click={() => activeTab='episodic'}>
      🕐 Episodic {#if episodic.length}<span class="tab-count">{episodic.length}</span>{/if}
    </button>
    <button class="tab {activeTab==='procedural'?'active':''}" on:click={() => activeTab='procedural'}>
      📋 Procedural {#if procDirty}<span class="tab-dot"></span>{/if}
    </button>
    <button class="tab {activeTab==='preview'?'active':''}" on:click={() => activeTab='preview'}>
      🔍 Context Preview
    </button>
  </div>

  <!-- ══ EPISODIC ══════════════════════════════════════════════════════════ -->
  {#if activeTab === 'episodic'}
    <div class="tab-toolbar">
      <input class="search-input" bind:value={epSearch} placeholder="Search records…" />
      <div style="flex:1"></div>
      <button class="btn-secondary" on:click={() => showWrite=true}>+ Write</button>
      {#if episodic.length}<button class="btn-danger-outline" on:click={() => showClearConfirm=true}>Clear all</button>{/if}
    </div>

    {#if epLoading}
      <div class="empty-state"><div class="spinner"></div></div>
    {:else if filteredEp.length === 0}
      <div class="empty-state">
        <div class="empty-icon">🕐</div>
        <p>{epSearch ? 'No records match.' : 'No episodic records yet. Tasks run with brain_memory.episodic.enabled: true will appear here.'}</p>
      </div>
    {:else}
      <div class="timeline">
        {#each filteredEp as rec (rec.id || rec.timestamp)}
          {@const xp = expandedIDs.has(rec.id || rec.timestamp)}
          <div class="tl-item {xp?'expanded':''}">
            <div class="tl-dot"></div>
            <div class="tl-card" role="button" tabindex="0" aria-expanded={xp}
                 on:click={() => toggleExpand(rec.id || rec.timestamp)}
                 on:keydown={(e) => (e.key === 'Enter' || e.key === ' ') && (e.preventDefault(), toggleExpand(rec.id || rec.timestamp))}>
              <div class="tl-header">
                <span class="tl-time">{fmtDate(rec.timestamp)}</span>
                <span class="tl-rel">{relTime(rec.timestamp)}</span>
                <div style="flex:1"></div>
                {#each (rec.tags||[]) as tag}<span class="tag">{tag}</span>{/each}
                <span class="tl-chevron">{xp?'▲':'▼'}</span>
              </div>
              <div class="tl-preview">{rec.content?.slice(0, xp?99999:180)}{!xp&&(rec.content?.length||0)>180?'…':''}</div>
              {#if xp && rec.id}
                <div class="tl-meta">
                  <span class="meta-item">ID: <code>{rec.id.slice(0,12)}…</code></span>
                  <span class="meta-item">Type: <code>{rec.type||'episodic'}</code></span>
                </div>
              {/if}
            </div>
          </div>
        {/each}
      </div>
      <div class="list-footer">{filteredEp.length} record{filteredEp.length!==1?'s':''}{epSearch&&filteredEp.length!==episodic.length?' (filtered from '+episodic.length+')':''}</div>
    {/if}
  {/if}

  <!-- ══ PROCEDURAL ════════════════════════════════════════════════════════ -->
  {#if activeTab === 'procedural'}
    <div class="tab-toolbar">
      <span class="proc-info">Operating rules injected as <code>## Operating rules</code> in the system prompt.</span>
      <div style="flex:1"></div>
      <button class="btn-icon {procPreview?'active':''}" on:click={() => procPreview=!procPreview} title="Toggle preview">{procPreview?'✎':'👁'}</button>
      {#if procDirty}
        <button class="btn-secondary" on:click={() => { procDraft=procRules }}>Reset</button>
        <button class="btn-primary" on:click={saveProcedural} disabled={procSaving}>{procSaving?'Saving…':'Save'}</button>
      {/if}
      {#if procRules}<button class="btn-danger-outline" on:click={clearProcedural}>Clear</button>{/if}
    </div>

    <div class="proc-pane {procPreview?'split':''}">
      <div class="proc-editor-wrap">
        <textarea class="proc-editor" bind:value={procDraft}
          placeholder="# Operating Rules&#10;&#10;- Always cite sources&#10;- Be concise&#10;&#10;The agent auto-updates these after each task when auto_update: true."
          spellcheck="false"></textarea>
        {#if procDirty}<div class="dirty-badge">unsaved</div>{/if}
      </div>
      {#if procPreview}
        <div class="proc-preview">
          {#if procDraft.trim()}{@html markdownToHtml(procDraft)}{:else}<div class="empty-state-sm">Nothing to preview.</div>{/if}
        </div>
      {/if}
    </div>

    {#if !procRules && !procDirty}
      <div class="proc-hint">
        <span class="hint-icon">💡</span>
        <p>No rules yet. Set <code>brain_memory.procedural.auto_update: true</code> in SOUL.yaml and they will be generated after each task.</p>
      </div>
    {/if}
  {/if}

  <!-- ══ CONTEXT PREVIEW ═══════════════════════════════════════════════════ -->
  {#if activeTab === 'preview'}
    <div class="preview-pane">
      <p class="preview-intro">Preview exactly what memory context will be injected into the system prompt for a given task. Uses <code>BuildContextBlock()</code> with the current memory store.</p>
      <div class="preview-form">
        <textarea class="preview-input" bind:value={previewQuery} rows="3"
          placeholder="e.g. Research the latest developments in agentic AI frameworks…"></textarea>
        <button class="btn-primary" on:click={runContextPreview}
          disabled={previewing||!previewQuery.trim()}>{previewing?'Previewing…':'Preview context →'}</button>
      </div>
      {#if previewResult}
        <div class="preview-meta">
          <div class="meta-chip"><span class="mc-num">{previewResult.episodic_count}</span><span class="mc-lbl">episodic</span></div>
          <div class="meta-chip"><span class="mc-num">{previewResult.semantic_count}</span><span class="mc-lbl">semantic</span></div>
          <div class="meta-chip"><span class="mc-num {previewResult.has_procedural?'green':'dim'}">{previewResult.has_procedural?'✓':'—'}</span><span class="mc-lbl">procedural</span></div>
          <div class="meta-chip"><span class="mc-num">~{previewResult.token_estimate}</span><span class="mc-lbl">tokens</span></div>
        </div>
        {#if previewResult.context_block}
          <div class="context-block-label">Injected context block:</div>
          <pre class="context-block">{previewResult.context_block}</pre>
        {:else}
          <div class="empty-state-sm">No memory to inject for this query yet.</div>
        {/if}
      {/if}
    </div>
  {/if}
</div>

<!-- Write modal -->
{#if showWrite}
  <div class="modal-bg" role="presentation" on:click|self={() => showWrite=false}
       on:keydown={(e) => e.key === 'Escape' && (showWrite = false)}>
    <div class="modal">
      <div class="modal-header"><h2>Write episodic record</h2><button class="modal-close" on:click={() => showWrite=false}>✕</button></div>
      <div class="modal-body">
        <label class="modal-label" for="ep-write-content">Content</label>
        <textarea id="ep-write-content" class="modal-textarea" bind:value={writeContent} rows="5"
          placeholder="Task: Researched X.&#10;Output: Found that Y…"></textarea>
        <label class="modal-label" style="margin-top:.6rem" for="ep-write-tags">Tags <span class="optional">(comma-separated)</span></label>
        <input id="ep-write-tags" class="modal-input" bind:value={writeTags} placeholder="research, ml" />
      </div>
      <div class="modal-footer">
        <button class="btn-secondary" on:click={() => showWrite=false}>Cancel</button>
        <button class="btn-primary" on:click={writeEpisodic} disabled={writing||!writeContent.trim()}>{writing?'Writing…':'Write record'}</button>
      </div>
    </div>
  </div>
{/if}

<!-- Clear confirm modal -->
{#if showClearConfirm}
  <div class="modal-bg" role="presentation" on:click|self={() => showClearConfirm=false}
       on:keydown={(e) => e.key === 'Escape' && (showClearConfirm = false)}>
    <div class="modal modal-sm">
      <div class="modal-header"><h2>Clear all episodic records?</h2><button class="modal-close" on:click={() => showClearConfirm=false}>✕</button></div>
      <div class="modal-body"><p>Permanently delete all <strong>{episodic.length}</strong> records for <strong>{selectedID}</strong>. This cannot be undone.</p></div>
      <div class="modal-footer">
        <button class="btn-secondary" on:click={() => showClearConfirm=false}>Cancel</button>
        <button class="btn-danger" on:click={clearAllEpisodic} disabled={clearing}>{clearing?'Clearing…':'Clear all'}</button>
      </div>
    </div>
  </div>
{/if}

<style>
  .page{padding:1.5rem;display:flex;flex-direction:column;gap:.9rem;height:100%;overflow:hidden}
  .page-header{display:flex;align-items:center;justify-content:space-between;flex-shrink:0;flex-wrap:wrap;gap:.75rem}
  .title-row{display:flex;align-items:center;gap:.6rem}
  .page-icon{font-size:1.35rem}
  h1{font-size:1.15rem;font-weight:700;margin:0}
  .subtitle{font-size:.73rem;color:#6b7294}
  .hdr-actions{display:flex;gap:.5rem;align-items:center}
  .agent-select{min-width:180px}
  .banner{padding:.65rem 1rem;border-radius:8px;font-size:.82rem;flex-shrink:0}
  .banner.err{background:rgba(240,96,96,.1);border:1px solid rgba(240,96,96,.3);color:#f06060}
  .banner.ok{background:rgba(76,175,130,.1);border:1px solid rgba(76,175,130,.3);color:#4caf82}
  .banner.warn{background:rgba(240,160,96,.1);border:1px solid rgba(240,160,96,.3);color:#f0a060}
  code{font-family:monospace;font-size:.8em;background:rgba(255,255,255,.07);padding:.1em .3em;border-radius:3px}
  .stats-row{display:flex;gap:.65rem;flex-shrink:0;flex-wrap:wrap}
  .stat-card{background:#141626;border:1px solid #1a1e36;border-radius:10px;padding:.65rem 1.1rem;min-width:100px;text-align:center}
  .stat-val{font-size:1.4rem;font-weight:700;color:#c5c9e8}
  .stat-val.green{color:#4caf82}.stat-val.dim{color:#6b7294}.stat-val.small{font-size:.9rem}
  .stat-lbl{font-size:.67rem;color:#6b7294;margin-top:.15rem;text-transform:uppercase;letter-spacing:.04em}
  .agent-grid{display:flex;gap:.45rem;flex-wrap:wrap;flex-shrink:0}
  .agent-chip{background:#141626;border:1px solid #1a1e36;border-radius:8px;padding:.35rem .75rem;cursor:pointer;display:flex;flex-direction:column;align-items:flex-start;gap:.18rem;transition:border-color .15s,background .15s}
  .agent-chip:hover{border-color:#6c63ff44;background:#1a1e36}.agent-chip.active{border-color:#6c63ff;background:rgba(108,99,255,.1)}
  .chip-name{font-size:.8rem;font-weight:600;color:#c5c9e8}.chip-badges{display:flex;gap:.28rem}
  .cbadge{font-size:.63rem;padding:.1rem .38rem;border-radius:999px;font-weight:600}
  .cbadge.ep{background:rgba(108,99,255,.2);color:#9b95ff}.cbadge.proc{background:rgba(76,175,130,.2);color:#4caf82}
  .tabs{display:flex;gap:.2rem;flex-shrink:0;border-bottom:1px solid #1a1e36}
  .tab{padding:.5rem 1rem;border-radius:6px 6px 0 0;border:1px solid transparent;border-bottom:none;font-size:.82rem;cursor:pointer;color:#6b7294;background:transparent;transition:color .15s,background .15s;display:flex;align-items:center;gap:.35rem}
  .tab:hover{color:#c5c9e8;background:#141626}.tab.active{color:#c5c9e8;background:#141626;border-color:#1a1e36;border-bottom-color:#141626;margin-bottom:-1px}
  .tab-count{background:rgba(108,99,255,.25);color:#9b95ff;font-size:.63rem;padding:.08rem .32rem;border-radius:999px;font-weight:700}
  .tab-dot{width:6px;height:6px;border-radius:50%;background:#f0a060}
  .tab-toolbar{display:flex;gap:.55rem;align-items:center;flex-shrink:0;padding:.65rem 0 .2rem}
  .search-input{flex:1;max-width:260px}
  .proc-info{font-size:.78rem;color:#6b7294;flex:1}
  .btn-icon{padding:.38rem .65rem;background:#141626;border:1px solid #1a1e36;border-radius:6px;cursor:pointer;font-size:.82rem;color:#6b7294}
  .btn-icon:hover,.btn-icon.active{color:#c5c9e8;border-color:#6c63ff44}
  .btn-danger-outline{padding:.38rem .85rem;background:transparent;border:1px solid rgba(240,96,96,.4);border-radius:6px;color:#f06060;cursor:pointer;font-size:.8rem}
  .btn-danger-outline:hover{background:rgba(240,96,96,.1)}
  .btn-danger{padding:.45rem 1rem;background:rgba(240,96,96,.15);border:1px solid rgba(240,96,96,.5);border-radius:6px;color:#f06060;cursor:pointer;font-size:.82rem}
  .btn-danger:hover:not(:disabled){background:rgba(240,96,96,.25)}
  .empty-state{flex:1;display:flex;flex-direction:column;align-items:center;justify-content:center;gap:.65rem;padding:2.5rem 2rem}
  .empty-icon{font-size:2.2rem;opacity:.35}.empty-state p{color:#6b7294;font-size:.83rem;text-align:center;max-width:360px}
  .empty-state-sm{padding:1.5rem;color:#6b7294;font-size:.82rem;text-align:center}
  .spinner{width:22px;height:22px;border:2px solid #1a1e36;border-top-color:#6c63ff;border-radius:50%;animation:spin .7s linear infinite}
  @keyframes spin{to{transform:rotate(360deg)}}
  .timeline{flex:1;overflow-y:auto;padding:.4rem 0;display:flex;flex-direction:column;gap:.45rem}
  .tl-item{display:flex;gap:.8rem;position:relative}
  .tl-dot{width:9px;height:9px;border-radius:50%;background:#6c63ff;flex-shrink:0;margin-top:.8rem;box-shadow:0 0 0 3px rgba(108,99,255,.15)}
  .tl-card{flex:1;background:#141626;border:1px solid #1a1e36;border-radius:9px;padding:.65rem .95rem;cursor:pointer;transition:border-color .15s,background .15s}
  .tl-card:hover{border-color:#6c63ff44;background:#1a1e36}.tl-item.expanded .tl-card{border-color:#6c63ff55}
  .tl-header{display:flex;align-items:center;gap:.45rem;margin-bottom:.3rem;flex-wrap:wrap}
  .tl-time{font-size:.7rem;color:#6b7294;font-family:monospace}.tl-rel{font-size:.68rem;color:#4a4e6a}.tl-chevron{font-size:.68rem;color:#4a4e6a;margin-left:auto}
  .tl-preview{font-size:.81rem;color:#9da3c0;line-height:1.5;white-space:pre-wrap;word-break:break-word}
  .tl-meta{display:flex;gap:.9rem;margin-top:.55rem;padding-top:.45rem;border-top:1px solid #1a1e36}
  .meta-item{font-size:.7rem;color:#6b7294}.tag{font-size:.63rem;padding:.1rem .4rem;border-radius:999px;background:rgba(108,99,255,.15);color:#9b95ff}
  .list-footer{font-size:.73rem;color:#6b7294;padding:.45rem 0;flex-shrink:0}
  .proc-pane{flex:1;display:flex;gap:.7rem;overflow:hidden}.proc-pane.split .proc-editor-wrap{flex:1}
  .proc-editor-wrap{flex:1;position:relative;display:flex;flex-direction:column}
  .proc-editor{flex:1;width:100%;background:#0e1020;border:1px solid #1a1e36;border-radius:9px;color:#c5c9e8;font-family:'Menlo','Monaco','Fira Code',monospace;font-size:.81rem;line-height:1.6;padding:.9rem;resize:none;tab-size:2}
  .proc-editor:focus{border-color:#6c63ff55;outline:none}
  .proc-preview{flex:1;background:#141626;border:1px solid #1a1e36;border-radius:9px;padding:.9rem 1.1rem;overflow-y:auto;font-size:.82rem;line-height:1.62;color:#c5c9e8}
  .dirty-badge{position:absolute;bottom:.65rem;right:.65rem;font-size:.65rem;padding:.12rem .45rem;border-radius:999px;background:rgba(240,160,96,.15);color:#f0a060;border:1px solid rgba(240,160,96,.3)}
  .proc-hint{display:flex;gap:.65rem;align-items:flex-start;padding:.9rem 1.1rem;background:rgba(108,99,255,.05);border:1px dashed #6c63ff33;border-radius:9px;flex-shrink:0}
  .hint-icon{font-size:1.1rem;flex-shrink:0}.proc-hint p{font-size:.8rem;color:#6b7294;margin:0;line-height:1.5}
  .preview-pane{flex:1;overflow-y:auto;display:flex;flex-direction:column;gap:.9rem}
  .preview-intro{font-size:.81rem;color:#6b7294;line-height:1.55;margin:0}
  .preview-form{display:flex;flex-direction:column;gap:.55rem}
  .preview-input{background:#0e1020;border:1px solid #1a1e36;border-radius:8px;color:#c5c9e8;font-size:.82rem;padding:.7rem .95rem;resize:vertical;font-family:inherit}
  .preview-input:focus{border-color:#6c63ff55;outline:none}
  .preview-meta{display:flex;gap:.65rem;flex-wrap:wrap}
  .meta-chip{background:#141626;border:1px solid #1a1e36;border-radius:8px;padding:.45rem .85rem;display:flex;flex-direction:column;align-items:center;gap:.08rem}
  .mc-num{font-size:1.15rem;font-weight:700;color:#c5c9e8}.mc-num.green{color:#4caf82}.mc-num.dim{color:#6b7294}
  .mc-lbl{font-size:.66rem;color:#6b7294;text-transform:uppercase;letter-spacing:.04em}
  .context-block-label{font-size:.76rem;color:#6b7294;font-weight:600;text-transform:uppercase;letter-spacing:.05em}
  .context-block{background:#0e1020;border:1px solid #1a1e36;border-radius:9px;padding:.9rem;font-size:.78rem;line-height:1.6;color:#9da3c0;white-space:pre-wrap;word-break:break-word;font-family:monospace;overflow-y:auto;max-height:380px;margin:0}
  .modal-bg{position:fixed;inset:0;background:rgba(0,0,0,.6);z-index:1000;display:flex;align-items:center;justify-content:center;padding:1rem}
  .modal{background:#141626;border:1px solid #1a1e36;border-radius:13px;width:100%;max-width:520px;max-height:88vh;display:flex;flex-direction:column}
  .modal .modal-body{overflow-y:auto}
  .modal.modal-sm{max-width:390px}
  .modal-header{display:flex;align-items:center;justify-content:space-between;padding:1rem 1.2rem .7rem;border-bottom:1px solid #1a1e36}
  .modal-header h2{font-size:.95rem;font-weight:600;margin:0}
  .modal-close{background:none;border:none;color:#6b7294;cursor:pointer;font-size:.95rem;padding:.2rem .4rem}
  .modal-body{padding:.9rem 1.2rem;display:flex;flex-direction:column;gap:.45rem}
  .modal-body p{font-size:.83rem;color:#9da3c0;margin:0}
  .modal-label{font-size:.75rem;font-weight:600;color:#6b7294;text-transform:uppercase;letter-spacing:.05em}
  .modal-textarea,.modal-input{width:100%;background:#0e1020;border:1px solid #1a1e36;border-radius:7px;color:#c5c9e8;font-size:.82rem;padding:.6rem .82rem;font-family:inherit}
  .modal-textarea{resize:vertical}.modal-textarea:focus,.modal-input:focus{border-color:#6c63ff55;outline:none}
  .modal-footer{display:flex;gap:.55rem;justify-content:flex-end;padding:.7rem 1.2rem 1rem;border-top:1px solid #1a1e36}
  .optional{font-weight:400;color:#4a4e6a;font-size:.85em}
  .proc-preview :global(h1){font-size:1.05rem;font-weight:700;margin:.45rem 0 .25rem;color:#c5c9e8}
  .proc-preview :global(h2){font-size:.9rem;font-weight:600;margin:.65rem 0 .25rem;color:#c5c9e8}
  .proc-preview :global(h3){font-size:.82rem;font-weight:600;margin:.55rem 0 .2rem;color:#9da3c0}
  .proc-preview :global(strong){color:#c5c9e8}
  .proc-preview :global(li){margin-left:1.1rem;list-style:disc;color:#9da3c0;line-height:1.5}
  .proc-preview :global(code){font-size:.78em}
  .proc-preview :global(p){margin:.25rem 0;color:#9da3c0}
</style>
