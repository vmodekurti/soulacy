import { writable } from 'svelte/store'

// Persisted API key — stored in localStorage so users only type it once.
function persistedWritable(key, initial) {
  const stored = localStorage.getItem(key)
  const store = writable(stored !== null ? stored : initial)
  store.subscribe(val => {
    if (val) localStorage.setItem(key, val)
    else localStorage.removeItem(key)
  })
  return store
}

export const apiKey   = persistedWritable('soulacy_api_key', '')
export const connected = writable(false)  // WebSocket event stream status

// True when the gateway rejected our credentials (401/403). Distinct from
// "offline": the gateway is reachable but authentication is required.
export const authRequired = writable(false)

// Agent to pre-select when navigating to the Activity page (set by "Watch" buttons).
export const activityAgent = writable('')

// Agent to pre-select when navigating to the Agents page (set by Studio after save).
export const editAgent = writable('')

// Studio working session — persisted across navigation so the intent, the
// generated/refined workflow, and the transparency panels survive switching to
// another screen and back (the Studio component is destroyed on unmount). Null
// until Studio first saves a snapshot.
export const studioSession = writable(null)

// Activity → Studio handoff: a concrete failed run to debug from the real
// action log. Studio consumes and clears this when opened.
export const studioDebugRun = writable(null)

// Chat page — persisted across navigation so in-flight requests survive unmount.
// chatThreads is keyed by a UI thread id. Each thread owns its agent, runtime
// session, visible messages, branch state, and per-session metrics baseline.
export const chatActiveThreadId = writable('')
export const chatThreads = writable({})

// Legacy single-chat stores kept for older code/tests that import them.
export const chatAgentId  = writable('')
export const chatMessages = writable([])
export const chatSending  = writable(false)
export const chatSessionId = writable(`gui-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`)
export const chatBranches = writable([])
export const chatBranchMessages = writable({})
export const chatMetricsBaseline = writable({})
