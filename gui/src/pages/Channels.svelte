<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'

  let channels = []
  let loading  = true
  let error    = ''
  let notice   = ''
  let actionLoading = {}
  let restartNeeded = false
  let restarting = false

  // Edit modal state
  let editing   = null   // the channel being edited
  let form      = {}     // key → value
  let botsForm  = []
  let saving    = false

  async function load() {
    loading = true
    error   = ''
    try {
      const res = await api.channels.list()
      channels  = res.channels || []
    } catch (e) {
      error = e.message
    } finally {
      loading = false
    }
  }

  async function toggleChannel(ch) {
    actionLoading = { ...actionLoading, [ch.id]: true }
    notice = ''
    try {
      const res = ch.enabled ? await api.channels.disable(ch.id) : await api.channels.enable(ch.id)
      notice = res.message || 'Saved.'
      restartNeeded = true
      await load()
    } catch (e) {
      error = e.message
    } finally {
      actionLoading = { ...actionLoading, [ch.id]: false }
    }
  }

  function openEdit(ch) {
    editing = ch
    form = {}
    for (const f of ch.schema || []) {
      // For secrets that are already set, leave the box blank — blank means "keep".
      form[f.key] = f.secret && ch.settings?.[f.key] === '***' ? '' : (ch.settings?.[f.key] || '')
    }
    botsForm = (ch.bots || []).map(bot => {
      const copy = {}
      for (const f of ch.bot_schema || ch.schema || []) {
        copy[f.key] = f.secret && bot[f.key] === '***' ? '' : (bot[f.key] || '')
      }
      copy._adapter_id = bot._adapter_id || ''
      return copy
    })
  }

  function closeEdit() {
    editing = null
    form = {}
    botsForm = []
  }

  async function saveEdit() {
    if (!editing) return
    saving = true
    error  = ''
    try {
      const body = { settings: form }
      if (editing.multi_bot) body.bots = botsForm
      const res = await api.channels.update(editing.id, body)
      notice = res.message || 'Channel saved.'
      restartNeeded = true
      closeEdit()
      await load()
    } catch (e) {
      error = e.message
    } finally {
      saving = false
    }
  }

  onMount(load)

  function statusColor(ch) {
    if (ch.status?.connected) return '#4caf82'
    if (ch.enabled && ch.configured) return '#f0a060'  // should connect but isn't
    return '#555a7a'
  }
  function statusLabel(ch) {
    if (ch.status?.connected) return ch.status.detail || 'connected'
    if (ch.always) return 'always on'
    if (ch.enabled && !ch.configured) return 'needs config'
    if (ch.enabled) return ch.status?.detail || 'restart to connect'
    if (ch.configured) return 'disabled'
    return 'not configured'
  }

  const CHANNEL_ICONS = {
    whatsapp: '📱', telegram: '✈️', slack: '💬', discord: '🎮', http: '🌐', email: '📧',
  }
  function chanIcon(id = '') { return CHANNEL_ICONS[id.toLowerCase()] || '⚡' }

  function displayValue(ch, f) {
    const v = ch.settings?.[f.key]
    if (!v) return '—'
    if (f.secret) return '•••• set'
    return v
  }

  function mappingRows(ch) {
    if (ch.bots?.length) {
      return ch.bots.map((bot, i) => ({
        adapter_id: bot._adapter_id || (i === 0 ? ch.id : `${ch.id}-${bot.agent_id || i + 1}`),
        agent_id: bot.agent_id || '—',
        connected: bot._connected,
        detail: bot._detail,
      }))
    }
    const agent = ch.settings?.agent_id
    return agent ? [{ adapter_id: ch.id, agent_id: agent, connected: ch.status?.connected, detail: ch.status?.detail }] : []
  }

  function addBotMapping() {
    if (!editing) return
    const row = {}
    for (const f of editing.bot_schema || editing.schema || []) row[f.key] = ''
    botsForm = [...botsForm, row]
  }

  function removeBotMapping(index) {
    botsForm = botsForm.filter((_, i) => i !== index)
  }

  function updateBotField(index, key, value) {
    botsForm = botsForm.map((bot, i) => i === index ? { ...bot, [key]: value } : bot)
  }

  async function restartGateway() {
    restarting = true
    error = ''
    try {
      await api.admin.restart()
      restartNeeded = false
      notice = 'Restart requested. Reconnect this page in a few seconds if it does not refresh automatically.'
    } catch (e) {
      error = e.message
    } finally {
      setTimeout(() => { restarting = false }, 5000)
    }
  }
