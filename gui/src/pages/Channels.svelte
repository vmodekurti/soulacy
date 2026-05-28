<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'

  let channels = []
  let loading  = true
  let error    = ''
  let notice   = ''
  let actionLoading = {}

  // Edit modal state
  let editing   = null   // the channel being edited
  let form      = {}     // key → value
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
  }

  function closeEdit() {
    editing = null
    form = {}
  }

  async function saveEdit() {
    if (!editing) return
    saving = true
    error  = ''
    try {
      const res = await api.channels.update(editing.id, { settings: form })
      notice = res.message || 'Channel saved.'
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
  <div class="modal-bg" on:click|self={closeEdit}>
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
    padding: 1.5rem; width: 440px; max-width: 92vw;
    display: flex; flex-direction: column; gap: 1rem;
  }
  .modal h2 { font-size: 1rem; font-weight: 600; }
  .fields { display: flex; flex-direction: column; gap: .85rem; }
  .field  { display: flex; flex-direction: column; gap: .3rem; }
  .field-label { font-size: .8rem; color: #c8cadf; }
  .req { color: #f06060; margin-left: .15rem; }
  .field-help { font-size: .72rem; color: #555a7a; }
  .modal-row { display: flex; gap: .75rem; justify-content: flex-end; }
</style>
