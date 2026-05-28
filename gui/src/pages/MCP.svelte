<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import KeyValueEditor from '../lib/KeyValueEditor.svelte'

  let servers = []
  let loading = true
  let error   = ''
  let info    = ''
  let expanded = {}      // serverID → bool
  let restartNeeded = false

  // Edit / create modal state. `editing` is null when the modal is closed.
  // When `editing.id` is set AND matches a row in `servers`, we're editing;
  // otherwise we're creating.
  let editing = null
  let saving = false
  let testing = false
  let testResult = null  // { ok, message }

  const BLANK_STDIO = () => ({ id: '', transport: 'stdio', command: '', args: [], env: {}, url: '', headers: {} })

  async function load() {
    loading = true
    error   = ''
    try {
      const res = await api.mcp.list()
      servers = res.servers || []
    } catch (e) {
      error = e.message
    } finally {
      loading = false
    }
  }
  onMount(load)

  function toggle(id) { expanded = { ...expanded, [id]: !expanded[id] } }

  function openNew() {
    editing = BLANK_STDIO()
    testResult = null
    error = ''; info = ''
  }
  function openEdit(s) {
    // We only know transport/connection from the list endpoint — full config
    // (command/args/env/url/headers) lives in config.yaml. The list endpoint
    // doesn't surface those (and probably shouldn't, since env may contain
    // secrets). So Edit is best-effort: we prefill transport + id and let the
    // user fill the rest. Saving sends a full PATCH that overwrites.
    editing = {
      ...BLANK_STDIO(),
      id: s.id,
      transport: s.transport || 'stdio',
    }
    testResult = null
    error = ''; info = ''
  }
  function closeModal() {
    editing = null
    testResult = null
    saving = false
    testing = false
  }

  // Args is an array; expose it as a single text input the user can type
  // space-separated into. Cheap and avoids needing yet another picker.
  function argsToString(arr) { return (arr || []).join(' ') }
  function stringToArgs(str) {
    // Crude split that respects double-quoted segments so `npx -y "@scope/pkg" /path` works.
    const out = []
    const re = /"([^"]*)"|(\S+)/g
    let m
    while ((m = re.exec(str)) !== null) out.push(m[1] !== undefined ? m[1] : m[2])
    return out
  }

  async function testConnection() {
    if (!editing) return
    testing = true
    testResult = null
    try {
      const res = await api.mcp.test(editing)
      testResult = res
    } catch (e) {
      testResult = { ok: false, error: e.message }
    } finally {
      testing = false
    }
  }

  async function save() {
    if (!editing) return
    saving = true
    error = ''; info = ''
    const isExisting = servers.some(s => s.id === editing.id)
    try {
      const res = isExisting
        ? await api.mcp.update(editing.id, editing)
        : await api.mcp.create(editing)
      info = res.message || 'Saved.'
      if (res.restart_needed) restartNeeded = true
      closeModal()
      await load()
    } catch (e) {
      error = e.message
    } finally {
      saving = false
    }
  }

  async function remove(s) {
    if (!confirm(`Remove MCP server "${s.id}"? It will stop accepting tool calls after the next gateway restart.`)) return
    error = ''; info = ''
    try {
      const res = await api.mcp.delete(s.id)
      info = res.message || 'Removed.'
      if (res.restart_needed) restartNeeded = true
      await load()
    } catch (e) {
      error = e.message
    }
  }

  const TRANSPORT_ICON = { stdio: '⎙', http: '🌐', https: '🌐' }

  // Quick-add templates for common stdio MCP servers. Clicking one prefills
  // the editor; user still has to set paths/tokens.
  const TEMPLATES = [
    { id: 'filesystem', label: 'Filesystem',  command: 'npx', args: ['-y', '@modelcontextprotocol/server-filesystem', '/Users/YOU/Documents'] },
    { id: 'github',     label: 'GitHub',      command: 'npx', args: ['-y', '@modelcontextprotocol/server-github'],     env: { GITHUB_TOKEN: '' } },
    { id: 'slack',      label: 'Slack',       command: 'npx', args: ['-y', '@modelcontextprotocol/server-slack'],      env: { SLACK_BOT_TOKEN: '', SLACK_TEAM_ID: '' } },
    { id: 'postgres',   label: 'Postgres',    command: 'npx', args: ['-y', '@modelcontextprotocol/server-postgres', 'postgresql://localhost/dbname'] },
    { id: 'puppeteer',  label: 'Puppeteer',   command: 'npx', args: ['-y', '@modelcontextprotocol/server-puppeteer'] },
    { id: 'fetch',      label: 'Web Fetch',   command: 'uvx', args: ['mcp-server-fetch'] },
  ]
  function applyTemplate(tpl) {
    editing = {
      ...BLANK_STDIO(),
      id: tpl.id,
      transport: 'stdio',
      command: tpl.command,
      args: [...tpl.args],
      env: { ...(tpl.env || {}) },
    }
    testResult = null
  }
