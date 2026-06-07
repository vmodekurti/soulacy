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
    throw Object.assign(new Error(body.error || res.statusText), { status: res.status })
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

export const api = {
  health: () => apiFetch('/health'),

  agents: {
    list:    ()        => apiFetch('/agents'),
    get:     (id)      => apiFetch(`/agents/${id}`),
    create:  (def)     => apiFetch('/agents',     { method: 'POST', body: JSON.stringify(def) }),
    update:  (id, def) => apiFetch(`/agents/${id}`, { method: 'PUT',  body: JSON.stringify(def) }),
    validate:(def)     => apiFetch('/agents/validate', { method: 'POST', body: JSON.stringify(def) }),
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
  },

  config: {
    get:   ()      => apiFetch('/config'),
    patch: (patch) => apiFetch('/config', { method: 'PATCH', body: JSON.stringify(patch) }),
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
