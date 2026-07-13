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
  let executorBackend = 'process'
  let executorWorkers = 4
  let dockerImage = 'python:3.12-slim'
  let dockerNetwork = 'none'
  let dockerVolumes = ''
  let sshHost = ''
  let sshUser = ''
  let sshPythonBin = 'python3'
  let sshIdentity = ''
  let sshIdentityCredential = ''
  let cloudPreset = ''
  let cloudTarget = ''
  let cloudCLI = ''
  let executorReadiness = null
  let executorError = ''
  let executorLoading = false
  let maxTurns    = 20
  let maxSessions = 100
  let maxAgentCallDepth = 5
  let defaultProvider = ''
  let providerOptions = []   // configured llm.providers names (for the dropdown)
  let providerDefaults = {}   // provider id -> configured default model
  let modelsByProvider = {}
  let modelsLoading = {}
  let modelsError = {}
  let studioProvider = ''    // llm.studio override (Studio compiler)
  let studioModel = ''
  let reasonerProvider = ''  // llm.reasoner override (ReAct/Plan-Execute loop)
  let reasonerModel = ''
  let searchProvider = 'ollama'
  let searchApiKey = ''
  let costRows = []
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

  function seedCostEditor(pricing = {}) {
    costRows = Object.entries(pricing || {})
      .map(([selector, price]) => ({
        selector,
        input: price?.input_per_mtok ?? '',
        output: price?.output_per_mtok ?? '',
      }))
      .sort((a, b) => a.selector.localeCompare(b.selector))
  }

  function addCostRow() {
    costRows = [...costRows, { selector: '', input: '', output: '' }]
  }

  function removeCostRow(idx) {
    costRows = costRows.filter((_, i) => i !== idx)
  }

  function costsPatch() {
    const pricing = {}
    for (const row of costRows) {
      const selector = (row.selector || '').trim()
      if (!selector) continue
      pricing[selector] = {
        input_per_mtok: Number(row.input || 0),
        output_per_mtok: Number(row.output || 0),
      }
    }
    return { pricing }
  }

  async function loadProviderRegistry(configured = []) {
    const ids = new Set(configured)
    const defaults = {}
    try {
      const res = await api.providers.list()
      for (const id of (res.registered || [])) ids.add(id)
      for (const id of (res.known || [])) ids.add(id)
      for (const [id, cfg] of Object.entries(res.providers || {})) {
        ids.add(id)
        if (cfg?.model) defaults[id] = cfg.model
      }
    } catch (_) {
      // Config remains usable with the static config.yaml provider list.
    }
    providerDefaults = defaults
    providerOptions = [...ids].filter(Boolean).sort()
  }

  async function loadModels(providerId) {
    if (!providerId || modelsByProvider[providerId] || modelsLoading[providerId]) return
    modelsLoading = { ...modelsLoading, [providerId]: true }
    try {
      const res = await api.providers.models(providerId)
      modelsByProvider = { ...modelsByProvider, [providerId]: res.models || [] }
      modelsError = { ...modelsError, [providerId]: '' }
    } catch (e) {
      modelsByProvider = { ...modelsByProvider, [providerId]: [] }
      modelsError = { ...modelsError, [providerId]: e.message }
    } finally {
      modelsLoading = { ...modelsLoading, [providerId]: false }
    }
  }

  function effectiveRoleProvider(roleProvider) {
    return roleProvider || defaultProvider
  }

  function modelOptions(providerId, current) {
    const list = modelsByProvider[providerId] || []
    const out = []
    if (current && !out.includes(current)) out.push(current)
    for (const m of list) {
      if (m && !out.includes(m)) out.push(m)
    }
    const def = providerDefaults[providerId]
    if (def && !out.includes(def)) out.unshift(def)
    return out
  }

  function pickProviderDefault(providerId, setter) {
    if (providerId && providerDefaults[providerId]) setter(providerDefaults[providerId])
  }

  async function loadExecutors() {
    executorLoading = true
    executorError = ''
    try {
      executorReadiness = await api.executors()
    } catch (e) {
      executorReadiness = null
      executorError = e.message
    } finally {
      executorLoading = false
    }
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
      executorBackend = config.executor?.backend || 'process'
      executorWorkers = config.executor?.workers || 4
      dockerImage     = config.executor?.docker_image || 'python:3.12-slim'
      dockerNetwork   = config.executor?.docker_network || 'none'
      dockerVolumes   = (config.executor?.docker_volumes || []).join('\n')
      sshHost         = config.executor?.ssh_host || ''
      sshUser         = config.executor?.ssh_user || ''
      sshPythonBin    = config.executor?.ssh_python_bin || 'python3'
      sshIdentity     = config.executor?.ssh_identity || ''
      sshIdentityCredential = config.executor?.ssh_identity_credential || ''
      cloudPreset     = config.executor?.cloud_preset || ''
      cloudTarget     = config.executor?.cloud_target || ''
      cloudCLI        = config.executor?.cloud_cli || ''
      maxTurns        = config.runtime?.default_max_turns       || 20
      maxSessions     = config.runtime?.max_concurrent_sessions || 100
      maxAgentCallDepth = config.runtime?.max_agent_call_depth || 5
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
      await loadProviderRegistry(providerOptions)
      searchProvider  = config.search?.provider || 'ollama'
      searchApiKey    = config.search?.api_key || ''
      seedCostEditor(config.costs?.pricing)
      agentDirs       = (config.agent_dirs || []).join('\n')
      skillDirs       = (config.skill_dirs || []).join('\n')
      seedPluginEditor(config.plugins_config)
      await loadExecutors()
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
          max_agent_call_depth:    Number(maxAgentCallDepth),
        },
        executor: {
          backend: executorBackend,
          workers: Number(executorWorkers || 0),
          docker_image: dockerImage,
          docker_network: dockerNetwork,
          docker_volumes: dockerVolumes.split('\n').map(s => s.trim()).filter(Boolean),
          ssh_host: sshHost,
          ssh_user: sshUser,
          ssh_python_bin: sshPythonBin,
          ssh_identity: sshIdentity,
          ssh_identity_credential: sshIdentityCredential,
          cloud_preset: cloudPreset,
          cloud_target: cloudTarget,
          cloud_cli: cloudCLI,
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
        costs: costsPatch(),
        log: { level: logLevel, format: logFormat, file: logFile },
        agent_dirs: agentDirs.split('\n').map(s => s.trim()).filter(Boolean),
        skill_dirs: skillDirs.split('\n').map(s => s.trim()).filter(Boolean),
      }
      const pcPatch = pluginsConfigPatch()
      if (Object.keys(pcPatch).length > 0) patch.plugins_config = pcPatch
      const res = await api.config.patch(patch)
      config = res.config
      seedPluginEditor(config.plugins_config)
      await loadExecutors()
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

  $: loadModels(effectiveRoleProvider(studioProvider))
  $: loadModels(effectiveRoleProvider(reasonerProvider))

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
              <label for="studio-provider" title="Provider used when Studio turns natural-language requests into workflows. Leave blank to use the default provider.">Studio builder — provider</label>
              <select id="studio-provider" bind:value={studioProvider} disabled={!writable}
                      title="Provider used when Studio turns natural-language requests into workflows. Leave blank to use the default provider."
                      on:change={() => pickProviderDefault(effectiveRoleProvider(studioProvider), (m) => studioModel = m)}>
                <option value="">— use default —</option>
                {#each providerOptions as p}<option value={p}>{p}</option>{/each}
              </select>
            </div>
            <div class="field">
              <label for="studio-model" title="Model Studio uses for prompt refinement, workflow generation, and repair. Stronger models usually produce better workflow graphs.">
                Studio model
                {#if modelsLoading[effectiveRoleProvider(studioProvider)]}
                  <span class="inline-status">loading…</span>
                {:else if modelsError[effectiveRoleProvider(studioProvider)]}
                  <span class="inline-error" title={modelsError[effectiveRoleProvider(studioProvider)]}>models unavailable</span>
                {/if}
              </label>
              <select id="studio-model" bind:value={studioModel} disabled={!writable}
                      title="Model Studio uses for prompt refinement, workflow generation, and repair. Stronger models usually produce better workflow graphs.">
                <option value="">— provider default —</option>
                {#each modelOptions(effectiveRoleProvider(studioProvider), studioModel) as m (m)}
                  <option value={m}>{m}</option>
                {/each}
                <option value="__custom__">Custom model ID…</option>
              </select>
              {#if studioModel === '__custom__'}
                <input bind:value={studioModel} placeholder="Enter exact model id" on:focus={() => studioModel = ''} disabled={!writable} />
              {/if}
            </div>
          </div>

          <div class="field-row">
            <div class="field">
              <label for="reasoner-provider" title="Provider used by ReAct and Plan-Execute control loops. Pick a reliable structured-output model for tool-heavy agents.">Reasoner (ReAct / Plan-Execute) — provider</label>
              <select id="reasoner-provider" bind:value={reasonerProvider} disabled={!writable}
                      title="Provider used by ReAct and Plan-Execute control loops. Pick a reliable structured-output model for tool-heavy agents."
                      on:change={() => pickProviderDefault(effectiveRoleProvider(reasonerProvider), (m) => reasonerModel = m)}>
                <option value="">— use default —</option>
                {#each providerOptions as p}<option value={p}>{p}</option>{/each}
              </select>
            </div>
            <div class="field">
              <label for="reasoner-model" title="Model used for internal thinking, planning, and final reflection. Lower-latency models are fine if they follow JSON reliably.">
                Reasoner model
                {#if modelsLoading[effectiveRoleProvider(reasonerProvider)]}
                  <span class="inline-status">loading…</span>
                {:else if modelsError[effectiveRoleProvider(reasonerProvider)]}
                  <span class="inline-error" title={modelsError[effectiveRoleProvider(reasonerProvider)]}>models unavailable</span>
                {/if}
              </label>
              <select id="reasoner-model" bind:value={reasonerModel} disabled={!writable}
                      title="Model used for internal thinking, planning, and final reflection. Lower-latency models are fine if they follow JSON reliably.">
                <option value="">— provider default —</option>
                {#each modelOptions(effectiveRoleProvider(reasonerProvider), reasonerModel) as m (m)}
                  <option value={m}>{m}</option>
                {/each}
                <option value="__custom__">Custom model ID…</option>
              </select>
              {#if reasonerModel === '__custom__'}
                <input bind:value={reasonerModel} placeholder="Enter exact model id" on:focus={() => reasonerModel = ''} disabled={!writable} />
              {/if}
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
          <h2 class="section-title">Cost estimation</h2>
          <p class="hint">
            Optional USD-per-million-token rates used by Activity, run metrics, and <code>/api/v1/costs</code>.
            Selectors match <code>provider/model</code>, <code>provider/*</code>, then <code>*/model</code>.
            Unknown models still record tokens with <code>cost_usd: 0</code>.
          </p>
          {#if costRows.length === 0}
            <p class="hint">No pricing configured yet. Add one row for exact model pricing or a provider wildcard.</p>
          {/if}
          {#each costRows as row, idx}
            <div class="cost-row">
              <label class="field cost-selector">
                <span title="Pricing selector. Use provider/model for exact pricing, provider/* for a provider default, or */model for a shared model default.">Selector</span>
                <input bind:value={row.selector} placeholder="openai/gpt-4.1-mini or omniroute/*" disabled={!writable} />
              </label>
              <label class="field cost-rate">
                <span title="USD charged per 1 million prompt/input tokens. Leave 0 for local or prepaid providers.">Input $/M</span>
                <input type="number" step="0.0001" min="0" bind:value={row.input} placeholder="0.00" disabled={!writable} />
              </label>
              <label class="field cost-rate">
                <span title="USD charged per 1 million completion/output tokens. Output is often more expensive than input.">Output $/M</span>
                <input type="number" step="0.0001" min="0" bind:value={row.output} placeholder="0.00" disabled={!writable} />
              </label>
              {#if writable}
                <button class="link-danger cost-del" title="Remove pricing row" on:click={() => removeCostRow(idx)}>✕</button>
              {/if}
            </div>
          {/each}
          {#if writable}
            <button class="btn-secondary kv-add" on:click={addCostRow}>+ Add pricing row</button>
          {/if}
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
            <div class="field">
              <label for="max-agent-call-depth" title="Caps recursive peer-agent delegation chains. Raise for deeper coordinator teams; lower to stop accidental loops sooner. Default is 5.">Max agent-call depth</label>
              <input id="max-agent-call-depth" type="number" bind:value={maxAgentCallDepth} min="1" max="50" disabled={!writable} />
            </div>
          </div>
        </div>

        <div class="section">
          <div class="section-heading">
            <h2 class="section-title">Executors</h2>
            <button class="btn-secondary tiny-btn" on:click={loadExecutors} disabled={executorLoading}>
              {executorLoading ? 'Checking…' : 'Re-check'}
            </button>
          </div>
          <p class="hint">
            Choose where Python tools run. Local process is simplest, pool is faster for chat,
            Docker isolates code, SSH/cloud move heavy work off this machine. Restart after changes.
          </p>
          {#if executorError}
            <div class="mini-warn">{executorError}</div>
          {/if}
          {#if executorReadiness}
            <div class="executor-grid">
              {#each executorReadiness.backends || [] as backend (backend.key)}
                <div class:active={backend.active} class={`executor-card ${backend.status}`}>
                  <div class="executor-top">
                    <strong>{backend.label}</strong>
                    <span>{backend.status}</span>
                  </div>
                  <p>{backend.detail}</p>
                  {#if backend.next}<small>{backend.next}</small>{/if}
                  {#if backend.command}<code>{backend.command}</code>{/if}
                </div>
              {/each}
            </div>
          {/if}

          <div class="field-row">
            <div class="field">
              <label for="executor-backend" title="Default backend for Python tools. Agents can override it per tool with execution.backend when needed.">Default backend</label>
              <select id="executor-backend" bind:value={executorBackend} disabled={!writable}>
                <option value="process">process — fresh local process</option>
                <option value="pool">pool — warm local workers</option>
                <option value="docker">docker — isolated container</option>
                <option value="ssh">ssh — remote worker</option>
              </select>
            </div>
            <div class="field">
              <label for="executor-workers" title="Number of warm local Python workers used by pool mode. Higher values improve concurrency but consume memory.">Pool workers</label>
              <input id="executor-workers" type="number" bind:value={executorWorkers} min="1" max="64" disabled={!writable} />
            </div>
          </div>

          <div class="field-row">
            <div class="field">
              <label for="docker-image" title="Container image used by docker execution. Keep it pinned for production repeatability.">Docker image</label>
              <input id="docker-image" bind:value={dockerImage} placeholder="python:3.12-slim" disabled={!writable} />
            </div>
            <div class="field">
              <label for="docker-network" title="Docker network mode. Use none for safer sandboxing, bridge only when tools need outbound network access.">Docker network</label>
              <input id="docker-network" bind:value={dockerNetwork} placeholder="none" disabled={!writable} />
            </div>
          </div>
          <div class="field">
            <label for="docker-volumes" title="Explicit Docker volume allowlist, one mount per line as host:container[:ro]. Empty means no host paths are mounted.">Docker volume allowlist</label>
            <textarea id="docker-volumes" bind:value={dockerVolumes} rows="2" placeholder="/safe/data:/data:ro" disabled={!writable}></textarea>
          </div>

          <div class="field-row">
            <div class="field">
              <label for="ssh-host" title="Remote host for SSH execution. Use host or user@host.">SSH host</label>
              <input id="ssh-host" bind:value={sshHost} placeholder="worker.example.com" disabled={!writable} />
            </div>
            <div class="field">
              <label for="ssh-user" title="Optional SSH username when it is not included in SSH host.">SSH user</label>
              <input id="ssh-user" bind:value={sshUser} placeholder="ubuntu" disabled={!writable} />
            </div>
          </div>
          <div class="field-row">
            <div class="field">
              <label for="ssh-python-bin" title="Python executable on the remote host.">SSH Python binary</label>
              <input id="ssh-python-bin" bind:value={sshPythonBin} placeholder="python3" disabled={!writable} />
            </div>
            <div class="field">
              <label for="ssh-identity-credential" title="Name of a vault secret containing the SSH private key. Prefer this over raw key paths.">SSH identity credential</label>
              <input id="ssh-identity-credential" bind:value={sshIdentityCredential} placeholder="remote-worker-key" disabled={!writable} />
            </div>
          </div>

          <div class="field-row">
            <div class="field">
              <label for="cloud-preset" title="Optional cloud execution preset. The provider CLI must already be installed and authenticated on this host.">Cloud preset</label>
              <select id="cloud-preset" bind:value={cloudPreset} disabled={!writable}>
                <option value="">— none —</option>
                <option value="modal">modal</option>
                <option value="runpod">runpod</option>
                <option value="daytona">daytona</option>
              </select>
            </div>
            <div class="field">
              <label for="cloud-target" title="Provider-specific cloud target such as a workspace, app, image, or pod id.">Cloud target</label>
              <input id="cloud-target" bind:value={cloudTarget} placeholder="workspace/app/pod id" disabled={!writable} />
            </div>
          </div>
          <div class="field">
            <label for="cloud-cli" title="Optional CLI binary override for the selected cloud preset. Leave blank to use the default CLI name.">Cloud CLI override</label>
            <input id="cloud-cli" bind:value={cloudCLI} placeholder="modal, runpodctl, or daytona" disabled={!writable} />
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
  .section-heading { display: flex; align-items: center; justify-content: space-between; gap: .75rem; }
  .section-title { font-size: .78rem; font-weight: 600; text-transform: uppercase; letter-spacing: .06em; color: #555a7a; margin-bottom: .1rem; }

  .field { display: flex; flex-direction: column; gap: .35rem; }
  .field label { font-size: .78rem; color: #7b82a8; font-weight: 500; }
  .field-row { display: grid; grid-template-columns: 1fr 1fr; gap: .75rem; }
  .inline-status { margin-left: .35rem; color: #8b85ff; font-size: .72rem; font-weight: 500; }
  .inline-error { margin-left: .35rem; color: #f0a060; font-size: .72rem; font-weight: 500; cursor: help; }
  .tiny-btn { font-size: .72rem; padding: .25rem .55rem; }
  .mini-warn { padding: .5rem .65rem; border-radius: 8px; background: rgba(240,160,96,.1); border: 1px solid rgba(240,160,96,.25); color: #f0a060; font-size: .75rem; }
  .executor-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(210px, 1fr)); gap: .55rem; }
  .executor-card { padding: .65rem; border: 1px solid #202542; border-radius: 8px; background: #10121f; display: flex; flex-direction: column; gap: .4rem; min-height: 132px; }
  .executor-card.active { border-color: #7567ff; box-shadow: 0 0 0 1px rgba(117,103,255,.25); }
  .executor-card.ok { border-color: rgba(76,175,130,.32); }
  .executor-card.warn { border-color: rgba(240,160,96,.35); }
  .executor-card.fail { border-color: rgba(240,96,96,.4); }
  .executor-top { display: flex; align-items: center; justify-content: space-between; gap: .5rem; }
  .executor-top strong { font-size: .78rem; color: #c5c8e8; }
  .executor-top span { font-size: .65rem; text-transform: uppercase; letter-spacing: .05em; color: #8b85ff; }
  .executor-card p { margin: 0; color: #9aa1c6; font-size: .74rem; line-height: 1.45; }
  .executor-card small { color: #6b7294; font-size: .7rem; line-height: 1.35; }
  .executor-card code { margin-top: auto; background: #0b0d19; border: 1px solid #1a1e36; padding: .35rem; border-radius: 6px; color: #8b85ff; font-size: .68rem; overflow-wrap: anywhere; }

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
  .cost-row {
    display: grid;
    grid-template-columns: minmax(180px, 1fr) 110px 110px auto;
    gap: .5rem;
    align-items: end;
    padding: .55rem .65rem;
    background: #10121f;
    border: 1px solid #1a1e36;
    border-radius: 8px;
  }
  .cost-selector input { font-family: monospace; font-size: .78rem; }
  .cost-rate input { text-align: right; }
  .cost-del { align-self: end; margin-bottom: .15rem; }
  @media (max-width: 640px) {
    .cost-row { grid-template-columns: 1fr; }
    .cost-rate input { text-align: left; }
  }
</style>
