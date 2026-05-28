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

// Agent to pre-select when navigating to the Activity page (set by "Watch" buttons).
export const activityAgent = writable('')

// Chat page — persisted across navigation so in-flight requests survive unmount.
export const chatAgentId  = writable('')
export const chatMessages = writable([])
export const chatSending  = writable(false)
