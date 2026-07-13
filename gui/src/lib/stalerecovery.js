const STALE_ASSET_RELOAD_KEY = 'soulacy:stale-asset-reload'

export function looksLikeStaleAssetError(value) {
  const msg = String(value?.message || value?.reason?.message || value?.reason || value || '')
  return (
    msg.includes('Failed to fetch dynamically imported module') ||
    msg.includes('Importing a module script failed') ||
    msg.includes('error loading dynamically imported module') ||
    (msg.includes('/assets/') && msg.includes('.js'))
  )
}

export async function recoverFromStaleAssets({ force = false } = {}) {
  if (!force && sessionStorage.getItem(STALE_ASSET_RELOAD_KEY) === '1') return false
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
  return true
}
