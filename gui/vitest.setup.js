// Minimal browser shims so stores.js (localStorage) and api.js (fetch,
// location) can be imported under Node.
const backing = new Map()
globalThis.localStorage = {
  getItem: (k) => (backing.has(k) ? backing.get(k) : null),
  setItem: (k, v) => backing.set(k, String(v)),
  removeItem: (k) => backing.delete(k),
  clear: () => backing.clear(),
}
globalThis.location = { protocol: 'http:', host: 'localhost:8080' }
