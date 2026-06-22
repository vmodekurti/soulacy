import { get } from 'svelte/store'
import { apiKey, authRequired } from './stores.js'

function authHeaders() {
  const key = get(apiKey)
  const h = { 'Content-Type': 'application/json' }
  if (key) h['Authorization'] = `Bearer ${key}`
  return h
}

export async function apiFetch(path, opts = {}) {
  const res = await fetch('/api/v1' + path, {
    ...opts,
    headers: { ...authHeaders(), ...(opts.headers || {}) },
  })
  if (!res.ok) {
    const body = await res.json().catch(() => ({}))
    if (res.status === 401 || res.status === 403) authRequired.set(true)
    // Preserve the full error body alongside the status so callers can read
    // structured fields (e.g. Studio's 409 consent fallback carries
    // requiresConsent + consentItems beyond the human `error` string).
    throw Object.assign(new Error(body.error || res.statusText), { status: res.status, body })
  }
  // /health bypasses auth on the server, so a success there says nothing
  // about credentials — only clear the auth-required flag on authenticated paths.
  if (path !== '/health') authRequired.set(false)
  // Some endpoints (e.g. DELETE) return 204 No Content with an empty body —
  // calling res.json() on that throws "Unexpected end of JSON input".
  if (res.status === 204) return null
  const text = await res.text()
  return text ? JSON.parse(text) : null
}

// streamSSE POSTs a body and parses a text/event-stream response, invoking
// onEvent({event, data}) for each frame. Resolves when the stream closes.
// Used by the Studio Architect's "Build until it works" so progress shows live.
export async function streamSSE(path, body, onEvent) {
  const res = await fetch('/api/v1' + path, {
    method: 'POST',
    headers: authHeaders(),
    body: JSON.stringify(body),
  })
  if (!res.ok || !res.body) {
    const b = await res.json().catch(() => ({}))
    if (res.status === 401 || res.status === 403) authRequired.set(true)
    throw Object.assign(new Error(b.error || res.statusText), { status: res.status, body: b })
  }
  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buf = ''
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    buf += decoder.decode(value, { stream: true })
    // Frames are separated by a blank line.
    let idx
    while ((idx = buf.indexOf('\n\n')) >= 0) {
      const frame = buf.slice(0, idx)
      buf = buf.slice(idx + 2)
      let event = 'message'
      let data = ''
      for (const line of frame.split('\n')) {
        if (line.startsWith('event:')) event = line.slice(6).trim()
        else if (line.startsWith('data:')) data += line.slice(5).trim()
      }
      if (data) onEvent({ event, data })
    }
  }
}

