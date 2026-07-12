import App from './App.svelte'
import { installPrompt, appInstalled } from './lib/stores.js'

const STALE_ASSET_RELOAD_KEY = 'soulacy:stale-asset-reload'

function looksLikeStaleAssetError(value) {
  const msg = String(value?.message || value?.reason?.message || value?.reason || value || '')
  return (
    msg.includes('Failed to fetch dynamically imported module') ||
    msg.includes('Importing a module script failed') ||
    msg.includes('error loading dynamically imported module') ||
    (msg.includes('/assets/') && msg.includes('.js'))
  )
}

async function recoverFromStaleAssets() {
  if (sessionStorage.getItem(STALE_ASSET_RELOAD_KEY) === '1') return
  sessionStorage.setItem(STALE_ASSET_RELOAD_KEY, '1')
  try {
    if ('serviceWorker' in navigator) {
      const regs = await navigator.serviceWorker.getRegistrations()
      await Promise.all(regs.map((reg) => reg.update().catch(() => undefined)))
    }
    if ('caches' in window) {
      const keys = await caches.keys()
      await Promise.all(keys.filter((key) => key.startsWith('soulacy-shell-')).map((key) => caches.delete(key)))
    }
  } catch (_) {}
  window.location.reload()
}

window.addEventListener('error', (event) => {
  if (looksLikeStaleAssetError(event.error || event.message)) recoverFromStaleAssets()
})
window.addEventListener('unhandledrejection', (event) => {
  if (looksLikeStaleAssetError(event.reason)) recoverFromStaleAssets()
})

const app = new App({ target: document.getElementById('app') })

if ('serviceWorker' in navigator && import.meta.env.PROD) {
  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js').catch(() => {})
  })
}

// PWA install prompt: the browser fires beforeinstallprompt when the app is
// installable. Stash the event so the UI can offer an explicit "Install app"
// button (Chrome/Edge/Android) rather than relying on the browser's own hint.
window.addEventListener('beforeinstallprompt', (e) => {
  e.preventDefault()
  installPrompt.set(e)
})
window.addEventListener('appinstalled', () => {
  installPrompt.set(null)
  appInstalled.set(true)
})

export default app