</script>

<div class="page">
  <div class="page-header">
    <h1>MCP Servers</h1>
    <div class="header-actions">
      <button class="btn-secondary" on:click={load} disabled={loading}>↺ Refresh</button>
      <button class="btn-primary"   on:click={openNew}>+ New Server</button>
    </div>
  </div>

  {#if restartNeeded}
    <div class="banner warn">
      <strong>Restart needed.</strong> MCP config was modified — your changes are persisted in <code>config.yaml</code>
      but won't be reflected in connected servers until you run <code>build-and-restart.command</code>.
    </div>
  {/if}
  {#if error}<div class="banner err">{error}</div>{/if}
  {#if info}<div class="banner ok">{info}</div>{/if}

  {#if loading && servers.length === 0}
    <div class="empty">Loading…</div>
  {:else if servers.length === 0}
    <div class="empty-card">
      <div class="empty-icon">🔌</div>
      <p>No MCP servers configured.</p>
      <p class="hint">Click <strong>+ New Server</strong> to add one — choose from a template or define your own.</p>
    </div>
  {:else}
    <div class="server-list">
      {#each servers as s}
        <div class="srv" class:ok={s.connected}>
          <div class="srv-head">
            <button class="srv-expand" on:click={() => toggle(s.id)}>
              <span class="srv-icon">{TRANSPORT_ICON[s.transport] || '🔌'}</span>
              <div class="srv-identity">
                <span class="srv-name">{s.id}</span>
                <span class="srv-tx">{s.transport}</span>
              </div>
              <span class="srv-tools-count">{s.tools?.length || 0} tool{s.tools?.length === 1 ? '' : 's'}</span>
              <span class="srv-badge" class:bad={!s.connected}>{s.connected ? '● Connected' : '○ ' + (s.detail || 'Disconnected')}</span>
              <span class="srv-chevron">{expanded[s.id] ? '▾' : '▸'}</span>
            </button>
            <div class="srv-actions">
              <button class="btn-secondary tiny" on:click={() => openEdit(s)}>Edit</button>
              <button class="btn-danger tiny"    on:click={() => remove(s)}>Delete</button>
            </div>
          </div>

          {#if expanded[s.id]}
            <div class="srv-body">
              {#if !s.connected}
                <div class="srv-error">{s.detail || 'Connection failed'}</div>
              {:else if !s.tools || s.tools.length === 0}
                <div class="srv-empty">This server exposes no tools.</div>
              {:else}
                <table class="tbl">
                  <thead>
                    <tr><th>Tool</th><th>LLM-facing name</th><th>Description</th></tr>
                  </thead>
                  <tbody>
                    {#each s.tools as t}
                      <tr>
                        <td class="td-name">{t.name}</td>
                        <td class="td-mono">{t.full_name}</td>
                        <td class="td-desc">{t.description}</td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              {/if}
            </div>
          {/if}
        </div>
      {/each}
    </div>
  {/if}

  <div class="info-card">
    <h3>About MCP</h3>
    <p>
      MCP (<a href="https://spec.modelcontextprotocol.io/" target="_blank" rel="noopener">Model Context Protocol</a>)
      lets Soulacy consume tools from external servers — filesystem, GitHub, Slack, Postgres, web fetch, and many others.
      Tools from connected servers are <strong>auto-injected into every agent</strong> with namespaced names
      (<code>mcp__&lt;server&gt;__&lt;tool&gt;</code>) and routed transparently by the engine.
    </p>
    <p>Changes here are written to <code>config.yaml</code>; the gateway must be restarted to pick them up.</p>
  </div>
</div>

{#if editing}
  <div class="modal-bg" on:click|self={closeModal}>
    <div class="modal wide">
      <h2>{servers.some(s => s.id === editing.id) ? 'Edit' : 'New'} MCP server</h2>

      {#if !servers.some(s => s.id === editing.id)}
        <div class="templates">
          <span class="templates-label">Quick start:</span>
          {#each TEMPLATES as tpl}
            <button class="template-chip" on:click={() => applyTemplate(tpl)}>{tpl.label}</button>
          {/each}
        </div>
      {/if}

      <div class="row-2">
        <div class="field">
          <label>Server ID <span class="req">*</span></label>
          <input type="text" bind:value={editing.id}
                 placeholder="filesystem"
                 disabled={servers.some(s => s.id === editing.id)} />
        </div>
        <div class="field">
          <label>Transport</label>
          <select bind:value={editing.transport}>
            <option value="stdio">stdio (spawn local process)</option>
            <option value="http">http (remote endpoint)</option>
          </select>
        </div>
      </div>

      {#if editing.transport === 'stdio'}
        <div class="field">
          <label>Command <span class="req">*</span></label>
          <input type="text" bind:value={editing.command} placeholder="npx" />
        </div>
        <div class="field">
          <label>Arguments <span class="optional">(space-separated, double-quote to group)</span></label>
          <input type="text"
                 value={argsToString(editing.args)}
                 on:input={(e) => editing.args = stringToArgs(e.target.value)}
                 placeholder='-y "@modelcontextprotocol/server-filesystem" /Users/you/Documents' />
        </div>
        <div class="field">
          <label>Environment variables <span class="optional">(merged onto os.Environ when the process starts)</span></label>
          <KeyValueEditor
            value={editing.env || {}}
            keyLabel="Variable" valueLabel="Value"
            keyPlaceholder="GITHUB_TOKEN" valuePlaceholder="ghp_..."
            maskValues={true}
            on:change={(e) => editing.env = e.detail}
          />
        </div>
      {:else}
        <div class="field">
          <label>URL <span class="req">*</span></label>
          <input type="text" bind:value={editing.url} placeholder="https://example.com/mcp" />
        </div>
        <div class="field">
          <label>Headers <span class="optional">(sent on every request)</span></label>
          <KeyValueEditor
            value={editing.headers || {}}
            keyLabel="Header" valueLabel="Value"
            keyPlaceholder="Authorization" valuePlaceholder="Bearer ..."
            maskValues={true}
            on:change={(e) => editing.headers = e.detail}
          />
        </div>
      {/if}

      {#if testResult}
        <div class="test-result" class:ok={testResult.ok}>
          {#if testResult.ok}
            ✓ Reachable{#if testResult.resolved_command} · resolved to <code>{testResult.resolved_command}</code>{/if}{#if testResult.status_code} · HTTP {testResult.status_code}{/if}
          {:else}
            ✗ {testResult.error}
          {/if}
        </div>
      {/if}

      <div class="modal-row">
        <button class="btn-secondary" on:click={closeModal} disabled={saving}>Cancel</button>
        <button class="btn-secondary" on:click={testConnection} disabled={testing || saving}>
          {testing ? 'Testing…' : 'Test connection'}
        </button>
        <button class="btn-primary" on:click={save} disabled={saving || !editing.id.trim()}>
          {saving ? 'Saving…' : 'Save'}
        </button>
      </div>
    </div>
  </div>
{/if}

<style>
  .page        { padding: 1.5rem; display: flex; flex-direction: column; gap: 1rem; }
  .page-header { display: flex; align-items: center; justify-content: space-between; }
  .page-header h1 { font-size: 1.2rem; font-weight: 600; }
  .header-actions { display: flex; gap: .5rem; }

  .banner { padding: .7rem 1rem; border-radius: 8px; font-size: .85rem; }
  .err    { background: rgba(240,96,96,.1); border: 1px solid rgba(240,96,96,.3); color: #f06060; }
  .ok     { background: rgba(96,240,160,.08); border: 1px solid rgba(96,240,160,.3); color: #60f0a0; }
  .warn   { background: rgba(240,196,96,.08); border: 1px solid rgba(240,196,96,.3); color: #f0c460; }
  .banner code { background: rgba(0,0,0,.25); padding: .05rem .3rem; border-radius: 4px; }

  .empty  { color: #6b7294; padding: 3rem; text-align: center; }
  .empty-card {
    background: #141626; border: 1px solid #1a1e36; border-radius: 10px;
    padding: 3rem 2rem; text-align: center; display: flex; flex-direction: column;
    align-items: center; gap: .75rem; color: #6b7294;
  }
  .empty-icon { font-size: 2.5rem; }
  .hint { font-size: .82rem; max-width: 540px; }

  .server-list { display: flex; flex-direction: column; gap: .65rem; }
  .srv { background: #141626; border: 1px solid #1a1e36; border-radius: 10px; overflow: hidden; }
  .srv.ok { border-color: rgba(76,175,130,.3); }

  .srv-head { display: flex; align-items: center; }
  .srv-expand {
    flex: 1; background: none; color: #e8eaf6; text-align: left;
    display: grid; grid-template-columns: 28px 1fr 110px 200px 24px; align-items: center; gap: .8rem;
    padding: .85rem 1rem; border-radius: 0; cursor: pointer; border: none;
  }
  .srv-expand:hover { background: rgba(255,255,255,.02); }
  .srv-actions { display: flex; gap: .35rem; padding-right: 1rem; }
  .tiny { padding: .25rem .55rem !important; font-size: .72rem !important; }

  .srv-icon       { font-size: 1.1rem; }
  .srv-identity   { display: flex; flex-direction: column; min-width: 0; }
  .srv-name       { font-weight: 600; font-size: .9rem; font-family: monospace; color: #8b85ff; overflow: hidden; text-overflow: ellipsis; }
  .srv-tx         { font-size: .68rem; color: #555a7a; text-transform: uppercase; letter-spacing: .04em; }
  .srv-tools-count{ font-size: .75rem; color: #7b82a8; text-align: right; }
  .srv-badge      { font-size: .72rem; font-weight: 600; color: #4caf82; text-align: right; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .srv-badge.bad  { color: #f06060; font-weight: 500; }
  .srv-chevron    { color: #555a7a; font-size: .9rem; text-align: center; }

  .srv-body { padding: .25rem 1rem 1rem; border-top: 1px solid #1a1e36; }
  .srv-error  { font-size: .8rem; color: #f06060; padding: .5rem .7rem; background: rgba(240,96,96,.08); border-radius: 6px; word-break: break-word; }
  .srv-empty  { font-size: .8rem; color: #6b7294; padding: .8rem 0; font-style: italic; }

  .tbl { width: 100%; border-collapse: collapse; font-size: .8rem; margin-top: .5rem; }
  .tbl th { padding: .5rem .8rem; text-align: left; color: #555a7a; font-weight: 500; font-size: .7rem; text-transform: uppercase; letter-spacing: .04em; border-bottom: 1px solid #1a1e36; }
  .tbl td { padding: .55rem .8rem; border-bottom: 1px solid #0e1020; vertical-align: top; }
  .td-name { font-family: monospace; color: #c8cadf; font-weight: 500; white-space: nowrap; }
  .td-mono { font-family: monospace; font-size: .75rem; color: #8b85ff; white-space: nowrap; }
  .td-desc { color: #7b82a8; }

  .info-card {
    background: #141626; border: 1px solid #1a1e36; border-radius: 10px;
    padding: 1.1rem 1.25rem; display: flex; flex-direction: column; gap: .6rem;
  }
  .info-card h3 { font-size: .875rem; font-weight: 600; }
  .info-card p  { font-size: .82rem; color: #7b82a8; line-height: 1.6; }
  .info-card a  { color: #8b85ff; }
  .info-card code { background: #1c1f35; padding: .1rem .35rem; border-radius: 4px; font-size: .78rem; color: #8b85ff; }

  /* Modal */
  .modal-bg {
    position: fixed; inset: 0; background: rgba(5,7,18,.6);
    display: flex; align-items: center; justify-content: center; z-index: 100;
  }
  .modal {
    background: #141626; border: 1px solid #2a2f4a; border-radius: 12px;
    padding: 1.5rem; width: 560px; max-width: 92vw; max-height: 90vh; overflow-y: auto;
    display: flex; flex-direction: column; gap: .75rem;
  }
  .modal.wide { width: 680px; }
  .modal h2 { font-size: 1.05rem; font-weight: 600; margin-bottom: .25rem; }
  .modal-row { display: flex; justify-content: flex-end; gap: .5rem; margin-top: .5rem; }

  .row-2 { display: grid; grid-template-columns: 1fr 1fr; gap: .75rem; }
  .field { display: flex; flex-direction: column; gap: .3rem; }
  .field label { font-size: .72rem; color: #6b7294; text-transform: uppercase; letter-spacing: .06em; font-weight: 600; }
  .field input, .field select {
    background: #0e1020; border: 1px solid #2a2f4a; border-radius: 6px;
    color: #e8eaf6; font-size: .85rem; padding: .45rem .65rem; font-family: monospace;
  }
  .req      { color: #f06060; margin-left: .15rem; }
  .optional { color: #555a7a; text-transform: none; font-weight: 400; font-size: .68rem; letter-spacing: 0; margin-left: .25rem; }

  .templates { display: flex; flex-wrap: wrap; align-items: center; gap: .35rem; padding-bottom: .25rem; }
  .templates-label { font-size: .72rem; color: #6b7294; text-transform: uppercase; letter-spacing: .06em; font-weight: 600; margin-right: .25rem; }
  .template-chip {
    background: rgba(108,99,255,.12); color: #8b85ff;
    border: 1px solid rgba(108,99,255,.35); padding: .25rem .6rem;
    border-radius: 999px; font-size: .72rem; font-weight: 600; cursor: pointer;
  }
  .template-chip:hover { background: rgba(108,99,255,.2); }

  .test-result {
    padding: .55rem .75rem; border-radius: 6px; font-size: .8rem;
    background: rgba(240,96,96,.08); border: 1px solid rgba(240,96,96,.3); color: #f06060;
  }
  .test-result.ok { background: rgba(96,240,160,.08); border-color: rgba(96,240,160,.3); color: #60f0a0; }
  .test-result code { background: rgba(0,0,0,.25); padding: .05rem .3rem; border-radius: 4px; }

  .btn-primary, .btn-secondary, .btn-danger {
    padding: .5rem .85rem; border-radius: 6px; font-size: .82rem; cursor: pointer; border: 1px solid transparent;
  }
  .btn-primary   { background: #6c63ff; color: white; border-color: #6c63ff; }
  .btn-primary:disabled { opacity: .5; cursor: not-allowed; }
  .btn-secondary { background: #1a1e36; color: #c8cadf; border-color: #2a2f4a; }
  .btn-danger    { background: transparent; color: #f06060; border-color: rgba(240,96,96,.4); }
</style>