export const api = {
  health: () => apiFetch('/health'),

  agents: {
    list:    ()        => apiFetch('/agents'),
    get:     (id)      => apiFetch(`/agents/${id}`),
    create:  (def)     => apiFetch('/agents',     { method: 'POST', body: JSON.stringify(def) }),
    update:  (id, def) => apiFetch(`/agents/${id}`, { method: 'PUT',  body: JSON.stringify(def) }),
    validate:(def)     => apiFetch('/agents/validate', { method: 'POST', body: JSON.stringify(def) }),
    // Raw SOUL.yaml view/edit: getYaml returns { id, path, yaml }; updateYaml
    // sends the edited YAML text back (server parses + validates before writing).
    getYaml:   (id)        => apiFetch(`/agents/${id}/yaml`),
    updateYaml:(id, yaml)  => apiFetch(`/agents/${id}/yaml`, { method: 'PUT', body: yaml }),
    delete:  (id)      => apiFetch(`/agents/${id}`, { method: 'DELETE' }),
    enable:  (id)      => apiFetch(`/agents/${id}/enable`,  { method: 'POST' }),
    disable: (id)      => apiFetch(`/agents/${id}/disable`, { method: 'POST' }),
    trigger: (id)      => apiFetch(`/agents/${id}/trigger`, { method: 'POST' }),
    clone:   (id)      => apiFetch(`/agents/${id}/clone`,   { method: 'POST' }),
    actions: (id, limit = 500, types = '') => apiFetch(`/agents/${id}/actions?limit=${limit}${types ? '&types=' + encodeURIComponent(types) : ''}`),
  },

  templates: {
    list:        ()         => apiFetch('/templates'),
    instantiate: (name, id = '') => apiFetch(
      `/templates/${encodeURIComponent(name)}/instantiate`,
      { method: 'POST', body: JSON.stringify({ id }) },
    ),
  },

  chat: (agentId, text, userId = 'gui-user', overrides = null, sessionId = '') =>
    apiFetch('/chat', {
      method: 'POST',
      body: JSON.stringify({ agent_id: agentId, user_id: userId, session_id: sessionId, text, ...(overrides ? { overrides } : {}) }),
    }),

  /** Cancel an in-flight run (Story #22). run_id is the session id for /chat. */
  cancelRun: (runId) =>
    apiFetch('/chat/cancel', { method: 'POST', body: JSON.stringify({ run_id: runId }) }),

  admin: {
    restart: () => apiFetch('/admin/restart', { method: 'POST' }),
  },

  memory: {
    list: (agentId) => apiFetch(`/memory/${agentId}`),
  },

  brainMemory: {
    stats: () => apiFetch('/brain-memory'),
    episodic: (agentId, limit = 100) =>
      apiFetch(`/brain-memory/${encodeURIComponent(agentId)}/episodic?limit=${limit}`),
    writeEpisodic: (agentId, content, tags = []) =>
      apiFetch(`/brain-memory/${encodeURIComponent(agentId)}/episodic`, {
        method: 'POST', body: JSON.stringify({ content, tags }),
      }),
    clearEpisodic: (agentId) =>
      apiFetch(`/brain-memory/${encodeURIComponent(agentId)}/episodic`, { method: 'DELETE' }),
    procedural: (agentId) =>
      apiFetch(`/brain-memory/${encodeURIComponent(agentId)}/procedural`),
    rulebook: (agentId) =>
      apiFetch(`/brain-memory/${encodeURIComponent(agentId)}/rulebook`),
    rulebookVersion: (agentId, version) =>
      apiFetch(`/brain-memory/${encodeURIComponent(agentId)}/rulebook/${version}`),
    rulebookRollback: (agentId, version) =>
      apiFetch(`/brain-memory/${encodeURIComponent(agentId)}/rulebook/rollback`, {
        method: 'POST', body: JSON.stringify({ version }),
      }),
    rulebookLock: (agentId, locked) =>
      apiFetch(`/brain-memory/${encodeURIComponent(agentId)}/rulebook/lock`, {
        method: 'POST', body: JSON.stringify({ locked }),
      }),
    updateProcedural: (agentId, rules) =>
      apiFetch(`/brain-memory/${encodeURIComponent(agentId)}/procedural`, {
        method: 'PUT', body: JSON.stringify({ rules }),
      }),
    clearProcedural: (agentId) =>
      apiFetch(`/brain-memory/${encodeURIComponent(agentId)}/procedural`, { method: 'DELETE' }),
    contextPreview: (agentId, taskInput, maxEpisodic = 5, maxSemantic = 8) =>
      apiFetch(`/brain-memory/${encodeURIComponent(agentId)}/context-preview`, {
        method: 'POST',
        body: JSON.stringify({ task_input: taskInput, max_episodic: maxEpisodic, max_semantic: maxSemantic }),
      }),
  },

  channels: {
    list:    ()          => apiFetch('/channels'),
    update:  (id, patch) => apiFetch(`/channels/${id}`, { method: 'PATCH', body: JSON.stringify(patch) }),
    enable:  (id)        => apiFetch(`/channels/${id}/enable`,  { method: 'POST' }),
    disable: (id)        => apiFetch(`/channels/${id}/disable`, { method: 'POST' }),
    pairWhatsAppWeb: (body) => apiFetch('/channels/whatsapp_web/pair', {
      method: 'POST', body: JSON.stringify(body),
    }),
  },

  schedule: {
    list:   () => apiFetch('/schedule'),
    status: () => apiFetch('/schedule/status'),
  },

  tools: {
    catalog: () => apiFetch('/tool-catalog'),
  },

  providers: {
    list:           ()          => apiFetch('/providers'),
    models:         (id)        => apiFetch(`/providers/${id}/models`),
    setModel:       (id, model) => apiFetch(`/providers/${id}/model`, { method: 'POST', body: JSON.stringify({ model }) }),
    setCredentials: (id, body)  => apiFetch(`/providers/${id}`,        { method: 'POST', body: JSON.stringify(body) }),
    delete:         (id)        => apiFetch(`/providers/${id}`,        { method: 'DELETE' }),
  },

  config: {
    get:   ()      => apiFetch('/config'),
    patch: (patch) => apiFetch('/config', { method: 'PATCH', body: JSON.stringify(patch) }),
  },

  // Encrypted secret slots stored in the workspace vault (~/.soulacy/soulspace).
  // The catalog never carries values — `set` is a bool flag only.
  secrets: {
    list:   ()             => apiFetch('/secrets'),
    set:    (name, value)  => apiFetch(`/secrets/${encodeURIComponent(name)}`, { method: 'PUT', body: JSON.stringify({ value }) }),
    delete: (name)         => apiFetch(`/secrets/${encodeURIComponent(name)}`, { method: 'DELETE' }),
  },

  logs: {
    get: (lines = 500, filter = '') => {
      const params = new URLSearchParams()
      if (lines)  params.set('lines', String(lines))
      if (filter) params.set('filter', filter)
      const qs = params.toString()
      return apiFetch('/logs' + (qs ? '?' + qs : ''))
    },
  },

  skills: {
    list:             ()     => apiFetch('/skills'),
    get:              (name) => apiFetch(`/skills/${name}`),
    provisionAgenticSkills: (body) => apiFetch('/skills/provision-agenticskills', {
      method: 'POST', body: JSON.stringify(body),
    }),
  },

  // Skill sources / package registries (Story E26)
  registries: {
    list:   ()      => apiFetch('/registries'),
    // Searches every configured (or default) skill source — natively including
    // skills.sh. Returns { packages:[{slug,version,checksum,source,description,
    // provider}], count }. Reused by Studio M4's `discover` bridge op.
    search: (q)     => apiFetch('/registries/search?q=' + encodeURIComponent(q)),
    probe: (url)   => apiFetch('/registries/probe', { method: 'POST', body: JSON.stringify({ url }) }),
    add:   (entry) => apiFetch('/registries', { method: 'POST', body: JSON.stringify(entry) }),
  },

  mcp: {
    list:   ()        => apiFetch('/mcp'),
    create: (body)    => apiFetch('/mcp',                                       { method: 'POST',   body: JSON.stringify(body) }),
    update: (id, body)=> apiFetch(`/mcp/${encodeURIComponent(id)}`,            { method: 'PATCH',  body: JSON.stringify(body) }),
    delete: (id)      => apiFetch(`/mcp/${encodeURIComponent(id)}`,            { method: 'DELETE' }),
    test:           (body)    => apiFetch('/mcp/test',             { method: 'POST', body: JSON.stringify(body) }),
    provisionGlama:    (body)         => apiFetch('/mcp/provision-glama',    { method: 'POST', body: JSON.stringify(body) }),
  },

  plugins: {
    ui:    ()   => apiFetch('/plugins/ui'),
    token: (id) => apiFetch(`/plugins/${encodeURIComponent(id)}/token`, { method: 'POST' }),
    // Install & management (Story E13)
    installed: ()                 => apiFetch('/plugins/installed'),
    stage:     (source, checksum) => apiFetch('/plugins/install', { method: 'POST', body: JSON.stringify({ source, checksum }) }),
    approve:   (staged, source, checksum) =>
      apiFetch(`/plugins/install/${encodeURIComponent(staged)}/approve`, { method: 'POST', body: JSON.stringify({ source, checksum }) }),
    discard:   (staged) => apiFetch(`/plugins/install/${encodeURIComponent(staged)}`, { method: 'DELETE' }),
    enable:    (id)     => apiFetch(`/plugins/${encodeURIComponent(id)}/enable`, { method: 'POST' }),
    disable:   (id)     => apiFetch(`/plugins/${encodeURIComponent(id)}/disable`, { method: 'POST' }),
    reapprove: (id)     => apiFetch(`/plugins/${encodeURIComponent(id)}/reapprove`, { method: 'POST' }),
    remove:    (id)     => apiFetch(`/plugins/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  },

  voice: {
    status:    () => apiFetch('/voice/status'),
    ephemeral: () => apiFetch('/voice/ephemeral', { method: 'POST' }),
  },

  // Studio visual builder (M1 Wave 2). Called by the host bridge in
  // PluginFrame.svelte on behalf of the sandboxed Studio plugin iframe.
  studio: {
    /**
     * Refine a rough intent into a clear specification BEFORE compiling it into
     * a workflow (mandatory pre-generation step). Returns the refined intent,
     * a plain-language summary, the assumptions made, and clarifying questions.
     * @returns {Promise<{original, refined_intent, summary,
     *                     assumptions:string[],
     *                     questions:{id,text,options?}[]}>}
     */
    refinePrompt: ({ intent, catalog, light } = {}) =>
      apiFetch('/studio/refine-prompt', {
        method: 'POST',
        body: JSON.stringify({ intent, catalog, light }),
      }),
    /** Compile an intent (+ optional clarifying answers) into a draft workflow. */
    compile: ({ intent, catalog, answers } = {}) =>
      apiFetch('/studio/compile', {
        method: 'POST',
        body: JSON.stringify({ intent, catalog, answers }),
      }),
    /** Serialize the current draft to SOUL.yaml for the Code view. */
    yaml: ({ workflow } = {}) =>
      apiFetch('/studio/yaml', { method: 'POST', body: JSON.stringify({ workflow }) }),
    /** Parse edited SOUL.yaml back into a draft (+ lossiness warnings). */
    fromYaml: ({ yaml } = {}) =>
      apiFetch('/studio/from-yaml', { method: 'POST', body: JSON.stringify({ yaml }) }),
    /** Save authored SOUL.yaml directly to disk (code view is authoritative). */
    saveYaml: ({ yaml } = {}) =>
      apiFetch('/studio/save-yaml', { method: 'POST', body: JSON.stringify({ yaml }) }),
    /** Full validation of edited SOUL.yaml: syntax + definition + graph + runtime. */
    validateYaml: ({ yaml } = {}) =>
      apiFetch('/studio/validate-yaml', { method: 'POST', body: JSON.stringify({ yaml }) }),
    /**
     * Generate a ReAct/Plan-Execute AGENT (no fixed flow) — for intents that
     * need a reasoning loop. Returns a draft with strategy + tools allowlist.
     */
    compileAgent: ({ intent, strategy, catalog, answers } = {}) =>
      apiFetch('/studio/compile-agent', {
        method: 'POST',
        body: JSON.stringify({ intent, strategy, catalog, answers }),
      }),
    /**
     * Consolidated pre-save validation against live state: missing tools/MCP
     * servers/channels/secrets, empty required tool args, invalid schedules.
     * @returns {Promise<{ok:boolean,
     *                     blockers:{severity,kind,nodeId?,message,fix?}[],
     *                     warnings:{severity,kind,nodeId?,message,fix?}[]}>}
     */
    preflight: ({ workflow } = {}) =>
      apiFetch('/studio/preflight', {
        method: 'POST',
        body: JSON.stringify({ workflow }),
      }),
    /**
     * Deterministic data-flow repair: fill empty required tool args + reconcile
     * dangling {{ .var }} references to the right upstream output.
     * @returns {Promise<{workflow, fixed:number}>}
     */
    autowire: ({ workflow } = {}) =>
      apiFetch('/studio/autowire', {
        method: 'POST',
        body: JSON.stringify({ workflow }),
      }),
    /** Fix a draft from a RUNTIME error message via the LLM. */
    troubleshoot: ({ workflow, error } = {}) =>
      apiFetch('/studio/troubleshoot', {
        method: 'POST',
        body: JSON.stringify({ workflow, error }),
      }),
    /**
     * Architect: autonomous build-verify-repair loop. Fills capability holes
     * with glue code, synthesizes self-tests, then repairs every blocker and
     * runtime error — actually running the agent — until it works.
     * @returns {Promise<{report:{workflow,ok,verified,attempts:{n,phase,problems,action,changed,ok}[],summary,residual?}, preflight, glue:string[]}>}
     */
    build: ({ workflow, intent, verify } = {}) =>
      apiFetch('/studio/build', {
        method: 'POST',
        body: JSON.stringify({
          workflow,
          ...(intent ? { intent } : {}),
          ...(verify === false ? { verify: false } : {}),
        }),
      }),
    /**
     * Streaming build: same loop, live progress via SSE. onEvent receives each
     * {kind, attempt?, phase?, message} progress frame; the returned promise
     * resolves with the final {report, preflight} payload.
     */
    buildStream: ({ workflow, intent, verify } = {}, onEvent) => {
      let final = null
      return streamSSE(
        '/studio/build/stream',
        { workflow, ...(intent ? { intent } : {}), ...(verify === false ? { verify: false } : {}) },
        ({ event, data }) => {
          let parsed
          try { parsed = JSON.parse(data) } catch (_) { return }
          if (event === 'done') { final = parsed; return }
          if (onEvent) onEvent(parsed)
        },
      ).then(() => final)
    },
    /**
     * List runs that FAILED at run time (including unattended scheduled runs),
     * from the dead-letter queue — the self-heal feed.
     * @returns {Promise<{runs:{id,agentId,agentName,error,attempts,failedAt,healable}[]}>}
     */
    failedRuns: () => apiFetch('/studio/failed-runs'),
    /**
     * Diagnose a failed run and self-heal its saved agent: repair against the
     * real error, then validate/re-run. Returns the healed draft + transcript.
     * @returns {Promise<{agentId,agentName,error,changed,workflow,report,preflight}>}
     */
    diagnoseRun: ({ id } = {}) =>
      apiFetch('/studio/diagnose-run', {
        method: 'POST',
        body: JSON.stringify({ id }),
      }),
    /**
     * Advice on whether the configured builder model is strong enough for agent
     * design (Stories #8/#9).
     * @returns {Promise<{provider,model,configured,strong,severity,message,recommendation?}>}
     */
    modelAdvice: () => apiFetch('/studio/model-advice'),
    /**
     * Exercise a workflow against a sample input as a test bench (M5).
     * Optional `mocks` ({<nodeId>:<output>}) override individual node outputs,
     * `assertions` ([{target,op,value}]) are evaluated against the trace/result,
     * and `mode` ("dry"|"live") selects the run mode (live is gated server-side).
     * @returns {Promise<{trace:{nodeId,kind,input,output,mocked?}[], result,
     *                     assertions:{target,op,value,pass,detail}[], passed,
     *                     mode, warnings?}>}
     */
    test: ({ workflow, input, mocks, assertions, mode } = {}) =>
      apiFetch('/studio/test', {
        method: 'POST',
        body: JSON.stringify({
          workflow,
          input,
          ...(mocks ? { mocks } : {}),
          ...(assertions ? { assertions } : {}),
          ...(mode ? { mode } : {}),
        }),
      }),
    /**
     * Classify the workflow's capability tier and decide whether saving it
     * would create a privileged channel exposure that needs consent.
     * @returns {Promise<{tier, reasons:string[], requiresConsent:boolean,
     *                     consentItems:{kind,name,reason}[]}>}
     */
    plan: ({ workflow } = {}) =>
      apiFetch('/studio/plan', {
        method: 'POST',
        body: JSON.stringify({ workflow }),
      }),
    /**
     * Validate a draft workflow (structure, ports, branch conditions).
     * Non-blocking: callers debounce this after compile/edits to surface
     * errors/warnings on the canvas.
     * @returns {Promise<{ok:boolean,
     *                     errors:{nodeId?,edgeIndex?,message}[],
     *                     warnings:{nodeId?,message}[]}>}
     */
    validate: ({ workflow } = {}) =>
      apiFetch('/studio/validate', {
        method: 'POST',
        body: JSON.stringify({ workflow }),
      }),
    /**
     * Persist a workflow as a (disabled) agent. Pass acceptPrivilegedExposure
     * when plan reported requiresConsent. On a 409 consent fallback the thrown
     * error carries .body.requiresConsent + .body.consentItems.
     */
    save: ({ workflow, acceptPrivilegedExposure, grants } = {}) =>
      apiFetch('/studio/save', {
        method: 'POST',
        body: JSON.stringify({
          workflow,
          acceptPrivilegedExposure: !!acceptPrivilegedExposure,
          ...(Array.isArray(grants) && grants.length ? { grants } : {}),
        }),
      }),

    // ── Studio M6: templates, draft library, per-node refine ────────────────
    /**
     * List starter templates a fresh draft can begin from.
     * @returns {Promise<{templates:{id,name,description,workflow}[]}>}
     */
    templates: () => apiFetch('/studio/templates'),
    /**
     * Persist the current draft into the server-side draft library.
     * @returns {Promise<{id}>}
     */
    draftSave: ({ name, workflow } = {}) =>
      apiFetch('/studio/drafts', {
        method: 'POST',
        body: JSON.stringify({ name, workflow }),
      }),
    /**
     * List saved drafts.
     * @returns {Promise<{drafts:{id,name,updated}[]}>}
     */
    draftsList: () => apiFetch('/studio/drafts'),
    /**
     * Load one saved draft by id.
     * @returns {Promise<{id,name,workflow}>}
     */
    draftLoad: (id) => apiFetch(`/studio/drafts/${encodeURIComponent(id)}`),
    /**
     * Delete one saved draft by id.
     * @returns {Promise<{ok:true}>}
     */
    draftDelete: (id) =>
      apiFetch(`/studio/drafts/${encodeURIComponent(id)}`, { method: 'DELETE' }),
    /**
     * Ask the backend to refine one node of a workflow from a natural-language
     * instruction, returning a NEW workflow.
     * @returns {Promise<{workflow}>}
     */
    refine: ({ workflow, nodeId, instruction } = {}) =>
      apiFetch('/studio/refine', {
        method: 'POST',
        body: JSON.stringify({ workflow, nodeId, instruction }),
      }),

    // "My Workflows": list workflow-bearing agents + load one back as a draft.
    agents: {
      list: () => apiFetch('/studio/agents'),
      get:  (id) => apiFetch(`/studio/agents/${encodeURIComponent(id)}`),
    },
    // Framework-written Python: deterministic scaffolds + in-framework codegen.
    scaffolds: () => apiFetch('/studio/scaffolds'),
    codegen: ({ nodeId, description, workflow } = {}) =>
      apiFetch('/studio/codegen', {
        method: 'POST',
        body: JSON.stringify({ nodeId, description, workflow }),
      }),
  },

  knowledge: {
    list:   ()        => apiFetch('/knowledge'),
    create: (body)    => apiFetch('/knowledge', { method: 'POST', body: JSON.stringify(body) }),
    delete: (kb)      => apiFetch(`/knowledge/${encodeURIComponent(kb)}`, { method: 'DELETE' }),
    listDocuments: (kb) => apiFetch(`/knowledge/${encodeURIComponent(kb)}/documents`),
    ingest: (kb, body) =>
      apiFetch(`/knowledge/${encodeURIComponent(kb)}/documents`, { method: 'POST', body: JSON.stringify(body) }),
    /**
     * Upload a file (PDF/DOCX/MD/TXT). Browser sets multipart Content-Type.
     */
    upload: async (kb, file, title = '') => {
      const fd = new FormData()
      fd.append('file', file)
      if (title) fd.append('title', title)
      const key = get(apiKey)
      const headers = {}
      if (key) headers['Authorization'] = `Bearer ${key}`
      const res = await fetch(`/api/v1/knowledge/${encodeURIComponent(kb)}/documents`, {
        method: 'POST', headers, body: fd,
      })
      if (!res.ok) {
        const body = await res.json().catch(() => ({}))
        if (res.status === 401 || res.status === 403) authRequired.set(true)
        throw Object.assign(new Error(body.error || res.statusText), { status: res.status })
      }
      authRequired.set(false)
      return res.json()
    },
    deleteDocument: (kb, doc) =>
      apiFetch(`/knowledge/${encodeURIComponent(kb)}/documents/${encodeURIComponent(doc)}`, { method: 'DELETE' }),
    search: (kb, query, topK = 5) =>
      apiFetch(`/knowledge/${encodeURIComponent(kb)}/search`, {
        method: 'POST', body: JSON.stringify({ query, top_k: topK }),
      }),
  },

  history: {
    get:  (sessionId, limit = 0) =>
      apiFetch(`/history/${encodeURIComponent(sessionId)}${limit ? '?limit=' + limit : ''}`),
    fork: (sessionId, body) =>
      apiFetch(`/history/${encodeURIComponent(sessionId)}/fork`, { method: 'POST', body: JSON.stringify(body) }),
  },

  runs: {
    metrics: (sessionId, agentId = '') =>
      apiFetch(`/runs/${encodeURIComponent(sessionId)}/metrics${agentId ? '?agent_id=' + encodeURIComponent(agentId) : ''}`),
  },

  workboard: {
    list: (filters = {}) => {
      const params = new URLSearchParams()
      if (filters.status)  params.set('status', filters.status)
      if (filters.agentId) params.set('agent_id', filters.agentId)
      const qs = params.toString()
      return apiFetch('/workboard/tasks' + (qs ? '?' + qs : ''))
    },
    get:    (id)        => apiFetch(`/workboard/tasks/${id}`),
    create: (body)      => apiFetch('/workboard/tasks',        { method: 'POST',  body: JSON.stringify(body) }),
    update: (id, patch) => apiFetch(`/workboard/tasks/${id}`,  { method: 'PATCH', body: JSON.stringify(patch) }),
    delete: (id)        => apiFetch(`/workboard/tasks/${id}`,  { method: 'DELETE' }),
    run:    (id)        => apiFetch(`/workboard/tasks/${id}/run`,  { method: 'POST' }),
    runs:   (id)        => apiFetch(`/workboard/tasks/${id}/runs`),
    artifacts: (id)     => apiFetch(`/workboard/tasks/${id}/artifacts`),
    comments:      (id)       => apiFetch(`/workboard/tasks/${id}/comments`),
    addComment:    (id, body) => apiFetch(`/workboard/tasks/${id}/comments`, { method: 'POST', body: JSON.stringify(body) }),
    deleteComment: (cid)      => apiFetch(`/workboard/comments/${cid}`, { method: 'DELETE' }),
  },

  builder: {
    /**
     * Send one conversational turn to the agent builder.
     * @param {string} message - the user's message
     * @param {string} sessionId - reuse across turns; omit for first turn
     * @param {string} [provider] - LLM provider override
     * @returns {Promise<{session_id, reply, understanding, ready}>}
     */
    chat: (message, sessionId = '', provider = '') =>
      apiFetch('/builder/chat', {
        method: 'POST',
        body: JSON.stringify({ session_id: sessionId, message, provider }),
      }),

    /**
     * Compile the current understanding into a SOUL.yaml + agent map.
     * @returns {Promise<{soul_yaml, agent}>}
     */
    generate: (sessionId, provider = '', model = '') =>
      apiFetch('/builder/generate', {
        method: 'POST',
        body: JSON.stringify({ session_id: sessionId, provider, model }),
      }),

    /**
     * Generate AND register the agent in one shot.
     * @returns {Promise<{agent_id, soul_yaml}>}
     */
    deploy: (sessionId, provider = '', model = '') =>
      apiFetch('/builder/deploy', {
        method: 'POST',
        body: JSON.stringify({ session_id: sessionId, provider, model }),
      }),

    /**
     * Discard a builder session from server memory.
     */
    deleteSession: (sessionId) =>
      apiFetch(`/builder/session/${sessionId}`, { method: 'DELETE' }),
  },
}

// Opens a WebSocket connection to the gateway event stream.
export function createEventSocket() {
  const key  = get(apiKey)
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  let url = `${proto}//${location.host}/ws/events`
  if (key) url += `?api_key=${encodeURIComponent(key)}`
  return new WebSocket(url)
}