</script>

<div class="page">
  <div class="page-header">
    <h1>Channels</h1>
    <button class="btn-secondary" on:click={load} disabled={loading}>↺ Refresh</button>
  </div>

  {#if error}
    <div class="banner err">{error}</div>
  {/if}
  {#if notice}
    <div class="banner ok">{notice}</div>
  {/if}
  {#if restartNeeded}
    <div class="banner warn restart-banner">
      <span>Channel settings were saved. Restart the gateway to reconnect adapters.</span>
      <button class="btn-secondary" on:click={restartGateway} disabled={restarting}>
        {restarting ? 'Restarting…' : 'Restart Gateway'}
      </button>
    </div>
  {/if}

  {#if loading && channels.length === 0}
    <div class="empty">Loading channels…</div>
  {:else}
    <div class="channel-grid">
      {#each channels as ch}
        <div class="ch-card" class:enabled={ch.enabled}>
          <div class="ch-header">
            <span class="ch-icon">{chanIcon(ch.id)}</span>
            <div class="ch-identity">
              <span class="ch-name">{ch.name || ch.id}</span>
              <span class="ch-id">{ch.id}</span>
            </div>
            <div class="ch-badge" style="color:{statusColor(ch)}">{statusLabel(ch)}</div>
          </div>

          <div class="ch-body">
            <div class="ch-row">
              <span class="ch-label">Connection</span>
              <span class="ch-val" style="color:{statusColor(ch)}">
                {ch.status?.connected ? '● Live' : '○ Offline'}
              </span>
            </div>
            <div class="ch-row">
              <span class="ch-label">Enabled</span>
              <span class="ch-val">{ch.always ? 'Always' : (ch.enabled ? 'Yes' : 'No')}</span>
            </div>

            {#if ch.schema?.length}
              <div class="settings">
                <span class="settings-title">Settings</span>
                {#each ch.schema as f}
                  <div class="ch-row">
                    <span class="ch-label">{f.label}</span>
                    <span class="ch-val mono">{displayValue(ch, f)}</span>
                  </div>
                {/each}
              </div>
            {:else}
              <div class="ch-row no-settings">No configurable settings.</div>
            {/if}

            {#if mappingRows(ch).length}
              <div class="settings">
                <span class="settings-title">Agent mappings</span>
                {#each mappingRows(ch) as row}
                  <div class="mapping-row">
                    <div>
                      <code>{row.adapter_id}</code>
                      <span>{row.connected ? 'connected' : (row.detail || 'pending restart')}</span>
                    </div>
                    <strong>{row.agent_id}</strong>
                  </div>
                {/each}
              </div>
            {/if}
          </div>

          <div class="ch-footer">
            {#if ch.schema?.length}
              <button class="btn-secondary small-btn" on:click={() => openEdit(ch)}>Edit</button>
            {/if}
            {#if !ch.always}
              <button
                class={ch.enabled ? 'btn-danger' : 'btn-primary'}
                disabled={actionLoading[ch.id] || (!ch.enabled && !ch.configured)}
                title={!ch.enabled && !ch.configured ? 'Add settings first' : ''}
                on:click={() => toggleChannel(ch)}>
                {actionLoading[ch.id] ? '…' : ch.enabled ? 'Disable' : 'Enable'}
              </button>
            {/if}
          </div>
        </div>
      {/each}
    </div>

    <div class="info-card">
      <h3>About channels</h3>
      <p>Configure any channel here — settings are written to your <code>config.yaml</code>. Secrets are masked; leave a secret field blank when editing to keep the existing value. <strong>Restart the gateway</strong> after changes for adapters to connect or disconnect.</p>
    </div>
  {/if}
</div>

<!-- Edit modal -->
{#if editing}
  <div
    class="modal-bg"
    role="button"
    tabindex="0"
    aria-label="Close channel modal"
    on:click|self={closeEdit}
    on:keydown={(e) => e.key === 'Escape' && closeEdit()}
  >
    <div class="modal">
      <h2>{chanIcon(editing.id)} Configure {editing.name}</h2>
      <div class="fields">
        {#each editing.schema as f}
          <label class="field">
            <span class="field-label">
              {f.label}{#if f.required}<span class="req">*</span>{/if}
            </span>
            {#if f.type === 'password'}
              <input type="password" bind:value={form[f.key]}
                placeholder={editing.settings?.[f.key] === '***' ? '•••• (unchanged)' : (f.help || '')} />
            {:else}
              <input type="text" bind:value={form[f.key]} placeholder={f.help || ''} />
            {/if}
            {#if f.help}<span class="field-help">{f.help}</span>{/if}
          </label>
        {/each}
      </div>

      {#if editing.multi_bot}
        <div class="bot-editor">
          <div class="bot-editor-head">
            <div>
              <h3>Bot mappings</h3>
              <p>Each row creates one channel adapter and routes that bot to its agent.</p>
            </div>
            <button class="btn-secondary small-btn" type="button" on:click={addBotMapping}>+ Add bot</button>
          </div>

          {#if botsForm.length === 0}
            <div class="bot-empty">No dedicated bot mappings. The single default fields above will be used.</div>
          {:else}
            <div class="bot-list">
              {#each botsForm as bot, i}
                <div class="bot-card">
                  <div class="bot-card-head">
                    <div>
                      <strong>{i === 0 ? editing.id : `${editing.id}-${bot.agent_id || i + 1}`}</strong>
                      <span>adapter ID</span>
                    </div>
                    <button class="btn-danger tiny" type="button" on:click={() => removeBotMapping(i)}>Remove</button>
                  </div>
                  <div class="bot-fields">
                    {#each editing.bot_schema as f}
                      <label class="field">
                        <span class="field-label">
                          {f.label}{#if f.required}<span class="req">*</span>{/if}
                        </span>
                        {#if f.type === 'password'}
                          <input type="password"
                            value={bot[f.key] || ''}
                            on:input={(e) => updateBotField(i, f.key, e.currentTarget.value)}
                            placeholder={bot[f.key] === '***' ? '•••• (unchanged)' : (f.help || '')} />
                        {:else}
                          <input type="text"
                            value={bot[f.key] || ''}
                            on:input={(e) => updateBotField(i, f.key, e.currentTarget.value)}
                            placeholder={f.help || ''} />
                        {/if}
                      </label>
                    {/each}
                  </div>
                </div>
              {/each}
            </div>
          {/if}
        </div>
      {/if}

      <div class="modal-row">
        <button class="btn-secondary" on:click={closeEdit} disabled={saving}>Cancel</button>
        <button class="btn-primary" on:click={saveEdit} disabled={saving}>
          {saving ? 'Saving…' : 'Save'}
        </button>
      </div>
    </div>
  </div>
{/if}

<style>
  .page        { padding: 1.5rem; display: flex; flex-direction: column; gap: 1.5rem; }
  .page-header { display: flex; align-items: center; justify-content: space-between; }
  .page-header h1 { font-size: 1.2rem; font-weight: 600; }
  .banner { padding: .7rem 1rem; border-radius: 8px; font-size: .85rem; }
  .err    { background: rgba(240,96,96,.1); border: 1px solid rgba(240,96,96,.3); color: #f06060; }
  .ok     { background: rgba(76,175,130,.1); border: 1px solid rgba(76,175,130,.3); color: #4caf82; }
  .warn   { background: rgba(240,196,96,.08); border: 1px solid rgba(240,196,96,.3); color: #f0c460; }
  .restart-banner { display: flex; align-items: center; justify-content: space-between; gap: .75rem; flex-wrap: wrap; }
  .empty  { color: #6b7294; padding: 3rem; text-align: center; }

  .channel-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 1rem; }

  .ch-card {
    background: #141626; border: 1px solid #1a1e36; border-radius: 10px;
    display: flex; flex-direction: column; overflow: hidden; transition: border-color .2s;
  }
  .ch-card.enabled { border-color: rgba(108,99,255,.3); }

  .ch-header {
    display: flex; align-items: center; gap: .75rem;
    padding: .9rem 1rem; border-bottom: 1px solid #1a1e36;
  }
  .ch-icon   { font-size: 1.4rem; }
  .ch-identity { flex: 1; min-width: 0; display: flex; flex-direction: column; }
  .ch-name   { font-weight: 600; font-size: .9rem; }
  .ch-id     { font-size: .72rem; color: #555a7a; font-family: monospace; }
  .ch-badge  { font-size: .7rem; font-weight: 600; text-transform: uppercase; letter-spacing: .04em; text-align: right; }

  .ch-body   { flex: 1; padding: .75rem 1rem; display: flex; flex-direction: column; gap: .45rem; }
  .ch-row    { display: flex; justify-content: space-between; font-size: .82rem; gap: .5rem; }
  .ch-label  { color: #555a7a; flex-shrink: 0; }
  .ch-val    { color: #c8cadf; text-align: right; word-break: break-all; }
  .ch-val.mono { font-family: monospace; font-size: .78rem; color: #8b85ff; }
  .no-settings { color: #555a7a; font-style: italic; }

  .settings { margin-top: .35rem; padding-top: .5rem; border-top: 1px solid #1a1e36; display: flex; flex-direction: column; gap: .4rem; }
  .settings-title { font-size: .68rem; text-transform: uppercase; letter-spacing: .06em; color: #555a7a; font-weight: 600; }
  .mapping-row {
    display: flex; align-items: center; justify-content: space-between; gap: .75rem;
    padding: .45rem .55rem; background: #101323; border: 1px solid #1a1e36; border-radius: 7px;
  }
  .mapping-row div { min-width: 0; display: flex; flex-direction: column; gap: .15rem; }
  .mapping-row code { color: #8b85ff; font-size: .72rem; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .mapping-row span { color: #555a7a; font-size: .68rem; }
  .mapping-row strong { color: #c8cadf; font-size: .78rem; text-align: right; word-break: break-word; }

  .ch-footer { padding: .75rem 1rem; border-top: 1px solid #1a1e36; display: flex; gap: .5rem; justify-content: flex-end; }
  .small-btn { padding: .35rem .9rem; font-size: .8rem; border-radius: 6px; }
  .ch-footer button { padding: .35rem .9rem; font-size: .8rem; border-radius: 6px; }

  .info-card {
    background: #141626; border: 1px solid #1a1e36; border-radius: 10px;
    padding: 1.1rem 1.25rem; display: flex; flex-direction: column; gap: .5rem;
  }
  .info-card h3 { font-size: .875rem; font-weight: 600; }
  .info-card p  { font-size: .82rem; color: #7b82a8; line-height: 1.6; }
  .info-card code { background: #1c1f35; padding: .1rem .35rem; border-radius: 4px; font-size: .78rem; color: #8b85ff; }

  /* Modal */
  .modal-bg {
    position: fixed; inset: 0; background: rgba(0,0,0,.65);
    display: flex; align-items: center; justify-content: center; z-index: 100;
  }
  .modal {
    background: #141626; border: 1px solid #2a2f4a; border-radius: 12px;
    padding: 1.5rem; width: 760px; max-width: 92vw; max-height: 88vh; overflow-y: auto;
    display: flex; flex-direction: column; gap: 1rem;
  }
  .modal h2 { font-size: 1rem; font-weight: 600; }
  .fields { display: flex; flex-direction: column; gap: .85rem; }
  .field  { display: flex; flex-direction: column; gap: .3rem; }
  .field-label { font-size: .8rem; color: #c8cadf; }
  .req { color: #f06060; margin-left: .15rem; }
  .field-help { font-size: .72rem; color: #555a7a; }
  .modal-row { display: flex; gap: .75rem; justify-content: flex-end; }
  .tiny { padding: .22rem .5rem; font-size: .72rem; border-radius: 5px; }
  .bot-editor { border-top: 1px solid #2a2f4a; padding-top: 1rem; display: flex; flex-direction: column; gap: .85rem; }
  .bot-editor-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 1rem; }
  .bot-editor h3 { font-size: .9rem; font-weight: 600; }
  .bot-editor p { font-size: .76rem; color: #7b82a8; margin-top: .2rem; }
  .bot-empty { color: #6b7294; background: #101323; border: 1px dashed #2a2f4a; border-radius: 8px; padding: .8rem; font-size: .8rem; }
  .bot-list { display: flex; flex-direction: column; gap: .8rem; }
  .bot-card { background: #101323; border: 1px solid #1a1e36; border-radius: 8px; padding: .85rem; display: flex; flex-direction: column; gap: .75rem; }
  .bot-card-head { display: flex; align-items: center; justify-content: space-between; gap: .75rem; }
  .bot-card-head div { display: flex; flex-direction: column; gap: .1rem; min-width: 0; }
  .bot-card-head strong { font-family: monospace; color: #8b85ff; font-size: .8rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .bot-card-head span { color: #555a7a; font-size: .68rem; }
  .bot-fields { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: .75rem; }
</style>
