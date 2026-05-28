<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'

  let config   = null
  let loading  = true
  let saving   = false
  let error    = ''
  let saved    = false
  let writable = false

  // Editable fields
  let logLevel  = 'info'
  let logFormat = 'console'
  let logFile   = ''
  let pythonBin = 'python3'
  let toolTimeout = '30s'
  let maxTurns    = 20
  let maxSessions = 100
  let defaultProvider = ''
  let agentDirs = ''
  let skillDirs = ''

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
      agentDirs       = (config.agent_dirs || []).join('\n')
      skillDirs       = (config.skill_dirs || []).join('\n')
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
        llm: { default_provider: defaultProvider },
        log: { level: logLevel, format: logFormat, file: logFile },
        agent_dirs: agentDirs.split('\n').map(s => s.trim()).filter(Boolean),
        skill_dirs: skillDirs.split('\n').map(s => s.trim()).filter(Boolean),
      }
      const res = await api.config.patch(patch)
      config = res.config
      saved  = true
      setTimeout(() => { saved = false }, 3000)
    } catch (e) {
      error = e.message
    } finally {
      saving = false
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
    <div class="banner ok">✓ Config saved. Restart the gateway for changes to take full effect.</div>
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
            <input id="default-provider" bind:value={defaultProvider} placeholder="ollama" disabled={!writable} />
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
  .err  { background: rgba(240,96,96,.1); border: 1px solid rgba(240,96,96,.3); color: #f06060; }
  .ok   { background: rgba(76,175,130,.1); border: 1px solid rgba(76,175,130,.3); color: #4caf82; }
  .warn { background: rgba(240,160,96,.1); border: 1px solid rgba(240,160,96,.3); color: #f0a060; }
  .warn code { background: #1c1f35; padding: .1rem .35rem; border-radius: 4px; font-size: .78rem; }
  .empty { padding: 3rem; text-align: center; color: #6b7294; }

  .config-layout { display: grid; grid-template-columns: 1fr 380px; gap: 1.25rem; align-items: start; }

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
</style>
