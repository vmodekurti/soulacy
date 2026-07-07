<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { rowsFromSettings, settingsPatchFromRows } from '../lib/pluginsettings.js'

  let config   = null
  let loading  = true
  let saving   = false
  let error    = ''
  let saved    = false
  let writable = false
  let restarting = false
  let downloadingSupport = false

  // Editable fields
  let logLevel  = 'info'
  let logFormat = 'console'
  let logFile   = ''
  let pythonBin = 'python3'
  let toolTimeout = '30s'
  let maxTurns    = 20
  let maxSessions = 100
  let defaultProvider = ''
  let providerOptions = []   // configured llm.providers names (for the dropdown)
  let studioProvider = ''    // llm.studio override (Studio compiler)
  let studioModel = ''
  let reasonerProvider = ''  // llm.reasoner override (ReAct/Plan-Execute loop)
  let reasonerModel = ''
  let searchProvider = 'ollama'
  let searchApiKey = ''
  let agentDirs = ''
  let skillDirs = ''

  // Plugin settings editor (Story 18): pid → editable rows; originals kept
  // for type-safe round-trips. Secrets arrive redacted as '***' and the
  // server skips those placeholders on PATCH, so saving never clobbers
  // real values on disk.
  let pluginRows = {}
  let pluginOriginals = {}
  let newPluginId = ''

  function seedPluginEditor(pc) {
    pluginOriginals = pc || {}
    pluginRows = {}
    for (const [pid, settings] of Object.entries(pluginOriginals)) {
      pluginRows[pid] = rowsFromSettings(settings)
    }
  }

  function addRow(pid) {
    pluginRows[pid] = [...pluginRows[pid], { key: '', value: '' }]
  }

  function removeRow(pid, idx) {
    pluginRows[pid] = pluginRows[pid].filter((_, i) => i !== idx)
  }

  function addPlugin() {
    const pid = newPluginId.trim()
    if (!pid || pluginRows[pid]) return
    pluginOriginals = { ...pluginOriginals, [pid]: {} }
    pluginRows = { ...pluginRows, [pid]: [{ key: '', value: '' }] }
    newPluginId = ''
  }

  function pluginsConfigPatch() {
    const patch = {}
    for (const [pid, rows] of Object.entries(pluginRows)) {
      patch[pid] = settingsPatchFromRows(pluginOriginals[pid], rows)
    }
    return patch
  }

  async function load() {
    loading = true
    error   = ''
    try {
      config = await api.config.get()
      writable = config._meta?.writable ?? false
      // Populate editable fields
      logLevel        = config.log?.level      || 'info'
      logFormat       = config.log?.format     || 'console'
      logFile         = config.log?.file       || ''
      pythonBin       = config.runtime?.python_bin    || 'python3'
      toolTimeout     = config.runtime?.tool_timeout  || '30s'
      maxTurns        = config.runtime?.default_max_turns       || 20
      maxSessions     = config.runtime?.max_concurrent_sessions || 100
      defaultProvider = config.llm?.default_provider || ''
      providerOptions = Object.keys(config.llm?.providers || {})
      // Make sure the current value is selectable even if it isn't a
      // configured provider block (e.g. set manually in config.yaml).
      if (defaultProvider && !providerOptions.includes(defaultProvider)) {
        providerOptions = [defaultProvider, ...providerOptions]
      }
      studioProvider   = config.llm?.studio?.provider || ''
      studioModel      = config.llm?.studio?.model || ''
      reasonerProvider = config.llm?.reasoner?.provider || ''
      reasonerModel    = config.llm?.reasoner?.model || ''
      searchProvider  = config.search?.provider || 'ollama'
      searchApiKey    = config.search?.api_key || ''
      agentDirs       = (config.agent_dirs || []).join('\n')
      skillDirs       = (config.skill_dirs || []).join('\n')
      seedPluginEditor(config.plugins_config)
    } catch (e) {
      error = e.message
    } finally {
      loading = false
    }
  }

  async function save() {
    saving = true
    error  = ''
    saved  = false
    try {
      const patch = {
        runtime: {
          python_bin:              pythonBin,
          tool_timeout:            toolTimeout,
          default_max_turns:       Number(maxTurns),
          max_concurrent_sessions: Number(maxSessions),
        },
        llm: {
          default_provider: defaultProvider,
          // Empty provider/model = fall back to the default (server-side).
          studio:   { provider: studioProvider,   model: studioModel },
          reasoner: { provider: reasonerProvider, model: reasonerModel },
        },
        // api_key of '***' (redacted placeholder) is skipped server-side, so
        // saving without retyping the key never clobbers the real one on disk.
        search: { provider: searchProvider, api_key: searchApiKey },
        log: { level: logLevel, format: logFormat, file: logFile },
        agent_dirs: agentDirs.split('\n').map(s => s.trim()).filter(Boolean),
        skill_dirs: skillDirs.split('\n').map(s => s.trim()).filter(Boolean),
      }
      const pcPatch = pluginsConfigPatch()
      if (Object.keys(pcPatch).length > 0) patch.plugins_config = pcPatch
      const res = await api.config.patch(patch)
      config = res.config
      seedPluginEditor(config.plugins_config)
      saved  = true
      setTimeout(() => { saved = false }, 3000)
    } catch (e) {
      error = e.message
    } finally {
      saving = false
    }
  }

  async function restartGateway() {
    restarting = true
    error = ''
    try {
      await api.admin.restart()
      saved = false
    } catch (e) {
      error = e.message
    } finally {
      setTimeout(() => { restarting = false }, 5000)
    }
  }

  async function downloadSupportBundle() {
    downloadingSupport = true
    error = ''
    try {
      const { blob, filename } = await api.support.bundle()
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = filename || `soulacy-support-${new Date().toISOString().slice(0, 19).replaceAll(':', '')}.zip`
      document.body.appendChild(a)
      a.click()
      a.remove()
      URL.revokeObjectURL(url)
    } catch (e) {
      error = e.message
    } finally {
      downloadingSupport = false
    }
  }

  onMount(load)
