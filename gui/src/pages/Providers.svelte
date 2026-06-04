<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'

  let providers       = {}
  let defaultProvider = ''
  let known           = []   // known provider ids the GUI offers
  let registered      = []   // currently-registered provider ids in the live router
  let models          = {}   // provider → model list
  let chosen          = {}   // provider → selected model (for Save)
  let modelsLoading   = {}   // provider → bool
  let savingModel     = {}   // provider → bool
  let loading         = true
  let testResults     = {}   // provider → {ok, msg}
  let error           = ''
  let notice          = ''
  let restartNeeded   = false
  let restarting      = false

  // Add/edit-credentials modal state
  let showAdd  = false
  let addId    = 'openai'
  let addKey   = ''
  let addBase  = ''
  let addModel = ''
  let saving   = false

  const KNOWN_BASES = {
    openai:      'https://api.openai.com/v1',
    anthropic:   'https://api.anthropic.com',
    google:      'https://generativelanguage.googleapis.com',
    ollama:      'http://localhost:11434',
    groq:        'https://api.groq.com/openai/v1',
    mistral:     'https://api.mistral.ai/v1',
    openrouter:  'https://openrouter.ai/api/v1',
  }
  const KNOWN_MODELS = {
    openai:      'gpt-4o-mini',
    anthropic:   'claude-3-5-sonnet-latest',
    google:      'gemini-2.5-flash',
    ollama:      'llama3',
    groq:        'llama-3.3-70b-versatile',
    mistral:     'mistral-small-latest',
    openrouter:  'meta-llama/llama-3.3-70b-instruct:free',
  }

  async function load() {
    loading = true
    error   = ''
    try {
      const res       = await api.providers.list()
      providers       = res.providers || {}
      defaultProvider = res.default_provider || ''
      known           = res.known || []
      registered      = res.registered || []
    } catch (e) {
      error = e.message
    } finally {
      loading = false
    }
  }

  function openAdd(id) {
    addId    = id || 'openai'
    addKey   = ''
    addBase  = providers[addId]?.base_url || KNOWN_BASES[addId] || ''
    addModel = providers[addId]?.model    || KNOWN_MODELS[addId] || ''
    showAdd  = true
  }
  async function saveCreds() {
    if (!addId) return
    saving = true; error = ''; notice = ''
    try {
      const res = await api.providers.setCredentials(addId, {
        base_url: addBase || undefined,
        api_key:  addKey  || undefined,
        model:    addModel || undefined,
      })
      notice = res.message || 'Saved.'
      restartNeeded = true
      showAdd = false
      await load()
    } catch (e) {
      error = e.message
    } finally {
      saving = false
    }
  }
  $: missingKnown = (known || []).filter(id => !(id in providers))

  async function listModels(providerId) {
    modelsLoading = { ...modelsLoading, [providerId]: true }
    error = ''
    try {
      const res = await api.providers.models(providerId)
      models = { ...models, [providerId]: res.models || [] }
      // Default the selection to the currently-configured model, else the first one.
      const current = res.selected || providers[providerId]?.model || ''
      chosen = { ...chosen, [providerId]: current || (res.models && res.models[0]) || '' }
    } catch (e) {
      models = { ...models, [providerId]: [] }
      error = `${providerId}: ${e.message}`
    } finally {
      modelsLoading = { ...modelsLoading, [providerId]: false }
    }
  }

  async function saveModel(providerId) {
    const model = chosen[providerId]
    if (!model) return
    savingModel = { ...savingModel, [providerId]: true }
    error = ''; notice = ''
    try {
      const res = await api.providers.setModel(providerId, model)
      // reflect immediately
      providers = { ...providers, [providerId]: { ...providers[providerId], model } }
      notice = res.message || `Saved ${model} as the default model for ${providerId}.`
      restartNeeded = true
    } catch (e) {
      error = e.message
    } finally {
      savingModel = { ...savingModel, [providerId]: false }
    }
  }

  async function test(providerId) {
    testResults = { ...testResults, [providerId]: { loading: true } }
    try {
      await api.providers.models(providerId)
      testResults = { ...testResults, [providerId]: { ok: true, msg: 'Reachable ✓' } }
    } catch (e) {
      testResults = { ...testResults, [providerId]: { ok: false, msg: e.message } }
    }
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

  onMount(load)

  const PROVIDER_ICONS = {
    ollama: '🦙', openai: '🤖', anthropic: '🔮', openrouter: '🌐',
    groq: '⚡', mistral: '💨', google: '🔵',
  }
  function providerIcon(id = '') { return PROVIDER_ICONS[id.toLowerCase()] || '⚙️' }

  $: providerList = Object.entries(providers)
</script>

<div class="page">
  <div class="page-header">
    <h1>Providers &amp; Models</h1>
    <div class="header-actions">
      <button class="btn-primary" on:click={() => openAdd('openai')}>+ Add provider</button>
      <button class="btn-secondary" on:click={load} disabled={loading}>↺ Refresh</button>
    </div>
  </div>

  {#if error}<div class="banner err">{error}</div>{/if}
  {#if notice}<div class="banner ok">{notice}</div>{/if}
  {#if restartNeeded}
    <div class="banner warn restart-banner">
      <span>Provider settings were saved. Restart the gateway to reload provider registrations.</span>
      <button class="btn-secondary" on:click={restartGateway} disabled={restarting}>
        {restarting ? 'Restarting…' : 'Restart Gateway'}
      </button>
    </div>
  {/if}

  {#if missingKnown.length > 0}
    <div class="suggest-card">
      <span class="suggest-label">Quick add:</span>
      {#each missingKnown as id}
        <button class="suggest-chip" on:click={() => openAdd(id)}>
          {providerIcon(id)} {id}
        </button>
      {/each}
    </div>
  {/if}

  {#if defaultProvider}
    <div class="default-banner">Default provider: <strong>{defaultProvider}</strong></div>
  {/if}

  {#if loading}
    <div class="empty">Loading providers…</div>
  {:else if providerList.length === 0}
    <div class="empty-card">
      <div class="empty-icon">⚙️</div>
      <p>No providers configured.</p>
      <p class="hint">Add providers under <code>llm.providers</code> in your config.</p>
    </div>
  {:else}
    <div class="provider-grid">
      {#each providerList as [id, pc]}
        <div class="pv-card" class:default={id === defaultProvider}>
          <div class="pv-header">
            <span class="pv-icon">{providerIcon(id)}</span>
            <div class="pv-identity">
              <span class="pv-name">{id}</span>
              {#if id === defaultProvider}<span class="default-badge">default</span>{/if}
              {#if !pc.registered && pc.api_key}
                <span class="warn-badge" title="Configured in config.yaml but not yet registered. Restart the gateway.">restart needed</span>
              {/if}
            </div>
            <button class="icon-btn-edit" title="Edit credentials" on:click={() => openAdd(id)}>✎</button>
          </div>

          <div class="pv-body">
            {#if pc.base_url && pc.base_url !== '***'}
              <div class="pv-row"><span class="pv-label">Base URL</span><span class="pv-val mono">{pc.base_url}</span></div>
            {/if}
            <div class="pv-row"><span class="pv-label">Default model</span><span class="pv-val mono">{pc.model || '—'}</span></div>
            <div class="pv-row"><span class="pv-label">API key</span><span class="pv-val">{pc.api_key ? '● Set' : '○ Not set'}</span></div>

            {#if testResults[id]}
              <div class="pv-row test-result" class:ok={testResults[id].ok} class:fail={!testResults[id].ok && !testResults[id].loading}>
                {testResults[id].loading ? '⏳ Testing…' : testResults[id].ok ? '✓ ' + testResults[id].msg : '✗ ' + testResults[id].msg}
              </div>
            {/if}

            {#if models[id]}
              {#if models[id].length === 0}
                <div class="model-empty">No models found. For Ollama, pull one with <code>ollama pull &lt;name&gt;</code>.</div>
              {:else}
                <div class="model-list">
                  <span class="pv-label">Select default model</span>
                  <div class="model-options">
                    {#each models[id] as m}
                      <label class="model-opt" class:sel={chosen[id] === m}>
                        <input type="radio" name={'model-' + id} value={m} bind:group={chosen[id]} />
                        <code>{m}</code>
                        {#if pc.model === m}<span class="cur">current</span>{/if}
                      </label>
                    {/each}
                  </div>
                  <button class="btn-primary small-btn save-model"
                          on:click={() => saveModel(id)}
                          disabled={savingModel[id] || !chosen[id] || chosen[id] === pc.model}>
                    {savingModel[id] ? 'Saving…' : 'Save model'}
                  </button>
                </div>
              {/if}
            {/if}
          </div>

          <div class="pv-footer">
            <button class="btn-secondary small-btn" on:click={() => test(id)}>Test connection</button>
            <button class="btn-secondary small-btn" on:click={() => listModels(id)} disabled={modelsLoading[id]}>
              {modelsLoading[id] ? 'Loading…' : 'List models'}
            </button>
          </div>
        </div>
      {/each}
    </div>
  {/if}

  <div class="info-card">
    <h3>Adding a provider</h3>
    <p>Click <strong>+ Add provider</strong> above to wire up an API key for OpenAI, Anthropic, or Google. Or edit <code>config.yaml</code> directly under <code>llm.providers</code>:</p>
    <pre class="code-block">llm:
  default_provider: ollama
  providers:
    ollama:
      base_url: http://localhost:11434
      model: llama3
    openai:
      api_key: sk-...
      model: gpt-4o-mini
    anthropic:
      api_key: sk-ant-...
      model: claude-3-5-sonnet-latest
    google:
      api_key: AIza...
      model: gemini-2.5-flash</pre>
    <p><strong>List models</strong> queries the live provider. Pick one and <strong>Save model</strong> to persist it to config.yaml. New providers and credential changes require a gateway restart.</p>
  </div>
</div>

{#if showAdd}
  <div
    class="modal-bg"
    role="button"
    tabindex="0"
    aria-label="Close provider modal"
    on:click|self={() => showAdd = false}
    on:keydown={(e) => e.key === 'Escape' && (showAdd = false)}
  >
    <div class="modal">
      <h2>{providers[addId] ? 'Edit' : 'Add'} provider</h2>
      <div class="modal-field">
        <span class="field-label">Provider</span>
        <select bind:value={addId} on:change={() => { addBase = providers[addId]?.base_url || KNOWN_BASES[addId] || ''; addModel = providers[addId]?.model || KNOWN_MODELS[addId] || '' }}>
          {#each (known.length ? known : ['openai','anthropic','google','ollama']) as id}
            <option value={id}>{providerIcon(id)} {id}</option>
          {/each}
        </select>
      </div>
      <div class="modal-field">
        <span class="field-label">Base URL <span class="opt">(optional)</span></span>
        <input type="text" bind:value={addBase} placeholder={KNOWN_BASES[addId] || ''} />
      </div>
      <div class="modal-field">
        <span class="field-label">API key</span>
        <input type="password" bind:value={addKey} placeholder={providers[addId]?.api_key ? 'Leave blank to keep current key' : 'sk-...'} />
      </div>
      <div class="modal-field">
        <span class="field-label">Default model</span>
        <input type="text" bind:value={addModel} placeholder={KNOWN_MODELS[addId] || ''} />
      </div>
      <div class="modal-row">
        <button class="btn-secondary" on:click={() => showAdd = false}>Cancel</button>
        <button class="btn-primary" on:click={saveCreds} disabled={saving}>{saving ? 'Saving…' : 'Save'}</button>
      </div>
      <p class="modal-hint">Settings persist to <code>config.yaml</code>. Restart the gateway for new providers to be registered.</p>
    </div>
  </div>
{/if}

<style>
  .page        { padding: 1.5rem; display: flex; flex-direction: column; gap: 1.5rem; }
  .page-header { display: flex; align-items: center; justify-content: space-between; }
  .page-header h1 { font-size: 1.2rem; font-weight: 600; }
  .header-actions { display: flex; gap: .5rem; }

  .suggest-card {
    display: flex; flex-wrap: wrap; align-items: center; gap: .5rem;
    background: rgba(108,99,255,.06); border: 1px solid rgba(108,99,255,.18);
    padding: .6rem .85rem; border-radius: 8px; font-size: .82rem;
  }
  .suggest-label { color: #8b85ff; font-weight: 600; margin-right: .25rem; }
  .suggest-chip  {
    background: #1c1f35; color: #c8cadf; border: 1px solid #2a2f4a;
    padding: .25rem .7rem; border-radius: 999px; font-size: .78rem;
    cursor: pointer; transition: background .12s, border-color .12s;
  }
  .suggest-chip:hover { background: #252840; border-color: #6c63ff; color: #e8eaf6; }

  .warn-badge {
    font-size: .65rem; padding: .12rem .45rem; border-radius: 999px;
    background: rgba(240,180,80,.18); color: #f0c060; font-weight: 600;
  }
  .icon-btn-edit {
    margin-left: auto; background: none; color: #6b7294;
    padding: .2rem .4rem; border-radius: 4px; font-size: .9rem;
  }
  .icon-btn-edit:hover { color: #e8eaf6; background: #1a1e36; }

  .modal-bg {
    position: fixed; inset: 0; background: rgba(0,0,0,.65);
    display: flex; align-items: center; justify-content: center; z-index: 100;
  }
  .modal {
    background: #141626; border: 1px solid #2a2f4a; border-radius: 12px;
    padding: 1.5rem; width: 460px; display: flex; flex-direction: column; gap: .9rem;
  }
  .modal h2 { font-size: 1rem; font-weight: 600; }
  .modal-field { display: flex; flex-direction: column; gap: .35rem; }
  .field-label { font-size: .78rem; color: #7b82a8; }
  .modal-field .opt { color: #555a7a; font-weight: normal; }
  .modal-row { display: flex; gap: .75rem; justify-content: flex-end; margin-top: .25rem; }
  .modal-hint { font-size: .72rem; color: #555a7a; }
  .modal-hint code { background: #1c1f35; padding: .05rem .3rem; border-radius: 4px; color: #8b85ff; }
  .banner { padding: .7rem 1rem; border-radius: 8px; font-size: .85rem; }
  .err    { background: rgba(240,96,96,.1); border: 1px solid rgba(240,96,96,.3); color: #f06060; }
  .ok     { background: rgba(76,175,130,.1); border: 1px solid rgba(76,175,130,.3); color: #4caf82; }
  .warn   { background: rgba(240,196,96,.08); border: 1px solid rgba(240,196,96,.3); color: #f0c460; }
  .restart-banner { display: flex; align-items: center; justify-content: space-between; gap: .75rem; flex-wrap: wrap; }
  .empty  { color: #6b7294; padding: 3rem; text-align: center; }

  .default-banner {
    background: rgba(108,99,255,.08); border: 1px solid rgba(108,99,255,.2);
    border-radius: 8px; padding: .6rem 1rem; font-size: .85rem; color: #8b85ff;
  }
  .empty-card {
    background: #141626; border: 1px solid #1a1e36; border-radius: 10px;
    padding: 3rem 2rem; text-align: center; display: flex; flex-direction: column;
    align-items: center; gap: .75rem; color: #6b7294;
  }
  .empty-icon { font-size: 2.5rem; }
  .hint { font-size: .82rem; }
  .hint code { background: #1c1f35; padding: .1rem .35rem; border-radius: 4px; font-size: .78rem; color: #8b85ff; }

  .provider-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(300px, 1fr)); gap: 1rem; }

  .pv-card {
    background: #141626; border: 1px solid #1a1e36; border-radius: 10px;
    display: flex; flex-direction: column; overflow: hidden; transition: border-color .2s;
  }
  .pv-card.default { border-color: rgba(108,99,255,.4); }

  .pv-header { display: flex; align-items: center; gap: .75rem; padding: .9rem 1rem; border-bottom: 1px solid #1a1e36; }
  .pv-icon     { font-size: 1.5rem; }
  .pv-identity { display: flex; align-items: center; gap: .5rem; }
  .pv-name     { font-weight: 600; font-size: .95rem; }
  .default-badge { font-size: .68rem; padding: .12rem .45rem; border-radius: 999px; background: rgba(108,99,255,.2); color: #8b85ff; font-weight: 600; }

  .pv-body  { flex: 1; padding: .75rem 1rem; display: flex; flex-direction: column; gap: .45rem; }
  .pv-row   { display: flex; justify-content: space-between; align-items: flex-start; font-size: .82rem; gap: .5rem; }
  .pv-label { color: #555a7a; flex-shrink: 0; }
  .pv-val   { color: #c8cadf; text-align: right; word-break: break-all; }
  .pv-val.mono { font-family: monospace; font-size: .78rem; color: #8b85ff; }

  .test-result { font-size: .78rem; padding: .35rem .5rem; border-radius: 6px; margin-top: .25rem; background: #1a1e36; }
  .test-result.ok   { color: #4caf82; }
  .test-result.fail { color: #f06060; }

  .model-empty { font-size: .76rem; color: #6b7294; margin-top: .35rem; }
  .model-empty code { background: #1a1e36; padding: .05rem .3rem; border-radius: 4px; color: #8b85ff; }

  .model-list { margin-top: .5rem; padding-top: .5rem; border-top: 1px solid #1a1e36; display: flex; flex-direction: column; gap: .4rem; }
  .model-options { display: flex; flex-direction: column; gap: .25rem; max-height: 220px; overflow-y: auto; }
  .model-opt {
    display: flex; align-items: center; gap: .5rem; padding: .3rem .45rem;
    border-radius: 6px; cursor: pointer; border: 1px solid transparent;
  }
  .model-opt:hover { background: #1a1e36; }
  .model-opt.sel  { background: rgba(108,99,255,.12); border-color: rgba(108,99,255,.35); }
  .model-opt input { width: auto; }
  .model-opt code { font-size: .77rem; color: #b0b5d8; }
  .cur { font-size: .65rem; color: #4caf82; margin-left: auto; }
  .save-model { align-self: flex-start; margin-top: .25rem; }

  .pv-footer { padding: .75rem 1rem; border-top: 1px solid #1a1e36; display: flex; gap: .5rem; justify-content: flex-end; }
  .small-btn { padding: .3rem .75rem; font-size: .78rem; border-radius: 6px; }

  .info-card {
    background: #141626; border: 1px solid #1a1e36; border-radius: 10px;
    padding: 1.1rem 1.25rem; display: flex; flex-direction: column; gap: .5rem;
  }
  .info-card h3 { font-size: .875rem; font-weight: 600; }
  .info-card p  { font-size: .82rem; color: #7b82a8; line-height: 1.6; }
  .info-card code { background: #1c1f35; padding: .1rem .35rem; border-radius: 4px; font-size: .78rem; color: #8b85ff; }
  .code-block {
    background: #0e1020; border: 1px solid #1a1e36; border-radius: 6px;
    padding: .85rem 1rem; font-family: monospace; font-size: .78rem;
    color: #b0b5d8; line-height: 1.65; white-space: pre;
  }
</style>
