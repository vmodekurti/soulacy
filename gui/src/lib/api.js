import { get } from 'svelte/store'
import { apiKey } from './stores.js'

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
    throw Object.assign(new Error(body.error || res.statusText), { status: res.status })
  }
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

  chat: (agentId, text, userId = 'gui-user', overrides = null) =>
    apiFetch('/chat', {
      method: 'POST',
      body: JSON.stringify({ agent_id: agentId, user_id: userId, text, ...(overrides ? { overrides } : {}) }),
    }),

  admin: {
    restart: () => apiFetch('/admin/restart', { method: 'POST' }),
  },

  memory: {
    list: (agentId) => apiFetch(`/memory/${agentId}`),
  },

  channels: {
    list:    ()          => apiFetch('/channels'),
    update:  (id, patch) => apiFetch(`/channels/${id}`, { method: 'PATCH', body: JSON.stringify(patch) }),
    enable:  (id)        => apiFetch(`/channels/${id}/enable`,  { method: 'POST' }),
    disable: (id)        => apiFetch(`/channels/${id}/disable`, { method: 'POST' }),
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
        throw Object.assign(new Error(body.error || res.statusText), { status: res.status })
      }
      return res.json()
    },
    deleteDocument: (kb, doc) =>
      apiFetch(`/knowledge/${encodeURIComponent(kb)}/documents/${encodeURIComponent(doc)}`, { method: 'DELETE' }),
    search: (kb, query, topK = 5) =>
      apiFetch(`/knowledge/${encodeURIComponent(kb)}/search`, {
        method: 'POST', body: JSON.stringify({ query, top_k: topK }),
      }),
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