</script>

<div class="page">
  <div class="page-header">
    <h1>Configuration</h1>
    <div class="header-actions">
      <button class="btn-secondary" on:click={load} disabled={loading}>↺ Refresh</button>
      {#if writable}
        <button class="btn-primary" on:click={save} disabled={saving || loading}>
          {saving ? 'Saving…' : 'Save changes'}
        </button>
      {/if}
    </div>
  </div>

  {#if error}
    <div class="banner err">{error}</div>
  {/if}
  {#if saved}
    <div class="banner ok restart-banner">
      <span>✓ Config saved. Restart the gateway for changes to take full effect.</span>
      <button class="btn-secondary" on:click={restartGateway} disabled={restarting}>
        {restarting ? 'Restarting…' : 'Restart Gateway'}
      </button>
    </div>
  {/if}
  {#if !writable && config}
    <div class="banner warn">
      ⚠ Config is read-only in this session (config file path unknown). Set <code>SOULACY_CONFIG_PATH</code> to enable writes.
    </div>
  {/if}

  {#if loading}
    <div class="empty">Loading configuration…</div>
  {:else if config}
    <div class="config-layout">
      <!-- Left: editable form -->
      <div class="form-col">

        <div class="section">
          <h2 class="section-title">LLM</h2>
          <div class="field">
            <label for="default-provider">Default provider</label>
            {#if providerOptions.length}
              <select id="default-provider" bind:value={defaultProvider} disabled={!writable}>
                {#each providerOptions as p}
                  <option value={p}>{p}</option>
                {/each}
              </select>
            {:else}
              <input id="default-provider" bind:value={defaultProvider} placeholder="ollama" disabled={!writable} />
            {/if}
          </div>

          <p class="hint">
            Task-specific model roles. Run agents on a cheap model but build &amp; reason with a
            stronger one. Leave a role blank to fall back to the default provider. A restart is
            required to take effect.
          </p>

          <div class="field-row">
            <div class="field">
              <label for="studio-provider">Studio builder — provider</label>
              <select id="studio-provider" bind:value={studioProvider} disabled={!writable}>
                <option value="">— use default —</option>
                {#each providerOptions as p}<option value={p}>{p}</option>{/each}
              </select>
            </div>
            <div class="field">
              <label for="studio-model">Studio model</label>
              <input id="studio-model" bind:value={studioModel} placeholder="(provider default)" disabled={!writable} />
            </div>
          </div>

          <div class="field-row">
            <div class="field">
              <label for="reasoner-provider">Reasoner (ReAct / Plan-Execute) — provider</label>
              <select id="reasoner-provider" bind:value={reasonerProvider} disabled={!writable}>
                <option value="">— use default —</option>
                {#each providerOptions as p}<option value={p}>{p}</option>{/each}
              </select>
            </div>
            <div class="field">
              <label for="reasoner-model">Reasoner model</label>
              <input id="reasoner-model" bind:value={reasonerModel} placeholder="(provider default)" disabled={!writable} />
            </div>
          </div>
        </div>

        <div class="section">
          <h2 class="section-title">Web search</h2>
          <p class="hint">Backs the built-in <code>web_search</code> tool. A restart is required to take effect.</p>
          <div class="field-row">
            <div class="field">
              <label for="search-provider">Provider</label>
              <select id="search-provider" bind:value={searchProvider} disabled={!writable}>
                <option value="ollama">Ollama (hosted web search)</option>
                <option value="tavily">Tavily</option>
                <option value="serper">Serper (Google)</option>
              </select>
            </div>
            <div class="field">
              <label for="search-api-key">API key</label>
              <input id="search-api-key" type="password" bind:value={searchApiKey}
                     placeholder="leave as ••• to keep current; or set env var" disabled={!writable} />
            </div>
          </div>
        </div>

        <div class="section">
          <h2 class="section-title">Runtime</h2>
          <div class="field-row">
            <div class="field">
              <label for="python-bin">Python binary</label>
              <input id="python-bin" bind:value={pythonBin} placeholder="python3" disabled={!writable} />
            </div>
            <div class="field">
              <label for="tool-timeout">Tool timeout</label>
              <input id="tool-timeout" bind:value={toolTimeout} placeholder="30s" disabled={!writable} />
            </div>
          </div>
          <div class="field-row">
            <div class="field">
              <label for="max-turns">Default max turns</label>
              <input id="max-turns" type="number" bind:value={maxTurns} min="1" max="100" disabled={!writable} />
            </div>
            <div class="field">
              <label for="max-sessions">Max concurrent sessions</label>
              <input id="max-sessions" type="number" bind:value={maxSessions} min="1" max="1000" disabled={!writable} />
            </div>
          </div>
        </div>

        <div class="section">
          <h2 class="section-title">Logging</h2>
          <div class="field-row">
            <div class="field">
              <label for="log-level">Level</label>
              <select id="log-level" bind:value={logLevel} disabled={!writable}>
                <option value="debug">debug</option>
                <option value="info">info</option>
                <option value="warn">warn</option>
                <option value="error">error</option>
              </select>
            </div>
            <div class="field">
              <label for="log-format">Format</label>
              <select id="log-format" bind:value={logFormat} disabled={!writable}>
                <option value="console">console</option>
                <option value="json">json</option>
              </select>
            </div>
          </div>
          <div class="field">
            <label for="log-file">Log file path (empty = stdout only)</label>
            <input id="log-file" bind:value={logFile} placeholder="/var/log/soulacy.log" disabled={!writable} />
          </div>
        </div>

        <div class="section">
          <h2 class="section-title">Webhooks</h2>
          {#if (config?.hooks || []).length === 0}
            <p class="hint">
              No outbound webhooks configured. Add a <code>hooks:</code> section
              to <code>config.yaml</code> to deliver signed events
              (run.failed, tool.call, …) to your own endpoints — see
              <code>docs/EVENTS.md</code> for the payload schema and signature
              verification.
            </p>
          {:else}
            {#each config.hooks as h}
              <div class="hook-row">
                <code class="hook-url">{h.url}</code>
                <span class="hook-meta">
                  on: {(h.on || []).join(', ') || '—'}
                  {#if (h.agents || []).length}· agents: {h.agents.join(', ')}{/if}
                  {#if h.secret_env}· signed (secret from ${h.secret_env}){:else}· ⚠ unsigned{/if}
                </span>
              </div>
            {/each}
            <p class="hint">Edit webhooks in <code>config.yaml</code> (restart to apply).</p>
          {/if}
        </div>

        <div class="section">
          <h2 class="section-title">Plugin settings</h2>
          {#if Object.keys(pluginRows).length === 0}
            <p class="hint">
              No plugin settings configured. Add a plugin ID below (or a
              <code>plugins_config:</code> block in <code>config.yaml</code>) —
              each plugin documents its own keys. Secret-looking values are
              redacted here and never reach the browser.
            </p>
          {:else}
            {#each Object.entries(pluginRows) as [pid, rows] (pid)}
              <div class="plugin-settings">
                <code class="hook-url">{pid}</code>
                {#each rows as row, idx}
                  <div class="kv-row">
                    <input type="text" class="kv-key" placeholder="key"
                           bind:value={row.key} disabled={!writable} />
                    <input type="text" class="kv-val" placeholder="value (JSON for objects/numbers)"
                           bind:value={row.value} disabled={!writable} />
                    {#if writable}
                      <button class="link-danger kv-del" title="Remove key"
                              on:click={() => removeRow(pid, idx)}>✕</button>
                    {/if}
                  </div>
                {/each}
                {#if writable}
                  <button class="btn-secondary kv-add" on:click={() => addRow(pid)}>+ Add key</button>
                {/if}
              </div>
            {/each}
            <p class="hint">
              Secrets show as <code>***</code> — leaving them unchanged keeps
              the real value on disk. Removing a row deletes that key on save.
            </p>
          {/if}
          {#if writable}
            <div class="kv-row">
              <input type="text" class="kv-key" placeholder="plugin id"
                     bind:value={newPluginId} />
              <button class="btn-secondary" on:click={addPlugin}
                      disabled={!newPluginId.trim()}>+ Add plugin section</button>
            </div>
          {/if}
        </div>

        <div class="section">
          <h2 class="section-title">Support</h2>
          <p class="hint">
            Download a redacted diagnostic bundle with doctor output, masked configuration,
            masked agent manifests, and recent log tails.
          </p>
          <button class="btn-secondary support-download" on:click={downloadSupportBundle} disabled={downloadingSupport}>
            {downloadingSupport ? 'Preparing bundle...' : 'Download support bundle'}
          </button>
        </div>

        <div class="section">
          <h2 class="section-title">Directories</h2>
          <div class="field">
            <label for="agent-dirs">Agent directories (one per line)</label>
            <textarea id="agent-dirs" bind:value={agentDirs} rows="3"
                      placeholder="~/.soulacy/agents" disabled={!writable}></textarea>
          </div>
          <div class="field">
            <label for="skill-dirs">Extra skill directories (one per line)</label>
            <textarea id="skill-dirs" bind:value={skillDirs} rows="2"
                      placeholder="~/.soulacy/skills" disabled={!writable}></textarea>
          </div>
        </div>
      </div>

      <!-- Right: read-only full JSON view -->
      <div class="json-col">
        <div class="json-header">
          <span class="json-label">Current config (read-only view)</span>
          {#if config._meta?.config_path}
            <code class="json-path">{config._meta.config_path}</code>
          {/if}
        </div>
        <pre class="json-view">{JSON.stringify(config, null, 2)}</pre>
      </div>
    </div>
  {/if}
</div>

<style>
  .page        { padding: 1.5rem; display: flex; flex-direction: column; gap: 1.25rem; }
  .page-header { display: flex; align-items: center; justify-content: space-between; }
  .page-header h1 { font-size: 1.2rem; font-weight: 600; }
  .header-actions { display: flex; gap: .5rem; }
  .banner { padding: .7rem 1rem; border-radius: 8px; font-size: .85rem; }
  .restart-banner { display: flex; align-items: center; justify-content: space-between; gap: .75rem; flex-wrap: wrap; }
  .err  { background: rgba(240,96,96,.1); border: 1px solid rgba(240,96,96,.3); color: #f06060; }
  .ok   { background: rgba(76,175,130,.1); border: 1px solid rgba(76,175,130,.3); color: #4caf82; }
  .warn { background: rgba(240,160,96,.1); border: 1px solid rgba(240,160,96,.3); color: #f0a060; }
  .warn code { background: #1c1f35; padding: .1rem .35rem; border-radius: 4px; font-size: .78rem; }
  .empty { padding: 3rem; text-align: center; color: #6b7294; }

  .config-layout { display: grid; grid-template-columns: 1fr 380px; gap: 1.25rem; align-items: start; }

  @media (max-width: 900px) {
    .config-layout { grid-template-columns: 1fr; }
    .json-col { position: static; }
  }
  @media (max-width: 640px) {
    .field-row { grid-template-columns: 1fr; }
  }

  /* Form column */
  .form-col { display: flex; flex-direction: column; gap: 1rem; }

  .section { background: #141626; border: 1px solid #1a1e36; border-radius: 10px; padding: 1.1rem 1.25rem; display: flex; flex-direction: column; gap: .85rem; }
  .section-title { font-size: .78rem; font-weight: 600; text-transform: uppercase; letter-spacing: .06em; color: #555a7a; margin-bottom: .1rem; }

  .field { display: flex; flex-direction: column; gap: .35rem; }
  .field label { font-size: .78rem; color: #7b82a8; font-weight: 500; }
  .field-row { display: grid; grid-template-columns: 1fr 1fr; gap: .75rem; }

  /* JSON column */
  .json-col {
    background: #0e1020; border: 1px solid #1a1e36; border-radius: 10px;
    overflow: hidden; position: sticky; top: 1.5rem;
  }
  .json-header {
    padding: .65rem 1rem; border-bottom: 1px solid #1a1e36;
    display: flex; flex-direction: column; gap: .2rem;
  }
  .json-label { font-size: .72rem; color: #555a7a; font-weight: 600; text-transform: uppercase; letter-spacing: .06em; }
  .json-path  { font-size: .72rem; color: #7b82a8; word-break: break-all; }
  .json-view  {
    font-family: monospace; font-size: .75rem; line-height: 1.6;
    color: #7b82a8; padding: 1rem; overflow-x: auto;
    white-space: pre; max-height: 600px; overflow-y: auto;
  }

  .hint { font-size: .78rem; color: #6b7294; line-height: 1.6; }
  .hint code { background: #1c1f35; padding: .08rem .35rem; border-radius: 4px; font-size: .72rem; }
  .hook-row { display: flex; flex-direction: column; gap: .15rem; padding: .5rem .65rem;
              background: #10121f; border: 1px solid #1a1e36; border-radius: 8px; }
  .hook-url { font-size: .78rem; color: #8b85ff; overflow-wrap: anywhere; }
  .hook-meta { font-size: .7rem; font-family: monospace; color: #6b7294; }
  .plugin-settings { display: flex; flex-direction: column; gap: .35rem; padding: .5rem .65rem;
                     background: #10121f; border: 1px solid #1a1e36; border-radius: 8px;
                     margin-bottom: .5rem; }
  .support-download { align-self: flex-start; }
  .kv-row { display: flex; gap: .4rem; align-items: center; }
  .kv-key { flex: 0 0 32%; }
  .kv-val { flex: 1; font-family: monospace; font-size: .78rem; }
  .kv-del { flex: 0 0 auto; }
  .kv-add { align-self: flex-start; font-size: .75rem; padding: .25rem .6rem; }
</style>
